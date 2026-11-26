package ledger

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mishankov/llm-meter/internal/pricing"
	"github.com/mishankov/llm-meter/internal/proxy"
)

type Store struct {
	db        *sql.DB
	pricing   pricing.Catalog
	pricingMu sync.RWMutex
}

const schema = `
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
PRAGMA journal_mode = WAL;

CREATE TABLE IF NOT EXISTS clients (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    archived_at INTEGER
);
CREATE TABLE IF NOT EXISTS client_keys (
    id TEXT PRIMARY KEY,
    client_id TEXT NOT NULL REFERENCES clients(id),
    digest BLOB NOT NULL UNIQUE,
    prefix TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    last_used_at INTEGER,
    revoked_at INTEGER
);
CREATE INDEX IF NOT EXISTS client_keys_client ON client_keys(client_id, created_at);

CREATE TABLE IF NOT EXISTS requests (
    id INTEGER PRIMARY KEY,
    time INTEGER NOT NULL,
    source TEXT NOT NULL,
    client_id TEXT NOT NULL REFERENCES clients(id),
    model TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    method TEXT NOT NULL,
    status INTEGER NOT NULL,
    response_id TEXT NOT NULL,
    usage_available INTEGER NOT NULL,
    input_tokens INTEGER NOT NULL,
    cached_input_tokens INTEGER NOT NULL,
    cache_write_tokens INTEGER NOT NULL,
    cache_write_12h_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL,
    cost REAL,
    input_rate REAL,
    cached_input_rate REAL,
    cache_write_rate REAL,
    cache_write_12h_rate REAL,
    output_rate REAL
);
CREATE INDEX IF NOT EXISTS requests_time ON requests(time, id);
CREATE INDEX IF NOT EXISTS requests_client_time ON requests(client_id, time, id);
CREATE INDEX IF NOT EXISTS requests_post_response ON requests(source, response_id)
    WHERE method = 'POST';
CREATE UNIQUE INDEX IF NOT EXISTS requests_response ON requests(source, response_id)
    WHERE usage_available = 1 AND response_id <> '';

CREATE TABLE IF NOT EXISTS allowances (
    client_id TEXT NOT NULL REFERENCES clients(id),
    limit_id TEXT NOT NULL,
    window TEXT NOT NULL,
    window_minutes INTEGER NOT NULL,
    remaining_percent REAL NOT NULL,
    resets_at INTEGER,
    observed_at INTEGER NOT NULL,
    PRIMARY KEY (client_id, limit_id, window)
);

CREATE TABLE IF NOT EXISTS relay_events (
    client_id TEXT NOT NULL REFERENCES clients(id),
    event_id TEXT NOT NULL,
    PRIMARY KEY (client_id, event_id)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS relay_completions (
    source TEXT NOT NULL,
    response_id TEXT NOT NULL,
    payload BLOB NOT NULL,
    PRIMARY KEY (source, response_id)
) WITHOUT ROWID;
`

func Open(path string, catalog pricing.Catalog) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize SQLite: %w", err)
	}
	if err := prepareClients(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize clients: %w", err)
	}
	store := &Store{db: db, pricing: catalog}
	if err := preparePricing(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize pricing ledger: %w", err)
	}
	if err := store.loadPricing(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("load saved prices: %w", err)
	}
	return store, nil
}

func prepareClients(db *sql.DB) error {
	var hasArchivedAt bool
	err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pragma_table_info('clients') WHERE name = 'archived_at')`).Scan(&hasArchivedAt)
	if err != nil || hasArchivedAt {
		return err
	}
	_, err = db.Exec(`ALTER TABLE clients ADD COLUMN archived_at INTEGER`)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

type Client struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	CreatedAt   time.Time      `json:"createdAt"`
	Keys        []Key          `json:"keys"`
	LastRequest *ClientRequest `json:"lastRequest,omitempty"`
}

type ClientRequest struct {
	At             time.Time `json:"at"`
	Status         int       `json:"status"`
	UsageAvailable bool      `json:"usageAvailable"`
}

type Key struct {
	ID         string     `json:"id"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
	Token      string     `json:"token,omitempty"`
}

