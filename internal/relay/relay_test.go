package relay

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mishankov/llm-meter/internal/proxy"
	"github.com/mishankov/llm-meter/internal/relayoutbox"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func antigravityRelay(host string, target *url.URL, key string, transport http.RoundTripper) http.Handler {
	provider := providers["antigravity"]
	return guard(host, nil, providerHandler{provider, remoteProxy(provider, target, key, transport)})
}

func TestRelayForwardsCredentialsAndStreams(t *testing.T) {
	const (
		path   = "/v1internal:streamGenerateContent"
		query  = "alt=sse&fixture=a%2Bb&label=a;b"
		body   = `{"model":"fixture"}`
		bearer = "Bearer fixture-google-token"
		key    = "llmm_fixture"
		first  = "data: first\n\n"
		last   = "data: last\n\n"
	)
	finish := make(chan struct{})
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/antigravity"+path || r.URL.RawQuery != query {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != bearer {
			t.Error("Google authorization changed")
		}
		if values := r.Header.Values(keyHeader); len(values) != 1 || values[0] != key {
			t.Error("the relay must replace any inbound proxy key")
		}
		if data, err := io.ReadAll(r.Body); err != nil || string(data) != body {
			t.Errorf("body changed: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, first)
		w.(http.Flusher).Flush()
		select {
		case <-finish:
			_, _ = io.WriteString(w, last)
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	target, err := parseHTTPS(upstream.URL + "/antigravity/")
	if err != nil {
		t.Fatal(err)
	}
	local := httptest.NewUnstartedServer(nil)
	local.Config.Handler = antigravityRelay(local.Listener.Addr().String(), target, key, upstream.Client().Transport)
	local.Start()
	defer local.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, local.URL+path+"?"+query, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", bearer)
	request.Header.Add(keyHeader, "inbound-1")
	request.Header.Add(keyHeader, "inbound-2")
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("unexpected response: %s, %s", response.Status, response.Header.Get("Content-Type"))
	}
	event := make([]byte, len(first))
	if _, err := io.ReadFull(response.Body, event); err != nil || string(event) != first {
		t.Fatalf("first event did not arrive before the upstream finished: %v", err)
	}
	close(finish)
	if data, err := io.ReadAll(response.Body); err != nil || string(data) != last {
		t.Fatalf("final event changed: %v", err)
	}
}

func TestRelayChecksRequestBeforeForwarding(t *testing.T) {
	const host = "127.0.0.1:8765"
	const rpc = "/v1internal:loadCodeAssist"
	target, err := parseHTTPS("https://proxy.test/antigravity")
	if err != nil {
		t.Fatal(err)
	}
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	})
	authorized := http.Header{"Authorization": {"Bearer fixture"}}
	for _, test := range []struct {
		name, listener, requestHost, path string
		headers                           http.Header
		status                            int
	}{
		{"implicit port 80", "127.0.0.1:80", "127.0.0.1", rpc, authorized, http.StatusNoContent},
		{"wrong port", "127.0.0.1:80", host, rpc, authorized, http.StatusForbidden},
		{"unexpected host", host, "example.test", rpc, authorized, http.StatusForbidden},
		{"browser origin", host, host, rpc, http.Header{"Origin": {"", "https://example.test"}}, http.StatusForbidden},
		{"fetch metadata", host, host, rpc, http.Header{"Sec-Fetch-Site": {"cross-site"}}, http.StatusForbidden},
		{"missing authorization", host, host, rpc, nil, http.StatusUnauthorized},
		{"dashboard route", host, host, "/api/clients", authorized, http.StatusNotFound},
		{"encoded slash", host, host, rpc + "%2f..%2fapi", authorized, http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://"+test.requestHost+test.path, nil)
			if test.headers != nil {
				request.Header = test.headers.Clone()
			}
			response := httptest.NewRecorder()
			handler := antigravityRelay(test.listener, target, "fixture", transport)
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Errorf("status %d, want %d", response.Code, test.status)
			}
		})
	}
}

