package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	c := Default()
	if c.BotName != "Slonik Bot (transcribing)" || c.CaptionLanguage != "English" {
		t.Fatalf("defaults: %+v", c)
	}
	if c.MaxDuration != 4*time.Hour || c.AloneTimeout != 60*time.Second {
		t.Fatalf("durations: %v %v", c.MaxDuration, c.AloneTimeout)
	}
	if c.LLM.Provider != "anthropic" || c.LLM.ResolvedModel() != "claude-opus-5-5" || c.LLM.MaxOutputTokens != 16000 {
		t.Fatalf("llm: %+v", c.LLM)
	}
	if !strings.HasSuffix(c.ProfileDir, filepath.Join("slonik-bot", "chrome-profile")) {
		t.Fatalf("profile dir %q", c.ProfileDir)
	}
}

func TestLoadOverridesAndExpandsHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	p := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(p, []byte("output_dir: ~/notes\nmax_duration: 90m\nllm:\n  provider: openai\n  model: llama3\n  base_url: http://localhost:11434/v1\n"), 0o600)
	c, err := Load(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.OutputDir != filepath.Join(home, "notes") {
		t.Fatalf("output dir %q", c.OutputDir)
	}
	if c.MaxDuration != 90*time.Minute || c.LLM.Provider != "openai" || c.LLM.ResolvedModel() != "llama3" {
		t.Fatalf("%+v", c)
	}
	if c.BotName != "Slonik Bot (transcribing)" {
		t.Fatal("unset keys must keep their defaults")
	}
}

func TestLoadMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope.yaml")
	if _, err := Load(p, false); err != nil {
		t.Fatalf("implicit missing file should give defaults, got %v", err)
	}
	if _, err := Load(p, true); err == nil {
		t.Fatal("explicit missing file should be an error")
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(p, []byte("bot_nme: typo\n"), 0o600)
	if _, err := Load(p, true); err == nil || !strings.Contains(err.Error(), "bot_nme") {
		t.Fatalf("want error naming the bad key, got %v", err)
	}
}

func TestValidate(t *testing.T) {
	c := Default()
	c.LLM.Provider = "gemini"
	if err := c.Validate(); err == nil {
		t.Fatal("unknown provider accepted")
	}
	c = Default()
	c.LLM.Provider = "openai"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("openai without model/base_url accepted: %v", err)
	}
	c = Default()
	c.MaxDuration = 0
	if err := c.Validate(); err == nil {
		t.Fatal("zero max_duration accepted")
	}
}

func TestAPIKey(t *testing.T) {
	l := Default().LLM
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := l.APIKey(); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("want error naming the variable, got %v", err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	if k, err := l.APIKey(); err != nil || k != "sk-test" {
		t.Fatalf("%q %v", k, err)
	}
	o := LLM{Provider: "openai", Model: "m", BaseURL: "http://localhost:1/v1"}
	t.Setenv("OPENAI_API_KEY", "")
	if k, err := o.APIKey(); err != nil || k != "" {
		t.Fatalf("openai with no key should be allowed: %q %v", k, err)
	}
}
