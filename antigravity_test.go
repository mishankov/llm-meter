package main

import (
	"io"
	"log/slog"
	"math"
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
)

func TestAntigravityProxyStreamsAndAttributesUsage(t *testing.T) {
	const generationPath = "/v1internal:streamGenerateContent"
	const requestBody = `{"model":"gemini-3.5-flash-lite"}`
	const firstEvents = `data: {"response":{"responseId":"generation-1","modelVersion":"gemini-4-neon"}}` + "\n\n" +
		`data: {"response":{"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":20}}}` + "\n\n"
	const lastEvent = `data: {"traceId":"shared-trace","response":{"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":20,"candidatesTokenCount":5,"thoughtsTokenCount":2}}}` + "\n\n"
	const accountResponse = `{"usageMetadata":{"promptTokenCount":999}}`
	const unaryResponse = `{"traceId":"shared-trace","response":{"responseId":"generation-2","usageMetadata":{"promptTokenCount":25,"cachedContentTokenCount":5,"toolUsePromptTokenCount":5,"candidatesTokenCount":3}}}`
	finishStream := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-LLM-Meter-Key") != "" || r.Header.Get("Authorization") != "Bearer fixture-google-token" {
			t.Error("incorrect credential forwarding")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || r.Method != http.MethodPost || string(body) != requestBody {
			t.Error("request method or body changed")
		}
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist", "/v1internal:fetchAvailableModels", "/v1internal:retrieveUserQuotaSummary":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, accountResponse)
		case "/v1internal:generateContent":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, unaryResponse)
		case generationPath:
			if r.URL.RawQuery != "alt=sse" {
				t.Error("generation query changed")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, firstEvents)
			w.(http.Flusher).Flush()
			select {
			case <-finishStream:
				_, _ = io.WriteString(w, lastEvent)
			case <-r.Context().Done():
			}
		default:
			t.Errorf("unexpected native path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	priceFeed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"google":{"models":{
			"gemini-4-neon":{"cost":{"input":3,"input_audio":3,"cache_read":0.15,"output":15}},
			"gemini-3.5-flash-lite":{"cost":{"input":0.3,"cache_read":0.03,"output":2.5}}
		}}}`)
	}))
	defer priceFeed.Close()
	snapshot, err := pricing.Fetch(t.Context(), priceFeed.Client(), pricing.Feed{Name: "models.dev", URL: priceFeed.URL})
	if err != nil {
		t.Fatal(err)
	}
	var catalog pricing.Catalog
	catalog.SetSnapshot(snapshot)
	store, err := ledger.Open(filepath.Join(t.TempDir(), "test.sqlite"), catalog)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	client, key, err := store.CreateClient(t.Context(), "Antigravity fixture", now)
	if err != nil {
		t.Fatal(err)
	}
	app := httptest.NewServer(routes(store, fstest.MapFS{}, nil, base, "fixture-dashboard-token-5e9ebc83", false, slog.New(slog.DiscardHandler)))
	defer app.Close()
	httpClient := app.Client()
	httpClient.Timeout = 5 * time.Second
	post := func(path, token string, status int) *http.Response {
		t.Helper()
		r, err := http.NewRequest(http.MethodPost, app.URL+"/antigravity"+path, strings.NewReader(requestBody))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer fixture-google-token")
		if token != "" {
			r.Header.Set("X-LLM-Meter-Key", token)
		}
		response, err := httpClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != status {
			response.Body.Close()
			t.Fatalf("%s: status %d, want %d", path, response.StatusCode, status)
		}
		return response
	}
	assertBody := func(response *http.Response, want string) {
		t.Helper()
		defer response.Body.Close()
		if body, err := io.ReadAll(response.Body); err != nil || string(body) != want {
			t.Fatalf("response body changed: %v", err)
		}
	}
	post("/v1internal:loadCodeAssist", "", http.StatusUnauthorized).Body.Close()
	for _, path := range []string{"/v1internal:loadCodeAssist", "/v1internal:fetchAvailableModels", "/v1internal:retrieveUserQuotaSummary"} {
		assertBody(post(path, key.Token, http.StatusOK), accountResponse)
	}
	for range 2 {
		response := post(generationPath+"?alt=sse", key.Token, http.StatusOK)
		first := make([]byte, len(firstEvents))
		if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != firstEvents {
			response.Body.Close()
			t.Fatalf("first events did not arrive before completion: %v", err)
		}
		finishStream <- struct{}{}
		assertBody(response, lastEvent)
	}
	assertBody(post("/v1internal:generateContent", key.Token, http.StatusOK), unaryResponse)
	if err := store.RevokeKey(t.Context(), client.ID, key.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	post("/v1internal:loadCodeAssist", key.Token, http.StatusUnauthorized).Body.Close()
	app.Close()
	dashboard, err := store.Dashboard(t.Context(), ledger.Filter{Start: now.Add(-time.Minute), End: now.Add(time.Minute), Interval: time.Minute, Clients: []string{client.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Summary.RequestCount != 2 || dashboard.Summary.PricedRequestCount != 2 || dashboard.Summary.InputTokens != 130 || dashboard.Summary.CachedInputTokens != 25 || dashboard.Summary.OutputTokens != 10 {
		t.Fatalf("incorrect Antigravity totals: %+v", dashboard.Summary)
	}
	if len(dashboard.Requests) != 2 {
		t.Fatalf("got %d Antigravity requests, want 2: %+v", len(dashboard.Requests), dashboard.Requests)
	}
	for i, expected := range []struct {
		model string
		cost  float64
	}{
		{"gemini-3.5-flash-lite", .00001515},
		{"gemini-4-neon", .000348},
	} {
		request := dashboard.Requests[i]
		if request.Provider != "google" || request.Client != client.Name || request.Model != expected.model || !request.UsageAvailable {
			t.Fatalf("incorrect Antigravity request attribution: %+v", request)
		}
		if request.PriceSource != "models.dev" || request.Cost == nil || math.Abs(*request.Cost-expected.cost) > 1e-12 {
			t.Fatalf("incorrect pricing for %s: %+v", expected.model, request)
		}
	}
}
