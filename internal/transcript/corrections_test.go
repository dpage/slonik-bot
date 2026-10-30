package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyCorrectionsBoundaries(t *testing.T) {
	pgd := []Correction{{From: "PG dump", To: "pg_dump"}}
	tests := []struct {
		name string
		in   string
		cs   []Correction
		want string
	}{
		{"adjacent sharing a space", "PG dump PG dump", pgd, "pg_dump pg_dump"},
		{"at start", "PG dump now", pgd, "pg_dump now"},
		{"at end", "run PG dump", pgd, "run pg_dump"},
		{"whole string", "PG dump", pgd, "pg_dump"},
		{"To contains From", "use pg now", []Correction{{From: "pg", To: "pg_dump"}}, "use pg_dump now"},
		{"non-ASCII neighbour", "éPG dump", pgd, "éPG dump"},
		{"non-ASCII From", "Hi Søren, Søren.", []Correction{{From: "Søren", To: "Soren"}}, "Hi Soren, Soren."},
		{"underscore before", "_PG dump", pgd, "_PG dump"},
		{"digit after", "PG dump2", pgd, "PG dump2"},
		{"sequence", "post gress and PG dump", []Correction{{From: "post gress", To: "Postgres"}, {From: "PG dump", To: "pg_dump"}}, "Postgres and pg_dump"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ApplyCorrections(tc.in, tc.cs); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestCreateDropsTruncatedFragment(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	w, _ := Create(p)
	w.Append(u("Alice Example", 0, "First."))
	w.Close()
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"speaker":"Bob Ex`)
	f.Close()
	w, err := Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w.Append(u("Bob Example", 5, "Second."))
	w.Close()
	got, err := Read(p)
	if err != nil || len(got) != 2 || got[1].Text != "Second." {
		t.Fatalf("got %+v, err %v", got, err)
	}
}
