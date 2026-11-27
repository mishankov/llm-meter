package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
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
	"github.com/mishankov/llm-meter/internal/usagereport"
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

func TestConfigStoreMigratesListeners(t *testing.T) {
	for _, test := range []struct {
		name, config, mode, proxyAddr, telemetryAddr string
		proxy, telemetry                             bool
	}{
		{
			name: "legacy proxy", config: `{"mode":"remote-proxy","listen":"127.0.0.1:9880","keyId":"fixture"}`,
			mode: relay.ModeRemoteProxy, proxyAddr: "127.0.0.1:9880", telemetryAddr: defaultTelemetryListen,
			proxy: true,
		},
		{
			name: "legacy telemetry", config: `{"mode":"telemetry","listen":"127.0.0.1:8765","tlsCert":"cert.pem","tlsKey":"key.pem","keyId":"fixture"}`,
			mode: relay.ModeUsageOnly, proxyAddr: "127.0.0.1:8766", telemetryAddr: relay.DefaultListen,
			telemetry: true,
		},
		{
			name: "both disabled", config: `{"mode":"usage-only","proxyEnabled":false,"telemetryEnabled":false,"keyId":"fixture"}`,
			mode: relay.ModeUsageOnly, proxyAddr: relay.DefaultListen, telemetryAddr: defaultTelemetryListen,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := testStore(t)
			if err := os.WriteFile(filepath.Join(store.dir, configFile), []byte(test.config), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := store.LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Mode != test.mode || cfg.ProxyEnabled != test.proxy || cfg.TelemetryEnabled != test.telemetry ||
				cfg.Listen != test.proxyAddr || cfg.TelemetryListen != test.telemetryAddr || cfg.KeyID != "fixture" {
				t.Fatalf("unexpected migrated config: %+v", cfg)
			}
			if test.telemetry && (cfg.TLSCert != "cert.pem" || cfg.TLSKey != "key.pem") {
				t.Fatal("migration discarded TLS paths")
			}
		})
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
			Target:       "https://proxy.test",
			Mode:         relay.ModeRemoteProxy,
			ProxyEnabled: true,
			Listen:       "127.0.0.1:0",
			AutoStart:    true,
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

func TestOccupiedTelemetryPortKeepsRunningProxy(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	store := testStore(t)
	mgr, err := NewRelayManager(store, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	cfg := AppConfig{Target: "https://proxy.test", Mode: relay.ModeRemoteProxy, ProxyEnabled: true, Listen: "127.0.0.1:0"}
	const key = "llmm_working_proxy"
	if err := mgr.SaveConfig(t.Context(), SaveConfigRequest{Config: cfg, NewKey: key, StartAfterSave: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Stop(context.Background()) })
	before := mgr.Snapshot()

	cfg.TelemetryEnabled = true
	cfg.TelemetryListen = occupied.Addr().String()
	if err := mgr.SaveConfig(t.Context(), SaveConfigRequest{Config: cfg, NewKey: "llmm_replacement"}); err == nil {
		t.Fatal("saved a telemetry listener on an occupied port")
	}
	after := mgr.Snapshot()
	if after.Config != before.Config || after.Status.ListenAddr != before.Status.ListenAddr || after.Status.State != relay.StateRunning {
		t.Fatalf("failed to preserve the running proxy: %+v", after)
	}
	saved, err := store.LoadConfig()
	if err != nil || saved.AppConfig != before.Config {
		t.Fatalf("failed to preserve saved settings: %v, %v", saved.AppConfig, err)
	}
	if savedKey, err := store.LoadKey(saved.KeyID); err != nil || savedKey != key {
		t.Fatal("failed to preserve the proxy credential", err)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+before.Status.ListenAddr+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer provider-token")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("proxy returned %d after failed apply", response.StatusCode)
	}
	if err := occupied.Close(); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SaveConfig(t.Context(), SaveConfigRequest{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	req, err = http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+cfg.TelemetryListen+"/otlp/v1/logs", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("OpenTelemetry returned %d after successful apply", response.StatusCode)
	}

	cfg.Listen = mgr.Snapshot().Status.ListenAddr
	cfg.TelemetryListen = "127.0.0.1:0"
	if err := mgr.SaveConfig(t.Context(), SaveConfigRequest{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	status := mgr.Snapshot().Status
	if status.State != relay.StateRunning || status.ListenAddr != cfg.Listen ||
		status.TelemetryListenAddr == cfg.TelemetryListen || status.TelemetryListenAddr == status.ListenAddr {
		t.Fatalf("expected a fixed proxy and an assigned telemetry port: %+v", status)
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
	cfg := AppConfig{Target: "https://meter.test", Listen: "127.0.0.1:0", Mode: relay.ModeRemoteProxy, ProxyEnabled: true}
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
	cfg := AppConfig{Target: "https://meter.invalid", Mode: relay.ModeUsageOnly, ProxyEnabled: true, Listen: relay.DefaultListen}
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
			cfg := AppConfig{Target: meter.URL, Listen: "127.0.0.1:0", Mode: relay.ModeUsageOnly, ProxyEnabled: true}
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

func TestDesktopTelemetryAndProxyRunTogether(t *testing.T) {
	const key = "llmm_telemetry_fixture"
	uploaded := make(chan usagereport.Event, 1)
	meter := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/usage/events" || r.Header.Get("X-LLM-Meter-Key") != key {
			t.Error("unexpected usage upload destination or key")
		}
		var event usagereport.Event
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
		select {
		case uploaded <- event:
		case <-r.Context().Done():
		}
	}))
	defer meter.Close()
	trustTestMeter(t, meter)

	store := testStore(t)
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/models" {
			t.Error("unexpected proxied request")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	mgr, err := NewRelayManager(store, transport)
	if err != nil {
		t.Fatal(err)
	}
	cfg := AppConfig{
		Target: meter.URL, Mode: relay.ModeRemoteProxy, ProxyEnabled: true, Listen: "127.0.0.1:0", Provider: "openai",
		TelemetryEnabled: true, TelemetryListen: "127.0.0.1:0",
		TLSCert: filepath.Join(store.dir, "cert.pem"), TLSKey: filepath.Join(store.dir, "key.pem"),
	}
	cert := meter.TLS.Certificates[0]
	privateKey, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.TLSCert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.TLSKey, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SaveConfig(t.Context(), SaveConfigRequest{Config: cfg, NewKey: key, StartAfterSave: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := mgr.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	snap := mgr.Snapshot()
	if !snap.Configured || snap.Status.State != relay.StateRunning || len(snap.Status.Routes) != 1 ||
		snap.Status.ListenAddr == snap.Status.TelemetryListenAddr || snap.Status.Outbox == nil ||
		len(snap.Endpoints) != 2 || snap.Endpoints[0].Provider != "openai" || snap.Endpoints[1].Provider != relay.ModeTelemetry ||
		snap.Endpoints[0].URL != "http://"+snap.Status.ListenAddr+"/v1" ||
		snap.Endpoints[1].URL != "https://"+snap.Status.TelemetryListenAddr+"/otlp" {
		t.Fatalf("unexpected combined listener snapshot: %+v", snap)
	}
	saved, err := store.LoadConfig()
	if err != nil || saved.TLSCert != cfg.TLSCert || saved.TLSKey != cfg.TLSKey {
		t.Fatal("telemetry TLS paths did not persist", err)
	}
	cfg.TLSKey += ".missing"
	if err := mgr.SaveConfig(t.Context(), SaveConfigRequest{Config: cfg}); err == nil {
		t.Fatal("saved an unreadable TLS key")
	}
	after := mgr.Snapshot()
	if after.Config.TLSKey != saved.TLSKey || after.Status.ListenAddr != snap.Status.ListenAddr ||
		after.Status.TelemetryListenAddr != snap.Status.TelemetryListenAddr || after.Status.State != relay.StateRunning {
		t.Fatal("invalid TLS settings disrupted the running relay")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	proxyReq, err := http.NewRequestWithContext(ctx, http.MethodGet, snap.Endpoints[0].URL+"/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	proxyReq.Header.Set("Authorization", "Bearer provider-fixture")
	proxyResp, err := http.DefaultClient.Do(proxyReq)
	if err != nil {
		t.Fatal(err)
	}
	proxyResp.Body.Close()
	if proxyResp.StatusCode != http.StatusOK || proxyResp.TLS != nil {
		t.Fatalf("HTTP proxy response: %s", proxyResp.Status)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, snap.Endpoints[1].URL+"/v1/logs", strings.NewReader(`{
		"resourceLogs":[{"scopeLogs":[{"logRecords":[{
			"eventName":"claude_code.api_request",
			"attributes":[
				{"key":"event.timestamp","value":{"stringValue":"2026-01-01T00:00:00Z"}},
				{"key":"model","value":{"stringValue":"claude-sonnet-fixture"}},
				{"key":"input_tokens","value":{"intValue":"10"}},
				{"key":"output_tokens","value":{"intValue":"20"}}
			]
		}]}]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := meter.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || response.TLS == nil {
		t.Fatalf("HTTPS OTLP response: %s", response.Status)
	}
	select {
	case event := <-uploaded:
		if event.Model != "claude-sonnet-fixture" || event.InputTokens != 10 || event.OutputTokens != 20 {
			t.Fatalf("unexpected usage upload: %+v", event)
		}
	case <-ctx.Done():
		t.Fatal("relay did not upload the harness event")
	}

	cfg.TLSKey = saved.TLSKey
	cfg.ProxyEnabled = false
	if err := mgr.SaveConfig(ctx, SaveConfigRequest{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	snap = mgr.Snapshot()
	if snap.Status.State != relay.StateRunning || snap.Status.ListenAddr != "" || snap.Status.TelemetryListenAddr == "" ||
		len(snap.Endpoints) != 1 || snap.Endpoints[0].URL != "https://"+snap.Status.TelemetryListenAddr+"/otlp" ||
		snap.Config.Provider != "openai" {
		t.Fatalf("telemetry did not stay enabled when turning off the proxy: %+v", snap)
	}

	cfg.TelemetryEnabled = false
	cfg.TLSKey += ".missing"
	if err := mgr.SaveConfig(ctx, SaveConfigRequest{Config: cfg, StartAfterSave: true}); err != nil {
		t.Fatal(err)
	}
	snap = mgr.Snapshot()
	if snap.Status.State != relay.StateStopped || len(snap.Endpoints) != 0 ||
		snap.Status.ListenAddr != "" || snap.Status.TelemetryListenAddr != "" || snap.Config.TLSKey != cfg.TLSKey {
		t.Fatalf("disabling both listeners did not stop the relay: %+v", snap)
	}
	if err := mgr.Start(); err == nil {
		t.Fatal("starting a profile with both listeners disabled should fail")
	}
}

func TestDesktopTelemetrySharesAndRetainsUsageQueue(t *testing.T) {
	const key = "llmm_original"
	meter := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-LLM-Meter-Key") != key {
			t.Error("kept telemetry uploaded with wrong key")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer meter.Close()
	trustTestMeter(t, meter)
	store := testStore(t)
	mgr, err := NewRelayManager(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := AppConfig{Target: meter.URL, Mode: relay.ModeUsageOnly, ProxyEnabled: true}
	if err := mgr.SaveConfig(t.Context(), SaveConfigRequest{Config: cfg, NewKey: key}); err != nil {
		t.Fatal(err)
	}
	dir := enqueueOfflineReport(t, store, cfg.Target, key)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cfg.Mode = relay.ModeRemoteProxy
	cfg.TelemetryEnabled = true
	cfg.Target += "/"
	if err := mgr.SaveConfig(ctx, SaveConfigRequest{Config: cfg, DrainBeforeSwitch: true}); err != nil {
		t.Fatal(err)
	}
	if snap := mgr.Snapshot(); snap.KeptQueues != 0 || snap.PendingOnDisk != 1 ||
		len(snap.Endpoints) != 3 ||
		snap.Endpoints[1].URL != "http://"+relay.DefaultListen+"/antigravity" ||
		snap.Endpoints[2].URL != "http://"+defaultTelemetryListen+"/otlp" {
		t.Fatalf("telemetry did not inherit the active usage queue: %+v", snap)
	}

	cfg.TelemetryEnabled = false
	if err := mgr.SaveConfig(ctx, SaveConfigRequest{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	if snap := mgr.Snapshot(); snap.KeptQueues != 1 || snap.PendingOnDisk != 0 {
		t.Fatalf("disabling the last local usage source did not retain the queue: %+v", snap)
	}
	cfg.TelemetryEnabled = true
	if err := mgr.SaveConfig(ctx, SaveConfigRequest{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	if snap := mgr.Snapshot(); snap.KeptQueues != 0 || snap.PendingOnDisk != 1 {
		t.Fatalf("re-enabling telemetry did not reclaim the retained queue: %+v", snap)
	}

	if err := mgr.SaveConfig(ctx, SaveConfigRequest{Config: cfg, NewKey: "llmm_replacement"}); err != nil {
		t.Fatal(err)
	}
	mgr, err = NewRelayManager(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snap := mgr.Snapshot(); snap.KeptQueues != 1 || snap.PendingOnDisk != 0 {
		t.Fatalf("rotating telemetry credentials did not retain the old queue: %+v", snap)
	}
	if err := mgr.UploadKeptReports(ctx); err != nil {
		t.Fatal(err)
	}
	if count, err := relayoutbox.PendingCount(dir); err != nil || count != 0 || mgr.Snapshot().KeptQueues != 0 {
		t.Fatal("old telemetry queue did not finish uploading", err)
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
	cfg := AppConfig{Target: first.URL, Mode: relay.ModeUsageOnly, ProxyEnabled: true}
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
		cfg := AppConfig{Target: meter.URL, Listen: "127.0.0.1:0", Mode: relay.ModeUsageOnly, ProxyEnabled: true}
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
	if !strings.Contains(script, "--cloud_code_endpoint 'http://127.0.0.1:8765/antigravity'") {
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
	if err != nil || string(out) != "acp\n--cloud_code_endpoint\nhttp://127.0.0.1:8765/antigravity\n--argument with spaces\n" {
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
