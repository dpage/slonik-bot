// Package fakemeet serves a stand-in for the Google Meet web page, so that the
// meet automation, the caption observer and the end-to-end test can be driven
// against real Chrome without a Google account. The selectors and strings in
// page.html are written as literals on purpose: the fake must not import the
// code it tests.
package fakemeet

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed page.html
var pageHTML []byte

const defaultSelfName = "Slonik Bot (Meeting Transcriber)"

// pollWait is how long /api/commands holds a request open when idle.
const pollWait = time.Second

// Scenario selects which variant of the Meet page is served.
type Scenario struct {
	SignedOut     bool   `json:"signedOut"`     // pre-join shows the "Your name" field and "Ask to join"
	NotFound      bool   `json:"notFound"`      // page shows "Check your meeting code" and no join button
	Lobby         bool   `json:"lobby"`         // join button reads "Ask to join"; waits for Admit or Deny
	CaptionsMenu  bool   `json:"captionsMenu"`  // in call, "Captions and translation" replaces "Turn on captions"
	PlainCaptions bool   `json:"plainCaptions"` // caption blocks carry no Meet class names
	SelfName      string `json:"selfName"`      // the bot's own tile name; "" means "Slonik Bot (Meeting Transcriber)"

	// The microphone and camera buttons ignore this many clicks each before
	// they respond, or every click when negative, standing for a Meet that
	// does not act on the bot's click.
	PrejoinClicksIgnored int  `json:"prejoinClicksIgnored"`
	InCallClicksIgnored  int  `json:"inCallClicksIgnored"`
	DevicesOnInCall      bool `json:"devicesOnInCall"` // the microphone and camera come back on once in the call
}

type queuedCommand struct {
	Seq     int             `json:"seq"`
	Op      string          `json:"op"`
	Block   int             `json:"block"`
	Speaker string          `json:"speaker"`
	Text    string          `json:"text"`
	Names   []string        `json:"names"`
	Cmds    []queuedCommand `json:"cmds,omitempty"` // for op "batch"
}

type report struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Seq   int    `json:"seq"` // the page's own order, since its fetches can arrive out of order
}

// Server is a running fake Meet page.
type Server struct {
	URL string

	ts       *httptest.Server
	scenario Scenario
	done     chan struct{}

	closeOnce sync.Once

	mu         sync.Mutex
	cmds       []queuedCommand
	changed    chan struct{}   // closed and replaced whenever cmds grows
	batch      []queuedCommand // non-nil while InOneTask collects commands
	joined     bool
	micOff     bool
	cameraOff  bool
	micSeq     int // Seq of the report micOff came from
	cameraSeq  int
	captionsOn bool
	language   string
	// What the page got when it asked for each device: a DOMException name
	// such as "NotAllowedError", "granted", or "permission <state>" when it
	// did not ask because the permission was not denied.
	micAccess    string
	cameraAccess string
	chat         []string
	left         bool
}

// New starts a server with the default Scenario.
func New() *Server { return NewWithScenario(Scenario{}) }

// NewWithScenario starts a server serving the given Scenario.
func NewWithScenario(sc Scenario) *Server {
	if sc.SelfName == "" {
		sc.SelfName = defaultSelfName
	}
	s := &Server{scenario: sc, done: make(chan struct{}), changed: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/scenario", s.handleScenario)
	mux.HandleFunc("GET /api/commands", s.handleCommands)
	mux.HandleFunc("POST /api/report", s.handleReport)
	mux.HandleFunc("GET /", s.handlePage)
	s.ts = httptest.NewServer(mux)
	s.URL = s.ts.URL
	return s
}

// Close stops the server, releasing any pending long polls.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.ts.Close()
	})
}

func (s *Server) push(c queuedCommand) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.batch != nil {
		s.batch = append(s.batch, c)
		return
	}
	c.Seq = len(s.cmds) + 1
	s.cmds = append(s.cmds, c)
	close(s.changed)
	s.changed = make(chan struct{})
}

// InOneTask runs f, and has the page apply the commands f sends in one
// JavaScript task, so that a MutationObserver sees only their combined
// effect. By default each command runs in a task of its own. Commands sent
// from other goroutines whilst f runs join the batch.
func (s *Server) InOneTask(f func()) {
	s.mu.Lock()
	s.batch = []queuedCommand{}
	s.mu.Unlock()
	f()
	s.mu.Lock()
	cmds := s.batch
	s.batch = nil
	s.mu.Unlock()
	s.push(queuedCommand{Op: "batch", Cmds: cmds})
}

