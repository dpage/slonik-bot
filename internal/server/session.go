package server

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/dpage/slonik-bot/internal/captions"
	"github.com/dpage/slonik-bot/internal/meet"
	"github.com/dpage/slonik-bot/internal/output"
	"github.com/dpage/slonik-bot/internal/record"
)

// Reasons a session stops recording.
const (
	reasonStopped     = "stopped"
	reasonIdle        = "idle"
	reasonInterrupted = "interrupted"
)

// shutdownGrace bounds how long Run waits for requests in flight.
const shutdownGrace = 5 * time.Second

// session is the one meeting being recorded. Its fields are guarded by
// Server.mu.
type session struct {
	id, tab, code string
	started       time.Time
	lastContact   time.Time
	acked         int  // the highest seq applied; every lower one has been too
	chatLogged    bool // the extension's chat notice failure has been logged
	rec           *record.Recording
}

// Run serves on ln until ctx ends. It then shuts the HTTP server down, stops
// any open session and waits for every set of notes being written.
func (s *Server) Run(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	ticker := time.NewTicker(s.opts.Tick)
	defer ticker.Stop()
	var err error
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case err = <-served:
			err = fmt.Errorf("server: %w", err)
			break loop
		case now := <-ticker.C:
			s.tick(ctx, now)
		}
	}

	// The recording outlives ctx, as in the bot, so that its notes are written.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	if serr := srv.Shutdown(sctx); serr != nil {
		s.log.Warn("shutting down the HTTP server", "error", serr)
	}
	if err == nil {
		if serr := <-served; !errors.Is(serr, http.ErrServerClosed) {
			err = fmt.Errorf("server: %w", serr)
		}
	}

	// A request still in flight past the grace period must not open a
	// session that nothing would finish.
	s.mu.Lock()
	s.closed = true
	if s.cur != nil {
		s.stop(ctx, time.Now(), reasonInterrupted)
	}
	s.mu.Unlock()
	s.finishing.Wait()
	return err
}

// tick lets the open session's assembler finish quiet speech, and stops the
// session once the extension has been silent for IdleTimeout.
func (s *Server) tick(ctx context.Context, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return
	}
	s.cur.rec.Tick(now)
	if now.Sub(s.cur.lastContact) >= s.opts.IdleTimeout {
		s.stop(ctx, now, reasonIdle)
	}
}

// stop ends the open session. With nothing captured its folder is removed;
// otherwise the notes are written in the background, tracked by finishing,
// under ctx without its cancellation. The caller holds s.mu.
func (s *Server) stop(ctx context.Context, now time.Time, reason string) {
	cur := s.cur
	s.cur = nil
	m, us := cur.rec.Stop(now)
	log := s.log.With("code", cur.code, "dir", cur.rec.Dir())
	log.Info("stopped recording", "reason", reason, "utterances", len(us))
	if len(us) == 0 {
		cur.rec.Discard()
		log.Warn("no captions were captured; nothing written")
		return
	}
	// Shutting down ends the meeting, not the summary.
	ctx = context.WithoutCancel(ctx)
	dir, jsonl := cur.rec.Dir(), cur.rec.JSONL()
	s.finishing.Add(1)
	go func() {
		defer s.finishing.Done()
		summarised, err := record.Finish(ctx, dir, jsonl, m, us, s.opts.LLM)
		if err != nil {
			log.Error("writing the notes", "error", err)
			return
		}
		log.Info("notes written", "summarised", summarised)
	}()
}

// sessionAnswer is the body of a 200 or 201 from POST /v1/sessions.
type sessionAnswer struct {
	ID         string `json:"id"`
	Dir        string `json:"dir"`
	Acked      int    `json:"acked"`
	ChatNotice string `json:"chat_notice"`
}

