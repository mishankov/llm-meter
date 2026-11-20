package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/mishankov/llm-proxy/internal/ledger"
	"github.com/mishankov/llm-proxy/internal/pricing"
)

func TestDashboardAndProxyShareClientAttribution(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-LLM-Proxy-Key") != "" || r.Header.Get("Authorization") != "Bearer fixture-upstream-token" {
			t.Errorf("incorrect authorization handling for %s", r.URL.Path)
		}
		id := "resp_api"
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/responses/resp_background" {
			_, _ = io.WriteString(w, `{"id":"resp_background","model":"gpt-6.2-sol","status":"completed","usage":{"input_tokens":12000,"output_tokens":30,"input_tokens_details":{"cached_tokens":4000,"cache_write_tokens":7000,"cache_write_tokens_details":{"ttl_30m_tokens":3000,"ttl_12h_tokens":4000}}}}`)
			return
		}
		switch r.URL.Path {
		case "/v1/responses":
			if body, _ := io.ReadAll(r.Body); bytes.Contains(body, []byte(`"background":true`)) {
				_, _ = io.WriteString(w, `{"id":"resp_background","model":"gpt-6.2-sol","status":"queued","usage":null}`)
				return
			}
		case "/backend-api/codex/responses":
			id = "resp_codex"
		default:
			t.Errorf("unexpected upstream route %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "model": "gpt-6.2-sol", "usage": map[string]any{
				"input_tokens": 120, "input_tokens_details": map[string]int{"cached_tokens": 20}, "output_tokens": 30,
			},
		})
	}))
	defer upstream.Close()
	api, _ := url.Parse(upstream.URL + "/v1")
	codex, _ := url.Parse(upstream.URL + "/backend-api/codex")
	directory := t.TempDir()
	override := filepath.Join(directory, "prices.json")
	if err := os.WriteFile(override, []byte(`{"gpt-6.2-sol":{"input":2,"cachedInput":0.1,"output":10,"cacheWrite":2.5,"cacheWrite12h":5}}`), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := pricing.Load(override)
	if err != nil {
		t.Fatal(err)
	}
	store, err := ledger.Open(filepath.Join(directory, "test.sqlite"), catalog)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const dashboardToken = "fixture-dashboard-token-5e9ebc83"
	ui := fstest.MapFS{"index.html": {Data: []byte("dashboard shell")}}
	app := httptest.NewServer(routes(store, ui, api, codex, nil, dashboardToken, false, slog.New(slog.DiscardHandler)))
	defer app.Close()
	client := app.Client()
	client.Jar, _ = cookiejar.New(nil)

	request := func(method, path, body, key string, status int) []byte {
		t.Helper()
		r, err := http.NewRequest(method, app.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if key != "" {
			r.Header.Set("X-LLM-Proxy-Key", key)
			r.Header.Set("Authorization", "Bearer fixture-upstream-token")
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status {
			t.Fatalf("%s %s: status %d, want %d, body %q, read error %v", method, path, response.StatusCode, status, data, err)
		}
		return data
	}

	if body := request("GET", "/", "", "", 200); string(body) != "dashboard shell" {
		t.Fatalf("unexpected dashboard shell %q", body)
	}
	request("GET", "/api/usage", "", "", 401)
	request("POST", "/api/login", `{"token":"wrong"}`, "", 401)
	request("POST", "/api/login", `{"token":"`+dashboardToken+`"}`, "", 204)
	request("GET", "/api/session", "", "", 204)
	request("POST", "/v1/responses", `{"model":"gpt-6.2-sol"}`, dashboardToken, 401)
	request("POST", "/opencode-go/chat/completions", "", "", 401)

	var created struct {
		Client ledger.Client `json:"client"`
		Key    ledger.Key    `json:"key"`
	}
	body := request("POST", "/api/clients", `{"name":"Fixture Client"}`, "", 201)
	if err := json.Unmarshal(body, &created); err != nil || created.Key.Token == "" || created.Client.Keys[0].Token != "" {
		t.Fatalf("invalid client/key creation response: %v", err)
	}
	body = request("GET", "/api/clients", "", "", 200)
	if bytes.Contains(body, []byte(created.Key.Token)) || !bytes.Contains(body, []byte(created.Key.Prefix)) {
		t.Fatal("client listing must include the key prefix but only show the key at creation")
	}
	request("POST", "/v1/responses", `{"model":"gpt-6.2-sol"}`, created.Key.Token, 200)
	request("POST", "/backend-api/codex/responses", `{"model":"gpt-6.2-sol"}`, created.Key.Token, 200)

	var dashboard ledger.Dashboard
	body = request("GET", "/api/usage?client="+created.Client.ID, "", "", 200)
	if err := json.Unmarshal(body, &dashboard); err != nil {
		t.Fatal(err)
	}
	summary := dashboard.Summary
	if summary.RequestCount != 2 || summary.InputTokens != 240 || summary.CachedInputTokens != 40 || summary.OutputTokens != 60 ||
		math.Abs(summary.APICost-0.000502) > 1e-10 || math.Abs(summary.CodexValue-0.000502) > 1e-10 {
		t.Fatalf("incorrect usage summary: %+v", summary)
	}
	if len(dashboard.Requests) != 2 || dashboard.Requests[0].Client != "Fixture Client" {
		t.Fatalf("incorrect request attribution: %+v", dashboard.Requests)
	}

	var poller struct{ Key ledger.Key }
	body = request("POST", "/api/clients", `{"name":"Poller"}`, "", 201)
	if err := json.Unmarshal(body, &poller); err != nil {
		t.Fatal(err)
	}
	request("POST", "/v1/responses", `{"model":"gpt-6.2-sol","background":true}`, created.Key.Token, 200)
	request("GET", "/v1/responses/resp_background", "", poller.Key.Token, 200)
	request("GET", "/v1/responses/resp_background", "", poller.Key.Token, 200)
	body = request("GET", "/api/usage?client="+created.Client.ID, "", "", 200)
	if err := json.Unmarshal(body, &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard.Summary.InputTokens != 12240 || math.Abs(dashboard.Summary.APICost-0.030702) > 1e-10 {
		t.Fatalf("background usage was not priced exactly once on its originating client: %+v", dashboard.Summary)
	}
	if r := dashboard.Requests[0]; r.CacheWriteTokens != 7000 || r.CacheWrite12hTokens != 4000 || r.Rates == nil || r.Rates.CacheWrite12h != 5 {
		t.Fatalf("missing cache-write details: %+v", r)
	}
	request("DELETE", "/api/clients/"+created.Client.ID+"/keys/"+created.Key.ID, "", "", 204)
	request("POST", "/v1/responses", `{"model":"gpt-6.2-sol"}`, created.Key.Token, 401)
	request("POST", "/api/logout", "", "", 204)
	request("GET", "/api/clients", "", "", 401)
}
