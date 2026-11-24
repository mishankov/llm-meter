package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mishankov/llm-meter/internal/relay"
	"github.com/mishankov/llm-meter/internal/relayoutbox"
)

const maxLogEntries = 200

// ClientEndpoint describes one local URL exposed by the relay.
type ClientEndpoint struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
	URL      string `json:"url"`
}

// SaveConfigRequest updates desktop configuration and optionally rotates the proxy key.
type SaveConfigRequest struct {
	Config            AppConfig `json:"config"`
	NewKey            string    `json:"newKey"`
	DrainBeforeSwitch bool      `json:"drainBeforeSwitch"`
	StartAfterSave    bool      `json:"startAfterSave"`
}

// AppSnapshot is the complete, secret-free UI and menu-bar state snapshot.
type AppSnapshot struct {
	Configured     bool                 `json:"configured"`
	KeyConfigured  bool                 `json:"keyConfigured"`
	KeyPrefix      string               `json:"keyPrefix,omitempty"`
	Config         AppConfig            `json:"config"`
	Status         relay.Status         `json:"status"`
	PendingOnDisk  int                  `json:"pendingOnDisk"`
	KeptQueues     int                  `json:"keptQueues"`
	Endpoints      []ClientEndpoint     `json:"endpoints"`
	Providers      []relay.ProviderSpec `json:"providers"`
	Logs           []relay.LogEntry     `json:"logs"`
	AntigravityBin string               `json:"antigravityBin,omitempty"`
	LauncherPath   string               `json:"launcherPath,omitempty"`
}

// RelayManager coordinates config persistence, relay lifecycle, outbox drain, and log history.
type RelayManager struct {
	store     *ConfigStore
	transport http.RoundTripper

	restartAfterApply bool            // preserve a running relay across a failed settings change
	operation         sync.Mutex      // serialize tray and window lifecycle commands
	closing           <-chan struct{} // held under operation after a timed-out stop
	mu                sync.Mutex
	cfg               storedConfig
	key               string
	inst              *relay.Instance
	logs              []relay.LogEntry
	lastError         string
	antigravityBin    string
	launcherPath      string
	onChange          func()
}

// NewRelayManager loads persisted config and key from store.
func NewRelayManager(store *ConfigStore, transport http.RoundTripper) (*RelayManager, error) {
	cfg, err := store.LoadConfig()
	if err != nil {
		return nil, err
	}
	key, keyErr := store.LoadKey(cfg.KeyID)
	m := &RelayManager{
		store:          store,
		transport:      transport,
		cfg:            cfg,
		key:            key,
		antigravityBin: DetectAntigravityBinary(),
		launcherPath:   DefaultLauncherPath(),
	}
	if keyErr != nil {
		m.lastError = keyErr.Error()
		m.appendLogLocked("error", keyErr.Error())
	}
	return m, nil
}

// SetOnChange registers a callback invoked whenever relay state, config, or logs change.
func (m *RelayManager) SetOnChange(fn func()) {
	m.mu.Lock()
	m.onChange = fn
	m.mu.Unlock()
}

// Snapshot returns a secret-free view of current configuration, relay health, and logs.
func (m *RelayManager) Snapshot() AppSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

func (m *RelayManager) snapshotLocked() AppSnapshot {
	status := relay.Status{
		State:     relay.StateStopped,
		Mode:      m.cfg.Mode,
		Target:    m.cfg.Target,
		Routes:    selectedRoutes(m.cfg.AppConfig),
		LastError: m.lastError,
	}
	if m.lastError != "" {
		status.State = relay.StateError
	}
	if m.inst != nil {
		status = m.inst.Status()
		if !m.cfg.ProxyEnabled {
			status.TelemetryListenAddr = status.ListenAddr
			status.ListenAddr = ""
		}
	}
	if m.cfg.ProxyEnabled && status.ListenAddr == "" {
		status.ListenAddr = m.cfg.Listen
	}
	if m.cfg.TelemetryEnabled && status.TelemetryListenAddr == "" {
		status.TelemetryListenAddr = m.cfg.TelemetryListen
	}
	if m.cfg.TelemetryEnabled && !m.cfg.ProxyEnabled {
		status.Mode = relay.ModeTelemetry
	}

	pendingOnDisk := 0
	if status.Outbox != nil {
		pendingOnDisk = status.Outbox.Pending
	} else if m.cfg.hasUsageQueue() && m.cfg.Target != "" && m.key != "" {
		if qdir, err := m.store.QueueDir(m.cfg.Target, m.key); err == nil {
			if n, err := relayoutbox.PendingCount(qdir); err == nil {
				pendingOnDisk = n
			}
		}
	}

	configured := m.cfg.Target != "" && m.key != "" &&
		relay.Validate(buildRelayConfig(m.cfg.AppConfig, m.key, "")) == nil

	logs := make([]relay.LogEntry, len(m.logs))
	copy(logs, m.logs)

	return AppSnapshot{
		Configured:     configured,
		KeyConfigured:  m.key != "",
		KeyPrefix:      KeyPrefix(m.key),
		Config:         m.cfg.AppConfig,
		Status:         status,
		PendingOnDisk:  pendingOnDisk,
		KeptQueues:     len(m.cfg.Kept),
		Endpoints:      buildEndpoints(m.cfg.AppConfig, status.ListenAddr, status.TelemetryListenAddr),
		Providers:      relay.Providers(),
		Logs:           logs,
		AntigravityBin: m.antigravityBin,
		LauncherPath:   m.launcherPath,
	}
}

