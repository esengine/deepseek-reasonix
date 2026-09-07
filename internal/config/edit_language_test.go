package config

import "testing"

func TestSetLanguage(t *testing.T) {
	c := Default()
	if err := c.SetLanguage("zh"); err != nil {
		t.Fatalf("SetLanguage zh: %v", err)
	}
	if c.Language != "zh" {
		t.Fatalf("language = %q, want zh", c.Language)
	}
	if err := c.SetLanguage("es"); err != nil {
		t.Fatalf("SetLanguage es: %v", err)
	}
	if c.Language != "es" {
		t.Fatalf("language = %q, want es", c.Language)
	}
	if err := c.SetLanguage("spanish"); err != nil {
		t.Fatalf("SetLanguage spanish: %v", err)
	}
	if c.Language != "es" {
		t.Fatalf("language = %q, want es (from spanish)", c.Language)
	}
	if err := c.SetLanguage("auto"); err != nil {
		t.Fatalf("SetLanguage auto: %v", err)
	}
	if c.Language != "" {
		t.Fatalf("language = %q, want cleared", c.Language)
	}
}
