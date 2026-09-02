package config

import (
	"os"
	"testing"
)

func TestNonLoopbackRequiresAPIKey(t *testing.T) {
	old, had := os.LookupEnv("QODER_PROXY_API_KEY")
	_ = os.Unsetenv("QODER_PROXY_API_KEY")
	defer func() {
		if had {
			_ = os.Setenv("QODER_PROXY_API_KEY", old)
		} else {
			_ = os.Unsetenv("QODER_PROXY_API_KEY")
		}
	}()
	c := Default()
	c.Listen = "0.0.0.0:8080"
	if err := c.Validate(); err == nil {
		t.Fatal("wanted refusal without API key")
	}
	_ = os.Setenv("QODER_PROXY_API_KEY", "secret")
	if err := c.Validate(); err != nil {
		t.Fatalf("with key: %v", err)
	}
}
