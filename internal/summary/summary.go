// Package summary turns a meeting transcript into minutes using an LLM.
package summary

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

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

// ReplyError is returned when the model answered but its reply could not be
// used. Raw is the reply, kept so that the failure can be looked into.
type ReplyError struct {
	Raw string
	Err error
}

func (e *ReplyError) Error() string { return e.Err.Error() }
func (e *ReplyError) Unwrap() error { return e.Err }

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

// property is one entry in an object schema. Properties are kept in a slice
// rather than a map because the model writes the fields in the order the
// schema lists them, and a map would be sent in alphabetical order, putting
// the title after the whole summary.
type property struct {
	name   string
	schema map[string]any
}

type properties []property

func (ps properties) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range ps {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(p.name)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(p.schema)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// object lists every property as required, so that none is moved: the model
// writes required properties before optional ones.
func object(props ...property) map[string]any {
	required := make([]string, len(props))
	for i, p := range props {
		required[i] = p.name
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties(props),
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
// The title comes first so that it is written before the long summary.
func Schema() map[string]any {
	str := map[string]any{"type": "string"}
	return object(
		property{"title", str},
		property{"summary", str},
		property{"decisions", array(str)},
		property{"action_items", array(object(
			property{"description", str},
			property{"owner", nullableString()},
			property{"due", nullableString()},
		))},
		property{"corrections", array(object(
			property{"from", str},
			property{"to", str},
		))},
	)
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
	// Models have answered with a placeholder such as "x" for the title.
	if utf8.RuneCountInString(strings.TrimSpace(r.Title)) < 3 {
		return nil, fmt.Errorf("summary: reply's title %q is too short", r.Title)
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
	r, err := ParseResult(raw)
	if err != nil {
		return nil, &ReplyError{Raw: raw, Err: err}
	}
	return r, nil
}
