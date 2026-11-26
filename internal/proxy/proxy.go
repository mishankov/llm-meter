// Package proxy forwards model provider requests and observes usage reported upstream.
package proxy

import (
	"context"
	"io"
	"log"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	apiPrefix         = "/v1"
	accessKey         = "X-LLM-Meter-Key"
	AntigravityPrefix = "/antigravity"
)

// Usage is the metadata available for an upstream request or WebSocket response.
// Quota-only updates use status zero and carry one or more rate limits.
type Usage struct {
	At                    time.Time
	Source                string
	Provider              string
	ClientID              string
	Model                 string
	ReasoningLevel        string
	Endpoint              string
	Method                string
	ResponseID            string
	ServiceTier           string
	CompletionWindow      string
	CacheRetention        string
	Status                int
	InputTokens           int64
	CachedInputTokens     int64
	CacheWriteTokens      int64
	CacheWrite12hTokens   int64
	HasCacheWriteSplit    bool
	UsageUpdate           bool
	OutputTokens          int64
	HasUsage              bool
	ReportedCost          *float64
	ReportedListCost      *float64
	ReportedAllowance     *float64
	ReportedCostEstimated bool
	RateLimits            []RateLimit
}

// RateLimit is a provider-reported account allowance window.
type RateLimit struct {
	ID            string
	Window        string
	WindowMinutes int
	UsedPercent   float64
	ResetsAt      time.Time
}

type clientContextKey struct{}
type meterContextKey struct{}

// WithClient associates an authenticated proxy client with the next request.
func WithClient(ctx context.Context, clientID string) context.Context {
	return context.WithValue(ctx, clientContextKey{}, clientID)
}

// New creates an HTTP and WebSocket proxy for the configured API upstream.
// The caller must authenticate access before passing requests to this handler.
func New(apiUpstream *url.URL, record func(Usage)) http.Handler {
	if record == nil {
		record = func(Usage) {}
	}
	if apiUpstream == nil {
		apiUpstream = &url.URL{Scheme: "https", Host: "api.openai.com", Path: apiPrefix}
	}
	api := newUpstream(apiPrefix, *apiUpstream, record)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hasPrefix(r.URL.Path, apiPrefix) {
			http.NotFound(w, r)
			return
		}
		api.serve(w, r)
	})
}

// NewAntigravity creates a Cloud Code Assist reverse proxy for authenticated clients.
func NewAntigravity(baseURL url.URL, record func(Usage)) http.Handler {
	if record == nil {
		record = func(Usage) {}
	}
	route := newUpstream(AntigravityPrefix, baseURL, func(usage Usage) {
		if usage.ResponseID != "" {
			usage.ResponseID = "antigravity:" + usage.ResponseID
		}
		record(usage)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hasPrefix(r.URL.Path, AntigravityPrefix) {
			http.NotFound(w, r)
			return
		}
		route.serve(w, r)
	})
}

type upstream struct {
	prefix   string
	provider string
	baseURL  url.URL
	record   func(Usage)
	http     *httputil.ReverseProxy
}

func newUpstream(prefix string, baseURL url.URL, record func(Usage)) *upstream {
	route := &upstream{
		prefix: prefix, provider: upstreamProvider(prefix, baseURL), baseURL: baseURL, record: record,
	}
	route.http = &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			target := route.targetURL(request.Out.URL)
			request.Out.URL = &target
			request.Out.Host = ""
			removeLocalHeaders(request.Out.Header)
			// Let the standard transport negotiate and decode gzip so usage
			// can be observed in JSON and in streaming responses.
			if request.Out.Header.Get("Range") == "" {
				request.Out.Header.Del("Accept-Encoding")
			}
		},
		ModifyResponse: route.observeResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			r.Context().Value(meterContextKey{}).(*httpMeter).usage.Status = http.StatusBadGateway
			http.Error(w, "Upstream unavailable", http.StatusBadGateway)
		},
		// ReverseProxy can include request details in its logs.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	return route
}

