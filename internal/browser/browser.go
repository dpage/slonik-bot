// Package browser starts Chrome under chromedp with a persistent profile, in a
// form Google Meet accepts as an ordinary signed-in browser.
package browser

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

// closeTimeout bounds the graceful shutdown in Close before Chrome is killed.
const closeTimeout = 10 * time.Second

// Options configures Launch.
type Options struct {
	ProfileDir string // Chrome user data dir; created 0o700 if missing
	ChromePath string // "" means Find("")
	Headless   bool
	Logger     *slog.Logger // nil means slog.Default()
}

// Browser is a running Chrome instance with one open tab.
type Browser struct {
	tabCtx      context.Context
	cancelTab   context.CancelFunc
	cancelAlloc context.CancelFunc
	lost        <-chan struct{}
	log         *slog.Logger
	once        sync.Once
}

// Launch starts Chrome and returns once it is running and connected.
func Launch(ctx context.Context, o Options) (*Browser, error) {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}

	path := Find(o.ChromePath)
	if path == "" {
		if o.ChromePath != "" {
			return nil, fmt.Errorf("browser: Chrome not found at %s", o.ChromePath)
		}
		return nil, fmt.Errorf("browser: no Chrome or Chromium found; set chrome_path in the config")
	}
	if err := os.MkdirAll(o.ProfileDir, 0o700); err != nil {
		return nil, fmt.Errorf("browser: creating profile directory %s: %w", o.ProfileDir, err)
	}

	// The defaults already disable background timer throttling, occluded-window
	// backgrounding and renderer backgrounding, which keeps the caption
	// observer running when the window is hidden.
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	if o.Headless {
		// New headless mode; the defaults set the plain --headless spelling.
		opts = append(opts, chromedp.Flag("headless", "new"))
	} else {
		// Removes the headless, mute-audio and automation-infobar flags;
		// mute-audio is put back below.
		opts = append(opts, chromedp.VisibleWindow)
	}
	opts = append(opts,
		// VisibleWindow drops this, and without it a headed bot plays the
		// meeting through the operator's speakers, where their own microphone
		// can feed it back into the call as echo. Captions are made by Meet's
		// servers, so the bot loses nothing by not hearing the audio.
		chromedp.Flag("mute-audio", true),
		// Meet turns automated browsers away; these hide navigator.webdriver.
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("autoplay-policy", "no-user-gesture-required"),
		// Meet's strings in selectors.go are English.
		chromedp.Flag("lang", "en-US"),
		chromedp.Flag("accept-lang", "en-US,en"),
		chromedp.WindowSize(1280, 800),
		chromedp.UserDataDir(o.ProfileDir),
		chromedp.ExecPath(path),
	)
	if runtime.GOOS == "linux" {
		// No keyring in a container or headless VM.
		opts = append(opts, chromedp.Flag("password-store", "basic"))
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	tabCtx, cancelTab := chromedp.NewContext(allocCtx)
	// Running with no actions starts Chrome now rather than on first use.
	if err := chromedp.Do(tabCtx); err != nil {
		cancelTab()
		cancelAlloc()
		return nil, fmt.Errorf("browser: starting Chrome with profile %s: %w%s", o.ProfileDir, err, startHint(err, o.Headless, runtime.GOOS, os.Getenv))
	}

	if err := denyDevices(tabCtx); err != nil {
		cancelTab()
		cancelAlloc()
		return nil, err
	}

	// Browser.LostConnection (chromedp browser.go) closes when the connection
	// to the Chrome process drops, which is what happens when it exits.
	c := chromedp.FromContext(tabCtx)
	log.Debug("Chrome started", "path", path, "profile", o.ProfileDir, "headless", o.Headless)
	return &Browser{
		tabCtx:      tabCtx,
		cancelTab:   cancelTab,
		cancelAlloc: cancelAlloc,
		lost:        c.Browser.LostConnection,
		log:         log,
	}, nil
}

// denyDevices has Chrome refuse every page the microphone and camera, so the
// bot cannot send sound or video into a call whatever Meet's buttons are
// called or do. Browser.setPermission with no origin applies to all origins
// in the default browser context; the override lasts until Chrome exits, so
// it is set on every launch, before any page is opened. The names are the
// Permissions API's ("microphone", "camera"), not the PermissionType values.
func denyDevices(ctx context.Context) error {
	err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		for _, name := range []string{"microphone", "camera"} {
			if _, err := cdp.Call(ctx, tg, cdpbrowser.SetPermission, cdpbrowser.SetPermissionParams{
				Permission: &cdpbrowser.PermissionDescriptor{Name: name},
				Setting:    cdpbrowser.PermissionSettingDenied,
			}); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
		return nil
	}))
	if err != nil {
		return fmt.Errorf("browser: denying the microphone and camera: %w", err)
	}
	return nil
}

// startHint adds advice to a failed start, for the two causes that can be told
// apart. A locked profile is recognised by what Chrome prints, which chromedp
// quotes in its error: "Failed to create .../SingletonLock: File exists" and
// "Failed to create a ProcessSingleton for your profile directory" (seen by
// experiment with Chrome on macOS; Linux uses the same process_singleton_posix
// code). A missing display is checked the way chromedp itself checks it.
func startHint(err error, headless bool, goos string, getenv func(string) string) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "SingletonLock") || strings.Contains(msg, "ProcessSingleton"):
		return "; another slonik-bot or Chrome is using this profile"
	case errors.Is(err, chromedp.ErrNoDisplay),
		goos == "linux" && !headless && getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "":
		return "; a Chrome window needs a display, and neither DISPLAY nor WAYLAND_DISPLAY is set (join can run with --headless instead)"
	}
	return ""
}

// Context returns the chromedp context bound to the first tab.
func (b *Browser) Context() context.Context { return b.tabCtx }

// Wait blocks until Chrome exits, for example because the user closed the window.
func (b *Browser) Wait() error {
	<-b.lost
	return nil
}

// Close shuts Chrome down, killing it if it does not stop in time. It is safe
// to call more than once and after Chrome has already exited.
func (b *Browser) Close() error {
	b.once.Do(func() {
		ctx, cancel := context.WithTimeout(b.tabCtx, closeTimeout)
		defer cancel()
		// An error here means Chrome has already gone; the cancels below make
		// sure nothing is left behind either way.
		if err := chromedp.Cancel(ctx); err != nil {
			b.log.Debug("closing Chrome", "error", err)
		}
		b.cancelTab()
		b.cancelAlloc()
	})
	return nil
}
