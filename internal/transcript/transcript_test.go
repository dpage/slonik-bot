package transcript

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)

func u(sp string, sec int, text string) Utterance {
	return Utterance{Speaker: sp, Start: t0.Add(time.Duration(sec) * time.Second), End: t0.Add(time.Duration(sec+2) * time.Second), Text: text}
}

func TestWriteReadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	w, err := Create(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []Utterance{u("Alice Example", 0, "Hello."), u("Bob Example", 5, "Hi Alice.")}
	for _, x := range want {
		if err := w.Append(x); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	got, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestReadToleratesTruncatedLastLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	w, _ := Create(p)
	w.Append(u("Alice Example", 0, "Complete."))
	w.Close()
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"speaker":"Bob Ex`)
	f.Close()
	got, err := Read(p)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d utterances, err %v", len(got), err)
	}
}

func TestReadRejectsCorruptMiddleLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(p, []byte("not json\n{\"speaker\":\"A\",\"text\":\"x\"}\n"), 0o600)
	if _, err := Read(p); err == nil {
		t.Fatal("corrupt line accepted")
	}
}

func TestFormat(t *testing.T) {
	got := Format([]Utterance{u("Alice Example", 0, "Hello."), u("Bob Example", 3725, "Late.")}, t0)
	want := "[00:00:00] Alice Example: Hello.\n[01:02:05] Bob Example: Late.\n"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestMergeAndSpeakers(t *testing.T) {
	in := []Utterance{u("Alice Example", 0, "One."), u("Alice Example", 3, "Two."), u("Bob Example", 6, "Three."), u("Alice Example", 9, "Four.")}
	m := Merge(in)
	if len(m) != 3 || m[0].Text != "One. Two." || m[0].Start != in[0].Start || m[0].End != in[1].End {
		t.Fatalf("merge %+v", m)
	}
	if s := Speakers(in); !reflect.DeepEqual(s, []string{"Alice Example", "Bob Example"}) {
		t.Fatalf("speakers %v", s)
	}
}

func TestApplyCorrections(t *testing.T) {
	cs := []Correction{{From: "post gress", To: "Postgres"}, {From: "PG dump", To: "pg_dump"}, {From: "", To: "x"}}
	got := ApplyCorrections("We run post gress and PG dump, not postgresql or PG dumper.", cs)
	want := "We run Postgres and pg_dump, not postgresql or PG dumper."
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestMeetingRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "meeting.json")
	m := Meeting{Code: "abc-defg-hij", Started: t0, Ended: t0.Add(time.Hour), Participants: []string{"Alice Example"}}
	if err := WriteMeeting(p, m); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMeeting(p)
	if err != nil || !reflect.DeepEqual(got, m) {
		t.Fatalf("%+v %v", got, err)
	}
}
