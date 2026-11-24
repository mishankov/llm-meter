// Package telemetry accepts local OpenTelemetry exports and queues usage metadata.
package telemetry

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"

	logscollector "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricscollector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracecollector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	mediaProtobuf = "application/x-protobuf"
	mediaJSON     = "application/json"
	maxOTLPBytes  = 4 << 20
	maxUsageBatch = 2048
)

type enqueueBatch func(context.Context, [][]byte) error

// Handler receives OTLP/HTTP logs, traces, and metrics. It takes usage from one
// signal per harness and acknowledges metrics for exporters that also send them.
func Handler(enqueue func(context.Context, [][]byte) error, onRequest func()) http.Handler {
	routes := map[string]http.Handler{
		"/v1/logs":    receiveOTLP(enqueue, &logscollector.ExportLogsServiceRequest{}, logsToUsage),
		"/v1/traces":  receiveOTLP(enqueue, &tracecollector.ExportTraceServiceRequest{}, tracesToUsage),
		"/v1/metrics": receiveOTLP(enqueue, &metricscollector.ExportMetricsServiceRequest{}, discardMetrics),
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

func receiveOTLP[T proto.Message](enqueue enqueueBatch, schema T, convert func(T, bool) (usageBatch, proto.Message)) http.Handler {
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
		batch, response := convert(request, fromJSON)
		if batch.records > maxUsageBatch {
			writeStatus(w, format, http.StatusRequestEntityTooLarge, codes.ResourceExhausted, "Too many OTLP records")
			return
		}
		if len(batch.payloads) > 0 && enqueue(r.Context(), batch.payloads) != nil {
			writeStatus(w, format, http.StatusServiceUnavailable, codes.Unavailable, "Could not persist usage")
			return
		}
		writeOTLP(w, format, http.StatusOK, response)
	})
}

func logsToUsage(request *logscollector.ExportLogsServiceRequest, _ bool) (usageBatch, proto.Message) {
	var batch usageBatch
	response := &logscollector.ExportLogsServiceResponse{}
	for _, resource := range request.GetResourceLogs() {
		for _, scope := range resource.GetScopeLogs() {
			batch.records += len(scope.GetLogRecords())
			if batch.records > maxUsageBatch {
				return batch, response
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
	if batch.rejected > 0 {
		response.PartialSuccess = &logscollector.ExportLogsPartialSuccess{
			RejectedLogRecords: batch.rejected,
			ErrorMessage:       "Usage records had invalid or missing fields",
		}
	}
	return batch, response
}

func tracesToUsage(request *tracecollector.ExportTraceServiceRequest, fromJSON bool) (usageBatch, proto.Message) {
	var batch usageBatch
	response := &tracecollector.ExportTraceServiceResponse{}
	for _, resource := range request.GetResourceSpans() {
		for _, scope := range resource.GetScopeSpans() {
			batch.records += len(scope.GetSpans())
			if batch.records > maxUsageBatch {
				return batch, response
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
	if batch.rejected > 0 {
		response.PartialSuccess = &tracecollector.ExportTracePartialSuccess{
			RejectedSpans: batch.rejected,
			ErrorMessage:  "Usage spans had invalid or missing fields",
		}
	}
	return batch, response
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

func discardMetrics(_ *metricscollector.ExportMetricsServiceRequest, _ bool) (usageBatch, proto.Message) {
	return usageBatch{}, &metricscollector.ExportMetricsServiceResponse{}
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
