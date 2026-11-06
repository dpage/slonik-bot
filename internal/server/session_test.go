package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dpage/slonik-bot/internal/config"
	"github.com/dpage/slonik-bot/internal/transcript"
)

const sampleResult = `{"title":"Meetup planning","summary":"We planned.","decisions":[],"action_items":[],"corrections":[]}`

type fakeLLM struct {
	out   string
	calls atomic.Int32
}

func (l *fakeLLM) Complete(context.Context, string, string, map[string]any) (string, error) {
	l.calls.Add(1)
	return l.out, nil
}

// logBuffer collects log output from the handlers' goroutines.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// running is a Server serving on a real loopback listener.
type running struct {
	t      *testing.T
	base   string
	out    string // Config.OutputDir
	llm    *fakeLLM
	opts   Options
	cancel context.CancelFunc
	done   chan error
}

// start runs a Server until the test ends; change adjusts the Options first.
func start(t *testing.T, change func(*Options)) *running {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.OutputDir = t.TempDir()
	cfg.ChatNotice = "Test notice"
	llm := &fakeLLM{out: sampleResult}
	o := Options{
		Config: cfg,
		Port:   ln.Addr().(*net.TCPAddr).Port,
		Token:  testToken,
		LLM:    llm,
		Idle:   50 * time.Millisecond,
		Tick:   10 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if change != nil {
		change(&o)
	}
	return serve(t, ln, o, llm)
}

// serve runs a Server with o on ln until the test ends.
func serve(t *testing.T, ln net.Listener, o Options, llm *fakeLLM) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{t: t, base: "http://" + ln.Addr().String(), out: o.Config.OutputDir, llm: llm, opts: o, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- New(o).Run(ctx, ln) }()
	t.Cleanup(func() { r.shutdown() })
	return r
}

// restart stops r and runs a new Server with the same Options on the same
// port, as a restarted slonik-bot serve would.
func (r *running) restart() *running {
	r.t.Helper()
	r.shutdown()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", r.opts.Port))
	if err != nil {
		r.t.Fatal(err)
	}
	return serve(r.t, ln, r.opts, r.llm)
}

// shutdown cancels Run and waits for it to return. It is safe to call twice.
func (r *running) shutdown() {
	r.t.Helper()
	r.cancel()
	select {
	case err, ok := <-r.done:
		if ok {
			close(r.done)
			if err != nil {
				r.t.Errorf("Run: %v", err)
			}
		}
	case <-time.After(10 * time.Second):
		r.t.Fatal("Run did not return")
	}
}

// post sends body as JSON and decodes the answer into out, if not nil.
func (r *running) post(path string, body, out any) int {
	r.t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		r.t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), "POST", r.base+path, bytes.NewReader(b))
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			r.t.Fatalf("%s: decoding the answer: %v", path, err)
		}
	}
	return resp.StatusCode
}

type created struct {
	ID         string `json:"id"`
	Dir        string `json:"dir"`
	Acked      int    `json:"acked"`
	ChatNotice string `json:"chat_notice"`
}

func (r *running) create(tab string) created {
	r.t.Helper()
	var c created
	if code := r.post("/v1/sessions", map[string]string{"code": "abc-defg-hij", "tab": tab}, &c); code != 201 {
		r.t.Fatalf("create: got %d, want 201", code)
	}
	return c
}

