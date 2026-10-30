package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

func TestFindExplicit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Find(p); got != p {
		t.Fatalf("Find(%q) = %q", p, got)
	}
	if got := Find(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Fatalf("missing explicit path gave %q", got)
	}
}

func TestLaunchMissingChrome(t *testing.T) {
	_, err := Launch(context.Background(), Options{ProfileDir: t.TempDir(), ChromePath: "/nonexistent/chrome", Headless: true})
	if err == nil || !strings.Contains(err.Error(), "/nonexistent/chrome") {
		t.Fatalf("%v", err)
	}
}

// fakeChrome writes a stand-in for Chrome that records its arguments, one
// per line, in the returned file and exits, so tests can see the command line
// Launch builds without starting a browser.
func fakeChrome(t *testing.T) (exe, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	exe = filepath.Join(dir, "chrome")
	argsFile = filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\nexit 1\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe, argsFile
}

// The meeting's audio must never come out of the operator's speakers: captions
// are made by Meet's servers, so the bot gains nothing from hearing it.
func TestLaunchMutesAudio(t *testing.T) {
	// A visible window on Linux needs a display before chromedp will start
	// the process; the fake never opens one.
	t.Setenv("DISPLAY", ":0")
	for _, headless := range []bool{true, false} {
		exe, argsFile := fakeChrome(t)
		// The fake exits at once, so Launch is expected to fail.
		_, _ = Launch(context.Background(), Options{ProfileDir: t.TempDir(), ChromePath: exe, Headless: headless})
		args, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatalf("headless=%v: fake Chrome was not started: %v", headless, err)
		}
		if !slices.Contains(strings.Split(string(args), "\n"), "--mute-audio") {
			t.Errorf("headless=%v: --mute-audio missing from:\n%s", headless, args)
		}
	}
}

// failingChrome is a stand-in for Chrome that prints output to stderr and exits.
func failingChrome(t *testing.T, output string) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "chrome")
	script := "#!/bin/sh\ncat >&2 <<'EOF'\n" + output + "\nEOF\nexit 21\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

// The profile hint is given only when Chrome says the profile is locked. The
// output is what Chrome 14x on macOS printed for a second launch on a profile
// already in use (seen by experiment), with the paths made up.
func TestLaunchProfileInUseHint(t *testing.T) {
	const locked = `[123:456:1030/141539.595698:ERROR:chrome/browser/process_singleton_posix.cc:349] Failed to create /tmp/profile/SingletonLock: File exists (17)
[123:456:1030/141539.596998:ERROR:chrome/app/chrome_main_delegate.cc:624] Failed to create a ProcessSingleton for your profile directory. This means that running multiple instances would start multiple browser processes rather than opening a new window in the existing process. Aborting now to avoid profile corruption.`
	_, err := Launch(context.Background(), Options{ProfileDir: t.TempDir(), ChromePath: failingChrome(t, locked), Headless: true})
	if err == nil || !strings.Contains(err.Error(), "another slonik-bot or Chrome is using this profile") {
		t.Fatalf("%v", err)
	}
	_, err = Launch(context.Background(), Options{ProfileDir: t.TempDir(), ChromePath: failingChrome(t, "something else went wrong"), Headless: true})
	if err == nil || strings.Contains(err.Error(), "using this profile") {
		t.Fatalf("%v", err)
	}
}

func TestStartHintNoDisplay(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	other := errors.New("chrome failed to start")
	for _, c := range []struct {
		name     string
		err      error
		headless bool
		goos     string
		vars     map[string]string
		want     bool
	}{
		{"linux, no display", other, false, "linux", nil, true},
		{"chromedp's own check", fmt.Errorf("x: %w", chromedp.ErrNoDisplay), false, "linux", nil, true},
		{"headless", other, true, "linux", nil, false},
		{"X11", other, false, "linux", map[string]string{"DISPLAY": ":0"}, false},
		{"Wayland", other, false, "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, false},
		{"macOS", other, false, "darwin", nil, false},
	} {
		got := strings.Contains(startHint(c.err, c.headless, c.goos, env(c.vars)), "display")
		if got != c.want {
			t.Errorf("%s: display hint %v, want %v", c.name, got, c.want)
		}
	}
}

func launch(t *testing.T) *Browser {
	t.Helper()
	path := Find("")
	if path == "" {
		t.Skip("no Chrome or Chromium found")
	}
	b, err := Launch(context.Background(), Options{ProfileDir: t.TempDir(), ChromePath: path, Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func TestLaunchHidesAutomation(t *testing.T) {
	b := launch(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("<html><body>ok</body></html>"))
	}))
	defer srv.Close()
	if err := chromedp.Do(b.Context(), chromedp.Navigate(srv.URL)); err != nil {
		t.Fatal(err)
	}
	webdriver, err := chromedp.Run(b.Context(), chromedp.Evaluate[bool](`navigator.webdriver === true`))
	if err != nil {
		t.Fatal(err)
	}
	if webdriver {
		t.Fatal("navigator.webdriver is true; Meet will spot the automation")
	}
}

func TestWaitReturnsWhenChromeExits(t *testing.T) {
	b := launch(t)
	done := make(chan error, 1)
	go func() { done <- b.Wait() }()
	// Closing the browser over CDP is what a user closing the window looks like to us.
	_ = chromedp.Do(b.Context(), chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		_, err := cdp.Call(ctx, tg, cdpbrowser.Close, cdp.Empty{})
		return err
	}))
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Wait did not return after Chrome exited")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close after exit: %v", err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	b := launch(t)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}
