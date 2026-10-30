package summary

import (
	"strings"

	"github.com/dpage/slonik-bot/internal/config"
)

// New builds the LLM provider selected by the configuration, reading its API
// key from the environment.
func New(cfg config.LLM) (LLM, error) {
	key, err := cfg.APIKey()
	if err != nil {
		return nil, err
	}
	if cfg.Provider == "openai" {
		return NewOpenAI(cfg, key, nil), nil
	}
	return NewAnthropic(cfg, key), nil
}

// redacted hides key in an error's message. The providers quote the server's
// reply in their errors, and a proxy or local server can echo the request's
// credentials back in it; the error is then printed and logged.
type redacted struct {
	err error
	key string
}

func redact(err error, key string) error {
	if err == nil || key == "" {
		return err
	}
	return redacted{err, key}
}

func (r redacted) Error() string { return strings.ReplaceAll(r.err.Error(), r.key, "[redacted]") }
func (r redacted) Unwrap() error { return r.err }
