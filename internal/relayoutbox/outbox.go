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

type Queue struct {
	db       *sql.DB
	endpoint string
	key      string
	client   *http.Client
	ready    chan struct{}
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
		ready: make(chan struct{}, 1),
	}, nil
}

// Enqueue persists one already-encoded event and wakes the uploader.
func (q *Queue) Enqueue(ctx context.Context, payload []byte) error {
	if len(payload) == 0 || len(payload) > maxEventSize {
		return errors.New("usage event has invalid size")
	}
	result, err := q.db.ExecContext(ctx, `INSERT INTO events (payload)
		SELECT ? WHERE (SELECT COUNT(*) FROM events) < ?`, payload, maxEvents)
	if err != nil {
		return fmt.Errorf("persist usage event: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check queued usage event: %w", err)
	}
	if count == 0 {
		return ErrFull
	}
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
			}
		case ctx.Err() == nil:
			slog.Warn("Usage event remains queued", "error", err)
			select {
			case <-ctx.Done():
			case <-time.After(delay):
			}
			delay = min(2*delay, maxRetry)
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
	if _, err := q.db.ExecContext(ctx, `DELETE FROM events WHERE id = ?`, id); err != nil {
		return errors.New("could not remove handled usage event")
	}
	if uploadErr != nil {
		slog.Warn("Discarded usage event rejected by the server", "status", int(status))
	}
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
