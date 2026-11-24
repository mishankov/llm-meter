package relay

import (
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"

	"github.com/mishankov/llm-meter/internal/proxy"
)

const keyHeader = "X-LLM-Meter-Key"

const (
	apiPrefix = "/v1"
)

// ProviderSpec describes a provider route served by the local relay.
type ProviderSpec struct {
	Name            string `json:"name"`
	Prefix          string `json:"prefix"`
	DefaultUpstream string `json:"defaultUpstream"`
}

type connectionProvider struct {
	name, prefix, defaultUpstream string
}

var providerOrder = []string{"antigravity", "openai", "codex", "opencode-go"}

var providers = map[string]connectionProvider{
	"antigravity": {"antigravity", proxy.AntigravityPrefix, "https://daily-cloudcode-pa.googleapis.com"},
	"openai":      {"openai", apiPrefix, "https://api.openai.com/v1"},
	"codex":       {"codex", proxy.CodexPrefix, "https://chatgpt.com/backend-api/codex"},
	"opencode-go": {"opencode-go", proxy.OpenCodeGoPrefix, "https://opencode.ai/inference/go"},
}

// Providers returns all supported relay providers in deterministic order.
func Providers() []ProviderSpec {
	out := make([]ProviderSpec, 0, len(providerOrder))
	for _, name := range providerOrder {
		p := providers[name]
		out = append(out, ProviderSpec{
			Name:            p.name,
			Prefix:          p.prefix,
			DefaultUpstream: p.defaultUpstream,
		})
	}
	return out
}

var cloudCodeRPC = regexp.MustCompile(`^/v1internal:[a-zA-Z][a-zA-Z0-9]*$`)

type relayRoute struct {
	provider connectionProvider
	target   url.URL
}

type relayConfig struct {
	routes      []relayRoute
	usageTarget *url.URL
}

type providerHandler struct {
	provider connectionProvider
	forward  http.Handler
}

func (config relayConfig) handler(host, key string, record func(proxy.Usage), transport http.RoundTripper, onRequest func()) http.Handler {
	handlers := make([]providerHandler, 0, len(config.routes))
	for _, route := range config.routes {
		var forward http.Handler
		if config.usageTarget != nil {
			forward = directProxy(route.provider, route.target, record)
		} else {
			forward = remoteProxy(route.provider, &route.target, key, transport)
		}
		handlers = append(handlers, providerHandler{route.provider, forward})
	}
	return guard(host, onRequest, handlers...)
}

func guard(host string, onRequest func(), handlers ...providerHandler) http.Handler {
	return guardClient(host, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			http.NotFound(w, r)
			return
		}
		for _, handler := range handlers {
			if !handler.provider.allowsPath(r.URL.Path) {
				continue
			}
			if r.Header.Get("Authorization") == "" {
				if handler.provider.prefix == proxy.AntigravityPrefix {
					http.Error(w, "Google authorization is required", http.StatusUnauthorized)
					return
				}
				if r.Header.Get("X-API-Key") == "" && r.Header.Get("Api-Key") == "" && r.Header.Get("X-Goog-Api-Key") == "" {
					http.Error(w, "Provider authorization is required", http.StatusUnauthorized)
					return
				}
			}
			if onRequest != nil {
				onRequest()
			}
			handler.forward.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	}))
}

func guardClient(host string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defaultPort := ":80"
		if r.TLS != nil {
			defaultPort = ":443"
		}
		if (r.Host != host && r.Host+defaultPort != host) ||
			len(r.Header.Values("Origin")) != 0 || len(r.Header.Values("Sec-Fetch-Site")) != 0 {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
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
			http.Error(w, "LLM Meter unavailable", http.StatusBadGateway)
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
	case proxy.CodexPrefix:
		return proxy.New(nil, &upstream, nil, record)
	case proxy.OpenCodeGoPrefix:
		return proxy.New(nil, nil, &upstream, record)
	default:
		return proxy.New(&upstream, nil, nil, record)
	}
}
