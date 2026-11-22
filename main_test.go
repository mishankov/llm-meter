package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
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
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/mishankov/llm-meter/internal/ledger"
	"github.com/mishankov/llm-meter/internal/pricing"
)

func TestDashboardServesPrecompressedAssets(t *testing.T) {
	const asset = "/_app/immutable/chunks/dashboard.js"
	const legacyAsset = "/_app/immutable/chunks/legacy.js"
	source := []byte(strings.Repeat("export const active = true;\n", 100))
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(source); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	handler := serveDashboard(fstest.MapFS{
		asset[1:]:         {Data: source},
		asset[1:] + ".gz": {Data: compressed.Bytes()},
		legacyAsset[1:]:   {Data: source},
	})

	for _, test := range []struct {
		name, method, path, accept, byteRange string
		status                                int
		zipped                                bool
	}{
		{"gzip GET", "GET", asset, "gzip, br", "", http.StatusOK, true},
		{"gzip HEAD", "HEAD", asset, "gzip", "", http.StatusOK, true},
		{"gzip quality", "GET", asset, "gzip;q=0.5, identity;q=0.1", "", http.StatusOK, true},
		{"wildcard", "GET", asset, "*;q=0.5", "", http.StatusOK, true},
		{"identity preferred", "GET", asset, "gzip;q=0.5, identity;q=1", "", http.StatusOK, false},
		{"gzip excluded", "GET", asset, "gzip;q=0, *;q=1", "", http.StatusOK, false},
		{"no accept encoding", "GET", asset, "", "", http.StatusOK, false},
		{"legacy build", "GET", legacyAsset, "gzip", "", http.StatusOK, false},
		{"gzip byte range", "GET", asset, "gzip", "bytes=0-5", http.StatusPartialContent, true},
		{"gzip multiple ranges return the full representation", "GET", asset, "gzip", "bytes=0-5,10-15", http.StatusOK, true},
		{"identity byte range", "GET", asset, "identity", "bytes=0-5", http.StatusPartialContent, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			if test.accept != "" {
				request.Header.Set("Accept-Encoding", test.accept)
			}
			if test.byteRange != "" {
				request.Header.Set("Range", test.byteRange)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status || (response.Header().Get("Content-Encoding") == "gzip") != test.zipped {
				t.Fatalf("status = %d, encoding = %q", response.Code, response.Header().Get("Content-Encoding"))
			}
			if response.Header().Get("Vary") != "Accept-Encoding" || !strings.Contains(response.Header().Get("Cache-Control"), "immutable") {
				t.Errorf("unexpected cache headers: %v", response.Header())
			}
			if !strings.HasPrefix(response.Header().Get("Content-Type"), "text/javascript") {
				t.Errorf("content type = %q", response.Header().Get("Content-Type"))
			}
			want := source
			if test.zipped {
				want = compressed.Bytes()
			}
			if test.status == http.StatusPartialContent {
				expectedRange := "bytes 0-5/" + strconv.Itoa(len(want))
				if response.Header().Get("Content-Range") != expectedRange {
					t.Errorf("content range = %q, want %q", response.Header().Get("Content-Range"), expectedRange)
				}
				want = want[:6]
			}
			if response.Header().Get("Content-Length") != strconv.Itoa(len(want)) {
				t.Errorf("incorrect content length: %q", response.Header().Get("Content-Length"))
			}
			if test.method == "HEAD" {
				want = nil
			}
			if !bytes.Equal(response.Body.Bytes(), want) {
				t.Errorf("response byte length = %d, want %d", response.Body.Len(), len(want))
			}
		})
	}
}

