package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	"github.com/mishankov/llm-meter/internal/telemetry"
	"github.com/mishankov/llm-meter/internal/usagereport"
	logscollector "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricscollector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracecollector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func gzipBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDirectOTLPIngestionAllHarnessesFormatsAndDeduplication(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Millisecond)
	store, err := ledger.Open(filepath.Join(t.TempDir(), "ledger.db"), pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	firstClient, firstKey, err := store.CreateClient(t.Context(), "Direct OTLP primary", at)
	if err != nil {
		t.Fatal(err)
	}
	secondClient, secondKey, err := store.CreateClient(t.Context(), "Direct OTLP secondary", at)
	if err != nil {
		t.Fatal(err)
	}
	app := routes(store, fstest.MapFS{}, nil, nil, "fixture-dashboard-token-5e9ebc83", false, slog.New(slog.DiscardHandler))

	send := func(path, contentType, encoding, key string, body []byte, wantStatus int) []byte {
		t.Helper()
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, bytes.NewReader(body))
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		if encoding != "" {
			r.Header.Set("Content-Encoding", encoding)
		}
		if key != "" {
			r.Header.Set("X-LLM-Meter-Key", key)
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		if w.Code != wantStatus {
			t.Fatalf("POST %s (%s, %s): status %d, want %d; body=%q", path, contentType, encoding, w.Code, wantStatus, w.Body.String())
		}
		return w.Body.Bytes()
	}

	// 1. Claude Code logs: JSON + gzipped protobuf replay + partial success on malformed record.
	claudeJSON := []byte(fmt.Sprintf(`{"resourceLogs":[{"resource":{"attributes":[{"key":"user.account_id","value":{"stringValue":"secret-account"}}]},
		"scopeLogs":[{"logRecords":[
			{"body":{"stringValue":"claude_code.user_prompt"},"attributes":[{"key":"prompt","value":{"stringValue":"secret-prompt"}}]},
			{"eventName":"claude_code.api_request","timeUnixNano":"%d","attributes":[
				{"key":"model","value":{"stringValue":"claude-sonnet-4-6"}},
				{"key":"request_id","value":{"stringValue":"req-direct-1"}},
				{"key":"session.id","value":{"stringValue":"secret-session"}},
				{"key":"input_tokens","value":{"intValue":"10"}},
				{"key":"output_tokens","value":{"intValue":"20"}},
				{"key":"cache_read_tokens","value":{"intValue":"30"}},
				{"key":"cache_creation_tokens","value":{"intValue":"40"}},
				{"key":"cost_usd","value":{"doubleValue":0.002}}
			]},
			{"eventName":"claude_code.api_request","attributes":[{"key":"input_tokens","value":{"intValue":"-1"}}]}
		]}]}]}`, at.UnixNano()))
	var claudeReq logscollector.ExportLogsServiceRequest
	if err := protojson.Unmarshal(claudeJSON, &claudeReq); err != nil {
		t.Fatal(err)
	}
	claudeProto, err := proto.Marshal(&claudeReq)
	if err != nil {
		t.Fatal(err)
	}
	claudeRespJSON := send("/otlp/v1/logs", "application/json", "", firstKey.Token, claudeJSON, http.StatusOK)
	var logsRes logscollector.ExportLogsServiceResponse
	if err := protojson.Unmarshal(claudeRespJSON, &logsRes); err != nil || logsRes.GetPartialSuccess().GetRejectedLogRecords() != 1 {
		t.Fatalf("Claude JSON partial success = %+v, err=%v", &logsRes, err)
	}
	// Replay as gzipped protobuf for same client (deduplicated) and for second client (recorded independently).
	claudeRespProto := send("/otlp/v1/logs", "application/x-protobuf", "gzip", firstKey.Token, gzipBytes(t, claudeProto), http.StatusOK)
	logsRes.Reset()
	if err := proto.Unmarshal(claudeRespProto, &logsRes); err != nil || logsRes.GetPartialSuccess().GetRejectedLogRecords() != 1 {
		t.Fatalf("Claude protobuf partial success = %+v, err=%v", &logsRes, err)
	}
	send("/otlp/v1/logs", "application/x-protobuf", "gzip", secondKey.Token, gzipBytes(t, claudeProto), http.StatusOK)

	// 2. Codex logs (JSON + protobuf replay).
	codexJSON := []byte(fmt.Sprintf(`{"resourceLogs":[{"resource":{"attributes":[
		{"key":"auth_mode","value":{"stringValue":"chatgpt"}},
		{"key":"model_reasoning_effort","value":{"stringValue":"high"}}
	]},"scopeLogs":[{"logRecords":[{
		"eventName":"codex.sse_event","timeUnixNano":"%d","attributes":[
			{"key":"event.kind","value":{"stringValue":"response.completed"}},
			{"key":"conversation.id","value":{"stringValue":"conv-direct-1"}},
			{"key":"model","value":{"stringValue":"gpt-5.4"}},
			{"key":"input_token_count","value":{"intValue":"100"}},
			{"key":"cached_token_count","value":{"intValue":"25"}},
			{"key":"output_token_count","value":{"intValue":"15"}}
		]
	}]}]}]}`, at.Add(time.Second).UnixNano()))
	var codexReq logscollector.ExportLogsServiceRequest
	if err := protojson.Unmarshal(codexJSON, &codexReq); err != nil {
		t.Fatal(err)
	}
	codexProto, err := proto.Marshal(&codexReq)
	if err != nil {
		t.Fatal(err)
	}
	send("/otlp/v1/logs", "application/json", "", firstKey.Token, codexJSON, http.StatusOK)
	send("/otlp/v1/logs", "application/x-protobuf", "", firstKey.Token, codexProto, http.StatusOK)

	// 3. Gemini CLI logs (gzipped JSON).
	geminiJSON := []byte(fmt.Sprintf(`{"resourceLogs":[{"resource":{"attributes":[
		{"key":"session.id","value":{"stringValue":"gemini-session-1"}}
	]},"scopeLogs":[{"logRecords":[{
		"eventName":"gemini_cli.api_response","timeUnixNano":"%d","attributes":[
			{"key":"status_code","value":{"intValue":"200"}},
			{"key":"model","value":{"stringValue":"gemini-2.5-pro"}},
			{"key":"prompt_id","value":{"stringValue":"prompt-1"}},
			{"key":"input_token_count","value":{"intValue":"50"}},
			{"key":"tool_token_count","value":{"intValue":"10"}},
			{"key":"cached_content_token_count","value":{"intValue":"20"}},
			{"key":"output_token_count","value":{"intValue":"8"}},
			{"key":"thoughts_token_count","value":{"intValue":"4"}}
		]
	}]}]}]}`, at.Add(2*time.Second).UnixNano()))
	send("/otlp/v1/logs", "application/json", "gzip", firstKey.Token, gzipBytes(t, geminiJSON), http.StatusOK)

	// 4. GitHub Copilot CLI traces (JSON + gzipped protobuf replay verifying identical trace/span IDs dedup).
	const copilotTraceHex, copilotSpanHex = "0123456789abcdef0123456789abcdef", "1234567890abcdef"
	copilotJSON := []byte(fmt.Sprintf(`{"resourceSpans":[{"resource":{"attributes":[
		{"key":"service.name","value":{"stringValue":"github-copilot"}}
	]},"scopeSpans":[{"scope":{"name":"github.copilot"},"spans":[{
		"traceId":"%s","spanId":"%s","kind":3,"startTimeUnixNano":"%d","attributes":[
			{"key":"gen_ai.operation.name","value":{"stringValue":"chat"}},
			{"key":"gen_ai.response.model","value":{"stringValue":"gpt-5.4"}},
			{"key":"gen_ai.usage.input_tokens","value":{"intValue":"40"}},
			{"key":"gen_ai.usage.cache_read.input_tokens","value":{"intValue":"10"}},
			{"key":"gen_ai.usage.output_tokens","value":{"intValue":"5"}}
		]
	}]}]}]}`, copilotTraceHex, copilotSpanHex, at.Add(3*time.Second).UnixNano()))
	var copilotReq tracecollector.ExportTraceServiceRequest
	if err := protojson.Unmarshal(copilotJSON, &copilotReq); err != nil {
		t.Fatal(err)
	}
	copilotSpan := copilotReq.GetResourceSpans()[0].GetScopeSpans()[0].GetSpans()[0]
	copilotSpan.TraceId, _ = hex.DecodeString(copilotTraceHex)
	copilotSpan.SpanId, _ = hex.DecodeString(copilotSpanHex)
	copilotProto, err := proto.Marshal(&copilotReq)
	if err != nil {
		t.Fatal(err)
	}
	send("/otlp/v1/traces", "application/json", "", firstKey.Token, copilotJSON, http.StatusOK)
	send("/otlp/v1/traces", "application/x-protobuf", "gzip", firstKey.Token, gzipBytes(t, copilotProto), http.StatusOK)

	// 5. OpenCode traces (protobuf).
	const openCodeTraceHex, openCodeSpanHex = "fedcba9876543210fedcba9876543210", "abcdef0123456789"
	openCodeJSON := []byte(fmt.Sprintf(`{"resourceSpans":[{"resource":{"attributes":[
		{"key":"service.name","value":{"stringValue":"opencode"}}
	]},"scopeSpans":[{"spans":[{
		"traceId":"%s","spanId":"%s","name":"SessionStep.attempt","endTimeUnixNano":"%d","attributes":[
			{"key":"gen_ai.provider.name","value":{"stringValue":"opencode-go"}},
			{"key":"gen_ai.request.model","value":{"stringValue":"kimi-k2.5"}},
			{"key":"gen_ai.usage.input_tokens","value":{"intValue":"30"}},
			{"key":"gen_ai.usage.cache_read.input_tokens","value":{"intValue":"6"}},
			{"key":"gen_ai.usage.cache_creation.input_tokens","value":{"intValue":"3"}},
			{"key":"gen_ai.usage.output_tokens","value":{"intValue":"10"}},
			{"key":"gen_ai.input.messages","value":{"stringValue":"secret-message"}}
		]
	}]}]}]}`, openCodeTraceHex, openCodeSpanHex, at.Add(4*time.Second).UnixNano()))
	send("/otlp/v1/traces", "application/json", "", firstKey.Token, openCodeJSON, http.StatusOK)

	// 6. Metrics acknowledgment (JSON + gzipped protobuf) records no requests.
	metricsJSON := []byte(`{"resourceMetrics":[]}`)
	metricsRespJSON := send("/otlp/v1/metrics", "application/json", "", firstKey.Token, metricsJSON, http.StatusOK)
	var metricsRes metricscollector.ExportMetricsServiceResponse
	if err := protojson.Unmarshal(metricsRespJSON, &metricsRes); err != nil {
		t.Fatalf("metrics JSON decode failed: %v", err)
	}
	metricsProto, err := proto.Marshal(&metricscollector.ExportMetricsServiceRequest{})
	if err != nil {
		t.Fatal(err)
	}
	metricsRespProto := send("/otlp/v1/metrics", "application/x-protobuf", "gzip", firstKey.Token, gzipBytes(t, metricsProto), http.StatusOK)
	if err := proto.Unmarshal(metricsRespProto, &metricsRes); err != nil {
		t.Fatalf("metrics protobuf decode failed: %v", err)
	}

	// Verify ledger state for primary client: exactly 5 deduplicated requests (one per harness).
	filter := ledger.Filter{
		Start: at.Add(-time.Minute), End: at.Add(time.Minute), Interval: time.Minute, Clients: []string{firstClient.ID},
	}
	view, err := store.Dashboard(t.Context(), filter)
	if err != nil {
		t.Fatal(err)
	}
	if view.Summary.RequestCount != 5 || len(view.Requests) != 5 {
		t.Fatalf("primary client recorded %d requests, want 5: %+v", view.Summary.RequestCount, view.Requests)
	}
	byEndpoint := make(map[string]ledger.Request, len(view.Requests))
	for _, req := range view.Requests {
		byEndpoint[req.Endpoint] = req
		if req.MeasurementSource != "relay" {
			t.Errorf("endpoint %s measurementSource = %q, want relay", req.Endpoint, req.MeasurementSource)
		}
	}
	if r := byEndpoint[usagereport.ClaudeCodeEndpoint]; r.Provider != "anthropic" || r.InputTokens != 80 || r.CachedInputTokens != 30 || r.CacheWriteTokens != 40 || r.OutputTokens != 20 || r.Cost == nil || *r.Cost != 0.002 {
		t.Errorf("unexpected Claude Code row: %+v", r)
	}
	if r := byEndpoint[usagereport.CodexTelemetryEndpoint]; r.Source != "codex" || r.ReasoningLevel != "high" || r.InputTokens != 100 || r.CachedInputTokens != 25 || r.OutputTokens != 15 {
		t.Errorf("unexpected Codex row: %+v", r)
	}
	if r := byEndpoint[usagereport.GeminiTelemetryEndpoint]; r.Provider != "google" || r.InputTokens != 60 || r.CachedInputTokens != 20 || r.OutputTokens != 12 {
		t.Errorf("unexpected Gemini row: %+v", r)
	}
	if r := byEndpoint[usagereport.CopilotTelemetryEndpoint]; r.Provider != "github-copilot" || r.InputTokens != 40 || r.CachedInputTokens != 10 || r.OutputTokens != 5 {
		t.Errorf("unexpected Copilot row: %+v", r)
	}
	if r := byEndpoint[usagereport.OpenCodeTelemetryEndpoint]; r.Provider != "opencode-go" || r.InputTokens != 30 || r.CachedInputTokens != 6 || r.CacheWriteTokens != 3 || r.OutputTokens != 10 {
		t.Errorf("unexpected OpenCode row: %+v", r)
	}

	// Verify secondary client recorded its own Claude Code event independently.
	filter.Clients = []string{secondClient.ID}
	secondView, err := store.Dashboard(t.Context(), filter)
	if err != nil {
		t.Fatal(err)
	}
	if secondView.Summary.RequestCount != 1 || secondView.Requests[0].Endpoint != usagereport.ClaudeCodeEndpoint {
		t.Fatalf("secondary client recorded %+v, want 1 Claude Code request", secondView.Requests)
	}
}

