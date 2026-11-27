package dashboard

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mishankov/llm-meter/internal/ledger"
	"github.com/mishankov/llm-meter/internal/pricing"
	"github.com/mishankov/llm-meter/internal/proxy"
)

func TestUsageEndpointsCombineSelections(t *testing.T) {
	ctx := t.Context()
	store, err := ledger.Open(filepath.Join(t.TempDir(), "test.sqlite"), pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	newClient := func(name string) string {
		t.Helper()
		client, _, err := store.CreateClient(ctx, name, now.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		quota := proxy.RateLimit{ID: "codex", Window: "primary", WindowMinutes: 300, UsedPercent: 20}
		if err := store.Record(ctx, proxy.Usage{At: now, ClientID: client.ID, RateLimits: []proxy.RateLimit{quota}}); err != nil {
			t.Fatal(err)
		}
		return client.ID
	}
	first, second, excluded := newClient("First"), newClient("Second"), newClient("Excluded")
	const literalModel = "model, revision + café"
	type dimensions struct{ source, client, model string }
	fixtures := []dimensions{
		{"api", first, literalModel},
		{"codex", second, "model/codex"},
		{"api", second, "model/other"},
		{"codex", excluded, "model/codex"},
	}
	for i, fixture := range fixtures {
		if err := store.Record(ctx, proxy.Usage{
			At: now.Add(-time.Duration(i+1) * time.Minute), Source: fixture.source, ClientID: fixture.client, Model: fixture.model,
			Endpoint: "/responses", Method: http.MethodPost, Status: http.StatusOK,
		}); err != nil {
			t.Fatal(err)
		}
	}

	handler := New(store, "dashboard-token", false, slog.New(slog.DiscardHandler))
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"token":"dashboard-token"}`)))
	cookies := login.Result().Cookies()
	if login.Code != http.StatusNoContent || len(cookies) != 1 {
		t.Fatalf("login failed with %d", login.Code)
	}
	query := url.Values{
		"source": {"api", "codex", "api"},
		"client": {first, "", second},
		"model":  {literalModel, "model/codex"},
	}
	get := func(t *testing.T, path string, query url.Values, status int) []byte {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path+"?"+query.Encode(), nil)
		request.AddCookie(cookies[0])
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("%s status = %d, want %d", path, response.Code, status)
		}
		return response.Body.Bytes()
	}
	for _, path := range []string{"/api/usage", "/api/usage/requests"} {
		t.Run(path, func(t *testing.T) {
			var result ledger.Dashboard
			if err := json.Unmarshal(get(t, path, query, http.StatusOK), &result); err != nil {
				t.Fatal(err)
			}
			var filtered []dimensions
			for _, request := range result.Requests {
				filtered = append(filtered, dimensions{request.Source, request.ClientID, request.Model})
			}
			if !slices.Equal(filtered, fixtures[:2]) {
				t.Errorf("filtered requests = %+v, want %+v", filtered, fixtures[:2])
			}
			if path == "/api/usage" && (result.Summary.RequestCount != 2 || len(result.Allowance) != 2) {
				t.Errorf("filtered summary count = %d, allowances = %d, want two of each", result.Summary.RequestCount, len(result.Allowance))
			}
			get(t, path, url.Values{"source": {"api", "invalid"}}, http.StatusBadRequest)
			get(t, path, url.Values{"client": {first, strings.Repeat("a", 101)}}, http.StatusBadRequest)
			get(t, path, url.Values{"model": {literalModel, strings.Repeat("a", 501)}}, http.StatusBadRequest)
		})
	}
	var apiOnly ledger.Dashboard
	if err := json.Unmarshal(get(t, "/api/usage", url.Values{"source": {"api", ""}}, http.StatusOK), &apiOnly); err != nil {
		t.Fatal(err)
	}
	if apiOnly.Summary.RequestCount != 2 || len(apiOnly.Allowance) != 0 {
		t.Errorf("API-only summary count = %d, allowances = %d, want 2 and 0", apiOnly.Summary.RequestCount, len(apiOnly.Allowance))
	}
}

func TestUsageEndpointsFilterAndGroupReasoningLevels(t *testing.T) {
	ctx := t.Context()
	store, err := ledger.Open(filepath.Join(t.TempDir(), "test.sqlite"), pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	proxyClient, _, err := store.CreateClient(ctx, "Work Laptop", now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	otlpClient, _, err := store.CreateClient(ctx, "Claude Code", now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for i, req := range []proxy.Usage{
		{At: now.Add(-4 * time.Minute), Source: "api", Provider: "openai", ClientID: proxyClient.ID, Model: "gpt-5.4", ReasoningLevel: "high", Endpoint: "/v1/responses", Method: http.MethodPost, Status: http.StatusOK, HasUsage: true, InputTokens: 100, OutputTokens: 20},
		{At: now.Add(-3 * time.Minute), Source: "api", Provider: "openai", ClientID: proxyClient.ID, Model: "gpt-5.4", ReasoningLevel: "medium", Endpoint: "/v1/responses", Method: http.MethodPost, Status: http.StatusOK, HasUsage: true, InputTokens: 50, OutputTokens: 10},
		{At: now.Add(-2 * time.Minute), Source: "api", Provider: "anthropic", ClientID: otlpClient.ID, Model: "claude-sonnet-4-6", ReasoningLevel: "", Endpoint: "/claude-code/api-request", Method: http.MethodPost, Status: http.StatusOK, HasUsage: true, InputTokens: 200, OutputTokens: 30},
	} {
		if err := store.Record(ctx, req); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	handler := New(store, "dashboard-token", false, slog.New(slog.DiscardHandler))
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"token":"dashboard-token"}`)))
	cookie := login.Result().Cookies()[0]

	fetch := func(t *testing.T, rawQuery string, wantStatus int) ledger.Dashboard {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/usage?"+rawQuery, nil)
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != wantStatus {
			t.Fatalf("status = %d, want %d (%s)", w.Code, wantStatus, w.Body.String())
		}
		var out ledger.Dashboard
		if wantStatus == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}

	all := fetch(t, "range=24h", http.StatusOK)
	if !slices.Equal(all.Filters.Reasoning, []string{"high", "medium", ledger.UnreportedReasoning}) {
		t.Fatalf("filters.reasoning = %v", all.Filters.Reasoning)
	}
	if len(all.Groups.Reasoning) != 3 || all.Groups.Reasoning[2].ID != ledger.UnreportedReasoning || all.Groups.Reasoning[2].Name != "Not reported" {
		t.Fatalf("groups.reasoning = %+v", all.Groups.Reasoning)
	}
	for _, clientGroup := range all.Groups.Clients {
		if clientGroup.ID == proxyClient.ID && clientGroup.Transport != "Proxy" {
			t.Fatalf("proxy client transport = %q, want Proxy", clientGroup.Transport)
		}
		if clientGroup.ID == otlpClient.ID && clientGroup.Transport != "OTLP" {
			t.Fatalf("otlp client transport = %q, want OTLP", clientGroup.Transport)
		}
	}

	highOnly := fetch(t, "range=24h&reasoning=high", http.StatusOK)
	if highOnly.Summary.RequestCount != 1 || len(highOnly.Requests) != 1 || highOnly.Requests[0].ReasoningLevel != "high" {
		t.Fatalf("high filter mismatch: summary=%+v reqs=%+v", highOnly.Summary, highOnly.Requests)
	}

	unreportedOnly := fetch(t, "range=24h&reasoning=unreported", http.StatusOK)
	if unreportedOnly.Summary.RequestCount != 1 || len(unreportedOnly.Requests) != 1 || unreportedOnly.Requests[0].ReasoningLevel != "" {
		t.Fatalf("unreported filter mismatch: summary=%+v reqs=%+v", unreportedOnly.Summary, unreportedOnly.Requests)
	}

	combined := fetch(t, "range=24h&reasoning=high&reasoning=unreported", http.StatusOK)
	if combined.Summary.RequestCount != 2 {
		t.Fatalf("combined reasoning filter count = %d, want 2", combined.Summary.RequestCount)
	}

	fetch(t, "range=24h&reasoning="+strings.Repeat("x", 65), http.StatusBadRequest)
}

