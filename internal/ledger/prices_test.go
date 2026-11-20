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
	now := time.Now().UTC()
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
	data, err := store.Dashboard(ctx, Filter{Start: now.Add(-time.Hour), End: now.Add(time.Minute), Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Requests) != 2 || data.Summary.APICost != 0.0005 {
		t.Fatalf("unexpected cost totals: %+v", data.Summary)
	}
	reported, estimate := data.Requests[0], data.Requests[1]
	if reported.Cost == nil || *reported.Cost != zero || reported.Rates != nil || reported.PriceSource != "provider-reported" ||
		reported.ReportedListCost == nil || *reported.ReportedListCost != list || reported.ReportedAllowance == nil || *reported.ReportedAllowance != allowance {
		t.Fatalf("unexpected provider pricing: %+v", reported)
	}
	if estimate.Cost == nil || *estimate.Cost != 0.0005 || estimate.Rates == nil || estimate.PriceSource != "models.dev" {
		t.Fatalf("unexpected saved rate fallback: %+v", estimate)
	}
}