func TestDirectOTLPAuthPathsMethodsAndProxyIsolation(t *testing.T) {
	var upstreamHits []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits = append(upstreamHits, r.Method+" "+r.URL.Path)
		if r.Header.Get("X-LLM-Meter-Key") != "" {
			t.Errorf("upstream received X-LLM-Meter-Key on %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"resp_proxy_%d","model":"gpt-5.4","usage":{"input_tokens":12,"output_tokens":3}}`, len(upstreamHits))
	}))
	defer upstream.Close()
	api, _ := url.Parse(upstream.URL + "/v1")

	at := time.Now().UTC()
	store, err := ledger.Open(filepath.Join(t.TempDir(), "ledger.db"), pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	activeClient, activeKey, err := store.CreateClient(t.Context(), "Active", at)
	if err != nil {
		t.Fatal(err)
	}
	revokedClient, revokedKey, err := store.CreateClient(t.Context(), "Revoked", at)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeKey(t.Context(), revokedClient.ID, revokedKey.ID, at); err != nil {
		t.Fatal(err)
	}
	archivedClient, archivedKey, err := store.CreateClient(t.Context(), "Archived", at)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveClient(t.Context(), archivedClient.ID, at); err != nil {
		t.Fatal(err)
	}
	const dashboardToken = "fixture-dashboard-token-5e9ebc83"
	app := routes(store, fstest.MapFS{}, api, nil, dashboardToken, false, slog.New(slog.DiscardHandler))

	// Verify withClientKey strips X-LLM-Meter-Key before calling next.
	var sawHeader string
	checked := withClientKey(store, func(w http.ResponseWriter, r *http.Request, clientID string) {
		sawHeader = r.Header.Get("X-LLM-Meter-Key")
		if clientID != activeClient.ID {
			t.Errorf("clientID = %q, want %q", clientID, activeClient.ID)
		}
		w.WriteHeader(http.StatusNoContent)
	}, slog.New(slog.DiscardHandler))
	stripReq := httptest.NewRequest(http.MethodPost, "/otlp/v1/logs", nil)
	stripReq.Header.Set("X-LLM-Meter-Key", activeKey.Token)
	stripRec := httptest.NewRecorder()
	checked.ServeHTTP(stripRec, stripReq)
	if stripRec.Code != http.StatusNoContent || sawHeader != "" {
		t.Fatalf("withClientKey status=%d, header=%q", stripRec.Code, sawHeader)
	}

	// Auth failures across valid and invalid /otlp paths.
	for _, tc := range []struct {
		name    string
		method  string
		path    string
		headers []string
	}{
		{"missing key logs", http.MethodPost, "/otlp/v1/logs", nil},
		{"missing key traces", http.MethodPost, "/otlp/v1/traces", nil},
		{"missing key metrics", http.MethodPost, "/otlp/v1/metrics", nil},
		{"missing key GET", http.MethodGet, "/otlp/v1/logs", nil},
		{"missing key root", http.MethodPost, "/otlp", nil},
		{"missing key traversal", http.MethodPost, "/otlp/v1/../v1/logs", nil},
		{"duplicate valid key", http.MethodPost, "/otlp/v1/logs", []string{activeKey.Token, activeKey.Token}},
		{"invalid key", http.MethodPost, "/otlp/v1/logs", []string{"llmm_invalid"}},
		{"dashboard token", http.MethodPost, "/otlp/v1/logs", []string{dashboardToken}},
		{"revoked key", http.MethodPost, "/otlp/v1/logs", []string{revokedKey.Token}},
		{"archived client key", http.MethodPost, "/otlp/v1/logs", []string{archivedKey.Token}},
	} {
		t.Run("auth/"+tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
			r.Header.Set("Content-Type", "application/json")
			for _, value := range tc.headers {
				r.Header.Add("X-LLM-Meter-Key", value)
			}
			w := httptest.NewRecorder()
			app.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s: status %d, want 401; body=%q", tc.method, tc.path, w.Code, w.Body.String())
			}
		})
	}

	// Non-POST methods on exact OTLP routes return 405 with Allow: POST after auth.
	for _, path := range []string{"/otlp/v1/logs", "/otlp/v1/traces", "/otlp/v1/metrics"} {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead} {
			r := httptest.NewRequest(method, path, nil)
			r.Header.Set("X-LLM-Meter-Key", activeKey.Token)
			w := httptest.NewRecorder()
			app.ServeHTTP(w, r)
			if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != http.MethodPost {
				t.Fatalf("%s %s: status=%d allow=%q, want 405 POST", method, path, w.Code, w.Header().Get("Allow"))
			}
		}
	}

	// Strict path handling: unknown paths, trailing slashes, and path traversal return 404 after auth.
	invalidPaths := []string{
		"/otlp",
		"/otlp/",
		"/otlp/v1",
		"/otlp/v1/logs/",
		"/otlp/v1/traces/",
		"/otlp/v1/metrics/",
		"/otlp/v1/unknown",
		"/otlp/v1/../v1/logs",
		"/otlp/v1/./logs",
		"/otlp//v1/logs",
		"/otlp/../v1/responses",
		"/otlp/v1/%2e%2e/v1/logs",
		"/otlp/v1/logs%2f",
		"/otlp%2fv1/logs",
	}
	for _, rawPath := range invalidPaths {
		for _, method := range []string{http.MethodPost, http.MethodGet} {
			r := httptest.NewRequest(method, "http://example.test"+rawPath, strings.NewReader(`{}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-LLM-Meter-Key", activeKey.Token)
			w := httptest.NewRecorder()
			app.ServeHTTP(w, r)
			if w.Code != http.StatusNotFound {
				t.Fatalf("%s %s: status=%d location=%q, want 404", method, rawPath, w.Code, w.Header().Get("Location"))
			}
		}
	}

	// Non-interference: /v1/responses and /v1/logs still route to the OpenAI proxy, not OTLP.
	for _, path := range []string{"/v1/responses", "/v1/logs"} {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-5.4"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-LLM-Meter-Key", activeKey.Token)
		r.Header.Set("Authorization", "Bearer upstream-secret")
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("POST %s: status=%d, want 200; body=%q", path, w.Code, w.Body.String())
		}
	}
	if len(upstreamHits) != 2 || upstreamHits[0] != "POST /v1/responses" || upstreamHits[1] != "POST /v1/logs" {
		t.Fatalf("upstream proxy hits = %v, want [/v1/responses, /v1/logs]", upstreamHits)
	}

	// Verify /api/usage/events still works and shares deduplication with direct /otlp.
	claudeJSON := []byte(fmt.Sprintf(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[{
		"eventName":"claude_code.api_request","timeUnixNano":"%d","attributes":[
			{"key":"model","value":{"stringValue":"claude-sonnet-4-6"}},
			{"key":"request_id","value":{"stringValue":"shared-dedup-id"}},
			{"key":"input_tokens","value":{"intValue":"10"}},
			{"key":"output_tokens","value":{"intValue":"5"}}
		]
	}]}]}]}`, at.UnixNano()))
	otlpReq := httptest.NewRequest(http.MethodPost, "/otlp/v1/logs", bytes.NewReader(claudeJSON))
	otlpReq.Header.Set("Content-Type", "application/json")
	otlpReq.Header.Set("X-LLM-Meter-Key", activeKey.Token)
	otlpRec := httptest.NewRecorder()
	app.ServeHTTP(otlpRec, otlpReq)
	if otlpRec.Code != http.StatusOK {
		t.Fatalf("POST /otlp/v1/logs: status=%d", otlpRec.Code)
	}

	// Capture the relay JSON payload that telemetry.Handler produces for the exact same OTLP export,
	// and upload it via POST /api/usage/events — it must deduplicate against the direct /otlp write.
	var relayPayloads [][]byte
	relayCollector := telemetry.Handler(func(_ context.Context, payloads [][]byte) error {
		relayPayloads = append(relayPayloads, payloads...)
		return nil
	}, nil)
	capReq := httptest.NewRequest(http.MethodPost, "/otlp/v1/logs", bytes.NewReader(claudeJSON))
	capReq.Header.Set("Content-Type", "application/json")
	capRec := httptest.NewRecorder()
	relayCollector.ServeHTTP(capRec, capReq)
	if capRec.Code != http.StatusOK || len(relayPayloads) != 1 {
		t.Fatalf("relay collector status=%d payloads=%d", capRec.Code, len(relayPayloads))
	}
	dupRelayReq := httptest.NewRequest(http.MethodPost, usagereport.Path, bytes.NewReader(relayPayloads[0]))
	dupRelayReq.Header.Set("Content-Type", "application/json")
	dupRelayReq.Header.Set("X-LLM-Meter-Key", activeKey.Token)
	dupRelayRec := httptest.NewRecorder()
	app.ServeHTTP(dupRelayRec, dupRelayReq)
	if dupRelayRec.Code != http.StatusNoContent {
		t.Fatalf("POST %s duplicate: status=%d", usagereport.Path, dupRelayRec.Code)
	}

	// Also verify a distinct /api/usage/events report still succeeds and records normally.
	relayEvent, err := usagereport.NewEvent(proxy.Usage{
		At: at, Provider: "openai", Model: "gpt-5.4", Endpoint: "/v1/responses",
		Method: http.MethodPost, Status: http.StatusOK, HasUsage: true, InputTokens: 20, OutputTokens: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	relayBody, err := json.Marshal(relayEvent)
	if err != nil {
		t.Fatal(err)
	}
	relayReq := httptest.NewRequest(http.MethodPost, usagereport.Path, bytes.NewReader(relayBody))
	relayReq.Header.Set("Content-Type", "application/json")
	relayReq.Header.Set("X-LLM-Meter-Key", activeKey.Token)
	relayRec := httptest.NewRecorder()
	app.ServeHTTP(relayRec, relayReq)
	if relayRec.Code != http.StatusNoContent {
		t.Fatalf("POST %s: status=%d", usagereport.Path, relayRec.Code)
	}

	// Active client should have: 2 proxy requests (/v1/responses, /v1/logs) + 1 deduplicated Claude OTLP + 1 distinct relay event = 4.
	view, err := store.Dashboard(t.Context(), ledger.Filter{
		Start: at.Add(-time.Minute), End: at.Add(time.Minute), Interval: time.Minute, Clients: []string{activeClient.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Summary.RequestCount != 4 {
		t.Fatalf("active client request count = %d, want 4: %+v", view.Summary.RequestCount, view.Requests)
	}
}