func (s *Server) answer(cur *session) sessionAnswer {
	return sessionAnswer{ID: cur.id, Dir: cur.rec.Dir(), Acked: cur.acked, ChatNotice: s.opts.Config.ChatNotice}
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
		Tab  string `json:"tab"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	code, err := meet.ParseCode(req.Code)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		writeError(w, http.StatusServiceUnavailable, "server is shutting down")
		return
	}
	now := time.Now()
	if cur := s.cur; cur != nil {
		// The same tab again, after the extension's service worker restarted.
		if cur.tab == req.Tab && cur.code == code {
			cur.lastContact = now
			writeJSON(w, http.StatusOK, s.answer(cur))
			return
		}
		writeError(w, http.StatusConflict, "another meeting is already being recorded")
		return
	}

	id, err := newID()
	if err != nil {
		s.fail(w, "making a session id", err)
		return
	}
	dir, err := output.NewDir(s.opts.Config.OutputDir, code, now)
	if err != nil {
		s.fail(w, "creating the notes folder", err)
		return
	}
	rec, err := record.Start(dir, code, now, s.opts.Idle, s.log)
	if err != nil {
		_ = os.Remove(dir)
		s.fail(w, "starting the recording", err)
		return
	}
	s.cur = &session{id: id, tab: req.Tab, code: code, started: now, lastContact: now, rec: rec}
	s.log.Info("recording a meeting", "code", code, "notes", dir)
	writeJSON(w, http.StatusCreated, s.answer(s.cur))
}

// eventIn is one caption event as the extension sends it.
type eventIn struct {
	Seq int   `json:"seq"`
	At  int64 `json:"at"` // the page's Date.now(), in milliseconds
	captions.Event
}

func (s *Server) postEvents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Events       []eventIn `json:"events"`
		Participants []string  `json:"participants"`
		ChatError    string    `json:"chat_error"` // why the chat notice was not posted
	}
	if !readJSON(w, r, &req) {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.open(r.PathValue("id"))
	if cur == nil {
		writeError(w, http.StatusNotFound, "no such session")
		return
	}
	now := time.Now()
	cur.lastContact = now
	if req.ChatError != "" && !cur.chatLogged {
		cur.chatLogged = true
		s.log.Warn("chat notice not posted", "code", cur.code, "error", req.ChatError)
	}
	// Events resent after a lost answer are skipped; after a gap, the rest
	// wait until the missing ones arrive.
	slices.SortStableFunc(req.Events, func(a, b eventIn) int { return cmp.Compare(a.Seq, b.Seq) })
	for _, e := range req.Events {
		if e.Seq <= cur.acked {
			continue
		}
		if e.Seq != cur.acked+1 {
			break
		}
		at := time.UnixMilli(e.At)
		if at.Before(cur.started) {
			at = cur.started
		}
		if at.After(now) {
			at = now
		}
		cur.rec.Observe(e.Event, at)
		cur.acked = e.Seq
	}
	cur.rec.AddParticipants(req.Participants...)
	writeJSON(w, http.StatusOK, map[string]int{"acked": cur.acked})
}

func (s *Server) stopSession(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !readJSON(w, r, &req) {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.open(r.PathValue("id"))
	if cur == nil {
		writeError(w, http.StatusNotFound, "no such session")
		return
	}
	dir := cur.rec.Dir()
	s.stop(r.Context(), time.Now(), reasonStopped)
	writeJSON(w, http.StatusOK, map[string]string{"dir": dir})
}

// open returns the open session if it has the given id. The caller holds s.mu.
func (s *Server) open(id string) *session {
	if s.cur == nil || s.cur.id != id {
		return nil
	}
	return s.cur
}

// fail logs an unexpected error and answers 500.
func (s *Server) fail(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "error", err)
	writeError(w, http.StatusInternalServerError, what+" failed")
}

// readJSON decodes the request body into v, answering 413 or 400 and
// returning false if it cannot. An empty body leaves v as it is.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(r.Body).Decode(v)
	var tooBig *http.MaxBytesError
	switch {
	case err == nil, errors.Is(err, io.EOF):
		return true
	case errors.As(err, &tooBig):
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
	default:
		writeError(w, http.StatusBadRequest, "bad request body: "+err.Error())
	}
	return false
}

// newID returns 16 random bytes in hex.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
