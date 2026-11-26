package proxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestAntigravityHandlerChecksPrefixAndSupportsNilRecorder(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/%62ase/v1internal:loadCodeAssist" {
			t.Errorf("unexpected forwarded path %q", r.URL.EscapedPath())
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	base, err := url.Parse(upstream.URL + "/%62ase")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewAntigravity(*base, nil)
	for path, want := range map[string]int{
		"/x":                                     http.StatusNotFound,
		"/antigravity/v1internal:loadCodeAssist": http.StatusNoContent,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
		if response.Code != want {
			t.Errorf("%s: status %d, want %d", path, response.Code, want)
		}
	}
}

func TestHTTPForwardsTheOriginalCredentialsAndMetersNewUsage(t *testing.T) {
	const response = `{"id":"resp_1","model":"gpt-example","service_tier":"priority","usage":{"input_tokens":13,"output_tokens":5,"input_tokens_details":{"cached_tokens":3,"cache_write_tokens":2}}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/upstream/responses/resp%2F1" || r.URL.RawQuery != "provider=1&expand=a%2Fb" {
			t.Errorf("unexpected upstream target %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer upstream-secret" || r.Header.Get(accessKey) != "" {
			t.Error("proxy and upstream credentials were not separated")
		}
		if r.Header.Get("OpenAI-Project") != "project-1" {
			t.Error("upstream project header was lost")
		}
		if r.Header.Get("Cookie") != "upstream=keep" {
			t.Errorf("unexpected upstream cookies %q", r.Header.Get("Cookie"))
		}
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != `{"model":"fallback","prompt_cache_retention":"12h","input":"private"}` {
				t.Errorf("upstream received body %q, err %v", body, err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		gzipBody := gzip.NewWriter(w)
		_, _ = io.WriteString(gzipBody, response)
		_ = gzipBody.Close()
	}))
	defer upstream.Close()

	records := make(chan Usage, 2)
	target := parseURL(t, upstream.URL+"/upstream?provider=1")
	proxy := testProxy(target, records)
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		var body io.Reader
		if method == http.MethodPost {
			body = strings.NewReader(`{"model":"fallback","prompt_cache_retention":"12h","input":"private"}`)
		}
		req := httptest.NewRequest(method, "/v1/responses/resp%2F1?expand=a%2Fb", body)
		req.Header.Set("Authorization", "Bearer upstream-secret")
		req.Header.Set("OpenAI-Project", "project-1")
		req.Header.Set("Cookie", "upstream=keep; llm_meter_session=do-not-forward")
		req.Header.Set(accessKey, "proxy-secret")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, req)
		if w.Code != http.StatusOK || w.Body.String() != response {
			t.Fatalf("HTTP response = %d %s", w.Code, w.Body.String())
		}
		if method == http.MethodGet {
			assertNoRecord(t, records)
			continue
		}
		record := nextRecord(t, records)
		if record.Method != method || record.Source != "api" || record.ClientID != "client-1" ||
			record.Endpoint != "/v1/responses/resp/1" || record.Status != http.StatusOK ||
			record.Model != "gpt-example" || record.ServiceTier != "priority" {
			t.Fatalf("unexpected metadata: %+v", record)
		}
		if !record.HasUsage || record.ResponseID != "resp_1" || record.InputTokens != 13 ||
			record.CachedInputTokens != 3 || record.CacheWriteTokens != 2 || record.OutputTokens != 5 ||
			record.CacheRetention != "12h" {
			t.Fatalf("incorrect token usage: %+v", record)
		}
	}
}

func TestSSEStreamsAndReadsReportedUsageAfterAnOversizedEvent(t *testing.T) {
	const outputSize = 5 << 20
	release := make(chan struct{})
	var once sync.Once
	resume := func() { once.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer upstream" {
			t.Errorf("unexpected Responses request: %s", r.URL)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_sse\",\"model\":\"gpt-sse\"}}\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: invalid event\n\n")
		_, _ = io.WriteString(w, "event: response.completed\r\ndata: {\"type\":\"response.completed\",")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "\r\ndata: \"response\":{\"id\":\"resp_sse\",\"output\":\""+strings.Repeat("x", outputSize)+
			"\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\r\n\r\n")
	}))
	defer upstream.Close()

	records := make(chan Usage, 1)
	server := httptest.NewServer(testProxy(parseURL(t, upstream.URL+"/v1"), records))
	defer server.Close()
	defer resume()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer upstream")
	client := server.Client()
	client.Timeout = 5 * time.Second
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "response.created") {
		t.Fatalf("stream failed to flush its first event: %q, %v", line, err)
	}
	resume()
	rest, err := io.ReadAll(reader)
	if err != nil || !strings.Contains(string(rest), "response.completed") || len(rest) <= outputSize {
		t.Fatalf("stream ended early: %d bytes, %v", len(rest), err)
	}
	record := nextRecord(t, records)
	if record.Source != "api" || record.Model != "gpt-sse" || record.ResponseID != "resp_sse" ||
		!record.HasUsage || record.InputTokens != 0 || record.OutputTokens != 0 {
		t.Fatalf("unexpected Responses usage: %+v", record)
	}
}

const responsesSSECompleted = `data: {"type":"response.completed","response":{"id":"resp_stream","usage":{"input_tokens":11,"output_tokens":5}}}`

func TestResponsesStreamFallbackForwardsAndMeters(t *testing.T) {
	const first = "da"
	const created = `ta: {"type":"response.created","response":{"id":"resp_stream","model":"gpt-sse"}}`
	// Metering must continue beyond the probe budget once framing is confirmed.
	tail := created + "\r\n\r\n: " + strings.Repeat("x", 8<<10) + "\r\n\r\n" + responsesSSECompleted + "\r\n\r\n"
	for _, tc := range []struct {
		name, contentType string
	}{
		{"plain text", "text/plain; charset=utf-8"},
		{"missing content type", ""},
		{"mislabeled JSON", "application/json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{}, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.contentType == "" {
					w.Header()["Content-Type"] = nil // Suppress automatic sniffing by the upstream server.
				} else {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.Header().Set("X-Upstream", "preserved")
				_, _ = io.WriteString(w, first)
				w.(http.Flusher).Flush()
				<-release
				_, _ = io.WriteString(w, tail)
			}))
			defer upstream.Close()

			records := make(chan Usage, 2)
			server := httptest.NewServer(testProxy(parseURL(t, upstream.URL), records))
			defer server.Close()
			defer close(release)

			client := server.Client()
			client.Timeout = 5 * time.Second
			postStream := func(target string) *http.Response {
				t.Helper()
				response, err := client.Post(target+"/v1/responses", "application/json", strings.NewReader(`{"stream":true}`))
				if err != nil {
					t.Fatal(err)
				}
				return response
			}
			if tc.contentType == "" {
				direct := postStream(upstream.URL)
				defer direct.Body.Close()
				if _, present := direct.Header["Content-Type"]; present {
					t.Fatalf("upstream sent Content-Type %q", direct.Header.Values("Content-Type"))
				}
				release <- struct{}{}
				if _, err := io.Copy(io.Discard, direct.Body); err != nil {
					t.Fatal(err)
				}
			}

			response := postStream(server.URL)
			defer response.Body.Close()
			contentType := response.Header.Get("Content-Type")
			// For a headerless stream, Go can sniff the first fragment or flush the headers first.
			contentTypePreserved := contentType == tc.contentType ||
				tc.contentType == "" && contentType == "text/plain; charset=utf-8"
			if response.StatusCode != http.StatusOK || !contentTypePreserved || response.Header.Get("X-Upstream") != "preserved" {
				t.Fatalf("unexpected response status %d, headers %v", response.StatusCode, response.Header)
			}
			prefix := make([]byte, len(first))
			if _, err := io.ReadFull(response.Body, prefix); err != nil || string(prefix) != first {
				t.Fatalf("stream failed to flush its first fragment: %q, %v", prefix, err)
			}
			release <- struct{}{}
			rest, err := io.ReadAll(response.Body)
			if err != nil || string(rest) != tail {
				t.Fatalf("stream body changed: read %d bytes, want %d, error %v", len(rest), len(tail), err)
			}
			server.Close()
			if len(records) != 1 {
				t.Fatalf("got %d usage records, want 1", len(records))
			}
			record := nextRecord(t, records)
			if !record.HasUsage || record.InputTokens != 11 || record.OutputTokens != 5 ||
				record.ResponseID != "resp_stream" || record.Model != "gpt-sse" {
				t.Fatalf("unexpected usage: %+v", record)
			}
		})
	}
}

func TestResponsesFallbackPreservesOtherResponses(t *testing.T) {
	const responses = "/v1/responses"
	const framed = responsesSSECompleted + "\n\n"
	const plain = "text/plain"
	// JSON metering must continue after the SSE probe exhausts its budget.
	jsonBody := `{"id":"resp_json","output":"` + strings.Repeat("x", 8<<10) +
		`","usage":{"input_tokens":11,"output_tokens":5}}`
	for _, tc := range []struct {
		name, method, path, contentType string
		status                          int
		body, wantResponseID            string
	}{
		{"ordinary text", http.MethodPost, responses, plain, http.StatusOK, "plain text\n", ""},
		{"headerless non-event data", http.MethodPost, responses, "", http.StatusOK, "data: not an event\n\n", ""},
		{"untyped data field", http.MethodPost, responses, plain, http.StatusOK, "data: {\"usage\":{\"input_tokens\":11}}\n\n", ""},
		{"incomplete frame", http.MethodPost, responses, plain, http.StatusOK, responsesSSECompleted + "\n", ""},
		{"frame beyond budget", http.MethodPost, responses, plain, http.StatusOK, ":" + strings.Repeat("x", 8<<10) + "\n\n" + framed, ""},
		{"other route", http.MethodPost, responses + "/resp_stream", plain, http.StatusOK, framed, ""},
		{"other method", http.MethodGet, responses, plain, http.StatusOK, framed, ""},
		{"error response", http.MethodPost, responses, plain, http.StatusBadRequest, framed, ""},
		{"genuine JSON", http.MethodPost, responses, "application/json", http.StatusOK, jsonBody, "resp_json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.contentType == "" {
					w.Header()["Content-Type"] = nil
				} else {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()

			records := make(chan Usage, 1)
			handler := testProxy(parseURL(t, upstream.URL), records)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.status || response.Header().Get("Content-Type") != tc.contentType || response.Body.String() != tc.body {
				t.Fatalf("proxy changed upstream response: status %d, content type %q, body length %d", response.Code,
					response.Header().Get("Content-Type"), response.Body.Len())
			}
			if tc.method != http.MethodPost {
				assertNoRecord(t, records)
				return
			}
			record := nextRecord(t, records)
			wantUsage := tc.wantResponseID != ""
			if record.HasUsage != wantUsage || record.ResponseID != tc.wantResponseID ||
				wantUsage && (record.InputTokens != 11 || record.OutputTokens != 5) {
				t.Fatalf("unexpected usage: %+v", record)
			}
		})
	}
}

func TestWebSocketMetersEachResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("unexpected Responses WebSocket path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer upstream" || r.Header.Get(accessKey) != "" {
			t.Error("proxy and upstream WebSocket credentials were not separated")
		}
		upgrader := websocket.Upgrader{Subprotocols: []string{"responses"}}
		remote, err := upgrader.Upgrade(w, r, http.Header{"X-Upstream": {"kept"}})
		if err != nil {
			t.Error(err)
			return
		}
		defer remote.Close()
		_ = remote.SetReadDeadline(time.Now().Add(4 * time.Second))
		for i := 1; i <= 2; i++ {
			kind, payload, err := remote.ReadMessage()
			if err != nil || kind != websocket.TextMessage || !strings.Contains(string(payload), "gpt-ws") {
				t.Errorf("upstream received message type %d and %q, err %v", kind, payload, err)
				return
			}
			var tokens string
			if i == 1 {
				tokens = `,"usage":{"input_tokens":12,"input_tokens_details":{"cached_tokens":2},"output_tokens":3}`
			}
			event := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_ws_%d"%s}}`, i, tokens)
			if err := remote.WriteMessage(websocket.TextMessage, []byte(event)); err != nil {
				t.Error(err)
				return
			}
		}
		close := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "complete")
		_ = remote.WriteControl(websocket.CloseMessage, close, time.Now().Add(time.Second))
	}))
	defer upstream.Close()
	records := make(chan Usage, 4)
	server := httptest.NewServer(testProxy(parseURL(t, upstream.URL+"/v1"), records))
	defer server.Close()

	dialer := websocket.Dialer{Subprotocols: []string{"responses"}, HandshakeTimeout: 4 * time.Second}
	client, response, err := dialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses",
		http.Header{"Authorization": {"Bearer upstream"}, accessKey: {"proxy-secret"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetReadDeadline(time.Now().Add(4 * time.Second))
	if client.Subprotocol() != "responses" || response.Header.Get("X-Upstream") != "kept" {
		t.Fatalf("handshake did not preserve negotiated headers: %+v", response.Header)
	}
	for i := 1; i <= 2; i++ {
		if err := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-ws"}`)); err != nil {
			t.Fatal(err)
		}
		_, payload, err := client.ReadMessage()
		id := fmt.Sprintf("resp_ws_%d", i)
		if err != nil || !strings.Contains(string(payload), id) {
			t.Fatalf("reply = %q, err %v", payload, err)
		}
		record := nextRecord(t, records)
		if record.Status != http.StatusSwitchingProtocols || record.Model != "gpt-ws" ||
			record.ResponseID != id || record.ClientID != "client-1" || record.Endpoint != "/v1/responses" {
			t.Fatalf("unexpected WebSocket usage metadata: %+v", record)
		}
		if i == 1 && (!record.HasUsage || record.InputTokens != 12 || record.CachedInputTokens != 2 || record.OutputTokens != 3) {
			t.Fatalf("incorrect reported WebSocket usage: %+v", record)
		}
		if i == 2 && record.HasUsage {
			t.Fatalf("unreported usage was counted: %+v", record)
		}
	}
	if _, _, err := client.ReadMessage(); !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
		t.Fatalf("WebSocket close not relayed: %v", err)
	}
}

