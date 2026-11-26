package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/mishankov/llm-meter/internal/ledger"
	"github.com/mishankov/llm-meter/internal/pricing"
	"github.com/mishankov/llm-meter/internal/proxy"
	"github.com/mishankov/llm-meter/internal/usagereport"
)

func TestUsageReportsShareLedgerAndRetrySafely(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_shared","model":"fixture","usage":{"input_tokens":9,"output_tokens":1}}`)
	}))
	defer upstream.Close()
	api, _ := url.Parse(upstream.URL + "/v1")
	store, err := ledger.Open(filepath.Join(t.TempDir(), "test.sqlite"), pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := routes(store, fstest.MapFS{}, api, nil, "fixture-dashboard-token-5e9ebc83", false, slog.New(slog.DiscardHandler))
	now := time.Now().UTC()
	first, firstKey, err := store.CreateClient(t.Context(), "First relay", now)
	if err != nil {
		t.Fatal(err)
	}
	second, secondKey, err := store.CreateClient(t.Context(), "Second relay", now)
	if err != nil {
		t.Fatal(err)
	}
	post := func(path, body, key string, want int) {
		t.Helper()
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("X-LLM-Meter-Key", key)
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("POST %s: status %d, want %d", path, w.Code, want)
		}
	}
	encode := func(usage proxy.Usage) string {
		t.Helper()
		event, err := usagereport.NewEvent(usage)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	completed := proxy.Usage{At: now, Provider: "openai", Endpoint: "/v1/responses", Method: http.MethodPost,
		Status: http.StatusOK, Model: "fixture", ResponseID: "resp_shared", HasUsage: true, InputTokens: 30, OutputTokens: 3}
	payload := encode(completed)
	post(usagereport.Path, payload, "", http.StatusUnauthorized)
	post(usagereport.Path, payload, "invalid", http.StatusUnauthorized)
	post(usagereport.Path, `{"clientId":"`+second.ID+`",`+payload[1:], firstKey.Token, http.StatusBadRequest)
	post(usagereport.Path, strings.Replace(payload, `"inputTokens":30`, `"inputTokens":-1`, 1), firstKey.Token, http.StatusBadRequest)
	post(usagereport.Path, strings.Repeat(" ", usagereport.MaxBodyBytes)+payload, firstKey.Token, http.StatusBadRequest)
	post(usagereport.Path, payload, firstKey.Token, http.StatusNoContent)
	post(usagereport.Path, payload, firstKey.Token, http.StatusNoContent)
	post(usagereport.Path, payload, secondKey.Token, http.StatusNoContent)
	post("/v1/responses", `{"model":"fixture"}`, firstKey.Token, http.StatusOK)

	pending := completed
	pending.ResponseID = "background-id"
	pending.HasUsage, pending.InputTokens, pending.OutputTokens = false, 0, 0
	post(usagereport.Path, encode(pending), firstKey.Token, http.StatusNoContent)
	completion := completed
	completion.ResponseID = pending.ResponseID
	completion.Method, completion.Endpoint = http.MethodGet, "/v1/responses/background-id?secret=fixture-query"
	completion.UsageUpdate, completion.InputTokens = true, 60
	completionPayload := encode(completion)
	if strings.Contains(completionPayload, "background-id") || strings.Contains(completionPayload, "fixture-query") {
		t.Fatal("the relay event contains an original path or response identifier")
	}
	post(usagereport.Path, strings.Replace(completionPayload, "/responses/:id", "/responses/background-id", 1), firstKey.Token, http.StatusBadRequest)
	post(usagereport.Path, completionPayload, secondKey.Token, http.StatusNoContent)
	filter := ledger.Filter{Start: now.Add(-time.Minute), End: now.Add(time.Minute), Interval: time.Minute, Clients: []string{first.ID}}
	firstUsage, err := store.Dashboard(t.Context(), filter)
	if err != nil {
		t.Fatal(err)
	}
	if firstUsage.Summary.RequestCount != 3 || firstUsage.Summary.InputTokens != 39 {
		t.Fatalf("incorrect count after retries and a different client's completion: %+v", firstUsage.Summary)
	}
	post(usagereport.Path, completionPayload, firstKey.Token, http.StatusNoContent)
	firstUsage, err = store.Dashboard(t.Context(), filter)
	if err != nil {
		t.Fatal(err)
	}
	if firstUsage.Summary.RequestCount != 4 || firstUsage.Summary.InputTokens != 99 {
		t.Fatalf("incorrect count after the authenticated client reported a completion: %+v", firstUsage.Summary)
	}
	counts := map[string]int{}
	for _, request := range firstUsage.Requests {
		counts[request.MeasurementSource]++
		if request.Method == http.MethodGet && request.Endpoint != "/v1/responses/:id" {
			t.Errorf("incorrect completion operation: %q", request.Endpoint)
		}
	}
	if counts["proxy"] != 1 || counts["relay"] != 3 {
		t.Errorf("incorrect measurement sources: %v", counts)
	}
	filter.Clients = []string{second.ID}
	secondUsage, err := store.Dashboard(t.Context(), filter)
	if err != nil {
		t.Fatal(err)
	}
	if secondUsage.Summary.RequestCount != 2 || secondUsage.Summary.InputTokens != 30 {
		t.Fatalf("incorrect second client usage: %+v", secondUsage.Summary)
	}
	if err := store.RevokeKey(t.Context(), second.ID, secondKey.ID, now); err != nil {
		t.Fatal(err)
	}
	post(usagereport.Path, payload, secondKey.Token, http.StatusUnauthorized)
}

