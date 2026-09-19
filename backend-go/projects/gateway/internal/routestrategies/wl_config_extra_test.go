package routestrategies

import (
	"database/sql"
	"testing"
)

// ---- 数值与枚举辅助 ----

func TestWlNumericValueTypes(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  int
		ok    bool
	}{
		{"int", 5, 5, true},
		{"int64", int64(6), 6, true},
		{"float64 整数", float64(7), 7, true},
		{"float64 小数", 1.5, 0, false},
		{"数字字符串", " 8 ", 8, true},
		{"非法字符串", "abc", 0, false},
		{"空字符串", "   ", 0, false},
		{"布尔", true, 0, false},
		{"nil", nil, 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := numericValue(test.value)
			if ok != test.ok || got != test.want {
				t.Fatalf("got=(%d,%v) want=(%d,%v)", got, ok, test.want, test.ok)
			}
		})
	}
}

func TestWlOptionalRecordAndConfiguredValue(t *testing.T) {
	t.Run("optionalRecord 与 hasConfiguredValue", func(t *testing.T) {
		if record, err := optionalRecord(nil, "m"); err != nil || record != nil {
			t.Fatalf("nil 必须返回 nil: %v %v", record, err)
		}
		if _, err := optionalRecord("x", "m"); err == nil {
			t.Fatal("非对象必须报错")
		}
		record, err := optionalRecord(map[string]any{}, "m")
		if err != nil || record == nil {
			t.Fatalf("对象必须返回: %v %v", record, err)
		}
		if hasConfiguredValue(nil) || hasConfiguredValue("") {
			t.Fatal("nil/空串不算已配置")
		}
		if !hasConfiguredValue(0) {
			t.Fatal("0 算已配置")
		}
	})
}

// ---- normal 路由配置 ----

