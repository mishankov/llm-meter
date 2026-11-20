package proxy

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type httpMeter struct {
	usage        Usage
	captureUsage bool
	request      *metadataCapture
	response     *metadataCapture
	sse          *eventStream
}

func (m *httpMeter) finish() {
	if m.response != nil {
		message := m.response.message()
		m.usage.applyMessage(message, m.captureUsage)
		state := string(message.RawStatus)
		if m.usage.UsageUpdate && state != "" && state != `"completed"` && state != `"incomplete"` {
			m.usage.HasUsage = false
			m.usage.ReportedCost = nil
			m.usage.ReportedListCost = nil
			m.usage.ReportedAllowance = nil
			m.usage.ReportedCostEstimated = false
		}
	}
	if m.sse != nil {
		m.sse.finish()
	}
	if m.request != nil {
		request := m.request.message()
		m.usage.applyFallback(request.wireMetadata)
		if m.usage.Method == http.MethodPost && m.usage.Endpoint == "/v1/responses" && request.Background && request.BackgroundOptions != nil {
			switch request.BackgroundOptions.CompletionWindow {
			case "2h", "8h":
				m.usage.CompletionWindow = request.BackgroundOptions.CompletionWindow
			}
		}
	}
}

func (m *httpMeter) observeEvent(message wireMessage) {
	if m.usage.Source == "codex" {
		m.usage.mergeRateLimits(message.limits())
	}
	m.usage.applyMessage(message, m.captureUsage && message.reportsUsage())
	if message.Response != nil && message.Response.ID != "" {
		m.usage.ResponseID = message.Response.ID
	}
}

type wireMetadata struct {
	Model          string `json:"model"`
	ServiceTier    string `json:"service_tier"`
	CacheRetention string `json:"prompt_cache_retention"`
}

type wireResult struct {
	wireMetadata
	Type         string `json:"type"`
	ID           string `json:"id"`
	GeminiID     string `json:"responseId"`
	ModelVersion string `json:"modelVersion"`

	Usage       *wireUsage      `json:"usage"`
	GeminiUsage json.RawMessage `json:"usageMetadata"`
}

type wireMessage struct {
	wireResult
	StreamID          string          `json:"stream_id"`
	RawStatus         json.RawMessage `json:"status"`
	Response          *wireResult     `json:"response"`
	Message           *wireResult     `json:"message"`
	Session           *wireMetadata   `json:"session"`
	Background        bool            `json:"background"`
	BackgroundOptions *struct {
		CompletionWindow string `json:"completion_window"`
	} `json:"background_options"`
	MeteredLimitName string `json:"metered_limit_name"`
	LimitName        string `json:"limit_name"`
	RateLimits       *struct {
		Primary   *wireWindow `json:"primary"`
		Secondary *wireWindow `json:"secondary"`
	} `json:"rate_limits"`
}

type wireUsage struct {
	wireMoney
	InputTokens   *int64            `json:"input_tokens"`
	PromptTokens  *int64            `json:"prompt_tokens"`
	OutputTokens  *int64            `json:"output_tokens"`
	Completion    *int64            `json:"completion_tokens"`
	TotalTokens   *int64            `json:"total_tokens"`
	InputDetails  *wireInputDetails `json:"input_tokens_details"`
	PromptDetails *wireInputDetails `json:"prompt_tokens_details"`
	CacheRead     *int64            `json:"cache_read_input_tokens"`
	CacheCreation *int64            `json:"cache_creation_input_tokens"`
}

type wireMoney struct {
	Cost     json.RawMessage `json:"cost"`
	OpenCode json.RawMessage `json:"opencode"`
}

type wireOpenCodeCosts struct {
	ListCost  json.RawMessage `json:"list_cost"`
	Allowance json.RawMessage `json:"allowance"`
	Estimated json.RawMessage `json:"estimated"`
}

