package telemetry

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"strconv"

	"github.com/mishankov/llm-meter/internal/usagereport"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

var eventIDEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func stableEventID(namespace, identity string) string {
	digest := sha256.Sum256([]byte(namespace + "\x00" + identity))
	return eventIDEncoding.EncodeToString(digest[:16])
}

func attribute(fields []*commonpb.KeyValue, key string) *commonpb.AnyValue {
	for _, field := range fields {
		if field.GetKey() == key {
			return field.GetValue()
		}
	}
	return nil
}

func tokenCount(value *commonpb.AnyValue) (int64, bool) {
	count, ok := integerAttribute(value)
	return count, ok && count >= 0 && count <= 1<<40
}

func integerAttribute(value *commonpb.AnyValue) (int64, bool) {
	switch number := value.GetValue().(type) {
	case *commonpb.AnyValue_IntValue:
		return number.IntValue, true
	case *commonpb.AnyValue_StringValue:
		// Some exporters encode numeric attributes as strings.
		parsed, err := strconv.ParseInt(number.StringValue, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func optionalTokenCount(value *commonpb.AnyValue) (int64, bool) {
	if value == nil {
		return 0, true
	}
	return tokenCount(value)
}

type usageBatch struct {
	payloads [][]byte
	rejected int64
	records  int
}

func (b *usageBatch) add(event usagereport.Event, matched bool, err error) {
	if !matched {
		return
	}
	if err != nil {
		b.rejected++
		return
	}
	payload, err := json.Marshal(event)
	if err != nil || len(payload) > usagereport.MaxBodyBytes {
		b.rejected++
		return
	}
	b.payloads = append(b.payloads, payload)
}
