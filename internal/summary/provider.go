package summary

import (
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
