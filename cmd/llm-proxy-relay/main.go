package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const keyHeader = "X-LLM-Proxy-Key"

var cloudCodeRPC = regexp.MustCompile(`^/v1internal:[a-zA-Z][a-zA-Z0-9]*$`)

func main() {
	listen := flag.String("listen", "127.0.0.1:8765", "loopback IP and port to listen on")
	target := flag.String("target", "", "HTTPS LLM Proxy /antigravity URL")
	keyFile := flag.String("key-file", "", "path to the private proxy key file")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("unexpected positional arguments")
	}
	if err := run(*listen, *target, *keyFile); err != nil {
		log.Fatal(err)
	}
}

func run(listen, rawTarget, keyFile string) error {
	if !isLoopback(listen) {
		return fmt.Errorf("--listen must specify a loopback IP and port")
	}
	target, err := parseTarget(rawTarget)
	if err != nil {
		return err
	}
	key, err := readKey(keyFile)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	host := listener.Addr().String()
	server := &http.Server{
		Handler:           relay(host, target, key, nil),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       time.Minute,
	}
	log.Printf("Listening on http://%s", host)
	return server.Serve(listener)
}

func isLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

func parseTarget(raw string) (*url.URL, error) {
	target, err := url.Parse(raw)
	if err != nil || target.Scheme != "https" || target.Hostname() == "" || target.User != nil ||
		strings.ContainsAny(raw, "?#") || strings.TrimSuffix(target.EscapedPath(), "/") != "/antigravity" {
		return nil, fmt.Errorf("--target must be an HTTPS /antigravity URL without user info, query, or fragment")
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

func relay(host string, target *url.URL, key string, transport http.RoundTripper) http.Handler {
	proxy := &httputil.ReverseProxy{
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Host != host && r.Host+":80" != host) ||
			len(r.Header.Values("Origin")) != 0 || len(r.Header.Values("Sec-Fetch-Site")) != 0 {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if !cloudCodeRPC.MatchString(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "Google authorization is required", http.StatusUnauthorized)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}
