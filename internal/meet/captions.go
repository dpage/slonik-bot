package meet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/dpage/slonik-bot/internal/captions"
)

// captionsOnTimeout is how long Meet gets to show that captions are on after
// each attempt to turn them on.
const captionsOnTimeout = 10 * time.Second

// errSessionClosed is returned by StartCaptions after Close.
var errSessionClosed = errors.New("meet: session closed")

// captionFeed is the Session's caption binding and the goroutine that
// forwards its events. It is started at most once per Session.
type captionFeed struct {
	mu      sync.Mutex
	started bool
	closed  bool
	cancel  context.CancelFunc
}

// stop ends the forwarding goroutine, if any, and stops later starts. It is
// safe to call more than once.
func (f *captionFeed) stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	if f.cancel != nil {
		f.cancel()
	}
}

// StartCaptions turns Meet's captions on, sets their language, and sends each
// caption block update to events until the tab goes or Close is called. An
// empty language, or English, leaves Meet's default. StartCaptions never
// closes events; the caller owns it.
//
// Only the first successful call starts the feed. A later call still makes
// sure captions are on and sets the language, but its events channel is
// ignored: every event keeps going to the first call's channel, once.
func (s *Session) StartCaptions(ctx context.Context, language string, events chan<- captions.Event) error {
	ctx, done := s.bind(ctx)
	defer done()

	if err := turnOnCaptions(ctx); err != nil {
		return err
	}

	if language != "" && !strings.EqualFold(language, "English") {
		if err := setCaptionLanguage(ctx, language); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.log.Warn("caption language not set; captions stay in Meet's default", "language", language, "error", err)
			s.closeDialog(ctx)
		}
	}

	if err := s.startFeed(ctx, events); err != nil {
		return err
	}
	if err := chromedp.Do(ctx, chromedp.Evaluate[chromedp.Void](captions.ObserverJS)); err != nil {
		return fmt.Errorf("meet: starting the caption observer: %w", err)
	}
	return nil
}

// captionsShownJS is true when Meet shows captions as on: either the toggle
// offers to turn them off, or the captions menu button is pressed.
var captionsShownJS = fmt.Sprintf(`(() => {
	if (document.querySelector(%s) !== null) { return true; }
	const m = document.querySelector(%s);
	return m !== null && m.getAttribute('aria-pressed') === 'true';
})()`, jsString(selCaptionsOff), jsString(selCaptionsMenu))

func captionsShown(ctx context.Context) (bool, error) {
	return chromedp.Run(ctx, chromedp.Evaluate[bool](captionsShownJS))
}

// turnOnCaptions clicks whichever captions control the page has, falling back
// to Meet's c shortcut, and fails if captions still are not on.
func turnOnCaptions(ctx context.Context) error {
	on, err := captionsShown(ctx)
	if err != nil || on {
		return err
	}

	clicked := false
	for _, c := range []struct{ what, sel string }{
		{"captions button", selCaptionsOn},
		{"captions menu button", selCaptionsMenu},
	} {
		there, err := present(ctx, c.sel)
		if err != nil {
			return err
		}
		if !there {
			continue
		}
		clicked = true
		// A control that will not take the click is dealt with by the c
		// key below, so only the end of ctx counts here.
		if err := click(ctx, c.what, c.sel); err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		break
	}
	if !clicked {
		if err := pressCaptionsKey(ctx); err != nil {
			return err
		}
	}

	waitOn := func() (bool, error) { return captionsShown(ctx) }
	on, err = waitFor(ctx, captionsOnTimeout, waitOn)
	if err != nil {
		return err
	}
	if !on && clicked {
		if err := pressCaptionsKey(ctx); err != nil {
			return err
		}
		if on, err = waitFor(ctx, captionsOnTimeout, waitOn); err != nil {
			return err
		}
	}
	if !on {
		return fmt.Errorf("meet: captions did not turn on (looked for %s, %s)", selCaptionsOn, selCaptionsMenu)
	}
	return nil
}

// pressCaptionsKey presses c, Meet's shortcut for toggling captions. Only the
// end of ctx is returned: whether captions came on is checked afterwards.
func pressCaptionsKey(ctx context.Context) error {
	kc, cancel := context.WithTimeout(ctx, clickTimeout)
	defer cancel()
	_ = chromedp.Do(kc, chromedp.KeyEvent("c"))
	return ctx.Err()
}

