package telemetry

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/mishankov/llm-meter/internal/usagereport"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// Codex 0.171.0 sends this event shape after HTTP and WebSocket completions.
// Input and output use strings, while cache counts use OTLP integers.
const codexCompletionLog = `{
  "timeUnixNano": "0",
  "observedTimeUnixNano": "1700000000123456789",
  "eventName": "event otel/src/events/session_telemetry.rs:1187",
  "attributes": [
    {"key":"event.name","value":{"stringValue":"codex.sse_event"}},
    {"key":"event.kind","value":{"stringValue":"response.completed"}},
    {"key":"event.timestamp","value":{"stringValue":"2023-11-14T22:13:20.123Z"}},
    {"key":"conversation.id","value":{"stringValue":"conversation-fixture"}},
    {"key":"user.email","value":{"stringValue":"user-fixture@example.com"}},
    {"key":"model","value":{"stringValue":"gpt-5.4"}},
    {"key":"service_tier","value":{"stringValue":"priority"}},
    {"key":"input_token_count","value":{"stringValue":"17"}},
    {"key":"cached_token_count","value":{"intValue":"2"}},
    {"key":"cache_write_token_count","value":{"intValue":"3"}},
    {"key":"output_token_count","value":{"stringValue":"4"}},
    {"key":"reasoning_token_count","value":{"intValue":"1"}},
    {"key":"tool_token_count","value":{"stringValue":"21"}}
  ]
}`

func TestCodexCompletionUsage(t *testing.T) {
	for _, test := range []struct{ authMode, provider, source string }{
		{"ApiKey", "", "api"},
		{"Chatgpt", "codex", "codex"},
		{"custom", "", "api"},
		{"", "", "api"},
	} {
		t.Run(test.authMode, func(t *testing.T) {
			var record logspb.LogRecord
			if err := protojson.Unmarshal([]byte(codexCompletionLog), &record); err != nil {
				t.Fatal(err)
			}
			if test.authMode != "" {
				record.Attributes = append(record.Attributes, &commonpb.KeyValue{
					Key:   "auth_mode",
					Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: test.authMode}},
				})
			}
			event, matched, err := codexUsage(nil, &record)
			if err != nil || !matched {
				t.Fatalf("mapped Codex completion = %t, %v", matched, err)
			}
			if event.Endpoint != usagereport.CodexTelemetryEndpoint || event.Provider != test.provider ||
				event.Usage().Source != test.source || event.Model != "gpt-5.4" || event.ServiceTier != "priority" ||
				event.At != time.Unix(1_700_000_000, 123_456_789).UTC() || event.Status != 200 || !event.HasUsage ||
				event.InputTokens != 17 || event.CachedInputTokens != 2 || event.CacheWriteTokens != 3 || event.OutputTokens != 4 ||
				event.ReportedCost != nil {
				t.Fatalf("unexpected Codex usage event: %+v", event)
			}
			replay, _, err := codexUsage(nil, &record)
			if err != nil || replay.ID != event.ID {
				t.Fatal("replayed completion changed event ID")
			}
			record.ObservedTimeUnixNano++
			other, _, err := codexUsage(nil, &record)
			if err != nil || other.ID == event.ID {
				t.Fatal("another completion with the same counts reused the event ID")
			}
			data, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte("fixture")) || event.ResponseIDHash != "" {
				t.Fatal("OTLP identities leaked into the usage event")
			}
			if _, err := usagereport.Decode(bytes.NewReader(data)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCodexUsageRejectsInvalidAndSkipsOtherEvents(t *testing.T) {
	var record logspb.LogRecord
	if err := protojson.Unmarshal([]byte(codexCompletionLog), &record); err != nil {
		t.Fatal(err)
	}
	for _, attr := range record.Attributes {
		if attr.Key == "cache_write_token_count" {
			attr.Value = &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 16}}
		}
	}
	if _, matched, err := codexUsage(nil, &record); !matched || err == nil {
		t.Fatalf("invalid cache breakdown was handled as %t, %v", matched, err)
	}
	for _, attr := range record.Attributes {
		if attr.Key == "event.kind" {
			attr.Value = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "response.output_text.delta"}}
		}
	}
	if _, matched, err := codexUsage(nil, &record); matched || err != nil {
		t.Fatalf("unrelated stream event was handled as %t, %v", matched, err)
	}
}

func TestCodexCompletionReasoningEffort(t *testing.T) {
	strAttr := func(key, value string) *commonpb.KeyValue {
		return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
	}
	tests := []struct {
		name          string
		resourceAttrs []*commonpb.KeyValue
		recordAttrs   []*commonpb.KeyValue
		want          string
	}{
		{
			name:        "model_reasoning_effort on completion log",
			recordAttrs: []*commonpb.KeyValue{strAttr("model_reasoning_effort", "high")},
			want:        "high",
		},
		{
			name:        "model_reasoning_effort normalizes case and xhigh",
			recordAttrs: []*commonpb.KeyValue{strAttr("model_reasoning_effort", " XHIGH ")},
			want:        "xhigh",
		},
		{
			name:        "record model_reasoning_effort beats reasoning_effort and resource",
			resourceAttrs: []*commonpb.KeyValue{
				strAttr("model_reasoning_effort", "low"),
				strAttr("reasoning_effort", "minimal"),
			},
			recordAttrs: []*commonpb.KeyValue{
				strAttr("reasoning_effort", "medium"),
				strAttr("model_reasoning_effort", "high"),
			},
			want: "high",
		},
		{
			name:        "reasoning_effort fallback on record",
			recordAttrs: []*commonpb.KeyValue{strAttr("reasoning_effort", "medium")},
			want:        "medium",
		},
		{
			name: "resource model_reasoning_effort beats resource reasoning_effort",
			resourceAttrs: []*commonpb.KeyValue{
				strAttr("reasoning_effort", "low"),
				strAttr("model_reasoning_effort", "minimal"),
			},
			want: "minimal",
		},
		{
			name:        "unrecognized effort stays empty without rejecting event",
			recordAttrs: []*commonpb.KeyValue{strAttr("model_reasoning_effort", "custom-effort")},
			want:        "",
		},
		{
			name: "absent effort stays empty",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var record logspb.LogRecord
			if err := protojson.Unmarshal([]byte(codexCompletionLog), &record); err != nil {
				t.Fatal(err)
			}
			record.Attributes = append(record.Attributes, strAttr("auth_mode", "Chatgpt"))
			record.Attributes = append(record.Attributes, tc.recordAttrs...)
			var resource *resourcepb.Resource
			if len(tc.resourceAttrs) > 0 {
				resource = &resourcepb.Resource{Attributes: tc.resourceAttrs}
			}
			event, matched, err := codexUsage(resource, &record)
			if err != nil || !matched {
				t.Fatalf("codexUsage = matched:%t err:%v", matched, err)
			}
			if event.ReasoningLevel != tc.want {
				t.Fatalf("ReasoningLevel = %q, want %q", event.ReasoningLevel, tc.want)
			}
			data, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := usagereport.Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if decoded.ReasoningLevel != tc.want {
				t.Fatalf("round-tripped ReasoningLevel = %q, want %q", decoded.ReasoningLevel, tc.want)
			}
		})
	}
}
