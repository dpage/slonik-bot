package captions

import (
	"testing"
	"time"

	"github.com/dpage/slonik-bot/internal/transcript"
)

var t0 = time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)

func at(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }

type sink struct{ got []transcript.Utterance }

func (s *sink) emit(u transcript.Utterance) { s.got = append(s.got, u) }

func TestMergeText(t *testing.T) {
	cases := []struct{ name, held, in, want string }{
		{"empty", "", "Hello", "Hello"},
		{"growth", "Hello every", "Hello everyone and welcome", "Hello everyone and welcome"},
		{"tail revision", "we should merge the patch to", "we should merge the patch today.", "we should merge the patch today."},
		{"punctuation revision", "so the plan is", "So, the plan is fine.", "So, the plan is fine."},
		{"front trim", "one two three four five six seven eight nine ten", "seven eight nine ten eleven", "one two three four five six seven eight nine ten eleven"},
		{"short block revised from first word", "Hollow there", "Hello there friends", "Hello there friends"},
		{"long block, no anchor", "a b c d e f g h i j", "x y z", "a b c d e f g h i j x y z"},
		{"tail-partial overlap", "alpha bravo charlie delta echo foxtrot golf hotel india juliet", "india juliet kilo", "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo"},
		{"repetitive speech", "yes I agree yes I agree with Bob", "agree with Bob entirely", "yes I agree yes I agree with Bob entirely"},
		{"longest overlap beats earliest", "I think so and I think so too", "I think so too, yes", "I think so and I think so too, yes"},
		{"clean continuation beats diverging tie", "so I think that we should do it and I think that", "I think that is fine", "so I think that we should do it and I think that is fine"},
		{"anchor retry, revised word", "one two three four five six seven eight nine ten", "one two tree four five six seven eight nine ten eleven", "one two tree four five six seven eight nine ten eleven"},
		{"anchor retry, front trim with revised word", "alpha bravo charlie delta echo foxtrot golf hotel", "dalta echo foxtrot golf hotel india", "alpha bravo charlie dalta echo foxtrot golf hotel india"},
		{"retry needs a full anchor", "w0 w1 w2 w3 w4 w5 w6 w7 w8 w9 w10 w11 w12 w13 w14 the w16 w17 w18 w19", "x y z the", "w0 w1 w2 w3 w4 w5 w6 w7 w8 w9 w10 w11 w12 w13 w14 the w16 w17 w18 w19 x y z the"},
		{"empty incoming keeps held", "Hello there", "  ", "Hello there"},
	}
	for _, c := range cases {
		if got := mergeText(c.held, c.in); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestAssemblerRemovalFinalises(t *testing.T) {
	var s sink
	a := NewAssembler(4*time.Second, s.emit)
	a.Observe(Event{Block: 1, Speaker: "Alice Example", Text: "Good"}, at(0))
	a.Observe(Event{Block: 1, Speaker: "Alice Example", Text: "Good morning all."}, at(1.5))
	a.Observe(Event{Block: 1, Removed: true}, at(3))
	if len(s.got) != 1 {
		t.Fatalf("got %d", len(s.got))
	}
	u := s.got[0]
	if u.Speaker != "Alice Example" || u.Text != "Good morning all." || !u.Start.Equal(at(0)) || !u.End.Equal(at(1.5)) {
		t.Fatalf("%+v", u)
	}
}

func TestAssemblerIdle(t *testing.T) {
	var s sink
	a := NewAssembler(4*time.Second, s.emit)
	a.Observe(Event{Block: 1, Speaker: "Alice Example", Text: "Still here"}, at(0))
	a.Tick(at(3.9))
	if len(s.got) != 0 {
		t.Fatal("finalised too early")
	}
	a.Tick(at(4))
	if len(s.got) != 1 {
		t.Fatal("not finalised after idle")
	}
	a.Tick(at(8))
	if len(s.got) != 1 {
		t.Fatal("a quiet block was emitted twice")
	}
	// Meet adds to the same block when the speaker resumes after a pause; the
	// new utterance holds only the words said since.
	a.Observe(Event{Block: 1, Speaker: "Alice Example", Text: "Still here and more"}, at(10))
	a.Flush(at(11))
	if len(s.got) != 2 || s.got[1].Text != "and more" || !s.got[1].Start.Equal(at(10)) {
		t.Fatalf("%+v", s.got)
	}
	// A quiet block that leaves the page has nothing more to emit.
	a.Observe(Event{Block: 2, Speaker: "Bob Example", Text: "Done"}, at(12))
	a.Tick(at(16))
	a.Observe(Event{Block: 2, Removed: true}, at(17))
	if len(s.got) != 3 {
		t.Fatalf("%+v", s.got)
	}
}

func TestAssemblerSameSpeakerNewBlock(t *testing.T) {
	var s sink
	a := NewAssembler(30*time.Second, s.emit)
	a.Observe(Event{Block: 1, Speaker: "Bob Example", Text: "First thought."}, at(0))
	a.Observe(Event{Block: 2, Speaker: "Alice Example", Text: "Interjection."}, at(1))
	a.Observe(Event{Block: 3, Speaker: "Bob Example", Text: "Second thought."}, at(2))
	if len(s.got) != 1 || s.got[0].Text != "First thought." {
		t.Fatalf("%+v", s.got)
	}
	a.Flush(at(3))
	if len(s.got) != 3 || s.got[1].Speaker != "Alice Example" || s.got[2].Text != "Second thought." {
		t.Fatalf("%+v", s.got)
	}
}

func TestAssemblerFrontTrim(t *testing.T) {
	var s sink
	a := NewAssembler(30*time.Second, s.emit)
	a.Observe(Event{Block: 7, Speaker: "Carol Example", Text: "alpha bravo charlie delta echo foxtrot golf hotel"}, at(0))
	// Meet drops the start of a long block and keeps appending.
	a.Observe(Event{Block: 7, Speaker: "Carol Example", Text: "echo foxtrot golf hotel india juliet"}, at(5))
	a.Observe(Event{Block: 7, Speaker: "Carol Example", Text: "golf hotel india juliet kilo lima."}, at(8))
	a.Flush(at(9))
	want := "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima."
	if len(s.got) != 1 || s.got[0].Text != want {
		t.Fatalf("%+v", s.got)
	}
}

func TestAssemblerIgnoresEmpty(t *testing.T) {
	var s sink
	a := NewAssembler(time.Second, s.emit)
	a.Observe(Event{Block: 1, Speaker: "Alice Example", Text: "   "}, at(0))
	a.Observe(Event{Block: 9, Removed: true}, at(1)) // unknown block
	a.Flush(at(2))
	if len(s.got) != 0 {
		t.Fatalf("%+v", s.got)
	}
}

func TestAssemblerEndIgnoresUnchangedText(t *testing.T) {
	var s sink
	a := NewAssembler(30*time.Second, s.emit)
	a.Observe(Event{Block: 1, Speaker: "Bob Example", Text: "Right then."}, at(0))
	a.Observe(Event{Block: 1, Speaker: "Bob Example", Text: "Right then."}, at(2))
	a.Flush(at(3))
	if len(s.got) != 1 || !s.got[0].End.Equal(at(0)) {
		t.Fatalf("%+v", s.got)
	}
}
