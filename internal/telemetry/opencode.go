package telemetry

import (
	"errors"
	"net/http"
	"time"

	"github.com/mishankov/llm-meter/internal/proxy"
	"github.com/mishankov/llm-meter/internal/usagereport"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

const openCodeAttemptSpan = "SessionStep.attempt"

var errInvalidOpenCodeUsage = errors.New("invalid OpenCode usage")

func openCodeUsage(resource *resourcepb.Resource, span *tracepb.Span) (usagereport.Event, bool, error) {
	if span.GetName() != openCodeAttemptSpan || attribute(resource.GetAttributes(), "service.name").GetStringValue() != "opencode" ||
		span.GetStatus().GetCode() == tracepb.Status_STATUS_CODE_ERROR {
		return usagereport.Event{}, false, nil
	}
	inputValue := attribute(span.GetAttributes(), "gen_ai.usage.input_tokens")
	outputValue := attribute(span.GetAttributes(), "gen_ai.usage.output_tokens")
	if inputValue == nil && outputValue == nil {
		return usagereport.Event{}, false, nil
	}
	input, inputOK := tokenCount(inputValue)
	output, outputOK := tokenCount(outputValue)
	cached, cachedOK := optionalTokenCount(attribute(span.GetAttributes(), "gen_ai.usage.cache_read.input_tokens"))
	writes, writesOK := optionalTokenCount(attribute(span.GetAttributes(), "gen_ai.usage.cache_creation.input_tokens"))
	model := attribute(span.GetAttributes(), "gen_ai.request.model").GetStringValue()
	ended := span.GetEndTimeUnixNano()
	if !inputOK || !outputOK || !cachedOK || !writesOK || cached+writes > input ||
		model == "" || ended == 0 || len(span.GetTraceId()) != 16 || len(span.GetSpanId()) != 8 {
		return usagereport.Event{}, true, errInvalidOpenCodeUsage
	}
	provider := attribute(span.GetAttributes(), "gen_ai.provider.name").GetStringValue()
	switch provider {
	case "openai", "opencode-go", "openrouter", "opencode", "anthropic", "deepseek", "groq", "mistral", "xai", "google":
	default:
		provider = ""
	}

	event, err := usagereport.NewEvent(proxy.Usage{
		At: time.Unix(int64(ended/1e9), int64(ended%1e9)), Provider: provider, Model: model,
		Endpoint: usagereport.OpenCodeTelemetryEndpoint, Method: http.MethodPost, Status: http.StatusOK, HasUsage: true,
		InputTokens: input, CachedInputTokens: cached, CacheWriteTokens: writes, OutputTokens: output,
	})
	if err != nil || event.Model == "" {
		return usagereport.Event{}, true, errInvalidOpenCodeUsage
	}
	// OpenCode uses the same cost attribute for catalog estimates and billed
	// amounts. Leave pricing to the ledger because the span cannot distinguish them.
	event.ID = stableEventID(openCodeAttemptSpan, string(span.GetTraceId())+string(span.GetSpanId()))
	return event, true, nil
}
