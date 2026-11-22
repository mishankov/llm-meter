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
	flag.StringVar(&opts.provider, "provider", "antigravity", "antigravity, openai, codex, or opencode-go")
	flag.StringVar(&opts.target, "target", "", "HTTPS LLM Proxy route in remote-proxy mode or origin in usage-only mode")
	flag.StringVar(&opts.upstream, "upstream", "", "optional direct HTTPS provider base URL in usage-only mode")
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
	provider, ok := providers[opts.provider]
	if !ok {
		return errors.New("unknown --provider")
	}
	target, upstream, err := connectionURLs(opts, provider)
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
	var handler http.Handler
	if opts.mode == usageOnlyMode {
		queue, err := relayoutbox.Open(opts.dataDir, target, key)
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
		handler = directProxy(provider, *upstream, usageRecorder(queue))
	} else {
		handler = remoteProxy(provider, target, key, nil)
	}

	log.Printf("Listening on http://%s (%s)", host, opts.mode)
	return serve(ctx, listener, guard(host, provider, handler))
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

func connectionURLs(opts options, provider connectionProvider) (target, upstream *url.URL, err error) {
	switch opts.mode {
	case remoteProxyMode:
		if opts.upstream != "" {
			return nil, nil, errors.New("--upstream requires --mode usage-only")
		}
		target, err := parseTarget(opts.target, provider.prefix)
		return target, nil, err
	case usageOnlyMode:
		target, err := parseTarget(opts.target, "")
		if err != nil {
			return nil, nil, err
		}
		target.Path = usagereport.Path
		rawUpstream := opts.upstream
		if rawUpstream == "" {
			rawUpstream = provider.defaultUpstream
		}
		upstream, err := parseHTTPS(rawUpstream)
		if err != nil {
			return nil, nil, errors.New("--upstream must be an HTTPS provider base URL without user info, query, or fragment")
		}
		if strings.EqualFold(strings.TrimSuffix(target.Hostname(), "."), strings.TrimSuffix(upstream.Hostname(), ".")) {
			return nil, nil, errors.New("the provider and LLM Proxy must use different hosts in usage-only mode")
		}
		return target, upstream, nil
	default:
		return nil, nil, errors.New("--mode must be remote-proxy or usage-only")
	}
}

func parseTarget(raw, route string) (*url.URL, error) {
	target, err := parseHTTPS(raw)
	if err == nil && strings.TrimSuffix(target.EscapedPath(), "/") == route {
		return target, nil
	}
	if route == "" {
		return nil, errors.New("--target must be an HTTPS LLM Proxy origin without credentials, path, query, or fragment")
	}
	return nil, fmt.Errorf("--target must be an HTTPS LLM Proxy %s URL without user info, query, or fragment", route)
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
