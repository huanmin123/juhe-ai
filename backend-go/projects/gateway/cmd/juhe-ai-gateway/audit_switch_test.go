package main

import (
	"strings"
	"testing"
)

// TestLoadRuntimeConfigAuditLogEnabled 锁定部署级审计开关契约：
// 缺省 true（升级零行为变化）、显式 false/0/no/off 关闭、非法值 fail-fast。
// 关闭语义由 gatewayusage 捕获层与 logreads 读面共同消费，本测试只钉配置入口。
func TestLoadRuntimeConfigAuditLogEnabled(t *testing.T) {
	cfg, err := loadRuntimeConfigEnv(t, developmentSecurityEnv(t))
	if err != nil || !cfg.AuditLogEnabled {
		t.Fatalf("未配置 JUHE_AI_AUDIT_LOG_ENABLED 必须默认开启: %v", err)
	}
	for _, value := range []string{"false", "0", "no", "OFF"} {
		env := developmentSecurityEnv(t)
		env["JUHE_AI_AUDIT_LOG_ENABLED"] = value
		cfg, err := loadRuntimeConfigEnv(t, env)
		if err != nil || cfg.AuditLogEnabled {
			t.Fatalf("JUHE_AI_AUDIT_LOG_ENABLED=%q 必须关闭审计: %v", value, err)
		}
	}
	for _, value := range []string{"true", "1", "yes", "On", "   "} {
		env := developmentSecurityEnv(t)
		env["JUHE_AI_AUDIT_LOG_ENABLED"] = value
		cfg, err := loadRuntimeConfigEnv(t, env)
		if err != nil || !cfg.AuditLogEnabled {
			t.Fatalf("JUHE_AI_AUDIT_LOG_ENABLED=%q 必须保持开启: %v", value, err)
		}
	}
	for _, value := range []string{"bogus", "2", "enabled"} {
		env := developmentSecurityEnv(t)
		env["JUHE_AI_AUDIT_LOG_ENABLED"] = value
		if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_AUDIT_LOG_ENABLED 只能配置为 true/false/1/0/yes/no/on/off") {
			t.Fatalf("JUHE_AI_AUDIT_LOG_ENABLED=%q 必须 fail-fast，得到 %v", value, err)
		}
	}
}
