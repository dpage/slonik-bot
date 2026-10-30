package browser

import (
	"context"
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
