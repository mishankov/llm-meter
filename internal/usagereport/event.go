// Package usagereport defines the metadata sent from a relay to the ledger.
package usagereport

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mishankov/llm-meter/internal/proxy"
)

const (
	Path               = "/api/usage/events"
	MaxBodyBytes       = 16 << 10
	ClaudeCodeEndpoint = "/claude-code/api-request"
)

// Event carries metering metadata. The collector derives the client identity
// from the upload key and the usage source from the canonical endpoint.
type Event struct {
	ID                    string      `json:"id"`
	At                    time.Time   `json:"at"`
	Provider              string      `json:"provider,omitempty"`
	Model                 string      `json:"model,omitempty"`
	Endpoint              string      `json:"endpoint"`
	Method                string      `json:"method"`
	Status                int         `json:"status"`
	ResponseIDHash        string      `json:"responseIdHash,omitempty"`
	ServiceTier           string      `json:"serviceTier,omitempty"`
	CompletionWindow      string      `json:"completionWindow,omitempty"`
	CacheRetention        string      `json:"cacheRetention,omitempty"`
	InputTokens           int64       `json:"inputTokens,omitempty"`
	CachedInputTokens     int64       `json:"cachedInputTokens,omitempty"`
	CacheWriteTokens      int64       `json:"cacheWriteTokens,omitempty"`
	CacheWrite12hTokens   int64       `json:"cacheWrite12hTokens,omitempty"`
	OutputTokens          int64       `json:"outputTokens,omitempty"`
	HasCacheWriteSplit    bool        `json:"hasCacheWriteSplit,omitempty"`
	UsageUpdate           bool        `json:"usageUpdate,omitempty"`
	HasUsage              bool        `json:"hasUsage,omitempty"`
	ReportedCost          *float64    `json:"reportedCost,omitempty"`
	ReportedListCost      *float64    `json:"reportedListCost,omitempty"`
	ReportedAllowance     *float64    `json:"reportedAllowance,omitempty"`
	ReportedCostEstimated bool        `json:"reportedCostEstimated,omitempty"`
	RateLimits            []RateLimit `json:"rateLimits,omitempty"`
}

type RateLimit struct {
	ID            string    `json:"id"`
	Window        string    `json:"window"`
	WindowMinutes int       `json:"windowMinutes"`
	UsedPercent   float64   `json:"usedPercent"`
	ResetsAt      time.Time `json:"resetsAt,omitzero"`
}

