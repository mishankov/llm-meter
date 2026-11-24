package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mishankov/llm-meter/internal/usagereport"
)

const (
	ModeRemoteProxy = "remote-proxy"
	ModeUsageOnly   = "usage-only"
	ModeTelemetry   = "telemetry"
	DefaultListen   = "127.0.0.1:8765"

	maxKeyBytes = 4096
)

// Config configures one relay instance.
type Config struct {
	Listen   string
	Bind     string
	Mode     string
	Provider string
	Target   string
	Upstream string
	Key      string
	DataDir  string
	TLSCert  string
	TLSKey   string
}

// Validate checks cfg without opening files, sockets, or databases.
func Validate(cfg Config) error {
	if _, err := listenAddress(cfg.Listen, cfg.Bind); err != nil {
		return err
	}
	if _, err := configureRelay(cfg); err != nil {
		return err
	}
	return ValidateKey(cfg.Key)
}

// ValidateKey checks that key is a single printable ASCII token without whitespace.
func ValidateKey(key string) error {
	trimmed := strings.TrimSpace(key)
	if len(key) > maxKeyBytes || trimmed == "" || strings.IndexFunc(trimmed, func(r rune) bool {
		return r <= ' ' || r >= 0x7f
	}) >= 0 {
		return errors.New("the proxy key must be a single non-empty key")
	}
	return nil
}

// ReadKeyFile loads and validates a private owner-only proxy key file.
func ReadKeyFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("--key-file is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("cannot open the proxy key file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("the proxy key file must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("the proxy key file must be accessible only to its owner")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxKeyBytes+1))
	key := strings.TrimSpace(string(data))
	if err != nil || len(data) > maxKeyBytes || ValidateKey(key) != nil {
		return "", fmt.Errorf("the proxy key file must contain a single key")
	}
	return key, nil
}

// ConnectionID returns a stable, non-secret short hex identifier derived from
// the usage upload destination and proxy key. Desktop callers can use it to
// isolate each (target, key) outbox directory automatically.
func ConnectionID(target, key string) (string, error) {
	origin, err := parseTargetOrigin(target)
	if err != nil {
		return "", err
	}
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	usageTarget := *origin
	usageTarget.Path, usageTarget.RawPath = usagereport.Path, ""
	sum := sha256.Sum256([]byte(usageTarget.String() + "\x00" + strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:8]), nil
}

// TestServerHealth verifies that target responds to GET /healthz with 204 No Content.
func TestServerHealth(ctx context.Context, target string) error {
	origin, err := parseTargetOrigin(target)
	if err != nil {
		return err
	}
	healthURL := *origin
	healthURL.Path, healthURL.RawPath = "/healthz", ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL.String(), nil)
	if err != nil {
		return errors.New("could not create health check request")
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("could not reach the LLM Meter server")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("server health check returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func listenAddress(listen, bind string) (string, error) {
	if !isLoopback(listen) {
		return "", errors.New("--listen must specify a loopback IP and port")
	}
	if bind == "" {
		return listen, nil
	}
	_, publicPort, _ := net.SplitHostPort(listen)
	public, publicErr := strconv.ParseUint(publicPort, 10, 16)
	host, port, err := net.SplitHostPort(bind)
	inside, insideErr := strconv.ParseUint(port, 10, 16)
	if publicErr != nil || public == 0 || err != nil || host != "0.0.0.0" || insideErr != nil || inside == 0 {
		return "", errors.New("--bind requires 0.0.0.0 and a fixed port, with a fixed loopback --listen address")
	}
	return bind, nil
}

func isLoopback(listen string) bool {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !addr.IsLoopback() {
		return false
	}
	_, err = net.LookupPort("tcp", port)
	return port != "" && err == nil
}

func parseTargetOrigin(raw string) (*url.URL, error) {
	origin, err := parseHTTPS(raw)
	if err != nil || strings.TrimSuffix(origin.EscapedPath(), "/") != "" {
		return nil, errors.New("--target must be an HTTPS LLM Meter origin without a route, user info, query, or fragment")
	}
	return origin, nil
}

func configureRelay(cfg Config) (relayConfig, error) {
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") || (cfg.TLSCert != "" && cfg.Mode != ModeTelemetry) {
		return relayConfig{}, errors.New("--tls-cert and --tls-key must be used together in telemetry mode")
	}
	selected := cfg.Provider
	if _, ok := providers[selected]; selected != "" && !ok {
		return relayConfig{}, errors.New("unknown --provider")
	}
	origin, err := parseTargetOrigin(cfg.Target)
	if err != nil {
		return relayConfig{}, err
	}
	var config relayConfig
	switch cfg.Mode {
	case ModeRemoteProxy:
		if cfg.Upstream != "" {
			return relayConfig{}, errors.New("--upstream requires --mode usage-only")
		}
	case ModeUsageOnly, ModeTelemetry:
		if cfg.Mode == ModeTelemetry && (selected != "" || cfg.Upstream != "") {
			return relayConfig{}, errors.New("--provider and --upstream cannot be used in telemetry mode")
		}
		if cfg.Upstream != "" && selected == "" {
			return relayConfig{}, errors.New("--upstream requires --provider")
		}
		usageTarget := *origin
		usageTarget.Path, usageTarget.RawPath = usagereport.Path, ""
		config.usageTarget = &usageTarget
		if cfg.Mode == ModeTelemetry {
			return config, nil
		}
	default:
		return relayConfig{}, errors.New("--mode must be remote-proxy, usage-only, or telemetry")
	}

	for _, name := range providerOrder {
		provider := providers[name]
		if selected != "" && name != selected {
			continue
		}
		if config.usageTarget == nil {
			target := *origin
			target.Path, target.RawPath = provider.prefix, ""
			config.routes = append(config.routes, relayRoute{provider, target})
			continue
		}
		upstream, err := directTarget(origin, provider, cfg.Upstream)
		if err != nil {
			return relayConfig{}, err
		}
		config.routes = append(config.routes, relayRoute{provider, *upstream})
	}
	return config, nil
}

func directTarget(origin *url.URL, provider connectionProvider, raw string) (*url.URL, error) {
	if raw == "" {
		raw = provider.defaultUpstream
	}
	upstream, err := parseHTTPS(raw)
	if err != nil {
		return nil, errors.New("--upstream must be an HTTPS provider base URL without user info, query, or fragment")
	}
	if strings.EqualFold(strings.TrimSuffix(origin.Hostname(), "."), strings.TrimSuffix(upstream.Hostname(), ".")) {
		return nil, errors.New("the provider and LLM Meter must use different hosts in usage-only mode")
	}
	return upstream, nil
}

func parseHTTPS(raw string) (*url.URL, error) {
	target, err := url.Parse(raw)
	if err != nil || target.Scheme != "https" || target.Hostname() == "" || target.User != nil ||
		strings.ContainsAny(raw, "?#") {
		return nil, errors.New("invalid HTTPS URL")
	}
	return target, nil
}
