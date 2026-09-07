package config

import (
	"fmt"
	"strings"
)

// SetLanguage pins the CLI UI/model language; empty/auto clears the override so runtime detection falls back to REASONIX_LANG / locale.
func (c *Config) SetLanguage(lang string) error {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "", "auto":
		c.Language = ""
	case "en":
		c.Language = "en"
	case "zh":
		c.Language = "zh"
	case "es", "spanish":
		c.Language = "es"
	default:
		return fmt.Errorf("language %q: must be auto|en|zh|es", lang)
	}
	c.ApplyDeepSeekOfficialDefaultPricing()
	return nil
}

// SetReasoningLanguage pins the preferred language for visible reasoning text.
// Empty/auto follows the conversation language.
func (c *Config) SetReasoningLanguage(lang string) error {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "", "auto", "follow", "conversation", "detect", "default", "model", "model-default", "model_default", "provider":
		c.Agent.ReasoningLanguage = ""
	case "zh", "cn", "chinese", "中文":
		c.Agent.ReasoningLanguage = "zh"
	case "en", "english":
		c.Agent.ReasoningLanguage = "en"
	default:
		return fmt.Errorf("reasoning language %q: must be auto|zh|en", lang)
	}
	return nil
}