func (m *RelayManager) notify() {
	m.mu.Lock()
	cb := m.onChange
	m.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (m *RelayManager) appendLogLocked(level, message string) {
	m.logs = append(m.logs, relay.LogEntry{
		Timestamp: time.Now().UTC(),
		Level:     level,
		Message:   message,
	})
	if len(m.logs) > maxLogEntries {
		m.logs = m.logs[len(m.logs)-maxLogEntries:]
	}
}

// A deadline can return control to the window while the old listener or SQLite
// worker is still closing. Keep it as the sole owner until shutdown completes.
func (m *RelayManager) finishPreviousStop() error {
	if m.closing == nil {
		return nil
	}
	select {
	case <-m.closing:
		m.mu.Lock()
		m.inst = nil
		m.mu.Unlock()
		m.closing = nil
		return nil
	default:
		return errors.New("the relay is still stopping; try again once it has stopped")
	}
}

// Start launches the relay using the persisted config and key.
func (m *RelayManager) Start() error {
	m.operation.Lock()
	defer m.operation.Unlock()
	return m.start()
}

func (m *RelayManager) start() error {
	return m.startWithTelemetryListener(nil)
}

func (m *RelayManager) startWithTelemetryListener(listener net.Listener) error {
	if err := m.finishPreviousStop(); err != nil {
		return err
	}
	m.mu.Lock()
	if m.inst != nil && m.inst.Status().State == relay.StateRunning {
		m.mu.Unlock()
		m.restartAfterApply = false
		return nil
	}
	cfg := m.cfg.AppConfig
	key := m.key
	m.mu.Unlock()

	if !cfg.hasListeners() {
		return errors.New("enable the local proxy or OpenTelemetry in Settings first")
	}
	if strings.TrimSpace(cfg.Target) == "" {
		return errors.New("configure the LLM Meter server URL first")
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("configure a proxy key first")
	}

	dataDir, err := m.store.QueueDir(cfg.Target, key)
	if err != nil {
		return err
	}

	relayCfg := buildRelayConfig(cfg, key, dataDir)
	inst, err := relay.New(
		relayCfg,
		relay.WithTransport(m.transport),
		relay.WithStatusObserver(func(_ relay.Status) {
			m.notify()
		}),
		relay.WithLogObserver(func(entry relay.LogEntry) {
			m.mu.Lock()
			m.appendLogLocked(entry.Level, entry.Message)
			m.mu.Unlock()
			m.notify()
		}),
	)
	if err != nil {
		m.mu.Lock()
		m.lastError = err.Error()
		m.appendLogLocked("error", err.Error())
		m.mu.Unlock()
		m.notify()
		return err
	}

	m.mu.Lock()
	m.inst = inst
	m.lastError = ""
	m.mu.Unlock()
	if err := inst.StartWithTelemetryListener(listener); err != nil {
		m.mu.Lock()
		m.inst = nil
		m.lastError = err.Error()
		m.mu.Unlock()
		m.notify()
		return err
	}
	m.restartAfterApply = false
	return nil
}

// Stop stops the running relay gracefully.
func (m *RelayManager) Stop(ctx context.Context) error {
	m.operation.Lock()
	defer m.operation.Unlock()
	m.restartAfterApply = false
	return m.stop(ctx, false)
}

// DrainAndStop stops accepting client traffic and waits for pending usage uploads.
func (m *RelayManager) DrainAndStop(ctx context.Context) error {
	m.operation.Lock()
	defer m.operation.Unlock()
	m.restartAfterApply = false
	return m.stop(ctx, true)
}

func (m *RelayManager) stop(ctx context.Context, drain bool) error {
	m.mu.Lock()
	inst := m.inst
	m.mu.Unlock()
	if inst == nil {
		return nil
	}
	var err error
	if drain {
		err = inst.DrainAndStop(ctx)
	} else {
		err = inst.Stop(ctx)
	}
	m.mu.Lock()
	if err != nil {
		m.lastError = err.Error()
		m.appendLogLocked("warn", err.Error())
	}
	// A timed-out caller can retry Stop while the instance finishes closing.
	select {
	case <-inst.Done():
		m.inst = nil
		m.closing = nil
	default:
		m.closing = inst.Done()
	}
	m.mu.Unlock()
	m.notify()
	return err
}

// RetryUploads wakes the outbox uploader immediately.
func (m *RelayManager) RetryUploads() {
	m.mu.Lock()
	inst := m.inst
	m.mu.Unlock()
	if inst != nil {
		inst.RetryUploads()
	}
}

// ClearLogs empties the in-memory log buffer.
func (m *RelayManager) ClearLogs() {
	m.mu.Lock()
	m.logs = nil
	m.mu.Unlock()
	m.notify()
}

// SaveConfig validates, persists, and optionally drains and restarts the relay.
func (m *RelayManager) SaveConfig(ctx context.Context, req SaveConfigRequest) error {
	m.operation.Lock()
	defer m.operation.Unlock()
	if err := m.finishPreviousStop(); err != nil {
		return err
	}
	nextCfg := req.Config
	nextCfg.Target = strings.TrimSpace(nextCfg.Target)
	nextCfg.Listen = strings.TrimSpace(nextCfg.Listen)
	if nextCfg.Listen == "" {
		nextCfg.Listen = relay.DefaultListen
	}
	nextCfg.TelemetryListen = strings.TrimSpace(nextCfg.TelemetryListen)
	if nextCfg.TelemetryListen == "" {
		nextCfg.TelemetryListen = defaultTelemetryListen
	}
	if nextCfg.Mode == "" {
		nextCfg.Mode = relay.ModeUsageOnly
	}
	if nextCfg.Mode != relay.ModeUsageOnly && nextCfg.Mode != relay.ModeRemoteProxy {
		return errors.New("select Usage-only or Remote proxy as the local proxy mode")
	}
	nextCfg.Provider = strings.TrimSpace(nextCfg.Provider)
	nextCfg.Upstream = strings.TrimSpace(nextCfg.Upstream)
	nextCfg.TLSCert = strings.TrimSpace(nextCfg.TLSCert)
	nextCfg.TLSKey = strings.TrimSpace(nextCfg.TLSKey)

	m.mu.Lock()
	prev := m.cfg
	prevKey := m.key
	var status relay.Status
	if m.inst != nil {
		status = m.inst.Status()
	}
	m.mu.Unlock()
	wasRunning := status.State == relay.StateRunning
	if prevKey == "" && prev.KeyID != "" {
		// First launch can run before Keychain is unlocked. Resolve the old
		// credential before deciding which queue a settings change will leave.
		var err error
		prevKey, err = m.store.LoadKey(prev.KeyID)
		if err != nil {
			return fmt.Errorf("cannot change settings until the saved proxy key is accessible: %w", err)
		}
		m.mu.Lock()
		m.key = prevKey
		m.mu.Unlock()
	}
	newKey := strings.TrimSpace(req.NewKey)
	effectiveKey := prevKey
	if newKey != "" {
		effectiveKey = newKey
	}
	if err := validateDesktopConfig(nextCfg, effectiveKey); err != nil {
		return err
	}
	if nextCfg.TelemetryEnabled && nextCfg.TLSCert != "" {
		if _, err := tls.LoadX509KeyPair(nextCfg.TLSCert, nextCfg.TLSKey); err != nil {
			return fmt.Errorf("load telemetry TLS certificate: %w", err)
		}
	}

	var telemetryListener net.Listener
	if wasRunning && nextCfg.TelemetryEnabled {
		// Reserve new fixed telemetry ports before stopping the running relay.
		// With port 0, bind the proxy first so the OS cannot assign its port to telemetry.
		address, err := net.ResolveTCPAddr("tcp", nextCfg.TelemetryListen)
		if err != nil {
			return fmt.Errorf("resolve OpenTelemetry listen address: %w", err)
		}
		if address.Port != 0 && address.String() != status.ListenAddr && address.String() != status.TelemetryListenAddr {
			telemetryListener, err = net.ListenTCP("tcp", address)
			if err != nil {
				return fmt.Errorf("listen for OpenTelemetry: %w", err)
			}
			defer func() {
				if telemetryListener != nil {
					_ = telemetryListener.Close()
				}
			}()
		}
	}

	var nextQueueID, prevQueueDir string
	if nextCfg.hasUsageQueue() {
		var err error
		nextQueueID, err = relay.ConnectionID(nextCfg.Target, effectiveKey)
		if err != nil {
			return err
		}
	}
	if prev.hasUsageQueue() && prev.Target != "" && prevKey != "" {
		var err error
		prevQueueDir, err = m.store.QueueDir(prev.Target, prevKey)
		if err != nil {
			return err
		}
	}
	switching := prevQueueDir != "" && filepath.Base(prevQueueDir) != nextQueueID
	drain := switching && req.DrainBeforeSwitch

	if wasRunning {
		m.restartAfterApply = true
		if err := m.stop(ctx, drain); err != nil {
			return fmt.Errorf("could not stop the relay before changing settings: %w", err)
		}
	} else if drain {
		if err := drainPendingReports(ctx, prev.Target, prevKey, prevQueueDir); err != nil {
			return fmt.Errorf("could not drain pending usage reports before switching: %w", err)
		}
	}
	next := prev
	next.AppConfig = nextCfg
	next.Kept = slices.Clone(prev.Kept)
	if switching && !drain {
		count, err := relayoutbox.PendingCount(prevQueueDir)
		if err != nil {
			return fmt.Errorf("check old queue before switching: %w", err)
		}
		if count > 0 {
			next.Kept = append(next.Kept, keptQueue{ID: filepath.Base(prevQueueDir), KeyID: prev.KeyID, Config: prev.AppConfig})
		}
	}
	if nextQueueID != "" {
		// The active relay now owns this queue, even if this save stored
		// the same key in a new Keychain item.
		next.Kept = slices.DeleteFunc(next.Kept, func(q keptQueue) bool { return q.ID == nextQueueID })
	}
	if effectiveKey == prevKey {
		newKey = ""
	}
	if err := m.persist(prev, next, newKey); err != nil {
		if wasRunning {
			err = errors.Join(err, m.start())
		}
		return err
	}

	m.mu.Lock()
	m.inst = nil
	m.lastError = ""
	m.appendLogLocked("info", "Saved relay settings")
	m.mu.Unlock()
	m.notify()

	if !nextCfg.hasListeners() {
		m.restartAfterApply = false
		return nil
	}
	if m.restartAfterApply || req.StartAfterSave {
		m.restartAfterApply = true
		if err := m.startWithTelemetryListener(telemetryListener); err != nil {
			return fmt.Errorf("settings saved, but the relay could not start: %w", err)
		}
		telemetryListener = nil
	}
	return nil
}

// Publish the new references atomically before retiring unused credentials.
// Several kept queues and the active profile can share one Keychain item.
func (m *RelayManager) persist(prev, next storedConfig, newKey string) error {
	saved, err := m.store.SaveConfig(next, newKey)
	if err != nil {
		return err
	}
	retired := map[string]bool{prev.KeyID: true}
	for _, q := range prev.Kept {
		retired[q.KeyID] = true
	}
	delete(retired, "")
	delete(retired, saved.KeyID)
	for _, q := range saved.Kept {
		delete(retired, q.KeyID)
	}
	m.mu.Lock()
	m.cfg = saved
	if newKey != "" {
		m.key = newKey
	}
	m.mu.Unlock()
	for id := range retired {
		if err := m.store.keys.remove(id); err != nil {
			m.mu.Lock()
			m.appendLogLocked("warn", "Could not remove an unused credential from the key store")
			m.mu.Unlock()
		}
	}
	return nil
}

// UploadKeptReports drains retained queues independently of the active profile.
func (m *RelayManager) UploadKeptReports(ctx context.Context) error {
	m.operation.Lock()
	defer m.operation.Unlock()
	if err := m.finishPreviousStop(); err != nil {
		return err
	}
	m.mu.Lock()
	prev := m.cfg
	m.mu.Unlock()
	next := prev
	next.Kept = nil
	var failures []error
	for index, q := range prev.Kept {
		// Give each destination a turn even when another endpoint is offline.
		attempt := ctx
		cancel := func() {}
		if end, ok := ctx.Deadline(); ok {
			attempt, cancel = context.WithTimeout(ctx, time.Until(end)/time.Duration(len(prev.Kept)-index))
		}
		err := m.drainKeptQueue(attempt, q)
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("upload kept reports to %s: %w", q.Config.Target, err))
			// A permanent rejection can empty the queue. Surface the lost
			// reports, but only offer retry for work that can still run.
			if _, finished := err.(*relayoutbox.DrainLoss); !finished {
				next.Kept = append(next.Kept, q)
			}
		}
	}
	if len(next.Kept) != len(prev.Kept) {
		if err := m.persist(prev, next, ""); err != nil {
			failures = append(failures, err)
		}
		m.notify()
	}
	return errors.Join(failures...)
}

