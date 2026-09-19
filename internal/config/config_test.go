package config

import (
	"os"
	"testing"
)

// TestNonLoopbackRequiresAPIKeyForServe：非迴環監聽的 API Key 約束現在只在
// serve 路徑生效（FORK-PLAN.md：全域校驗誤傷 login/import-pat/models/doctor）。
func TestNonLoopbackRequiresAPIKeyForServe(t *testing.T) {
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
	if err := c.ValidateServe(); err == nil {
		t.Fatal("wanted refusal without API key")
	}
	_ = os.Setenv("QODER_PROXY_API_KEY", "secret")
	if err := c.ValidateServe(); err != nil {
		t.Fatalf("with key: %v", err)
	}
}

// TestValidateAllowsNonLoopbackWithoutKey：全域 Validate() 不得再含迴環檢查——
// 這是今天加第二帳號時實際撞到的 bug：無 server 的子命令被 config.Load 拒起。
func TestValidateAllowsNonLoopbackWithoutKey(t *testing.T) {
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
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate must not gate on loopback for non-serve commands: %v", err)
	}
}

// TestValidateStillRejectsMalformedListen：拆分不得削弱基本校驗。
func TestValidateStillRejectsMalformedListen(t *testing.T) {
	c := Default()
	c.Listen = "not-an-ip:8080"
	if err := c.Validate(); err == nil {
		t.Fatal("wanted refusal on non-IP listen host")
	}
	if err := c.ValidateServe(); err == nil {
		t.Fatal("ValidateServe must inherit base validation")
	}
}