func (s *Store) Clients(ctx context.Context) ([]Client, error) {
	// Keep the latest-request lookup before the one-to-many key join.
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.name, c.created_at,
        k.id, k.prefix, k.created_at, k.last_used_at, k.revoked_at,
        r.time, r.status, r.usage_available
        FROM clients c
        LEFT JOIN requests r ON r.id = (
            SELECT id FROM requests WHERE client_id = c.id ORDER BY time DESC, id DESC LIMIT 1
        )
        CROSS JOIN client_keys k ON k.client_id = c.id
        WHERE c.archived_at IS NULL
        ORDER BY c.created_at DESC, c.id, k.created_at DESC, k.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	clients := []Client{}
	for rows.Next() {
		var client Client
		var key Key
		var clientTime, keyTime int64
		var lastUsed, revoked, requestTime, requestStatus sql.NullInt64
		var usageAvailable sql.NullBool
		if err := rows.Scan(&client.ID, &client.Name, &clientTime, &key.ID, &key.Prefix, &keyTime, &lastUsed, &revoked,
			&requestTime, &requestStatus, &usageAvailable); err != nil {
			return nil, err
		}
		key.CreatedAt = timestamp(keyTime)
		key.LastUsedAt = optionalTime(lastUsed)
		key.RevokedAt = optionalTime(revoked)
		if len(clients) == 0 || clients[len(clients)-1].ID != client.ID {
			client.CreatedAt = timestamp(clientTime)
			if requestTime.Valid {
				client.LastRequest = &ClientRequest{At: timestamp(requestTime.Int64), Status: int(requestStatus.Int64), UsageAvailable: usageAvailable.Bool}
			}
			clients = append(clients, client)
		}
		latest := &clients[len(clients)-1]
		latest.Keys = append(latest.Keys, key)
	}
	return clients, rows.Err()
}

func (s *Store) CreateClient(ctx context.Context, name string, now time.Time) (Client, Key, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Client{}, Key{}, err
	}
	defer tx.Rollback()
	client := Client{ID: rand.Text(), Name: name, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO clients (id, name, created_at) VALUES (?, ?, ?)`, client.ID, name, now.UnixMilli()); err != nil {
		return Client{}, Key{}, err
	}
	key, err := createKey(ctx, tx, client.ID, now)
	if err != nil {
		return Client{}, Key{}, err
	}
	if err := tx.Commit(); err != nil {
		return Client{}, Key{}, err
	}
	publicKey := key
	publicKey.Token = ""
	client.Keys = []Key{publicKey}
	return client, key, nil
}

func (s *Store) RenameClient(ctx context.Context, clientID, name string) error {
	return requireMatch(s.db.ExecContext(ctx, `UPDATE clients SET name = ?
        WHERE id = ? AND archived_at IS NULL`, name, clientID))
}

func (s *Store) ArchiveClient(ctx context.Context, clientID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireMatch(tx.ExecContext(ctx, `UPDATE clients SET archived_at = COALESCE(archived_at, ?)
        WHERE id = ?`, now.UnixMilli(), clientID)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE client_keys SET revoked_at = ?
        WHERE client_id = ? AND revoked_at IS NULL`, now.UnixMilli(), clientID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateKey(ctx context.Context, clientID string, now time.Time) (Key, error) {
	return createKey(ctx, s.db, clientID, now)
}

func createKey(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, clientID string, now time.Time) (Key, error) {
	var random [32]byte
	rand.Read(random[:])
	token := "llmm_" + base64.RawURLEncoding.EncodeToString(random[:])
	digest := sha256.Sum256([]byte(token))
	key := Key{ID: rand.Text(), Prefix: token[:13], CreatedAt: now, Token: token}
	result, err := db.ExecContext(ctx, `INSERT INTO client_keys (id, client_id, digest, prefix, created_at)
        SELECT ?, id, ?, ?, ? FROM clients WHERE id = ? AND archived_at IS NULL`, key.ID, digest[:], key.Prefix, now.UnixMilli(), clientID)
	if err := requireMatch(result, err); err != nil {
		return Key{}, err
	}
	return key, nil
}

func (s *Store) RevokeKey(ctx context.Context, clientID, keyID string, now time.Time) error {
	return requireMatch(s.db.ExecContext(ctx, `UPDATE client_keys SET revoked_at = COALESCE(revoked_at, ?)
        WHERE id = ? AND client_id = ?`, now.UnixMilli(), keyID, clientID))
}

func requireMatch(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return sql.ErrNoRows
	}
	return err
}

func (s *Store) AuthenticateKey(ctx context.Context, token string, now time.Time) (string, error) {
	if len(token) != 48 || token[:5] != "llmm_" {
		return "", sql.ErrNoRows
	}
	digest := sha256.Sum256([]byte(token))
	var id string
	err := s.db.QueryRowContext(ctx, `UPDATE client_keys SET last_used_at = MAX(COALESCE(last_used_at, 0), ?)
        WHERE digest = ? AND revoked_at IS NULL RETURNING client_id`, now.UnixMilli(), digest[:]).Scan(&id)
	return id, err
}

