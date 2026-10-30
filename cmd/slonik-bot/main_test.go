package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dpage/slonik-bot/internal/bot"
	"github.com/dpage/slonik-bot/internal/browser"
	"github.com/dpage/slonik-bot/internal/version"
)

func TestVersionCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "slonik-bot ") {
		t.Fatalf("unexpected output %q", out.String())
	}
}

// A plain go build has no commit, and must not print a trailing space.
func TestVersionWithAndWithoutCommit(t *testing.T) {
	v, c := version.Version, version.Commit
	t.Cleanup(func() { version.Version, version.Commit = v, c })
	version.Version = "1.0.0"
	for commit, want := range map[string]string{"": "slonik-bot 1.0.0\n", "abc1234": "slonik-bot 1.0.0 abc1234\n"} {
		version.Commit = commit
		var out, errb bytes.Buffer
		if run([]string{"version"}, &out, &errb); out.String() != want {
			t.Errorf("commit %q: got %q, want %q", commit, out.String(), want)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"frobnicate"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "unknown command") {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestNoArgsPrintsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "usage:") {
		t.Fatalf("stderr %q", errb.String())
	}
}

// noKeys makes sure no real API key, from the environment or the developer's
// key files, reaches a test, and that no real config file is read.
func noKeys(t *testing.T) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func TestJoinRejectsBadCode(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"join", "not-a-meeting"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "meeting code") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
}

func TestJoinNeedsOneMeeting(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"join"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	if code := run([]string{"join", "abc-defg-hij", "abc-defg-hik"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
}

func TestJoinFailsFastWithoutAPIKey(t *testing.T) {
	noKeys(t)
	outDir := filepath.Join(t.TempDir(), "notes")
	var out, errb bytes.Buffer
	start := time.Now()
	code := run([]string{"join", "--out", outDir, "https://meet.google.com/abc-defg-hij"}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "ANTHROPIC_API_KEY") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("took too long: did it start Chrome?")
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Fatal("output directory created before the API key check")
	}
}

// Flags after the meeting code still apply.
func TestJoinFlagsAfterCode(t *testing.T) {
	noKeys(t)
	var out, errb bytes.Buffer
	code := run([]string{"join", "abc-defg-hij", "--llm-provider", "bogus"}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), `"bogus"`) {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
}

func TestJoinBadFlag(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"join", "--frobnicate", "abc-defg-hij"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
}

func TestSummariseCommand(t *testing.T) {
	noKeys(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"finish_reason":"stop","message":{"content":"{\"title\":\"T\",\"summary\":\"S\",\"decisions\":[],\"action_items\":[],\"corrections\":[]}"}}]}`)
	}))
	defer srv.Close()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte("llm:\n  provider: openai\n  model: m\n  base_url: "+srv.URL+"\n"), 0o600)
	p := filepath.Join(dir, "transcript.jsonl")
	os.WriteFile(p, []byte(`{"speaker":"Alice Example","start":"2026-10-30T14:00:00Z","end":"2026-10-30T14:00:02Z","text":"Hello."}`+"\n"), 0o600)
	var out, errb bytes.Buffer
	if code := run([]string{"summarise", "--config", cfgPath, p}, &out, &errb); code != 0 {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "summary.md")); err != nil {
		t.Fatal(err)
	}
}

func TestSummariseMissingConfigIsAnError(t *testing.T) {
	noKeys(t)
	var out, errb bytes.Buffer
	code := run([]string{"summarise", "--config", filepath.Join(t.TempDir(), "absent.yaml"), "transcript.jsonl"}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
}

// login always shows the window, but accepts --headless rather than failing.
func TestLoginAcceptsHeadless(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"login", "--headless", "extra"}, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "takes no arguments") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
}

// Ctrl-C must not kill Chrome during login: chromedp starts it with
// exec.CommandContext, so a cancellable context would kill it before Close
// can ask it to quit and save the new sign-in cookies.
func TestLoginLaunchesChromeWithoutCancel(t *testing.T) {
	noKeys(t)
	var got context.Context
	orig := launchBrowser
	t.Cleanup(func() { launchBrowser = orig })
	launchBrowser = func(ctx context.Context, _ browser.Options) (*browser.Browser, error) {
		got = ctx
		return nil, errors.New("not starting Chrome in a test")
	}
	var out, errb bytes.Buffer
	if code := run([]string{"login"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	if got == nil || got.Done() != nil {
		t.Fatal("login gave Chrome a context that a signal can cancel")
	}
}

func TestFailHasOnePrefix(t *testing.T) {
	var errb bytes.Buffer
	if code := fail(&errb, errors.New("bot: summary failed")); code != 1 || errb.String() != "slonik-bot: summary failed\n" {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
}

func TestJoinResultNamesTheFolder(t *testing.T) {
	dir := filepath.Join("notes", "2026-10-30-abc-defg-hij")
	var out, errb bytes.Buffer
	if code := joinResult(bot.Outcome{Dir: dir, Reason: "ended"}, bot.ErrNoCaptions, &out, &errb); code != 1 || !strings.Contains(errb.String(), dir) {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	// No folder is named when the bot never got as far as recording.
	errb.Reset()
	if code := joinResult(bot.Outcome{Dir: dir}, errors.New("meet: not admitted to the meeting"), &out, &errb); code != 1 || strings.Contains(errb.String(), dir) {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	errb.Reset()
	if code := joinResult(bot.Outcome{Dir: dir, Reason: "ended", Summarised: true}, nil, &out, &errb); code != 0 || !strings.Contains(out.String(), dir) {
		t.Fatalf("exit %d stdout %q", code, out.String())
	}
}
