package ledger

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/mishankov/llm-meter/internal/pricing"
)

type Filter struct {
	Start, End                                      time.Time
	EndExclusive                                    bool
	Interval                                        time.Duration
	Sources, Providers, Clients, Models, Reasonings []string
	Outcome                                         string
	Bucket                                          *time.Time
	Cursor                                          int64
}

const (
	OutcomeSucceeded    = "succeeded"
	OutcomeFailed       = "failed"
	UnreportedReasoning = "unreported"
)

type Totals struct {
	EstimatedCost      float64 `json:"estimatedCost"`
	CacheSavings       float64 `json:"cacheSavings,omitempty"`
	InputTokens        int64   `json:"inputTokens"`
	CachedInputTokens  int64   `json:"cachedInputTokens"`
	CacheWriteTokens   int64   `json:"cacheWriteTokens"`
	OutputTokens       int64   `json:"outputTokens"`
	RequestCount       int64   `json:"requestCount"`
	PricedRequestCount int64   `json:"pricedRequestCount"`
}

type Bucket struct {
	Start time.Time `json:"start"`
	Totals
}

type ReasoningMixEntry struct {
	Level string  `json:"level"`
	Share float64 `json:"share"`
}

type Group struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	EstimatedCost      float64             `json:"estimatedCost"`
	Tokens             int64               `json:"tokens"`
	RequestCount       int64               `json:"requestCount"`
	PricedRequestCount int64               `json:"pricedRequestCount"`
	ReasoningMix       []ReasoningMixEntry `json:"reasoningMix,omitempty"`
	Transport          string              `json:"transport,omitempty"`
}

type GroupedBucket struct {
	Start         time.Time `json:"start"`
	ID            string    `json:"id"`
	EstimatedCost float64   `json:"estimatedCost"`
	Tokens        int64     `json:"tokens"`
}

type groupedBucketKey struct {
	start time.Time
	id    string
}

