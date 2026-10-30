package bot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dpage/slonik-bot/internal/captions"
	"github.com/dpage/slonik-bot/internal/config"
	"github.com/dpage/slonik-bot/internal/meet"
	"github.com/dpage/slonik-bot/internal/transcript"
)

const sampleResult = `{"title":"Meetup planning","summary":"We planned.","decisions":[],"action_items":[{"description":"Email the venue","owner":"Bob Example","due":null}],"corrections":[]}`

type fakeCall struct {
	mu           sync.Mutex
	script       []captions.Event
	endAfter     time.Duration // 0: never ends by itself
	participants []string
	captionsErr  error
	chatErr      error
	statusErr    error // returned by every Status call
	statusHang   bool  // Status blocks until its context ends
	start        time.Time
	chat, lang   string
	left, closed bool
}

func (f *fakeCall) Join(context.Context) error { f.start = time.Now(); return nil }
func (f *fakeCall) StartCaptions(_ context.Context, lang string, ev chan<- captions.Event) error {
	if f.captionsErr != nil {
		return f.captionsErr
	}
	f.lang = lang
	go func() {
		for _, e := range f.script {
			ev <- e
			time.Sleep(5 * time.Millisecond)
		}
	}()
	return nil
}
func (f *fakeCall) SendChat(_ context.Context, m string) error { f.chat = m; return f.chatErr }
func (f *fakeCall) Participants(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.participants, nil
}
func (f *fakeCall) Status(ctx context.Context) (meet.Status, error) {
	if f.statusHang {
		<-ctx.Done()
		return meet.InCall, ctx.Err()
	}
	if f.statusErr != nil {
		return meet.InCall, f.statusErr
	}
	if f.endAfter > 0 && time.Since(f.start) > f.endAfter {
		return meet.Ended, nil
	}
	return meet.InCall, nil
}
func (f *fakeCall) Leave(context.Context) error { f.left = true; return nil }
func (f *fakeCall) Close() error                { f.closed = true; return nil }

type fakeLLM struct {
	out    string
	err    error
	ctxErr error
}

func (l *fakeLLM) Complete(ctx context.Context, _, _ string, _ map[string]any) (string, error) {
	l.ctxErr = ctx.Err()
	return l.out, l.err
}

var script = []captions.Event{
	{Block: 1, Speaker: "Alice Example", Text: "Shall we"},
	{Block: 1, Speaker: "Alice Example", Text: "Shall we start?"},
	{Block: 2, Speaker: "Bob Example", Text: "I'll email the venue."},
	{Block: 1, Removed: true},
	{Block: 2, Removed: true},
}

func opts(t *testing.T) Options {
	c := config.Default()
	c.AloneTimeout = time.Hour
	return Options{Config: c, Code: "abc-defg-hij", OutDir: t.TempDir(), Poll: 10 * time.Millisecond, Idle: 50 * time.Millisecond}
}

func dial(c *fakeCall) func(context.Context) (Call, error) {
	return func(context.Context) (Call, error) { return c, nil }
}

