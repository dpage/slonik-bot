package meet

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/dpage/slonik-bot/internal/browser"
	"github.com/dpage/slonik-bot/internal/meet/fakemeet"
)

const botName = "Slonik Bot (transcribing)"

func session(t *testing.T, sc fakemeet.Scenario, admit time.Duration) (*Session, *fakemeet.Server) {
	t.Helper()
	path := browser.Find("")
	if path == "" {
		t.Skip("no Chrome or Chromium found")
	}
	srv := fakemeet.NewWithScenario(sc)
	t.Cleanup(srv.Close)
	b, err := browser.Launch(context.Background(), browser.Options{ProfileDir: t.TempDir(), ChromePath: path, Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	s, err := Dial(context.Background(), b, srv.URL, "abc-defg-hij", Options{BotName: botName, AdmitTimeout: admit})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, srv
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestJoinSignedIn(t *testing.T) {
	s, srv := session(t, fakemeet.Scenario{}, time.Minute)
	if err := s.Join(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if !srv.Joined() || !srv.MicOff() || !srv.CameraOff() {
		t.Fatalf("joined=%v mic off=%v camera off=%v", srv.Joined(), srv.MicOff(), srv.CameraOff())
	}
}

func TestJoinSignedOut(t *testing.T) {
	s, _ := session(t, fakemeet.Scenario{SignedOut: true}, time.Minute)
	if err := s.Join(ctxT(t)); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("%v", err)
	}
}

func TestJoinNotFound(t *testing.T) {
	s, _ := session(t, fakemeet.Scenario{NotFound: true}, time.Minute)
	if err := s.Join(ctxT(t)); !errors.Is(err, ErrMeetingNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestJoinLobbyAdmitted(t *testing.T) {
	s, srv := session(t, fakemeet.Scenario{Lobby: true}, time.Minute)
	time.AfterFunc(2*time.Second, srv.Admit)
	if err := s.Join(ctxT(t)); err != nil {
		t.Fatal(err)
	}
}

func TestJoinLobbyDenied(t *testing.T) {
	s, srv := session(t, fakemeet.Scenario{Lobby: true}, time.Minute)
	time.AfterFunc(2*time.Second, srv.Deny)
	if err := s.Join(ctxT(t)); !errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("%v", err)
	}
}

func TestJoinAdmitTimeout(t *testing.T) {
	s, _ := session(t, fakemeet.Scenario{Lobby: true}, 2*time.Second)
	if err := s.Join(ctxT(t)); !errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("%v", err)
	}
}

func joined(t *testing.T, sc fakemeet.Scenario) (*Session, *fakemeet.Server) {
	s, srv := session(t, sc, time.Minute)
	if err := s.Join(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	return s, srv
}

func TestSendChat(t *testing.T) {
	s, srv := joined(t, fakemeet.Scenario{})
	msg := `Slonik Bot is transcribing this call. "Quotes" & <angles> survive.`
	if err := s.SendChat(ctxT(t), msg); err != nil {
		t.Fatal(err)
	}
	if got := srv.Chat(); len(got) != 1 || got[0] != msg {
		t.Fatalf("%q", got)
	}
}

func TestParticipants(t *testing.T) {
	s, srv := joined(t, fakemeet.Scenario{})
	ctx := ctxT(t)
	if got, err := s.Participants(ctx); err != nil || len(got) != 0 {
		t.Fatalf("alone: %q %v", got, err)
	}
	srv.SetParticipants("Bob Example", "Alice Example")
	eventually(t, "participants", func() bool {
		got, err := s.Participants(ctx)
		return err == nil && slices.Equal(got, []string{"Alice Example", "Bob Example"})
	})
}

func TestStatusEnded(t *testing.T) {
	s, srv := joined(t, fakemeet.Scenario{})
	ctx := ctxT(t)
	if st, err := s.Status(ctx); err != nil || st != InCall {
		t.Fatalf("%v %v", st, err)
	}
	srv.End()
	eventually(t, "Ended", func() bool { st, _ := s.Status(ctx); return st == Ended })
}

func TestStatusRemoved(t *testing.T) {
	s, srv := joined(t, fakemeet.Scenario{})
	ctx := ctxT(t)
	srv.Kick()
	eventually(t, "Removed", func() bool { st, _ := s.Status(ctx); return st == Removed })
}

func TestStatusIgnoresSpokenEndPhrases(t *testing.T) {
	s, srv := joined(t, fakemeet.Scenario{})
	ctx := ctxT(t)
	srv.Caption(1, "Alice Example", "you left the meeting early and You've been removed from the meeting list")
	time.Sleep(time.Second)
	if st, err := s.Status(ctx); err != nil || st != InCall {
		t.Fatalf("%v %v", st, err)
	}
}

func TestLeave(t *testing.T) {
	s, srv := joined(t, fakemeet.Scenario{})
	if err := s.Leave(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "left", srv.Left)
}