type wireGeminiUsage struct {
	wireMoney
	InputTokens     *int64 `json:"promptTokenCount"`
	ToolInputTokens *int64 `json:"toolUsePromptTokenCount"`
	OutputTokens    *int64 `json:"candidatesTokenCount"`
	ThoughtTokens   *int64 `json:"thoughtsTokenCount"`
	CachedTokens    int64  `json:"cachedContentTokenCount"`
}

type wireInputDetails struct {
	CachedTokens      int64 `json:"cached_tokens"`
	CacheWriteTokens  int64 `json:"cache_write_tokens"`
	CacheWriteDetails *struct {
		Short int64 `json:"ttl_30m_tokens"`
		Long  int64 `json:"ttl_12h_tokens"`
	} `json:"cache_write_tokens_details"`
}

type wireWindow struct {
	UsedPercent   *float64 `json:"used_percent"`
	WindowMinutes int      `json:"window_minutes"`
	ResetAt       int64    `json:"reset_at"`
}

func decodeMessage(data []byte) wireMessage {
	var message wireMessage
	if len(data) != 0 && json.Unmarshal(data, &message) != nil {
		return wireMessage{}
	}
	return message
}

func (m wireMessage) terminal() bool {
	switch m.Type {
	case "response.completed", "response.incomplete", "response.failed", "response.done":
		return true
	default:
		return false
	}
}

func (m wireMessage) reportsUsage() bool {
	return m.terminal() || m.Type == "message_start" || m.Usage != nil || len(m.GeminiUsage) != 0
}

func (u *Usage) applyMessage(message wireMessage, captureUsage bool) {
	if message.Type == "message_start" && message.Message != nil {
		result := *message.Message
		result.Type = "message"
		u.applyResult(result, captureUsage)
		return
	}
	if message.Response != nil {
		u.applyResult(*message.Response, captureUsage)
	}
	u.applyResult(message.wireResult, captureUsage && message.Response == nil)
}

func (u *Usage) applyResult(result wireResult, captureUsage bool) {
	if result.Model != "" {
		u.Model = result.Model
	} else if result.ModelVersion != "" && u.geminiUsageAvailable() {
		u.Model = result.ModelVersion
	}
	if result.ServiceTier != "" {
		u.ServiceTier = result.ServiceTier
	}
	if result.CacheRetention != "" {
		u.CacheRetention = result.CacheRetention
	}
	if !captureUsage {
		return
	}
	if result.ID != "" {
		u.ResponseID = result.ID
	} else if result.GeminiID != "" && u.geminiUsageAvailable() {
		u.ResponseID = result.GeminiID
	}
	tokens := result.Usage
	if tokens == nil && u.geminiUsageAvailable() && len(result.GeminiUsage) != 0 {
		tokens = geminiTokens(result.GeminiUsage)
	}
	if tokens == nil {
		return
	}
	u.applyReportedCosts(tokens.wireMoney)
	if result.Type == "message" || result.Type == "message_delta" || tokens.CacheRead != nil || tokens.CacheCreation != nil {
		u.applyMessagesUsage(tokens)
		return
	}
	input := firstReported(tokens.InputTokens, tokens.PromptTokens)
	output := firstReported(tokens.OutputTokens, tokens.Completion)
	if input == nil && output == nil && strings.HasSuffix(u.Endpoint, "/embeddings") {
		input = tokens.TotalTokens
	}
	if input == nil && output == nil {
		return
	}
	u.HasUsage = true
	u.InputTokens = count(input)
	u.OutputTokens = count(output)
	u.CachedInputTokens, u.CacheWriteTokens, u.CacheWrite12hTokens = 0, 0, 0
	u.HasCacheWriteSplit = false
	details := tokens.InputDetails
	if details == nil {
		details = tokens.PromptDetails
	}
	if details == nil {
		return
	}
	u.CachedInputTokens = min(max(details.CachedTokens, 0), u.InputTokens)
	u.CacheWriteTokens = min(max(details.CacheWriteTokens, 0), u.InputTokens-u.CachedInputTokens)
	if split := details.CacheWriteDetails; split != nil {
		u.CacheWrite12hTokens = min(max(split.Long, 0), u.CacheWriteTokens)
		u.HasCacheWriteSplit = true
	}
}