func TestRelayHidesUpstreamErrors(t *testing.T) {
	const host = "127.0.0.1:8765"
	target, err := parseHTTPS("https://proxy.test/antigravity")
	if err != nil {
		t.Fatal(err)
	}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New(r.URL.String() + " " + r.Header.Get(keyHeader) + " " + r.Header.Get("Authorization"))
	})
	request := httptest.NewRequest(http.MethodPost, "http://"+host+"/v1internal:loadCodeAssist", nil)
	request.Header.Set("Authorization", "Bearer fixture")
	response := httptest.NewRecorder()
	antigravityRelay(host, target, "llmm_fixture", transport).ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || response.Body.String() != "LLM Meter unavailable\n" {
		t.Fatalf("upstream error was not sanitized: status %d", response.Code)
	}
}

func TestRelayConfiguration(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1"} {
		if !isLoopback(net.JoinHostPort(host, "8765")) {
			t.Errorf("rejected loopback address %s", host)
		}
	}
	for _, address := range []string{":8765", "0.0.0.0:8765", "[::]:8765", "example.test:8765"} {
		if isLoopback(address) {
			t.Errorf("accepted non-loopback address %s", address)
		}
	}
	for _, target := range []string{
		"http://proxy.test/antigravity", "https://proxy.test/api", "https://user@proxy.test/antigravity",
		"https://proxy.test/antigravity?x=1", "https://proxy.test/antigravity#fragment",
	} {
		if _, err := configureRelay(Config{Mode: ModeRemoteProxy, Provider: "antigravity", Target: target}); err == nil {
			t.Errorf("accepted invalid target %s", target)
		}
	}
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("llmm_fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if key, err := ReadKeyFile(path); err != nil || key != "llmm_fixture" {
		t.Fatalf("could not read the private key file: %v", err)
	}
	if err := os.WriteFile(path, []byte("llmm_fixture\nsecond-line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadKeyFile(path); err == nil {
		t.Fatal("accepted a multiline key")
	}
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(path, []byte("llmm_fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadKeyFile(path); err == nil {
			t.Fatal("accepted a group- and world-readable key file")
		}
	}
}

func TestRelayDestinations(t *testing.T) {
	for _, test := range []struct {
		name, mode, provider, target, upstream, wantRoute string
	}{
		{"remote target with route", ModeRemoteProxy, "", "https://proxy.test/antigravity", "", ""},
		{"remote with explicit provider", ModeRemoteProxy, "openai", "https://proxy.test/", "", "https://proxy.test/v1"},
		{"remote target with selected route", ModeRemoteProxy, "openai", "https://proxy.test/v1", "", ""},
		{"usage with explicit provider", ModeUsageOnly, "openai", "https://proxy.test/", "", providers["openai"].defaultUpstream},
		{"custom upstream", ModeUsageOnly, "openai", "https://proxy.test", "https://upstream.test/custom", "https://upstream.test/custom"},
		{"remote with override", ModeRemoteProxy, "openai", "https://proxy.test", "https://upstream.test/v1", ""},
		{"custom upstream without a provider", ModeUsageOnly, "", "https://proxy.test", "https://upstream.test", ""},
		{"usage with proxy route", ModeUsageOnly, "openai", "https://proxy.test/v1", "", ""},
		{"usage with same upstream host", ModeUsageOnly, "openai", "https://proxy.test", "https://PROXY.test.:8443/v1", ""},
		{"usage with a default upstream on the proxy host", ModeUsageOnly, "", "https://api.openai.com", "", ""},
		{"unknown mode", "auto", "openai", "https://proxy.test", "", ""},
		{"unknown provider", ModeRemoteProxy, "unknown", "https://proxy.test", "", ""},
		{"retired Codex provider", ModeRemoteProxy, "codex", "https://proxy.test", "", ""},
		{"retired OpenCode Go provider", ModeUsageOnly, "opencode-go", "https://proxy.test", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, err := configureRelay(Config{Mode: test.mode, Provider: test.provider, Target: test.target, Upstream: test.upstream})
			if test.wantRoute == "" {
				if err == nil {
					t.Fatal("accepted an incompatible connection configuration")
				}
				return
			}
			if err != nil || len(config.routes) != 1 || config.routes[0].target.String() != test.wantRoute {
				t.Fatalf("expected one route to %s, got %+v (%v)", test.wantRoute, config.routes, err)
			}
			if test.mode == ModeUsageOnly && (config.usageTarget == nil || config.usageTarget.String() != "https://proxy.test/api/usage/events") {
				t.Fatalf("unexpected usage target %v", config.usageTarget)
			}
		})
	}
	if addr, err := listenAddress("127.0.0.1:8765", "0.0.0.0:8765"); err != nil || addr != "0.0.0.0:8765" {
		t.Fatalf("container binding: %q, %v", addr, err)
	}
	if _, err := listenAddress("0.0.0.0:8765", "0.0.0.0:8765"); err == nil {
		t.Fatal("accepted a public-facing listen address")
	}
}

