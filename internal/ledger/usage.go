package ledger

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/mishankov/llm-proxy/internal/pricing"
)

type Filter struct {
	Start, End            time.Time
	Interval              time.Duration
	Source, Client, Model string
	Bucket                *time.Time
	Cursor                int64
}

type Totals struct {
	APICost           float64 `json:"apiCost"`
	CodexValue        float64 `json:"codexValue"`
	InputTokens       int64   `json:"inputTokens"`
	CachedInputTokens int64   `json:"cachedInputTokens"`
	CacheWriteTokens  int64   `json:"cacheWriteTokens"`
	OutputTokens      int64   `json:"outputTokens"`
	RequestCount      int64   `json:"requestCount"`
}

type Bucket struct {
	Start time.Time `json:"start"`
	Totals
}

type Group struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	APICost    float64 `json:"apiCost"`
	CodexValue float64 `json:"codexValue"`
	Tokens     int64   `json:"tokens"`
}

type Request struct {
	ID                  int64          `json:"id"`
	At                  time.Time      `json:"at"`
	Source              string         `json:"source"`
	Provider            string         `json:"provider,omitempty"`
	ClientID            string         `json:"clientId"`
	Client              string         `json:"client"`
	Model               string         `json:"model"`
	Endpoint            string         `json:"endpoint"`
	Method              string         `json:"method"`
	Status              int            `json:"status"`
	UsageAvailable      bool           `json:"usageAvailable"`
	InputTokens         int64          `json:"inputTokens"`
	CachedInputTokens   int64          `json:"cachedInputTokens"`
	CacheWriteTokens    int64          `json:"cacheWriteTokens"`
	CacheWrite12hTokens int64          `json:"cacheWrite12hTokens"`
	OutputTokens        int64          `json:"outputTokens"`
	Cost                *float64       `json:"cost"`
	Rates               *pricing.Rates `json:"rates"`
	PriceSource         string         `json:"priceSource,omitempty"`
	ReportedCost        *float64       `json:"reportedCost,omitempty"`
	ReportedListCost    *float64       `json:"reportedListCost,omitempty"`
	ReportedAllowance   *float64       `json:"reportedAllowance,omitempty"`
}

type Allowance struct {
	ID               string     `json:"id"`
	Label            string     `json:"label"`
	RemainingPercent float64    `json:"remainingPercent"`
	ResetsAt         *time.Time `json:"resetsAt"`
}

type Dashboard struct {
	Summary Totals   `json:"summary"`
	Buckets []Bucket `json:"buckets"`
	Groups  struct {
		Models  []Group `json:"models"`
		Clients []Group `json:"clients"`
	} `json:"groups"`
	Requests   []Request   `json:"requests"`
	NextCursor *string     `json:"nextCursor"`
	Allowance  []Allowance `json:"allowance"`
	Filters    struct {
		Models  []string `json:"models"`
		Clients []Group  `json:"clients"`
	} `json:"filters"`
}

const requestPageSize = 100

func (s *Store) Dashboard(ctx context.Context, filter Filter) (Dashboard, error) {
	var result Dashboard
	where, args := filter.conditions()
	if err := s.collectTotals(ctx, filter, where, args, &result); err != nil {
		return result, err
	}
	if err := s.collectRequests(ctx, filter, where, args, &result); err != nil {
		return result, err
	}
	if err := s.collectFilters(ctx, filter, &result); err != nil {
		return result, err
	}
	var err error
	result.Allowance, err = s.allowance(ctx, filter)
	return result, err
}

func (f Filter) conditions() (string, []any) {
	where := ` WHERE r.time >= ? AND r.time <= ?`
	args := []any{f.Start.UnixMilli(), f.End.UnixMilli()}
	if f.Source != "" {
		where += ` AND r.source = ?`
		args = append(args, f.Source)
	}
	if f.Client != "" {
		where += ` AND r.client_id = ?`
		args = append(args, f.Client)
	}
	if f.Model != "" {
		where += ` AND r.model = ?`
		args = append(args, f.Model)
	}
	return where, args
}

