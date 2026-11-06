package record

import (
	"os"
	"path/filepath"
	"slices"
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
	if _, err := os.Stat(filepath.Join(dir, "meeting.json")); err != nil {
		t.Fatalf("meeting.json should exist straight away: %v", err)
	}
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
