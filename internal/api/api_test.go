package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lavicitor/netwatch/internal/config"
	"github.com/lavicitor/netwatch/internal/model"
	"github.com/lavicitor/netwatch/internal/scanner"
)

// openPort starts a listener that accepts and immediately closes
// connections, and returns the port it bound to.
func openPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test listener: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	return ln.Addr().(*net.TCPAddr).Port
}

// closedPort returns a port on 127.0.0.1 that nothing is listening on, so
// probing it gets a fast refusal instead of a timeout.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve a port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// newTestServer builds a Server that scans only loopback, so tests never
// touch the real network. The default target is loopback too, which is
// what exercises the "target omitted" path safely.
func newTestServer(t *testing.T, ports ...int) *Server {
	t.Helper()
	if len(ports) == 0 {
		ports = []int{closedPort(t)}
	}

	cfg := config.Config{DefaultTarget: "127.0.0.1/32"}
	sc := &scanner.Scanner{
		Ports:          ports,
		MaxConcurrency: 4,
		ProbeTimeout:   200 * time.Millisecond,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	return NewServer(cfg, sc, nil, logger)
}

func TestHandleHealth(t *testing.T) {
	s := newTestServer(t)

	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got["status"] != "ok" {
		t.Errorf(`status field = %v, want "ok"`, got["status"])
	}
	if got["db"] != false {
		t.Errorf("db field = %v, want false (no store configured)", got["db"])
	}
}

func TestHandleStartScan(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantTarget string
	}{
		{
			name:       "explicit target",
			body:       `{"target":"127.0.0.1/32"}`,
			wantStatus: http.StatusAccepted,
			wantTarget: "127.0.0.1/32",
		},
		{
			name:       "missing target falls back to the default",
			body:       `{}`,
			wantStatus: http.StatusAccepted,
			wantTarget: "127.0.0.1/32",
		},
		{
			name:       "empty target falls back to the default",
			body:       `{"target":"  "}`,
			wantStatus: http.StatusAccepted,
			wantTarget: "127.0.0.1/32",
		},
		{
			name:       "empty body falls back to the default",
			body:       "",
			wantStatus: http.StatusAccepted,
			wantTarget: "127.0.0.1/32",
		},
		{
			name:       "bare IP is not a CIDR",
			body:       `{"target":"127.0.0.1"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "nonsense target",
			body:       `{"target":"not-a-network"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "malformed JSON",
			body:       `{"target":`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServer(t)

			req := httptest.NewRequest(http.MethodPost, "/api/scan", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			s.Routes().ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus != http.StatusAccepted {
				return
			}

			var got struct {
				Status string `json:"status"`
				Target string `json:"target"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if got.Target != tt.wantTarget {
				t.Errorf("target = %q, want %q", got.Target, tt.wantTarget)
			}
			if got.Status == "" {
				t.Error("response has no status field")
			}
		})
	}
}

// A scan must not block the response: the client is told the scan started
// and then watches /api/stream for results.
func TestHandleStartScanRespondsBeforeScanFinishes(t *testing.T) {
	// A /29 of unroutable addresses: every probe runs to its timeout, so
	// a handler that waited for the scan would be far slower than this.
	s := newTestServer(t, closedPort(t))
	s.scanner.ProbeTimeout = 2 * time.Second

	req := httptest.NewRequest(http.MethodPost, "/api/scan", strings.NewReader(`{"target":"192.0.2.0/29"}`))
	rec := httptest.NewRecorder()

	start := time.Now()
	s.Routes().ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if elapsed > time.Second {
		t.Errorf("handler took %v; it should return without waiting for the scan", elapsed)
	}
}

func TestHandleStreamSendsScanResults(t *testing.T) {
	port := openPort(t)
	s := newTestServer(t, port)

	ts := httptest.NewServer(s.Routes())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Connecting first guarantees the subscription exists before the scan
	// starts broadcasting -- handleStream subscribes before it flushes
	// the response headers we read here.
	stream := openStream(ctx, t, ts.URL)
	defer stream.Close()

	startScan(ctx, t, ts.URL, `{"target":"127.0.0.1/32"}`)

	hosts, sawDone := readStream(t, stream)
	if !sawDone {
		t.Error("stream never sent the done event")
	}
	if len(hosts) != 1 {
		t.Fatalf("got %d hosts on the stream, want 1: %+v", len(hosts), hosts)
	}
	if hosts[0].IP != "127.0.0.1" {
		t.Errorf("host IP = %q, want 127.0.0.1", hosts[0].IP)
	}
	if len(hosts[0].Ports) != 1 || hosts[0].Ports[0].Port != port {
		t.Errorf("ports = %+v, want just port %d", hosts[0].Ports, port)
	}
}

// Every connected client sees every result, not just whichever one the
// broadcast reaches first.
func TestHandleStreamFansOutToEveryClient(t *testing.T) {
	port := openPort(t)
	s := newTestServer(t, port)

	ts := httptest.NewServer(s.Routes())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	first := openStream(ctx, t, ts.URL)
	defer first.Close()
	second := openStream(ctx, t, ts.URL)
	defer second.Close()

	startScan(ctx, t, ts.URL, `{"target":"127.0.0.1/32"}`)

	for i, stream := range []io.ReadCloser{first, second} {
		hosts, sawDone := readStream(t, stream)
		if !sawDone {
			t.Errorf("client %d never saw the done event", i)
		}
		if len(hosts) != 1 || hosts[0].IP != "127.0.0.1" {
			t.Errorf("client %d got %+v, want one host 127.0.0.1", i, hosts)
		}
	}
}

// A disconnecting client must be unsubscribed, or the hub leaks a channel
// per dropped connection and broadcasts to nobody forever.
func TestHandleStreamUnsubscribesOnDisconnect(t *testing.T) {
	s := newTestServer(t)

	ts := httptest.NewServer(s.Routes())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := openStream(ctx, t, ts.URL)

	if got := s.hub.subscriberCount(); got != 1 {
		t.Fatalf("subscribers while connected = %d, want 1", got)
	}

	cancel()
	stream.Close()

	deadline := time.Now().Add(3 * time.Second)
	for s.hub.subscriberCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("subscribers after disconnect = %d, want 0", s.hub.subscriberCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHandleListHostsWithoutStore(t *testing.T) {
	port := openPort(t)
	s := newTestServer(t, port)

	ts := httptest.NewServer(s.Routes())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Before any scan: an empty array, not an error and not "null".
	body, hosts := listHosts(ctx, t, ts.URL)
	if len(hosts) != 0 {
		t.Errorf("hosts before any scan = %+v, want none", hosts)
	}
	if strings.TrimSpace(body) != "[]" {
		t.Errorf("body before any scan = %q, want an empty JSON array", strings.TrimSpace(body))
	}

	stream := openStream(ctx, t, ts.URL)
	defer stream.Close()
	startScan(ctx, t, ts.URL, `{"target":"127.0.0.1/32"}`)
	readStream(t, stream) // wait for the scan to finish

	_, hosts = listHosts(ctx, t, ts.URL)
	if len(hosts) != 1 {
		t.Fatalf("hosts after scan = %+v, want 1", hosts)
	}
	if hosts[0].IP != "127.0.0.1" {
		t.Errorf("host IP = %q, want 127.0.0.1", hosts[0].IP)
	}
	if len(hosts[0].Ports) != 1 || hosts[0].Ports[0].Port != port {
		t.Errorf("ports = %+v, want just port %d", hosts[0].Ports, port)
	}
}

// A later scan replaces the previous results rather than appending to them.
func TestHandleListHostsReplacesPreviousScan(t *testing.T) {
	s := newTestServer(t, openPort(t))

	ts := httptest.NewServer(s.Routes())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	for range 2 {
		stream := openStream(ctx, t, ts.URL)
		startScan(ctx, t, ts.URL, `{"target":"127.0.0.1/32"}`)
		readStream(t, stream)
		stream.Close()
	}

	if _, hosts := listHosts(ctx, t, ts.URL); len(hosts) != 1 {
		t.Fatalf("hosts after two scans = %+v, want 1 (results should be replaced, not accumulated)", hosts)
	}
}

func TestHubUnsubscribeClosesTheChannel(t *testing.T) {
	h := newHub()
	ch := h.subscribe()

	h.unsubscribe(ch)
	if _, open := <-ch; open {
		t.Error("channel still delivering after unsubscribe")
	}

	// Unsubscribing twice must not panic on a double close, and a
	// broadcast with no subscribers left must be a no-op.
	h.unsubscribe(ch)
	h.broadcast(event{host: model.Host{IP: "127.0.0.1"}})

	if got := h.subscriberCount(); got != 0 {
		t.Errorf("subscribers = %d, want 0", got)
	}
}

// openStream opens an SSE connection and returns its body once the
// response headers have arrived.
func openStream(ctx context.Context, t *testing.T, baseURL string) io.ReadCloser {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/stream", nil)
	if err != nil {
		t.Fatalf("build stream request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect to stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("stream status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("stream Content-Type = %q, want text/event-stream", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("stream Cache-Control = %q, want no-cache", got)
	}

	return resp.Body
}

// readStream consumes SSE events until the done event arrives or the
// connection ends, and returns the hosts it saw.
func readStream(t *testing.T, body io.Reader) (hosts []model.Host, sawDone bool) {
	t.Helper()

	scan := bufio.NewScanner(body)
	for scan.Scan() {
		line := scan.Text()
		switch {
		case line == "event: done":
			sawDone = true
		case strings.HasPrefix(line, "data: "):
			payload := strings.TrimPrefix(line, "data: ")
			if sawDone {
				return hosts, true // the done event's own (empty) payload
			}
			var host model.Host
			if err := json.Unmarshal([]byte(payload), &host); err != nil {
				t.Fatalf("decode streamed host %q: %v", payload, err)
			}
			hosts = append(hosts, host)
		}
	}
	if err := scan.Err(); err != nil {
		t.Fatalf("read stream: %v", err)
	}
	return hosts, sawDone
}

func startScan(ctx context.Context, t *testing.T, baseURL, body string) {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/scan", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build scan request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("start scan: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("scan status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
}

func listHosts(ctx context.Context, t *testing.T, baseURL string) (string, []model.Host) {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/hosts", nil)
	if err != nil {
		t.Fatalf("build hosts request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list hosts: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hosts status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read hosts body: %v", err)
	}

	var hosts []model.Host
	if err := json.Unmarshal(raw, &hosts); err != nil {
		t.Fatalf("decode hosts %q: %v", raw, err)
	}
	return string(raw), hosts
}
