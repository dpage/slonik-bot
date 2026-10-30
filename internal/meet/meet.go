package meet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/dpage/slonik-bot/internal/browser"
)

// DefaultBaseURL is where Google Meet lives; tests point Dial at a fake.
const DefaultBaseURL = "https://meet.google.com"

// Status is what Status reports about the call.
type Status int

// The states Status can report.
const (
	InCall  Status = iota // still in the call
	Ended                 // the call ended or the bot left
	Removed               // somebody removed the bot from the call
)

func (st Status) String() string {
	switch st {
	case InCall:
		return "in call"
	case Ended:
		return "ended"
	case Removed:
		return "removed"
	}
	return "Status(" + strconv.Itoa(int(st)) + ")"
}

// Errors Join returns; callers check them with errors.Is.
var (
	ErrNotSignedIn     = errors.New("meet: the browser profile is not signed in to Google")
	ErrNotAdmitted     = errors.New("meet: not admitted to the meeting")
	ErrMeetingNotFound = errors.New("meet: meeting not found; check the meeting code")
	ErrDevicesOn       = errors.New("meet: could not confirm that the microphone and camera are off")
)

// Options configures a Session.
type Options struct {
	BotName      string        // the bot's display name, left out of Participants
	AdmitTimeout time.Duration // 0 means 5 minutes
	Logger       *slog.Logger  // nil means slog.Default()
}

const (
	defaultAdmitTimeout = 5 * time.Minute
	pollInterval        = 500 * time.Millisecond // the pre-join and admission loops
	checkInterval       = 100 * time.Millisecond // short waits for one change on the page
	prejoinTimeout      = 30 * time.Second
	clickTimeout        = 10 * time.Second
	joinRetryWait       = 5 * time.Second
	navigateTimeout     = 60 * time.Second
	chatSentTimeout     = 3 * time.Second
	justLeaveTimeout    = 2 * time.Second
	deviceOffTimeout    = 3 * time.Second  // for a microphone or camera to show it is off
	leaveTimeout        = 10 * time.Second // leaving after the devices would not turn off
)

// Session drives one Meet call in the browser's tab.
type Session struct {
	tab          context.Context // chromedp context of the tab
	url          string
	botName      string
	admitTimeout time.Duration
	log          *slog.Logger
	feed         captionFeed // see captions.go
	lastPeople   string      // last participant readings logged
}

// Dial checks the meeting code and prepares a session in b's tab. It does not
// navigate; Join does.
func Dial(ctx context.Context, b *browser.Browser, baseURL, code string, o Options) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	code, err := ParseCode(code)
	if err != nil {
		return nil, err
	}
	s := &Session{
		tab:          b.Context(),
		url:          strings.TrimSuffix(baseURL, "/") + "/" + code + "?hl=en", // hl pins Meet's language to selectors.go
		botName:      o.BotName,
		admitTimeout: o.AdmitTimeout,
		log:          o.Logger,
	}
	if s.admitTimeout <= 0 {
		s.admitTimeout = defaultAdmitTimeout
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s, nil
}

// Close ends the session's own background work. The browser belongs to the
// caller, so Close leaves it, and the call, alone. It is safe to call twice.
func (s *Session) Close() error {
	s.feed.stop()
	return nil
}

// bind returns a context for chromedp actions on the tab that also ends when
// ctx does, because chromedp needs the tab's context and the caller's
// deadline and cancellation still have to apply.
func (s *Session) bind(ctx context.Context) (context.Context, context.CancelFunc) {
	base, cancelBase := context.WithCancel(s.tab)
	c, cancelDeadline := base, context.CancelFunc(func() {})
	if d, ok := ctx.Deadline(); ok {
		c, cancelDeadline = context.WithDeadline(base, d)
	}
	stop := context.AfterFunc(ctx, cancelBase)
	return c, func() {
		stop()
		cancelDeadline()
		cancelBase()
	}
}