func TestRunWritesEverything(t *testing.T) {
	o := opts(t)
	call := &fakeCall{script: script, endAfter: 200 * time.Millisecond, participants: []string{"Alice Example", "Bob Example"}}
	out, err := Run(context.Background(), o, Deps{Dial: dial(call), LLM: &fakeLLM{out: sampleResult}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Reason != "ended" || out.Utterances != 2 || !out.Summarised {
		t.Fatalf("%+v", out)
	}
	if call.chat != o.Config.ChatNotice || call.lang != "English" || call.left || !call.closed {
		t.Fatalf("call %+v", call)
	}
	us, _ := transcript.Read(filepath.Join(o.OutDir, "transcript.jsonl"))
	if len(us) != 2 || us[0].Text != "Shall we start?" {
		t.Fatalf("%+v", us)
	}
	s, _ := os.ReadFile(filepath.Join(o.OutDir, "summary.md"))
	if !strings.Contains(string(s), "# Meetup planning") || !strings.Contains(string(s), "Bob Example") {
		t.Fatalf("summary:\n%s", s)
	}
	m, _ := transcript.ReadMeeting(filepath.Join(o.OutDir, "meeting.json"))
	if m.Ended.IsZero() || len(m.Participants) != 2 {
		t.Fatalf("%+v", m)
	}
}

func TestRunSummaryFailureKeepsTranscript(t *testing.T) {
	o := opts(t)
	call := &fakeCall{script: script, endAfter: 200 * time.Millisecond, participants: []string{"Alice Example"}}
	_, err := Run(context.Background(), o, Deps{Dial: dial(call), LLM: &fakeLLM{err: errors.New("overloaded")}})
	if err == nil || !strings.Contains(err.Error(), "slonik-bot summarise") || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(o.OutDir, "transcript.md")); err != nil {
		t.Fatal("transcript.md missing after a summary failure")
	}
}

func TestRunAlone(t *testing.T) {
	o := opts(t)
	o.Config.AloneTimeout = 50 * time.Millisecond
	call := &fakeCall{script: script}
	out, _ := Run(context.Background(), o, Deps{Dial: dial(call), LLM: &fakeLLM{out: sampleResult}})
	if out.Reason != "alone" || !call.left {
		t.Fatalf("%+v left=%v", out, call.left)
	}
}

func TestRunMaxDuration(t *testing.T) {
	o := opts(t)
	o.Config.MaxDuration = 150 * time.Millisecond
	call := &fakeCall{script: script, participants: []string{"Alice Example"}}
	out, _ := Run(context.Background(), o, Deps{Dial: dial(call), LLM: &fakeLLM{out: sampleResult}})
	if out.Reason != "max duration" || !call.left {
		t.Fatalf("%+v", out)
	}
}

func TestRunInterruptedStillSummarises(t *testing.T) {
	o := opts(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(150*time.Millisecond, cancel)
	call := &fakeCall{script: script, participants: []string{"Alice Example"}}
	llm := &fakeLLM{out: sampleResult}
	out, err := Run(ctx, o, Deps{Dial: dial(call), LLM: llm})
	if err != nil || out.Reason != "interrupted" || !out.Summarised || !call.left {
		t.Fatalf("%+v %v", out, err)
	}
	if llm.ctxErr != nil {
		t.Fatal("summary ran with a cancelled context")
	}
}

func TestRunCaptionsFailureIsFatal(t *testing.T) {
	o := opts(t)
	call := &fakeCall{captionsErr: errors.New("captions button not found")}
	_, err := Run(context.Background(), o, Deps{Dial: dial(call)})
	if err == nil || !strings.Contains(err.Error(), "captions button not found") || !call.left || !call.closed {
		t.Fatalf("%v %+v", err, call)
	}
}

func TestRunChatFailureIsOnlyAWarning(t *testing.T) {
	o := opts(t)
	o.NoSummary = true
	call := &fakeCall{script: script, endAfter: 200 * time.Millisecond, participants: []string{"Alice Example"}, chatErr: errors.New("chat panel not found")}
	out, err := Run(context.Background(), o, Deps{Dial: dial(call)})
	if err != nil || out.Utterances != 2 || out.Summarised {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestRunNoCaptions(t *testing.T) {
	o := opts(t)
	call := &fakeCall{endAfter: 50 * time.Millisecond, participants: []string{"Alice Example"}}
	if _, err := Run(context.Background(), o, Deps{Dial: dial(call), LLM: &fakeLLM{out: sampleResult}}); err == nil || !strings.Contains(err.Error(), "no captions") {
		t.Fatalf("%v", err)
	}
}

func TestSummariseFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "transcript.jsonl")
	w, _ := transcript.Create(p)
	now := time.Now()
	w.Append(transcript.Utterance{Speaker: "Alice Example", Start: now, End: now, Text: "Hello."})
	w.Close()
	if err := SummariseFile(context.Background(), p, "", &fakeLLM{out: sampleResult}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "summary.md")); err != nil {
		t.Fatal(err)
	}
}

// Meet can show a head count without names; those people still count as
// company, but no empty name reaches the meeting's participants.
func TestRunUnnamedParticipantsAreNotAlone(t *testing.T) {
	o := opts(t)
	o.Config.AloneTimeout = 50 * time.Millisecond
	o.NoSummary = true
	call := &fakeCall{script: script, endAfter: 300 * time.Millisecond, participants: []string{"", ""}}
	out, err := Run(context.Background(), o, Deps{Dial: dial(call)})
	if err != nil || out.Reason != "ended" {
		t.Fatalf("%+v %v", out, err)
	}
	m, _ := transcript.ReadMeeting(filepath.Join(o.OutDir, "meeting.json"))
	if strings.Join(m.Participants, ",") != "Alice Example,Bob Example" {
		t.Fatalf("participants %q", m.Participants)
	}
}

// chromedp reports a lost browser as an error wrapping context.Canceled even
// though the caller's context is still live.
func TestRunBrowserClosed(t *testing.T) {
	o := opts(t)
	o.NoSummary = true
	call := &fakeCall{script: script, participants: []string{"Alice Example"}, statusErr: fmt.Errorf("lost connection: %w", context.Canceled)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	out, _ := Run(ctx, o, Deps{Dial: dial(call)})
	if out.Reason != "browser closed" || call.left || time.Since(start) > time.Second {
		t.Fatalf("%+v left=%v after %s", out, call.left, time.Since(start))
	}
}

// A page that stops answering must not stop the bot from leaving on time.
func TestRunMaxDurationWhilstStatusHangs(t *testing.T) {
	o := opts(t)
	o.NoSummary = true
	o.Config.MaxDuration = 150 * time.Millisecond
	call := &fakeCall{script: script, participants: []string{"Alice Example"}, statusHang: true}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	out, _ := Run(ctx, o, Deps{Dial: dial(call)})
	if out.Reason != "max duration" || !call.left || time.Since(start) > time.Second {
		t.Fatalf("%+v left=%v after %s", out, call.left, time.Since(start))
	}
}

func TestSummariseFileRetryHintNamesOutDir(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "transcript.jsonl")
	w, err := transcript.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	w.Append(transcript.Utterance{Speaker: "Alice Example", Start: now, End: now, Text: "Hello."})
	w.Close()

	out := filepath.Join(t.TempDir(), "elsewhere")
	err = SummariseFile(context.Background(), p, out, &fakeLLM{err: errors.New("overloaded")})
	if err == nil || !strings.Contains(err.Error(), "slonik-bot summarise --out "+out+" "+p) {
		t.Fatalf("%v", err)
	}
	err = SummariseFile(context.Background(), p, "", &fakeLLM{err: errors.New("overloaded")})
	if err == nil || strings.Contains(err.Error(), "--out") || !strings.Contains(err.Error(), "slonik-bot summarise "+p) {
		t.Fatalf("%v", err)
	}
}