func (u *upstream) serve(w http.ResponseWriter, r *http.Request) {
	suffix := strings.TrimPrefix(r.URL.Path, u.prefix)
	for _, segment := range strings.Split(suffix, "/") {
		if segment == "." || segment == ".." {
			http.Error(w, "Invalid upstream path", http.StatusBadRequest)
			return
		}
	}
	clientID, _ := r.Context().Value(clientContextKey{}).(string)
	usage := Usage{
		At:       time.Now().UTC(),
		Source:   "api",
		Provider: u.provider,
		ClientID: clientID,
		Endpoint: r.URL.Path,
		Method:   r.Method,
	}
	if u.prefix != AntigravityPrefix && websocket.IsWebSocketUpgrade(r) {
		u.serveWebSocket(w, r, usage)
		return
	}

	captureUsage := r.Method == http.MethodPost
	if u.prefix == AntigravityPrefix {
		captureUsage = captureUsage && (suffix == "/v1internal:generateContent" || suffix == "/v1internal:streamGenerateContent")
	} else {
		responsePath, isResponse := strings.CutPrefix(suffix, "/responses/")
		usage.UsageUpdate = r.Method == http.MethodGet && isResponse && responsePath != "" && !strings.Contains(responsePath, "/")
		captureUsage = captureUsage || usage.UsageUpdate
	}
	meter := &httpMeter{usage: usage, captureUsage: captureUsage}
	request := r.WithContext(context.WithValue(r.Context(), meterContextKey{}, meter))
	if request.Body != nil && request.ContentLength != 0 && isJSON(request.Header.Get("Content-Type")) &&
		request.Header.Get("Content-Encoding") == "" {
		meter.request = &metadataCapture{}
		request.Body = &observedBody{ReadCloser: request.Body, observer: meter.request}
	}
	defer func() {
		meter.finish()
		u.record(meter.usage)
	}()
	u.http.ServeHTTP(w, request)
}

func upstreamProvider(prefix string, baseURL url.URL) string {
	if prefix == AntigravityPrefix {
		return "google"
	}
	switch strings.ToLower(baseURL.Hostname()) {
	case "api.openai.com":
		return "openai"
	case "openrouter.ai":
		return "openrouter"
	case "opencode.ai":
		if hasPrefix(baseURL.Path, "/inference/go") {
			return "opencode-go"
		}
		if hasPrefix(baseURL.Path, "/inference") {
			return "opencode"
		}
	case "api.anthropic.com":
		return "anthropic"
	case "api.deepseek.com":
		return "deepseek"
	case "api.groq.com":
		return "groq"
	case "api.mistral.ai":
		return "mistral"
	case "api.x.ai":
		return "xai"
	case "generativelanguage.googleapis.com":
		return "google"
	}
	return ""
}

func (u *upstream) observeResponse(response *http.Response) error {
	meter := response.Request.Context().Value(meterContextKey{}).(*httpMeter)
	meter.usage.Status = response.StatusCode
	meter.usage.Model = response.Header.Get("OpenAI-Model")
	if meter.usage.Model == "" {
		meter.usage.Model = response.Header.Get("X-OpenAI-Model")
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	var observer io.Writer
	switch {
	case mediaType == "text/event-stream":
		meter.sse = &eventStream{handle: meter.observeEvent}
		observer = meter.sse
	case isJSON(mediaType):
		meter.response = &metadataCapture{}
		observer = meter.response
	}
	if meter.sse == nil && response.StatusCode == http.StatusOK &&
		meter.usage.Method == http.MethodPost && meter.usage.Endpoint == "/v1/responses" {
		encoding := response.Header.Get("Content-Encoding")
		if encoding == "" || strings.EqualFold(encoding, "identity") {
			observer = meter.probeResponseStream()
		}
	}
	if observer != nil {
		response.Body = &observedBody{ReadCloser: response.Body, observer: observer}
	}
	return nil
}

func (u *upstream) targetURL(incoming *url.URL) url.URL {
	target := u.baseURL
	suffix := strings.TrimPrefix(incoming.Path, u.prefix)
	target.Path = strings.TrimSuffix(target.Path, "/") + suffix
	if incoming.RawPath != "" || u.baseURL.RawPath != "" {
		// Each character in the route prefix is ASCII and may be percent encoded.
		escaped := incoming.EscapedPath()
		index := 0
		for range len(u.prefix) {
			if escaped[index] == '%' {
				index += 3
			} else {
				index++
			}
		}
		target.RawPath = strings.TrimSuffix(u.baseURL.EscapedPath(), "/") + escaped[index:]
	}
	if target.RawQuery != "" && incoming.RawQuery != "" {
		target.RawQuery += "&"
	}
	target.RawQuery += incoming.RawQuery
	target.ForceQuery = target.ForceQuery || incoming.ForceQuery
	return target
}

// IsAntigravityEndpoint reports whether a recorded endpoint uses Cloud Code Assist.
func IsAntigravityEndpoint(path string) bool {
	return hasPrefix(path, AntigravityPrefix)
}

func hasPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func isJSON(contentType string) bool {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func removeLocalHeaders(header http.Header) {
	header.Del(accessKey)
	for _, cookie := range (&http.Request{Header: header}).Cookies() {
		if cookie.Name != "llm_meter_session" {
			continue
		}
		cookies := (&http.Request{Header: header}).Cookies()
		header.Del("Cookie")
		for _, upstreamCookie := range cookies {
			if upstreamCookie.Name != cookie.Name {
				header.Add("Cookie", (&http.Cookie{Name: upstreamCookie.Name, Value: upstreamCookie.Value}).String())
			}
		}
		break
	}
}

type observedBody struct {
	io.ReadCloser
	observer io.Writer
}

func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		_, _ = b.observer.Write(p[:n])
	}
	return n, err
}
