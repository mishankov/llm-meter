package ledger

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mishankov/llm-meter/internal/pricing"
	"github.com/mishankov/llm-meter/internal/proxy"
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
	filter := Filter{Start: at.Add(-time.Minute), End: at.Add(time.Minute), Interval: time.Minute, Clients: []string{client.ID}}
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
				dashboard, err := store.Dashboard(ctx, Filter{Start: at.Add(-time.Minute), End: at.Add(time.Minute), Interval: time.Minute, Clients: []string{client.ID}})
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

func TestRelayCompletionReasoningOverridesRequestInAnyOrder(t *testing.T) {
	for _, postMetered := range []bool{false, true} {
		for _, completionFirst := range []bool{false, true} {
			name := "unmetered-post/post-first"
			if postMetered && completionFirst {
				name = "metered-post/completion-first"
			} else if postMetered {
				name = "metered-post/post-first"
			} else if completionFirst {
				name = "unmetered-post/completion-first"
			}
			t.Run(name, func(t *testing.T) {
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
				post := proxy.Usage{
					At: at, Source: "api", Provider: "openai", Method: http.MethodPost,
					Endpoint: "/v1/responses", Model: "gpt-5.4", ReasoningLevel: "low", ResponseID: "resp-order", Status: http.StatusOK,
				}
				if postMetered {
					post.HasUsage, post.InputTokens, post.OutputTokens = true, 20, 5
				}
				completion := proxy.Usage{
					At: at.Add(time.Second), Source: "api", Provider: "openai", Method: http.MethodGet,
					Endpoint: "/v1/responses/:id", Model: "gpt-5.4", ReasoningLevel: "high", ResponseID: "resp-order", Status: http.StatusOK,
					UsageUpdate: true, HasUsage: true, InputTokens: 20, OutputTokens: 5,
				}
				if completionFirst {
					if err := store.RecordRelay(ctx, client.ID, "evt-complete", completion); err != nil {
						t.Fatal(err)
					}
					if err := store.RecordRelay(ctx, client.ID, "evt-post", post); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := store.RecordRelay(ctx, client.ID, "evt-post", post); err != nil {
						t.Fatal(err)
					}
					if err := store.RecordRelay(ctx, client.ID, "evt-complete", completion); err != nil {
						t.Fatal(err)
					}
				}
				dashboard, err := store.Dashboard(ctx, Filter{Start: at.Add(-time.Minute), End: at.Add(time.Minute), Interval: time.Minute, Clients: []string{client.ID}})
				if err != nil {
					t.Fatal(err)
				}
				var got Request
				for _, req := range dashboard.Requests {
					if req.Method == http.MethodPost {
						got = req
					}
				}
				if got.ReasoningLevel != "high" || !got.UsageAvailable || got.InputTokens != 20 || got.OutputTokens != 5 {
					t.Fatalf("persisted POST request = %+v, want high with 20/5 tokens", got)
				}
			})
		}
	}
}

func TestRelayQueuedTokenAndCostCompletionsPreserveReasoningLevel(t *testing.T) {
	orders := []string{
		"post tokens cost",
		"post cost tokens",
		"tokens cost post",
		"cost tokens post",
		"tokens post cost",
		"cost post tokens",
	}
	for _, postMetered := range []bool{false, true} {
		mode := "unmetered-post"
		if postMetered {
			mode = "metered-post"
		}
		for _, order := range orders {
			t.Run(mode+"/"+order, func(t *testing.T) {
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
				post := proxy.Usage{
					At: at, Source: "api", Provider: "openai", Method: http.MethodPost,
					Endpoint: "/v1/responses", Model: "gpt-5.4", ReasoningLevel: "low", ResponseID: "resp-split", Status: http.StatusOK,
				}
				if postMetered {
					post.HasUsage, post.InputTokens, post.OutputTokens = true, 20, 5
				}
				tokens := proxy.Usage{
					At: at.Add(time.Second), Source: "api", Provider: "openai", Method: http.MethodGet,
					Endpoint: "/v1/responses/:id", Model: "gpt-5.4", ResponseID: "resp-split", Status: http.StatusOK,
					UsageUpdate: true, HasUsage: true, InputTokens: 20, OutputTokens: 5,
				}
				charge := 0.12
				cost := proxy.Usage{
					At: at.Add(2 * time.Second), Source: "api", Provider: "openai", Method: http.MethodGet,
					Endpoint: "/v1/responses/:id", Model: "gpt-5.4", ReasoningLevel: "high", ResponseID: "resp-split", Status: http.StatusOK,
					UsageUpdate: true, ReportedCost: &charge,
				}
				events := map[string]proxy.Usage{"post": post, "tokens": tokens, "cost": cost}
				for _, id := range strings.Fields(order) {
					if err := store.RecordRelay(ctx, client.ID, "evt-"+id, events[id]); err != nil {
						t.Fatal(err)
					}
				}
				dashboard, err := store.Dashboard(ctx, Filter{Start: at.Add(-time.Minute), End: at.Add(time.Minute), Interval: time.Minute, Clients: []string{client.ID}})
				if err != nil {
					t.Fatal(err)
				}
				var got Request
				for _, req := range dashboard.Requests {
					if req.Method == http.MethodPost {
						got = req
					}
				}
				if got.ReasoningLevel != "high" || !got.UsageAvailable || got.InputTokens != 20 || got.OutputTokens != 5 ||
					got.ReportedCost == nil || *got.ReportedCost != charge {
					t.Fatalf("persisted POST request = %+v, want high with 20/5 tokens and cost %.2f", got, charge)
				}
			})
		}
	}
}

