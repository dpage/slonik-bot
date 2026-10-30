package summary

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/dpage/slonik-bot/internal/config"
)

// Anthropic implements LLM using the Anthropic Messages API.
type Anthropic struct {
	client anthropic.Client
	model  string
	max    int64
}

// NewAnthropic builds a provider for the given settings and API key. Extra
// request options (such as a base URL for tests) are applied after the key.
func NewAnthropic(cfg config.LLM, key string, opts ...option.RequestOption) *Anthropic {
	all := append([]option.RequestOption{option.WithAPIKey(key)}, opts...)
	return &Anthropic{
		client: anthropic.NewClient(all...),
		model:  cfg.ResolvedModel(),
		max:    int64(cfg.MaxOutputTokens),
	}
}

// Complete sends the prompts and returns the model's text, constrained to the
// given JSON schema.
func (a *Anthropic) Complete(ctx context.Context, system, user string, schema map[string]any) (string, error) {
	params := anthropic.MessageNewParams{
		Model:     a.model,
		MaxTokens: a.max,
		System: []anthropic.TextBlockParam{{
			Text:         system,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(user))},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffortHigh,
			Format: anthropic.JSONOutputFormatParam{Schema: schema},
		},
	}
	// The SDK has no typed field for fallbacks, so set it in the body directly.
	stream := a.client.Messages.NewStreaming(ctx, params,
		option.WithJSONSet("fallbacks", "default"),
		option.WithHeaderAdd("anthropic-beta", "server-side-fallback-2026-07-01"),
	)
	defer func() { _ = stream.Close() }()

	var message anthropic.Message
	for stream.Next() {
		if err := message.Accumulate(stream.Current()); err != nil {
			return "", fmt.Errorf("summary: reading the model response: %w", err)
		}
	}
	if err := stream.Err(); err != nil {
		return "", fmt.Errorf("summary: anthropic request failed: %w", err)
	}

	switch message.StopReason {
	case anthropic.StopReasonRefusal:
		return "", fmt.Errorf("summary: the model declined (%s): %s",
			message.StopDetails.Category, message.StopDetails.Explanation)
	case anthropic.StopReasonMaxTokens:
		return "", fmt.Errorf("summary: the response was cut off at %d tokens; raise llm.max_output_tokens", a.max)
	}

	var out strings.Builder
	for _, block := range message.Content {
		if block.Type == "text" {
			out.WriteString(block.AsText().Text)
		}
	}
	return out.String(), nil
}