func TestProviderRoutingInBothModes(t *testing.T) {
	const (
		host   = "127.0.0.1:8765"
		bearer = "Bearer provider-fixture"
		key    = "llmm_relay-fixture"
		body   = `{"model":"fixture","input":"only the provider sees this in usage-only mode"}`
		usage  = `{"id":"resp_fixture","model":"fixture","usage":{"input_tokens":12,"output_tokens":2}}`
		gemini = `{"response":{"responseId":"resp_fixture","modelVersion":"fixture","usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":2}}}`
	)
	for _, test := range []struct {
		name, provider, authHeader, localPath, proxyPath, directPath, response string
	}{
		{"antigravity", "antigravity", "Authorization", "/v1internal:generateContent", "/antigravity/v1internal:generateContent", "/direct/v1internal:generateContent", gemini},
		{"openai", "openai", "Authorization", "/v1/responses", "/v1/responses", "/direct/responses", usage},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := providers[test.provider]
			request := func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "http://"+host+test.localPath+"?fixture=encoded%2Bquery", strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set(test.authHeader, bearer)
				r.Header.Set(keyHeader, "untrusted-inbound-key")
				return r
			}
			checkRequest := func(r *http.Request, path, wantKey string) {
				if r.URL.Path != path || r.URL.RawQuery != "fixture=encoded%2Bquery" || r.Header.Get(test.authHeader) != bearer || r.Header.Get(keyHeader) != wantKey {
					t.Error("upstream route, query, or credentials changed")
				}
				if data, err := io.ReadAll(r.Body); err != nil || string(data) != body {
					t.Errorf("upstream body changed: %v", err)
				}
			}
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				checkRequest(r, test.proxyPath, key)
				return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
			})
			target, err := parseHTTPS("https://proxy.test" + provider.prefix)
			if err != nil {
				t.Fatal(err)
			}
			remote := httptest.NewRecorder()
			guard(host, nil, providerHandler{provider, remoteProxy(provider, target, key, transport)}).ServeHTTP(remote, request())
			if remote.Code != http.StatusNoContent {
				t.Fatalf("remote proxy returned %d", remote.Code)
			}

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				checkRequest(r, test.directPath, "")
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.response)
			}))
			defer upstream.Close()
			base, err := url.Parse(upstream.URL + "/direct")
			if err != nil {
				t.Fatal(err)
			}
			var observed []proxy.Usage
			local := httptest.NewRecorder()
			forward := directProxy(provider, *base, func(usage proxy.Usage) {
				observed = append(observed, usage)
			})
			guard(host, nil, providerHandler{provider, forward}).ServeHTTP(local, request())
			if local.Code != http.StatusOK || local.Body.String() != test.response {
				t.Errorf("local relay changed provider response, status %d", local.Code)
			}
			if len(observed) != 1 || !observed[0].HasUsage || observed[0].InputTokens != 12 || observed[0].OutputTokens != 2 || observed[0].Endpoint != test.proxyPath {
				t.Errorf("unexpected local usage: %+v", observed)
			}
		})
	}
}

