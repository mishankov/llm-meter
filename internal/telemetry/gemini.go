package telemetry

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/mishankov/llm-meter/internal/proxy"
	"github.com/mishankov/llm-meter/internal/usagereport"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

const geminiResponseEvent = "gemini_cli.api_response"

var errInvalidGeminiUsage = errors.New("invalid Gemini CLI usage")

func geminiUsage(resource *resourcepb.Resource, record *logspb.LogRecord) (usagereport.Event, bool, error) {
	// Gemini CLI also emits a GenAI semantic log for each response. Only its
	// API response log carries the cache, tool prompt, and thinking breakdown.
	if record.GetEventName() != geminiResponseEvent && attribute(record.GetAttributes(), "event.name").GetStringValue() != geminiResponseEvent {
		return usagereport.Event{}, false, nil
	}
	fields := make(map[string]*commonpb.AnyValue, len(record.GetAttributes()))
	for _, attribute := range record.GetAttributes() {
		fields[attribute.GetKey()] = attribute.GetValue()
	}
	status, validStatus := integerAttribute(fields["status_code"])
	if !validStatus || status < 100 || status > 599 {
		return usagereport.Event{}, true, errInvalidGeminiUsage
	}
	if status < 200 || status >= 300 {
		return usagereport.Event{}, false, nil
	}

	at := geminiRecordTime(record, fields["event.timestamp"].GetStringValue())
	prompt, validPrompt := tokenCount(fields["input_token_count"])
	output, validOutput := tokenCount(fields["output_token_count"])
	cached, validCached := optionalTokenCount(fields["cached_content_token_count"])
	tools, validTools := optionalTokenCount(fields["tool_token_count"])
	thoughts, validThoughts := optionalTokenCount(fields["thoughts_token_count"])
	if !validPrompt || !validOutput || !validCached || !validTools || !validThoughts || cached > prompt {
		return usagereport.Event{}, true, errInvalidGeminiUsage
	}

	event, err := usagereport.NewEvent(proxy.Usage{
		At:                at,
		Provider:          "google",
		Model:             fields["model"].GetStringValue(),
		Endpoint:          usagereport.GeminiTelemetryEndpoint,
		Method:            http.MethodPost,
		Status:            int(status),
		HasUsage:          prompt+tools > 0 || output+thoughts > 0,
		InputTokens:       prompt + tools,
		CachedInputTokens: cached,
		OutputTokens:      output + thoughts,
	})
	if err != nil || event.Model == "" {
		return usagereport.Event{}, true, errInvalidGeminiUsage
	}

	// The API response log can repeat a prompt ID across tool round trips, and
	// the provider response ID is only available on the separate semantic log.
	// Include request-local fields to distinguish responses within one prompt.
	sessionID := fields["session.id"].GetStringValue()
	if sessionID == "" {
		sessionID = attribute(resource.GetAttributes(), "session.id").GetStringValue()
	}
	duration, _ := integerAttribute(fields["duration_ms"])
	identity := fmt.Sprintf("%q:%q:%s:%q:%d:%d:%d:%d:%d:%d",
		sessionID, fields["prompt_id"].GetStringValue(), at.UTC().Format(time.RFC3339Nano),
		event.Model, duration, prompt, output, cached, tools, thoughts)
	event.ID = stableEventID(geminiResponseEvent, identity)
	return event, true, nil
}

func geminiRecordTime(record *logspb.LogRecord, timestamp string) time.Time {
	if stamp := record.GetTimeUnixNano(); stamp != 0 {
		return time.Unix(int64(stamp/1e9), int64(stamp%1e9))
	}
	if at, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
		return at
	}
	if stamp := record.GetObservedTimeUnixNano(); stamp != 0 {
		return time.Unix(int64(stamp/1e9), int64(stamp%1e9))
	}
	return time.Time{}
}
