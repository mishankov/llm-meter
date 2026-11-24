package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/mishankov/llm-meter/internal/ledger"
	"github.com/mishankov/llm-meter/internal/pricing"
	"github.com/mishankov/llm-meter/internal/relay"
	"github.com/mishankov/llm-meter/internal/usagereport"
	collector "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestClaudeCodeTelemetryReachesLedger(t *testing.T) {
	at := time.Now().UTC()
	store, err := ledger.Open(filepath.Join(t.TempDir(), "ledger.db"), pricing.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client, key, err := store.CreateClient(t.Context(), "Claude Code test", at)
	if err != nil {
		t.Fatal(err)
	}
	handler := routes(store, fstest.MapFS{}, nil, nil, "fixture-dashboard-token-5e9ebc83", false, slog.New(slog.DiscardHandler))
	uploaded := make(chan int, 2)
	meter := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		for _, secret := range []string{"prompt-fixture", "request-fixture", "session-fixture", "account-fixture"} {
			if bytes.Contains(payload, []byte(secret)) {
				t.Errorf("upload contained %q", secret)
			}
		}
		var event usagereport.Event
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Error(err)
		} else if event.ResponseIDHash != "" || !event.ReportedCostEstimated {
			t.Error("upload has unexpected metering metadata")
		}
		r.Body = io.NopCloser(bytes.NewReader(payload))
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, r)
		w.WriteHeader(result.Code)
		select {
		case uploaded <- result.Code:
		case <-t.Context().Done():
		}
	}))
	defer meter.Close()
	oldTransport := http.DefaultTransport
	http.DefaultTransport = meter.Client().Transport
	defer func() { http.DefaultTransport = oldTransport }()

	inst, err := relay.New(relay.Config{
		Mode: relay.ModeTelemetry, Listen: "127.0.0.1:0", Target: meter.URL, Key: key.Token, DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := inst.Stop(ctx); err != nil {
			t.Error(err)
		}
	}()

	fixture := []byte(fmt.Sprintf(`{"resourceLogs":[{"resource":{"attributes":[{"key":"user.account_id","value":{"stringValue":"account-fixture"}}]},
		"scopeLogs":[{"logRecords":[
			{"body":{"stringValue":"claude_code.user_prompt"},"attributes":[{"key":"prompt","value":{"stringValue":"prompt-fixture"}}]},
			{"eventName":"claude_code.api_request","traceId":"0123456789abcdef0123456789abcdef","timeUnixNano":"%d","attributes":[
				{"key":"model","value":{"stringValue":"claude-sonnet-fixture"}},
				{"key":"request_id","value":{"stringValue":"request-fixture"}},
				{"key":"session.id","value":{"stringValue":"session-fixture"}},
				{"key":"input_tokens","value":{"intValue":"10"}},
				{"key":"output_tokens","value":{"intValue":"20"}},
				{"key":"cache_read_tokens","value":{"intValue":"30"}},
				{"key":"cache_creation_tokens","value":{"intValue":"40"}},
				{"key":"cost_usd","value":{"doubleValue":0.001}}
			]},
			{"body":{"stringValue":"claude_code.api_request"},"attributes":[{"key":"input_tokens","value":{"intValue":"-5"}}]}
		]}]}]}`, at.UnixNano()))
	var batch collector.ExportLogsServiceRequest
	if err := protojson.Unmarshal(fixture, &batch); err != nil {
		t.Fatal(err)
	}
	protobuf, err := proto.Marshal(&batch)
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write(protobuf); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	legacy := strings.NewReplacer(
		`"eventName":"claude_code.api_request"`, `"body":{"stringValue":"claude_code.api_request"}`,
		`"request_id"`, `"client_request_id"`,
		`"intValue"`, `"stringValue"`,
		`"doubleValue":0.001`, `"stringValue":"0.001"`,
	).Replace(string(fixture))
	for _, export := range []struct {
		format, encoding string
		payload          []byte
	}{
		{"application/json", "", fixture},
		{"application/x-protobuf", "gzip", compressed.Bytes()},
		{"application/json", "", []byte(legacy)},
	} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+inst.Status().ListenAddr+"/v1/logs", bytes.NewReader(export.payload))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", export.format)
		if export.encoding != "" {
			request.Header.Set("Content-Encoding", export.encoding)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("OTLP response status %d: %s (%v)", response.StatusCode, body, readErr)
		}
		var result collector.ExportLogsServiceResponse
		if export.format == "application/json" {
			err = protojson.Unmarshal(body, &result)
		} else {
			err = proto.Unmarshal(body, &result)
		}
		if err != nil || result.GetPartialSuccess().GetRejectedLogRecords() != 1 {
			t.Fatalf("OTLP response did not reject the malformed usage event: %v, %v", &result, err)
		}
		select {
		case code := <-uploaded:
			if code != http.StatusNoContent {
				t.Fatalf("usage upload status %d", code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("relay did not upload the usage event")
		}
	}

	view, err := store.Dashboard(t.Context(), ledger.Filter{
		Start: at.Add(-time.Minute), End: at.Add(time.Minute), Interval: time.Minute, Clients: []string{client.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Requests) != 2 || view.Summary.RequestCount != 2 {
		t.Fatalf("recorded %d requests after replay and legacy export, want 2", view.Summary.RequestCount)
	}
	for _, row := range view.Requests {
		if row.Provider != "anthropic" || row.Endpoint != usagereport.ClaudeCodeEndpoint || row.MeasurementSource != "relay" ||
			row.Model != "claude-sonnet-fixture" || row.InputTokens != 80 || row.OutputTokens != 20 ||
			row.CachedInputTokens != 30 || row.CacheWriteTokens != 40 || row.Cost == nil || *row.Cost != 0.001 || row.PriceSource != "provider-estimate" {
			t.Fatalf("unexpected metering row: %+v", row)
		}
	}
}
