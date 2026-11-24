package telemetry

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mishankov/llm-meter/internal/usagereport"
	tracecollector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestTraceExportsNormalizeIDsAndMeterOpenCode(t *testing.T) {
	const traceID, spanID = "0123456789abcdef0123456789abcdef", "1234567890abcdef"
	at := time.Now().UTC()
	fixture := fmt.Sprintf(`{"resourceSpans":[{
	  "resource":{"attributes":[{"key":"service.name","value":{"stringValue":"opencode"}}]},
	  "scopeSpans":[{"spans":[
	    {"traceId":"%s","spanId":"%s","name":"SessionStep.attempt","endTimeUnixNano":"%d",
	     "attributes":[
	       {"key":"gen_ai.provider.name","value":{"stringValue":"opencode-go"}},
	       {"key":"gen_ai.request.model","value":{"stringValue":"kimi-k2.5"}},
	       {"key":"gen_ai.usage.input_tokens","value":{"intValue":"30"}},
	       {"key":"gen_ai.usage.output_tokens","value":{"intValue":"10"}},
	       {"key":"gen_ai.usage.cache_read.input_tokens","value":{"intValue":"6"}},
	       {"key":"gen_ai.usage.cache_creation.input_tokens","value":{"intValue":"3"}},
	       {"key":"opencode.usage.cost_usd","value":{"doubleValue":0.02}},
	       {"key":"gen_ai.input.messages","value":{"stringValue":"private-fixture"}}
	     ]},
	    {"traceId":"%s","spanId":"%s","name":"LLM.stream",
	     "attributes":[{"key":"gen_ai.usage.input_tokens","value":{"intValue":"30"}}]},
	    {"traceId":"%s","spanId":"%s","name":"SessionStep.attempt","endTimeUnixNano":"%d",
	     "status":{"code":2},"attributes":[
	       {"key":"gen_ai.provider.name","value":{"stringValue":"opencode-go"}},
	       {"key":"gen_ai.request.model","value":{"stringValue":"kimi-k2.5"}},
	       {"key":"gen_ai.usage.input_tokens","value":{"intValue":"30"}},
	       {"key":"gen_ai.usage.output_tokens","value":{"intValue":"10"}}
	     ]}
	  ]}]
	}]}`, traceID, spanID, at.UnixNano(), traceID, spanID, traceID, spanID, at.UnixNano())

	var export tracecollector.ExportTraceServiceRequest
	if err := protojson.Unmarshal([]byte(fixture), &export); err != nil {
		t.Fatal(err)
	}
	for _, span := range export.GetResourceSpans()[0].GetScopeSpans()[0].GetSpans() {
		span.TraceId, _ = hex.DecodeString(traceID)
		span.SpanId, _ = hex.DecodeString(spanID)
	}
	binary, err := proto.Marshal(&export)
	if err != nil {
		t.Fatal(err)
	}

	var results []usagereport.Event
	handler := Handler(func(_ context.Context, payloads [][]byte) error {
		if len(payloads) != 1 {
			t.Fatalf("queued %d events, want one model attempt", len(payloads))
		}
		for _, private := range []string{traceID, spanID, "private-fixture"} {
			if bytes.Contains(payloads[0], []byte(private)) {
				t.Fatalf("queued private telemetry %q", private)
			}
		}
		event, err := usagereport.Decode(bytes.NewReader(payloads[0]))
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, event)
		return nil
	}, nil)
	for _, request := range []struct {
		contentType string
		body        []byte
	}{
		{mediaJSON, []byte(fixture)},
		{mediaProtobuf, binary},
	} {
		r := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(request.body))
		r.Header.Set("Content-Type", request.contentType)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != request.contentType {
			t.Fatalf("unexpected export response: %d %q", w.Code, w.Body.String())
		}
	}
	if len(results) != 2 || results[0].ID != results[1].ID {
		t.Fatalf("the same span needs the same ID in JSON and protobuf: %#v", results)
	}
	event := results[0]
	if event.Provider != "opencode-go" || event.Endpoint != usagereport.OpenCodeTelemetryEndpoint ||
		event.InputTokens != 30 || event.CachedInputTokens != 6 || event.CacheWriteTokens != 3 || event.OutputTokens != 10 ||
		event.ReportedCost != nil || !event.At.Equal(at) {
		t.Fatalf("unexpected OpenCode usage: %+v", event)
	}
}

func TestTelemetryBackpressureReturnsRetryableStatus(t *testing.T) {
	handler := Handler(func(context.Context, [][]byte) error {
		return fmt.Errorf("workspace-path-fixture")
	}, nil)
	fixture := fmt.Sprintf(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[{
	  "eventName":"claude_code.api_request","timeUnixNano":"%d","attributes":[
	    {"key":"model","value":{"stringValue":"claude-fixture"}},
	    {"key":"input_tokens","value":{"intValue":"5"}},
	    {"key":"output_tokens","value":{"intValue":"2"}}
	  ]
	}]}]}]}`, time.Now().UnixNano())
	request := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader(fixture))
	request.Header.Set("Content-Type", mediaJSON)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "workspace-path-fixture") {
		t.Fatalf("unexpected response on backpressure: %d %q", response.Code, response.Body.String())
	}
}
