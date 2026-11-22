package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"

	"github.com/mishankov/llm-proxy/internal/proxy"
)

const keyHeader = "X-LLM-Proxy-Key"

const (
	apiPrefix   = "/v1"
	codexPrefix = "/backend-api/codex"
)

type connectionProvider struct {
	prefix, defaultUpstream string
}

var providers = map[string]connectionProvider{
	"antigravity": {proxy.AntigravityPrefix, "https://daily-cloudcode-pa.googleapis.com"},
	"openai":      {apiPrefix, "https://api.openai.com/v1"},
	"codex":       {codexPrefix, "https://chatgpt.com/backend-api/codex"},
	"opencode-go": {proxy.OpenCodeGoPrefix, "https://opencode.ai/inference/go"},
}

var cloudCodeRPC = regexp.MustCompile(`^/v1internal:[a-zA-Z][a-zA-Z0-9]*$`)

func guard(host string, provider connectionProvider, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Host != host && r.Host+":80" != host) ||
			len(r.Header.Values("Origin")) != 0 || len(r.Header.Values("Sec-Fetch-Site")) != 0 {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if !provider.allowsPath(r.URL.Path) || r.Method == http.MethodConnect {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") == "" {
			if provider.prefix == proxy.AntigravityPrefix {
				http.Error(w, "Google authorization is required", http.StatusUnauthorized)
				return
			}
			if r.Header.Get("X-API-Key") == "" && r.Header.Get("Api-Key") == "" && r.Header.Get("X-Goog-Api-Key") == "" {
				http.Error(w, "Provider authorization is required", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (provider connectionProvider) allowsPath(path string) bool {
	if provider.prefix == proxy.AntigravityPrefix {
		return cloudCodeRPC.MatchString(path)
	}
	if path != provider.prefix && !strings.HasPrefix(path, provider.prefix+"/") {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." || strings.ContainsRune(segment, '\\') {
			return false
		}
	}
	return true
}

func remoteProxy(provider connectionProvider, target *url.URL, key string, transport http.RoundTripper) http.Handler {
	forward := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			// The relay leaves query interpretation to the remote proxy.
			r.Out.URL.RawQuery = r.In.URL.RawQuery
			r.Out.Header.Set(keyHeader, key)
		},
		ErrorLog: log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "LLM Proxy unavailable", http.StatusBadGateway)
		},
	}
	if provider.prefix == proxy.AntigravityPrefix {
		return forward
	}
	return http.StripPrefix(provider.prefix, forward)
}

func directProxy(provider connectionProvider, upstream url.URL, record func(proxy.Usage)) http.Handler {
	switch provider.prefix {
	case proxy.AntigravityPrefix:
		forward := proxy.NewAntigravity(upstream, record)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			request := r.Clone(r.Context())
			request.URL.Path = provider.prefix + request.URL.Path
			if request.URL.RawPath != "" {
				request.URL.RawPath = provider.prefix + request.URL.RawPath
			}
			forward.ServeHTTP(w, request)
		})
	case codexPrefix:
		return proxy.New(nil, &upstream, nil, record)
	case proxy.OpenCodeGoPrefix:
		return proxy.New(nil, nil, &upstream, record)
	default:
		return proxy.New(&upstream, nil, nil, record)
	}
}
