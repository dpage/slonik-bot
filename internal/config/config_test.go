package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	c := Default()
	if c.BotName != "Slonik Bot (Meeting Transcriber)" || c.CaptionLanguage != "English" {
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
	// ~ is expanded against a home of the test's own, never the real one.
	home := t.TempDir()
	t.Setenv("HOME", home)
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
	if c.BotName != "Slonik Bot (Meeting Transcriber)" {
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

// fakeHome points HOME at a temp dir so the real home is never read. Go's
// os.UserHomeDir returns $HOME on every platform except windows and plan9.
func fakeHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	return h
}

// captureLog replaces the default slog logger and returns the text written.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func TestAPIKeyFileFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on windows")
	}
	anth := Default().LLM
	oai := LLM{Provider: "openai", Model: "m", BaseURL: "http://localhost:1/v1"}

	t.Run("env wins over file", func(t *testing.T) {
		h := fakeHome(t)
		os.WriteFile(filepath.Join(h, ".anthropic-api-key"), []byte("sk-test-file"), 0o600)
		t.Setenv("ANTHROPIC_API_KEY", "sk-test-env")
		if k, err := anth.APIKey(); err != nil || k != "sk-test-env" {
			t.Fatalf("%q %v", k, err)
		}
	})

	t.Run("file used, trimmed, no warning at 0600", func(t *testing.T) {
		h := fakeHome(t)
		logs := captureLog(t)
		t.Setenv("ANTHROPIC_API_KEY", "")
		os.WriteFile(filepath.Join(h, ".anthropic-api-key"), []byte("  sk-test-file\n"), 0o600)
		if k, err := anth.APIKey(); err != nil || k != "sk-test-file" {
			t.Fatalf("%q %v", k, err)
		}
		if logs.Len() != 0 {
			t.Fatalf("unexpected log: %s", logs)
		}
	})

	t.Run("loose permissions warn once without leaking the key", func(t *testing.T) {
		h := fakeHome(t)
		logs := captureLog(t)
		t.Setenv("ANTHROPIC_API_KEY", "")
		p := filepath.Join(h, ".anthropic-api-key")
		os.WriteFile(p, []byte("  sk-test-SECRET-123\n"), 0o644)
		os.Chmod(p, 0o644) // defeat the umask
		k, err := anth.APIKey()
		if err != nil || k != "sk-test-SECRET-123" {
			t.Fatalf("%q %v", k, err)
		}
		out := logs.String()
		if strings.Count(out, "level=WARN") != 1 || !strings.Contains(out, p) || !strings.Contains(out, "chmod 600") {
			t.Fatalf("want one warning naming %s, got %q", p, out)
		}
		if strings.Contains(out, "SECRET") {
			t.Fatalf("key leaked into log: %q", out)
		}
	})

	t.Run("custom variable falls back to file", func(t *testing.T) {
		h := fakeHome(t)
		l := anth
		l.APIKeyEnv = "SLONIK_TEST_KEY"
		t.Setenv("SLONIK_TEST_KEY", "")
		os.WriteFile(filepath.Join(h, ".anthropic-api-key"), []byte("sk-test-file"), 0o600)
		if k, err := l.APIKey(); err != nil || k != "sk-test-file" {
			t.Fatalf("%q %v", k, err)
		}
	})

	t.Run("openai reads its own file", func(t *testing.T) {
		h := fakeHome(t)
		t.Setenv("OPENAI_API_KEY", "")
		os.WriteFile(filepath.Join(h, ".openai-api-key"), []byte("sk-test-oai\n"), 0o600)
		if k, err := oai.APIKey(); err != nil || k != "sk-test-oai" {
			t.Fatalf("%q %v", k, err)
		}
	})

	t.Run("openai with neither is allowed", func(t *testing.T) {
		fakeHome(t)
		t.Setenv("OPENAI_API_KEY", "")
		if k, err := oai.APIKey(); err != nil || k != "" {
			t.Fatalf("%q %v", k, err)
		}
	})

	t.Run("empty file counts as no key", func(t *testing.T) {
		h := fakeHome(t)
		t.Setenv("ANTHROPIC_API_KEY", "")
		os.WriteFile(filepath.Join(h, ".anthropic-api-key"), []byte(" \n"), 0o600)
		if _, err := anth.APIKey(); err == nil {
			t.Fatal("whitespace-only file accepted")
		}
	})

	t.Run("anthropic with neither names variable and file", func(t *testing.T) {
		h := fakeHome(t)
		t.Setenv("ANTHROPIC_API_KEY", "")
		_, err := anth.APIKey()
		want := "config: set ANTHROPIC_API_KEY or put your Anthropic API key in " + filepath.Join(h, ".anthropic-api-key")
		if err == nil || err.Error() != want {
			t.Fatalf("want %q, got %v", want, err)
		}
	})

	t.Run("directory is an error naming the path", func(t *testing.T) {
		h := fakeHome(t)
		t.Setenv("ANTHROPIC_API_KEY", "")
		p := filepath.Join(h, ".anthropic-api-key")
		os.Mkdir(p, 0o700)
		if _, err := anth.APIKey(); err == nil || !strings.Contains(err.Error(), p) {
			t.Fatalf("want error naming %s, got %v", p, err)
		}
	})
}

func TestAPIKey(t *testing.T) {
	fakeHome(t)
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
