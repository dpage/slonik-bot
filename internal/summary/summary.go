// Package summary turns a meeting transcript into minutes using an LLM.
package summary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dpage/slonik-bot/internal/transcript"
)

// ActionItem is a task arising from the meeting. Owner and Due are nil when
// the transcript does not say, rather than being guessed.
type ActionItem struct {
	Description string  `json:"description"`
	Owner       *string `json:"owner"`
	Due         *string `json:"due"`
}

// Result is the structured minutes produced by the model.
type Result struct {
	Title       string                  `json:"title"`
	Summary     string                  `json:"summary"`
	Decisions   []string                `json:"decisions"`
	ActionItems []ActionItem            `json:"action_items"`
	Corrections []transcript.Correction `json:"corrections"`
}

// LLM is one provider. Complete returns the model's raw text, which should be
// a JSON object matching schema.
type LLM interface {
	Complete(ctx context.Context, system, user string, schema map[string]any) (string, error)
}

func nullableString() map[string]any {
	return map[string]any{"anyOf": []any{
		map[string]any{"type": "string"},
		map[string]any{"type": "null"},
	}}
}

func object(props map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}
}

func array(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

// Schema returns the JSON Schema for Result. Providers' structured-output
// modes insist that every object forbids extra properties and lists every
// property as required, so optional values are expressed as string-or-null.
func Schema() map[string]any {
	str := map[string]any{"type": "string"}
	return object(map[string]any{
		"title":     str,
		"summary":   str,
		"decisions": array(str),
		"action_items": array(object(map[string]any{
			"description": str,
			"owner":       nullableString(),
			"due":         nullableString(),
		}, "description", "owner", "due")),
		"corrections": array(object(map[string]any{
			"from": str,
			"to":   str,
		}, "from", "to")),
	}, "title", "summary", "decisions", "action_items", "corrections")
}

// ParseResult decodes the model's reply. Models sometimes wrap the JSON in
// prose or a code fence despite instructions, so the outermost braces are
// taken; unknown fields are tolerated because extras are harmless.
func ParseResult(raw string) (*Result, error) {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, "{") {
		i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
		if i < 0 || j < i {
			return nil, errors.New("summary: reply contains no JSON object")
		}
		s = s[i : j+1]
	}
	var r Result
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return nil, fmt.Errorf("summary: parsing reply: %w", err)
	}
	if strings.TrimSpace(r.Title) == "" || strings.TrimSpace(r.Summary) == "" {
		return nil, errors.New("summary: reply lacks a title or summary")
	}
	if r.Decisions == nil {
		r.Decisions = []string{}
	}
	if r.ActionItems == nil {
		r.ActionItems = []ActionItem{}
	}
	if r.Corrections == nil {
		r.Corrections = []transcript.Correction{}
	}
	return &r, nil
}

// Summarise asks the model for minutes. An empty transcript is refused up
// front so that no tokens are spent inventing a meeting.
func Summarise(ctx context.Context, llm LLM, m transcript.Meeting, us []transcript.Utterance) (*Result, error) {
	if len(us) == 0 {
		return nil, errors.New("summary: transcript is empty")
	}
	system, user := BuildPrompt(m, us)
	raw, err := llm.Complete(ctx, system, user, Schema())
	if err != nil {
		return nil, fmt.Errorf("summary: %w", err)
	}
	return ParseResult(raw)
}
