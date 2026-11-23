package telemetry

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/mishankov/llm-meter/internal/proxy"
	"github.com/mishankov/llm-meter/internal/usagereport"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

const claudeRequestEvent = "claude_code.api_request"

var (
	errInvalidClaudeUsage = errors.New("invalid Claude Code usage")
	eventIDEncoding       = base32.StdEncoding.WithPadding(base32.NoPadding)
)

func claudeCodeUsage(record *logspb.LogRecord) (usagereport.Event, bool, error) {
	if record.GetEventName() != claudeRequestEvent && record.GetBody().GetStringValue() != claudeRequestEvent {
		return usagereport.Event{}, false, nil
	}

	fields := make(map[string]*commonpb.AnyValue, len(record.GetAttributes()))
	for _, attribute := range record.GetAttributes() {
		fields[attribute.GetKey()] = attribute.GetValue()
	}
	at := time.Unix(int64(record.GetTimeUnixNano()/1e9), int64(record.GetTimeUnixNano()%1e9))
	if record.GetTimeUnixNano() == 0 {
		at, _ = time.Parse(time.RFC3339Nano, fields["event.timestamp"].GetStringValue())
	}
	input, validInput := claudeTokenCount(fields["input_tokens"])
	output, validOutput := claudeTokenCount(fields["output_tokens"])
	read, validRead := claudeTokenCount(fields["cache_read_tokens"])
	write, validWrite := claudeTokenCount(fields["cache_creation_tokens"])
	if !validInput || !validOutput || (fields["cache_read_tokens"] != nil && !validRead) ||
		(fields["cache_creation_tokens"] != nil && !validWrite) {
		return usagereport.Event{}, true, errInvalidClaudeUsage
	}

	tier := ""
	if fields["speed"].GetStringValue() == "fast" {
		tier = "fast"
	}
	cost := claudeCostEstimate(fields)
	event, err := usagereport.NewEvent(proxy.Usage{
		At:       at,
		Provider: "anthropic",
		Model:    fields["model"].GetStringValue(),
		Endpoint: usagereport.ClaudeCodeEndpoint,
		Method:   http.MethodPost,
		Status:   http.StatusOK,
		HasUsage: true,
		// Claude's input count excludes cache traffic. The ledger stores total input.
		InputTokens:           input + read + write,
		CachedInputTokens:     read,
		CacheWriteTokens:      write,
		OutputTokens:          output,
		ServiceTier:           tier,
		ReportedCost:          cost,
		ReportedCostEstimated: cost != nil,
	})
	if err != nil || event.Model == "" {
		return usagereport.Event{}, true, errInvalidClaudeUsage
	}

	// This ID covers retry at both the exporter and usage upload hops. Correlation
	// attributes are used only as hash input and never leave the relay.
	requestID := fields["request_id"].GetStringValue()
	identity := "request:" + requestID
	if requestID == "" {
		sequence, _ := claudeInteger(fields["event.sequence"])
		identity = fmt.Sprintf("event:%q:%d:%d:%q",
			fields["session.id"].GetStringValue(), at.UnixNano(), sequence, event.Model)
	}
	digest := sha256.Sum256([]byte(claudeRequestEvent + "\x00" + identity))
	event.ID = eventIDEncoding.EncodeToString(digest[:16])
	return event, true, nil
}

func claudeTokenCount(value *commonpb.AnyValue) (int64, bool) {
	count, ok := claudeInteger(value)
	if !ok || count < 0 || count > 1<<40 {
		return 0, false
	}
	return count, true
}

func claudeInteger(value *commonpb.AnyValue) (int64, bool) {
	switch number := value.GetValue().(type) {
	case *commonpb.AnyValue_IntValue:
		return number.IntValue, true
	case *commonpb.AnyValue_StringValue:
		// Older Claude Code releases exported numeric attributes as strings.
		parsed, err := strconv.ParseInt(number.StringValue, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func claudeCostEstimate(fields map[string]*commonpb.AnyValue) *float64 {
	var cost float64
	switch amount := fields["cost_usd"].GetValue().(type) {
	case *commonpb.AnyValue_DoubleValue:
		cost = amount.DoubleValue
	case *commonpb.AnyValue_IntValue:
		cost = float64(amount.IntValue)
	case *commonpb.AnyValue_StringValue:
		var err error
		cost, err = strconv.ParseFloat(amount.StringValue, 64)
		if err != nil {
			return nil
		}
	default:
		micros, ok := claudeInteger(fields["cost_usd_micros"])
		if !ok {
			return nil
		}
		cost = float64(micros) / 1e6
	}
	if math.IsNaN(cost) || cost < 0 || cost > 1e12 {
		return nil
	}
	return &cost
}