func TestDashboardAndProxyShareClientAttribution(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-LLM-Meter-Key") != "" || r.Header.Get("Authorization") != "Bearer fixture-upstream-token" {
			t.Errorf("incorrect authorization handling for %s", r.URL.Path)
		}
		id, outputTokens := "resp_api", 30
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
			outputTokens = 40
		default:
			t.Errorf("unexpected upstream route %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "model": "gpt-6.2-sol", "usage": map[string]any{
				"input_tokens": 120, "input_tokens_details": map[string]int{"cached_tokens": 20}, "output_tokens": outputTokens,
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
	app := httptest.NewServer(routes(store, ui, api, codex, nil, nil, dashboardToken, false, slog.New(slog.DiscardHandler)))
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
			r.Header.Set("X-LLM-Meter-Key", key)
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
	request("GET", "/api/usage/requests", "", "", 401)
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
	if bytes.Contains(body, []byte(created.Key.Token)) || !bytes.Contains(body, []byte(created.Key.Prefix)) || bytes.Contains(body, []byte(`"lastRequest"`)) {
		t.Fatal("client listing must expose only the key prefix and omit unavailable request metadata")
	}
	request("POST", "/v1/responses", `{"model":"gpt-6.2-sol"}`, created.Key.Token, 200)
	request("GET", "/backend-api/codex/models", "", created.Key.Token, http.StatusNotFound)
	request("POST", "/codex/responses", `{"model":"gpt-6.2-sol"}`, created.Key.Token, 200)

	var dashboard ledger.Dashboard
	body = request("GET", "/api/usage?client="+created.Client.ID, "", "", 200)
	if err := json.Unmarshal(body, &dashboard); err != nil {
		t.Fatal(err)
	}
	const combinedEstimate = 0.001104
	summary := dashboard.Summary
	if summary.RequestCount != 2 || summary.PricedRequestCount != 2 || summary.InputTokens != 240 || summary.CachedInputTokens != 40 || summary.OutputTokens != 70 ||
		math.Abs(summary.EstimatedCost-combinedEstimate) > 1e-10 {
		t.Fatalf("incorrect usage summary: %+v", summary)
	}
	var bucketEstimate float64
	for _, bucket := range dashboard.Buckets {
		bucketEstimate += bucket.EstimatedCost
	}
	if math.Abs(bucketEstimate-combinedEstimate) > 1e-10 {
		t.Fatalf("incorrect usage estimate across time buckets: %g", bucketEstimate)
	}
	for _, groups := range [][]ledger.Group{dashboard.Groups.Models, dashboard.Groups.Clients} {
		if len(groups) != 1 || math.Abs(groups[0].EstimatedCost-combinedEstimate) > 1e-10 {
			t.Fatalf("incorrect grouped usage estimate: %+v", groups)
		}
	}
	if len(dashboard.Requests) != 2 || dashboard.Requests[0].Client != "Fixture Client" {
		t.Fatalf("incorrect request attribution: %+v", dashboard.Requests)
	}
	for source, want := range map[string]float64{"api": 0.000502, "codex": 0.000602} {
		var filtered ledger.Dashboard
		body = request("GET", "/api/usage?client="+created.Client.ID+"&source="+source, "", "", 200)
		if err := json.Unmarshal(body, &filtered); err != nil {
			t.Fatal(err)
		}
		if filtered.Summary.RequestCount != 1 || math.Abs(filtered.Summary.EstimatedCost-want) > 1e-10 {
			t.Fatalf("incorrect usage summary filtered by %s: %+v", source, filtered.Summary)
		}
	}

	latest := dashboard.Requests[0]
	var listed struct {
		Clients []ledger.Client `json:"clients"`
	}
	body = request("GET", "/api/clients", "", "", 200)
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Clients) != 1 || listed.Clients[0].LastRequest == nil ||
		*listed.Clients[0].LastRequest != (ledger.ClientRequest{At: latest.At, Status: latest.Status, UsageAvailable: latest.UsageAvailable}) {
		t.Fatalf("incorrect latest request in client list: %+v", listed)
	}
	body = request("GET", "/api/usage?outcome=failed", "", "", 200)
	var outcomeDashboard ledger.Dashboard
	if err := json.Unmarshal(body, &outcomeDashboard); err != nil || outcomeDashboard.Summary != summary || len(outcomeDashboard.Requests) != 0 {
		t.Fatalf("incorrect outcome-filtered overview: %s, decode error %v", body, err)
	}
	request("GET", "/api/usage?outcome=other", "", "", 400)
	request("GET", "/api/usage/requests?outcome=other", "", "", 400)
	query := url.Values{
		"range":   {"24h"},
		"client":  {created.Client.ID},
		"source":  {latest.Source},
		"model":   {latest.Model},
		"bucket":  {latest.At.Truncate(time.Hour).Format(time.RFC3339)},
		"outcome": {"succeeded"},
	}
	body = request("GET", "/api/usage/requests?"+query.Encode(), "", "", 200)
	wantPage, err := json.Marshal(ledger.RequestPage{Requests: []ledger.Request{latest}})
	if err != nil || !bytes.Equal(bytes.TrimSpace(body), wantPage) {
		t.Fatalf("incorrect requests-only response: got %s, want %s, marshal error %v", body, wantPage, err)
	}
	query.Set("cursor", base64.RawURLEncoding.EncodeToString(strconv.AppendInt(nil, latest.ID, 10)))
	body = request("GET", "/api/usage/requests?"+query.Encode(), "", "", 200)
	if string(body) != "{\"requests\":[],\"nextCursor\":null}\n" {
		t.Fatalf("incorrect empty requests page after cursor: %s", body)
	}
	request("GET", "/api/usage/requests?cursor=bad-cursor", "", "", 400)

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
	if dashboard.Summary.InputTokens != 12240 || math.Abs(dashboard.Summary.EstimatedCost-0.031304) > 1e-10 {
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
