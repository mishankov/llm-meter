package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishankov/llm-meter/internal/relay"
	"github.com/mishankov/llm-meter/internal/relayoutbox"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type testKeys map[string]string

func (keys testKeys) add(id, key string) error {
	keys[id] = key
	return nil
}

func (keys testKeys) get(id string) (string, error) { return keys[id], nil }

func (keys testKeys) remove(id string) error { delete(keys, id); return nil }

func testStore(t *testing.T) *ConfigStore {
	return &ConfigStore{dir: t.TempDir(), keys: testKeys{}}
}

func TestConfigStoreRotationIsAtomic(t *testing.T) {
	store := testStore(t)
	const secretKey = "llmm_1234567890abcdef_secret"
	initial := storedConfig{AppConfig: defaultAppConfig()}
	saved, err := store.SaveConfig(initial, secretKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.dir, configFile)
	raw, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), secretKey) {
		t.Fatal("expected secret-free settings on disk", err)
	}
	if key, err := store.LoadKey(saved.KeyID); err != nil || key != secretKey {
		t.Fatal("saved settings must reference the matching credential", err)
	}
	// Simulate failure to publish the next config after its new key was written.
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveConfig(saved, "replacement"); err == nil {
		t.Fatal("expected rotation failure")
	}
	if keys := store.keys.(testKeys); len(keys) != 1 || keys[saved.KeyID] != secretKey {
		t.Fatal("failed rotation must preserve the old credential and remove the staged one")
	}
}

func TestRelayManagerSaveStartAndStop(t *testing.T) {
	store := testStore(t)
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	})
	mgr, err := NewRelayManager(store, transport)
	if err != nil {
		t.Fatal(err)
	}
	if snap := mgr.Snapshot(); snap.Configured || snap.KeyConfigured {
		t.Fatalf("fresh manager should be unconfigured: %+v", snap)
	}

	err = mgr.SaveConfig(t.Context(), SaveConfigRequest{
		Config: AppConfig{
			Target:    "https://proxy.test",
			Mode:      relay.ModeRemoteProxy,
			Listen:    "127.0.0.1:0",
			AutoStart: true,
		},
		NewKey:         "llmm_desktop_fixture_key",
		StartAfterSave: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	serialized, err := json.Marshal(mgr.Snapshot())
	if err != nil || strings.Contains(string(serialized), "llmm_desktop_fixture_key") {
		t.Fatal("the snapshot must never include the proxy key", err)
	}
	snap := mgr.Snapshot()
	if !snap.Configured || !snap.KeyConfigured || snap.Status.State != relay.StateRunning {
		t.Fatalf("unexpected running snapshot: %+v", snap)
	}
	if strings.Contains(snap.KeyPrefix, "fixture_key") {
		t.Fatalf("KeyPrefix leaked full key: %q", snap.KeyPrefix)
	}

	// Send a proxied request and confirm counter increments.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+snap.Status.ListenAddr+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer provider-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := mgr.Snapshot().Status.RequestsTotal; got != 1 {
		t.Fatalf("RequestsTotal = %d, want 1", got)
	}

	stopCtx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := mgr.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if got := mgr.Snapshot().Status.State; got != relay.StateStopped {
		t.Fatalf("State = %q, want stopped", got)
	}
}

func TestTimedOutStopKeepsOwnershipUntilRequestCompletes(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-release
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	mgr, err := NewRelayManager(testStore(t), transport)
	if err != nil {
		t.Fatal(err)
	}
	cfg := AppConfig{Target: "https://meter.test", Listen: "127.0.0.1:0", Mode: relay.ModeRemoteProxy}
	if err := mgr.SaveConfig(t.Context(), SaveConfigRequest{Config: cfg, NewKey: "test-key", StartAfterSave: true}); err != nil {
		t.Fatal(err)
	}
	address := mgr.Snapshot().Status.ListenAddr
	go func() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+address+"/v1/models", nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer fixture")
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach the upstream")
	}
	done := mgr.inst.Done()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	err = mgr.Stop(cancelled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop = %v, want cancellation", err)
	}
	err = mgr.SaveConfig(t.Context(), SaveConfigRequest{Config: cfg, NewKey: "replacement"})
	if err == nil {
		t.Fatal("settings changed before old listener closed")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("relay did not shut down")
	}
	if err := mgr.SaveConfig(t.Context(), SaveConfigRequest{Config: cfg, NewKey: "replacement"}); err != nil {
		t.Fatal(err)
	}
}

