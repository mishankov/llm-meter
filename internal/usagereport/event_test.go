package usagereport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/mishankov/llm-proxy/internal/pricing"
	"github.com/mishankov/llm-proxy/internal/proxy"
)

func TestReportKeepsUsageWhenQuotaMetadataIsInvalid(t *testing.T) {
	limits := []proxy.RateLimit{{ID: "invalid", Window: "primary", WindowMinutes: -1, UsedPercent: 10}}
	for i := range 34 {
		limits = append(limits, proxy.RateLimit{ID: fmt.Sprintf("limit-%d", i), Window: "primary", WindowMinutes: 300, UsedPercent: 20})
	}
	usage := proxy.Usage{At: time.Now(), Provider: "codex", Endpoint: "/backend-api/codex/responses", Method: http.MethodPost,
		Status: http.StatusOK, HasUsage: true, InputTokens: 21, OutputTokens: 5, RateLimits: limits}
	event, err := NewEvent(usage)
	if err != nil {
		t.Fatal(err)
	}
	if len(event.RateLimits) != 32 || event.RateLimits[0].ID != "limit-0" || event.Usage().InputTokens != 21 {
		t.Fatal("the report lost usage or included invalid quota data")
	}
	event.Provider = "openai"
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(bytes.NewReader(payload)); err == nil {
		t.Fatal("accepted a provider incompatible with the Codex route")
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
