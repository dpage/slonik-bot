package summary

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dpage/slonik-bot/internal/config"
)

func TestOpenAIRequestAndResponse(t *testing.T) {
	var body map[string]any
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": sample}}}})
	}))
	defer srv.Close()
	cfg := config.LLM{Provider: "openai", Model: "llama3", BaseURL: srv.URL + "/v1/", MaxOutputTokens: 1000}
	out, err := NewOpenAI(cfg, "", nil).Complete(context.Background(), "SYS", "USER", Schema())
	if err != nil || out != sample {
		t.Fatalf("%q %v", out, err)
	}
	if path != "/v1/chat/completions" || auth != "" || body["model"] != "llama3" {
		t.Fatalf("path %q auth %q body %v", path, auth, body)
	}
	msgs := body["messages"].([]any)
	if !strings.Contains(msgs[0].(map[string]any)["content"].(string), "JSON Schema") {
		t.Fatal("schema not given to the model")
	}
}

func TestOpenAISendsKeyAndReportsErrors(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()
	cfg := config.LLM{Provider: "openai", Model: "m", BaseURL: srv.URL, MaxOutputTokens: 10}
	_, err := NewOpenAI(cfg, "sk-x", nil).Complete(context.Background(), "s", "u", Schema())
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("%v", err)
	}
	if auth != "Bearer sk-x" {
		t.Fatalf("auth %q", auth)
	}
}

func TestOpenAILength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"finish_reason":"length","message":{"content":"{"}}]}`)
	}))
	defer srv.Close()
	cfg := config.LLM{Provider: "openai", Model: "m", BaseURL: srv.URL, MaxOutputTokens: 10}
	if _, err := NewOpenAI(cfg, "", nil).Complete(context.Background(), "s", "u", Schema()); err == nil || !strings.Contains(err.Error(), "max_output_tokens") {
		t.Fatalf("%v", err)
	}
}