func (s *Store) collectTotals(ctx context.Context, filter Filter, where string, args []any, result *Dashboard) error {
	first := filter.Start.UTC().Truncate(filter.Interval)
	for at := first; !at.After(filter.End); at = at.Add(filter.Interval) {
		result.Buckets = append(result.Buckets, Bucket{Start: at})
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.time / ?, r.source, r.client_id, c.name, r.model,
        COALESCE(SUM(r.cost), 0), SUM(CASE WHEN r.usage_available THEN r.input_tokens ELSE 0 END),
        SUM(CASE WHEN r.usage_available THEN r.cached_input_tokens ELSE 0 END),
        SUM(CASE WHEN r.usage_available THEN r.cache_write_tokens ELSE 0 END),
        SUM(CASE WHEN r.usage_available THEN r.output_tokens ELSE 0 END), COUNT(*)
        FROM requests r JOIN clients c ON c.id = r.client_id`+where+
		` GROUP BY 1, 2, 3, 4, 5`, append([]any{filter.Interval.Milliseconds()}, args...)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	models, clients := make(map[string]Group), make(map[string]Group)
	for rows.Next() {
		var timeBucket int64
		var source, client, name, model string
		var cost float64
		var totals Totals
		if err := rows.Scan(&timeBucket, &source, &client, &name, &model, &cost, &totals.InputTokens,
			&totals.CachedInputTokens, &totals.CacheWriteTokens, &totals.OutputTokens, &totals.RequestCount); err != nil {
			return err
		}
		if source == "codex" {
			totals.CodexValue = cost
		} else {
			totals.APICost = cost
		}
		index := timeBucket - first.UnixMilli()/filter.Interval.Milliseconds()
		result.Buckets[index].Totals.add(totals)
		result.Summary.add(totals)
		if model != "" {
			addGroup(models, model, model, totals)
		}
		addGroup(clients, client, name, totals)
	}
	result.Groups.Models = groupList(models)
	result.Groups.Clients = groupList(clients)
	return rows.Err()
}

func (t *Totals) add(other Totals) {
	t.APICost += other.APICost
	t.CodexValue += other.CodexValue
	t.InputTokens += other.InputTokens
	t.CachedInputTokens += other.CachedInputTokens
	t.CacheWriteTokens += other.CacheWriteTokens
	t.OutputTokens += other.OutputTokens
	t.RequestCount += other.RequestCount
}

func addGroup(groups map[string]Group, id, name string, totals Totals) {
	group := groups[id]
	group.ID, group.Name = id, name
	group.APICost += totals.APICost
	group.CodexValue += totals.CodexValue
	group.Tokens += totals.InputTokens + totals.OutputTokens
	groups[id] = group
}

func groupList(groups map[string]Group) []Group {
	list := make([]Group, 0, len(groups))
	for _, group := range groups {
		list = append(list, group)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name || (list[i].Name == list[j].Name && list[i].ID < list[j].ID)
	})
	return list
}

func (s *Store) collectRequests(ctx context.Context, filter Filter, where string, args []any, result *Dashboard) error {
	if filter.Bucket != nil {
		where += ` AND r.time >= ? AND r.time < ?`
		args = append(args, filter.Bucket.UnixMilli(), filter.Bucket.Add(filter.Interval).UnixMilli())
	}
	if filter.Cursor != 0 {
		where += ` AND (r.time, r.id) < (SELECT time, id FROM requests WHERE id = ?)`
		args = append(args, filter.Cursor)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, r.time, r.source, r.client_id, c.name, r.model, r.endpoint,
        r.method, r.status, r.usage_available, r.input_tokens, r.cached_input_tokens, r.cache_write_tokens,
        r.cache_write_12h_tokens, r.output_tokens, r.cost, r.input_rate, r.cached_input_rate, r.cache_write_rate,
        r.cache_write_12h_rate, r.output_rate, r.provider, r.price_source,
        r.reported_cost, r.reported_list_cost, r.reported_allowance
        FROM requests r JOIN clients c ON c.id = r.client_id`+where+
		` ORDER BY r.time DESC, r.id DESC LIMIT ?`, append(args, requestPageSize+1)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	result.Requests = []Request{}
	var last int64
	for rows.Next() {
		if len(result.Requests) == requestPageSize {
			cursor := base64.RawURLEncoding.EncodeToString(strconv.AppendInt(nil, last, 10))
			result.NextCursor = &cursor
			break
		}
		var request Request
		var requestTime int64
		var cost sql.NullFloat64
		var input, cached, write, write12h, output sql.NullFloat64
		var reported, list, allowance sql.NullFloat64
		if err := rows.Scan(&last, &requestTime, &request.Source, &request.ClientID, &request.Client, &request.Model,
			&request.Endpoint, &request.Method, &request.Status, &request.UsageAvailable, &request.InputTokens,
			&request.CachedInputTokens, &request.CacheWriteTokens, &request.CacheWrite12hTokens, &request.OutputTokens,
			&cost, &input, &cached, &write, &write12h, &output, &request.Provider, &request.PriceSource,
			&reported, &list, &allowance); err != nil {
			return err
		}
		request.ID = last
		request.At = timestamp(requestTime)
		request.Cost = optionalAmount(cost)
		request.ReportedCost = optionalAmount(reported)
		request.ReportedListCost = optionalAmount(list)
		request.ReportedAllowance = optionalAmount(allowance)
		if input.Valid {
			request.Rates = &pricing.Rates{Input: input.Float64, CachedInput: cached.Float64, CacheWrite: write.Float64,
				CacheWrite12h: write12h.Float64, Output: output.Float64}
		}
		result.Requests = append(result.Requests, request)
	}
	return rows.Err()
}

func optionalAmount(amount sql.NullFloat64) *float64 {
	if !amount.Valid {
		return nil
	}
	return &amount.Float64
}

func ParseCursor(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	if len(value) > 26 {
		return 0, errors.New("invalid request cursor")
	}
	contents, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, errors.New("invalid request cursor")
	}
	id, err := strconv.ParseInt(string(contents), 10, 64)
	if err != nil || id < 1 {
		return 0, errors.New("invalid request cursor")
	}
	return id, nil
}

