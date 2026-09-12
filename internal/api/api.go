// Package api exposes Netwatch's REST endpoints and the live-results web GUI.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/lavicitor/netwatch/internal/scanner"
	"github.com/lavicitor/netwatch/internal/store"
)

type Server struct {
	scanner *scanner.Scanner
	store   *store.Store // nil in DB-less mode -- handlers must check this
	logger  *slog.Logger

	// TODO: a broadcast hub for streaming live scan results to every
	// connected GUI client, e.g.:
	//   type hub struct {
	//       mu   sync.Mutex
	//       subs map[chan model.Host]struct{}
	//   }
	// handleStartScan feeds it, handleStream drains it per-connection.
}

func NewServer(sc *scanner.Scanner, st *store.Store, logger *slog.Logger) *Server {
	return &Server{scanner: sc, store: st, logger: logger}
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

func (s *Server) handleStartScan(w http.ResponseWriter, r *http.Request) {
	// TODO: parse target CIDR from the request body (default to
	// config.DefaultTarget if omitted), run s.scanner.Scan in a goroutine,
	// and forward each result to the broadcast hub -- and to s.store, if
	// s.store != nil -- as it arrives.
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	// TODO: if s.store == nil, serve the last in-memory scan result
	// instead of erroring -- DB-less mode should still show live results,
	// it just won't have history across runs.
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	// TODO: upgrade to Server-Sent Events (Content-Type:
	// text/event-stream, flush after each event via http.Flusher) and
	// subscribe this connection to the broadcast hub until the client
	// disconnects (watch r.Context().Done()).
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
