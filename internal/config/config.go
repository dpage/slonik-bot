// Package config loads slonik-bot's YAML configuration.
package config

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Example is a commented configuration file, for printing in help output.
//
//go:embed example.yaml
var Example string

// LLM selects and configures the model that writes the summary.
type LLM struct {
	Provider        string `yaml:"provider"` // "anthropic" | "openai"
	Model           string `yaml:"model"`
	BaseURL         string `yaml:"base_url"`
	APIKeyEnv       string `yaml:"api_key_env"`
	MaxOutputTokens int    `yaml:"max_output_tokens"`
}

// Config is the whole configuration file.
type Config struct {
	BotName         string        `yaml:"bot_name"`
	ChatNotice      string        `yaml:"chat_notice"`
	CaptionLanguage string        `yaml:"caption_language"`
	ProfileDir      string        `yaml:"profile_dir"`
	ChromePath      string        `yaml:"chrome_path"`
	OutputDir       string        `yaml:"output_dir"`
	MaxDuration     time.Duration `yaml:"max_duration"`
	AloneTimeout    time.Duration `yaml:"alone_timeout"`
	LLM             LLM           `yaml:"llm"`
}

const (
	providerAnthropic = "anthropic"
	providerOpenAI    = "openai"
)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// homeDir returns the user's home directory, or "" if it cannot be found; the
// defaults then become relative paths rather than a startup failure.
func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// configDir is the directory holding the config file and the Chrome profile.
// It is ~/.config/slonik-bot on every OS so that there is one documented
// location, rather than whatever the platform's convention happens to be.
func configDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "slonik-bot")
	}
	return filepath.Join(homeDir(), ".config", "slonik-bot")
}

// DefaultPath is where the config file is looked for when --config is absent.
func DefaultPath() string {
	return filepath.Join(configDir(), "config.yaml")
}

// Default returns the configuration used for any key the file leaves out.
func Default() Config {
	return Config{
		BotName:         "Slonik Bot (transcribing)",
		ChatNotice:      "Slonik Bot is transcribing this call. The summary, action items and transcript will be shared with participants.",
		CaptionLanguage: "English",
		ProfileDir:      filepath.Join(configDir(), "chrome-profile"),
		OutputDir:       filepath.Join(homeDir(), "slonik-bot-notes"),
		MaxDuration:     4 * time.Hour,
		AloneTimeout:    60 * time.Second,
		LLM: LLM{
			Provider:        providerAnthropic,
			MaxOutputTokens: 16000,
		},
	}
}

// Load reads the file at path over the defaults. A missing file is an error
// only when explicit is true, that is when the user named the file.
func Load(path string, explicit bool) (Config, error) {
	c := Default()
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !explicit {
			return c, nil
		}
		return Config{}, fmt.Errorf("config: %w", err)
	}
	defer func() { _ = f.Close() }()

	// Unknown keys are an error because a misspelt key would otherwise be
	// silently ignored and the default used instead.
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("config: %s: %w", path, err)
	}

	c.ProfileDir = expandHome(c.ProfileDir)
	c.OutputDir = expandHome(c.OutputDir)
	c.ChromePath = expandHome(c.ChromePath)
	return c, nil
}

func expandHome(p string) string {
	if p == "~" {
		return homeDir()
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(homeDir(), rest)
	}
	return p
}

// Validate checks the configuration is usable, so that a mistake is reported
// before the bot joins a meeting rather than after it.
func (c Config) Validate() error {
	switch c.LLM.Provider {
	case providerAnthropic:
	case providerOpenAI:
		if c.LLM.Model == "" || c.LLM.BaseURL == "" {
			return errors.New("config: llm.model and llm.base_url are required when llm.provider is openai")
		}
	default:
		return fmt.Errorf("config: llm.provider %q is not one of anthropic, openai", c.LLM.Provider)
	}
	// A pasted key would otherwise be echoed back by APIKey's error message.
	if !envName.MatchString(c.LLM.APIKeyEnv) && c.LLM.APIKeyEnv != "" {
		return errors.New("config: llm.api_key_env must be the name of an environment variable, not the key itself")
	}
	if c.MaxDuration <= 0 {
		return errors.New("config: max_duration must be positive")
	}
	if c.AloneTimeout <= 0 {
		return errors.New("config: alone_timeout must be positive")
	}
	if c.LLM.MaxOutputTokens <= 0 {
		return errors.New("config: llm.max_output_tokens must be positive")
	}
	return nil
}

// ResolvedModel is the configured model, or the provider's default.
func (l LLM) ResolvedModel() string {
	if l.Model != "" {
		return l.Model
	}
	if l.Provider == providerAnthropic {
		return "claude-opus-5-5"
	}
	return ""
}

// KeyEnv is the name of the environment variable holding the API key.
func (l LLM) KeyEnv() string {
	if l.APIKeyEnv != "" {
		return l.APIKeyEnv
	}
	if l.Provider == providerOpenAI {
		return "OPENAI_API_KEY"
	}
	return "ANTHROPIC_API_KEY"
}

// KeyFile is the file in the home directory that supplies the API key when the
// environment variable is unset: ~/.openai-api-key for openai, otherwise
// ~/.anthropic-api-key. It is empty if the home directory cannot be found.
func (l LLM) KeyFile() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	if l.Provider == providerOpenAI {
		return filepath.Join(home, ".openai-api-key")
	}
	return filepath.Join(home, ".anthropic-api-key")
}

// APIKey reads the key from the environment variable named by KeyEnv and, if
// that is empty, from KeyFile; it is never kept in the config file. A missing
// or blank key file means no key. OpenAI-compatible local servers usually need
// no key, so an empty one is allowed for that provider. The key itself never
// appears in an error or log message.
func (l LLM) APIKey() (string, error) {
	env := l.KeyEnv()
	if k := os.Getenv(env); k != "" {
		return k, nil
	}
	path := l.KeyFile()
	k, err := readKeyFile(path)
	if err != nil {
		return "", err
	}
	if k == "" && l.Provider == providerAnthropic {
		if path == "" {
			return "", fmt.Errorf("config: set %s to your Anthropic API key", env)
		}
		return "", fmt.Errorf("config: set %s or put your Anthropic API key in %s", env, path)
	}
	return k, nil
}

// readKeyFile returns the trimmed contents of path, or "" if the path is empty
// or the file does not exist. Errors name the path, never the contents.
func readKeyFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("config: reading API key file %s: %w", path, err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", nil
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o077 != 0 {
			slog.Warn("API key file is readable by other users; run chmod 600 on it",
				"path", path, "fix", "chmod 600 "+path)
		}
	}
	return key, nil
}
