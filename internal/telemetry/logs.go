// Package telemetry accepts OpenTelemetry exports and extracts usage metadata.
package telemetry

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/mishankov/llm-meter/internal/usagereport"
	logscollector "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricscollector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracecollector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Prefix is the shared URL path prefix for OTLP/HTTP endpoints on both the
// LLM Meter server and the local relay.
const Prefix = "/otlp"

const (
	mediaProtobuf = "application/x-protobuf"
	mediaJSON     = "application/json"
	maxOTLPBytes  = 4 << 20
	maxUsageBatch = 2048
)

type deliverBatch func(context.Context, *usageBatch) error

// Handler receives OTLP/HTTP logs, traces, and metrics under Prefix. It takes
// usage from one signal per harness and acknowledges metrics for exporters
// that also send them.
func Handler(enqueue func(context.Context, [][]byte) error, onRequest func()) http.Handler {
	return newHandler(Prefix, func(ctx context.Context, batch *usageBatch) error {
		payloads := make([][]byte, 0, len(batch.events))
		for _, event := range batch.events {
			payload, err := json.Marshal(event)
			if err != nil || len(payload) > usagereport.MaxBodyBytes {
				batch.rejected++
				continue
			}
			payloads = append(payloads, payload)
		}
		if len(payloads) == 0 {
			return nil
		}
		return enqueue(ctx, payloads)
	}, onRequest)
}

// EventHandler receives OTLP/HTTP logs, traces, and metrics under prefix
// (for example "/otlp") and delivers extracted usage events directly without
// JSON round-tripping.
func EventHandler(prefix string, record func(context.Context, []usagereport.Event) error) http.Handler {
	return newHandler(prefix, func(ctx context.Context, batch *usageBatch) error {
		if len(batch.events) == 0 {
			return nil
		}
		return record(ctx, batch.events)
	}, nil)
}

func newHandler(prefix string, deliver deliverBatch, onRequest func()) http.Handler {
	prefix = strings.TrimSuffix(prefix, "/")
	routes := map[string]http.Handler{
		prefix + "/v1/logs":    receiveOTLP(deliver, &logscollector.ExportLogsServiceRequest{}, logsToUsage, logsResponse),
		prefix + "/v1/traces":  receiveOTLP(deliver, &tracecollector.ExportTraceServiceRequest{}, tracesToUsage, tracesResponse),
		prefix + "/v1/metrics": receiveOTLP(deliver, &metricscollector.ExportMetricsServiceRequest{}, discardMetrics, metricsResponse),
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler, ok := routes[r.URL.EscapedPath()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if onRequest != nil {
			onRequest()
		}
		handler.ServeHTTP(w, r)
	})
}

func receiveOTLP[T proto.Message](
	deliver deliverBatch,
	schema T,
	convert func(T, bool) usageBatch,
	buildResponse func(rejected int64) proto.Message,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		format, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || (format != mediaJSON && format != mediaProtobuf) {
			http.Error(w, "Expected OTLP JSON or protobuf", http.StatusUnsupportedMediaType)
			return
		}
		body := http.MaxBytesReader(w, r.Body, maxOTLPBytes)
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
		data, err := io.ReadAll(io.LimitReader(reader, maxOTLPBytes+1))
		if err != nil {
			writeReadError(w, format, err)
			return
		}
		if len(data) > maxOTLPBytes {
			writeStatus(w, format, http.StatusRequestEntityTooLarge, codes.ResourceExhausted, "OTLP request is too large")
			return
		}
		request := schema.ProtoReflect().New().Interface().(T)
		fromJSON := format == mediaJSON
		if fromJSON {
			err = (protojson.UnmarshalOptions{DiscardUnknown: true, RecursionLimit: 64}).Unmarshal(data, request)
		} else {
			err = (proto.UnmarshalOptions{DiscardUnknown: true, RecursionLimit: 64}).Unmarshal(data, request)
		}
		if err != nil {
			writeStatus(w, format, http.StatusBadRequest, codes.InvalidArgument, "Invalid OTLP request")
			return
		}
		batch := convert(request, fromJSON)
		if batch.records > maxUsageBatch {
			writeStatus(w, format, http.StatusRequestEntityTooLarge, codes.ResourceExhausted, "Too many OTLP records")
			return
		}
		if err := deliver(r.Context(), &batch); err != nil {
			writeStatus(w, format, http.StatusServiceUnavailable, codes.Unavailable, "Could not persist usage")
			return
		}
		writeOTLP(w, format, http.StatusOK, buildResponse(batch.rejected))
	})
}

func logsToUsage(request *logscollector.ExportLogsServiceRequest, _ bool) usageBatch {
	var batch usageBatch
	for _, resource := range request.GetResourceLogs() {
		for _, scope := range resource.GetScopeLogs() {
			batch.records += len(scope.GetLogRecords())
			if batch.records > maxUsageBatch {
				return batch
			}
			for _, record := range scope.GetLogRecords() {
				event, matched, err := claudeCodeUsage(record)
				if !matched {
					event, matched, err = codexUsage(resource.GetResource(), record)
				}
				if !matched {
					event, matched, err = geminiUsage(resource.GetResource(), record)
				}
				batch.add(event, matched, err)
			}
		}
	}
	return batch
}

func logsResponse(rejected int64) proto.Message {
	response := &logscollector.ExportLogsServiceResponse{}
	if rejected > 0 {
		response.PartialSuccess = &logscollector.ExportLogsPartialSuccess{
			RejectedLogRecords: rejected,
			ErrorMessage:       "Usage records had invalid or missing fields",
		}
	}
	return response
}

func tracesToUsage(request *tracecollector.ExportTraceServiceRequest, fromJSON bool) usageBatch {
	var batch usageBatch
	for _, resource := range request.GetResourceSpans() {
		for _, scope := range resource.GetScopeSpans() {
			batch.records += len(scope.GetSpans())
			if batch.records > maxUsageBatch {
				return batch
			}
			for _, span := range scope.GetSpans() {
				if span == nil {
					continue
				}
				if fromJSON {
					span.TraceId = otlpJSONID(span.GetTraceId(), 16)
					span.SpanId = otlpJSONID(span.GetSpanId(), 8)
				}
				event, matched, err := copilotUsage(resource.GetResource(), scope.GetScope(), span)
				if !matched {
					event, matched, err = openCodeUsage(resource.GetResource(), span)
				}
				batch.add(event, matched, err)
			}
		}
	}
	return batch
}

func tracesResponse(rejected int64) proto.Message {
	response := &tracecollector.ExportTraceServiceResponse{}
	if rejected > 0 {
		response.PartialSuccess = &tracecollector.ExportTracePartialSuccess{
			RejectedSpans: rejected,
			ErrorMessage:  "Usage spans had invalid or missing fields",
		}
	}
	return response
}

// Protobuf JSON decodes bytes as base64, while OTLP JSON encodes trace and span
// IDs as hex. Recover the original text, then decode its hex representation.
func otlpJSONID(id []byte, size int) []byte {
	decoded, err := hex.DecodeString(base64.RawStdEncoding.EncodeToString(id))
	if err != nil || len(decoded) != size {
		return nil
	}
	return decoded
}

func discardMetrics(_ *metricscollector.ExportMetricsServiceRequest, _ bool) usageBatch {
	return usageBatch{}
}

func metricsResponse(_ int64) proto.Message {
	return &metricscollector.ExportMetricsServiceResponse{}
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
