package bot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dpage/slonik-bot/internal/browser"
	"github.com/dpage/slonik-bot/internal/config"
	"github.com/dpage/slonik-bot/internal/meet"
	"github.com/dpage/slonik-bot/internal/meet/fakemeet"
	"github.com/dpage/slonik-bot/internal/transcript"
)

// TestE2E runs the whole bot against the fake Meet page in headless Chrome.
func TestE2E(t *testing.T) {
	path := browser.Find("")
	if path == "" {
		t.Skip("no Chrome or Chromium found")
	}
	srv := fakemeet.New()
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	b, err := browser.Launch(ctx, browser.Options{ProfileDir: t.TempDir(), ChromePath: path, Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })

	cfg := config.Default()
	cfg.AloneTimeout = time.Hour
	o := Options{Config: cfg, Code: "abc-defg-hij", OutDir: t.TempDir(), Poll: 100 * time.Millisecond, Idle: time.Second}

	// The meeting is scripted once the bot has posted its notice, which it
	// does after the caption observer is in place.
	go func() {
		deadline := time.Now().Add(60 * time.Second)
		for len(srv.Chat()) == 0 {
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		srv.SetParticipants("Alice Example", "Bob Example")
		srv.Caption(1, "Alice Example", "Shall we")
		srv.Caption(1, "Alice Example", "Shall we start?")
		srv.Caption(2, "Bob Example", "I'll email the venue.")
		srv.RemoveCaption(1)
		srv.RemoveCaption(2)
		time.Sleep(500 * time.Millisecond)
		srv.End()
	}()

	dial := func(ctx context.Context) (Call, error) {
		return meet.Dial(ctx, b, srv.URL, o.Code, meet.Options{BotName: cfg.BotName, AdmitTimeout: time.Minute})
	}
	out, err := Run(ctx, o, Deps{Dial: dial, LLM: &fakeLLM{out: sampleResult}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Reason != "ended" || !out.Summarised {
		t.Fatalf("%+v", out)
	}
	us, err := transcript.Read(filepath.Join(o.OutDir, "transcript.jsonl"))
	if err != nil || len(us) != 2 {
		t.Fatalf("%+v %v", us, err)
	}
	if chat := srv.Chat(); len(chat) != 1 || chat[0] != cfg.ChatNotice {
		t.Fatalf("chat %q", chat)
	}
	if _, err := os.Stat(filepath.Join(o.OutDir, "summary.md")); err != nil {
		t.Fatal(err)
	}
}
