package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
)

const maxGzipRequestMetadataBytes = 32 << 20

type httpMeter struct {
	usage         Usage
	captureUsage  bool
	request       *metadataCapture
	requestCloser io.Closer
	response      *metadataCapture
	sse           *eventStream
}

func (m *httpMeter) finish() {
	if m.requestCloser != nil {
		_ = m.requestCloser.Close()
	}
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
		if request.Request != nil {
			m.usage.applyFallback(*request.Request)
		}
		if m.usage.Method == http.MethodPost && m.usage.Endpoint == "/v1/responses" && request.Background && request.BackgroundOptions != nil {
			switch request.BackgroundOptions.CompletionWindow {
			case "2h", "8h":
				m.usage.CompletionWindow = request.BackgroundOptions.CompletionWindow
			}
		}
	}
}

func (m *httpMeter) observeEvent(message wireMessage) {
	// Antigravity can report a response ID before it reports token usage.
	m.usage.applyMessage(message, m.captureUsage && (IsAntigravityEndpoint(m.usage.Endpoint) || message.reportsUsage()))
	if message.Response != nil && message.Response.ID != "" {
		m.usage.ResponseID = message.Response.ID
	}
}

type wireMetadata struct {
	Model                 string            `json:"model"`
	ServiceTier           string            `json:"service_tier"`
	CacheRetention        string            `json:"prompt_cache_retention"`
	ReasoningEffort       string            `json:"reasoning_effort"`
	Reasoning             *wireReasoning    `json:"reasoning"`
	OutputConfig          *wireOutputConfig `json:"output_config"`
	Thinking              *wireThinking     `json:"thinking"`
	GenerationConfig      json.RawMessage   `json:"generationConfig"`
	GenerationConfigSnake json.RawMessage   `json:"generation_config"`
}

type wireReasoning struct {
	Effort string `json:"effort"`
}

type wireOutputConfig struct {
	Effort string `json:"effort"`
}

type wireThinking struct {
	Type string `json:"type"`
}

func (m wireMetadata) reasoningLevel() string {
	if level := NormalizeReasoningLevel(m.ReasoningEffort); level != "" {
		return level
	}
	if m.Reasoning != nil {
		if level := NormalizeReasoningLevel(m.Reasoning.Effort); level != "" {
			return level
		}
	}
	if m.OutputConfig != nil {
		if level := NormalizeReasoningLevel(m.OutputConfig.Effort); level != "" {
			return level
		}
	}
	for _, raw := range []json.RawMessage{m.GenerationConfig, m.GenerationConfigSnake} {
		if level := generationConfigReasoningLevel(raw); level != "" {
			return level
		}
	}
	if m.Thinking != nil {
		switch strings.ToLower(strings.TrimSpace(m.Thinking.Type)) {
		case "disabled":
			return "none"
		case "adaptive":
			return "adaptive"
		}
	}
	return ""
}

func generationConfigReasoningLevel(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var gc struct {
		ThinkingConfig      json.RawMessage `json:"thinkingConfig"`
		ThinkingConfigSnake json.RawMessage `json:"thinking_config"`
	}
	if json.Unmarshal(raw, &gc) != nil {
		return ""
	}
	for _, tcRaw := range []json.RawMessage{gc.ThinkingConfig, gc.ThinkingConfigSnake} {
		if len(tcRaw) == 0 {
			continue
		}
		var tc struct {
			ThinkingLevel       json.RawMessage `json:"thinkingLevel"`
			ThinkingLevelSnake  json.RawMessage `json:"thinking_level"`
			ThinkingBudget      json.RawMessage `json:"thinkingBudget"`
			ThinkingBudgetSnake json.RawMessage `json:"thinking_budget"`
		}
		if json.Unmarshal(tcRaw, &tc) != nil {
			continue
		}
		for _, levelRaw := range []json.RawMessage{tc.ThinkingLevel, tc.ThinkingLevelSnake} {
			var s string
			if len(levelRaw) != 0 && json.Unmarshal(levelRaw, &s) == nil {
				if level := NormalizeReasoningLevel(s); level != "" {
					return level
				}
			}
		}
		for _, budgetRaw := range []json.RawMessage{tc.ThinkingBudget, tc.ThinkingBudgetSnake} {
			var budget *int64
			if len(budgetRaw) != 0 && json.Unmarshal(budgetRaw, &budget) == nil && budget != nil {
				switch *budget {
				case 0:
					return "none"
				case -1:
					return "adaptive"
				}
			}
		}
	}
	return ""
}

// NormalizeReasoningLevel maps provider reasoning effort labels to canonical lowercase values.
func NormalizeReasoningLevel(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.TrimPrefix(normalized, "thinking_level_")
	switch normalized {
	case "none", "disabled", "off":
		return "none"
	case "minimal":
		return "minimal"
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh":
		return "xhigh"
	case "max":
		return "max"
	case "adaptive":
		return "adaptive"
	default:
		return ""
	}
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
	Request           *wireMetadata   `json:"request"`
	Background        bool            `json:"background"`
	BackgroundOptions *struct {
		CompletionWindow string `json:"completion_window"`
	} `json:"background_options"`
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
	if level := result.reasoningLevel(); level != "" {
		u.ReasoningLevel = level
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
	if u.ReasoningLevel == "" {
		u.ReasoningLevel = metadata.reasoningLevel()
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

const responseStreamProbeLimit = 8 << 10

// responseStreamProbe checks for a framed Responses event during normal body reads.
type responseStreamProbe struct {
	meter     *httpMeter
	stream    *eventStream
	remaining int
}

func (m *httpMeter) probeResponseStream() *responseStreamProbe {
	stream := &eventStream{}
	stream.handle = func(message wireMessage) {
		eventType, ok := strings.CutPrefix(message.Type, "response.")
		if !ok || eventType == "" {
			return
		}
		// Confirmed framing takes precedence over the response's MIME type.
		m.response = nil
		m.sse = stream
		stream.handle = m.observeEvent
		m.observeEvent(message)
	}
	return &responseStreamProbe{meter: m, stream: stream, remaining: responseStreamProbeLimit}
}

func (p *responseStreamProbe) Write(data []byte) (int, error) {
	if p.meter.sse != nil {
		return p.meter.sse.Write(data)
	}
	if p.stream != nil {
		n := min(len(data), p.remaining)
		p.remaining -= n
		_, _ = p.stream.Write(data[:n])
		if p.meter.sse != nil {
			_, _ = p.meter.sse.Write(data[n:])
			return len(data), nil
		}
		if p.remaining == 0 {
			p.stream = nil
		}
	}
	if p.meter.response != nil {
		return p.meter.response.Write(data)
	}
	return len(data), nil
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

type gzipMetadataObserver struct {
	pw   *io.PipeWriter
	done chan struct{}
	once sync.Once
}

func newGzipMetadataObserver(dst io.Writer) *gzipMetadataObserver {
	pr, pw := io.Pipe()
	g := &gzipMetadataObserver{
		pw:   pw,
		done: make(chan struct{}),
	}
	go func() {
		defer close(g.done)
		defer pr.Close()
		zr, err := gzip.NewReader(pr)
		if err != nil {
			return
		}
		defer zr.Close()
		_, _ = io.Copy(dst, io.LimitReader(zr, maxGzipRequestMetadataBytes))
	}()
	return g
}

func (g *gzipMetadataObserver) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	_, _ = g.pw.Write(p)
	return len(p), nil
}

func (g *gzipMetadataObserver) Close() error {
	g.once.Do(func() { _ = g.pw.Close() })
	<-g.done
	return nil
}
