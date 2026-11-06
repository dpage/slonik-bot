package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/dpage/slonik-bot/internal/browser"
	"github.com/dpage/slonik-bot/internal/meet/fakemeet"
	"github.com/dpage/slonik-bot/internal/transcript"
)

// call is the fake Meet page in headless Chrome, joined, with the extension's
// page scripts loaded and a transcriber, window.__t, posting to srv.
type call struct {
	t    *testing.T
	ctx  context.Context
	meet *fakemeet.Server
	srv  *running
}

// The shim stands in for the extension's content script: request is fetch
// with the token. While window.__fail is above zero, a request fails: first
// with a network error before anything is sent, then with a 503 that hides
// the server's real answer, so that the events it carried are sent again.
// window.__created counts the sessions the server has created.
const shim = `(() => {
  const base = %s, token = %s;
  window.__fail = 0;
  window.__created = 0;
  const request = async (method, path, body) => {
    if (window.__fail > 1) {
      window.__fail--;
      throw new TypeError('Failed to fetch');
    }
    const r = await fetch(base + path, {
      method,
      headers: { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' },
      body: JSON.stringify(body || {}),
    });
    const answer = { status: r.status, body: await r.json() };
    if (path === '/v1/sessions' && r.status === 201) window.__created++;
    if (window.__fail > 0) {
      window.__fail--;
      return { status: 503, body: { error: 'test failure' } };
    }
    return answer;
  };
  const t = SlonikTranscriber.create({
    request,
    settings: { displayName: 'Carol Example', hideCaptions: true },
    tab: '1',
    code: 'abc-defg-hij',
    now: () => Date.now(),
    retryMs: 50,
  });
  window.__t = t;
  window.__slonikCaption = t.onCaption;
})()`

// joinCall starts the server, opens and joins the fake call, and loads the
// extension's scripts into it.
func joinCall(t *testing.T) *call {
	t.Helper()
	path := browser.Find("")
	if path == "" {
		t.Skip("no Chrome or Chromium found")
	}
	m := fakemeet.New()
	t.Cleanup(m.Close)
	srv := start(t, func(o *Options) {
		o.AllowOrigin = func(origin string) bool { return origin == m.URL }
		o.Idle = 200 * time.Millisecond
		o.Tick = 20 * time.Millisecond
	})
	b, err := browser.Launch(context.Background(), browser.Options{ProfileDir: t.TempDir(), ChromePath: path, Headless: true, FakeDevices: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	ctx, cancel := context.WithTimeout(b.Context(), 60*time.Second)
	t.Cleanup(cancel)
	c := &call{t: t, ctx: ctx, meet: m, srv: srv}

	if err := chromedp.Do(ctx, chromedp.Navigate(m.URL+"/abc-defg-hij")); err != nil {
		t.Fatal(err)
	}
	c.eval(`new Promise((resolve, reject) => {
  const end = Date.now() + 10000;
  const look = () => {
    const b = Array.from(document.querySelectorAll('button')).find((b) => b.textContent.trim() === 'Join now');
    if (b) { b.click(); resolve(true); }
    else if (Date.now() > end) reject(new Error('no Join now button'));
    else setTimeout(look, 100);
  };
  look();
})`, nil)
	until(t, "joined", m.Joined)

	for _, name := range []string{"selectors.js", "transcriber.js"} {
		src, err := os.ReadFile(filepath.Join("..", "..", "extension", name))
		if err != nil {
			t.Fatal(err)
		}
		c.eval(string(src), nil)
	}
	c.eval(fmt.Sprintf(shim, jsonString(t, srv.base), jsonString(t, testToken)), nil)
	src, err := os.ReadFile(filepath.Join("..", "..", "extension", "observer.js"))
	if err != nil {
		t.Fatal(err)
	}
	c.eval(string(src), nil)
	return c
}

func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// eval runs script in the page, awaiting a promise, and decodes the result
// into out unless it is nil.
func (c *call) eval(script string, out any) {
	c.t.Helper()
	raw, err := chromedp.Run(c.ctx, chromedp.Evaluate[[]byte](script, chromedp.EvalAwaitPromise))
	if err != nil {
		c.t.Fatalf("%v\nin %.200s", err, script)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("decoding %s: %v", raw, err)
		}
	}
}

type startResult struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error"`
	ChatError string `json:"chatError"`
	Dir       string `json:"dir"`
}

