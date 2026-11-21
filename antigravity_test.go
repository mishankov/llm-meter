package main

import (
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

	"github.com/mishankov/llm-proxy/internal/ledger"
	"github.com/mishankov/llm-proxy/internal/pricing"
)

func TestAntigravityProxyStreamsAndAttributesUsage(t *testing.T) {
	const generationPath = "/v1internal:streamGenerateContent"
	const requestBody = `{"model":"requested-model"}`
	const firstEvent = `data: {"response":{"modelVersion":"gemini-test","usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":20}}}` + "\n\n"
	const lastEvent = `data: {"traceId":"generation-1","response":{"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":20,"candidatesTokenCount":5,"thoughtsTokenCount":2}}}` + "\n\n"
	finishStream := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-LLM-Proxy-Key") != "" || r.Header.Get("Authorization") != "Bearer fixture-google-token" {
			t.Error("incorrect credential forwarding")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || r.Method != http.MethodPost || string(body) != requestBody {
			t.Error("request method or body changed")
		}
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist", "/v1internal:fetchAvailableModels", "/v1internal:retrieveUserQuotaSummary":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"usageMetadata":{"promptTokenCount":999}}`)
		case generationPath:
			if r.URL.RawQuery != "alt=sse" {
				t.Error("generation query changed")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, firstEvent)
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
	store, err := ledger.Open(filepath.Join(t.TempDir(), "test.sqlite"), pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	client, key, err := store.CreateClient(t.Context(), "Antigravity fixture", now)
	if err != nil {
		t.Fatal(err)
	}
	app := httptest.NewServer(routes(store, fstest.MapFS{}, nil, nil, nil, base, "fixture-dashboard-token-5e9ebc83", false, slog.New(slog.DiscardHandler)))
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
			r.Header.Set("X-LLM-Proxy-Key", token)
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
	post("/v1internal:loadCodeAssist", "", http.StatusUnauthorized).Body.Close()
	for _, path := range []string{"/v1internal:loadCodeAssist", "/v1internal:fetchAvailableModels", "/v1internal:retrieveUserQuotaSummary"} {
		post(path, key.Token, http.StatusOK).Body.Close()
	}
	response := post(generationPath+"?alt=sse", key.Token, http.StatusOK)
	defer response.Body.Close()
	first := make([]byte, len(firstEvent))
	if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != firstEvent {
		t.Fatalf("first event did not arrive before completion: %v", err)
	}
	finishStream <- struct{}{}
	if last, err := io.ReadAll(response.Body); err != nil || string(last) != lastEvent {
		t.Fatalf("final event changed: %v", err)
	}
	if err := store.RevokeKey(t.Context(), client.ID, key.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	post("/v1internal:loadCodeAssist", key.Token, http.StatusUnauthorized).Body.Close()
	dashboard, err := store.Dashboard(t.Context(), ledger.Filter{Start: now.Add(-time.Minute), End: now.Add(time.Minute), Interval: time.Minute, Client: client.ID})
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Summary.RequestCount != 4 || dashboard.Summary.InputTokens != 100 || dashboard.Summary.CachedInputTokens != 20 || dashboard.Summary.OutputTokens != 7 {
		t.Fatalf("incorrect Antigravity totals: %+v", dashboard.Summary)
	}
	request := dashboard.Requests[0]
	if request.Provider != "antigravity" || request.Client != client.Name || request.Model != "gemini-test" || !request.UsageAvailable {
		t.Fatalf("incorrect Antigravity request attribution: %+v", request)
	}
}