func TestDefaultRelayDispatchesAllProviders(t *testing.T) {
	const host = "127.0.0.1:8765"
	const key = "llmm_fixture"
	const authorization = "Bearer provider-fixture"
	requests := []struct {
		provider, method, localPath, remotePath, directPath string
	}{
		{"antigravity", http.MethodPost, "/v1internal:loadCodeAssist", "/antigravity/v1internal:loadCodeAssist", "/v1internal:loadCodeAssist"},
		{"openai", http.MethodGet, "/v1/models", "/v1/models", "/v1/models"},
	}
	for _, mode := range []string{ModeRemoteProxy, ModeUsageOnly} {
		t.Run(mode, func(t *testing.T) {
			config, err := configureRelay(Config{Mode: mode, Target: "https://proxy.test/"})
			if err != nil || len(config.routes) != len(requests) {
				t.Fatalf("expected all provider routes, got %+v (%v)", config.routes, err)
			}
			check := func(r *http.Request) string {
				t.Helper()
				for _, request := range requests {
					path, wantKey := request.remotePath, key
					if mode == ModeUsageOnly {
						path, wantKey = request.directPath, ""
					}
					if r.URL.Path == path && r.Method == request.method {
						if r.Header.Get("Authorization") != authorization || r.Header.Get(keyHeader) != wantKey || r.URL.RawQuery != "fixture=1" {
							t.Errorf("authorization, proxy key, or query changed for %s", request.provider)
						}
						return request.provider
					}
				}
				t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
				return ""
			}
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(check(r)))}, nil
			})
			if mode == ModeUsageOnly {
				if config.usageTarget == nil || config.usageTarget.String() != "https://proxy.test/api/usage/events" {
					t.Fatalf("incorrect usage target: %v", config.usageTarget)
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.WriteString(w, check(r))
				}))
				defer upstream.Close()
				fixture, err := url.Parse(upstream.URL)
				if err != nil {
					t.Fatal(err)
				}
				for i, route := range config.routes {
					if route.target.String() != route.provider.defaultUpstream {
						t.Fatalf("incorrect default upstream: %s", &route.target)
					}
					config.routes[i].target.Scheme = fixture.Scheme
					config.routes[i].target.Host = fixture.Host
				}
			}
			var observed []proxy.Usage
			handler := config.handler(host, key, func(usage proxy.Usage) { observed = append(observed, usage) }, transport, nil)
			for _, request := range requests {
				r := httptest.NewRequest(request.method, "http://"+host+request.localPath+"?fixture=1", nil)
				r.Header.Set("Authorization", authorization)
				r.Header.Set(keyHeader, "untrusted-inbound-key")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, r)
				if response.Code != http.StatusOK || response.Body.String() != request.provider {
					t.Fatalf("incorrect route for %s: HTTP %d", request.provider, response.Code)
				}
			}
			if mode == ModeUsageOnly {
				if len(observed) != len(requests) {
					t.Fatalf("got %d local observations, want %d", len(observed), len(requests))
				}
				for i, usage := range observed {
					if usage.Endpoint != requests[i].remotePath || usage.Status != http.StatusOK {
						t.Errorf("unexpected observation for %s: %+v", requests[i].provider, usage)
					}
				}
			} else if len(observed) != 0 {
				t.Fatal("remote-proxy mode metered usage locally")
			}
			for _, path := range []string{"/api/usage/events", "/codex/responses", "/opencode-go/models", "/v1/../opencode-go/models", "/v1suffix", "/backend-api/codex/models"} {
				r := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
				r.Header.Set("Authorization", authorization)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, r)
				if response.Code != http.StatusNotFound {
					t.Errorf("unexpected status for %s: %d", path, response.Code)
				}
			}
		})
	}
}