func TestSocketMeterRecordsAnErrorAfterASuccess(t *testing.T) {
	var records []Usage
	meter := socketMeter{usage: Usage{Status: http.StatusSwitchingProtocols}, record: func(usage Usage) {
		records = append(records, usage)
	}}
	meter.request(decodeMessage([]byte(`{"type":"response.create","model":"first"}`)))
	meter.response(decodeMessage([]byte(`{"type":"response.completed","response":{"id":"response_1","usage":{"input_tokens":6}}}`)))
	meter.request(decodeMessage([]byte(`{"type":"response.create","stream_id":"follow-up","model":"second"}`)))
	meter.response(decodeMessage([]byte(`{"type":"response.created","stream_id":"follow-up","response":{"id":"response_2"}}`)))
	meter.response(decodeMessage([]byte(`{"type":"error","stream_id":"follow-up","status":400,"error":{"code":"previous_response_not_found"}}`)))
	meter.finish()
	if len(records) != 2 || !records[0].HasUsage || records[1].HasUsage || records[1].Status != 400 || records[1].Model != "second" {
		t.Fatalf("unexpected WebSocket records: %+v", records)
	}
}

func TestHTTPProxyExtractsReasoningLevelAcrossProviders(t *testing.T) {
	tests := []struct {
		name         string
		antigravity  bool
		path         string
		requestBody  string
		responseBody string
		want         string
		wantModel    string
	}{
		{
			name:         "openai responses request effort",
			path:         "/v1/responses",
			requestBody:  `{"model":"gpt-5.4","reasoning":{"effort":"high"},"input":"private"}`,
			responseBody: `{"id":"resp_1","model":"gpt-5.4","usage":{"input_tokens":10,"output_tokens":5}}`,
			want:         "high",
		},
		{
			name:         "openai responses response effort overrides",
			path:         "/v1/responses",
			requestBody:  `{"model":"gpt-5.4","reasoning":{"effort":"low"}}`,
			responseBody: `{"id":"resp_2","model":"gpt-5.4","reasoning":{"effort":"xhigh"},"usage":{"input_tokens":10,"output_tokens":5}}`,
			want:         "xhigh",
		},
		{
			name:         "openai chat completions reasoning_effort",
			path:         "/v1/chat/completions",
			requestBody:  `{"model":"o3-mini","reasoning_effort":"medium","messages":[{"role":"user","content":"hi"}]}`,
			responseBody: `{"id":"chat_1","model":"o3-mini","usage":{"prompt_tokens":8,"completion_tokens":4}}`,
			want:         "medium",
		},
		{
			name:         "anthropic output_config effort",
			path:         "/v1/messages",
			requestBody:  `{"model":"claude-sonnet-4-6","output_config":{"effort":"max"},"messages":[]}`,
			responseBody: `{"type":"message","id":"msg_1","model":"claude-sonnet-4-6","usage":{"input_tokens":10,"output_tokens":4}}`,
			want:         "max",
		},
		{
			name:         "anthropic thinking adaptive",
			path:         "/v1/messages",
			requestBody:  `{"model":"claude-sonnet-4-6","thinking":{"type":"adaptive"}}`,
			responseBody: `{"type":"message","id":"msg_2","model":"claude-sonnet-4-6","usage":{"input_tokens":10,"output_tokens":4}}`,
			want:         "adaptive",
		},
		{
			name:         "antigravity nested generationConfig thinkingLevel",
			antigravity:  true,
			path:         "/antigravity/v1internal:generateContent",
			requestBody:  `{"model":"gemini-2.5-pro","request":{"contents":[{"parts":[{"text":"private"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"HIGH"}}}}`,
			responseBody: `{"response":{"responseId":"g1","usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5}}}`,
			want:         "high",
		},
		{
			name:         "antigravity thinkingBudget zero maps to none",
			antigravity:  true,
			path:         "/antigravity/v1internal:generateContent",
			requestBody:  `{"model":"gemini-2.5-flash","request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}}`,
			responseBody: `{"response":{"responseId":"g2","usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5}}}`,
			want:         "none",
		},
		{
			name:         "antigravity thinkingBudget -1 maps to adaptive",
			antigravity:  true,
			path:         "/antigravity/v1internal:generateContent",
			requestBody:  `{"model":"gemini-2.5-pro","request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}}`,
			responseBody: `{"response":{"responseId":"g2b","usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5}}}`,
			want:         "adaptive",
		},
		{
			name:         "antigravity null thinkingBudget stays empty",
			antigravity:  true,
			path:         "/antigravity/v1internal:generateContent",
			requestBody:  `{"model":"gemini-2.5-pro","request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":null}}}}`,
			responseBody: `{"response":{"responseId":"g2n","usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5}}}`,
			want:         "",
			wantModel:    "gemini-2.5-pro",
		},
		{
			name:         "antigravity positive thinkingBudget does not invent level",
			antigravity:  true,
			path:         "/antigravity/v1internal:generateContent",
			requestBody:  `{"model":"gemini-2.5-pro","request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}}`,
			responseBody: `{"response":{"responseId":"g2c","usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5}}}`,
			want:         "",
		},
		{
			name:         "antigravity protobuf enum prefix THINKING_LEVEL_XHIGH",
			antigravity:  true,
			path:         "/antigravity/v1internal:streamGenerateContent",
			requestBody:  `{"model":"gemini-4-neon","request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"THINKING_LEVEL_XHIGH"}}}}`,
			responseBody: `{"response":{"responseId":"g2d","usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5}}}`,
			want:         "xhigh",
		},
		{
			name:         "antigravity protobuf enum THINKING_LEVEL_UNSPECIFIED stays empty",
			antigravity:  true,
			path:         "/antigravity/v1internal:generateContent",
			requestBody:  `{"model":"gemini-4-neon","request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"THINKING_LEVEL_UNSPECIFIED"}}}}`,
			responseBody: `{"response":{"responseId":"g2e","usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5}}}`,
			want:         "",
		},
		{
			name:         "antigravity snake_case generation_config thinking_level",
			antigravity:  true,
			path:         "/antigravity/v1internal:generateContent",
			requestBody:  `{"model":"gemini-4-neon","request":{"generation_config":{"thinking_config":{"thinking_level":"MEDIUM"}}}}`,
			responseBody: `{"response":{"responseId":"g2f","usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5}}}`,
			want:         "medium",
			wantModel:    "gemini-4-neon",
		},
		{
			name:         "antigravity numeric ProtoJSON thinking_level preserves request model",
			antigravity:  true,
			path:         "/antigravity/v1internal:generateContent",
			requestBody:  `{"model":"gemini-4-neon","request":{"generation_config":{"thinking_config":{"thinking_level":0}}}}`,
			responseBody: `{"response":{"responseId":"g2g","usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5}}}`,
			want:         "",
			wantModel:    "gemini-4-neon",
		},
		{
			name:         "antigravity numeric ProtoJSON thinkingLevel preserves request model",
			antigravity:  true,
			path:         "/antigravity/v1internal:generateContent",
			requestBody:  `{"model":"gemini-4-neon","request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":0}}}}`,
			responseBody: `{"response":{"responseId":"g2h","usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5}}}`,
			want:         "",
			wantModel:    "gemini-4-neon",
		},
		{
			name:         "antigravity thinkingLevel survives large responseSchema past capture limit",
			antigravity:  true,
			path:         "/antigravity/v1internal:generateContent",
			requestBody:  `{"model":"gemini-2.5-pro","request":{"generationConfig":{"responseSchema":"` + strings.Repeat("x", 70<<10) + `","thinkingConfig":{"thinkingLevel":"HIGH"}}}}`,
			responseBody: `{"response":{"responseId":"g3","usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5}}}`,
			want:         "high",
		},
		{
			name:         "unrecognized effort ignored",
			path:         "/v1/responses",
			requestBody:  `{"model":"gpt-5.4","reasoning":{"effort":"secret-prompt"}}`,
			responseBody: `{"id":"resp_3","model":"gpt-5.4","usage":{"input_tokens":10,"output_tokens":5}}`,
			want:         "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.responseBody)
			}))
			defer upstream.Close()
			records := make(chan Usage, 1)
			base := parseURL(t, upstream.URL)
			var handler http.Handler
			if tc.antigravity {
				h := NewAntigravity(*base, func(u Usage) { records <- u })
				handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					h.ServeHTTP(w, r.WithContext(WithClient(r.Context(), "client-1")))
				})
			} else {
				handler = testProxy(base, records)
			}
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.requestBody))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d", w.Code)
			}
			got := nextRecord(t, records)
			if got.ReasoningLevel != tc.want {
				t.Fatalf("ReasoningLevel = %q, want %q", got.ReasoningLevel, tc.want)
			}
			if tc.wantModel != "" && got.Model != tc.wantModel {
				t.Fatalf("Model = %q, want %q", got.Model, tc.wantModel)
			}
		})
	}
}

