package telemetry

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mishankov/llm-meter/internal/usagereport"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestCopilotUsage(t *testing.T) {
	str := func(key, value string) *commonpb.KeyValue {
		return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
	}
	count := func(key string, value int64) *commonpb.KeyValue {
		return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: value}}}
	}
	at := time.Now().UTC()
	resource := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{str("service.name", "github-copilot")}}
	scope := &commonpb.InstrumentationScope{Name: "github.copilot"}
	chat := &tracepb.Span{
		Name:              "chat claude-haiku-4-5",
		Kind:              tracepb.Span_SPAN_KIND_CLIENT,
		TraceId:           []byte("trace-id-fixture"),
		SpanId:            []byte("span-id!"),
		StartTimeUnixNano: uint64(at.UnixNano()),
		Attributes: []*commonpb.KeyValue{
			str("gen_ai.operation.name", "chat"),
			str("gen_ai.provider.name", "anthropic"),
			str("gen_ai.request.model", "request-model"),
			str("gen_ai.response.model", "claude-haiku-4-5"),
			str("gen_ai.response.id", "raw-response-id"),
			str("gen_ai.input.messages", "user-content-fixture"),
			str("gen_ai.output.messages", "assistant-content-fixture"),
			count("gen_ai.usage.input_tokens", 15),
			count("gen_ai.usage.output_tokens", 4),
			count("gen_ai.usage.cache_read.input_tokens", 3),
			count("gen_ai.usage.cache_write.input_tokens", 2),
			count("gen_ai.usage.reasoning.output_tokens", 1),
			count("github.copilot.cost", 2),
			count("github.copilot.nano_aiu", 1000000000),
		},
	}
	event, matched, err := copilotUsage(resource, scope, chat)
	if !matched || err != nil {
		t.Fatalf("chat matched %v, error %v", matched, err)
	}
	if event.Provider != "github-copilot" || event.Endpoint != usagereport.CopilotTelemetryEndpoint || event.Model != "claude-haiku-4-5" ||
		!event.At.Equal(at) || event.InputTokens != 15 || event.OutputTokens != 4 ||
		event.CachedInputTokens != 3 || event.CacheWriteTokens != 2 ||
		event.ReportedCost != nil || event.ReportedAllowance != nil || event.ResponseIDHash != "" {
		t.Fatalf("unexpected Copilot event: %+v", event)
	}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"trace-id-fixture", "span-id!", "raw-response-id", "user-content-fixture", "assistant-content-fixture"} {
		if strings.Contains(string(payload), secret) {
			t.Errorf("usage upload included %q", secret)
		}
	}
	replayed, _, _ := copilotUsage(resource, scope, chat)
	if replayed.ID != event.ID {
		t.Fatal("the same Copilot span was assigned a different ID on replay")
	}
	second := proto.CloneOf(chat)
	second.SpanId = []byte("other-id")
	distinct, _, _ := copilotUsage(resource, scope, second)
	if distinct.ID == event.ID {
		t.Fatal("separate chat spans within one trace received the same ID")
	}

	for _, test := range []struct {
		name    string
		update  func(*tracepb.Span)
		matched bool
	}{
		{name: "parent usage", update: func(span *tracepb.Span) {
			span.Kind = tracepb.Span_SPAN_KIND_INTERNAL
			span.Attributes[0] = str("gen_ai.operation.name", "invoke_agent")
		}},
		{name: "failed chat", update: func(span *tracepb.Span) {
			span.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR}
		}},
		{name: "invalid cache subset", update: func(span *tracepb.Span) {
			span.Attributes = append(span.Attributes, count("gen_ai.usage.cache_write.input_tokens", 13))
		}, matched: true},
		{name: "invalid token type", update: func(span *tracepb.Span) {
			span.Attributes = append(span.Attributes, str("gen_ai.usage.input_tokens", "invalid"))
		}, matched: true},
		{name: "no usage", update: func(span *tracepb.Span) {
			span.Attributes = slices.DeleteFunc(span.Attributes, func(attribute *commonpb.KeyValue) bool {
				return strings.HasPrefix(attribute.GetKey(), "gen_ai.usage.")
			})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			span := proto.CloneOf(chat)
			test.update(span)
			_, matched, err := copilotUsage(resource, scope, span)
			if matched != test.matched || (err != nil) != test.matched {
				t.Fatalf("chat matched %v, error %v, want matched/invalid %v", matched, err, test.matched)
			}
		})
	}

	if _, matched, _ := copilotUsage(nil, &commonpb.InstrumentationScope{Name: "other"}, chat); matched {
		t.Fatal("accepted a chat span from an unrelated instrumentor")
	}
	if _, matched, err := copilotUsage(resource, &commonpb.InstrumentationScope{Name: "custom"}, chat); !matched || err != nil {
		t.Fatalf("rejected Copilot service with a custom scope: matched %v, error %v", matched, err)
	}
}