func (u *Usage) applyMessagesUsage(tokens *wireUsage) {
	// Messages reports uncached input separately. A cumulative delta can
	// replace a count or omit it and leave the previous count in effect.
	if tokens.InputTokens != nil || tokens.CacheRead != nil || tokens.CacheCreation != nil {
		uncached := u.InputTokens - u.CachedInputTokens - u.CacheWriteTokens
		if tokens.InputTokens != nil {
			uncached = count(tokens.InputTokens)
		}
		if tokens.CacheRead != nil {
			u.CachedInputTokens = count(tokens.CacheRead)
		}
		if tokens.CacheCreation != nil {
			u.CacheWriteTokens = count(tokens.CacheCreation)
		}
		u.CachedInputTokens = min(u.CachedInputTokens, math.MaxInt64-uncached)
		u.CacheWriteTokens = min(u.CacheWriteTokens, math.MaxInt64-uncached-u.CachedInputTokens)
		u.InputTokens = uncached + u.CachedInputTokens + u.CacheWriteTokens
		u.HasUsage = true
	}
	if tokens.OutputTokens != nil {
		u.OutputTokens = count(tokens.OutputTokens)
		u.HasUsage = true
	}
}

func (u *Usage) geminiUsageAvailable() bool {
	switch u.Provider {
	case "google", "opencode", "opencode-go":
		return true
	default:
		return false
	}
}

func geminiTokens(details json.RawMessage) *wireUsage {
	var usage *wireGeminiUsage
	if err := json.Unmarshal(details, &usage); err != nil || usage == nil {
		return nil
	}
	return &wireUsage{
		wireMoney:    usage.wireMoney,
		InputTokens:  addTokenCounts(usage.InputTokens, usage.ToolInputTokens),
		OutputTokens: addTokenCounts(usage.OutputTokens, usage.ThoughtTokens),
		InputDetails: &wireInputDetails{CachedTokens: usage.CachedTokens},
	}
}

func addTokenCounts(base, extra *int64) *int64 {
	if extra == nil {
		return base
	}
	total := count(base)
	total += min(count(extra), math.MaxInt64-total)
	return &total
}

func (u *Usage) applyReportedCosts(wire wireMoney) {
	var openCode wireOpenCodeCosts
	switch u.Provider {
	case "opencode", "opencode-go":
		if json.Unmarshal(wire.OpenCode, &openCode) == nil {
			if amount := reportedAmount(openCode.ListCost); amount != nil {
				u.ReportedListCost = amount
			}
			if amount := reportedAmount(openCode.Allowance); amount != nil {
				u.ReportedAllowance = amount
			}
		}
	case "openrouter":
		// OpenRouter charges against a USD-denominated credit balance.
	default:
		return
	}
	if amount := reportedAmount(wire.Cost); amount != nil {
		u.ReportedCost = amount
		u.ReportedCostEstimated = string(openCode.Estimated) == "true"
	}
}

func reportedAmount(raw json.RawMessage) *float64 {
	var value *float64
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil
	}
	if *value < 0 || math.IsInf(*value, 0) || math.IsNaN(*value) {
		return nil
	}
	return value
}

func (u *Usage) applyFallback(metadata wireMetadata) {
	if u.Model == "" {
		u.Model = metadata.Model
	}
	if u.ServiceTier == "" {
		u.ServiceTier = metadata.ServiceTier
	}
	if u.CacheRetention == "" {
		u.CacheRetention = metadata.CacheRetention
	}
}

func firstReported(first, second *int64) *int64 {
	if first != nil {
		return first
	}
	return second
}

func count(value *int64) int64 {
	if value == nil {
		return 0
	}
	return max(*value, 0)
}