func (s *Store) collectFilters(ctx context.Context, filter Filter, result *Dashboard) error {
	result.Filters.Models = []string{}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT model FROM requests WHERE time >= ? AND time <= ? AND model <> '' ORDER BY model`,
		filter.Start.UnixMilli(), filter.End.UnixMilli())
	if err != nil {
		return err
	}
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			_ = rows.Close()
			return err
		}
		result.Filters.Models = append(result.Filters.Models, model)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	result.Filters.Clients = []Group{}
	rows, err = s.db.QueryContext(ctx, `SELECT id, name FROM clients ORDER BY name, id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var group Group
		if err := rows.Scan(&group.ID, &group.Name); err != nil {
			return err
		}
		result.Filters.Clients = append(result.Filters.Clients, group)
	}
	return rows.Err()
}

func (s *Store) allowance(ctx context.Context, filter Filter) ([]Allowance, error) {
	items := []Allowance{}
	if filter.Source == "api" {
		return items, nil
	}
	where := ` WHERE a.observed_at >= ? AND (a.resets_at IS NULL OR a.resets_at > ?)`
	args := []any{filter.End.Add(-24 * time.Hour).UnixMilli(), filter.End.UnixMilli()}
	if filter.Client != "" {
		where += ` AND a.client_id = ?`
		args = append(args, filter.Client)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.name, a.limit_id, a.window, a.window_minutes, a.remaining_percent,
        a.resets_at FROM allowances a JOIN clients c ON c.id = a.client_id`+where+` ORDER BY c.name, a.limit_id, a.window`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item Allowance
		var clientID, client, limitID, window string
		var minutes int
		var resetsAt sql.NullInt64
		if err := rows.Scan(&clientID, &client, &limitID, &window, &minutes, &item.RemainingPercent, &resetsAt); err != nil {
			return nil, err
		}
		item.ID = clientID + ":" + limitID + ":" + window
		item.ResetsAt = optionalTime(resetsAt)
		label := window
		switch {
		case minutes == 10080:
			label = "weekly"
		case minutes >= 60 && minutes%60 == 0:
			label = fmt.Sprintf("%d-hour", minutes/60)
		case minutes > 0:
			label = fmt.Sprintf("%d-minute", minutes)
		}
		if limitID != "" && limitID != "codex" {
			label = limitID + " " + label
		}
		item.Label = client + " · " + label
		items = append(items, item)
	}
	return items, rows.Err()
}
