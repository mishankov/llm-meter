package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/mishankov/llm-meter/internal/proxy"
)

func (s *Store) completeRequest(ctx context.Context, tx *sql.Tx, usage *proxy.Usage, measurementSource string) (bool, error) {
	var id, originalTime int64
	var originalModel, originalTier, completionWindow string
	var originalCost sql.NullFloat64
	var alreadyMetered bool
	err := tx.QueryRowContext(ctx, `SELECT id, time, model, cost, service_tier, completion_window, usage_available FROM requests
        WHERE source = ? AND response_id = ? AND method = 'POST' AND measurement_source = ?
            AND (usage_available = 0 OR measurement_source = 'relay')
        ORDER BY (measurement_source = 'relay' AND (usage_available = 1 OR cost IS NOT NULL)) DESC, id LIMIT 1`,
		usage.Source, usage.ResponseID, measurementSource).
		Scan(&id, &originalTime, &originalModel, &originalCost, &originalTier, &completionWindow, &alreadyMetered)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if measurementSource == "relay" && (usage.ReportedCost != nil || usage.ReportedListCost != nil) {
		if err := s.mergeReportedCompletionCost(ctx, tx, id, *usage); err != nil {
			return false, err
		}
	}
	if alreadyMetered {
		if usage.ReasoningLevel != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE requests SET reasoning_level = ? WHERE id = ?`, usage.ReasoningLevel, id); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	usage.CompletionWindow = completionWindow
	if usage.Model == "" {
		usage.Model = originalModel
	}
	if usage.ServiceTier == "" {
		usage.ServiceTier = originalTier
	}
	if !usage.HasCacheWriteSplit && usage.CacheRetention == "12h" {
		usage.CacheWrite12hTokens = usage.CacheWriteTokens
	}
	var inputRate, cachedRate, writeRate, write12hRate, outputRate *float64
	rates, cost, priceSource := s.priceFor(*usage, timestamp(originalTime))
	if rates != nil && !originalCost.Valid {
		inputRate, cachedRate, writeRate, write12hRate, outputRate = &rates.Input, &rates.CachedInput, &rates.CacheWrite, &rates.CacheWrite12h, &rates.Output
	}
	_, err = tx.ExecContext(ctx, `UPDATE OR IGNORE requests SET usage_available = ?,
        model = COALESCE(NULLIF(?, ''), model), reasoning_level = COALESCE(NULLIF(?, ''), reasoning_level),
        input_tokens = ?, cached_input_tokens = ?, cache_write_tokens = ?,
        cache_write_12h_tokens = ?, output_tokens = ?, cost = COALESCE(cost, ?), input_rate = ?, cached_input_rate = ?,
        cache_write_rate = ?, cache_write_12h_rate = ?, output_rate = ?,
        price_source = COALESCE(NULLIF(price_source, ''), ?), reported_cost = COALESCE(reported_cost, ?),
        reported_list_cost = COALESCE(reported_list_cost, ?), reported_allowance = COALESCE(reported_allowance, ?)
        WHERE id = ? AND usage_available = 0`, usage.HasUsage, usage.Model, usage.ReasoningLevel, usage.InputTokens, usage.CachedInputTokens,
		usage.CacheWriteTokens, usage.CacheWrite12hTokens, usage.OutputTokens, cost, inputRate, cachedRate, writeRate,
		write12hRate, outputRate, priceSource, usage.ReportedCost, usage.ReportedListCost, usage.ReportedAllowance, id)
	return true, err
}

// A delayed provider charge can replace an estimate after the tokens landed.
func (s *Store) mergeReportedCompletionCost(ctx context.Context, tx *sql.Tx, id int64, usage proxy.Usage) error {
	var reported, list, allowance sql.NullFloat64
	var priceSource string
	if err := tx.QueryRowContext(ctx, `SELECT reported_cost, reported_list_cost, reported_allowance, price_source FROM requests WHERE id = ?`, id).
		Scan(&reported, &list, &allowance, &priceSource); err != nil {
		return err
	}
	merged := proxy.Usage{
		ReportedCost: optionalAmount(reported), ReportedListCost: optionalAmount(list), ReportedAllowance: optionalAmount(allowance),
		ReportedCostEstimated: priceSource == "provider-estimate",
	}
	mergeReportedAmounts(&merged, usage)
	_, cost, origin := s.priceFor(merged, usage.At)
	_, err := tx.ExecContext(ctx, `UPDATE requests SET cost = ?, price_source = ?, reported_cost = ?, reported_list_cost = ?,
        reported_allowance = ?, input_rate = NULL, cached_input_rate = NULL, cache_write_rate = NULL, cache_write_12h_rate = NULL,
        output_rate = NULL WHERE id = ?`, cost, origin, merged.ReportedCost, merged.ReportedListCost, merged.ReportedAllowance, id)
	return err
}

func mergeReportedAmounts(saved *proxy.Usage, observed proxy.Usage) {
	if observed.ReportedCost != nil && (saved.ReportedCost == nil || (saved.ReportedCostEstimated && !observed.ReportedCostEstimated)) {
		saved.ReportedCost, saved.ReportedCostEstimated = observed.ReportedCost, observed.ReportedCostEstimated
	}
	if saved.ReportedListCost == nil {
		saved.ReportedListCost = observed.ReportedListCost
	}
	if saved.ReportedAllowance == nil {
		saved.ReportedAllowance = observed.ReportedAllowance
	}
}

// Different relay queues can deliver a completion before the POST that incurred it.
func saveRelayCompletion(ctx context.Context, tx *sql.Tx, usage proxy.Usage) error {
	usage.RateLimits = nil // Quota readings are already applied in this transaction.
	var saved []byte
	err := tx.QueryRowContext(ctx, `SELECT payload FROM relay_completions WHERE source = ? AND response_id = ?`,
		usage.Source, usage.ResponseID).Scan(&saved)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		var queued proxy.Usage
		if err := json.Unmarshal(saved, &queued); err != nil {
			return err
		}
		usage = mergeRelayCompletion(queued, usage)
	}
	payload, err := json.Marshal(usage)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO relay_completions (source, response_id, payload)
        SELECT ?, ?, ? WHERE NOT EXISTS (
            SELECT 1 FROM requests WHERE source = ? AND response_id = ? AND method = 'POST' AND measurement_source = 'relay'
        ) ON CONFLICT (source, response_id) DO UPDATE SET payload = excluded.payload`,
		usage.Source, usage.ResponseID, payload, usage.Source, usage.ResponseID)
	return err
}

