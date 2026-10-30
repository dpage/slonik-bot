package meet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/dpage/slonik-bot/internal/browser"
)

// vanishingPage holds a button that hides itself the first time chromedp's
// visibility check reads its offsetWidth, so the element is visible for the
// wait and gone for the click that follows.
const vanishingPage = `<!DOCTYPE html><html><body>
<button aria-label="Vanishing">Vanishing</button>
<script>
const b = document.querySelector('button');
const width = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetWidth').get;
Object.defineProperty(b, 'offsetWidth', {get() { const w = width.call(b); b.style.display = 'none'; return w; }});
</script></body></html>`

func TestClickIsBoundedWhenTheElementVanishes(t *testing.T) {
	path := browser.Find("")
	if path == "" {
		t.Skip("no Chrome or Chromium found")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(vanishingPage))
	}))
	defer srv.Close()
	b, err := browser.Launch(context.Background(), browser.Options{ProfileDir: t.TempDir(), ChromePath: path, Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := chromedp.Do(b.Context(), chromedp.Navigate(srv.URL)); err != nil {
		t.Fatal(err)
	}

	// The caller allows longer than clickTimeout, so only click's own bound
	// can produce the "not found" error in time.
	ctx, cancel := context.WithTimeout(b.Context(), clickTimeout+5*time.Second)
	defer cancel()
	start := time.Now()
	err = click(ctx, "vanishing button", `button[aria-label="Vanishing"]`)
	if err == nil || !strings.Contains(err.Error(), "vanishing button not found") {
		t.Fatalf("after %s: %v", time.Since(start).Round(time.Second), err)
	}
}
