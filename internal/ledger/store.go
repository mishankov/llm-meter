package ledger

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mishankov/llm-proxy/internal/pricing"
	"github.com/mishankov/llm-proxy/internal/proxy"
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
    created_at INTEGER NOT NULL
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

func (s *Store) Close() error { return s.db.Close() }

type Client struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	Keys      []Key     `json:"keys"`
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
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.name, c.created_at,
        k.id, k.prefix, k.created_at, k.last_used_at, k.revoked_at
        FROM clients c JOIN client_keys k ON k.client_id = c.id
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
		var lastUsed, revoked sql.NullInt64
		if err := rows.Scan(&client.ID, &client.Name, &clientTime, &key.ID, &key.Prefix, &keyTime, &lastUsed, &revoked); err != nil {
			return nil, err
		}
		key.CreatedAt = timestamp(keyTime)
		key.LastUsedAt = optionalTime(lastUsed)
		key.RevokedAt = optionalTime(revoked)
		if len(clients) == 0 || clients[len(clients)-1].ID != client.ID {
			client.CreatedAt = timestamp(clientTime)
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

func (s *Store) CreateKey(ctx context.Context, clientID string, now time.Time) (Key, error) {
	return createKey(ctx, s.db, clientID, now)
}

func createKey(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, clientID string, now time.Time) (Key, error) {
	var random [32]byte
	rand.Read(random[:])
	token := "llmp_" + base64.RawURLEncoding.EncodeToString(random[:])
	digest := sha256.Sum256([]byte(token))
	key := Key{ID: rand.Text(), Prefix: token[:13], CreatedAt: now, Token: token}
	result, err := db.ExecContext(ctx, `INSERT INTO client_keys (id, client_id, digest, prefix, created_at)
        SELECT ?, id, ?, ?, ? FROM clients WHERE id = ?`, key.ID, digest[:], key.Prefix, now.UnixMilli(), clientID)
	if err != nil {
		return Key{}, err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		err = sql.ErrNoRows
	}
	return key, err
}

func (s *Store) RevokeKey(ctx context.Context, clientID, keyID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE client_keys SET revoked_at = COALESCE(revoked_at, ?)
        WHERE id = ? AND client_id = ?`, now.UnixMilli(), keyID, clientID)
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
	if len(token) != 48 || token[:5] != "llmp_" {
		return "", sql.ErrNoRows
	}
	digest := sha256.Sum256([]byte(token))
	var id string
	err := s.db.QueryRowContext(ctx, `UPDATE client_keys SET last_used_at = MAX(COALESCE(last_used_at, 0), ?)
        WHERE digest = ? AND revoked_at IS NULL RETURNING client_id`, now.UnixMilli(), digest[:]).Scan(&id)
	return id, err
}

func (s *Store) Record(ctx context.Context, usage proxy.Usage) error {
	// Quota readings sometimes arrive independently of a response completion.
	if usage.Status == 0 && len(usage.RateLimits) == 0 {
		return nil
	}
	at := usage.At
	if at.IsZero() {
		at = time.Now()
	}
	if len(usage.RateLimits) > 0 {
		if err := s.recordAllowances(ctx, usage.ClientID, at, usage.RateLimits); err != nil {
			return err
		}
	}
	if usage.Status == 0 {
		return nil
	}
	pricingAt := at
	var originatingID int64
	var originalCost sql.NullFloat64
	if usage.UsageUpdate && (usage.HasUsage || usage.ReportedCost != nil || usage.ReportedListCost != nil) && usage.ResponseID != "" {
		var originalTime int64
		var originalModel string
		err := s.db.QueryRowContext(ctx, `SELECT id, time, model, cost FROM requests
            WHERE source = ? AND response_id = ? AND method = 'POST' AND usage_available = 0
            ORDER BY id LIMIT 1`, usage.Source, usage.ResponseID).Scan(&originatingID, &originalTime, &originalModel, &originalCost)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if originatingID != 0 {
			pricingAt = timestamp(originalTime)
			if usage.Model == "" {
				usage.Model = originalModel
			}
		}
	}
	var inputRate, cachedRate, writeRate, write12hRate, outputRate, cost *float64
	var priceSource string
	if !usage.UsageUpdate || originatingID != 0 {
		if !usage.HasCacheWriteSplit && usage.CacheRetention == "12h" {
			usage.CacheWrite12hTokens = usage.CacheWriteTokens
		}
		rates, amount, origin := s.priceFor(usage, pricingAt)
		cost, priceSource = amount, origin
		if rates != nil && !originalCost.Valid {
			inputRate, cachedRate, writeRate, write12hRate, outputRate = &rates.Input, &rates.CachedInput, &rates.CacheWrite, &rates.CacheWrite12h, &rates.Output
		}
	}
	if usage.UsageUpdate {
		if originatingID != 0 {
			// The POST incurred the usage. Pollers may use another client key or
			// fetch the same completed response more than once.
			_, err := s.db.ExecContext(ctx, `UPDATE OR IGNORE requests SET usage_available = ?,
                model = COALESCE(NULLIF(?, ''), model), input_tokens = ?, cached_input_tokens = ?, cache_write_tokens = ?,
                cache_write_12h_tokens = ?, output_tokens = ?, cost = COALESCE(cost, ?), input_rate = ?, cached_input_rate = ?,
                cache_write_rate = ?, cache_write_12h_rate = ?, output_rate = ?,
                price_source = COALESCE(NULLIF(price_source, ''), ?), reported_cost = COALESCE(reported_cost, ?),
                reported_list_cost = COALESCE(reported_list_cost, ?), reported_allowance = COALESCE(reported_allowance, ?)
				WHERE id = ? AND usage_available = 0`, usage.HasUsage, usage.Model, usage.InputTokens, usage.CachedInputTokens,
				usage.CacheWriteTokens, usage.CacheWrite12hTokens, usage.OutputTokens, cost, inputRate, cachedRate, writeRate,
				write12hRate, outputRate, priceSource, usage.ReportedCost, usage.ReportedListCost, usage.ReportedAllowance, originatingID)
			if err != nil {
				return err
			}
		}
		usage.HasUsage = false
		usage.InputTokens, usage.CachedInputTokens, usage.CacheWriteTokens, usage.CacheWrite12hTokens, usage.OutputTokens = 0, 0, 0, 0, 0
		cost, inputRate, cachedRate, writeRate, write12hRate, outputRate = nil, nil, nil, nil, nil, nil
		priceSource = ""
		usage.ReportedCost, usage.ReportedListCost, usage.ReportedAllowance = nil, nil, nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO requests (time, source, client_id, model, endpoint,
        method, status, response_id, usage_available, input_tokens, cached_input_tokens,
        cache_write_tokens, cache_write_12h_tokens, output_tokens, cost, input_rate, cached_input_rate,
		cache_write_rate, cache_write_12h_rate, output_rate, provider, price_source,
		reported_cost, reported_list_cost, reported_allowance)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		at.UnixMilli(), usage.Source, usage.ClientID, usage.Model, usage.Endpoint, usage.Method, usage.Status,
		usage.ResponseID, usage.HasUsage, usage.InputTokens, usage.CachedInputTokens, usage.CacheWriteTokens,
		usage.CacheWrite12hTokens, usage.OutputTokens, cost, inputRate, cachedRate, writeRate, write12hRate, outputRate,
		usage.Provider, priceSource, usage.ReportedCost, usage.ReportedListCost, usage.ReportedAllowance)
	return err
}

func (s *Store) recordAllowances(ctx context.Context, clientID string, at time.Time, limits []proxy.RateLimit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
	return tx.Commit()
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
