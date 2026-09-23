// Package server implements the ingestion HTTP API (spec §5). It never
// logs request bodies, IPs, or User-Agents.
package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/geo"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// maxBody accommodates a full 500-event batch; single events are a batch
// of one.
const maxBody = 256 << 10

type Enqueuer interface {
	EnqueueView(v store.View)
	EnqueueEvent(e store.ProductEvent)
}

// NameStore is the slice of store.Store the handler needs for display names.
// Names are written straight through rather than buffered: they are rare,
// idempotent, and must be readable by the next daily pass.
type NameStore interface {
	UpsertIdentities(ctx context.Context, ids []store.Identity) error
}

// keyCounters accumulates per-key-label ingest counts for the per-minute
// summary line. Labels only, never the keys themselves. This is what makes a
// key safe to retire: the summary shows an old label falling to zero.
type keyCounters struct {
	mu   sync.Mutex
	seen map[string][2]int
}

func newKeyCounters() *keyCounters { return &keyCounters{seen: map[string][2]int{}} }

func (c *keyCounters) record(label string, accepted, rejected int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.seen[label]
	c.seen[label] = [2]int{v[0] + accepted, v[1] + rejected}
}

// Drain returns the accumulated counts and resets them.
func (c *keyCounters) Drain() map[string][2]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.seen
	c.seen = map[string][2]int{}
	return out
}

type Salt interface {
	Current(ctx context.Context) (string, error)
}

// Server is the ingestion HTTP handler.
type Server struct {
	cfg      *config.Config
	reg      *manage.Registry
	queue    Enqueuer
	geo      geo.Provider
	salt     Salt
	names    NameStore
	counters *keyCounters
	// idsSeen holds "<project id>/<actor kind>" for every project and kind
	// that has sent an id since the process started. Nothing on the server
	// decides whether ids are stored, so this is how an operator sees that
	// they are: one Info line per project per kind per process.
	idsSeen sync.Map
	logger  *slog.Logger
	mux     *http.ServeMux
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// Counters exposes ingest counters for the periodic summary log.
func (s *Server) Counters() *keyCounters { return s.counters }

// noteIDs logs "project receives ids" the first time a batch for the
// project resolves an actor of kind (user or install) in this process.
func (s *Server) noteIDs(projectID int64, kind string) {
	key := strconv.FormatInt(projectID, 10) + "/" + kind
	if _, loaded := s.idsSeen.LoadOrStore(key, struct{}{}); !loaded {
		s.logger.Info("project receives ids", "project", projectID, "kind", kind)
	}
}

func New(cfg *config.Config, reg *manage.Registry, q Enqueuer, g geo.Provider, salt Salt, names NameStore, logger *slog.Logger) *Server {
	s := &Server{cfg: cfg, reg: reg, queue: q, geo: g, salt: salt, names: names,
		counters: newKeyCounters(), logger: logger, mux: http.NewServeMux()}
	s.Mount(s.mux)
	return s
}

// Mount registers the ingest surface's routes on mux: its own when the
// surface has a listener to itself, the shared one beside the API
// otherwise.
func (s *Server) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /ingest/events", s.handleEvents)
	mux.HandleFunc("OPTIONS /ingest/events", s.handlePreflight)
	// /api/events is where events went before the API surface took /api/.
	// Native apps ship it compiled in and browsers keep cached SDKs posting
	// there, so it stays an alias. On a shared listener these patterns are
	// more specific than the API's /api/ prefix, so they never reach its auth.
	mux.HandleFunc("POST /api/events", s.handleEvents)
	mux.HandleFunc("OPTIONS /api/events", s.handlePreflight)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	s.registerScript(mux)
}

// originAllowed reports whether the request origin is allowed for the
// project and emits CORS headers when it is.
func (s *Server) originAllowed(w http.ResponseWriter, r *http.Request, projectID int64) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	if !s.reg.Snapshot(r.Context()).OriginAllowed(projectID, origin) {
		return false
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Vary", "Origin")
	return true
}

func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin != "" && s.reg.Snapshot(r.Context()).AnyOriginAllowed(origin) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Vary", "Origin")
		h.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type, X-Analytics-Key")
		h.Set("Access-Control-Max-Age", "86400")
	}
	w.WriteHeader(http.StatusNoContent)
}

func clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
