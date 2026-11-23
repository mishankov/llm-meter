// Package relay runs the local loopback LLM Meter relay in remote-proxy or usage-only mode.
package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mishankov/llm-meter/internal/proxy"
	"github.com/mishankov/llm-meter/internal/relayoutbox"
	"github.com/mishankov/llm-meter/internal/usagereport"
)

const (
	StateStopped  = "stopped"
	StateRunning  = "running"
	StateDraining = "draining"
	StateError    = "error"
)

// LogEntry is a sanitized diagnostic event emitted by a relay instance.
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}

// Status is a thread-safe snapshot of a relay instance's state.
type Status struct {
	State         string              `json:"state"`
	ListenAddr    string              `json:"listenAddr"`
	Mode          string              `json:"mode"`
	Target        string              `json:"target"`
	Routes        []string            `json:"routes"`
	RequestsTotal int64               `json:"requestsTotal"`
	LastError     string              `json:"lastError,omitempty"`
	Outbox        *relayoutbox.Status `json:"outbox,omitempty"`
}

// Option customizes a relay Instance.
type Option func(*Instance)

// WithStatusObserver registers a callback invoked when relay or outbox status changes.
func WithStatusObserver(fn func(Status)) Option {
	return func(i *Instance) {
		i.onStatus = fn
	}
}

// WithLogObserver registers a callback invoked for sanitized operational logs.
func WithLogObserver(fn func(LogEntry)) Option {
	return func(i *Instance) {
		i.onLog = fn
	}
}

// WithTransport overrides the HTTP RoundTripper used in remote-proxy mode (primarily for tests).
func WithTransport(transport http.RoundTripper) Option {
	return func(i *Instance) {
		i.transport = transport
	}
}

// Instance manages one running or stopped local relay.
type Instance struct {
	cfg       Config
	bindAddr  string
	config    relayConfig
	routes    []string
	onStatus  func(Status)
	onLog     func(LogEntry)
	transport http.RoundTripper

	requestsTotal atomic.Int64

	mu           sync.Mutex
	started      bool
	state        string
	listenAddr   string
	stopHTTP     context.CancelFunc
	drainCtx     context.Context
	drainDropped int64
	done         chan struct{}
	runErr       error
	queue        *relayoutbox.Queue
}

// New validates cfg and creates a ready-to-start relay Instance.
func New(cfg Config, opts ...Option) (*Instance, error) {
	bind, err := listenAddress(cfg.Listen, cfg.Bind)
	if err != nil {
		return nil, err
	}
	rc, err := configureRelay(cfg)
	if err != nil {
		return nil, err
	}
	if err := ValidateKey(cfg.Key); err != nil {
		return nil, err
	}
	cfg.Key = strings.TrimSpace(cfg.Key)

	routes := make([]string, 0, len(rc.routes))
	for _, r := range rc.routes {
		routes = append(routes, r.provider.name)
	}

	inst := &Instance{
		cfg:      cfg,
		bindAddr: bind,
		config:   rc,
		routes:   routes,
		state:    StateStopped,
		done:     make(chan struct{}),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(inst)
		}
	}
	return inst, nil
}

// Start binds the listener and starts a single relay lifetime.
func (i *Instance) Start() error {
	i.mu.Lock()
	if i.started {
		i.mu.Unlock()
		return errors.New("relay instance already started")
	}
	i.started = true
	listener, err := net.Listen("tcp", i.bindAddr)
	if err != nil {
		i.mu.Unlock()
		return i.finish(fmt.Errorf("listen: %w", err))
	}
	host := listener.Addr().String()
	if i.cfg.Bind != "" {
		host = i.cfg.Listen
	}

	var record func(proxy.Usage)
	if i.config.usageTarget != nil {
		i.queue, err = relayoutbox.Open(i.cfg.DataDir, i.config.usageTarget, i.cfg.Key)
		if err != nil {
			_ = listener.Close()
			i.mu.Unlock()
			return i.finish(fmt.Errorf("open usage queue: %w", err))
		}
		if i.onStatus != nil {
			i.queue.SetObserver(func(relayoutbox.Status) { i.emitStatus() })
		}
		if i.onLog != nil {
			i.queue.SetWarningLogger(func(message string) { i.log("warn", message) })
		}
		record = i.usageRecorder(i.queue)
	}
	handler := i.config.handler(host, i.cfg.Key, record, i.transport, func() {
		i.requestsTotal.Add(1)
		i.emitStatus()
	})
	httpCtx, cancel := context.WithCancel(context.Background())
	i.stopHTTP = cancel
	i.listenAddr = host
	i.state = StateRunning
	i.mu.Unlock()

	go i.run(httpCtx, listener, handler)
	i.log("info", fmt.Sprintf("Listening on http://%s (%s)", host, i.cfg.Mode))
	i.emitStatus()
	return nil
}

// The serving goroutine owns finalization on both requested and unexpected exits.
func (i *Instance) run(ctx context.Context, listener net.Listener, handler http.Handler) {
	workerCtx, stopWorker := context.WithCancel(context.Background())
	var worker sync.WaitGroup
	if i.queue != nil {
		worker.Go(func() { i.queue.Run(workerCtx) })
	}
	err := serve(ctx, listener, handler)
	i.mu.Lock()
	drainCtx, drainDropped := i.drainCtx, i.drainDropped
	i.mu.Unlock()
	if err == nil && drainCtx != nil && i.queue != nil {
		err = i.queue.WaitEmpty(drainCtx)
	}
	stopWorker()
	worker.Wait()
	if drainCtx != nil {
		err = i.queue.DrainResult(drainDropped, err)
	}
	if i.queue != nil {
		err = errors.Join(err, i.queue.Close())
	}
	i.finish(err)
}