func (m *RelayManager) drainKeptQueue(ctx context.Context, q keptQueue) error {
	key, err := m.store.LoadKey(q.KeyID)
	if err != nil {
		return err
	}
	dir, err := m.store.QueueDir(q.Config.Target, key)
	if err != nil {
		return err
	}
	if filepath.Base(dir) != q.ID {
		return errors.New("saved credential no longer matches the kept queue")
	}
	return drainPendingReports(ctx, q.Config.Target, key, dir)
}

// The destination and key own the queue even when the receiver settings change.
func drainPendingReports(ctx context.Context, target, key, dir string) error {
	return relay.DrainPending(ctx, relay.Config{
		Listen: relay.DefaultListen, Mode: relay.ModeTelemetry, Target: target, Key: key, DataDir: dir,
	})
}

// TestConnection checks GET <target>/healthz.
func (m *RelayManager) TestConnection(ctx context.Context, target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		m.mu.Lock()
		target = m.cfg.Target
		m.mu.Unlock()
	}
	return relay.TestServerHealth(ctx, target)
}

func buildRelayConfig(cfg AppConfig, key, dataDir string) relay.Config {
	rc := relay.Config{
		Mode: relay.ModeRemoteProxy, Listen: relay.DefaultListen, Target: cfg.Target, Key: key, DataDir: dataDir,
	}
	if cfg.ProxyEnabled {
		rc.Mode = cfg.Mode
		rc.Listen = cfg.Listen
		rc.Provider = cfg.Provider
		if cfg.Mode == relay.ModeUsageOnly {
			rc.Upstream = cfg.Upstream
		}
	}
	if cfg.TelemetryEnabled {
		rc.TLSCert = cfg.TLSCert
		rc.TLSKey = cfg.TLSKey
		if cfg.ProxyEnabled {
			rc.TelemetryListen = cfg.TelemetryListen
		} else {
			rc.Mode = relay.ModeTelemetry
			rc.Listen = cfg.TelemetryListen
		}
	}
	return rc
}

