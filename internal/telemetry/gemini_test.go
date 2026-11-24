package telemetry

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mishankov/llm-meter/internal/usagereport"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestGeminiCLIUsage(t *testing.T) {
	at := time.Date(2025, time.October, 15, 2, 3, 4, 123000000, time.UTC)
	record := &logspb.LogRecord{}
	if err := protojson.Unmarshal([]byte(`{
		"body":{"stringValue":"API response from gemini-3-flash-preview. Status: 200. Duration: 52ms."},
		"attributes":[
			{"key":"event.name","value":{"stringValue":"gemini_cli.api_response"}},
			{"key":"event.timestamp","value":{"stringValue":"2025-10-15T02:03:04.123Z"}},
			{"key":"model","value":{"stringValue":"gemini-3-flash-preview"}},
			{"key":"status_code","value":{"stringValue":"200"}},
			{"key":"prompt_id","value":{"stringValue":"private-prompt-id"}},
			{"key":"duration_ms","value":{"intValue":"52"}},
			{"key":"input_token_count","value":{"intValue":"24"}},
			{"key":"output_token_count","value":{"intValue":"9"}},
			{"key":"cached_content_token_count","value":{"intValue":"10"}},
			{"key":"tool_token_count","value":{"intValue":"7"}},
			{"key":"thoughts_token_count","value":{"intValue":"5"}},
			{"key":"total_token_count","value":{"intValue":"45"}},
			{"key":"response_text","value":{"stringValue":"private-prompt-content"}}
		]
	}`), record); err != nil {
		t.Fatal(err)
	}
	resource := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
		{Key: "session.id", Value: &commonpb.AnyValue{
			Value: &commonpb.AnyValue_StringValue{StringValue: "private-session-id"},
		}},
	}}
	event, matched, err := geminiUsage(resource, record)
	if err != nil || !matched {
		t.Fatalf("Gemini CLI event: matched=%v, err=%v", matched, err)
	}
	if !event.At.Equal(at) || event.Provider != "google" || event.Model != "gemini-3-flash-preview" ||
		event.Endpoint != usagereport.GeminiTelemetryEndpoint || event.Status != http.StatusOK || !event.HasUsage ||
		event.InputTokens != 31 || event.CachedInputTokens != 10 || event.OutputTokens != 14 || event.ReportedCost != nil {
		t.Fatalf("Gemini CLI event = %+v", event)
	}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "private-") {
		t.Fatal("Gemini usage event contained private content or identifiers")
	}

	replayed, _, _ := geminiUsage(resource, proto.CloneOf(record))
	if replayed.ID != event.ID {
		t.Fatalf("replay ID = %q, want %q", replayed.ID, event.ID)
	}
	other := proto.CloneOf(record)
	other.TimeUnixNano = uint64(at.Add(time.Millisecond).UnixNano())
	next, _, _ := geminiUsage(resource, other)
	if next.ID == event.ID || next.ID == "" {
		t.Fatal("another response sharing a prompt ID did not get a distinct ID")
	}

	semantic := proto.CloneOf(record)
	semantic.Attributes[0].Value = &commonpb.AnyValue{
		Value: &commonpb.AnyValue_StringValue{StringValue: "gen_ai.client.inference.operation.details"},
	}
	if _, matched, err := geminiUsage(resource, semantic); matched || err != nil {
		t.Fatalf("semantic duplicate: matched=%v, err=%v", matched, err)
	}

	invalid := proto.CloneOf(record)
	invalid.Attributes[8].Value = &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 25}}
	if _, matched, err := geminiUsage(resource, invalid); !matched || err == nil {
		t.Fatalf("cached tokens exceeded prompt: matched=%v, err=%v", matched, err)
	}
}
