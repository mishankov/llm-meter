package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mishankov/llm-meter/internal/relay"
)

func TestCLIListenerOptions(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want relay.Config
	}{
		{
			name: "default proxy",
			want: relay.Config{Mode: relay.ModeRemoteProxy, Listen: relay.DefaultListen},
		},
		{
			name: "proxy and telemetry in Docker",
			args: []string{
				"--mode=usage-only", "--bind=0.0.0.0:8765",
				"--telemetry-listen=127.0.0.1:4318", "--telemetry-bind=0.0.0.0:4318",
				"--tls-cert=cert.pem", "--tls-key=key.pem",
			},
			want: relay.Config{
				Mode: relay.ModeUsageOnly, Listen: relay.DefaultListen, Bind: "0.0.0.0:8765",
				TelemetryListen: "127.0.0.1:4318", TelemetryBind: "0.0.0.0:4318",
				TLSCert: "cert.pem", TLSKey: "key.pem",
			},
		},
		{
			name: "proxy explicitly disabled",
			args: []string{
				"--proxy=false", "--mode=usage-only", "--provider=openai",
				"--upstream=https://upstream.test/v1",
				"--telemetry-listen=127.0.0.1:4318", "--telemetry-bind=0.0.0.0:4319",
			},
			want: relay.Config{Mode: relay.ModeTelemetry, Listen: "127.0.0.1:4318", Bind: "0.0.0.0:4319"},
		},
		{
			name: "legacy standalone telemetry",
			args: []string{"--mode=telemetry", "--listen=127.0.0.1:4318", "--bind=0.0.0.0:4319"},
			want: relay.Config{Mode: relay.ModeTelemetry, Listen: "127.0.0.1:4318", Bind: "0.0.0.0:4319"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parseFlags(flag.NewFlagSet(t.Name(), flag.ContinueOnError), tt.args)
			if err != nil {
				t.Fatal(err)
			}
			tt.want.DataDir = "./data/relay"
			if opts.Config != tt.want {
				t.Fatalf("config = %+v, want %+v", opts.Config, tt.want)
			}
		})
	}

	_, err := parseFlags(flag.NewFlagSet(t.Name(), flag.ContinueOnError), []string{"--proxy=false"})
	if err == nil || !strings.Contains(err.Error(), "set --telemetry-listen") {
		t.Fatalf("expected actionable error for disabled listeners, got %v", err)
	}
}

func TestCLIRunStartsAndStopsCleanly(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "proxy.key")
	if err := os.WriteFile(keyPath, []byte("llmm_cli_fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, options{
			Config:  relay.Config{Listen: "127.0.0.1:0", Mode: relay.ModeRemoteProxy, Target: "https://proxy.test"},
			keyFile: keyPath,
		})
	}()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CLI relay did not stop on context cancellation")
	}

	// Missing key file fails before listening.
	if err := run(t.Context(), options{
		Config:  relay.Config{Listen: "127.0.0.1:0", Mode: relay.ModeRemoteProxy, Target: "https://proxy.test"},
		keyFile: filepath.Join(t.TempDir(), "missing.key"),
	}); err == nil {
		t.Fatal("expected missing key file to fail")
	}
}