func validateDesktopConfig(cfg AppConfig, key string) error {
	if !cfg.hasListeners() && (cfg.Target == "" || key == "") {
		if key != "" {
			return relay.ValidateKey(key)
		}
		return nil
	}
	return relay.Validate(buildRelayConfig(cfg, key, ""))
}

func selectedRoutes(cfg AppConfig) []string {
	all := relay.Providers()
	routes := make([]string, 0, len(all))
	if !cfg.ProxyEnabled {
		return routes
	}
	for _, p := range all {
		if cfg.Provider != "" && p.Name != cfg.Provider {
			continue
		}
		routes = append(routes, p.Name)
	}
	return routes
}

func buildEndpoints(cfg AppConfig, proxyAddr, telemetryAddr string) []ClientEndpoint {
	endpoints := make([]ClientEndpoint, 0, 3)
	if cfg.ProxyEnabled {
		base := "http://" + proxyAddr
		if cfg.Provider == "" || cfg.Provider == "openai" {
			endpoints = append(endpoints, ClientEndpoint{Provider: "openai", Label: "OpenAI-compatible (/v1)", URL: base + "/v1"})
		}
		if cfg.Provider == "" || cfg.Provider == "antigravity" {
			endpoints = append(endpoints, ClientEndpoint{Provider: "antigravity", Label: "Antigravity ACP (root)", URL: base})
		}
	}
	if cfg.TelemetryEnabled {
		scheme := "http://"
		if cfg.TLSCert != "" && cfg.TLSKey != "" {
			scheme = "https://"
		}
		endpoints = append(endpoints, ClientEndpoint{Provider: relay.ModeTelemetry, Label: "OTLP/HTTP", URL: scheme + telemetryAddr})
	}
	return endpoints
}