func TestShutdownWaitsForWebSocketMetering(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finalizing := make(chan struct{})
	finish := make(chan struct{}, 1)
	stopped := make(chan error, 1)
	go func() {
		stopped <- serve(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			upgrader := websocket.Upgrader{}
			socket, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer socket.Close()
			<-r.Context().Done()
			close(finalizing)
			select {
			case <-finish:
			case <-t.Context().Done():
			}
		}))
	}()
	dialer := websocket.Dialer{HandshakeTimeout: time.Second}
	socket, _, err := dialer.Dial("ws://"+listener.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	cancel()
	select {
	case <-finalizing:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the hijacked request")
	}
	select {
	case <-stopped:
		t.Fatal("shutdown returned before metering finished")
	case <-time.After(30 * time.Millisecond):
	}
	finish <- struct{}{}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish after the WebSocket handler returned")
	}
}

func TestInstanceLifecycleAndRequestCounter(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	})
	statuses := make(chan Status, 8)
	inst, err := New(Config{
		Listen: "127.0.0.1:0",
		Mode:   ModeRemoteProxy,
		Target: "https://proxy.test",
		Key:    "llmm_instance_fixture",
	}, WithTransport(transport), WithStatusObserver(func(s Status) {
		select {
		case statuses <- s:
		default:
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	st := inst.Status()
	if st.State != StateRunning || st.ListenAddr == "" || len(st.Routes) != 2 {
		t.Fatalf("unexpected running status: %+v", st)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+st.ListenAddr+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer fixture")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := inst.Status().RequestsTotal; got != 1 {
		t.Fatalf("requestsTotal = %d, want 1", got)
	}

	stopCtx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := inst.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if got := inst.Status().State; got != StateStopped {
		t.Fatalf("state after Stop = %q, want %q", got, StateStopped)
	}
}

func TestConnectionIDAndProviders(t *testing.T) {
	id1, err := ConnectionID("https://proxy.test", "llmm_a")
	if err != nil || len(id1) != 16 {
		t.Fatalf("unexpected ConnectionID: %q (%v)", id1, err)
	}
	id2, err := ConnectionID("https://proxy.test/", "llmm_a")
	if err != nil || id2 != id1 {
		t.Fatalf("ConnectionID should be stable for trailing slash: %q vs %q (%v)", id1, id2, err)
	}
	id3, err := ConnectionID("https://proxy.test", "llmm_b")
	if err != nil || id3 == id1 {
		t.Fatalf("ConnectionID should differ for different keys: %q vs %q (%v)", id1, id3, err)
	}
	if len(Providers()) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(Providers()))
	}
}

func TestTelemetryRelayOnlyAcceptsLocalOTLPExports(t *testing.T) {
	config := Config{Mode: ModeTelemetry, Listen: "127.0.0.1:0", Target: "https://meter.test",
		Key: "fixture-key", DataDir: t.TempDir()}
	for _, invalid := range []Config{
		{Mode: ModeTelemetry, Target: config.Target, Provider: "openai"},
		{Mode: ModeTelemetry, Target: config.Target, Upstream: "https://provider.test"},
		{Mode: ModeTelemetry, Target: config.Target, TLSCert: "cert.pem"},
		{Mode: ModeUsageOnly, Target: config.Target, TLSCert: "cert.pem", TLSKey: "key.pem"},
	} {
		if _, err := configureRelay(invalid); err == nil {
			t.Fatalf("accepted a model routing option in telemetry mode: %+v", invalid)
		}
	}
	inst, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	defer func() {
		if err := inst.Stop(ctx); err != nil {
			t.Error(err)
		}
	}()
	for _, test := range []struct {
		name, method, path, host string
		headers                  http.Header
		status                   int
	}{
		{name: "local export", method: http.MethodPost, path: "/v1/logs", status: http.StatusOK},
		{name: "trace export", method: http.MethodPost, path: "/v1/traces", status: http.StatusOK},
		{name: "metric export", method: http.MethodPost, path: "/v1/metrics", status: http.StatusOK},
		{name: "model traffic", method: http.MethodPost, path: "/v1/messages", status: http.StatusNotFound},
		{name: "path suffix", method: http.MethodPost, path: "/v1/logs/", status: http.StatusNotFound},
		{name: "wrong method", method: http.MethodGet, path: "/v1/logs", status: http.StatusMethodNotAllowed},
		{name: "wrong host", method: http.MethodPost, path: "/v1/logs", host: "example.test", status: http.StatusForbidden},
		{name: "browser origin", method: http.MethodPost, path: "/v1/logs", headers: http.Header{"Origin": {"https://example.test"}}, status: http.StatusForbidden},
		{name: "fetch metadata", method: http.MethodPost, path: "/v1/logs", headers: http.Header{"Sec-Fetch-Site": {"cross-site"}}, status: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(ctx, test.method, "http://"+inst.Status().ListenAddr+test.path,
				strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			request.Host = test.host
			for name, values := range test.headers {
				request.Header[name] = values
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.status {
				t.Fatalf("response status %d, want %d", response.StatusCode, test.status)
			}
		})
	}
	status := inst.Status()
	if status.RequestsTotal != 3 || len(status.Routes) != 0 || status.Outbox == nil || status.Outbox.Pending != 0 {
		t.Fatalf("unexpected telemetry relay status: %+v", status)
	}
}

func TestTelemetryRelayAcceptsTrustedTLS(t *testing.T) {
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	defer fixture.Close()
	dir := t.TempDir()
	certFile, keyFile := writeTestTLSFiles(t, dir, fixture.TLS.Certificates[0])
	inst, err := New(Config{
		Mode: ModeTelemetry, Listen: "127.0.0.1:0", Target: "https://meter.test",
		Key: "fixture-key", DataDir: dir, TLSCert: certFile, TLSKey: keyFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() {
		if err := inst.Stop(ctx); err != nil {
			t.Error(err)
		}
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+inst.Status().ListenAddr+"/v1/traces", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := fixture.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.TLS == nil || response.StatusCode != http.StatusOK {
		t.Fatalf("expected trusted HTTPS OTLP, got %v", response.Status)
	}
}

func writeTestTLSFiles(t *testing.T, dir string, cert tls.Certificate) (string, string) {
	t.Helper()
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func TestProxyAndTelemetryListenersRunTogether(t *testing.T) {
	const key = "fixture-key"
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	proxyStarted := make(chan struct{})
	releaseProxy := make(chan struct{})
	reports := make(chan string, 2)
	meter := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(keyHeader) != key {
			t.Error("the dashboard client key was missing")
		}
		switch r.URL.Path {
		case "/v1/responses":
			if r.Header.Get("Authorization") != "Bearer provider-credential" {
				t.Error("provider authorization did not reach the remote proxy")
			}
			close(proxyStarted)
			select {
			case <-releaseProxy:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"model":"proxy-fixture","usage":{"input_tokens":3,"output_tokens":1}}`)
		case "/api/usage/events":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			select {
			case reports <- string(body):
			default:
				t.Error("unexpected extra telemetry reports")
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer meter.Close()
	oldTransport := http.DefaultTransport
	http.DefaultTransport = meter.Client().Transport
	defer func() { http.DefaultTransport = oldTransport }()

	cfg := Config{
		Mode: ModeRemoteProxy, Provider: "openai", Listen: "127.0.0.1:0", TelemetryListen: "127.0.0.1:0",
		Target: meter.URL, Key: key, DataDir: t.TempDir(),
	}
	cfg.TLSCert, cfg.TLSKey = writeTestTLSFiles(t, cfg.DataDir, meter.TLS.Certificates[0])
	inst, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	defer inst.Stop(ctx)
	status := inst.Status()
	if status.TelemetryListenAddr == "" || status.TelemetryListenAddr == status.ListenAddr || status.Outbox == nil {
		t.Fatalf("expected distinct proxy and telemetry listeners with an outbox: %+v", status)
	}
	proxyReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+status.ListenAddr+"/v1/responses", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	proxyReq.Header.Set("Authorization", "Bearer provider-credential")
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyFinished := make(chan error, 1)
	go func() {
		resp, err := meter.Client().Do(proxyReq)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				proxyFinished <- errors.New("proxy request returned " + resp.Status)
				return
			}
			_, err = io.Copy(io.Discard, resp.Body)
		}
		proxyFinished <- err
	}()
	select {
	case <-proxyStarted:
	case err := <-proxyFinished:
		t.Fatalf("request did not reach the remote proxy: %v", err)
	case <-ctx.Done():
		t.Fatal("remote proxy did not receive the request")
	}

	// The receiver is available over TLS while the HTTP proxy request is still in flight.
	otlp := `{"resourceLogs":[{"scopeLogs":[{"logRecords":[{
		"eventName":"claude_code.api_request",
		"attributes":[
			{"key":"event.timestamp","value":{"stringValue":"2026-01-01T00:00:00Z"}},
			{"key":"model","value":{"stringValue":"telemetry-fixture"}},
			{"key":"input_tokens","value":{"intValue":"10"}},
			{"key":"output_tokens","value":{"intValue":"20"}}
		]
	}]}]}]}`
	telemetryReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+status.TelemetryListenAddr+"/v1/logs", strings.NewReader(otlp))
	if err != nil {
		t.Fatal(err)
	}
	telemetryReq.Header.Set("Content-Type", "application/json")
	resp, err := meter.Client().Do(telemetryReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.TLS == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTPS telemetry request returned %s", resp.Status)
	}
	close(releaseProxy)
	if err := <-proxyFinished; err != nil {
		t.Fatal(err)
	}
	if err := inst.DrainAndStop(ctx); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || !strings.Contains(<-reports, "telemetry-fixture") {
		t.Fatal("expected only the telemetry report from the local outbox")
	}
	status = inst.Status()
	if status.State != StateStopped || status.RequestsTotal != 2 || status.Outbox.Pending != 0 {
		t.Fatalf("expected both requests and a drained relay: %+v", status)
	}
	for _, addr := range []string{status.ListenAddr, status.TelemetryListenAddr} {
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("stopped relay still holds %s: %v", addr, err)
		}
		listener.Close()
	}
}

func TestCombinedRelayRequiresDistinctLoopbackListeners(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8765", "[::ffff:127.0.0.1]:8765", "0.0.0.0:4318"} {
		t.Run(address, func(t *testing.T) {
			cfg := Config{
				Mode: ModeRemoteProxy, Listen: DefaultListen, TelemetryListen: address,
				Target: "https://meter.test", Key: "fixture-key",
			}
			if err := Validate(cfg); err == nil {
				t.Fatalf("accepted incompatible telemetry listener %s", address)
			}
			if _, err := New(cfg); err == nil {
				t.Fatalf("created a relay with incompatible telemetry listener %s", address)
			}
		})
	}
}

func TestCombinedRelayContainerBindings(t *testing.T) {
	cfg := Config{
		Mode: ModeUsageOnly, Listen: DefaultListen, Bind: "0.0.0.0:18765",
		TelemetryListen: "127.0.0.1:4318", TelemetryBind: "0.0.0.0:14318",
		Target: "https://meter.test", Key: "fixture-key",
	}
	proxyBind, telemetryBind, err := relayListenAddresses(cfg)
	if err != nil || proxyBind != cfg.Bind || telemetryBind != cfg.TelemetryBind {
		t.Fatalf("expected separate container bindings: %q %q %v", proxyBind, telemetryBind, err)
	}
	for _, bind := range []string{"0.0.0.0:18765", "0.0.0.0:0", "127.0.0.1:14318", "[::]:14318"} {
		t.Run(bind, func(t *testing.T) {
			cfg := cfg
			cfg.TelemetryBind = bind
			if err := Validate(cfg); err == nil {
				t.Fatalf("accepted incompatible container telemetry bind %s", bind)
			}
		})
	}
}

func TestRelayGuardAcceptsDefaultHTTPSPort(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "https://127.0.0.1/v1/traces", nil)
	response := httptest.NewRecorder()
	guardClient("127.0.0.1:443", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("request without explicit default port returned %d", response.Code)
	}
}

func TestDrainFinishesInFlightUsageAndCanResumeOffline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	upstreamStarted := make(chan struct{})
	releaseUpstream := make(chan struct{})
	reports := make(chan string, 2)
	meter := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responses":
			close(upstreamStarted)
			select {
			case <-releaseUpstream:
			case <-ctx.Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"response-fixture","model":"fixture","usage":{"input_tokens":3,"output_tokens":1}}`)
		case "/api/usage/events":
			if r.Header.Get(keyHeader) != "fixture-key" {
				t.Error("upload used the wrong credential")
			}
			body, _ := io.ReadAll(r.Body)
			reports <- string(body)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer meter.Close()
	// Trust the fixture's TLS certificate for the uploader and provider transport.
	oldTransport := http.DefaultTransport
	http.DefaultTransport = meter.Client().Transport
	defer func() { http.DefaultTransport = oldTransport }()

	cfg := Config{Mode: ModeUsageOnly, Provider: "openai", Listen: "127.0.0.1:0",
		Target: meter.URL, Key: "fixture-key", DataDir: t.TempDir()}
	draining := make(chan struct{}, 1)
	inst, err := New(cfg, WithStatusObserver(func(st Status) {
		if st.State == StateDraining {
			select {
			case draining <- struct{}{}:
			default:
			}
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	providerURL, err := url.Parse(meter.URL)
	if err != nil {
		t.Fatal(err)
	}
	inst.config.routes[0].target = *providerURL
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	defer inst.Stop(ctx)
	listen := inst.Status().ListenAddr
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+listen+"/v1/responses", strings.NewReader(`{"model":"fixture"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer provider-credential")
	req.Header.Set("Content-Type", "application/json")
	requestDone := make(chan error, 1)
	go func() {
		resp, err := meter.Client().Do(req)
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		requestDone <- err
	}()
	select {
	case <-upstreamStarted:
	case <-ctx.Done():
		t.Fatal("provider request never started")
	}
	drained := make(chan error, 1)
	go func() { drained <- inst.DrainAndStop(ctx) }()
	select {
	case <-draining:
	case <-ctx.Done():
		t.Fatal("relay did not start draining")
	}
	close(releaseUpstream)
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
	select {
	case report := <-reports:
		if !strings.Contains(report, `"inputTokens":3`) || strings.Contains(report, "provider-credential") {
			t.Error("final in-flight usage was not reported correctly")
		}
	default:
		t.Fatal("drain finished without reporting the final request")
	}
	if st := inst.Status(); st.State != StateStopped || st.Outbox.Pending != 0 {
		t.Fatalf("relay did not stop with a drained outbox: %+v", st)
	}

	queue, err := relayoutbox.Open(cfg.DataDir, inst.config.usageTarget, cfg.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(ctx, []byte("persisted-fixture")); err != nil {
		t.Fatal(err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	// Offline rotation must also work when another process occupies the old port.
	blocker, err := net.Listen("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	cfg.Listen = listen
	if err := DrainPending(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if got := <-reports; got != "persisted-fixture" {
		t.Fatalf("unexpected offline report: %q", got)
	}
}