func (m wireMessage) limits() []RateLimit {
	if m.Type != "codex.rate_limits" || m.RateLimits == nil {
		return nil
	}
	id := m.MeteredLimitName
	if id == "" {
		id = m.LimitName
	}
	if id == "" {
		id = "codex"
	}
	var result []RateLimit
	if window, ok := m.RateLimits.Primary.limit(id, "primary"); ok {
		result = append(result, window)
	}
	if window, ok := m.RateLimits.Secondary.limit(id, "secondary"); ok {
		result = append(result, window)
	}
	return result
}

func limitsFromHeaders(header http.Header) []RateLimit {
	var result []RateLimit
	for name := range header {
		prefix, ok := strings.CutSuffix(strings.ToLower(name), "-used-percent")
		if !ok || !strings.HasPrefix(prefix, "x-codex-") {
			continue
		}
		index := strings.LastIndexByte(prefix, '-')
		id, label := strings.TrimPrefix(prefix[:index], "x-"), prefix[index+1:]
		if label != "primary" && label != "secondary" {
			continue
		}
		percentage, err := strconv.ParseFloat(header.Get(name), 64)
		if err != nil {
			continue
		}
		minutes, _ := strconv.Atoi(header.Get(prefix + "-window-minutes"))
		reset, _ := strconv.ParseInt(header.Get(prefix+"-reset-at"), 10, 64)
		window := wireWindow{UsedPercent: &percentage, WindowMinutes: minutes, ResetAt: reset}
		if limit, ok := window.limit(id, label); ok {
			result = append(result, limit)
		}
	}
	return result
}

func (w *wireWindow) limit(id, label string) (RateLimit, bool) {
	if w == nil || w.UsedPercent == nil || math.IsNaN(*w.UsedPercent) || math.IsInf(*w.UsedPercent, 0) {
		return RateLimit{}, false
	}
	limit := RateLimit{
		ID:            strings.ReplaceAll(strings.ToLower(strings.TrimSpace(id)), "-", "_"),
		Window:        label,
		WindowMinutes: w.WindowMinutes,
		UsedPercent:   min(max(*w.UsedPercent, 0), 100),
	}
	if w.ResetAt > 0 {
		limit.ResetsAt = time.Unix(w.ResetAt, 0).UTC()
	}
	return limit, true
}

func (u *Usage) mergeRateLimits(limits []RateLimit) {
	for _, limit := range limits {
		replaced := false
		for i, existing := range u.RateLimits {
			if existing.ID == limit.ID && existing.Window == limit.Window {
				u.RateLimits[i] = limit
				replaced = true
				break
			}
		}
		if !replaced {
			u.RateLimits = append(u.RateLimits, limit)
		}
	}
}

type eventStream struct {
	handle     func(wireMessage)
	prefix     [5]byte
	prefixSize int
	data       *metadataCapture
	isData     bool
}

func (s *eventStream) Write(p []byte) (int, error) {
	size := len(p)
	for len(p) != 0 {
		line, rest, end := bytes.Cut(p, []byte{'\n'})
		s.consume(line)
		if end {
			s.endLine()
		}
		p = rest
	}
	return size, nil
}

func (s *eventStream) consume(line []byte) {
	if s.prefixSize < len(s.prefix) {
		n := copy(s.prefix[s.prefixSize:], line)
		s.prefixSize += n
		line = line[n:]
		if s.prefixSize == len(s.prefix) && string(s.prefix[:]) == "data:" {
			s.isData = true
			if s.data == nil {
				s.data = &metadataCapture{}
			}
		}
	}
	if s.isData {
		_, _ = s.data.Write(line)
	}
}

func (s *eventStream) endLine() {
	if s.prefixSize == 0 || s.prefixSize == 1 && s.prefix[0] == '\r' {
		s.emit()
	} else if s.isData {
		_, _ = s.data.Write([]byte{'\n'})
	}
	s.prefixSize, s.isData = 0, false
}

func (s *eventStream) finish() { s.emit() }

func (s *eventStream) emit() {
	if s.data != nil {
		s.handle(s.data.message())
		s.data = nil
	}
}