func (c *call) start() startResult {
	c.t.Helper()
	var r startResult
	c.eval(`window.__t.start()`, &r)
	if !r.OK || r.ChatError != "" || r.Dir == "" {
		c.t.Fatalf("start: %+v", r)
	}
	return r
}

type transcriberStatus struct {
	Transcribing bool   `json:"transcribing"`
	Queued       int    `json:"queued"`
	Acked        int    `json:"acked"`
	ChatError    string `json:"chatError"`
	LastError    string `json:"lastError"`
}

func (c *call) status() transcriberStatus {
	c.t.Helper()
	var s transcriberStatus
	c.eval(`window.__t.status()`, &s)
	return s
}

// sent waits until the transcriber has had at least n events acknowledged
// and has nothing left queued.
func (c *call) sent(n int) {
	c.t.Helper()
	until(c.t, fmt.Sprintf("%d events acknowledged", n), func() bool {
		s := c.status()
		return s.Acked >= n && s.Queued == 0
	})
}

func (c *call) stop() string {
	c.t.Helper()
	var r struct {
		Dir   string `json:"dir"`
		Error string `json:"error"`
	}
	c.eval(`window.__t.stop()`, &r)
	if r.Error != "" || r.Dir == "" {
		c.t.Fatalf("stop: %+v", r)
	}
	return r.Dir
}

// until polls cond for up to ten seconds.
func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// texts returns the text of every utterance in dir's transcript.jsonl.
func texts(t *testing.T, dir string) []string {
	t.Helper()
	us, err := transcript.Read(filepath.Join(dir, "transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, u := range us {
		out = append(out, u.Text)
	}
	return out
}

func TestTranscriberRecordsFakeMeet(t *testing.T) {
	c := joinCall(t)
	r := c.start()
	until(t, "captions on", c.meet.CaptionsOn)
	until(t, "the chat notice", func() bool {
		got := c.meet.Chat()
		return len(got) == 1 && got[0] == "Test notice"
	})

	c.meet.Caption(1, "Alice Example", "Shall we start?")
	c.meet.Caption(2, "You", "Yes, let's begin.")
	c.meet.End()

	md := filepath.Join(r.Dir, "transcript.md")
	until(t, "transcript.md", func() bool {
		_, err := os.Stat(md)
		return err == nil
	})
	b, err := os.ReadFile(md)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{"Alice Example:** Shall we start?", "Carol Example:** Yes, let's begin."} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript.md lacks %q:\n%s", want, got)
		}
	}
	if s := c.status(); s.Transcribing {
		t.Errorf("still transcribing after the call ended: %+v", s)
	}
}

func TestTranscriberSurvivesServerRestart(t *testing.T) {
	c := joinCall(t)
	first := c.start().Dir
	c.meet.Caption(1, "Alice Example", "Before the restart.")
	c.sent(1)

	c.srv = c.srv.restart()
	c.meet.Caption(2, "Bob Example", "After the restart.")
	until(t, "a second session", func() bool {
		var n int
		c.eval(`window.__created`, &n)
		return n == 2
	})
	c.sent(1)
	second := c.stop()
	c.srv.shutdown()

	if second == first {
		t.Fatalf("both sessions wrote to %s", first)
	}
	if got := strings.Join(texts(t, first), " "); !strings.Contains(got, "Before the restart.") || strings.Contains(got, "After") {
		t.Errorf("first transcript %q", got)
	}
	if got := strings.Join(texts(t, second), " "); !strings.Contains(got, "After the restart.") || strings.Contains(got, "Before") {
		t.Errorf("second transcript %q", got)
	}
}

func TestTranscriberRetriesAfterFailure(t *testing.T) {
	c := joinCall(t)
	dir := c.start().Dir
	c.eval(`window.__fail = 2`, nil)
	c.meet.Caption(1, "Alice Example", "First.")
	c.meet.Caption(2, "Bob Example", "Second.")
	c.meet.RemoveCaption(1)
	c.meet.RemoveCaption(2)
	c.sent(4) // two captions and two removals
	var left int
	c.eval(`window.__fail`, &left)
	if left != 0 {
		t.Fatalf("%d failures not used", left)
	}
	c.stop()
	c.srv.shutdown()

	got := strings.Join(texts(t, dir), " ")
	for _, want := range []string{"First.", "Second."} {
		if n := strings.Count(got, want); n != 1 {
			t.Errorf("%q appears %d times in %q", want, n, got)
		}
	}
}