// Join opens the meeting, gets past the pre-join screen and waits until the
// bot is in the call.
func (s *Session) Join(ctx context.Context) error {
	parent := ctx
	ctx, done := s.bind(ctx)
	defer done()

	nc, cancel := context.WithTimeout(ctx, navigateTimeout)
	err := chromedp.Do(nc, chromedp.Navigate(s.url))
	timedOut := nc.Err() != nil && ctx.Err() == nil
	cancel()
	if timedOut {
		return fmt.Errorf("meet: opening %s: the page did not load within %s", s.url, navigateTimeout)
	}
	if err != nil {
		return fmt.Errorf("meet: opening %s: %w", s.url, err)
	}
	if err := s.prejoin(ctx); err != nil {
		return err
	}
	// Chrome already refuses Meet the microphone and camera; seeing both
	// turned off in Meet as well is a second check, so the bot never joins
	// with either live if that refusal ever stops working.
	if err := s.devicesOff(ctx); err != nil {
		return err
	}
	if err := s.pressJoin(ctx); err != nil {
		return err
	}
	if err := s.awaitAdmission(ctx); err != nil {
		return err
	}
	// Checked again in the call, since the devices could have come back on
	// and the bot is now heard and seen by everyone.
	if err := s.devicesOff(ctx); err != nil {
		if ctx.Err() != nil {
			return err
		}
		lctx, cancel := context.WithTimeout(context.WithoutCancel(parent), leaveTimeout)
		defer cancel()
		if lerr := s.Leave(lctx); lerr != nil {
			return errors.Join(err, fmt.Errorf("meet: leaving the call after that failed too: %w", lerr))
		}
		return fmt.Errorf("%w; the bot left the call", err)
	}
	return nil
}

// device is a microphone or camera control whose label offers to turn the
// device off, which means it is on.
type device struct{ name, sel string }

var devices = []device{{"microphone", selMicOff}, {"camera", selCameraOff}}

// devicesOn returns the devices whose "Turn off" control is on the page.
func devicesOn(ctx context.Context) ([]device, error) {
	var on []device
	for _, d := range devices {
		there, err := present(ctx, d.sel)
		if err != nil {
			return nil, err
		}
		if there {
			on = append(on, d)
		}
	}
	return on, nil
}

// devicesOff clicks the "Turn off" control of each device that is on, and
// once more for any that a click did not turn off, then returns ErrDevicesOn
// naming the devices that are still on. It knows a device is on only by its
// "Turn off" label being present, so a device whose control has gone, or been
// relabelled, counts as off.
func (s *Session) devicesOff(ctx context.Context) error {
	for attempt := range 2 {
		on, err := devicesOn(ctx)
		if err != nil || len(on) == 0 {
			return err
		}
		if attempt > 0 {
			s.log.Warn("the microphone or camera did not turn off; clicking again", "devices", deviceNames(on))
		}
		for _, d := range on {
			if err := s.clickIfPresent(ctx, d.name+" button", d.sel); err != nil {
				return err
			}
		}
		if _, err := waitFor(ctx, deviceOffTimeout, func() (bool, error) {
			on, err := devicesOn(ctx)
			return len(on) == 0, err
		}); err != nil {
			return err
		}
	}
	on, err := devicesOn(ctx)
	if err != nil || len(on) == 0 {
		return err
	}
	sels := make([]string, len(on))
	for i, d := range on {
		sels[i] = d.sel
	}
	return fmt.Errorf("%w: the %s did not turn off (still found %s)", ErrDevicesOn, deviceNames(on), strings.Join(sels, ", "))
}

func deviceNames(ds []device) string {
	names := make([]string, len(ds))
	for i, d := range ds {
		names[i] = d.name
	}
	return strings.Join(names, " and ")
}