func TestHTTPProxyHandlesCompressedJSONRequests(t *testing.T) {
	gzipBytes := func(t *testing.T, plain string) []byte {
		t.Helper()
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := io.WriteString(zw, plain); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}

	antigravityJSON := `{"model":"gemini-4-neon","request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"HIGH"}}}}`
	openaiJSON := `{"model":"gpt-5.4","reasoning":{"effort":"medium"}}`

	tests := []struct {
		name         string
		antigravity  bool
		path         string
		encodings    []string
		body         []byte
		responseBody string
		wantLevel    string
		wantModel    string
	}{
		{
			name:         "gzip antigravity request extracts level and forwards compressed bytes",
			antigravity:  true,
			path:         "/antigravity/v1internal:streamGenerateContent",
			encodings:    []string{"gzip"},
			body:         gzipBytes(t, antigravityJSON),
			responseBody: `{"response":{"responseId":"g-gz","usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5}}}`,
			wantLevel:    "high",
			wantModel:    "gemini-4-neon",
		},
		{
			name:         "identity openai request extracts level",
			path:         "/v1/responses",
			encodings:    []string{"Identity"},
			body:         []byte(openaiJSON),
			responseBody: `{"id":"resp_id","usage":{"input_tokens":10,"output_tokens":5}}`,
			wantLevel:    "medium",
			wantModel:    "gpt-5.4",
		},
		{
			name:         "corrupt gzip request still forwards without failing",
			antigravity:  true,
			path:         "/antigravity/v1internal:generateContent",
			encodings:    []string{"gzip"},
			body:         []byte("not-actually-gzip-bytes"),
			responseBody: `{"response":{"responseId":"g-bad","usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}}`,
			wantLevel:    "",
			wantModel:    "",
		},
		{
			name:         "stacked gzip encoding skipped safely",
			path:         "/v1/responses",
			encodings:    []string{"gzip", "gzip"},
			body:         gzipBytes(t, openaiJSON),
			responseBody: `{"id":"resp_stack","model":"gpt-5.4","usage":{"input_tokens":10,"output_tokens":5}}`,
			wantLevel:    "",
			wantModel:    "gpt-5.4",
		},
		{
			name:         "unsupported br encoding skipped safely",
			path:         "/v1/responses",
			encodings:    []string{"br"},
			body:         []byte("compressed-br-payload"),
			responseBody: `{"id":"resp_br","model":"gpt-5.4","usage":{"input_tokens":10,"output_tokens":5}}`,
			wantLevel:    "",
			wantModel:    "gpt-5.4",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var received []byte
			var receivedEncoding []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var err error
				received, err = io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read upstream body: %v", err)
				}
				receivedEncoding = append([]string(nil), r.Header.Values("Content-Encoding")...)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.responseBody)
			}))
			defer upstream.Close()

			records := make(chan Usage, 1)
			base := parseURL(t, upstream.URL)
			var handler http.Handler
			if tc.antigravity {
				h := NewAntigravity(*base, func(u Usage) { records <- u })
				handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					h.ServeHTTP(w, r.WithContext(WithClient(r.Context(), "client-1")))
				})
			} else {
				handler = testProxy(base, records)
			}

			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			for _, enc := range tc.encodings {
				req.Header.Add("Content-Encoding", enc)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d", w.Code)
			}
			if !bytes.Equal(received, tc.body) {
				t.Fatalf("upstream body was altered: got %d bytes, want %d bytes", len(received), len(tc.body))
			}
			if len(receivedEncoding) != len(tc.encodings) {
				t.Fatalf("upstream Content-Encoding = %v, want %v", receivedEncoding, tc.encodings)
			}
			got := nextRecord(t, records)
			if got.ReasoningLevel != tc.wantLevel || got.Model != tc.wantModel {
				t.Fatalf("got level=%q model=%q, want level=%q model=%q", got.ReasoningLevel, got.Model, tc.wantLevel, tc.wantModel)
			}
		})
	}

	t.Run("gzip request with unreachable upstream exits cleanly", func(t *testing.T) {
		closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		closedURL := closed.URL
		closed.Close()

		records := make(chan Usage, 1)
		handler := testProxy(parseURL(t, closedURL), records)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(gzipBytes(t, openaiJSON)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Content-Encoding", "gzip")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", w.Code)
		}
		got := nextRecord(t, records)
		if got.Status != http.StatusBadGateway {
			t.Fatalf("recorded status = %d, want 502", got.Status)
		}
	})
}

