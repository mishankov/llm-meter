package ledger

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mishankov/llm-proxy/internal/pricing"
	"github.com/mishankov/llm-proxy/internal/proxy"
)

func TestRelayRecordRollsBackIdempotencyAndAllowancesOnFailure(t *testing.T) {
	store, err := Open(":memory:", pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	at := time.Now().UTC()
	client, _, err := store.CreateClient(ctx, "Relay", at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_request BEFORE INSERT ON requests BEGIN SELECT RAISE(ABORT, 'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	usage := proxy.Usage{At: at, Status: http.StatusOK, Source: "codex", Provider: "codex", Method: http.MethodPost,
		RateLimits: []proxy.RateLimit{{ID: "codex", Window: "primary", WindowMinutes: 300, UsedPercent: 12}}}
	const id = "fixture-event"
	if err := store.RecordRelay(ctx, client.ID, id, usage); err == nil {
		t.Fatal("expected the first record attempt to fail")
	}
	var limits int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM allowances`).Scan(&limits); err != nil || limits != 0 {
		t.Fatalf("partial allowance commit: count %d, %v", limits, err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_request`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.RecordRelay(ctx, client.ID, id, usage); err != nil {
			t.Fatal(err)
		}
	}
	filter := Filter{Start: at.Add(-time.Minute), End: at.Add(time.Minute), Interval: time.Minute, Client: client.ID}
	dashboard, err := store.Dashboard(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Summary.RequestCount != 1 || len(dashboard.Allowance) != 1 || dashboard.Allowance[0].RemainingPercent != 88 {
		t.Fatalf("request and quota did not commit together on retry: %+v, %+v", dashboard.Summary, dashboard.Allowance)
	}
}

func TestRelayBackgroundUpdatesInAnyOrder(t *testing.T) {
	for _, order := range []string{"request cost tokens", "request tokens cost", "cost request tokens", "cost tokens request", "tokens cost request", "tokens request cost"} {
		for _, price := range []string{"catalog", "list", "provider estimate", "queued estimate", "two actual charges", "duplicate request", "metered request"} {
			t.Run(price+"/"+order, func(t *testing.T) {
				store, err := Open(":memory:", pricing.Catalog{})
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				ctx := t.Context()
				at := time.Now().UTC()
				client, _, err := store.CreateClient(ctx, "Relay", at)
				if err != nil {
					t.Fatal(err)
				}
				created := proxy.Usage{At: at, Source: "api", Provider: "openai", Method: http.MethodPost,
					Endpoint: "/v1/responses", Model: "gpt-5.2", ResponseID: "fixture", Status: http.StatusOK}
				completed := proxy.Usage{At: at.Add(2 * time.Second), Source: "api", Provider: "openai", Method: http.MethodGet,
					Endpoint: "/v1/responses/:id", ResponseID: created.ResponseID, Status: http.StatusOK,
					UsageUpdate: true, HasUsage: true, InputTokens: 12, OutputTokens: 3}
				cost, listValue := 0.0, 0.25
				hasListValue := price == "list" || price == "provider estimate" || price == "queued estimate"
				if hasListValue {
					completed.ReportedListCost = &listValue
				}
				if price == "provider estimate" {
					completed.ReportedCost, completed.ReportedCostEstimated = &listValue, true
				}
				costOnly := completed
				costOnly.At = at.Add(time.Second)
				costOnly.HasUsage, costOnly.InputTokens, costOnly.OutputTokens, costOnly.ReportedCost = false, 0, 0, &cost
				costOnly.ReportedCostEstimated, costOnly.ReportedListCost = false, nil
				expectedCost := cost
				expectedInput, expectedOutput := int64(12), int64(3)
				if price == "two actual charges" || price == "metered request" {
					tokenCharge, standaloneCharge := 0.0, 0.25
					completed.ReportedCost, costOnly.ReportedCost = &tokenCharge, &standaloneCharge
					if strings.Index(order, "cost") < strings.Index(order, "tokens") {
						expectedCost = standaloneCharge
					}
				}
				if price == "metered request" {
					postCharge := 0.75
					created.ReportedCost, created.HasUsage, created.InputTokens, created.OutputTokens = &postCharge, true, 1, 1
					if strings.HasPrefix(order, "request ") {
						expectedCost = postCharge
					}
					if strings.Index(order, "request") < strings.Index(order, "tokens") {
						expectedInput, expectedOutput = 1, 1
					}
				}
				expectedRequests := int64(4)
				if price == "queued estimate" {
					quote := costOnly
					quote.ReportedCost, quote.ReportedCostEstimated = &listValue, true
					if err := store.RecordRelay(ctx, client.ID, "earlier estimate", quote); err != nil {
						t.Fatal(err)
					}
					expectedRequests++
				}
				events := map[string]proxy.Usage{"request": created, "cost": costOnly, "tokens": completed}
				for _, id := range strings.Fields(order) {
					for range 2 {
						if err := store.RecordRelay(ctx, client.ID, id, events[id]); err != nil {
							t.Fatal(err)
						}
					}
				}
				if price == "duplicate request" {
					if err := store.RecordRelay(ctx, client.ID, "another request", created); err != nil {
						t.Fatal(err)
					}
					expectedRequests++
				}
				laterCharge := 0.50
				costOnly.ReportedCost = &laterCharge
				if err := store.RecordRelay(ctx, client.ID, "later charge", costOnly); err != nil {
					t.Fatal(err)
				}
				dashboard, err := store.Dashboard(ctx, Filter{Start: at.Add(-time.Minute), End: at.Add(time.Minute), Interval: time.Minute, Client: client.ID})
				if err != nil {
					t.Fatal(err)
				}
				if dashboard.Summary.RequestCount != expectedRequests || dashboard.Summary.InputTokens != expectedInput ||
					dashboard.Summary.OutputTokens != expectedOutput || dashboard.Summary.EstimatedCost != expectedCost {
					t.Fatalf("wrong usage after both events and retries: %+v", dashboard.Summary)
				}
				for _, request := range dashboard.Requests {
					if request.Method != http.MethodPost || !request.UsageAvailable {
						continue
					}
					if request.Model != created.Model || request.Endpoint != created.Endpoint ||
						request.At.UnixMilli() != created.At.UnixMilli() || request.PriceSource != "provider-reported" ||
						request.ReportedCost == nil || *request.ReportedCost != expectedCost || request.Rates != nil {
						t.Fatalf("completion usage was not attached to the original request: %+v", request)
					}
					if hasListValue && (request.ReportedListCost == nil || *request.ReportedListCost != listValue) {
						t.Fatalf("provider charge erased the list value: %+v", request)
					}
				}
			})
		}
	}
}