func TestServiceRequestsSkippedWhilePreservingRateLimits(t *testing.T) {
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

	serviceEvents := []proxy.Usage{
		{At: at, Source: "api", Provider: "openai", Endpoint: "/v1/models", Method: http.MethodGet, Status: http.StatusOK},
		{At: at, Source: "api", Provider: "openai", Endpoint: "/v1/models/:id", Method: http.MethodGet, Status: http.StatusOK},
		{At: at, Source: "api", Provider: "openai", Endpoint: "/v1/usage", Method: http.MethodGet, Status: http.StatusOK},
		{At: at, Source: "api", Provider: "openai", Endpoint: "/v1/responses", Method: http.MethodGet, Status: http.StatusOK, Model: "gpt-5.4"},
		{At: at, Source: "api", Provider: "google", Endpoint: "/antigravity/v1internal:loadCodeAssist", Method: http.MethodPost, Status: http.StatusOK},
		{At: at, Source: "api", Provider: "google", Endpoint: "/antigravity/v1internal:fetchAvailableModels", Method: http.MethodPost, Status: http.StatusOK},
		{At: at, Source: "api", Provider: "google", Endpoint: "/antigravity/v1internal:fetchUserInfo", Method: http.MethodPost, Status: http.StatusOK},
		{At: at, Source: "api", Provider: "google", Endpoint: "/antigravity/v1internal:retrieveUserQuotaSummary", Method: http.MethodPost, Status: http.StatusOK},
		{At: at, Source: "api", Provider: "google", Endpoint: "/antigravity/v1internal:listExperiments", Method: http.MethodPost, Status: http.StatusOK},
		{At: at, Source: "api", Provider: "google", Endpoint: "/antigravity/v1internal:fetchAdminControls", Method: http.MethodPost, Status: http.StatusOK},
		{At: at, Source: "api", Provider: "google", Endpoint: "/antigravity/v1internal:writeTrajectoryAcls", Method: http.MethodPost, Status: http.StatusOK},
	}
	for i, usage := range serviceEvents {
		if err := store.RecordRelay(ctx, client.ID, "svc-"+strings.Repeat("a", i+1), usage); err != nil {
			t.Fatalf("RecordRelay service %d: %v", i, err)
		}
	}

	var relayEventCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM relay_events`).Scan(&relayEventCount); err != nil {
		t.Fatal(err)
	}
	if relayEventCount != 0 {
		t.Fatalf("service events without rate limits wrote %d rows to relay_events, want 0", relayEventCount)
	}

	// A service event carrying rate limits must still update allowances without adding a request row.
	withQuota := proxy.Usage{
		At: at, Source: "api", Provider: "openai", Endpoint: "/v1/models", Method: http.MethodGet, Status: http.StatusOK,
		RateLimits: []proxy.RateLimit{{ID: "codex", Window: "primary", WindowMinutes: 300, UsedPercent: 35}},
	}
	if err := store.RecordRelay(ctx, client.ID, "svc-with-quota", withQuota); err != nil {
		t.Fatalf("RecordRelay service with quota: %v", err)
	}

	// Live proxy records (including rejected non-4xx WebSocket handshakes) must not be reclassified by Record.
	rejectedHandshake := proxy.Usage{
		At: at, Source: "api", Provider: "openai", ClientID: client.ID,
		Endpoint: "/v1/responses", Method: http.MethodGet, Status: http.StatusFound,
	}
	if err := store.Record(ctx, rejectedHandshake); err != nil {
		t.Fatalf("Record live rejected WebSocket handshake: %v", err)
	}

	// Generation requests (including 4xx/5xx, Gemini POST /v1/models/:id, WebSocket) must still be recorded.
	generationEvents := []proxy.Usage{
		{At: at, Source: "api", Provider: "openai", Endpoint: "/v1/responses", Method: http.MethodPost, Status: http.StatusTooManyRequests, Model: "gpt-5.4"},
		{At: at, Source: "api", Provider: "openai", Endpoint: "/v1/chat/completions", Method: http.MethodPost, Status: http.StatusInternalServerError, Model: "gpt-5.4"},
		{At: at, Source: "api", Provider: "google", Endpoint: "/v1/models/:id", Method: http.MethodPost, Status: http.StatusOK, Model: "gemini-2.5-pro", HasUsage: true, InputTokens: 10, OutputTokens: 5},
		{At: at, Source: "api", Provider: "google", Endpoint: "/antigravity/v1internal:streamGenerateContent", Method: http.MethodPost, Status: http.StatusServiceUnavailable, Model: "gemini-4-neon"},
		{At: at, Source: "api", Provider: "openai", Endpoint: "/v1/responses", Method: http.MethodGet, Status: http.StatusSwitchingProtocols, Model: "gpt-ws", HasUsage: true, InputTokens: 12, OutputTokens: 3},
	}
	for i, usage := range generationEvents {
		if err := store.RecordRelay(ctx, client.ID, "gen-"+strings.Repeat("b", i+1), usage); err != nil {
			t.Fatalf("RecordRelay generation %d: %v", i, err)
		}
	}

	dashboard, err := store.Dashboard(ctx, Filter{Start: at.Add(-time.Minute), End: at.Add(time.Minute), Interval: time.Minute, Clients: []string{client.ID}})
	if err != nil {
		t.Fatal(err)
	}
	wantCount := int64(len(generationEvents) + 1)
	if dashboard.Summary.RequestCount != wantCount {
		t.Fatalf("RequestCount = %d, want %d (requests=%+v)", dashboard.Summary.RequestCount, wantCount, dashboard.Requests)
	}
	if len(dashboard.Allowance) != 1 || dashboard.Allowance[0].RemainingPercent != 65 {
		t.Fatalf("allowance from service event was not saved: %+v", dashboard.Allowance)
	}
}