type Request struct {
	ID                  int64          `json:"id"`
	At                  time.Time      `json:"at"`
	Source              string         `json:"source"`
	MeasurementSource   string         `json:"measurementSource"`
	Provider            string         `json:"provider,omitempty"`
	ClientID            string         `json:"clientId"`
	Client              string         `json:"client"`
	Model               string         `json:"model"`
	ReasoningLevel      string         `json:"reasoningLevel,omitempty"`
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

type RequestPage struct {
	Requests   []Request `json:"requests"`
	NextCursor *string   `json:"nextCursor"`
}

type Dashboard struct {
	Summary       Totals   `json:"summary"`
	BucketSeconds int64    `json:"bucketSeconds,omitempty"`
	Buckets       []Bucket `json:"buckets"`
	Groups  struct {
		Models    []Group `json:"models"`
		Clients   []Group `json:"clients"`
		Reasoning []Group `json:"reasoning"`
		Providers []Group `json:"providers,omitempty"`
	} `json:"groups"`
	GroupedBuckets struct {
		Models  []GroupedBucket `json:"models"`
		Clients []GroupedBucket `json:"clients"`
	} `json:"groupedBuckets"`
	RequestPage
	Allowance []Allowance `json:"allowance"`
	Filters   struct {
		Models    []string `json:"models"`
		Clients   []Group  `json:"clients"`
		Providers []Group  `json:"providers,omitempty"`
		Reasoning []string `json:"reasoning"`
	} `json:"filters"`
}

const requestPageSize = 100

func (s *Store) Dashboard(ctx context.Context, filter Filter) (Dashboard, error) {
	var result Dashboard
	where, args := filter.conditions()
	if err := s.collectTotals(ctx, filter, where, args, &result); err != nil {
		return result, err
	}
	if err := s.collectRequests(ctx, filter, where, args, &result.RequestPage); err != nil {
		return result, err
	}
	if err := s.collectFilters(ctx, filter, &result); err != nil {
		return result, err
	}
	var err error
	result.Allowance, err = s.allowance(ctx, filter)
	return result, err
}

func (s *Store) Requests(ctx context.Context, filter Filter) (RequestPage, error) {
	var result RequestPage
	where, args := filter.conditions()
	err := s.collectRequests(ctx, filter, where, args, &result)
	return result, err
}

func (f Filter) timeOp() string {
	if f.EndExclusive {
		return "<"
	}
	return "<="
}

const normalizedProviderExpr = `CASE WHEN r.source = 'codex' THEN 'codex' WHEN r.provider = '' THEN 'api' ELSE r.provider END`

func (f Filter) conditions() (string, []any) {
	where := ` WHERE r.time >= ? AND r.time ` + f.timeOp() + ` ?`
	args := []any{f.Start.UnixMilli(), f.End.UnixMilli()}
	where, args = withSelection(where, args, "r.source", f.Sources)
	where, args = withSelection(where, args, normalizedProviderExpr, f.Providers)
	where, args = withSelection(where, args, "r.client_id", f.Clients)
	where, args = withSelection(where, args, "r.model", f.Models)
	if len(f.Reasonings) == 0 {
		return where, args
	}
	mapped := make([]string, len(f.Reasonings))
	for i, value := range f.Reasonings {
		if value == UnreportedReasoning {
			mapped[i] = ""
		} else {
			mapped[i] = value
		}
	}
	return withSelection(where, args, "r.reasoning_level", mapped)
}

func withSelection(where string, args []any, column string, values []string) (string, []any) {
	if len(values) == 0 {
		return where, args
	}
	// Binding each list as one value keeps the query within SQLite's parameter limit.
	encoded, _ := json.Marshal(values)
	return where + ` AND ` + column + ` IN (SELECT value FROM json_each(?))`, append(args, string(encoded))
}

func (s *Store) collectTotals(ctx context.Context, filter Filter, where string, args []any, result *Dashboard) error {
	result.BucketSeconds = int64(filter.Interval / time.Second)
	first := filter.Start.UTC().Truncate(filter.Interval)
	lastTime := filter.End.UTC()
	if filter.EndExclusive {
		lastTime = lastTime.Add(-time.Millisecond)
	}
	last := lastTime.Truncate(filter.Interval)
	if last.Before(first) {
		last = first
	}
	for at := first; !at.After(last); at = at.Add(filter.Interval) {
		result.Buckets = append(result.Buckets, Bucket{Start: at})
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.time / ?, r.client_id, c.name, r.model, r.reasoning_level, r.source, r.provider,
        CASE WHEN r.endpoint IN ('/claude-code/api-request', '/codex/telemetry', '/gemini-cli/api-request', '/copilot/chat', '/opencode/telemetry') THEN 1 ELSE 0 END,
        COALESCE(SUM(r.cost), 0),
        COALESCE(SUM(CASE
            WHEN r.usage_available AND r.cached_input_tokens > 0
                 AND r.input_rate IS NOT NULL AND r.cached_input_rate IS NOT NULL
                 AND r.input_rate > r.cached_input_rate
            THEN (r.cached_input_tokens * (r.input_rate - r.cached_input_rate)) / 1000000.0
            ELSE 0
        END), 0),
        SUM(CASE WHEN r.usage_available THEN r.input_tokens ELSE 0 END),
        SUM(CASE WHEN r.usage_available THEN r.cached_input_tokens ELSE 0 END),
        SUM(CASE WHEN r.usage_available THEN r.cache_write_tokens ELSE 0 END),
        SUM(CASE WHEN r.usage_available THEN r.output_tokens ELSE 0 END), COUNT(*), COUNT(r.cost)
        FROM requests r JOIN clients c ON c.id = r.client_id`+where+
		` GROUP BY 1, 2, 3, 4, 5, 6, 7, 8`, append([]any{filter.Interval.Milliseconds()}, args...)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	models, clients, reasonings, providers := make(map[string]Group), make(map[string]Group), make(map[string]Group), make(map[string]Group)
	modelReasoningCounts := make(map[string]map[string]int64)
	clientTransports := make(map[string]uint8)
	providerTransports := make(map[string]uint8)
	modelBuckets, clientBuckets := make(map[groupedBucketKey]GroupedBucket), make(map[groupedBucketKey]GroupedBucket)
	for rows.Next() {
		var timeBucket int64
		var client, name, model, reasoningLevel, source, provider string
		var isOTLP int
		var totals Totals
		if err := rows.Scan(&timeBucket, &client, &name, &model, &reasoningLevel, &source, &provider, &isOTLP, &totals.EstimatedCost, &totals.CacheSavings, &totals.InputTokens,
			&totals.CachedInputTokens, &totals.CacheWriteTokens, &totals.OutputTokens, &totals.RequestCount, &totals.PricedRequestCount); err != nil {
			return err
		}
		index := timeBucket - first.UnixMilli()/filter.Interval.Milliseconds()
		bucket := &result.Buckets[index]
		bucket.Totals.add(totals)
		result.Summary.add(totals)
		modelName := model
		if modelName == "" {
			modelName = "Unknown model"
		}
		reasoningID, reasoningName := reasoningLevel, reasoningLevel
		if reasoningID == "" {
			reasoningID, reasoningName = UnreportedReasoning, "Not reported"
		}
		providerID, providerName := providerGroup(source, provider)
		addGroup(models, model, modelName, totals)
		addGroup(clients, client, name, totals)
		addGroup(reasonings, reasoningID, reasoningName, totals)
		addGroup(providers, providerID, providerName, totals)
		if modelReasoningCounts[model] == nil {
			modelReasoningCounts[model] = make(map[string]int64)
		}
		modelReasoningCounts[model][reasoningID] += totals.RequestCount
		if isOTLP == 1 {
			clientTransports[client] |= 1
			providerTransports[providerID] |= 1
		} else {
			clientTransports[client] |= 2
			providerTransports[providerID] |= 2
		}
		addGroupedBucket(modelBuckets, bucket.Start, model, totals)
		addGroupedBucket(clientBuckets, bucket.Start, client, totals)
	}
	for id, group := range models {
		counts := modelReasoningCounts[id]
		if len(counts) == 0 || group.RequestCount == 0 {
			continue
		}
		mix := make([]ReasoningMixEntry, 0, len(counts))
		for level, n := range counts {
			mix = append(mix, ReasoningMixEntry{Level: level, Share: float64(n) / float64(group.RequestCount)})
		}
		sort.Slice(mix, func(i, j int) bool {
			if mix[i].Share != mix[j].Share {
				return mix[i].Share > mix[j].Share
			}
			return reasoningRank(mix[i].Level) < reasoningRank(mix[j].Level)
		})
		group.ReasoningMix = mix
		models[id] = group
	}
	for id, group := range clients {
		group.Transport = transportLabel(clientTransports[id])
		clients[id] = group
	}
	for id, group := range providers {
		group.Transport = transportLabel(providerTransports[id])
		providers[id] = group
	}
	result.Groups.Models = groupList(models)
	result.Groups.Clients = groupList(clients)
	result.Groups.Reasoning = reasoningGroupList(reasonings)
	result.Groups.Providers = groupList(providers)
	result.GroupedBuckets.Models = groupedBucketList(modelBuckets)
	result.GroupedBuckets.Clients = groupedBucketList(clientBuckets)
	return rows.Err()
}

func transportLabel(mask uint8) string {
	switch mask {
	case 1:
		return "OTLP"
	case 2:
		return "Proxy"
	case 3:
		return "Proxy · OTLP"
	default:
		return ""
	}
}

func providerGroup(source, provider string) (string, string) {
	if source == "codex" {
		return "codex", "Codex subscription"
	}
	switch provider {
	case "antigravity":
		return "antigravity", "Antigravity"
	case "openai":
		return "openai", "OpenAI API"
	case "openrouter":
		return "openrouter", "OpenRouter"
	case "opencode-go":
		return "opencode-go", "OpenCode Go"
	case "opencode":
		return "opencode", "OpenCode"
	case "anthropic":
		return "anthropic", "Anthropic"
	case "deepseek":
		return "deepseek", "DeepSeek"
	case "groq":
		return "groq", "Groq"
	case "mistral":
		return "mistral", "Mistral"
	case "xai":
		return "xai", "xAI"
	case "google":
		return "google", "Google"
	case "":
		return "api", "API"
	default:
		return provider, provider
	}
}

func (t *Totals) add(other Totals) {
	t.EstimatedCost += other.EstimatedCost
	t.CacheSavings += other.CacheSavings
	t.InputTokens += other.InputTokens
	t.CachedInputTokens += other.CachedInputTokens
	t.CacheWriteTokens += other.CacheWriteTokens
	t.OutputTokens += other.OutputTokens
	t.RequestCount += other.RequestCount
	t.PricedRequestCount += other.PricedRequestCount
}

func addGroup(groups map[string]Group, id, name string, totals Totals) {
	group := groups[id]
	group.ID, group.Name = id, name
	group.EstimatedCost += totals.EstimatedCost
	group.Tokens += totals.InputTokens + totals.OutputTokens
	group.RequestCount += totals.RequestCount
	group.PricedRequestCount += totals.PricedRequestCount
	groups[id] = group
}

func reasoningRank(level string) int {
	switch level {
	case "max":
		return 0
	case "xhigh":
		return 1
	case "high":
		return 2
	case "medium":
		return 3
	case "low":
		return 4
	case "minimal":
		return 5
	case "adaptive":
		return 6
	case "none":
		return 7
	case UnreportedReasoning:
		return 99
	default:
		return 50
	}
}

func reasoningGroupList(groups map[string]Group) []Group {
	list := make([]Group, 0, len(groups))
	for _, group := range groups {
		list = append(list, group)
	}
	sort.Slice(list, func(i, j int) bool {
		ri, rj := reasoningRank(list[i].ID), reasoningRank(list[j].ID)
		if ri != rj {
			return ri < rj
		}
		return list[i].ID < list[j].ID
	})
	return list
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

func addGroupedBucket(buckets map[groupedBucketKey]GroupedBucket, start time.Time, id string, totals Totals) {
	key := groupedBucketKey{start: start, id: id}
	bucket := buckets[key]
	bucket.Start, bucket.ID = start, id
	bucket.EstimatedCost += totals.EstimatedCost
	bucket.Tokens += totals.InputTokens + totals.OutputTokens
	buckets[key] = bucket
}

func groupedBucketList(buckets map[groupedBucketKey]GroupedBucket) []GroupedBucket {
	list := make([]GroupedBucket, 0, len(buckets))
	for _, bucket := range buckets {
		list = append(list, bucket)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Start.Before(list[j].Start) || (list[i].Start.Equal(list[j].Start) && list[i].ID < list[j].ID)
	})
	return list
}

func (s *Store) collectRequests(ctx context.Context, filter Filter, where string, args []any, result *RequestPage) error {
	switch filter.Outcome {
	case OutcomeSucceeded:
		where += ` AND r.status BETWEEN 200 AND 299`
	case OutcomeFailed:
		where += ` AND (r.status = 0 OR r.status >= 400)`
	}
	if filter.Bucket != nil {
		where += ` AND r.time >= ? AND r.time < ?`
		args = append(args, filter.Bucket.UnixMilli(), filter.Bucket.Add(filter.Interval).UnixMilli())
	}
	if filter.Cursor != 0 {
		where += ` AND (r.time, r.id) < (SELECT time, id FROM requests WHERE id = ?)`
		args = append(args, filter.Cursor)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, r.time, r.source, r.client_id, c.name, r.model, r.reasoning_level, r.endpoint,
        r.method, r.status, r.usage_available, r.input_tokens, r.cached_input_tokens, r.cache_write_tokens,
        r.cache_write_12h_tokens, r.output_tokens, r.cost, r.input_rate, r.cached_input_rate, r.cache_write_rate,
        r.cache_write_12h_rate, r.output_rate, r.provider, r.price_source,
        r.reported_cost, r.reported_list_cost, r.reported_allowance, r.measurement_source
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
			&request.ReasoningLevel, &request.Endpoint, &request.Method, &request.Status, &request.UsageAvailable, &request.InputTokens,
			&request.CachedInputTokens, &request.CacheWriteTokens, &request.CacheWrite12hTokens, &request.OutputTokens,
			&cost, &input, &cached, &write, &write12h, &output, &request.Provider, &request.PriceSource,
			&reported, &list, &allowance, &request.MeasurementSource); err != nil {
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
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT model FROM requests WHERE time >= ? AND time `+filter.timeOp()+` ? AND model <> '' ORDER BY model`,
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

	result.Filters.Reasoning = []string{}
	rows, err = s.db.QueryContext(ctx, `SELECT DISTINCT reasoning_level FROM requests WHERE time >= ? AND time `+filter.timeOp()+` ?`,
		filter.Start.UnixMilli(), filter.End.UnixMilli())
	if err != nil {
		return err
	}
	for rows.Next() {
		var level string
		if err := rows.Scan(&level); err != nil {
			_ = rows.Close()
			return err
		}
		if level == "" {
			level = UnreportedReasoning
		}
		result.Filters.Reasoning = append(result.Filters.Reasoning, level)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	sort.Slice(result.Filters.Reasoning, func(i, j int) bool {
		ri, rj := reasoningRank(result.Filters.Reasoning[i]), reasoningRank(result.Filters.Reasoning[j])
		if ri != rj {
			return ri < rj
		}
		return result.Filters.Reasoning[i] < result.Filters.Reasoning[j]
	})

	result.Filters.Providers = []Group{}
	rows, err = s.db.QueryContext(ctx, `SELECT DISTINCT source, provider FROM requests WHERE time >= ? AND time `+filter.timeOp()+` ?`,
		filter.Start.UnixMilli(), filter.End.UnixMilli())
	if err != nil {
		return err
	}
	filterProviders := make(map[string]Group)
	for rows.Next() {
		var source, provider string
		if err := rows.Scan(&source, &provider); err != nil {
			_ = rows.Close()
			return err
		}
		id, name := providerGroup(source, provider)
		filterProviders[id] = Group{ID: id, Name: name}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	result.Filters.Providers = groupList(filterProviders)

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
	if len(filter.Sources) > 0 && !slices.Contains(filter.Sources, "codex") {
		return items, nil
	}
	if len(filter.Providers) > 0 && !slices.Contains(filter.Providers, "codex") {
		return items, nil
	}
	where := ` WHERE a.observed_at >= ? AND (a.resets_at IS NULL OR a.resets_at > ?)`
	args := []any{filter.End.Add(-24 * time.Hour).UnixMilli(), filter.End.UnixMilli()}
	where, args = withSelection(where, args, "a.client_id", filter.Clients)
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