func TestWlNormalizeNormalRoutingConfig(t *testing.T) {
	t.Run("nil 回落 cost_first", func(t *testing.T) {
		config, err := normalizeNormalRoutingConfig(nil)
		if err != nil || config.SchedulingPreference != "cost_first" || config.SpeedFirstConfig != nil {
			t.Fatalf("config=%+v err=%v", config, err)
		}
	})
	t.Run("非对象拒绝", func(t *testing.T) {
		if _, err := normalizeNormalRoutingConfig("x"); err == nil {
			t.Fatal("非对象必须报错")
		}
	})
	t.Run("偏好无效", func(t *testing.T) {
		if _, err := normalizeNormalRoutingConfig(map[string]any{"schedulingPreference": "random"}); err == nil {
			t.Fatal("无效偏好必须报错")
		}
	})
	t.Run("cost_first 短路不含速度配置", func(t *testing.T) {
		config, err := normalizeNormalRoutingConfig(map[string]any{"schedulingPreference": "cost_first"})
		if err != nil || config.FirstByteDeadlineMs != nil {
			t.Fatalf("config=%+v err=%v", config, err)
		}
	})
	t.Run("speed_first 完整默认", func(t *testing.T) {
		config, err := normalizeNormalRoutingConfig(map[string]any{"schedulingPreference": "speed_first"})
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if config.FirstByteDeadlineMs == nil || *config.FirstByteDeadlineMs != defaultSpeedFirstDeadlineMs {
			t.Fatalf("deadline=%v", config.FirstByteDeadlineMs)
		}
		if config.SpeedFirstConfig == nil || config.SpeedFirstConfig.SlowTriggerCount != 3 {
			t.Fatalf("speedFirst=%+v", config.SpeedFirstConfig)
		}
	})
	t.Run("双截止字段冲突", func(t *testing.T) {
		payload := map[string]any{
			"schedulingPreference": "speed_first",
			"firstByteDeadlineMs":  20000,
			"speedFirstConfig":     map[string]any{"firstByteThresholdMs": 30000},
		}
		if _, err := normalizeNormalRoutingConfig(payload); err == nil || !contains(err.Error(), "不能同时配置") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("旧字段截止", func(t *testing.T) {
		payload := map[string]any{
			"schedulingPreference": "speed_first",
			"speedFirstConfig":     map[string]any{"firstByteThresholdMs": 20000},
		}
		config, err := normalizeNormalRoutingConfig(payload)
		if err != nil || config.FirstByteDeadlineMs == nil || *config.FirstByteDeadlineMs != 20000 {
			t.Fatalf("config=%+v err=%v", config, err)
		}
	})
	t.Run("截止超范围", func(t *testing.T) {
		payload := map[string]any{"schedulingPreference": "speed_first", "firstByteDeadlineMs": 100}
		if _, err := normalizeNormalRoutingConfig(payload); err == nil {
			t.Fatal("超范围截止必须报错")
		}
	})
	t.Run("speedFirstConfig 非对象", func(t *testing.T) {
		payload := map[string]any{"schedulingPreference": "speed_first", "speedFirstConfig": "x"}
		if _, err := normalizeNormalRoutingConfig(payload); err == nil || !contains(err.Error(), "速度优先配置无效") {
			t.Fatalf("err=%v", err)
		}
	})
}

func contains(text, sub string) bool {
	return len(text) >= len(sub) && (text == sub || stringsContains(text, sub))
}

func stringsContains(text, sub string) bool {
	for i := 0; i+len(sub) <= len(text); i++ {
		if text[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestWlNormalizeSpeedFirstConfigRanges(t *testing.T) {
	t.Run("nil 全默认", func(t *testing.T) {
		config, err := normalizeSpeedFirstConfig(nil)
		if err != nil || config.SlowWindowSeconds != 120 || config.DegradedTtlSeconds != 300 {
			t.Fatalf("config=%+v err=%v", config, err)
		}
	})
	t.Run("非对象拒绝", func(t *testing.T) {
		if _, err := normalizeSpeedFirstConfig(3); err == nil {
			t.Fatal("非对象必须报错")
		}
	})
	tests := []struct {
		field string
		value any
		want  string
	}{
		{"slowTriggerCount", 1, "触发次数"},
		{"slowTriggerCount", 11, "触发次数"},
		{"slowTriggerCount", "x", "触发次数"},
		{"slowWindowSeconds", 59, "窗口期"},
		{"slowWindowSeconds", 601, "窗口期"},
		{"recoverySuccessCount", 2, "恢复次数"},
		{"recoverySuccessCount", 11, "恢复次数"},
		{"probeIntervalSeconds", 9, "探针间隔"},
		{"probeIntervalSeconds", 301, "探针间隔"},
		{"degradedTtlSeconds", 59, "降级保留"},
		{"degradedTtlSeconds", 3601, "降级保留"},
		{"maxFirstByteRetriesPerRequest", 0, "切号次数"},
		{"maxFirstByteRetriesPerRequest", 4, "切号次数"},
	}
	for _, test := range tests {
		t.Run(test.field, func(t *testing.T) {
			payload := map[string]any{test.field: test.value}
			_, err := normalizeSpeedFirstConfig(payload)
			if err == nil || !contains(err.Error(), test.want) {
				t.Fatalf("value=%v err=%v want 含 %q", test.value, err, test.want)
			}
		})
	}
	t.Run("数字字符串可接受", func(t *testing.T) {
		config, err := normalizeSpeedFirstConfig(map[string]any{"slowTriggerCount": "5"})
		if err != nil || config.SlowTriggerCount != 5 {
			t.Fatalf("config=%+v err=%v", config, err)
		}
	})
}

// ---- 模式 / 状态 / 写入归一化 ----

func TestWlNormalizeModeAndStatus(t *testing.T) {
	mode := ModeWeighted
	got, err := normalizeMode(&mode)
	if err != nil || got != ModeWeighted {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if got, _ := normalizeMode(nil); got != ModeNormal {
		t.Fatalf("nil 回落 normal: %q", got)
	}
	empty := ""
	if got, _ := normalizeMode(&empty); got != ModeNormal {
		t.Fatalf("空串回落 normal: %q", got)
	}
	bad := "weird"
	if _, err := normalizeMode(&bad); err == nil {
		t.Fatal("未知模式必须报错")
	}
	if got, err := normalizeStatus(nil, "active"); err != nil || got != "active" {
		t.Fatalf("nil 回落: %q %v", got, err)
	}
	if got, _ := normalizeStatus(strPtrWl("disabled"), "active"); got != "disabled" {
		t.Fatalf("got=%q", got)
	}
	if _, err := normalizeStatus(strPtrWl("paused"), "active"); err == nil {
		t.Fatal("未知状态必须报错")
	}
}

func strPtrWl(value string) *string { return &value }

func TestWlNormalizeConfigForWrite(t *testing.T) {
	normalRaw := map[string]any{"schedulingPreference": "cost_first"}
	if _, err := normalizeConfigForWrite(normalRaw, ModeNormal); err != nil {
		t.Fatalf("normal 合法: %v", err)
	}
	// weighted 无配置回落 cost_first 默认对象（config_json 仍判 NULL）。
	normal, err := normalizeConfigForWrite(nil, ModeWeighted)
	if err != nil || normal == nil || normal.SchedulingPreference != defaultNormalSchedulingPreference {
		t.Fatalf("weighted 无配置回落 cost_first 默认: %v %v", normal, err)
	}
	if _, err := normalizeConfigForWrite(map[string]any{"schedulingPreference": "bogus"}, ModeWeighted); err == nil {
		t.Fatal("无效偏好必须报错")
	}
}

func TestWlRouteStrategyConfigJSONAndParse(t *testing.T) {
	t.Run("cost_first 存 NULL", func(t *testing.T) {
		normal := &NormalRoutingConfig{SchedulingPreference: "cost_first"}
		if stored := routeStrategyConfigJSON(normal); stored.Valid {
			t.Fatalf("cost_first 必须存 NULL: %v", stored)
		}
		if stored := routeStrategyConfigJSON(nil); stored.Valid {
			t.Fatalf("全空必须存 NULL: %v", stored)
		}
	})
	t.Run("speed_first 落盘", func(t *testing.T) {
		deadline := 20000
		normal := &NormalRoutingConfig{SchedulingPreference: "speed_first", FirstByteDeadlineMs: &deadline}
		stored := routeStrategyConfigJSON(normal)
		if !stored.Valid || !contains(stored.String, "speed_first") {
			t.Fatalf("stored=%v", stored)
		}
		parsedNormal, err := parseStoredConfig(stored)
		if err != nil || parsedNormal == nil {
			t.Fatalf("normal=%+v err=%v", parsedNormal, err)
		}
		if parsedNormal.SchedulingPreference != "speed_first" || *parsedNormal.FirstByteDeadlineMs != 20000 {
			t.Fatalf("normal=%+v", parsedNormal)
		}
	})
	t.Run("未知落盘键按未知键忽略", func(t *testing.T) {
		stored := sql.NullString{String: `{"legacyRoutingConfig":{"scoringModel":""}}`, Valid: true}
		parsedNormal, err := parseStoredConfig(stored)
		if err != nil || parsedNormal != nil {
			t.Fatalf("normal=%+v err=%v", parsedNormal, err)
		}
	})
	t.Run("无效 JSON", func(t *testing.T) {
		if _, err := parseStoredConfig(sql.NullString{String: "{broken", Valid: true}); err == nil {
			t.Fatal("坏 JSON 必须报错")
		}
	})
	t.Run("空值", func(t *testing.T) {
		normal, err := parseStoredConfig(sql.NullString{})
		if err != nil || normal != nil {
			t.Fatalf("%v %v", normal, err)
		}
	})
	t.Run("普通配置损坏", func(t *testing.T) {
		raw := `{"normalRoutingConfig":{"schedulingPreference":"bogus"}}`
		if _, err := parseStoredConfig(sql.NullString{String: raw, Valid: true}); err == nil {
			t.Fatal("损坏普通配置必须报错")
		}
	})
}

func TestWlConfigValuesEqual(t *testing.T) {
	if !configValuesEqual(map[string]any{"a": float64(1)}, map[string]any{"a": float64(1)}) {
		t.Fatal("相同 JSON 必须相等")
	}
	if configValuesEqual(map[string]any{"a": float64(1)}, map[string]any{"a": float64(2)}) {
		t.Fatal("不同 JSON 必须不等")
	}
	// NaN 无法序列化，两侧都必须判 false。
	if configValuesEqual(mathNaN(), map[string]any{}) {
		t.Fatal("不可序列化左侧必须判 false")
	}
	if configValuesEqual(map[string]any{}, mathNaN()) {
		t.Fatal("不可序列化右侧必须判 false")
	}
}

func mathNaN() any { return struct{ Ch chan int }{make(chan int)} }
