// Package api exposes Netwatch's REST endpoints and the live-results web GUI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/lavicitor/netwatch/internal/config"
	"github.com/lavicitor/netwatch/internal/model"
	"github.com/lavicitor/netwatch/internal/scanner"
	"github.com/lavicitor/netwatch/internal/store"
)

type Server struct {
	scanner *scanner.Scanner
	store   *store.Store // nil in DB-less mode -- handlers must check this
	logger  *slog.Logger

	defaultTarget string
	hub           *hub

	// Most recent scan results, kept so /api/hosts has something to serve
	// in DB-less mode. scanSeq identifies the scan that owns the buffer:
	// if two scans overlap, results from the older one are dropped rather
	// than interleaved into the newer one's list.
	mu       sync.Mutex
	scanSeq  uint64
	lastScan []model.Host
}

func NewServer(cfg config.Config, sc *scanner.Scanner, st *store.Store, logger *slog.Logger) *Server {
	return &Server{
		scanner:       sc,
		store:         st,
		logger:        logger,
		defaultTarget: cfg.DefaultTarget,
		hub:           newHub(),
	}
}

// Routes wires up the REST API and serves the static web GUI at "/".
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("POST /api/scan", s.handleStartScan)
	mux.HandleFunc("GET /api/hosts", s.handleListHosts)
	mux.HandleFunc("GET /api/stream", s.handleStream) // SSE: live results
	mux.Handle("/", http.FileServer(http.Dir("web/static")))

	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"db":     s.store != nil,
	})
}

type scanRequest struct {
	Target string `json:"target"`
}

func (s *Server) handleStartScan(w http.ResponseWriter, r *http.Request) {
	var req scanRequest
	// An empty body is not an error: it means "scan the default target".
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	target := strings.TrimSpace(req.Target)
	if target == "" {
		target = s.defaultTarget
	}
	if _, _, err := net.ParseCIDR(target); err != nil {
		http.Error(w, fmt.Sprintf("invalid target %q: expected CIDR notation, e.g. 10.89.0.0/24", target), http.StatusBadRequest)
		return
	}

	// The scan outlives this request, so it can't borrow the request's
	// context -- that one is cancelled as soon as we return below.
	go s.runScan(context.Background(), target)

	s.logger.Info("scan started", "target", target)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "scanning",
		"target": target,
	})
}

// runScan drives one scan to completion, publishing each host to the live
// stream and to the in-memory buffer as it arrives.
func (s *Server) runScan(ctx context.Context, target string) {
	seq := s.beginScan()

	results := make(chan model.Host)
	errCh := make(chan error, 1)
	go func() { errCh <- s.scanner.Scan(ctx, target, results) }()

	count := 0
	for host := range results {
		count++
		s.recordHost(seq, host)
		s.hub.broadcast(event{host: host})

		// Persistence goes here once internal/store grows a write method
		// (see the TODO at the bottom of internal/store/store.go). Until
		// then results are in-memory only, even with a store configured.
	}

	if err := <-errCh; err != nil {
		s.logger.Error("scan failed", "target", target, "err", err)
	}

	// The GUI closes its EventSource on this event; without it the stream
	// stays open and the status line never settles.
	s.hub.broadcast(event{done: true})
	s.logger.Info("scan finished", "target", target, "hosts", count)
}

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	// With a store configured this should prefer persisted history, which
	// survives restarts and covers more than the last scan. internal/store
	// has no read method yet, so for now both modes serve the same
	// in-memory results -- DB-less mode still shows live results, it just
	// won't have history across runs.
	writeJSON(w, http.StatusOK, s.hosts())
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Subscribe before the headers go out, so that a client which has seen
	// the response headers is guaranteed to receive every event broadcast
	// from then on -- including one from a scan it starts immediately after.
	events := s.hub.subscribe()
	defer s.hub.unsubscribe(events)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return // client disconnected
		case ev, ok := <-events:
			if !ok {
				return
			}
			if err := writeEvent(w, ev); err != nil {
				s.logger.Debug("dropping stream client", "err", err)
				return
			}
			flusher.Flush()
		}
	}
}

func writeEvent(w io.Writer, ev event) error {
	if ev.done {
		_, err := io.WriteString(w, "event: done\ndata: {}\n\n")
		return err
	}

	payload, err := json.Marshal(ev.host)
	if err != nil {
		return fmt.Errorf("encode host: %w", err)
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
	return err
}

// beginScan claims the in-memory result buffer for a new scan and returns
// the sequence number identifying it.
func (s *Server) beginScan() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scanSeq++
	s.lastScan = nil
	return s.scanSeq
}

func (s *Server) recordHost(seq uint64, host model.Host) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq != s.scanSeq {
		return // a newer scan owns the buffer now
	}
	s.lastScan = append(s.lastScan, host)
}

// hosts returns a copy of the most recent scan's results. The copy matters:
// the caller encodes it as JSON while the scan goroutine may still be
// appending. It is never nil, so the GUI always gets a JSON array.
func (s *Server) hosts() []model.Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.Host, len(s.lastScan))
	copy(out, s.lastScan)
	return out
}

// event is one item on the live stream: either a discovered host or the
// marker that the scan producing them has finished.
type event struct {
	host model.Host
	done bool
}

// hub fans scan results out to every connected stream client. Subscribers
// come and go with HTTP connections, so the set is guarded by a mutex and
// each subscriber gets its own buffered channel.
type hub struct {
	mu   sync.Mutex
	subs map[chan event]struct{}
}

// subBuffer is deliberately generous: broadcast never blocks, so a client
// that stops reading loses events rather than stalling the scan.
const subBuffer = 128

func newHub() *hub {
	return &hub{subs: make(map[chan event]struct{})}
}

func (h *hub) subscribe() chan event {
	ch := make(chan event, subBuffer)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subs[ch] = struct{}{}
	return ch
}

func (h *hub) unsubscribe(ch chan event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[ch]; !ok {
		return
	}
	delete(h.subs, ch)
	close(ch)
}

// broadcast sends to every current subscriber. Sending under the same lock
// unsubscribe holds is what makes "send on a closed channel" impossible.
func (h *hub) broadcast(ev event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default: // subscriber is behind; drop rather than block the scan
		}
	}
}

func (h *hub) subscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
