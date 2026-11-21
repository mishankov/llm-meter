package ledger

import (
	"math"
	"path/filepath"
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
	if data.Summary.EstimatedCost != 0.004 || data.Summary.InputTokens != 1000 {
		t.Fatalf("background request used the wrong rate: %+v", data.Summary)
	}
}

func TestBackgroundUsageRetainsCompletionWindowAcrossRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "usage.db")
	store, err := Open(path, pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	client, _, err := store.CreateClient(ctx, "Background client", at)
	if err != nil {
		t.Fatal(err)
	}
	for _, window := range []string{"2h", "8h"} {
		post := proxy.Usage{At: at, Source: "api", Provider: "openai", ClientID: client.ID, Model: "gpt-6.2-sol",
			Method: "POST", Endpoint: "/v1/responses", Status: 200, ResponseID: "resp_" + window,
			ServiceTier: "default", CompletionWindow: window}
		if err := store.Record(ctx, post); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	pollAt := at.Add(time.Hour)
	for _, window := range []string{"2h", "8h"} {
		poll := proxy.Usage{At: pollAt, Source: "api", Provider: "openai", ClientID: client.ID,
			Method: "GET", Endpoint: "/v1/responses/resp_" + window, Status: 200, ResponseID: "resp_" + window,
			UsageUpdate: true, HasUsage: true, InputTokens: 1000, OutputTokens: 1000}
		if err := store.Record(ctx, poll); err != nil {
			t.Fatal(err)
		}
	}
	data, err := store.Dashboard(ctx, Filter{Start: at.Add(-time.Hour), End: pollAt, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(data.Summary.EstimatedCost-.0156) > 1e-12 || data.Summary.InputTokens != 2000 {
		t.Fatalf("incorrect discounted background estimate: %+v", data.Summary)
	}
	for window, want := range map[string]float64{"2h": .7, "8h": .6} {
		var cost, inputRate, outputRate float64
		err := store.db.QueryRowContext(ctx, `SELECT cost, input_rate, output_rate FROM requests
			WHERE method = 'POST' AND response_id = ?`, "resp_"+window).Scan(&cost, &inputRate, &outputRate)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(cost-.012*want) > 1e-12 || inputRate != 2*want || outputRate != 10*want {
			t.Fatalf("incorrect %s estimate and rates: %g, %g, %g", window, cost, inputRate, outputRate)
		}
	}
}