// setCaptionLanguage picks language in Meet's caption settings.
func setCaptionLanguage(ctx context.Context, language string) error {
	steps := []struct{ what, sel string }{
		{"more options button", selMoreOptions},
		{"settings menu item", xpSettings},
		{"captions settings tab", selCaptionsTab},
	}
	for _, st := range steps {
		if err := click(ctx, st.what, st.sel); err != nil {
			return err
		}
	}
	if err := waitVisible(ctx, "meeting language list", selLanguageItems); err != nil {
		return err
	}
	script := fmt.Sprintf(`(() => {
	const want = %s.toLowerCase();
	const item = Array.from(document.querySelectorAll(%s)).find(e => (e.textContent || '').trim().toLowerCase() === want);
	if (item === undefined) { return false; }
	item.click();
	return true;
})()`, jsString(language), jsString(selLanguageItems))
	found, err := chromedp.Run(ctx, chromedp.Evaluate[bool](script))
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("meet: language %q not offered (looked for %s)", language, selLanguageItems)
	}
	return click(ctx, "close dialog button", selCloseDialog)
}

// closeDialog makes a best effort to shut a half-open settings dialog, so it
// does not cover the call.
func (s *Session) closeDialog(ctx context.Context) {
	if _, err := jsClick(ctx, selCloseDialog); err != nil {
		s.log.Debug("closing the settings dialog", "error", err)
	}
	kc, cancel := context.WithTimeout(ctx, clickTimeout)
	defer cancel()
	if err := chromedp.Do(kc, chromedp.KeyEvent(kb.Escape)); err != nil {
		s.log.Debug("pressing Escape", "error", err)
	}
}

// startFeed adds the page binding and starts forwarding its calls to events,
// once per Session.
func (s *Session) startFeed(ctx context.Context, events chan<- captions.Event) error {
	f := &s.feed
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errSessionClosed
	}
	if f.started {
		return nil
	}

	if _, err := chromedp.Call(ctx, runtime.AddBinding, runtime.AddBindingParams{Name: captions.BindingName}); err != nil {
		return fmt.Errorf("meet: adding the caption binding: %w", err)
	}
	// The binding survives a reload of the page but the observer does not,
	// so have Chrome inject it into every new document too. Its
	// window.__slonikObserver guard keeps the immediate injection in
	// StartCaptions from attaching a second observer.
	if _, err := chromedp.Call(ctx, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: captions.ObserverJS}); err != nil {
		return fmt.Errorf("meet: registering the caption observer: %w", err)
	}

	f.cancel = s.runFeed(events) //nolint:contextcheck // the feed outlives ctx by design; see runFeed
	f.started = true
	return nil
}

// runFeed subscribes to the binding's calls and forwards them to events from
// a goroutine, until the tab goes or the returned func is called. The feed
// belongs to the tab rather than to StartCaptions' ctx, which ends when
// StartCaptions returns. chromedp's subscription queues events without limit
// (a mutex-guarded slice and a wake channel), so the tab's event loop never
// waits for the goroutine, however slowly the caller reads events.
func (s *Session) runFeed(events chan<- captions.Event) context.CancelFunc {
	ctx, cancel := context.WithCancel(s.tab)
	calls := chromedp.Events(ctx, runtime.BindingCalled)
	go s.forward(ctx, calls, events)
	return cancel
}

// forward runs the feed, and warns if it stops for any reason but Close,
// because no captions will be recorded after that.
func (s *Session) forward(ctx context.Context, calls iter.Seq2[runtime.EventBindingCalled, error], events chan<- captions.Event) {
	err := pump(ctx, calls, events, s.log)
	s.feed.mu.Lock()
	closed := s.feed.closed
	s.feed.mu.Unlock()
	if closed {
		return
	}
	if err != nil {
		s.log.Warn("caption feed stopped; no further captions will be recorded", "error", err)
		return
	}
	s.log.Warn("caption feed stopped because the tab has gone; no further captions will be recorded")
}

// pump decodes the binding calls into Events and sends them to events until
// ctx ends, which it returns, or the subscription closes.
func pump(ctx context.Context, calls iter.Seq2[runtime.EventBindingCalled, error], events chan<- captions.Event, log *slog.Logger) error {
	for call, err := range calls {
		if err != nil {
			return err
		}
		if call.Name != captions.BindingName {
			continue
		}
		var ev captions.Event
		if err := json.Unmarshal([]byte(call.Payload), &ev); err != nil {
			log.Debug("dropping a malformed caption event", "payload", call.Payload, "error", err)
			continue
		}
		select {
		case events <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