func TestServiceRequestsSkippedAndUnparsed(t *testing.T) {
	serviceRoutes := []struct {
		name        string
		antigravity bool
		method      string
		path        string
		status      int
		contentType string
		respBody    string
	}{
		{"list models", false, http.MethodGet, "/v1/models", http.StatusOK, "application/json", `{"data":[{"id":"gpt-5.4"}]}`},
		{"get model", false, http.MethodGet, "/v1/models/gpt-5.4", http.StatusOK, "application/json", `{"id":"gpt-5.4"}`},
		{"get usage", false, http.MethodGet, "/v1/usage", http.StatusOK, "application/json", `{"usage":{"input_tokens":999}}`},
		{"get responses list", false, http.MethodGet, "/v1/responses", http.StatusOK, "application/json", `{"data":[]}`},
		{"get response input items", false, http.MethodGet, "/v1/responses/resp_1/input_items", http.StatusOK, "application/json", `{"data":[]}`},
		{"delete response", false, http.MethodDelete, "/v1/responses/resp_1", http.StatusOK, "application/json", `{"deleted":true}`},
		{"service error status", false, http.MethodGet, "/v1/models", http.StatusInternalServerError, "application/json", `{"error":"upstream failure"}`},
		{"antigravity loadCodeAssist", true, http.MethodPost, "/antigravity/v1internal:loadCodeAssist", http.StatusOK, "application/json", `{"usageMetadata":{"promptTokenCount":999}}`},
		{"antigravity fetchAvailableModels", true, http.MethodPost, "/antigravity/v1internal:fetchAvailableModels", http.StatusOK, "application/json", `{"models":[]}`},
		{"antigravity fetchUserInfo", true, http.MethodPost, "/antigravity/v1internal:fetchUserInfo", http.StatusOK, "application/json", `{"email":"user@example.test"}`},
		{"antigravity retrieveUserQuotaSummary", true, http.MethodPost, "/antigravity/v1internal:retrieveUserQuotaSummary", http.StatusOK, "application/json", `{"buckets":[]}`},
		{"antigravity listExperiments", true, http.MethodPost, "/antigravity/v1internal:listExperiments", http.StatusOK, "application/json", `{"experiments":[]}`},
		{"antigravity fetchAdminControls", true, http.MethodPost, "/antigravity/v1internal:fetchAdminControls", http.StatusOK, "application/json", `{"controls":{}}`},
		{"antigravity writeTrajectoryAcls", true, http.MethodPost, "/antigravity/v1internal:writeTrajectoryAcls", http.StatusOK, "application/json", `{}`},
		{"antigravity service SSE", true, http.MethodPost, "/antigravity/v1internal:loadCodeAssist", http.StatusOK, "text/event-stream", "data: {\"response\":{\"usageMetadata\":{\"promptTokenCount\":99}}}\n\n"},
		{"antigravity service error", true, http.MethodPost, "/antigravity/v1internal:fetchAvailableModels", http.StatusServiceUnavailable, "application/json", `{"error":"unavailable"}`},
		{"antigravity GET generateContent", true, http.MethodGet, "/antigravity/v1internal:generateContent", http.StatusMethodNotAllowed, "application/json", `{}`},
	}

	for _, tc := range serviceRoutes {
		t.Run(tc.name, func(t *testing.T) {
			const reqBody = `{"model":"should-not-be-parsed"}`
			upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.method == http.MethodPost {
					body, err := io.ReadAll(r.Body)
					if err != nil || string(body) != reqBody {
						t.Errorf("upstream body = %q, err %v", body, err)
					}
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.respBody)
			}))
			defer upstreamServer.Close()

			records := make(chan Usage, 1)
			base := parseURL(t, upstreamServer.URL)
			var route *upstream
			if tc.antigravity {
				route = newUpstream(AntigravityPrefix, *base, func(u Usage) { records <- u })
			} else {
				route = newUpstream(apiPrefix, *base, func(u Usage) { records <- u })
			}
			// Verify no request/response metadata parser is attached for service routes.
			origModify := route.http.ModifyResponse
			route.http.ModifyResponse = func(resp *http.Response) error {
				if err := origModify(resp); err != nil {
					return err
				}
				meter := resp.Request.Context().Value(meterContextKey{}).(*httpMeter)
				if meter.request != nil || meter.requestCloser != nil || meter.response != nil || meter.sse != nil {
					t.Errorf("service route attached body parser: req=%v resp=%v sse=%v", meter.request != nil, meter.response != nil, meter.sse != nil)
				}
				if _, wrapped := resp.Body.(*observedBody); wrapped {
					t.Errorf("service route wrapped response body")
				}
				return nil
			}

			var body io.Reader
			if tc.method == http.MethodPost {
				body = strings.NewReader(reqBody)
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			req = req.WithContext(WithClient(req.Context(), "client-1"))
			if tc.method == http.MethodPost {
				req.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			route.serve(w, req)
			if w.Code != tc.status || w.Body.String() != tc.respBody {
				t.Fatalf("response = %d %q, want %d %q", w.Code, w.Body.String(), tc.status, tc.respBody)
			}
			assertNoRecord(t, records)
		})
	}

	t.Run("service route with rate limits emits quota-only update", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`)
		}))
		defer upstream.Close()
		records := make(chan Usage, 1)
		route := newUpstream(apiPrefix, *parseURL(t, upstream.URL), func(u Usage) { records <- u })
		origModify := route.http.ModifyResponse
		route.http.ModifyResponse = func(resp *http.Response) error {
			if err := origModify(resp); err != nil {
				return err
			}
			meter := resp.Request.Context().Value(meterContextKey{}).(*httpMeter)
			meter.usage.RateLimits = []RateLimit{{ID: "codex", Window: "primary", WindowMinutes: 300, UsedPercent: 40}}
			return nil
		}
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req = req.WithContext(WithClient(req.Context(), "client-1"))
		w := httptest.NewRecorder()
		route.serve(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		got := nextRecord(t, records)
		if got.Status != 0 || len(got.RateLimits) != 1 || got.RateLimits[0].UsedPercent != 40 {
			t.Fatalf("expected quota-only update with status 0, got %+v", got)
		}
	})

	t.Run("service route unreachable upstream is skipped", func(t *testing.T) {
		closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		closedURL := closed.URL
		closed.Close()

		records := make(chan Usage, 1)
		handler := testProxy(parseURL(t, closedURL), records)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
		if w.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", w.Code)
		}
		assertNoRecord(t, records)
	})
}

func TestGenerationRequestsIncludingErrorsAreRecorded(t *testing.T) {
	cases := []struct {
		name        string
		antigravity bool
		method      string
		path        string
		status      int
		body        string
	}{
		{"responses 400", false, http.MethodPost, "/v1/responses", http.StatusBadRequest, `{"model":"gpt-5.4"}`},
		{"responses 429", false, http.MethodPost, "/v1/responses", http.StatusTooManyRequests, `{"model":"gpt-5.4"}`},
		{"chat completions 500", false, http.MethodPost, "/v1/chat/completions", http.StatusInternalServerError, `{"model":"gpt-5.4"}`},
		{"messages 429", false, http.MethodPost, "/v1/messages", http.StatusTooManyRequests, `{"model":"claude-sonnet-4-6"}`},
		{"post models 405", false, http.MethodPost, "/v1/models", http.StatusMethodNotAllowed, `{}`},
		{"post usage 400", false, http.MethodPost, "/v1/usage", http.StatusBadRequest, `{}`},
		{"gemini models generateContent 503", false, http.MethodPost, "/v1/models/gemini-2.5-pro:generateContent", http.StatusServiceUnavailable, `{}`},
		{"background response poll", false, http.MethodGet, "/v1/responses/resp_bg", http.StatusOK, ""},
		{"antigravity generateContent 429", true, http.MethodPost, "/antigravity/v1internal:generateContent", http.StatusTooManyRequests, `{"model":"gemini-4-neon"}`},
		{"antigravity streamGenerateContent 500", true, http.MethodPost, "/antigravity/v1internal:streamGenerateContent", http.StatusInternalServerError, `{"model":"gemini-4-neon"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"error":"fixture"}`)
			}))
			defer upstream.Close()

			records := make(chan Usage, 1)
			base := parseURL(t, upstream.URL)
			var handler http.Handler
			if tc.antigravity {
				h := NewAntigravity(*base, func(u Usage) { records <- u })
				handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					h.ServeHTTP(w, r.WithContext(WithClient(r.Context(), "client-1")))
				})
			} else {
				handler = testProxy(base, records)
			}

			var reqBody io.Reader
			if tc.body != "" {
				reqBody = strings.NewReader(tc.body)
			}
			req := httptest.NewRequest(tc.method, tc.path, reqBody)
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			got := nextRecord(t, records)
			if got.Status != tc.status || got.Endpoint != tc.path || got.Method != tc.method {
				t.Fatalf("unexpected record: %+v", got)
			}
		})
	}
}

func TestIsServiceRequest(t *testing.T) {
	tests := []struct {
		name  string
		usage Usage
		want  bool
	}{
		{"GET /v1/models", Usage{Method: http.MethodGet, Endpoint: "/v1/models", Status: 200}, true},
		{"GET /v1/models/:id", Usage{Method: http.MethodGet, Endpoint: "/v1/models/:id", Status: 200}, true},
		{"GET /v1/models/gpt-5.4", Usage{Method: http.MethodGet, Endpoint: "/v1/models/gpt-5.4", Status: 200}, true},
		{"GET /v1/usage", Usage{Method: http.MethodGet, Endpoint: "/v1/usage", Status: 200}, true},
		{"POST /v1/models", Usage{Method: http.MethodPost, Endpoint: "/v1/models", Status: 405}, false},
		{"POST /v1/usage", Usage{Method: http.MethodPost, Endpoint: "/v1/usage", Status: 405}, false},
		{"POST /v1/models/:id gemini", Usage{Method: http.MethodPost, Endpoint: "/v1/models/:id", Status: 200}, false},
		{"POST /v1/responses", Usage{Method: http.MethodPost, Endpoint: "/v1/responses", Status: 500}, false},
		{"POST /v1/chat/completions", Usage{Method: http.MethodPost, Endpoint: "/v1/chat/completions", Status: 429}, false},
		{"POST /v1 fallback", Usage{Method: http.MethodPost, Endpoint: "/v1", Status: 200}, false},
		{"GET /v1 fallback", Usage{Method: http.MethodGet, Endpoint: "/v1", Status: 200}, true},
		{"GET /v1/responses/:id usage update", Usage{Method: http.MethodGet, Endpoint: "/v1/responses/:id", Status: 200, UsageUpdate: true}, false},
		{"GET /v1/responses/:id without usage update", Usage{Method: http.MethodGet, Endpoint: "/v1/responses/:id", Status: 200, UsageUpdate: false}, true},
		{"DELETE /v1/responses/:id", Usage{Method: http.MethodDelete, Endpoint: "/v1/responses/:id", Status: 200}, true},
		{"GET /v1/responses plain HTTP 200", Usage{Method: http.MethodGet, Endpoint: "/v1/responses", Status: 200}, true},
		{"GET /v1/responses plain HTTP 200 with model header", Usage{Method: http.MethodGet, Endpoint: "/v1/responses", Status: 200, Model: "gpt-5.4"}, true},
		{"GET /v1/responses websocket 101", Usage{Method: http.MethodGet, Endpoint: "/v1/responses", Status: http.StatusSwitchingProtocols}, false},
		{"GET /v1/responses websocket error", Usage{Method: http.MethodGet, Endpoint: "/v1/responses", Status: 400, Model: "gpt-ws"}, false},
		{"GET /v1/realtime websocket", Usage{Method: http.MethodGet, Endpoint: "/v1/realtime", Status: http.StatusSwitchingProtocols}, false},
		{"POST antigravity generateContent", Usage{Method: http.MethodPost, Endpoint: "/antigravity/v1internal:generateContent", Status: 200}, false},
		{"POST antigravity streamGenerateContent", Usage{Method: http.MethodPost, Endpoint: "/antigravity/v1internal:streamGenerateContent", Status: 500}, false},
		{"POST antigravity loadCodeAssist", Usage{Method: http.MethodPost, Endpoint: "/antigravity/v1internal:loadCodeAssist", Status: 200}, true},
		{"POST antigravity fetchAvailableModels", Usage{Method: http.MethodPost, Endpoint: "/antigravity/v1internal:fetchAvailableModels", Status: 200}, true},
		{"POST antigravity fetchUserInfo", Usage{Method: http.MethodPost, Endpoint: "/antigravity/v1internal:fetchUserInfo", Status: 200}, true},
		{"POST antigravity retrieveUserQuotaSummary", Usage{Method: http.MethodPost, Endpoint: "/antigravity/v1internal:retrieveUserQuotaSummary", Status: 200}, true},
		{"POST antigravity listExperiments", Usage{Method: http.MethodPost, Endpoint: "/antigravity/v1internal:listExperiments", Status: 200}, true},
		{"POST antigravity fetchAdminControls", Usage{Method: http.MethodPost, Endpoint: "/antigravity/v1internal:fetchAdminControls", Status: 200}, true},
		{"POST antigravity writeTrajectoryAcls", Usage{Method: http.MethodPost, Endpoint: "/antigravity/v1internal:writeTrajectoryAcls", Status: 200}, true},
		{"POST antigravity fallback", Usage{Method: http.MethodPost, Endpoint: "/antigravity", Status: 200}, true},
		{"GET antigravity generateContent", Usage{Method: http.MethodGet, Endpoint: "/antigravity/v1internal:generateContent", Status: 405}, true},
		{"telemetry claude", Usage{Method: http.MethodPost, Endpoint: "/claude-code/api-request", Status: 200}, false},
		{"telemetry codex", Usage{Method: http.MethodPost, Endpoint: "/codex/telemetry", Status: 200}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsServiceRequest(tc.usage); got != tc.want {
				t.Fatalf("IsServiceRequest(%+v) = %v, want %v", tc.usage, got, tc.want)
			}
		})
	}
}

func testProxy(api *url.URL, records chan<- Usage) http.Handler {
	proxy := New(api, func(usage Usage) { records <- usage })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r.WithContext(WithClient(r.Context(), "client-1")))
	})
}

func assertNoRecord(t *testing.T, records <-chan Usage) {
	t.Helper()
	select {
	case record := <-records:
		t.Fatalf("unexpected usage record: %+v", record)
	default:
	}
}

func nextRecord(t *testing.T, records <-chan Usage) Usage {
	t.Helper()
	select {
	case record := <-records:
		return record
	case <-time.After(4 * time.Second):
		t.Fatal("expected a usage record")
		return Usage{}
	}
}

func parseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