// prejoin waits for the join button, failing early on the pages that mean it
// will never come, and dismissing the dialogues that can cover it.
func (s *Session) prejoin(ctx context.Context) error {
	deadline := time.Now().Add(prejoinTimeout)
	for {
		loc, err := chromedp.Run(ctx, chromedp.Location())
		if err != nil {
			return err
		}
		if strings.HasPrefix(loc, signInURLPrefix) {
			return ErrNotSignedIn
		}
		signIn, err := present(ctx, xpSignInHeader, selNameInput)
		if err != nil {
			return err
		}
		if signIn {
			return ErrNotSignedIn
		}
		text, err := pageText(ctx)
		if err != nil {
			return err
		}
		if _, ok := match(text, textsNotFound); ok {
			return ErrMeetingNotFound
		}
		if t, ok := match(text, textsBlocked); ok {
			return fmt.Errorf("%w: Meet refused entry (%s)", ErrNotAdmitted, t)
		}
		if err := s.clickIfPresent(ctx, "consent dialogue button", selConsentOK); err != nil {
			return err
		}
		if err := s.clickIfPresent(ctx, "Got it button", xpGotIt); err != nil {
			return err
		}
		shown, err := present(ctx, xpContinueWithoutDevices)
		if err != nil {
			return err
		}
		if shown {
			if err := click(ctx, "continue without microphone and camera button", xpContinueWithoutDevices); err != nil {
				return err
			}
		}
		ready, err := present(ctx, xpJoin)
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("meet: join button not found (looked for %s)", xpJoin)
		}
		if err := sleep(ctx, pollInterval); err != nil {
			return err
		}
	}
}

// pressJoin clicks Join, falling back to a JavaScript click and then the Enter
// key, because Meet may ignore synthetic CDP clicks on this button.
func (s *Session) pressJoin(ctx context.Context) error {
	if err := click(ctx, "join button", xpJoin); err != nil {
		return err
	}
	gone, err := waitGone(ctx, xpJoin, joinRetryWait)
	if err != nil || gone {
		return err
	}

	s.log.Warn("join button ignored a CDP click; trying a JavaScript click")
	if _, err := jsClick(ctx, xpJoin); err != nil {
		return err
	}
	if gone, err = waitGone(ctx, xpJoin, joinRetryWait); err != nil || gone {
		return err
	}

	s.log.Warn("join button ignored a JavaScript click; trying the Enter key")
	kc, cancel := context.WithTimeout(ctx, clickTimeout)
	err = chromedp.Do(kc, chromedp.Focus(chromedp.JSPath(elementJS(xpJoin))), chromedp.KeyEvent(kb.Enter))
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		// The button may have gone between the check and the key press; the
		// check below decides.
		s.log.Debug("pressing Enter on the join button", "error", err)
	}
	if gone, err = waitGone(ctx, xpJoin, joinRetryWait); err != nil || gone {
		return err
	}
	return errors.New("meet: the join button did not respond to clicks; see 'Join button or chat ignored' under Troubleshooting in the README")
}

// awaitAdmission waits until the bot is in the call, has been refused, or
// nobody has let it in within the admit timeout.
func (s *Session) awaitAdmission(ctx context.Context) error {
	deadline := time.Now().Add(s.admitTimeout)
	logged := false
	for {
		text, err := pageText(ctx)
		if err != nil {
			return err
		}
		_, waiting := match(text, textsWaiting)
		if !waiting {
			// Leave call is no help here: the lobby has one too.
			in, err := present(ctx, selCaptionsOn, selCaptionsOff, selCaptionsMenu, selShareScreen)
			if err != nil {
				return err
			}
			if in {
				s.log.Debug("in the call")
				return nil
			}
		}
		if t, ok := match(text, textsDenied); ok {
			return fmt.Errorf("%w: %s", ErrNotAdmitted, t)
		}
		if waiting && !logged {
			s.log.Info("waiting to be let in")
			logged = true
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: nobody let the bot in within %s", ErrNotAdmitted, s.admitTimeout)
		}
		if err := sleep(ctx, pollInterval); err != nil {
			return err
		}
	}
}

