package proxy

import (
	"encoding/json"
	"sync"
)

const metadataValueLimit = 64 << 10

// metadataCapture scans JSON without copying payload fields such as output, data,
// or messages. Only bounded values that the ledger uses are retained.
type metadataCapture struct {
	mu       sync.Mutex
	selector objectSelector
}

func (c *metadataCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, b := range p {
		c.selector.feed(b)
	}
	return len(p), nil
}

func (c *metadataCapture) message() wireMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.selector.phase != objectDone {
		return wireMessage{}
	}
	encoded, err := json.Marshal(c.selector.fields)
	if err != nil {
		return wireMessage{}
	}
	return decodeMessage(encoded)
}

type objectPhase uint8

const (
	objectStart objectPhase = iota
	objectKey
	objectReadingKey
	objectColon
	objectValue
	objectReadingValue
	objectDelimiter
	objectDone
	objectInvalid
)

type objectSelector struct {
	phase     objectPhase
	fields    map[string]json.RawMessage
	key       []byte
	name      string
	escapeKey bool
	depth     int
	value     selectedValue
}

func (s *objectSelector) feed(b byte) {
	switch s.phase {
	case objectStart:
		if jsonSpace(b) {
			return
		}
		if b != '{' {
			s.phase = objectInvalid
			return
		}
		s.phase = objectKey
	case objectKey:
		if jsonSpace(b) {
			return
		}
		switch b {
		case '"':
			s.phase, s.key = objectReadingKey, append(s.key[:0], b)
		case '}':
			s.phase = objectDone
		default:
			s.phase = objectInvalid
		}
	case objectReadingKey:
		if len(s.key) <= 128 {
			s.key = append(s.key, b)
		}
		if s.escapeKey {
			s.escapeKey = false
			return
		}
		if b == '\\' {
			s.escapeKey = true
			return
		}
		if b == '"' {
			s.name = ""
			if len(s.key) <= 128 {
				_ = json.Unmarshal(s.key, &s.name)
			}
			s.phase = objectColon
		}
	case objectColon:
		if jsonSpace(b) {
			return
		}
		if b != ':' {
			s.phase = objectInvalid
			return
		}
		s.phase = objectValue
	case objectValue:
		if jsonSpace(b) {
			return
		}
		s.value = selectedValue{}
		switch s.name {
		case "type", "id", "model", "service_tier", "prompt_cache_retention", "background", "background_options", "usage", "rate_limits",
			"metered_limit_name", "limit_name", "stream_id", "status", "usageMetadata", "modelVersion", "responseId",
			"reasoning", "reasoning_effort", "output_config", "thinking", "thinkingConfig":
			s.value.keep = true
		case "response", "session", "message", "request":
			if s.depth == 0 && b == '{' {
				s.value.nested = &objectSelector{depth: s.depth + 1}
			}
		case "generationConfig":
			if s.depth <= 1 && b == '{' {
				s.value.nested = &objectSelector{depth: s.depth + 1}
			}
		}
		s.value.start(b)
		s.phase = objectReadingValue
	case objectReadingValue:
		done, delimiter := s.value.feed(b)
		if done {
			s.keepValue()
			if delimiter {
				s.feed(b)
			}
		}
	case objectDelimiter:
		if jsonSpace(b) {
			return
		}
		switch b {
		case ',':
			s.phase = objectKey
		case '}':
			s.phase = objectDone
		default:
			s.phase = objectInvalid
		}
	}
}

func (s *objectSelector) keepValue() {
	value := s.value.raw
	if nested := s.value.nested; nested != nil && nested.phase == objectDone {
		value, _ = json.Marshal(nested.fields)
	}
	if len(value) != 0 {
		if s.fields == nil {
			s.fields = make(map[string]json.RawMessage)
		}
		s.fields[s.name] = value
	}
	s.phase = objectDelimiter
}

type selectedValue struct {
	raw                              []byte
	nested                           *objectSelector
	depth                            int
	keep, quoted, escaped, primitive bool
}

func (v *selectedValue) start(b byte) {
	switch b {
	case '{', '[':
		v.depth = 1
	case '"':
		v.quoted = true
	default:
		v.primitive = true
	}
	v.capture(b)
}

func (v *selectedValue) feed(b byte) (done, delimiter bool) {
	if v.primitive && (jsonSpace(b) || b == ',' || b == '}') {
		return true, true
	}
	v.capture(b)
	if v.quoted {
		switch {
		case v.escaped:
			v.escaped = false
		case b == '\\':
			v.escaped = true
		case b == '"':
			v.quoted = false
			return v.depth == 0, false
		}
		return false, false
	}
	switch b {
	case '"':
		v.quoted = true
	case '{', '[':
		v.depth++
	case '}', ']':
		v.depth--
		return v.depth == 0, false
	}
	return false, false
}

func (v *selectedValue) capture(b byte) {
	if v.nested != nil {
		v.nested.feed(b)
	}
	if !v.keep {
		return
	}
	if len(v.raw) == metadataValueLimit {
		v.raw, v.keep = nil, false
		return
	}
	v.raw = append(v.raw, b)
}

func jsonSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' }