func TestLegacyRelayServiceEventsAcceptedWithoutLoggingRequests(t *testing.T) {
	store, err := ledger.Open(filepath.Join(t.TempDir(), "test.sqlite"), pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := routes(store, fstest.MapFS{}, nil, nil, "fixture-dashboard-token-5e9ebc83", false, slog.New(slog.DiscardHandler))
	now := time.Now().UTC()
	client, key, err := store.CreateClient(t.Context(), "Legacy relay", now)
	if err != nil {
		t.Fatal(err)
	}

	sendEvent := func(usage proxy.Usage) {
		t.Helper()
		event, err := usagereport.NewEvent(usage)
		if err != nil {
			t.Fatalf("NewEvent(%s %s): %v", usage.Method, usage.Endpoint, err)
		}
		body, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, usagereport.Path, strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-LLM-Meter-Key", key.Token)
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			t.Fatalf("POST %s (%s %s): status %d, want 204 (body=%s)", usagereport.Path, usage.Method, usage.Endpoint, w.Code, w.Body.String())
		}
	}

	// Simulate an older relay sending service endpoints with HTTP 200 and no usage.
	for _, usage := range []proxy.Usage{
		{At: now, Provider: "openai", Endpoint: "/v1/models", Method: http.MethodGet, Status: http.StatusOK},
		{At: now, Provider: "openai", Endpoint: "/v1/models/gpt-5.4", Method: http.MethodGet, Status: http.StatusOK},
		{At: now, Provider: "openai", Endpoint: "/v1/usage", Method: http.MethodGet, Status: http.StatusOK},
		{At: now, Provider: "openai", Endpoint: "/v1/responses", Method: http.MethodGet, Status: http.StatusOK, Model: "gpt-5.4"},
		{At: now, Provider: "google", Endpoint: "/antigravity/v1internal:loadCodeAssist", Method: http.MethodPost, Status: http.StatusOK},
		{At: now, Provider: "google", Endpoint: "/antigravity/v1internal:fetchAvailableModels", Method: http.MethodPost, Status: http.StatusOK},
		{At: now, Provider: "google", Endpoint: "/antigravity/v1internal:fetchUserInfo", Method: http.MethodPost, Status: http.StatusOK},
		{At: now, Provider: "google", Endpoint: "/antigravity/v1internal:retrieveUserQuotaSummary", Method: http.MethodPost, Status: http.StatusOK},
		{At: now, Provider: "google", Endpoint: "/antigravity/v1internal:listExperiments", Method: http.MethodPost, Status: http.StatusOK},
		{At: now, Provider: "google", Endpoint: "/antigravity/v1internal:fetchAdminControls", Method: http.MethodPost, Status: http.StatusOK},
		{At: now, Provider: "google", Endpoint: "/antigravity/v1internal:writeTrajectoryAcls", Method: http.MethodPost, Status: http.StatusOK},
	} {
		sendEvent(usage)
	}

	// A legacy service event carrying rate limits must still update allowances without creating a request row.
	sendEvent(proxy.Usage{
		At: now, Provider: "openai", Endpoint: "/v1/models", Method: http.MethodGet, Status: http.StatusOK,
		RateLimits: []proxy.RateLimit{{ID: "codex", Window: "primary", WindowMinutes: 300, UsedPercent: 25}},
	})

	// Failed and successful generation events must still be persisted.
	generationEvents := []proxy.Usage{
		{At: now, Provider: "openai", Endpoint: "/v1/responses", Method: http.MethodPost, Status: http.StatusTooManyRequests, Model: "gpt-5.4"},
		{At: now, Provider: "openai", Endpoint: "/v1/chat/completions", Method: http.MethodPost, Status: http.StatusInternalServerError, Model: "gpt-5.4"},
		{At: now, Provider: "google", Endpoint: "/antigravity/v1internal:generateContent", Method: http.MethodPost, Status: http.StatusBadRequest, Model: "gemini-4-neon"},
		{At: now, Provider: "google", Endpoint: "/antigravity/v1internal:streamGenerateContent", Method: http.MethodPost, Status: http.StatusOK, Model: "gemini-4-neon", HasUsage: true, InputTokens: 50, OutputTokens: 10},
	}
	for _, usage := range generationEvents {
		sendEvent(usage)
	}

	view, err := store.Dashboard(t.Context(), ledger.Filter{
		Start: now.Add(-time.Minute), End: now.Add(time.Minute), Interval: time.Minute, Clients: []string{client.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Summary.RequestCount != int64(len(generationEvents)) || view.Summary.InputTokens != 50 || view.Summary.OutputTokens != 10 {
		t.Fatalf("unexpected summary after legacy service + generation events: %+v", view.Summary)
	}
	if len(view.Allowance) != 1 || view.Allowance[0].RemainingPercent != 75 {
		t.Fatalf("unexpected allowance after legacy service event with rate limits: %+v", view.Allowance)
	}
}