func enqueueOfflineReport(t *testing.T, store *ConfigStore, target, key string) string {
	t.Helper()
	dir, err := store.QueueDir(target, key)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := url.Parse(target + "/api/usage/events")
	if err != nil {
		t.Fatal(err)
	}
	queue, err := relayoutbox.Open(dir, endpoint, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(t.Context(), []byte(`{"id":"fixture"}`)); err != nil {
		t.Fatal(err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func trustTestMeter(t *testing.T, meter *httptest.Server) {
	t.Helper()
	previousTransport := http.DefaultTransport
	http.DefaultTransport = meter.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
}

type lockedKeys struct{ keyStore }

func (lockedKeys) get(string) (string, error) { return "", errors.New("keychain locked") }

func TestRotateAfterKeychainUnlock(t *testing.T) {
	store := testStore(t)
	const original = "llmm_original"
	cfg := AppConfig{Target: "https://meter.invalid", Mode: relay.ModeUsageOnly, Listen: relay.DefaultListen}
	saved, err := store.SaveConfig(storedConfig{AppConfig: cfg}, original)
	if err != nil {
		t.Fatal(err)
	}
	enqueueOfflineReport(t, store, cfg.Target, original)
	keys := store.keys
	store.keys = lockedKeys{keys}
	mgr, err := NewRelayManager(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	rotate := SaveConfigRequest{Config: cfg, NewKey: "llmm_new"}
	if err := mgr.SaveConfig(t.Context(), rotate); err == nil {
		t.Fatal("rotation succeeded while the original credential was inaccessible")
	}
	unchanged, err := store.LoadConfig()
	if err != nil || unchanged.KeyID != saved.KeyID {
		t.Fatal("failed rotation changed the stored credential reference", err)
	}
	store.keys = keys
	if err := mgr.SaveConfig(t.Context(), rotate); err != nil {
		t.Fatal(err)
	}
	stored, err := store.LoadConfig()
	if err != nil || len(stored.Kept) != 1 || stored.Kept[0].KeyID != saved.KeyID {
		t.Fatal("rotation after unlock lost the original queue reference", err)
	}
	if key, err := store.LoadKey(saved.KeyID); err != nil || key != original {
		t.Fatal("rotation removed the retained credential", err)
	}
}

func TestStoppedQueueRotation(t *testing.T) {
	for _, kind := range []string{"drain", "recover", "reactivate"} {
		t.Run(kind, func(t *testing.T) {
			const oldKey = "llmm_original"
			var uploads atomic.Int32
			var unavailable atomic.Bool
			meter := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-LLM-Meter-Key") != oldKey {
					t.Error("kept report sent with wrong key")
				}
				if unavailable.Load() {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				uploads.Add(1)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer meter.Close()
			trustTestMeter(t, meter)
			store := testStore(t)
			mgr, err := NewRelayManager(store, nil)
			if err != nil {
				t.Fatal(err)
			}
			cfg := AppConfig{Target: meter.URL, Listen: "127.0.0.1:0", Mode: relay.ModeUsageOnly}
			if err := mgr.SaveConfig(t.Context(), SaveConfigRequest{Config: cfg, NewKey: oldKey}); err != nil {
				t.Fatal(err)
			}
			oldID := mgr.cfg.KeyID
			oldDir := enqueueOfflineReport(t, store, cfg.Target, oldKey)
			if mgr.Snapshot().PendingOnDisk != 1 {
				t.Fatal("stopped relay should report persisted events")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := mgr.SaveConfig(ctx, SaveConfigRequest{Config: cfg, NewKey: "llmm_replacement", DrainBeforeSwitch: kind == "drain"}); err != nil {
				t.Fatal(err)
			}
			if mgr.Snapshot().PendingOnDisk != 0 {
				t.Fatal("new credential unexpectedly inherited old reports")
			}
			if kind == "drain" {
				count, err := relayoutbox.PendingCount(oldDir)
				if err != nil || count != 0 || uploads.Load() != 1 || mgr.Snapshot().KeptQueues != 0 {
					t.Fatal("switch failed to drain original queue", err)
				}
			} else {
				// A fresh app instance must recover the old destination and
				// credential from disk, even with the new settings already saved.
				mgr, err = NewRelayManager(store, nil)
				if err != nil || mgr.Snapshot().KeptQueues != 1 || uploads.Load() != 0 {
					t.Fatal("expected one recoverable queue after relaunch", err)
				}
				if kind == "reactivate" {
					if err := mgr.SaveConfig(ctx, SaveConfigRequest{Config: cfg, NewKey: oldKey}); err != nil {
						t.Fatal(err)
					}
					if mgr.Snapshot().PendingOnDisk != 1 || mgr.Snapshot().KeptQueues != 0 {
						t.Fatal("queue must move back to active ownership")
					}
					if err := mgr.UploadKeptReports(ctx); err != nil || uploads.Load() != 0 {
						t.Fatal("recovery must not open the active queue", err)
					}
				} else {
					unavailable.Store(true)
					short, cancelAttempt := context.WithTimeout(t.Context(), 50*time.Millisecond)
					err := mgr.UploadKeptReports(short)
					cancelAttempt()
					if err == nil || mgr.Snapshot().KeptQueues != 1 {
						t.Fatal("failed recovery must preserve the old credential and queue")
					}
					unavailable.Store(false)
					if err := mgr.UploadKeptReports(ctx); err != nil || mgr.Snapshot().KeptQueues != 0 || uploads.Load() != 1 {
						t.Fatal("failed to recover reports after retry", err)
					}
					reopened, err := NewRelayManager(store, nil)
					if err != nil || reopened.Snapshot().KeptQueues != 0 || !reopened.Snapshot().Configured {
						t.Fatal("completed recovery must persist without disrupting active settings", err)
					}
				}
			}
			if _, ok := store.keys.(testKeys)[oldID]; ok {
				t.Fatal("unreferenced credential was retained")
			}
		})
	}
}

func TestKeptQueuesShareCredentialUntilFinalDrain(t *testing.T) {
	const shared = "llmm_two_servers"
	first := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-LLM-Meter-Key") != shared {
			t.Error("first queue received the wrong key")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer first.Close()
	trustTestMeter(t, first)
	var finalAttempt atomic.Bool
	var secondAttempts atomic.Int32
	second := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-LLM-Meter-Key") != shared {
			t.Error("second queue received the wrong key")
		}
		if secondAttempts.Add(1) == 1 || finalAttempt.Load() {
			w.WriteHeader(http.StatusUnprocessableEntity)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer second.Close()
	store := testStore(t)
	mgr, err := NewRelayManager(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := AppConfig{Target: first.URL, Mode: relay.ModeUsageOnly}
	if err := mgr.SaveConfig(t.Context(), SaveConfigRequest{Config: cfg, NewKey: shared}); err != nil {
		t.Fatal(err)
	}
	firstID := mgr.cfg.KeyID
	enqueueOfflineReport(t, store, cfg.Target, shared)
	cfg.Target = second.URL
	if err := mgr.SaveConfig(t.Context(), SaveConfigRequest{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	enqueueOfflineReport(t, store, cfg.Target, shared)
	enqueueOfflineReport(t, store, cfg.Target, shared)
	if err := mgr.SaveConfig(t.Context(), SaveConfigRequest{Config: cfg, NewKey: "llmm_replacement"}); err != nil {
		t.Fatal(err)
	}

	short, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	err = mgr.UploadKeptReports(short)
	cancel()
	var loss *relayoutbox.DrainLoss
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &loss) || loss.Discarded != 1 ||
		mgr.Snapshot().KeptQueues != 1 || store.keys.(testKeys)[firstID] != shared {
		t.Fatal("partial recovery must retain the credential for the remaining endpoint", err)
	}
	finalAttempt.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	err = mgr.UploadKeptReports(ctx)
	loss = nil
	if !errors.As(err, &loss) || loss.Discarded != 1 || mgr.Snapshot().KeptQueues != 0 {
		t.Fatal("permanent rejection must report the loss and retire the completed queue", err)
	}
	if _, ok := store.keys.(testKeys)[firstID]; ok {
		t.Fatal("final drain must retire the unused credential")
	}
}

func TestRetryApplyRetainsRunningIntent(t *testing.T) {
	meter := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer meter.Close()
	trustTestMeter(t, meter)
	for _, explicitStop := range []bool{false, true} {
		store := testStore(t)
		mgr, err := NewRelayManager(store, nil)
		if err != nil {
			t.Fatal(err)
		}
		cfg := AppConfig{Target: meter.URL, Listen: "127.0.0.1:0", Mode: relay.ModeUsageOnly}
		key := "llmm_fixture"
		enqueueOfflineReport(t, store, cfg.Target, key)
		if err := mgr.SaveConfig(t.Context(), SaveConfigRequest{Config: cfg, NewKey: key, StartAfterSave: true}); err != nil {
			t.Fatal(err)
		}
		done := mgr.inst.Done()
		cfg.Mode = relay.ModeRemoteProxy
		short, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		err = mgr.SaveConfig(short, SaveConfigRequest{Config: cfg, DrainBeforeSwitch: true})
		cancel()
		if err == nil {
			t.Fatal("expected failed drain")
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("relay did not stop")
		}
		if explicitStop {
			_ = mgr.Stop(t.Context())
		}
		if err := mgr.SaveConfig(t.Context(), SaveConfigRequest{Config: cfg}); err != nil {
			t.Fatal(err)
		}
		if running := mgr.Snapshot().Status.State == relay.StateRunning; running == explicitStop {
			t.Errorf("running after retry = %v, explicit stop = %v", running, explicitStop)
		}
		if err := mgr.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAntigravityLauncherGenerationAndInstall(t *testing.T) {
	script, err := BuildAntigravityLauncher("127.0.0.1:8765", "/Applications/Antigravity.app/Contents/MacOS/antigravity")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "--cloud_code_endpoint 'http://127.0.0.1:8765'") {
		t.Fatalf("unexpected launcher script:\n%s", script)
	}

	tmp := t.TempDir()
	source := filepath.Join(tmp, "real cli's bin")
	if err := os.WriteFile(source, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(tmp, "antigravity")
	installed, err := InstallAntigravityLauncher(dest, "127.0.0.1:8765", source)
	if err != nil || installed != dest {
		t.Fatalf("InstallAntigravityLauncher = %q, %v", installed, err)
	}
	info, err := os.Stat(dest)
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("installed launcher must be executable: %v", err)
	}

	out, err := exec.CommandContext(t.Context(), dest, "acp", "--argument with spaces").Output()
	if err != nil || string(out) != "acp\n--cloud_code_endpoint\nhttp://127.0.0.1:8765\n--argument with spaces\n" {
		t.Fatalf("launcher argument forwarding = %q, %v", out, err)
	}
	out, err = exec.CommandContext(t.Context(), dest, "--version").Output()
	if err != nil || string(out) != "--version\n" {
		t.Fatalf("launcher intercepted a non-ACP command: %q, %v", out, err)
	}
	existing := filepath.Join(tmp, "agy")
	if err := os.WriteFile(existing, []byte("user launcher"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallAntigravityLauncher(existing, "127.0.0.1:8765", source); err == nil {
		t.Fatal("install would replace an unrelated existing launcher")
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != "user launcher" {
		t.Fatal("existing launcher changed", err)
	}
	if _, err := InstallAntigravityLauncher(filepath.Join(tmp, "wrong-name"), "127.0.0.1:8765", source); err == nil {
		t.Fatal("expected non-antigravity/agy filename to be rejected")
	}
	if _, err := InstallAntigravityLauncher(dest, "127.0.0.1:8765", dest); err == nil {
		t.Fatal("expected overwriting source binary to be rejected")
	}
}
