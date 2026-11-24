package telemetry

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mishankov/llm-meter/internal/proxy"
	"github.com/mishankov/llm-meter/internal/usagereport"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

const codexCompletionEvent = "codex.sse_event"

var errInvalidCodexUsage = errors.New("invalid Codex usage")

func codexUsage(resource *resourcepb.Resource, record *logspb.LogRecord) (usagereport.Event, bool, error) {
	if record.GetEventName() != codexCompletionEvent && attribute(record.GetAttributes(), "event.name").GetStringValue() != codexCompletionEvent {
		return usagereport.Event{}, false, nil
	}
	fields := make(map[string]*commonpb.AnyValue, len(record.GetAttributes()))
	for _, attr := range record.GetAttributes() {
		fields[attr.GetKey()] = attr.GetValue()
	}
	// Codex emits this completion event for both HTTP and WebSocket responses.
	if fields["event.kind"].GetStringValue() != "response.completed" ||
		fields["error.message"] != nil || (fields["input_token_count"] == nil && fields["output_token_count"] == nil) {
		return usagereport.Event{}, false, nil
	}

	input, validInput := tokenCount(fields["input_token_count"])
	output, validOutput := tokenCount(fields["output_token_count"])
	cached, validCached := optionalTokenCount(fields["cached_token_count"])
	written, validWritten := optionalTokenCount(fields["cache_write_token_count"])
	// Input already includes both cache categories, and output includes reasoning.
	if !validInput || !validOutput || !validCached || !validWritten ||
		cached > input || written > input-cached {
		return usagereport.Event{}, true, errInvalidCodexUsage
	}

	model, authMode := fields["model"].GetStringValue(), fields["auth_mode"].GetStringValue()
	for _, attr := range resource.GetAttributes() {
		switch {
		case model == "" && attr.GetKey() == "model":
			model = attr.GetValue().GetStringValue()
		case authMode == "" && attr.GetKey() == "auth_mode":
			authMode = attr.GetValue().GetStringValue()
		}
	}
	// API-key mode can send requests to custom endpoints and does not identify their provider.
	var provider string
	switch strings.ToLower(authMode) {
	case "chatgpt", "chatgptauthtokens", "swic":
		provider = "codex"
	}

	nanos := record.GetTimeUnixNano()
	if nanos == 0 {
		nanos = record.GetObservedTimeUnixNano()
	}
	at := time.Unix(int64(nanos/1e9), int64(nanos%1e9))
	if nanos == 0 {
		at, _ = time.Parse(time.RFC3339Nano, fields["event.timestamp"].GetStringValue())
	}
	event, err := usagereport.NewEvent(proxy.Usage{
		At:                at,
		Provider:          provider,
		Model:             model,
		Endpoint:          usagereport.CodexTelemetryEndpoint,
		Method:            http.MethodPost,
		Status:            http.StatusOK,
		HasUsage:          true,
		InputTokens:       input,
		CachedInputTokens: cached,
		CacheWriteTokens:  written,
		OutputTokens:      output,
		ServiceTier:       fields["service_tier"].GetStringValue(),
	})
	if err != nil || event.Model == "" {
		return usagereport.Event{}, true, errInvalidCodexUsage
	}

	// Codex does not include an upstream response ID on the completion record.
	identity := fmt.Sprintf("%q:%d:%q:%d:%d:%d:%d", fields["conversation.id"].GetStringValue(),
		at.UnixNano(), event.Model, input, output, cached, written)
	event.ID = stableEventID(codexCompletionEvent, identity)
	return event, true, nil
}
