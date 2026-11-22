package ledger

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mishankov/llm-meter/internal/pricing"
	"github.com/mishankov/llm-meter/internal/proxy"
)

func TestAntigravityReplayRetainsCompleteUsageAndOriginalClient(t *testing.T) {
	const model = "gemini-replay-fixture"
	priceFile := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(priceFile, []byte(`{"antigravity/`+model+`":{"input":2,"cachedInput":1,"output":4}}`), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := pricing.Load(priceFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name                      string
		firstOutput, replayOutput int64
	}{
		{name: "partial then complete", firstOutput: 0, replayOutput: 7},
		{name: "complete then partial", firstOutput: 7, replayOutput: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			store, err := Open(":memory:", catalog)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
			origin, _, err := store.CreateClient(ctx, "Original client", at)
			if err != nil {
				t.Fatal(err)
			}
			replayer, _, err := store.CreateClient(ctx, "Replaying client", at)
			if err != nil {
				t.Fatal(err)
			}
			usage := proxy.Usage{At: at, Source: "api", Provider: "antigravity", Model: model, Status: 200, Method: "POST",
				ResponseID: "antigravity:fixture", ClientID: origin.ID, HasUsage: true, InputTokens: 100, CachedInputTokens: 20, OutputTokens: test.firstOutput}
			if err := store.Record(ctx, usage); err != nil {
				t.Fatal(err)
			}
			usage.At, usage.ClientID, usage.OutputTokens = at.Add(time.Minute), replayer.ID, test.replayOutput
			usage.Model = ""
			if err := store.Record(ctx, usage); err != nil {
				t.Fatal(err)
			}
			page, err := store.Requests(ctx, Filter{Start: at, End: at.Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Requests) != 1 {
				t.Fatalf("got %d request rows, want 1", len(page.Requests))
			}
			request := page.Requests[0]
			if request.ClientID != origin.ID || !request.At.Equal(at) || request.InputTokens != 100 || request.CachedInputTokens != 20 || request.OutputTokens != 7 {
				t.Fatalf("incorrect replayed request: %+v", request)
			}
			if request.Model != model || request.PriceSource != "override" || request.Rates == nil || request.Rates.Output != 4 ||
				request.Cost == nil || math.Abs(*request.Cost-0.000208) > 1e-12 {
				t.Fatalf("incorrect pricing for replayed request: %+v", request)
			}
		})
	}
}

func TestClientsReportLatestRecordedRequest(t *testing.T) {
	ctx := t.Context()
	store, err := Open(":memory:", pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	idle, _, err := store.CreateClient(ctx, "Idle", at.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	client, _, err := store.CreateClient(ctx, "Connected", at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateKey(ctx, client.ID, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	latest := at.Add(5 * time.Minute)
	for _, usage := range []proxy.Usage{
		{At: latest, Status: 200, HasUsage: true},
		{At: latest, Status: 429},
		{At: at, Status: 500},
	} {
		usage.ClientID, usage.Source = client.ID, "api"
		if err := store.Record(ctx, usage); err != nil {
			t.Fatal(err)
		}
	}
	clients, err := store.Clients(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 2 || clients[0].ID != client.ID || len(clients[0].Keys) != 2 ||
		clients[1].ID != idle.ID || clients[1].LastRequest != nil {
		t.Fatalf("unexpected clients: %+v", clients)
	}
	if request := clients[0].LastRequest; request == nil || !request.At.Equal(latest) || request.Status != 429 || request.UsageAvailable {
		t.Fatalf("incorrect latest request metadata: %+v", request)
	}
}

func TestRequestOutcomeFiltersPagesWithoutChangingOverview(t *testing.T) {
	ctx := t.Context()
	store, err := Open(":memory:", pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 11, 20, 12, 30, 0, 0, time.UTC)
	client, _, err := store.CreateClient(ctx, "Outcome fixture", at)
	if err != nil {
		t.Fatal(err)
	}
	usage := proxy.Usage{At: at, Source: "codex", ClientID: client.ID, Model: "gpt-example", Status: 400, ResponseID: "interrupted"}
	if err := store.Record(ctx, usage); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE requests SET status = 0 WHERE response_id = ?", usage.ResponseID); err != nil {
		t.Fatal(err)
	}
	usage.ResponseID = ""
	usage.Status = 429
	for range requestPageSize {
		if err := store.Record(ctx, usage); err != nil {
			t.Fatal(err)
		}
	}
	for _, status := range []int{101, 199, 200, 299, 300} {
		usage.Status = status
		if err := store.Record(ctx, usage); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Record(ctx, proxy.Usage{At: at, ClientID: client.ID, RateLimits: []proxy.RateLimit{
		{ID: "codex", Window: "primary", WindowMinutes: 300, UsedPercent: 25},
	}}); err != nil {
		t.Fatal(err)
	}
	filter := Filter{Start: at.Add(-time.Hour), End: at.Add(time.Minute), Interval: time.Hour, Client: client.ID}
	baseline, err := store.Dashboard(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Summary.RequestCount != requestPageSize+6 || len(baseline.Allowance) != 1 {
		t.Fatalf("invalid outcome fixture: %+v, allowances=%+v", baseline.Summary, baseline.Allowance)
	}
	filter.Outcome = OutcomeFailed
	failed, err := store.Dashboard(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Summary != baseline.Summary || !reflect.DeepEqual(failed.Buckets, baseline.Buckets) ||
		!reflect.DeepEqual(failed.Groups, baseline.Groups) || !reflect.DeepEqual(failed.GroupedBuckets, baseline.GroupedBuckets) ||
		!reflect.DeepEqual(failed.Allowance, baseline.Allowance) {
		t.Fatal("outcome filter changed the overview")
	}
	if failed.NextCursor == nil || len(failed.Requests) != requestPageSize {
		t.Fatalf("incorrect failed page length or cursor: %d rows, cursor=%v", len(failed.Requests), failed.NextCursor)
	}
	for _, request := range failed.Requests {
		if request.Status != 429 {
			t.Fatalf("unexpected request on first failed page: %+v", request)
		}
	}
	filter.Cursor, err = ParseCursor(*failed.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.Requests(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Requests) != 1 || page.Requests[0].Status != 0 || page.NextCursor != nil {
		t.Fatalf("unexpected second failed page: %+v", page)
	}
	filter.Cursor, filter.Outcome = 0, OutcomeSucceeded
	page, err = store.Requests(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Requests) != 2 || page.Requests[0].Status != 299 || page.Requests[1].Status != 200 || page.NextCursor != nil {
		t.Fatalf("unexpected succeeded page: %+v", page)
	}
}

func TestDashboardGroupedBuckets(t *testing.T) {
	ctx := t.Context()
	store, err := Open(":memory:", pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	first, _, err := store.CreateClient(ctx, "First", start)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.CreateClient(ctx, "Second", start)
	if err != nil {
		t.Fatal(err)
	}
	cost := 0.125
	for _, usage := range []proxy.Usage{
		{At: start, Source: "api", ClientID: first.ID, Model: "a", HasUsage: true, InputTokens: 12},
		{At: start.Add(10 * time.Minute), Source: "api", ClientID: first.ID, Model: "b", HasUsage: true, InputTokens: 4, OutputTokens: 6},
		{At: start.Add(20 * time.Minute), Source: "codex", ClientID: second.ID, Model: "a", HasUsage: true, InputTokens: 8, CachedInputTokens: 3, OutputTokens: 2},
		{At: start.Add(time.Hour), Source: "api", ClientID: first.ID, HasUsage: true, InputTokens: 20},
		{At: start.Add(time.Hour + time.Minute), Source: "api", ClientID: first.ID, Model: "a", InputTokens: 100},
	} {
		usage.Status = 200
		if usage.HasUsage {
			usage.ReportedCost = &cost
		}
		if err := store.Record(ctx, usage); err != nil {
			t.Fatal(err)
		}
	}
	query := Filter{Start: start, End: start.Add(2 * time.Hour), Interval: time.Hour}
	data, err := store.Dashboard(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	wantModels := []GroupedBucket{
		{Start: start, ID: "a", Tokens: 22, EstimatedCost: 2 * cost},
		{Start: start, ID: "b", Tokens: 10, EstimatedCost: cost},
		{Start: start.Add(time.Hour), ID: "", Tokens: 20, EstimatedCost: cost},
		{Start: start.Add(time.Hour), ID: "a"},
	}
	if !reflect.DeepEqual(data.GroupedBuckets.Models, wantModels) {
		t.Fatalf("model bucket totals = %+v, want %+v", data.GroupedBuckets.Models, wantModels)
	}
	for _, bucket := range data.Buckets {
		var tokens int64
		var cost float64
		for _, clientBucket := range data.GroupedBuckets.Clients {
			if clientBucket.Start.Equal(bucket.Start) {
				tokens += clientBucket.Tokens
				cost += clientBucket.EstimatedCost
			}
		}
		if tokens != bucket.InputTokens+bucket.OutputTokens || cost != bucket.EstimatedCost {
			t.Fatalf("client totals for %s = %d tokens and %g, aggregate = %+v", bucket.Start, tokens, cost, bucket)
		}
	}
	query.Start, query.Interval, query.Source, query.Client, query.Model = start.Add(time.Minute), 24*time.Hour, "codex", second.ID, "a"
	filtered, err := store.Dashboard(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	want := []GroupedBucket{{Start: start.Truncate(24 * time.Hour), ID: second.ID, Tokens: 10, EstimatedCost: cost}}
	if !reflect.DeepEqual(filtered.GroupedBuckets.Clients, want) {
		t.Fatalf("filtered client buckets = %+v, want %+v", filtered.GroupedBuckets.Clients, want)
	}
}

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
