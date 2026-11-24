package telemetry

import (
	"errors"
	"net/http"
	"time"

	"github.com/mishankov/llm-meter/internal/proxy"
	"github.com/mishankov/llm-meter/internal/usagereport"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

const copilotProvider = "github-copilot"

var errInvalidCopilotUsage = errors.New("invalid GitHub Copilot usage")

func copilotUsage(resource *resourcepb.Resource, scope *commonpb.InstrumentationScope, span *tracepb.Span) (usagereport.Event, bool, error) {
	if span.GetKind() != tracepb.Span_SPAN_KIND_CLIENT {
		return usagereport.Event{}, false, nil
	}
	if scope.GetName() != "github.copilot" && attribute(resource.GetAttributes(), "service.name").GetStringValue() != copilotProvider {
		return usagereport.Event{}, false, nil
	}

	fields := make(map[string]*commonpb.AnyValue, len(span.GetAttributes()))
	for _, attribute := range span.GetAttributes() {
		fields[attribute.GetKey()] = attribute.GetValue()
	}
	if fields["gen_ai.operation.name"].GetStringValue() != "chat" ||
		span.GetStatus().GetCode() == tracepb.Status_STATUS_CODE_ERROR || fields["error.type"].GetStringValue() != "" {
		return usagereport.Event{}, false, nil
	}

	inputField, outputField := fields["gen_ai.usage.input_tokens"], fields["gen_ai.usage.output_tokens"]
	if inputField == nil && outputField == nil {
		return usagereport.Event{}, false, nil
	}
	input, validInput := tokenCount(inputField)
	output, validOutput := tokenCount(outputField)
	read, validRead := optionalTokenCount(fields["gen_ai.usage.cache_read.input_tokens"])
	write, validWrite := optionalTokenCount(fields["gen_ai.usage.cache_write.input_tokens"])
	if !validInput || !validOutput || !validRead || !validWrite || read > input || write > input-read {
		return usagereport.Event{}, true, errInvalidCopilotUsage
	}

	model := fields["gen_ai.response.model"].GetStringValue()
	if model == "" {
		model = fields["gen_ai.request.model"].GetStringValue()
	}
	if span.GetStartTimeUnixNano() == 0 || len(span.GetTraceId()) != 16 || len(span.GetSpanId()) != 8 {
		return usagereport.Event{}, true, errInvalidCopilotUsage
	}
	ts := span.GetStartTimeUnixNano()
	event, err := usagereport.NewEvent(proxy.Usage{
		At:       time.Unix(int64(ts/1e9), int64(ts%1e9)),
		Provider: copilotProvider,
		Model:    model,
		Endpoint: usagereport.CopilotTelemetryEndpoint,
		Method:   http.MethodPost,
		Status:   http.StatusOK,
		HasUsage: true,
		// Copilot's OpenTelemetry totals already include cache and reasoning tokens.
		InputTokens:       input,
		CachedInputTokens: read,
		CacheWriteTokens:  write,
		OutputTokens:      output,
	})
	if err != nil || event.Model == "" {
		return usagereport.Event{}, true, errInvalidCopilotUsage
	}

	event.ID = stableEventID(copilotProvider, string(span.GetTraceId())+string(span.GetSpanId()))
	return event, true, nil
}
