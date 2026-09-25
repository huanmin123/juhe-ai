package main

import (
	"strings"
	"testing"
)

// TestLoadRuntimeConfigCaptchaDisabledStrictBoolean 锁定
// JUHE_AI_AUTH_CAPTCHA_DISABLED 的严格布尔契约（Node strictBooleanConfig）：
// true/1/yes/on 关闭验证码，false/0/no/off 与空/未设保持开启，其他非空值
// 启动即失败。管理面（compose.go authDeps）与 J3b 面（main.go 装配
// captcha 服务）都只消费 runtimeCfg.CaptchaDisabled，`1` 这类布尔语法必须
// 两处同判，任何装配面不得再单独解析该 env（布尔语法漂移回归门）。
func TestLoadRuntimeConfigCaptchaDisabledStrictBoolean(t *testing.T) {
	for _, value := range []string{"true", "TRUE", "1", "yes", "On"} {
		env := developmentSecurityEnv(t)
		env["JUHE_AI_AUTH_CAPTCHA_DISABLED"] = value
		cfg, err := loadRuntimeConfigEnv(t, env)
		if err != nil || !cfg.CaptchaDisabled {
			t.Fatalf("JUHE_AI_AUTH_CAPTCHA_DISABLED=%q 必须判定为关闭验证码（两装配面同判）: %v", value, err)
		}
	}
	for _, value := range []string{"false", "0", "no", "OFF", "", "   "} {
		env := developmentSecurityEnv(t)
		env["JUHE_AI_AUTH_CAPTCHA_DISABLED"] = value
		cfg, err := loadRuntimeConfigEnv(t, env)
		if err != nil || cfg.CaptchaDisabled {
			t.Fatalf("JUHE_AI_AUTH_CAPTCHA_DISABLED=%q 必须保持验证码开启: %v", value, err)
		}
	}
	// 未配置（缺键）等价空值。
	cfg, err := loadRuntimeConfigEnv(t, developmentSecurityEnv(t))
	if err != nil || cfg.CaptchaDisabled {
		t.Fatalf("未配置 JUHE_AI_AUTH_CAPTCHA_DISABLED 必须保持验证码开启: %v", err)
	}
	// 其他非空值启动即失败，文案与 strictEnvBool 一致。
	for _, value := range []string{"bogus", "2", "enabled"} {
		env := developmentSecurityEnv(t)
		env["JUHE_AI_AUTH_CAPTCHA_DISABLED"] = value
		if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_AUTH_CAPTCHA_DISABLED 只能配置为 true/false/1/0/yes/no/on/off") {
			t.Fatalf("JUHE_AI_AUTH_CAPTCHA_DISABLED=%q 必须 fail-fast，得到 %v", value, err)
		}
	}
}
