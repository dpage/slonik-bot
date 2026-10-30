package summary

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/dpage/slonik-bot/internal/config"
)

func sse(w http.ResponseWriter, stop string, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	ev := func(name string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
	}
	ev("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5", "content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": 10, "output_tokens": 0}}})
	ev("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	ev("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})
	ev("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	ev("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop}, "usage": map[string]any{"output_tokens": 5}})
	ev("message_stop", map[string]any{"type": "message_stop"})
}

func TestAnthropicRequestAndResponse(t *testing.T) {
	var body map[string]any
	var beta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		beta = r.Header.Get("anthropic-beta")
		sse(w, "end_turn", sample)
	}))
	defer srv.Close()
	cfg := config.Default().LLM
	llm := NewAnthropic(cfg, "test-key", option.WithBaseURL(srv.URL))
	out, err := llm.Complete(context.Background(), "SYS", "USER", Schema())
	if err != nil {
		t.Fatal(err)
	}
	if out != sample {
		t.Fatalf("got %q", out)
	}
	if body["model"] != "claude-opus-5-5" || body["stream"] != true || body["fallbacks"] != "default" {
		t.Fatalf("body %v", body)
	}
	if !strings.Contains(beta, "server-side-fallback-2026-07-01") {
		t.Fatalf("beta header %q", beta)
	}
	oc, _ := body["output_config"].(map[string]any)
	if oc["effort"] != "high" || oc["format"] == nil {
		t.Fatalf("output_config %v", oc)
	}
}

func TestAnthropicMaxTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sse(w, "max_tokens", "{") }))
	defer srv.Close()
	_, err := NewAnthropic(config.Default().LLM, "k", option.WithBaseURL(srv.URL)).Complete(context.Background(), "s", "u", Schema())
	if err == nil || !strings.Contains(err.Error(), "max_output_tokens") {
		t.Fatalf("got %v", err)
	}
}

func TestAnthropicHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	}))
	defer srv.Close()
	_, err := NewAnthropic(config.Default().LLM, "bad", option.WithBaseURL(srv.URL), option.WithMaxRetries(0)).Complete(context.Background(), "s", "u", Schema())
	if err == nil {
		t.Fatal("401 not reported")
	}
}
