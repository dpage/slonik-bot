package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dpage/slonik-bot/internal/config"
)

const testToken = "test-token"

func newTestServer() *Server {
	cfg := config.Default()
	cfg.ChatNotice = "Test notice"
	return New(Options{Config: cfg, Port: 7381, Token: testToken})
}

// do sends a request through the handler with the given host, token and
// origin; an empty token or origin means the header is left out.
func do(s *Server, method, path, host, token, origin string, body *strings.Reader) *httptest.ResponseRecorder {
	if body == nil {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, body)
	req.Host = host
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHelloNeedsToken(t *testing.T) {
	s := newTestServer()
	if rec := do(s, "GET", "/v1/hello", "127.0.0.1:7381", "", "", nil); rec.Code != 401 {
		t.Errorf("no token: got %d, want 401", rec.Code)
	}
	if rec := do(s, "GET", "/v1/hello", "127.0.0.1:7381", "wrong", "", nil); rec.Code != 401 {
		t.Errorf("wrong token: got %d, want 401", rec.Code)
	}
}

func TestHello(t *testing.T) {
	s := newTestServer()
	rec := do(s, "GET", "/v1/hello", "127.0.0.1:7381", testToken, "", nil)
	if rec.Code != 200 {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	var got struct {
		Version    string `json:"version"`
		ChatNotice string `json:"chat_notice"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ChatNotice != "Test notice" || got.Version == "" {
		t.Errorf("unexpected body %+v", got)
	}
}

func TestHostChecked(t *testing.T) {
	s := newTestServer()
	for host, want := range map[string]int{
		"evil.example:7381": 403,
		"localhost:7381":    200,
		"127.0.0.1:7381":    200,
		"127.0.0.1:9999":    403,
	} {
		if rec := do(s, "GET", "/v1/hello", host, testToken, "", nil); rec.Code != want {
			t.Errorf("host %q: got %d, want %d", host, rec.Code, want)
		}
	}
}

func TestOriginChecked(t *testing.T) {
	s := newTestServer()
	if rec := do(s, "GET", "/v1/hello", "127.0.0.1:7381", testToken, "https://evil.example", nil); rec.Code != 403 {
		t.Errorf("bad origin: got %d, want 403", rec.Code)
	}
	rec := do(s, "GET", "/v1/hello", "127.0.0.1:7381", testToken, "chrome-extension://abcdef", nil)
	if rec.Code != 200 {
		t.Fatalf("extension origin: got %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "chrome-extension://abcdef" {
		t.Errorf("Allow-Origin = %q", got)
	}
	if rec := do(s, "GET", "/v1/hello", "127.0.0.1:7381", testToken, "", nil); rec.Code != 200 {
		t.Errorf("no origin: got %d, want 200", rec.Code)
	}
}

func TestPreflight(t *testing.T) {
	s := newTestServer()
	rec := do(s, "OPTIONS", "/v1/sessions", "127.0.0.1:7381", "", "chrome-extension://abcdef", nil)
	if rec.Code != 204 {
		t.Fatalf("got %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "chrome-extension://abcdef" {
		t.Errorf("Allow-Origin = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Authorization, Content-Type" {
		t.Errorf("Allow-Headers = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST" {
		t.Errorf("Allow-Methods = %q", got)
	}
	if rec := do(s, "OPTIONS", "/v1/sessions", "127.0.0.1:7381", "", "https://evil.example", nil); rec.Code != 403 {
		t.Errorf("preflight from bad origin: got %d, want 403", rec.Code)
	}
}

func TestBodyCapped(t *testing.T) {
	s := newTestServer()
	big := strings.NewReader(strings.Repeat("x", 2<<20))
	rec := do(s, "POST", "/v1/sessions", "127.0.0.1:7381", testToken, "", big)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("got %d, want 413", rec.Code)
	}
}

func TestBodyOfUnknownLengthCapped(t *testing.T) {
	s := newTestServer()
	// Valid JSON so far, so the decoder keeps reading until the cap stops it.
	big := `{"code":"` + strings.Repeat("x", 2<<20) + `"}`
	req := httptest.NewRequest("POST", "/v1/sessions", strings.NewReader(big))
	req.Host = "127.0.0.1:7381"
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("got %d, want 413", rec.Code)
	}
}

func TestUnknownPath(t *testing.T) {
	s := newTestServer()
	if rec := do(s, "GET", "/v1/nothing", "127.0.0.1:7381", testToken, "", nil); rec.Code != 404 {
		t.Errorf("got %d, want 404", rec.Code)
	}
}
