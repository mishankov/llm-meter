package relayoutbox

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	uploadPath = "/api/usage/events"
	fixtureKey = "llmp_fixture_usage_key"
)

func TestRunRetriesPersistedEventsInOrder(t *testing.T) {
	const first = `{"id":"first"}`
	const second = `{"id":"second"}`
	attempts := make(chan string, 4)
	returnFailure := make(chan struct{})
	var count atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != uploadPath ||
			r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-LLM-Proxy-Key") != fixtureKey {
			t.Error("incorrect usage upload request")
		}
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read usage payload: %v", err)
		}
		attempt := count.Add(1)
		select {
		case attempts <- string(payload):
		case <-r.Context().Done():
			return
		}
		if attempt == 1 {
			select {
			case <-r.Context().Done():
			case <-returnFailure:
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	directory := t.TempDir()
	queue := openTestQueue(t, directory, server)
	if err := queue.Enqueue(t.Context(), []byte(first)); err != nil {
		t.Fatal(err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}

	queue = openTestQueue(t, directory, server)
	defer queue.Close()
	ctx, stop := context.WithTimeout(t.Context(), 5*time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		queue.Run(ctx)
	}()
	defer func() {
		stop()
		<-done
	}()
	assertAttempt := func(want string) {
		t.Helper()
		select {
		case got := <-attempts:
			if got != want {
				t.Fatalf("upload payload %q, want %q", got, want)
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", want)
		}
	}
	assertAttempt(first)
	if err := queue.Enqueue(ctx, []byte(second)); err != nil {
		t.Fatal(err)
	}
	close(returnFailure)
	assertAttempt(first)
	assertAttempt(second)

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for queuedEvents(t, queue) > 0 {
		select {
		case <-ctx.Done():
			t.Fatal("acknowledged events were still queued")
		case <-ticker.C:
		}
	}
}

func TestRedirectRetainsUsageWithoutSendingCredentials(t *testing.T) {
	var destinationRequests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == uploadPath {
			http.Redirect(w, r, "/unexpected", http.StatusTemporaryRedirect)
			return
		}
		destinationRequests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	queue := openTestQueue(t, t.TempDir(), server)
	defer queue.Close()
	if err := queue.Enqueue(t.Context(), []byte(`{"id":"fixture"}`)); err != nil {
		t.Fatal(err)
	}
	err := queue.deliverNext(t.Context())
	if err == nil || err.Error() != "usage server returned HTTP 307" {
		t.Fatal("redirect must be a sanitized upload error")
	}
	if destinationRequests.Load() != 0 || queuedEvents(t, queue) != 1 {
		t.Fatal("redirect must leave the event queued and the destination untouched")
	}
}

func TestRejectedEventDoesNotBlockFollowingUploads(t *testing.T) {
	uploaded := make(chan string, 3)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upload: %v", err)
		}
		if string(payload) == "bad" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		uploaded <- string(payload)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	queue := openTestQueue(t, t.TempDir(), server)
	defer queue.Close()
	for _, payload := range []string{"first", "bad", "last"} {
		if err := queue.Enqueue(t.Context(), []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		if err := queue.deliverNext(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if queuedEvents(t, queue) != 0 || len(uploaded) != 2 || <-uploaded != "first" || <-uploaded != "last" {
		t.Fatal("queue did not advance past the rejected event in order")
	}
}

func TestOutboxBounds(t *testing.T) {
	endpoint, err := url.Parse("https://proxy.test" + uploadPath)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := Open(t.TempDir(), endpoint, fixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if err := queue.Enqueue(t.Context(), []byte(strings.Repeat("x", maxEventSize+1))); err == nil {
		t.Fatal("oversized event was queued")
	}
	if _, err := queue.db.Exec(`WITH RECURSIVE ids(n) AS (
		SELECT 1 UNION ALL SELECT n + 1 FROM ids WHERE n < ?
	) INSERT INTO events (payload) SELECT X'7b7d' FROM ids`, maxEvents); err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(t.Context(), []byte(`{"id":"overflow"}`)); !errors.Is(err, ErrFull) {
		t.Fatalf("queue allowed an event over the capacity limit: %v", err)
	}
	if count := queuedEvents(t, queue); count != maxEvents {
		t.Fatalf("full queue holds %d events, want %d", count, maxEvents)
	}
}

func TestQueueRemainsBoundToItsConnection(t *testing.T) {
	endpoint := &url.URL{Scheme: "https", Host: "proxy.test", Path: uploadPath}
	directory := t.TempDir()
	queue, err := Open(directory, endpoint, fixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(t.Context(), []byte(`{"id":"queued"}`)); err != nil {
		t.Fatal(err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	if wrong, err := Open(directory, endpoint, "different-key"); err == nil {
		wrong.Close()
		t.Fatal("another key can access this connection's pending events")
	}
	other := &url.URL{Scheme: "https", Host: "other.test", Path: uploadPath}
	if wrong, err := Open(directory, other, fixtureKey); err == nil {
		wrong.Close()
		t.Fatal("another server can receive this connection's pending events")
	}
}

func openTestQueue(t *testing.T, directory string, server *httptest.Server) *Queue {
	t.Helper()
	endpoint, err := url.Parse(server.URL + uploadPath)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := Open(directory, endpoint, fixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	queue.client.Transport = server.Client().Transport
	return queue
}

func queuedEvents(t *testing.T, queue *Queue) int {
	t.Helper()
	var count int
	if err := queue.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
