package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

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
	target, err := parseTarget(upstream.URL + "/antigravity/")
	if err != nil {
		t.Fatal(err)
	}
	local := httptest.NewUnstartedServer(nil)
	local.Config.Handler = relay(local.Listener.Addr().String(), target, key, upstream.Client().Transport)
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
	target, err := parseTarget("https://proxy.test/antigravity")
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
			handler := relay(test.listener, target, "fixture", transport)
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Errorf("status %d, want %d", response.Code, test.status)
			}
		})
	}
}

func TestRelayHidesUpstreamErrors(t *testing.T) {
	const host = "127.0.0.1:8765"
	target, err := parseTarget("https://proxy.test/antigravity")
	if err != nil {
		t.Fatal(err)
	}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New(r.URL.String() + " " + r.Header.Get(keyHeader) + " " + r.Header.Get("Authorization"))
	})
	request := httptest.NewRequest(http.MethodPost, "http://"+host+"/v1internal:loadCodeAssist", nil)
	request.Header.Set("Authorization", "Bearer fixture")
	response := httptest.NewRecorder()
	relay(host, target, "llmp_fixture", transport).ServeHTTP(response, request)
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
		if _, err := parseTarget(target); err == nil {
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
