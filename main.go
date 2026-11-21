package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mishankov/llm-proxy/internal/dashboard"
	"github.com/mishankov/llm-proxy/internal/ledger"
	"github.com/mishankov/llm-proxy/internal/pricing"
	"github.com/mishankov/llm-proxy/internal/proxy"
)

func main() {
	if err := run(); err != nil {
		slog.Error("LLM Proxy stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	dashboardToken := os.Getenv("DASHBOARD_TOKEN")
	if len(dashboardToken) < 24 || len(dashboardToken) > 512 {
		return errors.New("DASHBOARD_TOKEN must contain 24 to 512 characters")
	}
	secureCookie, err := strconv.ParseBool(valueOrDefault("COOKIE_SECURE", "true"))
	if err != nil {
		return errors.New("COOKIE_SECURE must be true or false")
	}
	api, err := upstream("OPENAI_UPSTREAM", "https://api.openai.com/v1")
	if err != nil {
		return err
	}
	codex, err := upstream("CODEX_UPSTREAM", "https://chatgpt.com/backend-api/codex")
	if err != nil {
		return err
	}
	opencodeGo, err := upstream("OPENCODE_GO_UPSTREAM", "https://opencode.ai/inference/go")
	if err != nil {
		return err
	}
	ui, err := dashboardAssets()
	if err != nil {
		return err
	}
	catalog, err := pricing.Load(os.Getenv("PRICING_FILE"))
	if err != nil {
		return err
	}
	dataDir := valueOrDefault("DATA_DIR", "./data")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	store, err := ledger.Open(filepath.Join(dataDir, "llm-proxy.db"), catalog)
	if err != nil {
		return err
	}
	defer store.Close()

	server := &http.Server{
		Addr:              valueOrDefault("LISTEN_ADDR", ":8080"),
		Handler:           routes(store, ui, api, codex, opencodeGo, dashboardToken, secureCookie, slog.Default()),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	pricingDone := make(chan struct{})
	go func() {
		defer close(pricingDone)
		store.RefreshPrices(ctx, slog.Default())
	}()
	defer func() {
		stop()
		<-pricingDone
	}()
	stopped := make(chan error, 1)
	go func() { stopped <- server.ListenAndServe() }()
	slog.Info("LLM Proxy serving", "address", server.Addr)
	select {
	case err := <-stopped:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

func routes(store *ledger.Store, ui fs.FS, api, codex, opencodeGo *url.URL, dashboardToken string, secureCookie bool, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", dashboard.New(store, dashboardToken, secureCookie, log))
	forward := proxy.New(api, codex, opencodeGo, func(usage proxy.Usage) {
		// Persist completed requests even if the downstream client has gone away.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := store.Record(ctx, usage); err != nil {
			log.Error("record request usage", "error", err)
		}
	})
	proxyAccess := withProxyKey(store, forward, log)
	for _, prefix := range []string{"/v1", "/backend-api/codex", proxy.OpenCodeGoPrefix} {
		mux.Handle(prefix, proxyAccess)
		mux.Handle(prefix+"/", proxyAccess)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/", serveDashboard(ui))
	return mux
}

func withProxyKey(store *ledger.Store, next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys := r.Header.Values("X-LLM-Proxy-Key")
		if len(keys) != 1 {
			proxyError(w, http.StatusUnauthorized, "A valid X-LLM-Proxy-Key is required")
			return
		}
		clientID, err := store.AuthenticateKey(r.Context(), keys[0], time.Now().UTC())
		if ledger.NotFound(err) {
			proxyError(w, http.StatusUnauthorized, "A valid X-LLM-Proxy-Key is required")
			return
		}
		if err != nil {
			log.Error("authenticate proxy client", "error", err)
			proxyError(w, http.StatusServiceUnavailable, "Proxy authentication unavailable")
			return
		}
		r.Header.Del("X-LLM-Proxy-Key")
		next.ServeHTTP(w, r.WithContext(proxy.WithClient(r.Context(), clientID)))
	})
}

func proxyError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": message, "type": "proxy_error"}})
}

func serveDashboard(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		if r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		file, err := fs.Stat(ui, name)
		if err != nil || file.IsDir() {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(name, "_app/immutable/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			if serveCompressedDashboardAsset(w, r, ui, name, file.ModTime()) {
				return
			}
		}
		files.ServeHTTP(w, r)
	})
}

func serveCompressedDashboardAsset(w http.ResponseWriter, r *http.Request, ui fs.FS, name string, modified time.Time) bool {
	var contentType string
	switch path.Ext(name) {
	case ".css":
		contentType = "text/css; charset=utf-8"
	case ".js":
		contentType = "text/javascript; charset=utf-8"
	default:
		return false
	}
	w.Header().Add("Vary", "Accept-Encoding")
	if !acceptsDashboardGzip(r.Header.Values("Accept-Encoding")) {
		return false
	}
	file, err := ui.Open(name + ".gz")
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	body, seekable := file.(io.ReadSeeker)
	if err != nil || info.IsDir() || !seekable {
		return false
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	if strings.Contains(r.Header.Get("Range"), ",") {
		// A multipart response is not itself gzip-encoded.
		r = r.Clone(r.Context())
		r.Header.Del("Range")
	}
	http.ServeContent(w, r, name, modified, body)
	return true
}

func acceptsDashboardGzip(values []string) bool {
	gzip, wildcard, identity := -1.0, 0.0, 0.0
	for _, value := range values {
		for coding := range strings.SplitSeq(value, ",") {
			name, parameters, exists := strings.Cut(coding, ";")
			quality := 1.0
			if exists {
				key, weight, ok := strings.Cut(strings.TrimSpace(parameters), "=")
				quality = 0
				if parsed, err := strconv.ParseFloat(strings.TrimSpace(weight), 64); ok && strings.EqualFold(strings.TrimSpace(key), "q") && err == nil && parsed > 0 && parsed <= 1 {
					quality = parsed
				}
			}
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "gzip":
				gzip = quality
			case "*":
				wildcard = quality
			case "identity":
				identity = quality
			}
		}
	}
	if gzip < 0 {
		gzip = wildcard
	}
	return gzip > 0 && gzip >= identity
}

func upstream(name, fallback string) (*url.URL, error) {
	base, err := url.Parse(valueOrDefault(name, fallback))
	if err != nil || base.Host == "" || base.Scheme != "http" && base.Scheme != "https" ||
		base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("%s must be an HTTP or HTTPS base URL without credentials, query, or fragment", name)
	}
	return base, nil
}

func valueOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