type event struct {
	Seq     int    `json:"seq"`
	At      int64  `json:"at"`
	Block   int    `json:"block"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
	Removed bool   `json:"removed"`
}

func ev(seq, block int, text string) event {
	return event{Seq: seq, At: time.Now().UnixMilli(), Block: block, Speaker: "Alice Example", Text: text}
}

// send posts events and returns the acked count.
func (r *running) send(id string, evs ...event) int {
	r.t.Helper()
	var got struct {
		Acked int `json:"acked"`
	}
	body := map[string]any{"events": evs, "participants": []string{"Alice Example"}}
	if code := r.post("/v1/sessions/"+id+"/events", body, &got); code != 200 {
		r.t.Fatalf("events: got %d, want 200", code)
	}
	return got.Acked
}

func (r *running) stop(id string) int {
	r.t.Helper()
	return r.post("/v1/sessions/"+id+"/stop", map[string]any{}, nil)
}

// waitFor polls for path until it exists or the deadline passes.
func waitFor(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSessionRecordsAndFinishes(t *testing.T) {
	r := start(t, nil)
	c := r.create("1")
	if c.ChatNotice != "Test notice" || c.Acked != 0 || c.ID == "" {
		t.Fatalf("%+v", c)
	}
	if acked := r.send(c.ID, ev(1, 1, "Hello"), ev(2, 1, "Hello everyone")); acked != 2 {
		t.Fatalf("acked %d, want 2", acked)
	}
	if code := r.stop(c.ID); code != 200 {
		t.Fatalf("stop: got %d", code)
	}
	r.shutdown()
	for _, f := range []string{"transcript.md", "summary.md"} {
		if _, err := os.Stat(filepath.Join(c.Dir, f)); err != nil {
			t.Error(err)
		}
	}
	m, err := transcript.ReadMeeting(filepath.Join(c.Dir, "meeting.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Participants) != 1 || m.Participants[0] != "Alice Example" {
		t.Errorf("participants %v", m.Participants)
	}
}

func TestResentEventsIgnored(t *testing.T) {
	r := start(t, nil)
	c := r.create("1")
	r.send(c.ID, ev(1, 1, "Hello"), ev(2, 1, "Hello everyone"))
	if acked := r.send(c.ID, ev(1, 1, "Hello"), ev(2, 1, "Hello everyone"), ev(3, 2, "Goodbye")); acked != 3 {
		t.Fatalf("acked %d, want 3", acked)
	}
	r.stop(c.ID)
	r.shutdown()
	us, err := transcript.Read(filepath.Join(c.Dir, "transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var all []string
	for _, u := range us {
		all = append(all, u.Text)
	}
	text := strings.Join(all, " ")
	if strings.Count(text, "Hello") != 1 || strings.Count(text, "Goodbye") != 1 {
		t.Errorf("transcript %q", text)
	}
}

func TestGapStopsApplying(t *testing.T) {
	r := start(t, nil)
	c := r.create("1")
	if acked := r.send(c.ID, ev(1, 1, "Hello")); acked != 1 {
		t.Fatalf("acked %d, want 1", acked)
	}
	if acked := r.send(c.ID, ev(3, 2, "Goodbye")); acked != 1 {
		t.Fatalf("after a gap: acked %d, want 1", acked)
	}
	// Out of order in the batch, which is sorted by seq.
	if acked := r.send(c.ID, ev(3, 2, "Goodbye"), ev(2, 1, "Hello everyone")); acked != 3 {
		t.Fatalf("acked %d, want 3", acked)
	}
}

func TestSecondTabConflicts(t *testing.T) {
	r := start(t, nil)
	c := r.create("1")
	r.send(c.ID, ev(1, 1, "Hello"))
	var e struct {
		Error string `json:"error"`
	}
	if code := r.post("/v1/sessions", map[string]string{"code": "abc-defg-hij", "tab": "2"}, &e); code != 409 || e.Error == "" {
		t.Fatalf("second tab: got %d %+v, want 409", code, e)
	}
	var again created
	if code := r.post("/v1/sessions", map[string]string{"code": "abc-defg-hij", "tab": "1"}, &again); code != 200 {
		t.Fatalf("same tab: got %d, want 200", code)
	}
	if again.ID != c.ID || again.Dir != c.Dir || again.Acked != 1 || again.ChatNotice != "Test notice" {
		t.Errorf("same tab: %+v, first %+v", again, c)
	}
}

func TestIdleTimeoutFinishes(t *testing.T) {
	r := start(t, func(o *Options) { o.IdleTimeout = 100 * time.Millisecond })
	c := r.create("1")
	r.send(c.ID, ev(1, 1, "Hello"))
	waitFor(t, filepath.Join(c.Dir, "summary.md"))
	if code := r.stop(c.ID); code != 404 {
		t.Errorf("stop after idle: got %d, want 404", code)
	}
}

func TestStopTwice(t *testing.T) {
	r := start(t, nil)
	c := r.create("1")
	r.send(c.ID, ev(1, 1, "Hello"))
	if code := r.stop(c.ID); code != 200 {
		t.Fatalf("first stop: got %d, want 200", code)
	}
	if code := r.stop(c.ID); code != 404 {
		t.Fatalf("second stop: got %d, want 404", code)
	}
	r.shutdown()
	if n := r.llm.calls.Load(); n != 1 {
		t.Errorf("summarised %d times, want once", n)
	}
}

func TestUnknownSession404(t *testing.T) {
	r := start(t, nil)
	r.create("1")
	if code := r.post("/v1/sessions/nope/events", map[string]any{"events": []event{}}, nil); code != 404 {
		t.Errorf("events: got %d, want 404", code)
	}
	if code := r.stop("nope"); code != 404 {
		t.Errorf("stop: got %d, want 404", code)
	}
}

func TestBadCode400(t *testing.T) {
	r := start(t, nil)
	if code := r.post("/v1/sessions", map[string]string{"code": "../etc", "tab": "1"}, nil); code != 400 {
		t.Fatalf("got %d, want 400", code)
	}
	if ents, _ := os.ReadDir(r.out); len(ents) != 0 {
		t.Errorf("%d entries left in the output folder", len(ents))
	}
}

// A chat notice the extension could not post is logged once per session,
// however many POSTs carry it.
func TestChatErrorLoggedOnce(t *testing.T) {
	var logs logBuffer
	r := start(t, func(o *Options) { o.Logger = slog.New(slog.NewTextHandler(&logs, nil)) })
	c := r.create("1")
	body := map[string]any{"events": []event{}, "chat_error": "The chat message was not sent"}
	for range 2 {
		if code := r.post("/v1/sessions/"+c.ID+"/events", body, nil); code != 200 {
			t.Fatalf("events: got %d, want 200", code)
		}
	}
	got := logs.String()
	if n := strings.Count(got, "chat notice not posted"); n != 1 {
		t.Fatalf("logged %d times, want once:\n%s", n, got)
	}
	for _, want := range []string{"level=WARN", "code=abc-defg-hij", `error="The chat message was not sent"`} {
		if !strings.Contains(got, want) {
			t.Errorf("log lacks %s:\n%s", want, got)
		}
	}
}

func TestShutdownFinishesOpenSession(t *testing.T) {
	r := start(t, nil)
	c := r.create("1")
	r.send(c.ID, ev(1, 1, "Hello"))
	r.shutdown()
	if _, err := os.Stat(filepath.Join(c.Dir, "summary.md")); err != nil {
		t.Error(err)
	}
}

func TestEmptySessionDiscarded(t *testing.T) {
	r := start(t, nil)
	c := r.create("1")
	if code := r.stop(c.ID); code != 200 {
		t.Fatalf("stop: got %d, want 200", code)
	}
	r.shutdown()
	if ents, _ := os.ReadDir(r.out); len(ents) != 0 {
		t.Errorf("%d entries left in the output folder", len(ents))
	}
}

func TestNewSessionAfterFinishGetsNewFolder(t *testing.T) {
	r := start(t, nil)
	first := r.create("1")
	r.send(first.ID, ev(1, 1, "Hello"))
	r.stop(first.ID)
	second := r.create("1")
	if second.ID == first.ID {
		t.Fatal("the second session has the first one's id")
	}
	if want := first.Dir + "-2"; second.Dir != want {
		t.Errorf("second folder %s, want %s", second.Dir, want)
	}
	r.send(second.ID, ev(1, 1, "Hello again"))
	r.stop(second.ID)
	r.shutdown()
	for _, d := range []string{first.Dir, second.Dir} {
		if _, err := os.Stat(filepath.Join(d, "summary.md")); err != nil {
			t.Error(err)
		}
	}
}
