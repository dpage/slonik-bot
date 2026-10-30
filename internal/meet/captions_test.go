package meet

import (
	"maps"
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
	// The fake page reports over an asynchronous fetch.
	eventually(t, "captions on", srv.CaptionsOn)
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

func TestCaptionsSurviveReload(t *testing.T) {
	s, srv := joined(t, fakemeet.Scenario{})
	events := make(chan captions.Event, 64)
	ctx := ctxT(t)
	if err := s.StartCaptions(ctx, "", events); err != nil {
		t.Fatal(err)
	}
	// Joining again loads a new document, without the observer that
	// StartCaptions evaluated in the old one.
	if err := s.Join(ctx); err != nil {
		t.Fatal(err)
	}
	srv.Caption(1, "Alice Example", "After the reload.")
	srv.RemoveCaption(1)
	checkFinal(t, collect(t, events, removals(1)), "After the reload.")
	time.Sleep(500 * time.Millisecond)
	if len(events) != 0 {
		t.Fatalf("%d further events queued", len(events))
	}
}

// settled waits for evs to satisfy want, then for half a second more, and
// returns everything that arrived.
func settled(t *testing.T, events <-chan captions.Event, want func([]captions.Event) bool) []captions.Event {
	t.Helper()
	evs := collect(t, events, want)
	time.Sleep(500 * time.Millisecond)
	for len(events) > 0 {
		evs = append(evs, <-events)
	}
	return evs
}

// checkTexts checks that evs hold exactly the given texts, by speaker, and
// a removal for each of their blocks.
func checkTexts(t *testing.T, evs []captions.Event, want map[string]string) {
	t.Helper()
	got := map[string]string{}
	blocks := map[int]bool{}
	removed := map[int]bool{}
	for _, e := range evs {
		if e.Removed {
			if removed[e.Block] {
				t.Fatalf("block %d removed twice: %+v", e.Block, evs)
			}
			removed[e.Block] = true
			continue
		}
		if _, dup := got[e.Speaker]; dup || blocks[e.Block] {
			t.Fatalf("more than one text event for %q: %+v", e.Speaker, evs)
		}
		got[e.Speaker], blocks[e.Block] = e.Text, true
	}
	if !maps.Equal(got, want) || !maps.Equal(blocks, removed) {
		t.Fatalf("want texts %v, each removed once; got %+v", want, evs)
	}
}

func TestCaptionWrapperRemovedWithTrackedBlocks(t *testing.T) {
	srv, events := startedFeed(t, fakemeet.Scenario{})
	srv.Caption(1, "Alice Example", "One.")
	srv.Caption(2, "Bob Example", "Two.")
	srv.WrapCaptions()
	srv.RemoveCaptionWrapper()
	checkTexts(t, settled(t, events, removals(2)), map[string]string{
		"Alice Example": "One.",
		"Bob Example":   "Two.",
	})
}

func TestCaptionWrapperRemovedWithUnseenBlocks(t *testing.T) {
	srv, events := startedFeed(t, fakemeet.Scenario{})
	srv.WrapCaptions()
	srv.InOneTask(func() {
		srv.Caption(1, "Alice Example", "One.")
		srv.Caption(2, "Bob Example", "Two.")
		srv.RemoveCaptionWrapper()
	})
	checkTexts(t, settled(t, events, removals(2)), map[string]string{
		"Alice Example": "One.",
		"Bob Example":   "Two.",
	})
}

// startedFeed joins the fake call and starts captions.
func startedFeed(t *testing.T, sc fakemeet.Scenario) (*fakemeet.Server, chan captions.Event) {
	t.Helper()
	s, srv := joined(t, sc)
	events := make(chan captions.Event, 64)
	if err := s.StartCaptions(ctxT(t), "", events); err != nil {
		t.Fatal(err)
	}
	return srv, events
}

// checkFinal checks that evs are the text of one block followed by its
// removal.
func checkFinal(t *testing.T, evs []captions.Event, text string) {
	t.Helper()
	if len(evs) != 2 || evs[0].Removed || evs[0].Speaker != "Alice Example" || evs[0].Text != text ||
		!evs[1].Removed || evs[1].Block != evs[0].Block {
		t.Fatalf("want %q then its removal, got %+v", text, evs)
	}
}

func checkAddedAndRemoved(t *testing.T, sc fakemeet.Scenario) {
	srv, events := startedFeed(t, sc)
	srv.InOneTask(func() {
		srv.Caption(1, "Alice Example", "Gone at once.")
		srv.RemoveCaption(1)
	})
	checkFinal(t, collect(t, events, removals(1)), "Gone at once.")
}

func TestCaptionAddedAndRemovedInOneTask(t *testing.T) {
	checkAddedAndRemoved(t, fakemeet.Scenario{})
}

func TestCaptionAddedAndRemovedInOneTaskStructural(t *testing.T) {
	checkAddedAndRemoved(t, fakemeet.Scenario{PlainCaptions: true})
}

func TestCaptionUpdatedAndRemovedInOneTask(t *testing.T) {
	srv, events := startedFeed(t, fakemeet.Scenario{})
	srv.Caption(1, "Alice Example", "Shall we")
	first := collect(t, events, func(evs []captions.Event) bool { return len(evs) == 1 })
	if first[0].Text != "Shall we" {
		t.Fatalf("%+v", first)
	}
	srv.InOneTask(func() {
		srv.Caption(1, "Alice Example", "Shall we start?")
		srv.RemoveCaption(1)
	})
	evs := collect(t, events, removals(1))
	checkFinal(t, evs, "Shall we start?")
	if evs[0].Block != first[0].Block {
		t.Fatalf("block id changed: %+v then %+v", first, evs)
	}
}