// SendChat posts msg to the call's chat.
func (s *Session) SendChat(ctx context.Context, msg string) error {
	ctx, done := s.bind(ctx)
	defer done()

	open, err := present(ctx, selChatInput)
	if err != nil {
		return err
	}
	if !open {
		if err := click(ctx, "chat button", selChatButton); err != nil {
			return err
		}
		if err := waitVisible(ctx, "chat box", selChatInput); err != nil {
			return err
		}
	}

	script := fmt.Sprintf(`(() => {
	const t = document.querySelector(%s);
	if (t === null) { return false; }
	t.focus();
	t.value = %s;
	t.dispatchEvent(new Event('input', {bubbles: true}));
	t.dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true}));
	return true;
})()`, jsString(selChatInput), jsString(msg))
	found, err := chromedp.Run(ctx, chromedp.Evaluate[bool](script))
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("meet: chat box not found (looked for %s)", selChatInput)
	}

	sent, err := chatEmpty(ctx)
	if err != nil {
		return err
	}
	if !sent {
		s.log.Warn("chat box ignored a scripted Enter; sending the Enter key")
		kc, cancel := context.WithTimeout(ctx, clickTimeout)
		err := chromedp.Do(kc, chromedp.SendKeys(chromedp.CSS(selChatInput), kb.Enter))
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			s.log.Debug("sending Enter to the chat box", "error", err)
		}
		if sent, err = chatEmpty(ctx); err != nil {
			return err
		}
		if !sent {
			return errors.New("meet: chat message was not sent")
		}
	}

	// An open chat panel narrows the captions area.
	return click(ctx, "chat button", selChatButton)
}

// chatEmpty waits up to chatSentTimeout for the chat box to empty, which is
// what Meet does once it has taken the message.
func chatEmpty(ctx context.Context) (bool, error) {
	script := fmt.Sprintf(`(() => { const t = document.querySelector(%s); return t !== null && t.value === ""; })()`, jsString(selChatInput))
	return waitFor(ctx, chatSentTimeout, func() (bool, error) {
		return chromedp.Run(ctx, chromedp.Evaluate[bool](script))
	})
}

// people is what the page shows about who is in the call.
type people struct {
	Menus       []string `json:"menus"`
	AvatarCount *string  `json:"avatarCount"`
	PeopleLabel *string  `json:"peopleLabel"`
	// Tiles and Buttons are only logged, to show what Meet exposes when the
	// selectors above stop matching.
	Tiles   []string `json:"tiles"`
	Buttons []string `json:"buttons"`
}

// Participants returns the names of the other people in the call, sorted.
// When Meet shows how many people there are but not their names, the result
// holds one empty string for each person other than the bot.
func (s *Session) Participants(ctx context.Context) ([]string, error) {
	ctx, done := s.bind(ctx)
	defer done()

	script := fmt.Sprintf(`(() => {
	const attr = (sel, name) => { const e = document.querySelector(sel); return e === null ? null : e.getAttribute(name); };
	return {
		menus: Array.from(document.querySelectorAll(%s), e => e.getAttribute('aria-label') || ''),
		avatarCount: attr(%s, 'data-avatar-count'),
		peopleLabel: attr(%s, 'aria-label'),
		tiles: [...new Set(Array.from(document.querySelectorAll('[data-participant-id]'), e => e.getAttribute('data-participant-id')))],
		buttons: Array.from(document.querySelectorAll('button[aria-label]'), e => e.getAttribute('aria-label')).filter(l => /people|everyone|participant|more options for/i.test(l)),
	};
})()`, jsString(selParticipantMenus), jsString(selAvatarCount), jsString(selPeopleButton))
	p, err := chromedp.Run(ctx, chromedp.Evaluate[people](script))
	if err != nil {
		return nil, err
	}
	if raw := fmt.Sprintf("%q %q %q %q %q", p.Menus, deref(p.AvatarCount), deref(p.PeopleLabel), p.Tiles, p.Buttons); raw != s.lastPeople {
		s.lastPeople = raw
		s.log.Debug("participant readings changed", "menus", p.Menus, "avatar_count", deref(p.AvatarCount),
			"people_label", deref(p.PeopleLabel), "tiles", p.Tiles, "buttons", p.Buttons)
	}

	var names []string
	for _, label := range p.Menus {
		name := strings.TrimSpace(strings.TrimPrefix(label, participantMenuPrefix))
		if name == "" || s.isSelf(name) || slices.Contains(names, name) {
			continue
		}
		names = append(names, name)
	}
	if len(names) > 0 {
		slices.Sort(names)
		return names, nil
	}

	count, ok := 0, false
	if p.AvatarCount != nil {
		if n, err := strconv.Atoi(strings.TrimSpace(*p.AvatarCount)); err == nil {
			count, ok = n, true
		}
	}
	if !ok && p.PeopleLabel != nil {
		if m := peopleLabel.FindStringSubmatch(*p.PeopleLabel); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				count, ok = n, true
			}
		}
	}
	switch {
	case !ok:
		return nil, fmt.Errorf("meet: participant list not found (looked for %s, %s, %s)", selParticipantMenus, selAvatarCount, selPeopleButton)
	case count <= 1:
		return []string{}, nil
	default:
		// Others are present but unnamed.
		return make([]string, count-1), nil
	}
}

