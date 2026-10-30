package meet

import (
	"testing"
	"time"

	"github.com/dpage/slonik-bot/internal/captions"
	"github.com/dpage/slonik-bot/internal/meet/fakemeet"
)

// collect reads events until want returns true or ten seconds pass.
func collect(t *testing.T, events <-chan captions.Event, want func([]captions.Event) bool) []captions.Event {
	t.Helper()
	var got []captions.Event
	deadline := time.After(10 * time.Second)
	for !want(got) {
		select {
		case ev := <-events:
			got = append(got, ev)
		case <-deadline:
			t.Fatalf("timed out; got %+v", got)
		}
	}
	return got
}

func removals(n int) func([]captions.Event) bool {
	return func(evs []captions.Event) bool {
		c := 0
		for _, e := range evs {
			if e.Removed {
				c++
			}
		}
		return c >= n
	}
}

func checkFlow(t *testing.T, sc fakemeet.Scenario) {
	s, srv := joined(t, sc)
	events := make(chan captions.Event, 64)
	if err := s.StartCaptions(ctxT(t), "English", events); err != nil {
		t.Fatal(err)
	}
	if !srv.CaptionsOn() {
		t.Fatal("captions not turned on")
	}
	if srv.Language() != "" {
		t.Fatal("opened the settings for English")
	}
	srv.Caption(1, "Alice Example", "Shall we")
	srv.Caption(1, "Alice Example", "Shall we start?")
	srv.Caption(2, "Bob Example", "Yes.")
	srv.RemoveCaption(1)
	srv.RemoveCaption(2)
	evs := collect(t, events, removals(2))

	var alice, bob int
	var aliceLast string
	for _, e := range evs {
		switch {
		case e.Removed:
		case e.Speaker == "Alice Example":
			if alice != 0 && alice != e.Block {
				t.Fatalf("Alice's block id changed: %+v", evs)
			}
			alice, aliceLast = e.Block, e.Text
		case e.Speaker == "Bob Example":
			bob = e.Block
		default:
			t.Fatalf("unexpected event %+v", e)
		}
	}
	if alice == 0 || bob == 0 || alice == bob || aliceLast != "Shall we start?" {
		t.Fatalf("%+v", evs)
	}
}

func TestCaptionsWithMeetClasses(t *testing.T) {
	checkFlow(t, fakemeet.Scenario{})
}

func TestCaptionsStructuralFallback(t *testing.T) {
	checkFlow(t, fakemeet.Scenario{PlainCaptions: true})
}

func TestCaptionsMenuButton(t *testing.T) {
	checkFlow(t, fakemeet.Scenario{CaptionsMenu: true})
}

func TestCaptionsSurviveRegionReplacement(t *testing.T) {
	s, srv := joined(t, fakemeet.Scenario{})
	events := make(chan captions.Event, 64)
	if err := s.StartCaptions(ctxT(t), "", events); err != nil {
		t.Fatal(err)
	}
	srv.ReplaceCaptionRegion()
	time.Sleep(3 * time.Second) // longer than the observer's two-second re-attach
	srv.Caption(3, "Carol Example", "Still here.")
	collect(t, events, func(evs []captions.Event) bool {
		for _, e := range evs {
			if e.Speaker == "Carol Example" && e.Text == "Still here." {
				return true
			}
		}
		return false
	})
}

func TestCaptionLanguage(t *testing.T) {
	s, srv := joined(t, fakemeet.Scenario{})
	if err := s.StartCaptions(ctxT(t), "French", make(chan captions.Event, 64)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "language", func() bool { return srv.Language() == "French" })
}

func TestStartCaptionsTwiceDoesNotDoubleReport(t *testing.T) {
	s, srv := joined(t, fakemeet.Scenario{})
	events := make(chan captions.Event, 64)
	ctx := ctxT(t)
	if err := s.StartCaptions(ctx, "", events); err != nil {
		t.Fatal(err)
	}
	if err := s.StartCaptions(ctx, "", events); err != nil {
		t.Fatal(err)
	}
	srv.Caption(1, "Alice Example", "Once.")
	srv.RemoveCaption(1)
	evs := collect(t, events, removals(1))
	time.Sleep(500 * time.Millisecond)
	if len(evs)+len(events) != 2 {
		t.Fatalf("expected one update and one removal, got %+v plus %d queued", evs, len(events))
	}
}
