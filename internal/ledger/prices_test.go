package ledger

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishankov/llm-proxy/internal/pricing"
	"github.com/mishankov/llm-proxy/internal/proxy"
)

func TestPricingSnapshotSurvivesARefreshFailureAndRestart(t *testing.T) {
	var available atomic.Bool
	available.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !available.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"openai":{"models":{"fetched-model":{"cost":{"input":2,"output":3}}}}}`)
	}))
	defer server.Close()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "ledger.db")
	store, err := Open(path, pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	feed := pricing.Feed{Name: "models.dev", URL: server.URL}
	if err := store.refreshFeed(ctx, server.Client(), feed); err != nil {
		t.Fatal(err)
	}
	available.Store(false)
	if err := store.refreshFeed(ctx, server.Client(), feed); err == nil {
		t.Fatal("failed refresh unexpectedly succeeded")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Truncate(time.Hour)
	client, _, err := store.CreateClient(ctx, "Pricing fixture", now)
	if err != nil {
		t.Fatal(err)
	}
	usage := proxy.Usage{At: now, Provider: "openai", Source: "api", Model: "fetched-model", ClientID: client.ID,
		ResponseID: "estimated", Status: 200, HasUsage: true, InputTokens: 100, OutputTokens: 100}
	if err := store.Record(ctx, usage); err != nil {
		t.Fatal(err)
	}
	usage.At = now.Add(time.Second)
	usage.Provider, usage.ResponseID = "opencode-go", "reported"
	zero, list, allowance := 0.0, 0.01, 0.005
	usage.ReportedCost, usage.ReportedListCost, usage.ReportedAllowance = &zero, &list, &allowance
	if err := store.Record(ctx, usage); err != nil {
		t.Fatal(err)
	}
	usage.At = now.Add(2 * time.Second)
	usage.Provider, usage.Model, usage.ResponseID = "openai", "unpriced-fixture", "unpriced"
	usage.ReportedCost, usage.ReportedListCost, usage.ReportedAllowance = nil, nil, nil
	if err := store.Record(ctx, usage); err != nil {
		t.Fatal(err)
	}
	data, err := store.Dashboard(ctx, Filter{Start: now.Add(-time.Hour), End: now.Add(time.Minute), Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Requests) != 3 || data.Summary.RequestCount != 3 || data.Summary.PricedRequestCount != 2 || data.Summary.EstimatedCost != 0.0005 {
		t.Fatalf("unexpected cost totals: %+v", data.Summary)
	}
	if data.Buckets[len(data.Buckets)-1].PricedRequestCount != 2 ||
		len(data.Groups.Models) != 2 || data.Groups.Models[0].PricedRequestCount != 2 || data.Groups.Models[1].PricedRequestCount != 0 ||
		len(data.Groups.Clients) != 1 || data.Groups.Clients[0].RequestCount != 3 || data.Groups.Clients[0].PricedRequestCount != 2 {
		t.Fatalf("unexpected pricing coverage: buckets=%+v groups=%+v", data.Buckets, data.Groups)
	}
	if data.Requests[0].Cost != nil {
		t.Fatalf("unexpected cost for unpriced request: %+v", data.Requests[0])
	}
	reported, estimate := data.Requests[1], data.Requests[2]
	if reported.Cost == nil || *reported.Cost != zero || reported.Rates != nil || reported.PriceSource != "provider-reported" ||
		reported.ReportedListCost == nil || *reported.ReportedListCost != list || reported.ReportedAllowance == nil || *reported.ReportedAllowance != allowance {
		t.Fatalf("unexpected provider pricing: %+v", reported)
	}
	if estimate.Cost == nil || *estimate.Cost != 0.0005 || estimate.Rates == nil || estimate.PriceSource != "models.dev" {
		t.Fatalf("unexpected saved rate fallback: %+v", estimate)
	}
}