func (s *Session) isSelf(name string) bool {
	if s.botName != "" && strings.EqualFold(name, s.botName) {
		return true
	}
	for _, n := range selfNames {
		if strings.EqualFold(name, n) {
			return true
		}
	}
	return false
}

// pageState is what Status reads from the page in one go.
type pageState struct {
	EndScreen *string `json:"endScreen"`
	Page      *string `json:"page"`
}

// Status reports whether the bot is still in the call. An error means the page
// could not be read, typically because the browser has gone.
func (s *Session) Status(ctx context.Context) (Status, error) {
	ctx, done := s.bind(ctx)
	defer done()
	return status(ctx)
}

func status(ctx context.Context) (Status, error) {
	// The whole page is read only once the in-call controls have gone:
	// captions are page text, so during the call a participant saying "you
	// left the meeting" would otherwise end the recording.
	script := fmt.Sprintf(`(() => {
	const norm = s => s.replace(/\u2019/g, "'");
	const end = document.querySelector(%s);
	const controls = %s.some(sel => document.querySelector(sel) !== null);
	return {
		endScreen: end === null ? null : norm(end.innerText || end.textContent || ''),
		page: controls || !document.body ? null : norm(document.body.innerText),
	};
})()`, jsString(selEndScreen), jsValue([]string{selCaptionsOn, selCaptionsOff, selCaptionsMenu, selLeave}))
	p, err := chromedp.Run(ctx, chromedp.Evaluate[pageState](script))
	if err != nil {
		return InCall, err
	}
	for _, text := range []*string{p.EndScreen, p.Page} {
		if text == nil {
			continue
		}
		if _, ok := match(*text, textsRemoved); ok {
			return Removed, nil
		}
		if _, ok := match(*text, textsEnded); ok {
			return Ended, nil
		}
	}
	return InCall, nil
}

// Leave hangs up. A missing Leave button is not an error once the call has
// already ended or the bot has been removed.
func (s *Session) Leave(ctx context.Context) error {
	ctx, done := s.bind(ctx)
	defer done()

	clicked, err := jsClick(ctx, selLeave)
	if err != nil {
		return err
	}
	if !clicked {
		st, err := status(ctx)
		if err != nil {
			return err
		}
		if st == Ended || st == Removed {
			return nil
		}
		return fmt.Errorf("meet: leave button not found (looked for %s)", selLeave)
	}
	// Meet asks a host whether to end the call for everyone.
	shown, err := waitFor(ctx, justLeaveTimeout, func() (bool, error) { return present(ctx, xpJustLeave) })
	if err != nil || !shown {
		return err
	}
	return click(ctx, "Just leave the meeting button", xpJustLeave)
}

// isXPath tells the XPath expressions in selectors.go from the CSS selectors.
func isXPath(sel string) bool { return strings.HasPrefix(sel, "/") }

// jsValue renders v as a JavaScript literal, so that nothing spliced into a
// script can break out of it.
func jsValue(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // only strings and string slices come through here
	}
	return string(b)
}

func jsString(s string) string { return jsValue(s) }

// elementJS is a JavaScript expression for the first element sel matches, or
// null.
func elementJS(sel string) string {
	if isXPath(sel) {
		return fmt.Sprintf(`document.evaluate(%s, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue`, jsString(sel))
	}
	return fmt.Sprintf(`document.querySelector(%s)`, jsString(sel))
}

// present reports whether any of the selectors matches an element now,
// without waiting.
func present(ctx context.Context, sels ...string) (bool, error) {
	checks := make([]string, len(sels))
	for i, sel := range sels {
		checks[i] = "(" + elementJS(sel) + ") !== null"
	}
	return chromedp.Run(ctx, chromedp.Evaluate[bool](strings.Join(checks, " || ")))
}

