package output

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dpage/slonik-bot/internal/summary"
	"github.com/dpage/slonik-bot/internal/transcript"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}

var t0 = time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)

func str(s string) *string { return &s }

func fixture() (transcript.Meeting, []transcript.Utterance, *summary.Result) {
	m := transcript.Meeting{Code: "abc-defg-hij", Started: t0, Ended: t0.Add(30 * time.Minute), Participants: []string{"Alice Example", "Bob Example", "Carol Example"}}
	us := []transcript.Utterance{
		{Speaker: "Alice Example", Start: t0.Add(5 * time.Second), End: t0.Add(8 * time.Second), Text: "The post gress meetup is full."},
		{Speaker: "Alice Example", Start: t0.Add(9 * time.Second), End: t0.Add(11 * time.Second), Text: "We need a bigger room."},
		{Speaker: "Bob Example", Start: t0.Add(65 * time.Second), End: t0.Add(70 * time.Second), Text: "I'll ask the venue."},
	}
	r := &summary.Result{
		Title:       "Meetup planning",
		Summary:     "The meetup is oversubscribed.\n\nBob will contact the venue.",
		Decisions:   []string{"Look for a bigger room"},
		ActionItems: []summary.ActionItem{{Description: "Ask the venue about a bigger room", Owner: str("Bob Example"), Due: str("Friday")}, {Description: "Update the website", Owner: nil, Due: nil}},
		Corrections: []transcript.Correction{{From: "post gress", To: "Postgres"}},
	}
	return m, us, r
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		os.WriteFile(p, []byte(got), 0o644)
	}
	want, _ := os.ReadFile(p)
	if got != string(want) {
		t.Fatalf("%s differs:\n%s", name, got)
	}
}

func TestRenderSummaryGolden(t *testing.T) {
	m, _, r := fixture()
	golden(t, "summary.golden.md", RenderSummary(m, r))
}

func TestRenderTranscriptGolden(t *testing.T) {
	m, us, r := fixture()
	got := RenderTranscript(m, us, r.Corrections)
	if !strings.Contains(got, "The Postgres meetup is full. We need a bigger room.") {
		t.Fatalf("corrections or merging missing:\n%s", got)
	}
	golden(t, "transcript.golden.md", got)
}

func TestRenderEscapesNames(t *testing.T) {
	m, us, r := fixture()
	m.Participants = []string{"Bob *Example*", "O'Brien"}
	us[0].Speaker = "Bob *Example*"
	r.ActionItems[0].Owner = str("Bob *Example*")
	s := RenderSummary(m, r)
	if !strings.Contains(s, `Bob \*Example\*`) || strings.Contains(s, "Bob *Example*") {
		t.Fatalf("not escaped:\n%s", s)
	}
	if !strings.Contains(s, "O'Brien") {
		t.Fatal("apostrophe mangled")
	}
	if tr := RenderTranscript(m, us, nil); !strings.Contains(tr, `Bob \*Example\*`) {
		t.Fatalf("transcript not escaped:\n%s", tr)
	}
}

// Caption text is whatever people said, so it must not turn into Markdown
// formatting, links or HTML when transcript.md is rendered.
func TestRenderTranscriptEscapesText(t *testing.T) {
	m, us, _ := fixture()
	us = us[:1]
	us[0].Text = "Use *all* the `psql` _flags_, see [docs](http://example.org) <b>now</b> \\o/\nand # more"
	got := RenderTranscript(m, us, nil)
	want := "Use \\*all\\* the \\`psql\\` \\_flags\\_, see \\[docs\\](http://example.org) \\<b\\>now\\</b\\> \\\\o/ and # more"
	if !strings.Contains(got, want) {
		t.Fatalf("want %s in:\n%s", want, got)
	}
	us[0].Text = "# not a heading"
	if got := RenderTranscript(m, us, nil); !strings.Contains(got, `\# not a heading`) {
		t.Fatalf("leading # not escaped:\n%s", got)
	}
}

func TestEmptyListsAndDir(t *testing.T) {
	m, _, r := fixture()
	r.Decisions, r.ActionItems = nil, nil
	s := RenderSummary(m, r)
	if strings.Count(s, "_None recorded._") != 2 {
		t.Fatalf("empty sections not marked:\n%s", s)
	}
	if d := Dir("/notes", "abc-defg-hij", t0); d != filepath.Join("/notes", "2026-10-30-abc-defg-hij") {
		t.Fatal(d)
	}
}

// Joining the same meeting twice in a day must never overwrite the first
// session's notes.
func TestNewDirNeverReusesAFolder(t *testing.T) {
	base := filepath.Join(t.TempDir(), "notes")
	want := []string{"2026-10-30-abc-defg-hij", "2026-10-30-abc-defg-hij-2", "2026-10-30-abc-defg-hij-3"}
	for _, w := range want {
		d, err := NewDir(base, "abc-defg-hij", t0)
		if err != nil {
			t.Fatal(err)
		}
		if d != filepath.Join(base, w) {
			t.Fatalf("got %s, want %s", d, w)
		}
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v", d, fi, err)
		}
	}
}

func TestWriteFilesWithoutSummary(t *testing.T) {
	m, us, _ := fixture()
	dir := t.TempDir()
	if err := WriteFiles(dir, m, us, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "transcript.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "summary.md")); !os.IsNotExist(err) {
		t.Fatal("summary.md written without a summary")
	}
}