// Admit lets the bot in from the lobby.
func (s *Server) Admit() { s.push(queuedCommand{Op: "admit"}) }

// Deny refuses the bot's request to join.
func (s *Server) Deny() { s.push(queuedCommand{Op: "deny"}) }

// SetParticipants sets the other people in the call.
func (s *Server) SetParticipants(names ...string) {
	if names == nil {
		names = []string{}
	}
	s.push(queuedCommand{Op: "participants", Names: names})
}

// Caption creates or updates a caption block.
func (s *Server) Caption(block int, speaker, text string) {
	s.push(queuedCommand{Op: "caption", Block: block, Speaker: speaker, Text: text})
}

// RemoveCaption removes a caption block.
func (s *Server) RemoveCaption(block int) { s.push(queuedCommand{Op: "remove", Block: block}) }

// WrapCaptions moves every caption block into one new div inside the region;
// blocks added afterwards go into it too.
func (s *Server) WrapCaptions() { s.push(queuedCommand{Op: "wrap"}) }

// RemoveCaptionWrapper removes the div WrapCaptions made, and the blocks in it.
func (s *Server) RemoveCaptionWrapper() { s.push(queuedCommand{Op: "remove-wrapper"}) }

// ReplaceCaptionRegion swaps the region node for a new, empty one.
func (s *Server) ReplaceCaptionRegion() { s.push(queuedCommand{Op: "replace-region"}) }

// End shows the "call ended" text.
func (s *Server) End() { s.push(queuedCommand{Op: "end"}) }

// Kick shows the "removed from the meeting" notice.
func (s *Server) Kick() { s.push(queuedCommand{Op: "kick"}) }

// Joined reports whether the bot entered the call.
func (s *Server) Joined() bool { return s.read(func() bool { return s.joined }) }

// MicOff reports whether the microphone is off, as the page last said.
func (s *Server) MicOff() bool { return s.read(func() bool { return s.micOff }) }

// CameraOff reports whether the camera is off, as the page last said.
func (s *Server) CameraOff() bool { return s.read(func() bool { return s.cameraOff }) }

// CaptionsOn reports whether captions are currently switched on.
func (s *Server) CaptionsOn() bool { return s.read(func() bool { return s.captionsOn }) }

// Left reports whether the bot pressed Leave call.
func (s *Server) Left() bool { return s.read(func() bool { return s.left }) }

// Language returns the language the bot chose, or "" if none yet.
func (s *Server) Language() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.language
}

// MicrophoneAccess and CameraAccess return what the page got when it asked
// for the device, or "" before it has reported.
func (s *Server) MicrophoneAccess() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.micAccess
}

// CameraAccess is MicrophoneAccess for the camera.
func (s *Server) CameraAccess() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cameraAccess
}

// Chat returns the chat messages the bot sent.
func (s *Server) Chat() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.chat...)
}

func (s *Server) read(f func() bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return f()
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(pageHTML)
}

func (s *Server) handleScenario(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.scenario)
}

func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	deadline := time.NewTimer(pollWait)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		out := []queuedCommand{}
		for _, c := range s.cmds {
			if c.Seq > after {
				out = append(out, c)
			}
		}
		wait := s.changed
		s.mu.Unlock()
		if len(out) > 0 {
			writeJSON(w, out)
			return
		}
		select {
		case <-wait:
		case <-deadline.C:
			writeJSON(w, out)
			return
		case <-s.done:
			writeJSON(w, out)
			return
		case <-r.Context().Done():
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	var rep report
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch rep.Kind {
	case "joined":
		s.joined = true
	case "mic-off", "mic-on":
		if rep.Seq > s.micSeq {
			s.micOff, s.micSeq = rep.Kind == "mic-off", rep.Seq
		}
	case "camera-off", "camera-on":
		if rep.Seq > s.cameraSeq {
			s.cameraOff, s.cameraSeq = rep.Kind == "camera-off", rep.Seq
		}
	case "microphone":
		s.micAccess = rep.Value
	case "camera":
		s.cameraAccess = rep.Value
	case "captions-on":
		s.captionsOn = true
	case "captions-off":
		s.captionsOn = false
	case "language":
		s.language = rep.Value
	case "chat":
		s.chat = append(s.chat, rep.Value)
	case "left":
		s.left = true
	default:
		http.Error(w, "unknown report kind", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
