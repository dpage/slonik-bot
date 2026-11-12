package record

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dpage/slonik-bot/internal/captions"
	"github.com/dpage/slonik-bot/internal/transcript"
)

var t0 = time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)

func TestRecordingWritesAsItGoes(t *testing.T) {
	dir := t.TempDir()
	r, err := Start(dir, "abc-defg-hij", t0, 4*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Discard()
	r.Observe(captions.Event{Block: 1, Speaker: "Alice Example", Text: "Hello there"}, t0.Add(time.Second))
	r.Tick(t0.Add(10 * time.Second))
	us, err := transcript.Read(r.JSONL())
	if err != nil {
		t.Fatal(err)
	}
	if len(us) != 1 || us[0].Speaker != "Alice Example" {
		t.Fatalf("got %+v", us)
	}
	if r.Utterances() != 1 || r.Dir() != dir {
		t.Fatalf("Utterances=%d Dir=%s", r.Utterances(), r.Dir())
	}
}

// Start leaves meeting.json alone, so that a bot whose join fails does not
// replace an earlier run's; SetStarted writes it.
func TestRecordingSetStartedWritesMeeting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meeting.json")
	earlier := transcript.Meeting{Code: "abc-defg-hij", Started: t0.Add(-time.Hour)}
	if err := transcript.WriteMeeting(path, earlier); err != nil {
		t.Fatal(err)
	}
	r, err := Start(dir, "abc-defg-hij", t0, 4*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if m, err := transcript.ReadMeeting(path); err != nil || !m.Started.Equal(earlier.Started) {
		t.Fatalf("after Start, meeting.json = %+v, %v; want it unchanged", m, err)
	}
	r.SetStarted(t0)
	m, err := transcript.ReadMeeting(path)
	if err != nil || m.Code != "abc-defg-hij" || !m.Started.Equal(t0) {
		t.Fatalf("after SetStarted, meeting.json = %+v, %v", m, err)
	}
}

func TestRecordingStopMergesParticipants(t *testing.T) {
	dir := t.TempDir()
	r, err := Start(dir, "abc-defg-hij", t0, 4*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.AddParticipants("Bob Example", "")
	r.Observe(captions.Event{Block: 1, Speaker: "Alice Example", Text: "Hello there"}, t0.Add(time.Second))
	end := t0.Add(time.Minute)
	m, us := r.Stop(end)
	if want := []string{"Alice Example", "Bob Example"}; !slices.Equal(m.Participants, want) {
		t.Fatalf("participants = %v, want %v", m.Participants, want)
	}
	if !m.Ended.Equal(end) || len(us) != 1 {
		t.Fatalf("ended=%v utterances=%d", m.Ended, len(us))
	}
	got, err := transcript.ReadMeeting(filepath.Join(dir, "meeting.json"))
	if err != nil || !slices.Equal(got.Participants, m.Participants) {
		t.Fatalf("meeting.json = %+v, %v", got, err)
	}
	m2, us2 := r.Stop(end.Add(time.Hour))
	if !m2.Ended.Equal(m.Ended) || !slices.Equal(m2.Participants, m.Participants) || len(us2) != len(us) {
		t.Fatalf("second Stop differs: %+v %d", m2, len(us2))
	}
}

func TestRecordingDiscardRemovesEmptyFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "notes")
	r, err := Start(dir, "abc-defg-hij", t0, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Discard()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("folder should be gone: %v", err)
	}
}

type replyLLM string

func (r replyLLM) Complete(context.Context, string, string, map[string]any) (string, error) {
	return string(r), nil
}

// A reply that cannot be used is kept beside the notes, so that the failure
// can be looked into, and the error says where it is.
func TestFinishKeepsUnusableReply(t *testing.T) {
	dir := t.TempDir()
	us := []transcript.Utterance{{Speaker: "Alice Example", Start: t0, End: t0.Add(time.Second), Text: "Hello"}}
	m := transcript.Meeting{Code: "abc-defg-hij", Started: t0, Ended: t0.Add(time.Minute)}
	const reply = `{"title":"x","summary":"Hello."}`
	ok, err := Finish(context.Background(), dir, filepath.Join(dir, "transcript.jsonl"), m, us, replyLLM(reply))
	path := filepath.Join(dir, "summary-reply.txt")
	if ok || err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != reply {
		t.Fatalf("saved reply %q, %v", b, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/home/jane/notes/2026-10-30-abc-defg-hij": "/home/jane/notes/2026-10-30-abc-defg-hij",
		"/tmp/a+b,d@e:f": "/tmp/a+b,d@e:f",
		"a=~x":           "'a=~x'",   // bash expands ~ after =
		"=notes":         "'=notes'", // zsh expands a leading =
		"/tmp/my notes":  "'/tmp/my notes'",
		"/tmp/Bob's":     `'/tmp/Bob'\''s'`,
		"/tmp/$HOME":     "'/tmp/$HOME'",
		"~jane/notes":    "'~jane/notes'", // a leading ~ would be expanded
		"":               "''",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
