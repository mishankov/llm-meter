package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mishankov/llm-proxy/internal/proxy"
	"github.com/mishankov/llm-proxy/internal/relayoutbox"
	"github.com/mishankov/llm-proxy/internal/usagereport"
)

const (
	remoteProxyMode = "remote-proxy"
	usageOnlyMode   = "usage-only"
)

type options struct {
	listen, bind, mode, provider, target, upstream, keyFile, dataDir string
}

func main() {
	var opts options
	flag.StringVar(&opts.listen, "listen", "127.0.0.1:8765", "client-facing loopback IP and port")
	flag.StringVar(&opts.bind, "bind", "", "container socket address, e.g. 0.0.0.0:8765 with a loopback-only Docker port mapping")
	flag.StringVar(&opts.mode, "mode", remoteProxyMode, "remote-proxy or usage-only")
	flag.StringVar(&opts.provider, "provider", "", "restrict to antigravity, openai, codex, or opencode-go (default all)")
	flag.StringVar(&opts.target, "target", "", "HTTPS LLM Proxy origin (remote-proxy also accepts a legacy provider route)")
	flag.StringVar(&opts.upstream, "upstream", "", "direct HTTPS provider base URL override in usage-only mode (scopes to Antigravity if --provider is omitted)")
	flag.StringVar(&opts.keyFile, "key-file", "", "path to the private proxy key file")
	flag.StringVar(&opts.dataDir, "data-dir", "./data/relay", "persistent directory for this connection's pending usage in usage-only mode")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("unexpected positional arguments")
	}
	if err := run(opts); err != nil {
		log.Fatal(err)
	}
}

func run(opts options) error {
	bind, err := listenAddress(opts.listen, opts.bind)
	if err != nil {
		return err
	}
	config, err := configureRelay(opts)
	if err != nil {
		return err
	}
	key, err := readKey(opts.keyFile)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", bind)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()
	host := listener.Addr().String()
	if opts.bind != "" {
		host = opts.listen
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var record func(proxy.Usage)
	if config.usageTarget != nil {
		queue, err := relayoutbox.Open(opts.dataDir, config.usageTarget, key)
		if err != nil {
			return fmt.Errorf("open usage queue: %w", err)
		}
		defer queue.Close()
		done := make(chan struct{})
		go func() {
			defer close(done)
			queue.Run(ctx)
		}()
		defer func() {
			stop()
			<-done
		}()
		record = usageRecorder(queue)
	}

	log.Printf("Listening on http://%s (%s)", host, opts.mode)
	return serve(ctx, listener, config.handler(host, key, record, nil))
}

func serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	var sockets sync.WaitGroup
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if websocket.IsWebSocketUpgrade(r) {
				sockets.Add(1)
				defer sockets.Done()
				socketContext, cancel := context.WithCancel(r.Context())
				defer cancel()
				stop := context.AfterFunc(ctx, cancel)
				defer stop()
				r = r.WithContext(socketContext)
			}
			handler.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       time.Minute,
	}
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	select {
	case err := <-stopped:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		// Shutdown waits for HTTP handlers. Hijacked sockets finalize separately.
		done := make(chan struct{})
		go func() {
			sockets.Wait()
			close(done)
		}()
		select {
		case <-done:
			return nil
		case <-shutdown.Done():
			return shutdown.Err()
		}
	}
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
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

func configureRelay(opts options) (relayConfig, error) {
	selected := opts.provider
	if _, ok := providers[selected]; selected != "" && !ok {
		return relayConfig{}, errors.New("unknown --provider")
	}
	origin, err := parseHTTPS(opts.target)
	if err != nil {
		return relayConfig{}, errors.New("--target must be an HTTPS LLM Proxy URL without user info, query, or fragment")
	}
	path := strings.TrimSuffix(origin.EscapedPath(), "/")
	var config relayConfig
	switch opts.mode {
	case remoteProxyMode:
		if opts.upstream != "" {
			return relayConfig{}, errors.New("--upstream requires --mode usage-only")
		}
		if path != "" {
			// A target from the single-provider relay keeps its previous scope.
			if selected == "" {
				for name, provider := range providers {
					if provider.prefix == path {
						selected = name
						break
					}
				}
			}
			if selected == "" || providers[selected].prefix != path {
				return relayConfig{}, errors.New("--target must be an HTTPS LLM Proxy origin or a matching provider route")
			}
		}
	case usageOnlyMode:
		if path != "" {
			return relayConfig{}, errors.New("--target must be an HTTPS LLM Proxy origin in usage-only mode")
		}
		if opts.upstream != "" && selected == "" {
			// Before multi-provider routing, an upstream override defaulted to Antigravity.
			selected = "antigravity"
		}
		usageTarget := *origin
		usageTarget.Path, usageTarget.RawPath = usagereport.Path, ""
		config.usageTarget = &usageTarget
	default:
		return relayConfig{}, errors.New("--mode must be remote-proxy or usage-only")
	}

	for name, provider := range providers {
		if selected != "" && name != selected {
			continue
		}
		if config.usageTarget == nil {
			target := *origin
			target.Path, target.RawPath = provider.prefix, ""
			config.routes = append(config.routes, relayRoute{provider, target})
			continue
		}
		upstream, err := directTarget(origin, provider, opts.upstream)
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
		return nil, errors.New("the provider and LLM Proxy must use different hosts in usage-only mode")
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

func readKey(path string) (string, error) {
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
	const maxKeyBytes = 4096
	data, err := io.ReadAll(io.LimitReader(file, maxKeyBytes+1))
	key := strings.TrimSpace(string(data))
	if err != nil || len(data) > maxKeyBytes || key == "" || strings.IndexFunc(key, func(r rune) bool {
		return r <= ' ' || r >= 0x7f
	}) >= 0 {
		return "", fmt.Errorf("the proxy key file must contain a single key")
	}
	return key, nil
}

func usageRecorder(queue *relayoutbox.Queue) func(proxy.Usage) {
	return func(usage proxy.Usage) {
		if usage.Status == 0 && len(usage.RateLimits) == 0 {
			return
		}
		event, err := usagereport.NewEvent(usage)
		if err != nil {
			log.Print("Could not prepare a usage report")
			return
		}
		payload, err := json.Marshal(event)
		if err != nil {
			log.Print("Could not encode a usage report")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := queue.Enqueue(ctx, payload); err != nil {
			log.Print("Could not queue a usage report")
		}
	}
}
