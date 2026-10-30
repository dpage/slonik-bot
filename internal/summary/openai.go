package summary

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dpage/slonik-bot/internal/config"
)

// maxErrorBody caps how much of a failed response is quoted in an error.
const maxErrorBody = 512

// OpenAI implements LLM against an OpenAI-compatible chat completions API.
type OpenAI struct {
	hc    *http.Client
	url   string
	model string
	max   int
	key   string
}

// NewOpenAI builds a provider for the given settings. The key may be empty for
// local servers; a nil client gets a long timeout suited to slow local models.
func NewOpenAI(cfg config.LLM, key string, hc *http.Client) *OpenAI {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Minute}
	}
	return &OpenAI{
		hc:    hc,
		url:   strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions",
		model: cfg.ResolvedModel(),
		max:   cfg.MaxOutputTokens,
		key:   key,
	}
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIRequest struct {
	Model          string          `json:"model"`
	MaxTokens      int             `json:"max_tokens"`
	ResponseFormat map[string]any  `json:"response_format"`
	Messages       []openAIMessage `json:"messages"`
}

type openAIResponse struct {
	Choices []struct {
		FinishReason string        `json:"finish_reason"`
		Message      openAIMessage `json:"message"`
	} `json:"choices"`
}

// Complete sends the prompts and returns the model's reply. The schema is
// included in the system prompt, as not every server enforces one itself.
func (o *OpenAI) Complete(ctx context.Context, system, user string, schema map[string]any) (string, error) {
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return "", fmt.Errorf("summary: encoding the schema: %w", err)
	}
	body, err := json.Marshal(openAIRequest{
		Model:          o.model,
		MaxTokens:      o.max,
		ResponseFormat: map[string]any{"type": "json_object"},
		Messages: []openAIMessage{
			{Role: "system", Content: system + "\n\nReply with a single JSON object matching this JSON Schema:\n" + string(schemaJSON)},
			{Role: "user", Content: user},
		},
	})
	if err != nil {
		return "", fmt.Errorf("summary: encoding the request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("summary: building the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if o.key != "" {
		req.Header.Set("Authorization", "Bearer "+o.key)
	}

	resp, err := o.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("summary: openai request failed: %w", redact(err, o.key))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody+1))
		cut := len(b) > maxErrorBody
		if cut {
			b = b[:maxErrorBody]
		}
		return "", fmt.Errorf("summary: openai request failed: %s: %s", resp.Status, strings.TrimSpace(redactQuote(string(b), o.key, cut)))
	}

	var out openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("summary: reading the model response: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", errors.New("summary: the model returned no choices")
	}
	c := out.Choices[0]
	if c.FinishReason == "length" {
		return "", fmt.Errorf("summary: the response was cut off at %d tokens; raise llm.max_output_tokens", o.max)
	}
	return c.Message.Content, nil
}

// redactQuote hides key in a server's reply before it is quoted in an error,
// since a server or proxy can echo the request's credentials back. When the
// reply was cut short, the cut may have split a copy of the key, so a tail
// that could be the start of it is dropped as well.
func redactQuote(s, key string, cut bool) string {
	if key == "" {
		return s
	}
	s = strings.ReplaceAll(s, key, "[redacted]")
	if cut {
		for i := len(key) - 1; i > 0; i-- {
			if strings.HasSuffix(s, key[:i]) {
				return s[:len(s)-i]
			}
		}
	}
	return s
}
