// Package server is the loopback HTTP server that the Chrome extension posts
// Meet captions to. Every request passes the Host, Origin and bearer token
// checks before it reaches a handler.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dpage/slonik-bot/internal/config"
	"github.com/dpage/slonik-bot/internal/summary"
	"github.com/dpage/slonik-bot/internal/version"
)

// maxBody is the largest request body the server will read.
const maxBody = 1 << 20

// Options configures New.
type Options struct {
	Config config.Config
	Port   int    // the port the server listens on; the Host header must match it
	Token  string // the bearer token every request must carry

	LLM         summary.LLM   // nil means no summary (used by sessions)
	Idle        time.Duration // the assembler idle; 0 means 4s, as in the bot (used by sessions)
	IdleTimeout time.Duration // 0 means Config.Serve.IdleTimeout (used by sessions)
	Tick        time.Duration // how often sessions check for idleness; 0 means 1s

	// AllowOrigin decides whether a request's Origin is acceptable; nil means
	// a chrome-extension:// prefix. Tests widen it.
	AllowOrigin func(origin string) bool
	Logger      *slog.Logger
}

// Server holds the state shared by the handlers.
type Server struct {
	opts Options
	log  *slog.Logger

	mu  sync.Mutex
	cur *session // the one open session, or nil

	finishing sync.WaitGroup // notes being written
}

// New returns a Server with the Options defaults filled in.
func New(o Options) *Server {
	if o.Idle == 0 {
		o.Idle = 4 * time.Second
	}
	if o.IdleTimeout == 0 {
		o.IdleTimeout = o.Config.Serve.IdleTimeout
	}
	if o.Tick == 0 {
		o.Tick = time.Second
	}
	if o.AllowOrigin == nil {
		o.AllowOrigin = func(origin string) bool {
			return strings.HasPrefix(origin, "chrome-extension://")
		}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Server{opts: o, log: o.Logger}
}

// Handler returns the server's routes wrapped in the security checks.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/hello", s.hello)
	mux.HandleFunc("POST /v1/sessions", s.createSession)
	mux.HandleFunc("POST /v1/sessions/{id}/events", s.postEvents)
	mux.HandleFunc("POST /v1/sessions/{id}/stop", s.stopSession)
	return s.secure(mux)
}

// secure applies, in order: the Host check, the Origin check (which also sets
// the CORS headers), the CORS preflight, the bearer token and the body cap.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A DNS-rebinding page reaches us under its own hostname.
		if r.Host != fmt.Sprintf("127.0.0.1:%d", s.opts.Port) &&
			r.Host != fmt.Sprintf("localhost:%d", s.opts.Port) {
			writeError(w, http.StatusForbidden, "bad host")
			return
		}

		origin := r.Header.Get("Origin")
		if origin != "" {
			if !s.opts.AllowOrigin(origin) {
				writeError(w, http.StatusForbidden, "bad origin")
				return
			}
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Vary", "Origin")
		}

		if r.Method == http.MethodOptions && origin != "" {
			h := w.Header()
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Allow-Methods", "GET, POST")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if !s.validToken(r.Header.Get("Authorization")) {
			writeError(w, http.StatusUnauthorized, "missing or wrong token")
			return
		}

		if r.ContentLength > maxBody {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		// Covers bodies of unknown length; handlers see an error past the cap.
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

// validToken reports whether the Authorization header carries the token. An
// empty configured token matches nothing.
func (s *Server) validToken(header string) bool {
	got, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || s.opts.Token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.opts.Token)) == 1
}

func (s *Server) hello(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version":     version.Version,
		"chat_notice": s.opts.Config.ChatNotice,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