var (
	eventID    = regexp.MustCompile(`^[A-Z2-7]{26}$`)
	idHash     = regexp.MustCompile(`^[a-f0-9]{64}$`)
	identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/@+~-]{0,255}$`)
	providers  = []string{"codex", "openai", "opencode-go", "openrouter", "opencode", "anthropic", "deepseek", "groq", "mistral", "xai", "google"}
	tiers      = []string{"auto", "default", "standard", "scale", "batch", "flex", "fast", "priority", "ultrafast", "unknown"}
	methods    = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, "OTHER"}
)

// NewEvent takes a snapshot after the relay finishes metering. Its ID is
// generated once and retained with the payload across upload retries.
func NewEvent(usage proxy.Usage) (Event, error) {
	e := Event{
		ID: rand.Text(), At: usage.At.UTC(), Provider: usage.Provider, Endpoint: endpointName(usage.Endpoint),
		Method: usage.Method, Status: usage.Status,
		ServiceTier:      serviceTier(usage.ServiceTier),
		CompletionWindow: knownValue(usage.CompletionWindow, []string{"2h", "8h"}),
		CacheRetention:   knownValue(usage.CacheRetention, []string{"12h"}),
		InputTokens:      usage.InputTokens, CachedInputTokens: usage.CachedInputTokens,
		CacheWriteTokens: usage.CacheWriteTokens, CacheWrite12hTokens: usage.CacheWrite12hTokens, OutputTokens: usage.OutputTokens,
		HasCacheWriteSplit: usage.HasCacheWriteSplit, UsageUpdate: usage.UsageUpdate, HasUsage: usage.HasUsage,
		ReportedCost: usage.ReportedCost, ReportedListCost: usage.ReportedListCost, ReportedAllowance: usage.ReportedAllowance,
		ReportedCostEstimated: usage.ReportedCostEstimated,
	}
	// Keep the original wire value so a new relay can still report to older servers.
	if e.Provider == "google" && proxy.IsAntigravityEndpoint(e.Endpoint) {
		e.Provider = "antigravity"
	}
	if identifier.MatchString(usage.Model) {
		e.Model = usage.Model
	}
	if !slices.Contains(methods, e.Method) {
		e.Method = "OTHER"
	}
	if usage.ResponseID != "" {
		digest := sha256.Sum256([]byte(usage.ResponseID))
		e.ResponseIDHash = hex.EncodeToString(digest[:])
	}
	for _, limit := range usage.RateLimits {
		if len(e.RateLimits) == 32 {
			break
		}
		if reading := RateLimit(limit); reading.valid() {
			e.RateLimits = append(e.RateLimits, reading)
		}
	}
	return e, e.validate()
}

// Decode accepts exactly one event. The HTTP caller bounds the reader.
func Decode(body io.Reader) (Event, error) {
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var event Event
	if err := decoder.Decode(&event); err != nil || decoder.Decode(new(any)) != io.EOF {
		return Event{}, errors.New("invalid usage event JSON")
	}
	return event, event.validate()
}

func (e Event) validate() error {
	if !eventID.MatchString(e.ID) || e.At.IsZero() || e.At.After(time.Now().Add(10*time.Minute)) {
		return errors.New("invalid event ID or observation time")
	}
	if e.Endpoint == "" || e.Endpoint != endpointName(e.Endpoint) || !slices.Contains(methods, e.Method) ||
		!validProvider(e.Endpoint, e.Provider) || (e.Model != "" && !identifier.MatchString(e.Model)) ||
		(e.ResponseIDHash != "" && !idHash.MatchString(e.ResponseIDHash)) {
		return errors.New("invalid request metadata")
	}
	if e.ServiceTier != knownValue(e.ServiceTier, tiers) || e.CompletionWindow != knownValue(e.CompletionWindow, []string{"2h", "8h"}) ||
		e.CacheRetention != knownValue(e.CacheRetention, []string{"12h"}) || (e.UsageUpdate && e.Method != http.MethodGet) {
		return errors.New("invalid pricing metadata")
	}
	if (e.Status == 0 && len(e.RateLimits) == 0) || (e.Status != 0 && (e.Status < 100 || e.Status > 599)) || len(e.RateLimits) > 32 {
		return errors.New("invalid status or rate limits")
	}
	for _, count := range [...]int64{e.InputTokens, e.CachedInputTokens, e.CacheWriteTokens, e.CacheWrite12hTokens, e.OutputTokens} {
		if count < 0 || count > 1<<40 {
			return errors.New("invalid token count")
		}
	}
	for _, amount := range [...]*float64{e.ReportedCost, e.ReportedListCost, e.ReportedAllowance} {
		if amount != nil && !(*amount >= 0 && *amount <= 1e12) {
			return errors.New("invalid reported amount")
		}
	}
	for _, limit := range e.RateLimits {
		if !limit.valid() {
			return errors.New("invalid rate limit")
		}
	}
	return nil
}

func (limit RateLimit) valid() bool {
	return identifier.MatchString(limit.ID) && (limit.Window == "primary" || limit.Window == "secondary") &&
		limit.WindowMinutes >= 0 && limit.WindowMinutes <= 525600 && limit.UsedPercent >= 0 && limit.UsedPercent <= 100 &&
		limit.ResetsAt.Year() >= 1 && limit.ResetsAt.Year() <= 9999
}

func validProvider(endpoint, provider string) bool {
	switch {
	case endpoint == ClaudeCodeEndpoint:
		return provider == "anthropic"
	case strings.HasPrefix(endpoint, proxy.CodexPrefix):
		return provider == "codex"
	case strings.HasPrefix(endpoint, proxy.OpenCodeGoPrefix):
		return provider == "opencode-go"
	case proxy.IsAntigravityEndpoint(endpoint):
		return provider == "google" || provider == "antigravity"
	default:
		return provider == "" || (provider != "codex" && slices.Contains(providers, provider))
	}
}

func serviceTier(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || slices.Contains(tiers, value) {
		return value
	}
	return "unknown"
}

func (e Event) Usage() proxy.Usage {
	source := "api"
	provider := e.Provider
	if strings.HasPrefix(e.Endpoint, proxy.CodexPrefix) {
		source = "codex"
	}
	if proxy.IsAntigravityEndpoint(e.Endpoint) {
		provider = "google"
	}
	usage := proxy.Usage{
		At: e.At, Source: source, Provider: provider, Model: e.Model, Endpoint: e.Endpoint, Method: e.Method, Status: e.Status,
		ResponseID: e.ResponseIDHash, ServiceTier: e.ServiceTier, CompletionWindow: e.CompletionWindow, CacheRetention: e.CacheRetention,
		InputTokens: e.InputTokens, CachedInputTokens: e.CachedInputTokens, CacheWriteTokens: e.CacheWriteTokens,
		CacheWrite12hTokens: e.CacheWrite12hTokens, OutputTokens: e.OutputTokens,
		HasCacheWriteSplit: e.HasCacheWriteSplit, UsageUpdate: e.UsageUpdate, HasUsage: e.HasUsage,
		ReportedCost: e.ReportedCost, ReportedListCost: e.ReportedListCost, ReportedAllowance: e.ReportedAllowance,
		ReportedCostEstimated: e.ReportedCostEstimated,
	}
	for _, limit := range e.RateLimits {
		usage.RateLimits = append(usage.RateLimits, proxy.RateLimit(limit))
	}
	return usage
}

func knownValue(value string, values []string) string {
	if slices.Contains(values, value) {
		return value
	}
	return ""
}

// endpointName drops URL parameters and path segments that may contain user
// data. Only known operation names and fixed route templates cross the wire.
func endpointName(path string) string {
	path, _, _ = strings.Cut(path, "?")
	if path == ClaudeCodeEndpoint {
		return path
	}
	for _, prefix := range [...]string{"/v1", proxy.CodexPrefix, proxy.OpenCodeGoPrefix, proxy.AntigravityPrefix} {
		suffix, ok := strings.CutPrefix(path, prefix)
		if !ok || (suffix != "" && !strings.HasPrefix(suffix, "/")) {
			continue
		}
		switch suffix {
		case "/responses", "/chat/completions", "/completions", "/models", "/embeddings", "/messages", "/usage", "/realtime",
			"/v1internal:generateContent", "/v1internal:streamGenerateContent", "/v1internal:loadCodeAssist", "/v1internal:fetchAvailableModels",
			"/v1internal:retrieveUserQuotaSummary", "/v1internal:listExperiments", "/v1internal:fetchUserInfo", "/v1internal:fetchAdminControls",
			"/v1internal:writeTrajectoryAcls":
			return prefix + suffix
		}
		if strings.HasPrefix(suffix, "/responses/") {
			return prefix + "/responses/:id"
		}
		if strings.HasPrefix(suffix, "/models/") {
			return prefix + "/models/:id"
		}
		return prefix
	}
	return ""
}