func TestUsageSupports1hRangeCacheSavingsAndProviders(t *testing.T) {
	ctx := t.Context()
	store, err := ledger.Open(filepath.Join(t.TempDir(), "test.sqlite"), pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Truncate(time.Minute)
	client, _, err := store.CreateClient(ctx, "Live Client", now.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for i, req := range []proxy.Usage{
		{
			At: now.Add(-10 * time.Minute), Source: "api", Provider: "openai", ClientID: client.ID,
			Model: "gpt-5.4", ReasoningLevel: "high", Endpoint: "/v1/responses", Method: http.MethodPost,
			Status: http.StatusOK, HasUsage: true, InputTokens: 200_000, CachedInputTokens: 100_000, OutputTokens: 10_000,
		},
		{
			At: now.Add(-5 * time.Minute), Source: "codex", Provider: "openai", ClientID: client.ID,
			Model: "gpt-5.3-codex", Endpoint: "/codex/telemetry", Method: http.MethodPost,
			Status: http.StatusOK, HasUsage: true, InputTokens: 50_000, CachedInputTokens: 0, OutputTokens: 5_000,
		},
	} {
		if err := store.Record(ctx, req); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	handler := New(store, "dashboard-token", false, slog.New(slog.DiscardHandler))
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"token":"dashboard-token"}`)))
	cookie := login.Result().Cookies()[0]

	req := httptest.NewRequest(http.MethodGet, "/api/usage?range=1h", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var out ledger.Dashboard
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Buckets) < 12 || len(out.Buckets) > 13 {
		t.Fatalf("1h bucket count = %d, want 12 or 13", len(out.Buckets))
	}
	if out.Summary.CacheSavings <= 0 {
		t.Fatalf("summary.cacheSavings = %v, want > 0", out.Summary.CacheSavings)
	}
	if len(out.Groups.Providers) != 2 {
		t.Fatalf("groups.providers = %+v, want 2 providers (Codex subscription + OpenAI API)", out.Groups.Providers)
	}
	if len(out.Filters.Providers) != 2 {
		t.Fatalf("filters.providers = %+v, want 2 provider filter options", out.Filters.Providers)
	}

	// Filtering by provider=openai returns only the OpenAI API request (not codex)
	reqOpenAI := httptest.NewRequest(http.MethodGet, "/api/usage?range=1h&provider=openai", nil)
	reqOpenAI.AddCookie(cookie)
	wOpenAI := httptest.NewRecorder()
	handler.ServeHTTP(wOpenAI, reqOpenAI)
	if wOpenAI.Code != http.StatusOK {
		t.Fatalf("provider=openai status = %d", wOpenAI.Code)
	}
	var openAIOut ledger.Dashboard
	if err := json.Unmarshal(wOpenAI.Body.Bytes(), &openAIOut); err != nil {
		t.Fatal(err)
	}
	if openAIOut.Summary.RequestCount != 1 || openAIOut.Requests[0].Model != "gpt-5.4" {
		t.Fatalf("provider=openai mismatch: %+v", openAIOut.Requests)
	}

	// Filtering by provider=codex returns only the Codex subscription request
	reqCodex := httptest.NewRequest(http.MethodGet, "/api/usage?range=1h&provider=codex", nil)
	reqCodex.AddCookie(cookie)
	wCodex := httptest.NewRecorder()
	handler.ServeHTTP(wCodex, reqCodex)
	if wCodex.Code != http.StatusOK {
		t.Fatalf("provider=codex status = %d", wCodex.Code)
	}
	var codexOut ledger.Dashboard
	if err := json.Unmarshal(wCodex.Body.Bytes(), &codexOut); err != nil {
		t.Fatal(err)
	}
	if codexOut.Summary.RequestCount != 1 || codexOut.Requests[0].Model != "gpt-5.3-codex" {
		t.Fatalf("provider=codex mismatch: %+v", codexOut.Requests)
	}
}

func TestUsageSupportsCustomDateTimeRange(t *testing.T) {
	ctx := t.Context()
	store, err := ledger.Open(filepath.Join(t.TempDir(), "test.sqlite"), pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	client, _, err := store.CreateClient(ctx, "Range Client", base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for i, at := range []time.Time{
		base.Add(-time.Minute),  // before from (excluded)
		base,                    // exact from (included)
		base.Add(45 * time.Minute),
		base.Add(2*time.Hour - time.Second), // inside [from, to)
		base.Add(2 * time.Hour),             // exact to (excluded: half-open)
	} {
		if err := store.Record(ctx, proxy.Usage{
			At: at, Source: "api", Provider: "openai", ClientID: client.ID,
			Model: "gpt-5.4", Endpoint: "/v1/responses", Method: http.MethodPost,
			Status: http.StatusOK, HasUsage: true, InputTokens: 100, OutputTokens: 20,
		}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	handler := New(store, "dashboard-token", false, slog.New(slog.DiscardHandler))
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"token":"dashboard-token"}`)))
	cookie := login.Result().Cookies()[0]

	get := func(rawQuery string, wantStatus int) ledger.Dashboard {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/usage?"+rawQuery, nil)
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != wantStatus {
			t.Fatalf("%s status = %d, want %d (%s)", rawQuery, w.Code, wantStatus, w.Body.String())
		}
		var out ledger.Dashboard
		if wantStatus == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}

	q := url.Values{
		"from": {base.Format(time.RFC3339)},
		"to":   {base.Add(2 * time.Hour).Format(time.RFC3339)},
	}
	res := get(q.Encode(), http.StatusOK)
	if res.Summary.RequestCount != 3 {
		t.Fatalf("custom range requestCount = %d, want 3 (half-open [from, to))", res.Summary.RequestCount)
	}
	if res.BucketSeconds != 300 {
		t.Fatalf("2h custom range bucketSeconds = %d, want 300 (5m)", res.BucketSeconds)
	}
	if len(res.Buckets) != 24 {
		t.Fatalf("2h custom range bucket count = %d, want 24", len(res.Buckets))
	}

	// Validation errors
	get("from="+base.Format(time.RFC3339), http.StatusBadRequest)
	get("to="+base.Format(time.RFC3339), http.StatusBadRequest)
	get("from="+base.Format(time.RFC3339)+"&to="+base.Add(-time.Minute).Format(time.RFC3339), http.StatusBadRequest)
	get("from="+base.Format(time.RFC3339)+"&to="+base.AddDate(2, 0, 0).Format(time.RFC3339), http.StatusBadRequest)
}
