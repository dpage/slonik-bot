package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRejectsKeyInAPIKeyEnv(t *testing.T) {
	secret := "sk-ant-api03-not-a-real-key"
	c := Default()
	c.LLM.APIKeyEnv = secret
	err := c.Validate()
	if err == nil {
		t.Fatal("api_key_env holding a key accepted")
	}
	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "api_key_env") {
		t.Fatalf("error %q must name the field but not echo the value", err)
	}
	c.LLM.APIKeyEnv = "MY_KEY_2"
	if err := c.Validate(); err != nil {
		t.Fatalf("valid variable name rejected: %v", err)
	}
}

func TestDefaultPathHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	if got, want := DefaultPath(), filepath.Join("/tmp/xdg", "slonik-bot", "config.yaml"); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