// pageText returns the page's visible text with typographic apostrophes
// turned into plain ones, to match the texts in selectors.go.
func pageText(ctx context.Context) (string, error) {
	return chromedp.Run(ctx, chromedp.Evaluate[string](`(document.body ? document.body.innerText : "").replace(/\u2019/g, "'")`))
}

// match returns the first entry of texts that text contains.
func match(text string, texts []string) (string, bool) {
	for _, t := range texts {
		if strings.Contains(text, t) {
			return t, true
		}
	}
	return "", false
}

// notFoundError reports a control that did not become visible within
// clickTimeout; naming it is what makes a Meet layout change diagnosable.
type notFoundError struct{ what, sel string }

func (e *notFoundError) Error() string {
	return fmt.Sprintf("meet: %s not found (looked for %s)", e.what, e.sel)
}

// visibleAction and clickAction query the first element sel matches. An XPath
// goes through document.evaluate rather than DOM.performSearch, which returns
// every match and would make the visibility wait cover all of them.
func visibleAction(sel string) chromedp.Action[chromedp.Void] {
	if isXPath(sel) {
		return chromedp.WaitVisible(chromedp.JSPath(elementJS(sel)))
	}
	return chromedp.WaitVisible(chromedp.CSS(sel))
}

func clickAction(sel string) chromedp.Action[chromedp.Void] {
	if isXPath(sel) {
		return chromedp.Click(chromedp.JSPath(elementJS(sel)))
	}
	return chromedp.Click(chromedp.CSS(sel))
}

// bounded runs the actions under clickTimeout, turning that timeout (but not
// the end of ctx) into a notFoundError for the control.
func bounded(ctx context.Context, what, sel string, actions ...chromedp.Action[chromedp.Void]) error {
	wc, cancel := context.WithTimeout(ctx, clickTimeout)
	defer cancel()
	err := chromedp.Do(wc, actions...)
	if err != nil && ctx.Err() == nil && wc.Err() != nil {
		return &notFoundError{what, sel}
	}
	return err
}

// waitVisible waits up to clickTimeout for sel to be visible.
func waitVisible(ctx context.Context, what, sel string) error {
	return bounded(ctx, what, sel, visibleAction(sel))
}

// click waits for sel to be visible and clicks it with a CDP mouse click, all
// within clickTimeout, so an element that vanishes between the two steps
// cannot stall the caller.
func click(ctx context.Context, what, sel string) error {
	return bounded(ctx, what, sel, visibleAction(sel), clickAction(sel))
}

// clickIfPresent clicks an optional control that is on the page now. If it
// does not become clickable in time, Join can still go on, so that is logged
// rather than returned.
func (s *Session) clickIfPresent(ctx context.Context, what, sel string) error {
	shown, err := present(ctx, sel)
	if err != nil || !shown {
		return err
	}
	err = click(ctx, what, sel)
	if nf := (*notFoundError)(nil); errors.As(err, &nf) {
		s.log.Warn("optional control not clicked; carrying on", "control", what, "error", err)
		return nil
	}
	return err
}

// jsClick clicks sel with JavaScript's element.click(), which Meet honours on
// in-call buttons. It reports whether the element was there to click.
func jsClick(ctx context.Context, sel string) (bool, error) {
	script := fmt.Sprintf(`(() => { const e = %s; if (e === null) { return false; } e.click(); return true; })()`, elementJS(sel))
	return chromedp.Run(ctx, chromedp.Evaluate[bool](script))
}

// waitGone waits up to d for sel to disappear, and reports whether it did.
func waitGone(ctx context.Context, sel string, d time.Duration) (bool, error) {
	return waitFor(ctx, d, func() (bool, error) {
		there, err := present(ctx, sel)
		return !there, err
	})
}

// waitFor calls cond every checkInterval until it is true or d has passed.
func waitFor(ctx context.Context, d time.Duration, cond func() (bool, error)) (bool, error) {
	deadline := time.Now().Add(d)
	for {
		ok, err := cond()
		if err != nil || ok {
			return ok, err
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		if err := sleep(ctx, checkInterval); err != nil {
			return false, err
		}
	}
}

// sleep waits for d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// deref returns *p, or "<none>" when p is nil.
func deref(p *string) string {
	if p == nil {
		return "<none>"
	}
	return *p
}
