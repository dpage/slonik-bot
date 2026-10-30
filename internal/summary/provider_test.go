package summary

import (
	"testing"

	"github.com/dpage/slonik-bot/internal/config"
)

func TestNewSelectsProvider(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "k")
	if l, err := New(config.Default().LLM); err != nil {
		t.Fatal(err)
	} else if _, ok := l.(*Anthropic); !ok {
		t.Fatalf("%T", l)
	}
	if l, err := New(config.LLM{Provider: "openai", Model: "m", BaseURL: "http://localhost:1", MaxOutputTokens: 1}); err != nil {
		t.Fatal(err)
	} else if _, ok := l.(*OpenAI); !ok {
		t.Fatalf("%T", l)
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := New(config.Default().LLM); err == nil {
		t.Fatal("missing key accepted")
	}
}