func mergeRelayCompletion(saved, observed proxy.Usage) proxy.Usage {
	mergeReportedAmounts(&saved, observed)
	if saved.HasUsage || !observed.HasUsage {
		if observed.ReasoningLevel != "" {
			saved.ReasoningLevel = observed.ReasoningLevel
		}
		return saved
	}
	observed.ReportedCost, observed.ReportedCostEstimated = saved.ReportedCost, saved.ReportedCostEstimated
	observed.ReportedListCost, observed.ReportedAllowance = saved.ReportedListCost, saved.ReportedAllowance
	if observed.Model == "" {
		observed.Model = saved.Model
	}
	if observed.ReasoningLevel == "" {
		observed.ReasoningLevel = saved.ReasoningLevel
	}
	if observed.ServiceTier == "" {
		observed.ServiceTier = saved.ServiceTier
	}
	return observed
}

func mergePendingWithPost(ctx context.Context, tx *sql.Tx, post proxy.Usage) (proxy.Usage, error) {
	var payload []byte
	err := tx.QueryRowContext(ctx, `DELETE FROM relay_completions WHERE source = ? AND response_id = ? RETURNING payload`,
		post.Source, post.ResponseID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return post, nil
	}
	if err != nil {
		return proxy.Usage{}, err
	}
	var pending proxy.Usage
	if err := json.Unmarshal(payload, &pending); err != nil {
		return proxy.Usage{}, err
	}
	observed := mergeRelayCompletion(pending, post)
	post.HasUsage, post.HasCacheWriteSplit, post.CacheRetention = observed.HasUsage, observed.HasCacheWriteSplit, observed.CacheRetention
	post.InputTokens, post.CachedInputTokens, post.OutputTokens = observed.InputTokens, observed.CachedInputTokens, observed.OutputTokens
	post.CacheWriteTokens, post.CacheWrite12hTokens = observed.CacheWriteTokens, observed.CacheWrite12hTokens
	post.ReportedCost, post.ReportedCostEstimated = observed.ReportedCost, observed.ReportedCostEstimated
	post.ReportedListCost, post.ReportedAllowance = observed.ReportedListCost, observed.ReportedAllowance
	if observed.Model != "" {
		post.Model = observed.Model
	}
	if pending.ReasoningLevel != "" {
		post.ReasoningLevel = pending.ReasoningLevel
	} else if observed.ReasoningLevel != "" {
		post.ReasoningLevel = observed.ReasoningLevel
	}
	if observed.ServiceTier != "" {
		post.ServiceTier = observed.ServiceTier
	}
	return post, nil
}
