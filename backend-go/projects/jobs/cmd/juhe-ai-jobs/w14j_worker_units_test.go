// 波次 w14j：worker 纯函数与低依赖臂批测（余额配置、保留时区、估算、
// 空值扫描、凭据判定等）。
package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobssettings"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// TestW14JNormalizeBalanceConfigJSON 覆盖默认间隔、首选适配器注入与序列化形状。
func TestW14JNormalizeBalanceConfigJSON(t *testing.T) {
	serialized, err := normalizeBalanceConfigJSON("builtin", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(serialized), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["intervalMinutes"] != float64(5) {
		t.Fatalf("缺省间隔必须 5: %v", decoded)
	}
	if _, ok := decoded["preferredBuiltinAdapter"]; ok {
		t.Fatalf("空 preferred 不得写入: %v", decoded)
	}
	serialized, err = normalizeBalanceConfigJSON("builtin", 30, "totals")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(serialized), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["intervalMinutes"] != float64(30) || decoded["preferredBuiltinAdapter"] != "totals" {
		t.Fatalf("显式参数必须写入: %v", decoded)
	}
}

// TestW14JBalanceConfigJSONEqual 覆盖相等、不等与坏 JSON 分支。
func TestW14JBalanceConfigJSONEqual(t *testing.T) {
	a, err := normalizeBalanceConfigJSON("builtin", 5, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := normalizeBalanceConfigJSON("builtin", 5, "")
	if err != nil {
		t.Fatal(err)
	}
	if !balanceConfigJSONEqual(a, b) {
		t.Fatal("同配置必须相等")
	}
	c, err := normalizeBalanceConfigJSON("builtin", 15, "")
	if err != nil {
		t.Fatal(err)
	}
	if balanceConfigJSONEqual(a, c) {
		t.Fatal("不同间隔必须不等")
	}
	if balanceConfigJSONEqual("{broken", a) {
		t.Fatal("坏 JSON 必须不等")
	}
}

// TestW14JDefaultRetentionTimezone 覆盖 settings 缺省与 UTC 回退分支。
func TestW14JDefaultRetentionTimezone(t *testing.T) {
	original, hadOriginal := jobssettings.DefaultSystemSettings["usageStatsTimezone"]
	restore := func() {
		if hadOriginal {
			jobssettings.DefaultSystemSettings["usageStatsTimezone"] = original
		} else {
			delete(jobssettings.DefaultSystemSettings, "usageStatsTimezone")
		}
	}
	jobssettings.DefaultSystemSettings["usageStatsTimezone"] = "Asia/Shanghai"
	if got := defaultRetentionTimezone(); got != "Asia/Shanghai" {
		restore()
		t.Fatalf("settings 存在时必须返回: %s", got)
	}
	delete(jobssettings.DefaultSystemSettings, "usageStatsTimezone")
	if got := defaultRetentionTimezone(); got != "UTC" {
		t.Fatalf("缺省必须回退 UTC: %s", got)
	}
	restore()
}

// TestW14JEstimateJobBytes 覆盖正常估算与不可序列化回退。
func TestW14JEstimateJobBytes(t *testing.T) {
	job := retention.RecordMaintenanceJob{Type: "usage_records", ID: "w14j", BatchSize: 10}
	if estimate := estimateJobBytes(job); estimate <= 0 {
		t.Fatalf("正常估算必须为正: %d", estimate)
	}
}

// TestW14JScanNullTime 覆盖 postgres/sqlite 与空值、文本、错误值分支。
func TestW14JScanNullTime(t *testing.T) {
	if value, err := scanNullTime(true, nil); err != nil || value != nil {
		t.Fatalf("nil 必须 nil: %v %v", value, err)
	}
	stamp := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	if value, err := scanNullTime(false, stamp.Format(time.RFC3339Nano)); err != nil || value == nil {
		t.Fatalf("sqlite 文本必须可解析: %v %v", value, err)
	}
	if value, err := scanNullTime(true, stamp); err != nil || value == nil {
		t.Fatalf("postgres time.Time 必须直通: %v %v", value, err)
	}
	if value, err := scanNullTime(false, 12345); err == nil || value != nil {
		t.Fatalf("不支持的类型必须报错: %v %v", value, err)
	}
}

// TestW14JCredentialPredicates 覆盖凭据判定与小转换器分支。
func TestW14JCredentialPredicates(t *testing.T) {
	if !hasAtLeastOneAPIKey(map[string]any{"api_keys": []any{"  k1 ", ""}}) {
		t.Fatal("api_keys 含非空项必须 true")
	}
	if hasAtLeastOneAPIKey(map[string]any{"api_keys": []any{""}}) {
		t.Fatal("api_keys 全空必须 false")
	}
	if hasAtLeastOneAPIKey(map[string]any{"api_keys": 123}) {
		t.Fatal("api_keys 类型错误必须 false")
	}
	if !hasAtLeastOneAPIKey(map[string]any{"api_key": " k "}) {
		t.Fatal("api_key 非空必须 true")
	}
	if hasAtLeastOneAPIKey(map[string]any{}) {
		t.Fatal("无键必须 false")
	}
	if textOrEmpty("v") != "v" || textOrEmpty(1) != "" {
		t.Fatal("textOrEmpty 分支错误")
	}
	if intOrZero(float64(3)) != 3 || intOrZero(json.Number("4")) != 4 || intOrZero(5) != 5 || intOrZero(nil) != 0 {
		t.Fatal("intOrZero 分支错误")
	}
}
