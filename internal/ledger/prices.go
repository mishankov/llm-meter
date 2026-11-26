package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/mishankov/llm-meter/internal/pricing"
	"github.com/mishankov/llm-meter/internal/proxy"
)

const refreshInterval = 24 * time.Hour

func preparePricing(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`PRAGMA table_info(requests)`)
	if err != nil {
		return err
	}
	columns := make(map[string]bool)
	for rows.Next() {
		var position, notNull, primaryKey int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&position, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, column := range []struct{ name, definition string }{
		{"provider", "TEXT NOT NULL DEFAULT ''"},
		{"price_source", "TEXT NOT NULL DEFAULT ''"},
		{"reported_cost", "REAL"},
		{"reported_list_cost", "REAL"},
		{"reported_allowance", "REAL"},
		{"completion_window", "TEXT NOT NULL DEFAULT ''"},
		{"service_tier", "TEXT NOT NULL DEFAULT ''"},
		{"measurement_source", "TEXT NOT NULL DEFAULT 'proxy'"},
		{"reasoning_level", "TEXT NOT NULL DEFAULT ''"},
	} {
		if columns[column.name] {
			continue
		}
		if _, err := tx.Exec("ALTER TABLE requests ADD COLUMN " + column.name + " " + column.definition); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS pricing_snapshots (
        source TEXT PRIMARY KEY, url TEXT NOT NULL, fetched_at INTEGER NOT NULL, snapshot BLOB NOT NULL
    );
    CREATE UNIQUE INDEX IF NOT EXISTS requests_metered_response ON requests(source, response_id)
        WHERE (usage_available = 1 OR cost IS NOT NULL) AND response_id <> '';
    UPDATE requests SET provider = 'google' WHERE provider = 'antigravity';`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) loadPricing() error {
	rows, err := s.db.Query(`SELECT snapshot FROM pricing_snapshots`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return err
		}
		var snapshot pricing.Snapshot
		if err := json.Unmarshal(data, &snapshot); err != nil {
			slog.Warn("Ignoring unreadable saved model prices", "error", err)
			continue
		}
		s.pricing.SetSnapshot(snapshot)
	}
	return rows.Err()
}

func (s *Store) savePricing(ctx context.Context, snapshot pricing.Snapshot) error {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO pricing_snapshots (source, url, fetched_at, snapshot) VALUES (?, ?, ?, ?)
        ON CONFLICT (source) DO UPDATE SET url = excluded.url, fetched_at = excluded.fetched_at, snapshot = excluded.snapshot`,
		snapshot.Source, snapshot.URL, snapshot.FetchedAt.UnixMilli(), data); err != nil {
		return err
	}
	s.pricingMu.Lock()
	s.pricing.SetSnapshot(snapshot)
	s.pricingMu.Unlock()
	return nil
}

// RefreshPrices runs outside the request path. Fetch failures leave the saved
// snapshot available to both current requests and subsequent restarts.
func (s *Store) RefreshPrices(ctx context.Context, log *slog.Logger) {
	client := &http.Client{Timeout: 20 * time.Second}
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		for _, feed := range pricing.DefaultFeeds {
			if ctx.Err() != nil {
				return
			}
			if err := s.refreshFeed(ctx, client, feed); err != nil {
				if ctx.Err() == nil {
					log.Warn("Model price refresh failed", "source", feed.Name, "error", err)
				}
			} else {
				log.Info("Model prices refreshed", "source", feed.Name)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Store) refreshFeed(ctx context.Context, client *http.Client, feed pricing.Feed) error {
	snapshot, err := pricing.Fetch(ctx, client, feed)
	if err != nil {
		return err
	}
	if err := s.savePricing(ctx, snapshot); err != nil {
		return fmt.Errorf("save model prices: %w", err)
	}
	return nil
}

func (s *Store) priceFor(usage proxy.Usage, at time.Time) (*pricing.Rates, *float64, string) {
	if usage.ReportedCost != nil {
		if usage.ReportedCostEstimated {
			return nil, usage.ReportedCost, "provider-estimate"
		}
		return nil, usage.ReportedCost, "provider-reported"
	}
	if usage.ReportedListCost != nil {
		return nil, usage.ReportedListCost, "provider-list"
	}
	if !usage.HasUsage {
		return nil, nil, ""
	}
	s.pricingMu.RLock()
	defer s.pricingMu.RUnlock()
	if proxy.IsAntigravityEndpoint(usage.Endpoint) {
		return s.pricing.ForAntigravityUsage(usage.Model, usage.ServiceTier, at,
			usage.InputTokens, usage.CachedInputTokens, usage.CacheWriteTokens, usage.CacheWrite12hTokens, usage.OutputTokens)
	}
	rates, cost, origin := s.pricing.ForProviderUsage(usage.Provider, usage.Model, usage.ServiceTier, at,
		usage.InputTokens, usage.CachedInputTokens, usage.CacheWriteTokens, usage.CacheWrite12hTokens, usage.OutputTokens)
	if usage.Provider == "openai" && rates != nil && origin != "override" {
		*cost *= rates.ApplyBackgroundDiscount(usage.Model, usage.ServiceTier, usage.CompletionWindow)
	}
	return rates, cost, origin
}
