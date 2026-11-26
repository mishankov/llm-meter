package usagereport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/mishankov/llm-meter/internal/pricing"
	"github.com/mishankov/llm-meter/internal/proxy"
)

func TestReportKeepsUsageWhenQuotaMetadataIsInvalid(t *testing.T) {
	limits := []proxy.RateLimit{{ID: "invalid", Window: "primary", WindowMinutes: -1, UsedPercent: 10}}
	for i := range 34 {
		limits = append(limits, proxy.RateLimit{ID: fmt.Sprintf("limit-%d", i), Window: "primary", WindowMinutes: 300, UsedPercent: 20})
	}
	usage := proxy.Usage{At: time.Now(), Provider: "codex", Endpoint: CodexTelemetryEndpoint, Method: http.MethodPost,
		Status: http.StatusOK, HasUsage: true, InputTokens: 21, OutputTokens: 5, RateLimits: limits}
	event, err := NewEvent(usage)
	if err != nil {
		t.Fatal(err)
	}
	reported := event.Usage()
	if len(event.RateLimits) != 32 || event.RateLimits[0].ID != "limit-0" || reported.InputTokens != 21 ||
		reported.Source != "codex" || reported.Endpoint != CodexTelemetryEndpoint {
		t.Fatal("the report lost usage or included invalid quota data")
	}
	event.Provider = "openai"
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(bytes.NewReader(payload)); err == nil {
		t.Fatal("accepted a provider incompatible with Codex telemetry")
	}
}

func TestUsageReportRejectsRetiredProxyRoutes(t *testing.T) {
	for _, fixture := range []struct{ provider, endpoint string }{
		{"codex", "/codex/responses"},
		{"opencode-go", "/opencode-go/chat/completions"},
	} {
		event, err := NewEvent(proxy.Usage{
			At: time.Now(), Provider: fixture.provider, Endpoint: fixture.endpoint, Method: http.MethodPost,
			Status: http.StatusOK, HasUsage: true, InputTokens: 12,
		})
		if err == nil {
			t.Fatalf("created a report for retired route %q", fixture.endpoint)
		}
		event.Endpoint = fixture.endpoint
		payload, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Decode(bytes.NewReader(payload)); err == nil {
			t.Fatalf("decoded a report for retired route %q", fixture.endpoint)
		}
	}
}

func TestUnknownTierDoesNotUseStandardPricing(t *testing.T) {
	usage := proxy.Usage{At: time.Now(), Provider: "openai", Endpoint: "/v1/responses", Method: http.MethodPost,
		Status: http.StatusOK, Model: "gpt-5.2", ServiceTier: "unrecognized-tier", HasUsage: true, InputTokens: 40, OutputTokens: 4}
	event, err := NewEvent(usage)
	if err != nil {
		t.Fatal(err)
	}
	catalog := pricing.Catalog{}
	costFor := func(tier string) *float64 {
		_, cost, _ := catalog.ForProviderUsage(usage.Provider, usage.Model, tier, usage.At, usage.InputTokens, 0, 0, 0, usage.OutputTokens)
		return cost
	}
	if event.ServiceTier != "unknown" || costFor(usage.ServiceTier) != nil || costFor(event.ServiceTier) != nil || costFor("") == nil {
		t.Fatal("relay pricing did not preserve the distinction between unknown and standard tiers")
	}
}

func TestAntigravityRelayReportsGoogleUsage(t *testing.T) {
	usage := proxy.Usage{At: time.Now(), Provider: "google", Endpoint: "/antigravity/v1internal:generateContent",
		Method: http.MethodPost, Status: http.StatusOK, Model: "gemini-3.5-flash-lite", HasUsage: true, InputTokens: 40}
	event, err := NewEvent(usage)
	if err != nil {
		t.Fatal(err)
	}
	if event.Provider != "antigravity" {
		t.Fatalf("older servers require the original wire value, got %q", event.Provider)
	}
	for _, provider := range []string{"antigravity", "google"} {
		event.Provider = provider
		payload, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := Decode(bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		if reported := decoded.Usage(); reported.Provider != "google" || reported.Endpoint != usage.Endpoint {
			t.Fatalf("incorrect attribution for %q: %+v", provider, reported)
		}
	}
}

func TestReasoningLevelValidationAndRoundTrip(t *testing.T) {
	event, err := NewEvent(proxy.Usage{
		At: time.Now(), Provider: "openai", Endpoint: "/v1/responses", Method: http.MethodPost,
		Status: http.StatusOK, Model: "gpt-5.4", ReasoningLevel: "HIGH", HasUsage: true, InputTokens: 20, OutputTokens: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.ReasoningLevel != "high" {
		t.Fatalf("normalized ReasoningLevel = %q, want high", event.ReasoningLevel)
	}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(bytes.NewReader(payload))
	if err != nil || decoded.Usage().ReasoningLevel != "high" {
		t.Fatalf("round-trip failed: decoded=%+v, err=%v", decoded, err)
	}

	event.ReasoningLevel = "not-a-valid-level"
	badPayload, _ := json.Marshal(event)
	if _, err := Decode(bytes.NewReader(badPayload)); err == nil {
		t.Fatal("accepted an unknown reasoning level")
	}
}
