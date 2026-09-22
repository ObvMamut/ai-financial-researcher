package orchestrator

import (
	"maps"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// AcceptanceConfig resolves the same defaults and engine gates used by a run,
// without initializing providers, opening caches, or dispatching model calls.
// The returned configuration contains credentials and must never be serialized.
func AcceptanceConfig(c Config) (Config, bool, error) {
	c.Models = maps.Clone(c.Models)
	c.Binaries = maps.Clone(c.Binaries)
	c.applyDefaults()
	if _, _, _, err := resolveCheapEngine(c); err != nil {
		return Config{}, false, err
	}
	chief, err := resolveChiefEngine(c)
	if err != nil {
		return Config{}, false, err
	}
	_, fallback, err := chiefFallbackAllowed(c, chief)
	for _, api := range []*model.APIConfig{&c.API, &c.Local, &c.ChiefAPI} {
		if api.MaxTokens <= 0 {
			api.MaxTokens = defaultMaxTokens
		}
	}
	return c, fallback, err
}
