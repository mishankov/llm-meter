package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishankov/llm-meter/internal/relay"
)

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
