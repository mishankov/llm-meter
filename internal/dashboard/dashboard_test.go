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
