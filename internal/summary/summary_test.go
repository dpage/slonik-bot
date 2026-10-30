package summary

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dpage/slonik-bot/internal/transcript"
)

var update = flag.Bool("update", false, "rewrite golden files")

var t0 = time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)

func fixture() (transcript.Meeting, []transcript.Utterance) {
	m := transcript.Meeting{Code: "abc-defg-hij", Started: t0, Ended: t0.Add(30 * time.Minute), Participants: []string{"Carol Example", "Alice Example"}}
	us := []transcript.Utterance{
		{Speaker: "Alice Example", Start: t0.Add(5 * time.Second), End: t0.Add(9 * time.Second), Text: "Let's look at the post gress conference schedule."},
		{Speaker: "Bob Example", Start: t0.Add(12 * time.Second), End: t0.Add(15 * time.Second), Text: "I'll email the venue by Friday."},
	}
	return m, us
}

func TestBuildPromptGolden(t *testing.T) {
	m, us := fixture()
	sys, user := BuildPrompt(m, us)
	if !strings.Contains(sys, "Glossary:") || !strings.Contains(sys, "PostgreSQL") {
		t.Fatal("system prompt lacks glossary")
	}
	if strings.Contains(sys, "abc-defg-hij") {
		t.Fatal("variable content leaked into the system prompt, which defeats caching")
	}
	got := []byte(user)
	if *update {
		os.WriteFile("testdata/prompt.golden", got, 0o644)
	}
	want, _ := os.ReadFile("testdata/prompt.golden")
	if string(got) != string(want) {
		t.Fatalf("user prompt differs from golden:\n%s", got)
	}
	if !strings.Contains(user, "Participants: Alice Example, Bob Example, Carol Example") {
		t.Fatalf("participants line wrong:\n%s", user)
	}
}

func TestSchemaIsStrict(t *testing.T) {
	var walk func(any)
	walk = func(n any) {
		m, ok := n.(map[string]any)
		if !ok {
			return
		}
		if m["type"] == "object" {
			if m["additionalProperties"] != false {
				t.Errorf("object without additionalProperties:false: %v", m)
			}
			props, _ := m["properties"].(map[string]any)
			req, _ := m["required"].([]string)
			if len(req) != len(props) {
				t.Errorf("not every property required: %v", m)
			}
		}
		for _, v := range m {
			switch x := v.(type) {
			case map[string]any:
				walk(x)
			case []any:
				for _, e := range x {
					walk(e)
				}
			}
		}
	}
	walk(Schema())
	if _, err := json.Marshal(Schema()); err != nil {
		t.Fatal(err)
	}
}

const sample = `{"title":"Conference planning","summary":"We discussed the schedule.","decisions":["Use the main hall"],"action_items":[{"description":"Email the venue","owner":"Bob Example","due":"Friday"},{"description":"Book catering","owner":null,"due":null}],"corrections":[{"from":"post gress","to":"Postgres"}]}`

func TestParseResult(t *testing.T) {
	r, err := ParseResult(sample)
	if err != nil {
		t.Fatal(err)
	}
	if r.Title != "Conference planning" || len(r.ActionItems) != 2 || *r.ActionItems[0].Owner != "Bob Example" {
		t.Fatalf("%+v", r)
	}
}

func TestParseResultNullOwner(t *testing.T) {
	r, err := ParseResult(sample)
	if err != nil || r.ActionItems[1].Owner != nil || r.ActionItems[1].Due != nil {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestParseResultFenced(t *testing.T) {
	r, err := ParseResult("Here you go:\n```json\n" + sample + "\n```\n")
	if err != nil || r.Title != "Conference planning" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestParseResultRejectsEmptyTitle(t *testing.T) {
	if _, err := ParseResult(`{"title":"","summary":"x","decisions":[],"action_items":[],"corrections":[]}`); err == nil {
		t.Fatal("empty title accepted")
	}
	if _, err := ParseResult(`not json`); err == nil {
		t.Fatal("garbage accepted")
	}
}

type fakeLLM struct {
	out    string
	err    error
	system string
}

func (f *fakeLLM) Complete(_ context.Context, system, _ string, _ map[string]any) (string, error) {
	f.system = system
	return f.out, f.err
}

func TestSummarise(t *testing.T) {
	m, us := fixture()
	r, err := Summarise(context.Background(), &fakeLLM{out: sample}, m, us)
	if err != nil || r.Decisions[0] != "Use the main hall" {
		t.Fatalf("%+v %v", r, err)
	}
	_, err = Summarise(context.Background(), &fakeLLM{err: errors.New("boom")}, m, us)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error not propagated: %v", err)
	}
	if _, err := Summarise(context.Background(), &fakeLLM{out: sample}, m, nil); err == nil {
		t.Fatal("empty transcript should be an error, not an LLM call")
	}
}
