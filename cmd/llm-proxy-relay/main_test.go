package main

import (
	"context"
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

	"github.com/mishankov/llm-proxy/internal/proxy"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func antigravityRelay(host string, target *url.URL, key string, transport http.RoundTripper) http.Handler {
	provider := providers["antigravity"]
	return guard(host, providerHandler{provider, remoteProxy(provider, target, key, transport)})
}

func TestRelayForwardsCredentialsAndStreams(t *testing.T) {
	const (
		path   = "/v1internal:streamGenerateContent"
		query  = "alt=sse&fixture=a%2Bb&label=a;b"
		body   = `{"model":"fixture"}`
		bearer = "Bearer fixture-google-token"
		key    = "llmp_fixture"
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
	antigravityRelay(host, target, "llmp_fixture", transport).ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || response.Body.String() != "LLM Proxy unavailable\n" {
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
		if _, err := configureRelay(options{mode: remoteProxyMode, provider: "antigravity", target: target}); err == nil {
			t.Errorf("accepted invalid target %s", target)
		}
	}
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("llmp_fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if key, err := readKey(path); err != nil || key != "llmp_fixture" {
		t.Fatalf("could not read the private key file: %v", err)
	}
	if err := os.WriteFile(path, []byte("llmp_fixture\nsecond-line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readKey(path); err == nil {
		t.Fatal("accepted a multiline key")
	}
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(path, []byte("llmp_fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readKey(path); err == nil {
			t.Fatal("accepted a group- and world-readable key file")
		}
	}
}

func TestRelayDestinations(t *testing.T) {
	for _, test := range []struct {
		name, mode, provider, target, upstream, wantRoute string
	}{
		{"legacy Antigravity command", remoteProxyMode, "", "https://proxy.test/antigravity", "", "https://proxy.test/antigravity"},
		{"legacy Codex route", remoteProxyMode, "", "https://proxy.test/backend-api/codex/", "", "https://proxy.test/backend-api/codex"},
		{"remote with explicit provider", remoteProxyMode, "codex", "https://proxy.test", "", "https://proxy.test/backend-api/codex"},
		{"remote with explicit matching route", remoteProxyMode, "codex", "https://proxy.test/backend-api/codex", "", "https://proxy.test/backend-api/codex"},
		{"usage with explicit provider", usageOnlyMode, "codex", "https://proxy.test/", "", providers["codex"].defaultUpstream},
		{"custom upstream", usageOnlyMode, "codex", "https://proxy.test", "https://upstream.test/codex", "https://upstream.test/codex"},
		{"remote with mismatched route", remoteProxyMode, "codex", "https://proxy.test/v1", "", ""},
		{"remote with override", remoteProxyMode, "codex", "https://proxy.test", "https://upstream.test/codex", ""},
		{"legacy Antigravity upstream override", usageOnlyMode, "", "https://proxy.test", "https://upstream.test", "https://upstream.test"},
		{"usage with proxy route", usageOnlyMode, "codex", "https://proxy.test/backend-api/codex", "", ""},
		{"usage with same upstream host", usageOnlyMode, "codex", "https://proxy.test", "https://PROXY.test.:8443/v1", ""},
		{"usage with a default upstream on the proxy host", usageOnlyMode, "", "https://chatgpt.com", "", ""},
		{"unknown mode", "auto", "codex", "https://proxy.test", "", ""},
		{"unknown provider", remoteProxyMode, "unknown", "https://proxy.test", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, err := configureRelay(options{mode: test.mode, provider: test.provider, target: test.target, upstream: test.upstream})
			if test.wantRoute == "" {
				if err == nil {
					t.Fatal("accepted an incompatible connection configuration")
				}
				return
			}
			if err != nil || len(config.routes) != 1 || config.routes[0].target.String() != test.wantRoute {
				t.Fatalf("expected one route to %s, got %+v (%v)", test.wantRoute, config.routes, err)
			}
			if test.mode == usageOnlyMode && (config.usageTarget == nil || config.usageTarget.String() != "https://proxy.test/api/usage/events") {
				t.Fatalf("unexpected usage target %v", config.usageTarget)
			}
			if test.mode == usageOnlyMode && test.provider == "" && config.routes[0].provider != providers["antigravity"] {
				t.Fatal("the legacy upstream override must scope to Antigravity")
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
		key    = "llmp_relay-fixture"
		body   = `{"model":"fixture","input":"only the provider sees this in usage-only mode"}`
		usage  = `{"id":"resp_fixture","model":"fixture","usage":{"input_tokens":12,"output_tokens":2}}`
		gemini = `{"response":{"responseId":"resp_fixture","modelVersion":"fixture","usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":2}}}`
	)
	for _, test := range []struct {
		name, provider, authHeader, localPath, proxyPath, directPath, response string
	}{
		{"antigravity", "antigravity", "Authorization", "/v1internal:generateContent", "/antigravity/v1internal:generateContent", "/direct/v1internal:generateContent", gemini},
		{"openai", "openai", "Authorization", "/v1/responses", "/v1/responses", "/direct/responses", usage},
		{"codex", "codex", "Authorization", "/backend-api/codex/responses", "/backend-api/codex/responses", "/direct/responses", usage},
		{"opencode-go", "opencode-go", "Authorization", "/opencode-go/responses", "/opencode-go/responses", "/direct/openai/v1/responses", usage},
		{"opencode-go Gemini", "opencode-go", "X-Goog-Api-Key", "/opencode-go/models/fixture:generateContent", "/opencode-go/models/fixture:generateContent", "/direct/google/v1beta/models/fixture:generateContent", usage},
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
			guard(host, providerHandler{provider, remoteProxy(provider, target, key, transport)}).ServeHTTP(remote, request())
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
			guard(host, providerHandler{provider, forward}).ServeHTTP(local, request())
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
	const key = "llmp_fixture"
	const authorization = "Bearer provider-fixture"
	requests := []struct {
		provider, method, localPath, remotePath, directPath string
	}{
		{"antigravity", http.MethodPost, "/v1internal:loadCodeAssist", "/antigravity/v1internal:loadCodeAssist", "/v1internal:loadCodeAssist"},
		{"openai", http.MethodGet, "/v1/models", "/v1/models", "/v1/models"},
		{"codex", http.MethodGet, "/backend-api/codex/models", "/backend-api/codex/models", "/backend-api/codex/models"},
		{"opencode-go", http.MethodGet, "/opencode-go/models", "/opencode-go/models", "/inference/go/v1/models"},
	}
	for _, mode := range []string{remoteProxyMode, usageOnlyMode} {
		t.Run(mode, func(t *testing.T) {
			config, err := configureRelay(options{mode: mode, target: "https://proxy.test/"})
			if err != nil || len(config.routes) != len(requests) {
				t.Fatalf("expected all provider routes, got %+v (%v)", config.routes, err)
			}
			check := func(r *http.Request) string {
				t.Helper()
				for _, request := range requests {
					path, wantKey := request.remotePath, key
					if mode == usageOnlyMode {
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
			if mode == usageOnlyMode {
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
			handler := config.handler(host, key, func(usage proxy.Usage) { observed = append(observed, usage) }, transport)
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
			if mode == usageOnlyMode {
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
			for _, path := range []string{"/api/usage/events", "/v1/../opencode-go/models", "/v1suffix"} {
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
