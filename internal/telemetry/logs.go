// Package telemetry accepts local OpenTelemetry exports and queues usage metadata.
package telemetry

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/mishankov/llm-meter/internal/usagereport"
	logscollector "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	mediaProtobuf = "application/x-protobuf"
	mediaJSON     = "application/json"
	maxLogBytes   = 4 << 20
	maxLogRecords = 2048
)

// LogsHandler acknowledges an OTLP/HTTP batch after its usage events are persisted.
func LogsHandler(enqueue func(context.Context, [][]byte) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		format, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || (format != mediaJSON && format != mediaProtobuf) {
			http.Error(w, "Expected OTLP JSON or protobuf", http.StatusUnsupportedMediaType)
			return
		}
		body := http.MaxBytesReader(w, r.Body, maxLogBytes)
		var reader io.Reader = body
		switch r.Header.Get("Content-Encoding") {
		case "", "identity":
		case "gzip":
			gz, err := gzip.NewReader(body)
			if err != nil {
				writeReadError(w, format, err)
				return
			}
			defer gz.Close()
			reader = gz
		default:
			writeStatus(w, format, http.StatusUnsupportedMediaType, codes.InvalidArgument, "Unsupported OTLP content encoding")
			return
		}
		data, err := io.ReadAll(io.LimitReader(reader, maxLogBytes+1))
		if err != nil {
			writeReadError(w, format, err)
			return
		}
		if len(data) > maxLogBytes {
			writeStatus(w, format, http.StatusRequestEntityTooLarge, codes.ResourceExhausted, "OTLP request is too large")
			return
		}
		var batch logscollector.ExportLogsServiceRequest
		if format == mediaJSON {
			// Trace and span IDs are never used. Their fixed-length hexadecimal
			// encodings also decode as protobuf JSON bytes.
			err = (protojson.UnmarshalOptions{DiscardUnknown: true, RecursionLimit: 64}).Unmarshal(data, &batch)
		} else {
			err = (proto.UnmarshalOptions{DiscardUnknown: true, RecursionLimit: 64}).Unmarshal(data, &batch)
		}
		if err != nil {
			writeStatus(w, format, http.StatusBadRequest, codes.InvalidArgument, "Invalid OTLP log request")
			return
		}
		payloads, rejected, tooMany := usagePayloads(&batch)
		if tooMany {
			writeStatus(w, format, http.StatusRequestEntityTooLarge, codes.ResourceExhausted, "Too many OTLP log records")
			return
		}
		if len(payloads) > 0 && enqueue(r.Context(), payloads) != nil {
			writeStatus(w, format, http.StatusServiceUnavailable, codes.Unavailable, "Could not persist usage")
			return
		}
		response := &logscollector.ExportLogsServiceResponse{}
		if rejected > 0 {
			response.PartialSuccess = &logscollector.ExportLogsPartialSuccess{
				RejectedLogRecords: rejected,
				ErrorMessage:       "Claude Code usage records had invalid or missing fields",
			}
		}
		writeOTLP(w, format, http.StatusOK, response)
	})
}

func usagePayloads(batch *logscollector.ExportLogsServiceRequest) (payloads [][]byte, rejected int64, tooMany bool) {
	count := 0
	for _, resource := range batch.GetResourceLogs() {
		for _, scope := range resource.GetScopeLogs() {
			count += len(scope.GetLogRecords())
			if count > maxLogRecords {
				return nil, 0, true
			}
			for _, record := range scope.GetLogRecords() {
				event, matched, err := claudeCodeUsage(record)
				if !matched {
					continue
				}
				if err != nil {
					rejected++
					continue
				}
				payload, err := json.Marshal(event)
				if err != nil || len(payload) > usagereport.MaxBodyBytes {
					rejected++
					continue
				}
				payloads = append(payloads, payload)
			}
		}
	}
	return payloads, rejected, false
}

func writeReadError(w http.ResponseWriter, format string, err error) {
	var sizeError *http.MaxBytesError
	if errors.As(err, &sizeError) {
		writeStatus(w, format, http.StatusRequestEntityTooLarge, codes.ResourceExhausted, "OTLP request is too large")
		return
	}
	writeStatus(w, format, http.StatusBadRequest, codes.InvalidArgument, "Could not read OTLP request")
}

func writeStatus(w http.ResponseWriter, format string, httpCode int, code codes.Code, message string) {
	writeOTLP(w, format, httpCode, &status.Status{Code: int32(code), Message: message})
}

func writeOTLP(w http.ResponseWriter, format string, code int, message proto.Message) {
	var payload []byte
	var err error
	if format == mediaJSON {
		payload, err = protojson.Marshal(message)
	} else {
		payload, err = proto.Marshal(message)
	}
	if err != nil {
		http.Error(w, "Could not encode OTLP response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", format)
	w.WriteHeader(code)
	_, _ = w.Write(payload)
}
