package proxy

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

func (u *upstream) serveWebSocket(w http.ResponseWriter, r *http.Request, usage Usage) {
	target := u.targetURL(r.URL)
	switch target.Scheme {
	case "https":
		target.Scheme = "wss"
	case "http":
		target.Scheme = "ws"
	}
	headers := websocketHeaders(r.Header)
	removeLocalHeaders(headers)

	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = websocket.Subprotocols(r)
	dialer.EnableCompression = true
	remote, response, err := dialer.DialContext(r.Context(), target.String(), headers)
	if err != nil {
		usage.Status = http.StatusBadGateway
		if response == nil {
			http.Error(w, "Upstream unavailable", usage.Status)
		} else {
			defer response.Body.Close()
			usage.Status = response.StatusCode
			copyHeader(w.Header(), websocketHeaders(response.Header))
			w.WriteHeader(usage.Status)
			_, _ = io.Copy(w, response.Body)
		}
		u.record(usage)
		return
	}
	defer remote.Close()

	upgrader := websocket.Upgrader{EnableCompression: true}
	if protocol := remote.Subprotocol(); protocol != "" {
		upgrader.Subprotocols = []string{protocol}
	}
	client, err := upgrader.Upgrade(w, r, websocketHeaders(response.Header))
	if err != nil {
		return
	}
	defer client.Close()

	usage.Status = http.StatusSwitchingProtocols
	usage.Model = target.Query().Get("model")
	meter := socketMeter{usage: usage, record: u.record}
	done := make(chan struct{}, 2)
	go func() {
		relayMessages(client, remote, meter.request, true)
		done <- struct{}{}
	}()
	go func() {
		relayMessages(remote, client, meter.response, false)
		done <- struct{}{}
	}()
	finished := 0
	select {
	case <-r.Context().Done():
	case <-done:
		finished++
	}
	_ = client.Close()
	_ = remote.Close()
	for ; finished < 2; finished++ {
		<-done
	}
	meter.finish()
}

func websocketHeaders(incoming http.Header) http.Header {
	headers := incoming.Clone()
	for _, value := range headers.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			headers.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{
		"Connection", "Upgrade", "Proxy-Connection", "Proxy-Authenticate", "Proxy-Authorization",
		"Keep-Alive", "Transfer-Encoding", "Content-Length", "TE", "Trailer",
		"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto",
		"Sec-WebSocket-Accept", "Sec-WebSocket-Key", "Sec-WebSocket-Protocol",
		"Sec-WebSocket-Version", "Sec-WebSocket-Extensions",
	} {
		headers.Del(name)
	}
	return headers
}

func copyHeader(destination, source http.Header) {
	for name, values := range source {
		destination[name] = values
	}
}

func relayMessages(from, to *websocket.Conn, observe func(wireMessage), observeBeforeClose bool) {
	for {
		messageType, input, err := from.NextReader()
		if err != nil {
			var closeError *websocket.CloseError
			if errors.As(err, &closeError) {
				switch closeError.Code {
				case websocket.CloseAbnormalClosure, websocket.CloseTLSHandshake:
					return
				}
				payload := websocket.FormatCloseMessage(closeError.Code, closeError.Text)
				_ = to.WriteControl(websocket.CloseMessage, payload, time.Now().Add(time.Second))
			}
			return
		}
		output, err := to.NextWriter(messageType)
		if err != nil {
			return
		}
		var capture metadataCapture
		if messageType == websocket.TextMessage {
			input = io.TeeReader(input, &capture)
		}
		if _, err := io.Copy(output, input); err != nil {
			return
		}
		if observeBeforeClose {
			observe(capture.message())
		}
		err = output.Close()
		if !observeBeforeClose {
			observe(capture.message())
		}
		if err != nil {
			return
		}
	}
}

type socketMeter struct {
	usage  Usage
	record func(Usage)

	mu        sync.Mutex
	last      wireMetadata
	lanes     map[string]wireMetadata
	pending   map[string]pendingResponse
	completed bool
}

type pendingResponse struct {
	usage    Usage
	streamID string
}

func (m *socketMeter) request(message wireMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch message.Type {
	case "response.create":
		metadata := message.wireMetadata
		if message.Response != nil {
			metadata = message.Response.wireMetadata
		}
		if message.StreamID == "" {
			m.last = metadata
		} else if _, exists := m.lanes[message.StreamID]; exists || len(m.lanes) < 32 {
			if m.lanes == nil {
				m.lanes = make(map[string]wireMetadata)
			}
			m.lanes[message.StreamID] = metadata
		}
	case "session.update":
		if message.Session != nil && message.Session.Model != "" {
			m.usage.Model = message.Session.Model
		}
	}
}

func (m *socketMeter) response(message wireMessage) {
	if message.Type != "response.created" && message.Type != "error" && !message.reportsUsage() {
		return
	}
	m.mu.Lock()
	usage := m.usage
	usage.At = time.Now().UTC()
	metadata := m.last
	if message.StreamID != "" {
		metadata = m.lanes[message.StreamID]
	}
	usage.applyResult(wireResult{wireMetadata: metadata}, false)
	id := message.ID
	if message.Response != nil {
		id = message.Response.ID
	}
	if message.Type == "error" && id == "" {
		for responseID, previous := range m.pending {
			if previous.streamID == message.StreamID {
				id = responseID
				break
			}
		}
	}
	if previous, ok := m.pending[id]; ok {
		usage = previous.usage
		delete(m.pending, id)
	}
	if message.Type == "error" {
		if status, err := strconv.Atoi(string(message.RawStatus)); err == nil && status >= 400 && status <= 599 {
			usage.Status = status
		} else {
			usage.Status = http.StatusBadGateway
		}
	}
	usage.applyMessage(message, message.Type != "response.created")
	if message.Type == "response.created" {
		// An out-of-band Realtime response can overlap the main response.
		if id != "" && len(m.pending) < 64 {
			if m.pending == nil {
				m.pending = make(map[string]pendingResponse)
			}
			m.pending[id] = pendingResponse{usage: usage, streamID: message.StreamID}
		}
		m.mu.Unlock()
		return
	}
	m.completed = true
	m.mu.Unlock()
	m.record(usage)
}

func (m *socketMeter) finish() {
	// Both relays have exited, so no more callbacks can mutate the meter.
	for _, response := range m.pending {
		m.record(response.usage)
	}
	if !m.completed && len(m.pending) == 0 {
		m.usage.applyResult(wireResult{wireMetadata: m.last}, false)
		m.record(m.usage)
	}
}
