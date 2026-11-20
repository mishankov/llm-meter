package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenCodeGoReportsZeroChargeAndListValue(t *testing.T) {
	cases := []struct {
		name, path, contentType, payload, responseID string
	}{
		{
			name: "chat", path: "/chat/completions", contentType: "application/json", responseID: "chat-1",
			payload: `{"id":"chat-1","model":"fixture-model","usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0,"opencode":{"list_cost":0.005,"allowance":0.003}}}`,
		},
		{
			name: "responses stream", path: "/responses", contentType: "text/event-stream", responseID: "response-1",
			payload: "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"response-1\",\"model\":\"fixture-model\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"cost\":0,\"opencode\":{\"list_cost\":0.005,\"allowance\":0.003}}}}\n\n",
		},
		{
			name: "gemini", path: "/models/fixture-model:generateContent", contentType: "application/json", responseID: "gemini-1",
			payload: `{"responseId":"gemini-1","modelVersion":"fixture-model","usageMetadata":{"promptTokenCount":8,"toolUsePromptTokenCount":2,"candidatesTokenCount":3,"thoughtsTokenCount":2,"cost":0,"opencode":{"list_cost":0.005,"allowance":0.003}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = io.WriteString(w, tc.payload)
			}))
			defer upstream.Close()
			records := make(chan Usage, 2)
			handler := New(nil, nil, parseURL(t, upstream.URL+"/inference/go"), func(usage Usage) { records <- usage })
			req := httptest.NewRequest(http.MethodPost, OpenCodeGoPrefix+tc.path, nil)
			handler.ServeHTTP(httptest.NewRecorder(), req)
			usage := nextRecord(t, records)
			if usage.Provider != "opencode-go" || usage.Model != "fixture-model" || !usage.HasUsage ||
				usage.InputTokens != 10 || usage.OutputTokens != 5 || usage.ResponseID != "opencode-go:"+tc.responseID {
				t.Fatalf("incorrect attribution or token counts: %+v", usage)
			}
			assertReportedAmount(t, "charged", usage.ReportedCost, 0)
			assertReportedAmount(t, "list", usage.ReportedListCost, .005)
			assertReportedAmount(t, "allowance", usage.ReportedAllowance, .003)
			if usage.ReportedCostEstimated {
				t.Fatal("a reported zero charge was marked as an estimate")
			}
		})
	}
}

func TestWebSocketRecordsReportedCostWithoutTokens(t *testing.T) {
	var records []Usage
	meter := socketMeter{usage: Usage{Provider: "opencode-go", Status: http.StatusSwitchingProtocols}, record: func(usage Usage) {
		records = append(records, usage)
	}}
	meter.response(decodeMessage([]byte(`{"type":"response.created","response":{"id":"first"}}`)))
	meter.response(decodeMessage([]byte(`{"type":"response.completed","response":{"id":"first","usage":{"cost":0,"opencode":{"list_cost":0.005,"allowance":0.003}}}}`)))
	meter.finish()
	if len(records) != 1 || records[0].ResponseID != "first" || records[0].HasUsage {
		t.Fatalf("incorrect WebSocket record: %+v", records)
	}
	assertReportedAmount(t, "charged", records[0].ReportedCost, 0)
	assertReportedAmount(t, "list", records[0].ReportedListCost, .005)
	assertReportedAmount(t, "allowance", records[0].ReportedAllowance, .003)
}

func TestReportedCostsFollowVerifiedProviderSemantics(t *testing.T) {
	message := decodeMessage([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.01,"opencode":{"estimated":true,"list_cost":0,"allowance":0}}}`))
	var direct Usage
	direct.applyMessage(message, true)
	if !direct.HasUsage || direct.ReportedCost != nil || direct.ReportedListCost != nil || direct.ReportedAllowance != nil {
		t.Fatal("read undocumented amounts or discarded valid token counts on unknown route")
	}

	console := Usage{Provider: "opencode"}
	console.applyMessage(message, true)
	assertReportedAmount(t, "estimated", console.ReportedCost, .01)
	assertReportedAmount(t, "list", console.ReportedListCost, 0)
	assertReportedAmount(t, "allowance", console.ReportedAllowance, 0)
	if !console.ReportedCostEstimated {
		t.Fatal("missed BYOK estimated-cost flag")
	}

	router := Usage{Provider: "openrouter"}
	router.applyMessage(message, true)
	assertReportedAmount(t, "OpenRouter credits", router.ReportedCost, .01)
	if router.ReportedCostEstimated || router.ReportedListCost != nil || router.ReportedAllowance != nil {
		t.Fatal("applied OpenCode-specific fields to OpenRouter")
	}

	for _, malformed := range []string{"null", "-1", "1e320", `"not-a-number"`} {
		metadata := &metadataCapture{}
		_, _ = io.Copy(metadata, strings.NewReader(`{"usage":{"prompt_tokens":10,"cost":`+malformed+`}}`))
		usage := Usage{Provider: "opencode-go"}
		usage.applyMessage(metadata.message(), true)
		if !usage.HasUsage || usage.InputTokens != 10 || usage.ReportedCost != nil {
			t.Fatalf("bad cost field %q interfered with token counts", malformed)
		}
	}
}

func TestProviderInferredFromExactConfiguredHost(t *testing.T) {
	for address, want := range map[string]string{
		"https://api.openai.com/v1":            "openai",
		"https://openrouter.ai:443/api/v1":     "openrouter",
		"https://openrouter.ai.example/api/v1": "",
		"http://127.0.0.1/v1":                  "",
		"https://opencode.ai/inference/openai": "opencode",
		"https://opencode.ai/inference/go/v1":  "opencode-go",
		"https://opencode.ai/console/api/v1":   "",
		"https://api.deepseek.com/v1":          "deepseek",
	} {
		if got := upstreamProvider(apiPrefix, *parseURL(t, address)); got != want {
			t.Fatalf("%s provider = %q, want %q", address, got, want)
		}
	}
}

func assertReportedAmount(t *testing.T, name string, amount *float64, want float64) {
	t.Helper()
	if amount == nil {
		t.Fatalf("missing reported %s amount", name)
	}
	if *amount != want {
		t.Fatalf("reported %s amount = %v, want %v", name, *amount, want)
	}
}
