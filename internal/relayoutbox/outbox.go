// Package relayoutbox persists usage events while the reporting server is unavailable.
package relayoutbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mishankov/llm-meter/internal/usagereport"
)

const (
	maxEvents    = 10_000
	maxEventSize = usagereport.MaxBodyBytes
	firstRetry   = time.Second
	maxRetry     = time.Minute
)

var ErrFull = errors.New("usage queue is full")

const schema = `
PRAGMA busy_timeout = 5000;
PRAGMA journal_mode = WAL;
PRAGMA synchronous = FULL;
CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    payload BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS destination (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    fingerprint BLOB NOT NULL
);
`

// Status captures a point-in-time snapshot of the durable usage outbox.
type Status struct {
	Pending       int       `json:"pending"`
	LastSuccessAt time.Time `json:"lastSuccessAt,omitzero"`
	LastError     string    `json:"lastError,omitempty"`
	LastHTTPCode  int       `json:"lastHttpCode,omitempty"`
	NextRetryAt   time.Time `json:"nextRetryAt,omitzero"`
	Dropped       int64     `json:"dropped"`
}

type Queue struct {
	db       *sql.DB
	endpoint string
	key      string
	client   *http.Client
	ready    chan struct{}
	retry    chan struct{}

	mu       sync.Mutex
	status   Status
	observer func(Status)
	warn     func(string)
	changed  chan struct{}
}

// PendingCount returns the number of queued usage events in directory without
// mutating or creating the database. A missing database reports 0.
func PendingCount(directory string) (int, error) {
	name := filepath.Join(directory, "usage-outbox.db")
	if _, err := os.Stat(name); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	} else if err != nil {
		return 0, fmt.Errorf("stat usage queue file: %w", err)
	}
	uri := (&url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(name),
		RawQuery: "mode=ro&_pragma=busy_timeout(5000)",
	}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return 0, fmt.Errorf("open usage queue read-only: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count queued usage events: %w", err)
	}
	return count, nil
}

// Open opens the persistent queue for a single relay connection.
func Open(directory string, endpoint *url.URL, key string) (*Queue, error) {
	if endpoint == nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" ||
		endpoint.User != nil || endpoint.Path != "/api/usage/events" ||
		endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
		return nil, errors.New("invalid HTTPS usage upload endpoint")
	}
	if key == "" {
		return nil, errors.New("usage upload key is required")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create usage queue directory: %w", err)
	}
	name := filepath.Join(directory, "usage-outbox.db")
	file, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open usage queue file: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close usage queue file: %w", err)
	}
	db, err := sql.Open("sqlite", name)
	if err != nil {
		return nil, fmt.Errorf("open usage queue: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize usage queue: %w", err)
	}
	// Prevent pending reports from being uploaded under another connection's key.
	fingerprint := sha256.Sum256([]byte(endpoint.String() + "\x00" + key))
	if _, err := db.Exec(`INSERT INTO destination (id, fingerprint) VALUES (1, ?) ON CONFLICT (id) DO NOTHING`, fingerprint[:]); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("bind usage queue to destination: %w", err)
	}
	var saved []byte
	if err := db.QueryRow(`SELECT fingerprint FROM destination WHERE id = 1`).Scan(&saved); err != nil || !bytes.Equal(saved, fingerprint[:]) {
		_ = db.Close()
		return nil, errors.New("use a separate usage queue directory for a different server or key")
	}
	var pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&pending); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("count queued usage events: %w", err)
	}
	return &Queue{
		db:       db,
		endpoint: endpoint.String(),
		key:      key,
		client: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		ready:   make(chan struct{}, 1),
		retry:   make(chan struct{}, 1),
		status:  Status{Pending: pending},
		changed: make(chan struct{}),
	}, nil
}

// Status returns a snapshot of the queue state.
func (q *Queue) Status() Status {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.status
}

// SetObserver registers a callback invoked whenever queue status changes.
func (q *Queue) SetObserver(fn func(Status)) {
	q.mu.Lock()
	q.observer = fn
	q.mu.Unlock()
}

// SetWarningLogger directs sanitized upload diagnostics to the caller. Set it before Run.
func (q *Queue) SetWarningLogger(fn func(string)) { q.warn = fn }

func (q *Queue) logWarning(message string) {
	if q.warn != nil {
		q.warn(message)
	} else {
		slog.Warn(message)
	}
}

// RetryNow wakes the uploader immediately if it is idle or backing off.
func (q *Queue) RetryNow() {
	select {
	case q.retry <- struct{}{}:
	default:
	}
}

// DrainLoss reports discards during a drain. It is returned alone only on completion.
type DrainLoss struct {
	Discarded int64
}

func (e *DrainLoss) Error() string {
	return fmt.Sprintf("usage reports discarded during drain: %d", e.Discarded)
}