func (s *Store) Record(ctx context.Context, usage proxy.Usage) error {
	return s.record(ctx, usage, "")
}

// RecordRelay records a usage event once per authenticated client. The event ID
// and its ledger changes commit together so retries can safely reuse that ID.
func (s *Store) RecordRelay(ctx context.Context, clientID, eventID string, usage proxy.Usage) error {
	if eventID == "" {
		return errors.New("relay event ID is required")
	}
	if proxy.IsServiceRequest(usage) {
		usage.Status = 0
	}
	usage.ClientID = clientID
	if usage.ResponseID != "" {
		// Uploaded identifiers can only correlate responses reported for this
		// client and namespace, independently of IDs from the remote proxy.
		namespace := usage.Provider
		if proxy.IsAntigravityEndpoint(usage.Endpoint) {
			// Replays must match IDs recorded before the provider was named Google.
			namespace = "antigravity"
		}
		usage.ResponseID = "relay:" + clientID + ":" + namespace + ":" + usage.ResponseID
	}
	return s.record(ctx, usage, eventID)
}

func (s *Store) record(ctx context.Context, usage proxy.Usage, eventID string) error {
	// Quota readings sometimes arrive independently of a response completion.
	if usage.Status == 0 && len(usage.RateLimits) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	measurementSource := "proxy"
	if eventID != "" {
		result, err := tx.ExecContext(ctx, `INSERT INTO relay_events (client_id, event_id) VALUES (?, ?)
            ON CONFLICT (client_id, event_id) DO NOTHING`, usage.ClientID, eventID)
		if err != nil {
			return err
		}
		if rows, err := result.RowsAffected(); err != nil || rows == 0 {
			return err
		}
		measurementSource = "relay"
	}
	at := usage.At
	if at.IsZero() {
		at = time.Now()
	}
	if len(usage.RateLimits) > 0 {
		if err := recordAllowances(ctx, tx, usage.ClientID, at, usage.RateLimits); err != nil {
			return err
		}
	}
	if usage.Status != 0 {
		if err := s.recordRequest(ctx, tx, usage, at, measurementSource); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) recordRequest(ctx context.Context, tx *sql.Tx, usage proxy.Usage, at time.Time, measurementSource string) error {
	if measurementSource == "relay" && usage.Method == http.MethodPost && usage.ResponseID != "" {
		var err error
		usage, err = mergePendingWithPost(ctx, tx, usage)
		if err != nil {
			return err
		}
	}
	pricingAt := at
	if proxy.IsAntigravityEndpoint(usage.Endpoint) && usage.HasUsage && !usage.UsageUpdate && usage.ResponseID != "" && usage.Model == "" {
		var originalTime int64
		var originalTier string
		err := tx.QueryRowContext(ctx, `SELECT time, model, service_tier FROM requests
            WHERE source = ? AND response_id = ? AND usage_available = 1
                AND (endpoint = '/antigravity' OR endpoint GLOB '/antigravity/*')
                AND measurement_source = ?`,
			usage.Source, usage.ResponseID, measurementSource).Scan(&originalTime, &usage.Model, &originalTier)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			pricingAt = timestamp(originalTime)
			if usage.ServiceTier == "" {
				usage.ServiceTier = originalTier
			}
		}
	}
	if usage.UsageUpdate {
		if usage.ResponseID != "" && (usage.HasUsage || usage.ReportedCost != nil || usage.ReportedListCost != nil) {
			matched, err := s.completeRequest(ctx, tx, &usage, measurementSource)
			if err != nil {
				return err
			}
			if !matched && measurementSource == "relay" {
				if err := saveRelayCompletion(ctx, tx, usage); err != nil {
					return err
				}
			}
		}
		usage.HasUsage = false
		usage.InputTokens, usage.CachedInputTokens, usage.CacheWriteTokens, usage.CacheWrite12hTokens, usage.OutputTokens = 0, 0, 0, 0, 0
		usage.CompletionWindow = ""
		usage.ReportedCost, usage.ReportedListCost, usage.ReportedAllowance = nil, nil, nil
	}
	if !usage.HasCacheWriteSplit && usage.CacheRetention == "12h" {
		usage.CacheWrite12hTokens = usage.CacheWriteTokens
	}
	var inputRate, cachedRate, writeRate, write12hRate, outputRate *float64
	rates, cost, priceSource := s.priceFor(usage, pricingAt)
	if rates != nil {
		inputRate, cachedRate, writeRate, write12hRate, outputRate = &rates.Input, &rates.CachedInput, &rates.CacheWrite, &rates.CacheWrite12h, &rates.Output
	}
	// A replay of an Antigravity stream can report more usage for the same response.
	// Keep the original request and replace its usage and price together.
	_, err := tx.ExecContext(ctx, `INSERT INTO requests (time, source, client_id, model, reasoning_level, endpoint,
        method, status, response_id, usage_available, input_tokens, cached_input_tokens,
        cache_write_tokens, cache_write_12h_tokens, output_tokens, cost, input_rate, cached_input_rate,
		cache_write_rate, cache_write_12h_rate, output_rate, provider, price_source,
		reported_cost, reported_list_cost, reported_allowance, service_tier, completion_window, measurement_source)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT DO UPDATE SET
            model = COALESCE(NULLIF(excluded.model, ''), requests.model),
            reasoning_level = COALESCE(NULLIF(excluded.reasoning_level, ''), requests.reasoning_level),
            input_tokens = excluded.input_tokens, cached_input_tokens = excluded.cached_input_tokens,
            cache_write_tokens = excluded.cache_write_tokens, cache_write_12h_tokens = excluded.cache_write_12h_tokens,
            output_tokens = excluded.output_tokens, cost = excluded.cost, input_rate = excluded.input_rate,
            cached_input_rate = excluded.cached_input_rate, cache_write_rate = excluded.cache_write_rate,
            cache_write_12h_rate = excluded.cache_write_12h_rate, output_rate = excluded.output_rate,
            price_source = excluded.price_source, reported_cost = excluded.reported_cost,
            reported_list_cost = excluded.reported_list_cost, reported_allowance = excluded.reported_allowance
        WHERE (requests.endpoint = '/antigravity' OR requests.endpoint GLOB '/antigravity/*')
            AND (excluded.endpoint = '/antigravity' OR excluded.endpoint GLOB '/antigravity/*')
            AND requests.measurement_source = excluded.measurement_source
            AND requests.usage_available = 1 AND excluded.usage_available = 1
            AND excluded.input_tokens >= requests.input_tokens AND excluded.cached_input_tokens >= requests.cached_input_tokens
            AND excluded.cache_write_tokens >= requests.cache_write_tokens
            AND excluded.cache_write_12h_tokens >= requests.cache_write_12h_tokens
            AND excluded.output_tokens >= requests.output_tokens
            AND (excluded.input_tokens > requests.input_tokens OR excluded.cached_input_tokens > requests.cached_input_tokens
                OR excluded.cache_write_tokens > requests.cache_write_tokens
                OR excluded.cache_write_12h_tokens > requests.cache_write_12h_tokens
                OR excluded.output_tokens > requests.output_tokens)`,
		at.UnixMilli(), usage.Source, usage.ClientID, usage.Model, usage.ReasoningLevel, usage.Endpoint, usage.Method, usage.Status,
		usage.ResponseID, usage.HasUsage, usage.InputTokens, usage.CachedInputTokens, usage.CacheWriteTokens,
		usage.CacheWrite12hTokens, usage.OutputTokens, cost, inputRate, cachedRate, writeRate, write12hRate, outputRate,
		usage.Provider, priceSource, usage.ReportedCost, usage.ReportedListCost, usage.ReportedAllowance, usage.ServiceTier, usage.CompletionWindow, measurementSource)
	return err
}

func recordAllowances(ctx context.Context, tx *sql.Tx, clientID string, at time.Time, limits []proxy.RateLimit) error {
	for _, limit := range limits {
		if limit.UsedPercent < 0 || limit.UsedPercent > 100 {
			continue
		}
		var reset *int64
		if !limit.ResetsAt.IsZero() {
			value := limit.ResetsAt.UnixMilli()
			reset = &value
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO allowances
            (client_id, limit_id, window, window_minutes, remaining_percent, resets_at, observed_at)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            ON CONFLICT (client_id, limit_id, window) DO UPDATE SET
                window_minutes = excluded.window_minutes, remaining_percent = excluded.remaining_percent,
                resets_at = excluded.resets_at, observed_at = excluded.observed_at
            WHERE excluded.observed_at >= allowances.observed_at`, clientID, limit.ID, limit.Window,
			limit.WindowMinutes, 100-limit.UsedPercent, reset, at.UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}

func timestamp(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func optionalTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	result := timestamp(value.Int64)
	return &result
}

func NotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }
