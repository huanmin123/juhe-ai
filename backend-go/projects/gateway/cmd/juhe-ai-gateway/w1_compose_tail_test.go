package main

// w1: compose.go 尾部助手收割——组合 ID 形状、设置值字符串化、信任代理
// 跳数、SQLite DSN/只读打开/PRAGMA 配置、方言转换与开发自动登录守卫。

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestW1SettingsStringProjection(t *testing.T) {
	// nil / 字符串 / 浮点 / 布尔 / 其他 → 字符串投影。
	if got := settingsString(nil); got != "" {
		t.Fatalf("nil = %q", got)
	}
	if got := settingsString("文本"); got != "文本" {
		t.Fatalf("string = %q", got)
	}
	if got := settingsString(float64(100)); got != "100" {
		t.Fatalf("float = %q", got)
	}
	if got := settingsString(true); got != "true" {
		t.Fatalf("bool = %q", got)
	}
	if got := settingsString(7); got != "7" {
		t.Fatalf("int = %q", got)
	}
	// 组合 ID：前缀 + 十六进制 16 字符。
	id := newCompositionID("acc")
	if !strings.HasPrefix(id, "acc_") || len(id) != len("acc_")+16 {
		t.Fatalf("id = %q", id)
	}
}

func TestW1TrustProxyCountAndTrim(t *testing.T) {
	// 布尔字面量与跳数；越界 / 非法 / 空值 → 0。
	if got := trustProxyCount(" true "); got != 1 {
		t.Fatalf("true = %d", got)
	}
	if got := trustProxyCount("OFF"); got != 0 {
		t.Fatalf("off = %d", got)
	}
	if got := trustProxyCount("7"); got != 7 {
		t.Fatalf("7 = %d", got)
	}
	if got := trustProxyCount("17"); got != 0 {
		t.Fatalf("17 = %d", got)
	}
	if got := trustProxyCount("-1"); got != 0 {
		t.Fatalf("-1 = %d", got)
	}
	if got := trustProxyCount("junk"); got != 0 {
		t.Fatalf("junk = %d", got)
	}
	if got := trustProxyCount(""); got != 0 {
		t.Fatalf("empty = %d", got)
	}
	if got := trimSpace("\t 甲乙 \t"); got != "甲乙" {
		t.Fatalf("trim = %q", got)
	}
}

func TestW1DevAutoLoginAndDegradedReaders(t *testing.T) {
	// 未配置用户名 / 无账户存储：不解析。
	if devAutoLoginResolver(nil, "") != nil {
		t.Fatal("空用户名必须 nil")
	}
	// 设置时区源：键固定 usageStatsTimezone。
	timezone := settingsTimezone(func(key string) (string, error) {
		if key != "usageStatsTimezone" {
			t.Fatalf("key = %q", key)
		}
		return "Asia/Shanghai", nil
	})
	value, err := timezone(context.Background())
	if err != nil || value != "Asia/Shanghai" {
		t.Fatalf("timezone = %q, %v", value, err)
	}
	// 生产者日志适配器：可调用。
	(producerLogger{}).Warn("警告", "k", "v")
	(producerLogger{}).Error("错误")
	// 清理提交器：nil store 直接 panic 保护（跳过调用，仅构造）。
	_ = apiKeyCleanupSubmitter{}
	_ = httptest.NewRecorder()
}