// DrainResult combines the delivery outcome with any losses since the drain began.
// The queue owner takes the final count after its worker exits.
func (q *Queue) DrainResult(droppedBefore int64, err error) error {
	discarded := q.Status().Dropped - droppedBefore
	if discarded <= 0 {
		return err
	}
	loss := &DrainLoss{Discarded: discarded}
	if err != nil {
		return errors.Join(err, loss)
	}
	return loss
}

// WaitEmpty waits until all queued events are handled or ctx is cancelled.
func (q *Queue) WaitEmpty(ctx context.Context) error {
	q.RetryNow()
	for {
		q.mu.Lock()
		if q.status.Pending == 0 {
			q.mu.Unlock()
			return nil
		}
		ch := q.changed
		q.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (q *Queue) updateStatus(fn func(*Status)) {
	q.mu.Lock()
	fn(&q.status)
	q.publishLocked()
}

// publishLocked releases mu before calling external observers.
func (q *Queue) publishLocked() {
	snapshot := q.status
	observer := q.observer
	close(q.changed)
	q.changed = make(chan struct{})
	q.mu.Unlock()
	if observer != nil {
		observer(snapshot)
	}
}

// Enqueue persists one already-encoded event and wakes the uploader.
func (q *Queue) Enqueue(ctx context.Context, payload []byte) error {
	if len(payload) == 0 || len(payload) > maxEventSize {
		return errors.New("usage event has invalid size")
	}
	q.mu.Lock()
	result, err := q.db.ExecContext(ctx, `INSERT INTO events (payload)
		SELECT ? WHERE (SELECT COUNT(*) FROM events) < ?`, payload, maxEvents)
	if err != nil {
		q.mu.Unlock()
		return fmt.Errorf("persist usage event: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		q.mu.Unlock()
		return fmt.Errorf("check queued usage event: %w", err)
	}
	if count == 0 {
		q.status.Dropped++
		q.publishLocked()
		return ErrFull
	}
	q.status.Pending++
	q.publishLocked()
	select {
	case q.ready <- struct{}{}:
	default:
	}
	return nil
}

// Run delivers events in order. A caller should run one worker and wait for it
// to stop after cancellation before closing the queue.
func (q *Queue) Run(ctx context.Context) {
	delay := firstRetry
	for ctx.Err() == nil {
		err := q.deliverNext(ctx)
		switch {
		case err == nil:
			delay = firstRetry
		case errors.Is(err, sql.ErrNoRows):
			select {
			case <-ctx.Done():
			case <-q.ready:
			case <-q.retry:
			}
		case ctx.Err() == nil:
			var statusCode uploadStatus
			httpCode := 0
			if errors.As(err, &statusCode) {
				httpCode = int(statusCode)
			}
			nextRetry := time.Now().UTC().Add(delay)
			q.updateStatus(func(s *Status) {
				s.LastError = err.Error()
				s.LastHTTPCode = httpCode
				s.NextRetryAt = nextRetry
			})
			q.logWarning("Usage event remains queued: " + err.Error())
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-q.retry:
				timer.Stop()
				delay = firstRetry
				q.updateStatus(func(s *Status) {
					s.NextRetryAt = time.Time{}
				})
			case <-timer.C:
				delay = min(2*delay, maxRetry)
			}
		}
	}
}

func (q *Queue) deliverNext(ctx context.Context) error {
	var id int64
	var payload []byte
	err := q.db.QueryRowContext(ctx, `SELECT id, payload FROM events ORDER BY id LIMIT 1`).Scan(&id, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err != nil {
		return errors.New("could not read queued usage event")
	}
	uploadErr := q.upload(ctx, payload)
	var status uploadStatus
	if uploadErr != nil && (!errors.As(uploadErr, &status) || !status.permanent()) {
		return uploadErr
	}
	q.mu.Lock()
	if _, err := q.db.ExecContext(ctx, `DELETE FROM events WHERE id = ?`, id); err != nil {
		q.mu.Unlock()
		return errors.New("could not remove handled usage event")
	}
	q.status.Pending--
	q.status.NextRetryAt = time.Time{}
	if uploadErr != nil {
		q.status.Dropped++
		q.status.LastError = uploadErr.Error()
		q.status.LastHTTPCode = int(status)
		q.publishLocked()
		q.logWarning("Discarded usage event: " + uploadErr.Error())
		return nil
	}
	q.status.LastSuccessAt = time.Now().UTC()
	q.status.LastError = ""
	q.status.LastHTTPCode = 0
	q.publishLocked()
	return nil
}

func (q *Queue) upload(ctx context.Context, payload []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, q.endpoint, bytes.NewReader(payload))
	if err != nil {
		return errors.New("could not create usage upload request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-LLM-Meter-Key", q.key)
	response, err := q.client.Do(request)
	if err != nil {
		return errors.New("could not reach the usage server")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return uploadStatus(response.StatusCode)
	}
	return nil
}

type uploadStatus int

func (status uploadStatus) Error() string {
	return fmt.Sprintf("usage server returned HTTP %d", status)
}

func (status uploadStatus) permanent() bool {
	switch int(status) {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

func (q *Queue) Close() error { return q.db.Close() }
