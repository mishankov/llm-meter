package ledger

import (
	"testing"
	"time"

	"github.com/mishankov/llm-proxy/internal/pricing"
	"github.com/mishankov/llm-proxy/internal/proxy"
)

func TestBackgroundUsageUsesTheOriginalPricingDate(t *testing.T) {
	ctx := t.Context()
	store, err := Open(":memory:", pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	postAt := time.Date(2026, 12, 1, 7, 59, 0, 0, time.UTC)
	pollAt := postAt.Add(2 * time.Minute)
	client, _, err := store.CreateClient(ctx, "Background client", postAt)
	if err != nil {
		t.Fatal(err)
	}
	post := proxy.Usage{At: postAt, Source: "api", Provider: "openai", ClientID: client.ID, Model: "gpt-5.6-sol",
		Method: "POST", Endpoint: "/v1/responses", Status: 200, ResponseID: "resp_background"}
	if err := store.Record(ctx, post); err != nil {
		t.Fatal(err)
	}
	poll := post
	poll.At, poll.Method, poll.Endpoint = pollAt, "GET", "/v1/responses/resp_background"
	poll.UsageUpdate, poll.HasUsage, poll.InputTokens = true, true, 1000
	if err := store.Record(ctx, poll); err != nil {
		t.Fatal(err)
	}
	data, err := store.Dashboard(ctx, Filter{Start: postAt.Add(-time.Hour), End: pollAt, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if data.Summary.APICost != 0.004 || data.Summary.InputTokens != 1000 {
		t.Fatalf("background request used the wrong rate: %+v", data.Summary)
	}
}