func (i *Instance) finish(err error) error {
	i.mu.Lock()
	i.state = StateStopped
	i.runErr = err
	if err != nil {
		i.state = StateError
	}
	i.stopHTTP = nil
	close(i.done)
	i.mu.Unlock()
	if err != nil {
		i.log("error", err.Error())
	} else {
		i.log("info", "Relay stopped")
	}
	i.emitStatus()
	return err
}

// Stop waits for HTTP and WebSocket metering, then stops the uploader.
func (i *Instance) Stop(ctx context.Context) error {
	return i.shutdown(ctx, false)
}

// DrainAndStop additionally waits for the quiesced queue to finish uploading.
func (i *Instance) DrainAndStop(ctx context.Context) error {
	return i.shutdown(ctx, true)
}

func (i *Instance) shutdown(ctx context.Context, drain bool) error {
	i.mu.Lock()
	if !i.started {
		i.mu.Unlock()
		return nil
	}
	if i.stopHTTP != nil {
		if drain && i.queue != nil {
			i.state = StateDraining
			i.drainCtx = ctx
			i.drainDropped = i.queue.Status().Dropped
		}
		i.stopHTTP()
		i.stopHTTP = nil
	}
	queue, drainCtx, dropped := i.queue, i.drainCtx, i.drainDropped
	i.mu.Unlock()
	i.emitStatus()

	select {
	case <-i.done:
		return i.Err()
	case <-ctx.Done():
		if drainCtx != nil {
			return queue.DrainResult(dropped, ctx.Err())
		}
		return ctx.Err()
	}
}

// DrainPending uploads an offline usage-only queue before switching its configuration.
// No listener is opened, so new client traffic cannot refill the queue.
func DrainPending(ctx context.Context, cfg Config) error {
	inst, err := New(cfg)
	if err != nil {
		return err
	}
	if inst.config.usageTarget == nil {
		return errors.New("draining a usage queue requires usage-only mode")
	}
	queue, err := relayoutbox.Open(inst.cfg.DataDir, inst.config.usageTarget, inst.cfg.Key)
	if err != nil {
		return err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	var worker sync.WaitGroup
	worker.Go(func() { queue.Run(workerCtx) })
	err = queue.WaitEmpty(ctx)
	cancel()
	worker.Wait()
	err = queue.DrainResult(0, err)
	if closeErr := queue.Close(); closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return err
}

// Done closes when the relay has completely stopped.
func (i *Instance) Done() <-chan struct{} { return i.done }

// Err returns the fatal error that caused an unexpected exit, if any.
func (i *Instance) Err() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.runErr
}

// Status returns a point-in-time snapshot of the relay instance.
func (i *Instance) Status() Status {
	i.mu.Lock()
	defer i.mu.Unlock()
	var outbox *relayoutbox.Status
	if i.queue != nil {
		status := i.queue.Status()
		outbox = &status
	}
	var lastError string
	if i.runErr != nil {
		lastError = i.runErr.Error()
	}
	routes := append([]string(nil), i.routes...)
	return Status{
		State:         i.state,
		ListenAddr:    i.listenAddr,
		Mode:          i.cfg.Mode,
		Target:        i.cfg.Target,
		Routes:        routes,
		RequestsTotal: i.requestsTotal.Load(),
		LastError:     lastError,
		Outbox:        outbox,
	}
}

// RetryUploads triggers an immediate outbox upload attempt if running in usage-only mode.
func (i *Instance) RetryUploads() {
	i.mu.Lock()
	queue := i.queue
	i.mu.Unlock()
	if queue != nil {
		queue.RetryNow()
	}
}

func (i *Instance) emitStatus() {
	if i.onStatus == nil {
		return
	}
	i.onStatus(i.Status())
}

func (i *Instance) log(level, message string) {
	if i.onLog == nil {
		return
	}
	i.onLog(LogEntry{
		Timestamp: time.Now().UTC(),
		Level:     level,
		Message:   message,
	})
}

func (i *Instance) usageRecorder(queue *relayoutbox.Queue) func(proxy.Usage) {
	return func(usage proxy.Usage) {
		if usage.Status == 0 && len(usage.RateLimits) == 0 {
			return
		}
		event, err := usagereport.NewEvent(usage)
		if err != nil {
			i.log("warn", "Could not prepare a usage report")
			return
		}
		payload, err := json.Marshal(event)
		if err != nil {
			i.log("warn", "Could not encode a usage report")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := queue.Enqueue(ctx, payload); err != nil {
			i.log("warn", "Could not queue a usage report")
		}
	}
}

func serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	// Shutdown does not wait for hijacked handlers. The barrier also keeps the
	// outbox alive while forcibly closed HTTP requests finish recording usage.
	var handlers sync.RWMutex
	closed := false
	socketCtx, stopSockets := context.WithCancel(ctx)
	defer stopSockets()
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handlers.RLock()
			defer handlers.RUnlock()
			if closed {
				return
			}
			if websocket.IsWebSocketUpgrade(r) {
				socketContext, cancel := context.WithCancel(r.Context())
				defer cancel()
				stop := context.AfterFunc(socketCtx, cancel)
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
	var err error
	select {
	case err = <-stopped:
		stopSockets()
		_ = server.Close()
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = server.Shutdown(shutdown)
		if err != nil {
			_ = server.Close()
		}
		<-stopped
	}
	handlers.Lock()
	closed = true
	handlers.Unlock()
	return err
}
