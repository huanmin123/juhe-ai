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

func TestWlNormalizeEnumAndStrings(t *testing.T) {
	t.Run("normalizeEnumField", func(t *testing.T) {
		got, err := normalizeEnumField(nil, "fallback", []string{"a"}, "m")
		if err != nil || got != "fallback" {
			t.Fatalf("nil 必须回落: %q %v", got, err)
		}
		if got, _ := normalizeEnumField("", "fallback", []string{"a"}, "m"); got != "fallback" {
			t.Fatalf("空串必须回落: %q", got)
		}
		if got, _ := normalizeEnumField("a", "fallback", []string{"a", "b"}, "m"); got != "a" {
			t.Fatalf("合法值: %q", got)
		}
		if _, err := normalizeEnumField(3, "fallback", []string{"a"}, "m"); err == nil {
			t.Fatal("非字符串必须报错")
		}
		if _, err := normalizeEnumField("c", "fallback", []string{"a"}, "m"); err == nil {
			t.Fatal("不在允许列表必须报错")
		}
	})
	t.Run("optionalTrimmedString", func(t *testing.T) {
		if got := optionalTrimmedString("  x  "); got != "x" {
			t.Fatalf("got=%q", got)
		}
		if got := optionalTrimmedString(7); got != "" {
			t.Fatalf("非字符串=%q", got)
		}
	})
	t.Run("requiredTrimmedString", func(t *testing.T) {
		if _, err := requiredTrimmedString("   ", "m"); err == nil {
			t.Fatal("空白必须报错")
		}
		if got, err := requiredTrimmedString(" v ", "m"); err != nil || got != "v" {
			t.Fatalf("got=%q err=%v", got, err)
		}
	})
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
		if rawValueConfigured(nil) {
			t.Fatal("nil 不算已配置")
		}
		if !rawValueConfigured(struct{}{}) {
			t.Fatal("任意非 nil 值算已配置")
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

// ---- hybrid 混合路由配置 ----

// wlHybridLevelRoutes 构造合法的三档覆盖 1-10。
func wlHybridLevelRoutes() []any {
	return []any{
		map[string]any{"minLevel": 1, "maxLevel": 3, "targetModel": "model-a"},
		map[string]any{"minLevel": 4, "maxLevel": 6, "targetModel": "model-b"},
		map[string]any{"minLevel": 7, "maxLevel": 10, "targetModel": "model-c"},
	}
}

func wlValidHybridRaw() map[string]any {
	return map[string]any{
		"scoringModel":   " judge-model ",
		"levelRoutes":    wlHybridLevelRoutes(),
		"scoringGroupId": "  group-1  ",
	}
}

func TestWlNormalizeHybridRoutingConfig(t *testing.T) {
	t.Run("nil 拒绝", func(t *testing.T) {
		if _, err := normalizeHybridRoutingConfig(nil); err == nil {
			t.Fatal("nil 必须拒绝")
		}
		if _, err := normalizeHybridRoutingConfig("x"); err == nil {
			t.Fatal("非对象必须拒绝")
		}
	})
	t.Run("合法负载", func(t *testing.T) {
		config, err := normalizeHybridRoutingConfig(wlValidHybridRaw())
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if config.ScoringModel != "judge-model" || config.ScoringGroupID == nil || *config.ScoringGroupID != "group-1" {
			t.Fatalf("config=%+v", config)
		}
		if !config.ScoringCacheEnabled || !config.CacheAffinityEnabled {
			t.Fatalf("缓存默认开启: %+v", config)
		}
		if len(config.LevelRoutes) != 3 {
			t.Fatalf("levelRoutes=%+v", config.LevelRoutes)
		}
		if config.QualityInspection == nil || config.QualityInspection.ScoringModel != "judge-model" {
			t.Fatalf("qualityInspection=%+v", config.QualityInspection)
		}
	})
	t.Run("评分模型必填", func(t *testing.T) {
		payload := wlValidHybridRaw()
		delete(payload, "scoringModel")
		if _, err := normalizeHybridRoutingConfig(payload); err == nil {
			t.Fatal("缺评分模型必须报错")
		}
	})
	tests := []struct {
		name  string
		field string
		value any
		want  string
	}{
		{"上下文模式无效", "scoringContextMode", "partial", "上下文模式"},
		{"质量偏好无效", "qualityPreference", "random", "质量偏好"},
		{"评分超时超范围", "scoringTimeoutMs", 1, "评分超时"},
		{"兜底上限超范围", "scoringFallbackMaxLevel", 6, "兜底上限"},
		{"缓存 TTL 超范围", "scoringCacheTtlSeconds", 0, "缓存 TTL"},
		{"亲和 TTL 超范围", "affinityTtlSeconds", 100000, "缓存亲和"},
		{"切换等级差超范围", "switchMinLevelDelta", 10, "切换等级差"},
		{"降级确认超范围", "downgradeConsecutiveLowCount", 21, "降级确认"},
		{"等级范围缺失", "levelRoutes", nil, "等级范围不能为空"},
		{"质量评分配置无效", "qualityInspection", "x", "质量评分配置无效"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := wlValidHybridRaw()
			payload[test.field] = test.value
			_, err := normalizeHybridRoutingConfig(payload)
			if err == nil || !contains(err.Error(), test.want) {
				t.Fatalf("field=%s err=%v want 含 %q", test.field, err, test.want)
			}
		})
	}
}

func TestWlNormalizeHybridLevelRoutes(t *testing.T) {
	valid := wlHybridLevelRoutes()
	t.Run("合法三档", func(t *testing.T) {
		routes, err := normalizeHybridLevelRoutes(valid)
		if err != nil || len(routes) != 3 {
			t.Fatalf("routes=%+v err=%v", routes, err)
		}
	})
	tests := []struct {
		name   string
		mutate func([]any) []any
		want   string
	}{
		{"空列表", func(r []any) []any { return nil }, "等级范围不能为空"},
		{"项非对象", func(r []any) []any { return []any{"x"} }, "等级范围无效"},
		{"min 大于 max", func(r []any) []any { r[0] = map[string]any{"minLevel": 5, "maxLevel": 1, "targetModel": "a"}; return r }, "最小值不能大于最大值"},
		{"目标模型缺失", func(r []any) []any { r[0] = map[string]any{"minLevel": 1, "maxLevel": 3}; return r }, "目标模型不能为空"},
		{"enabled 非布尔", func(r []any) []any {
			r[0] = map[string]any{"minLevel": 1, "maxLevel": 3, "targetModel": "a", "enabled": "yes"}
			return r
		}, "布尔值"},
		{"全部禁用", func(r []any) []any {
			for index, item := range r {
				item.(map[string]any)["enabled"] = false
				r[index] = item
			}
			return r
		}, "至少需要一个启用"},
		{"不足两个模型", func(r []any) []any {
			for index, item := range r {
				item.(map[string]any)["targetModel"] = "same"
				r[index] = item
			}
			return r
		}, "2 个不同的目标模型"},
		{"首档越界", func(r []any) []any { r[0] = map[string]any{"minLevel": 1, "maxLevel": 6, "targetModel": "a"}; return r }, "最低档"},
		{"首档不从 1 开始", func(r []any) []any { r[0] = map[string]any{"minLevel": 2, "maxLevel": 3, "targetModel": "a"}; return r }, "最低档"},
		{"等级不连续", func(r []any) []any { r[1] = map[string]any{"minLevel": 5, "maxLevel": 6, "targetModel": "b"}; return r }, "必须从等级"},
		{"覆盖不满 1-10", func(r []any) []any { r[2] = map[string]any{"minLevel": 7, "maxLevel": 9, "targetModel": "c"}; return r }, "连续覆盖"},
		{"等级越界", func(r []any) []any {
			r[2] = map[string]any{"minLevel": 7, "maxLevel": 11, "targetModel": "c"}
			return r
		}, "必须是 1-10"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeHybridLevelRoutes(test.mutate(wlHybridLevelRoutes()))
			if err == nil || !contains(err.Error(), test.want) {
				t.Fatalf("err=%v want 含 %q", err, test.want)
			}
		})
	}
	t.Run("超过五个启用档", func(t *testing.T) {
		routes := []any{}
		boundaries := []int{1, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 10}
		for index := 0; index < 6; index++ {
			routes = append(routes, map[string]any{
				"minLevel": boundaries[index*2], "maxLevel": boundaries[index*2+1],
				"targetModel": "model-" + string(rune('a'+index)),
			})
		}
		if _, err := normalizeHybridLevelRoutes(routes); err == nil || !contains(err.Error(), "最多只能配置 5 个") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("禁用档不计入覆盖", func(t *testing.T) {
		routes := []any{
			map[string]any{"minLevel": 1, "maxLevel": 5, "targetModel": "a"},
			map[string]any{"minLevel": 6, "maxLevel": 10, "targetModel": "b"},
			map[string]any{"minLevel": 2, "maxLevel": 3, "targetModel": "c", "enabled": false},
		}
		routesOut, err := normalizeHybridLevelRoutes(routes)
		if err != nil || len(routesOut) != 2 {
			t.Fatalf("routes=%+v err=%v", routesOut, err)
		}
	})
}

func TestWlNormalizeQualityInspection(t *testing.T) {
	t.Run("缺失物化默认", func(t *testing.T) {
		inspection, err := normalizeQualityInspection(nil, "primary-model")
		if err != nil || !inspection.Enabled || inspection.ScoringModel != "primary-model" || inspection.TriggerMode != "risk_based" {
			t.Fatalf("inspection=%+v err=%v", inspection, err)
		}
	})
	t.Run("非对象拒绝", func(t *testing.T) {
		if _, err := normalizeQualityInspection(5, "m"); err == nil {
			t.Fatal("非对象必须拒绝")
		}
	})
	t.Run("启用但缺模型", func(t *testing.T) {
		payload := map[string]any{"enabled": true, "scoringModel": "  "}
		if _, err := normalizeQualityInspection(payload, ""); err == nil {
			t.Fatal("启用且无模型必须报错")
		}
	})
	t.Run("禁用时允许缺模型", func(t *testing.T) {
		payload := map[string]any{"enabled": false}
		inspection, err := normalizeQualityInspection(payload, "")
		if err != nil || inspection.Enabled || inspection.ScoringModel != "" {
			t.Fatalf("inspection=%+v err=%v", inspection, err)
		}
	})
	tests := []struct {
		name  string
		field string
		value any
		want  string
	}{
		{"开关非布尔", "enabled", "yes", "开关必须是布尔值"},
		{"触发模式无效", "triggerMode", "random", "触发模式无效"},
		{"触发等级超范围", "maxTriggerLevel", 11, "最高触发等级"},
		{"重试次数超范围", "maxRetries", 3, "重试次数"},
		{"失败动作无效", "failureAction", "none", "失败动作无效"},
		{"不可用动作无效", "unavailableAction", "ignore", "不可用处理方式无效"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := map[string]any{test.field: test.value, "scoringModel": "m"}
			_, err := normalizeQualityInspection(payload, "m")
			if err == nil || !contains(err.Error(), test.want) {
				t.Fatalf("field=%s err=%v want 含 %q", test.field, err, test.want)
			}
		})
	}
	t.Run("评分组保留", func(t *testing.T) {
		payload := map[string]any{"scoringGroupId": " g9 ", "scoringModel": "m"}
		inspection, err := normalizeQualityInspection(payload, "m")
		if err != nil || inspection.ScoringGroupID == nil || *inspection.ScoringGroupID != "g9" {
			t.Fatalf("inspection=%+v err=%v", inspection, err)
		}
	})
}

// ---- 模式 / 状态 / 写入归一化 ----

func TestWlNormalizeModeAndStatus(t *testing.T) {
	mode := ModeHybridSmart
	got, err := normalizeMode(&mode)
	if err != nil || got != ModeHybridSmart {
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
	hybridRaw := wlValidHybridRaw()
	normalRaw := map[string]any{"schedulingPreference": "cost_first"}
	if _, _, err := normalizeConfigForWrite(normalRaw, nil, ModeNormal); err != nil {
		t.Fatalf("normal 合法: %v", err)
	}
	if _, _, err := normalizeConfigForWrite(normalRaw, hybridRaw, ModeHybridSmart); err == nil || !contains(err.Error(), "混合智能路由不支持调度偏好") {
		t.Fatalf("err=%v", err)
	}
	if _, _, err := normalizeConfigForWrite(nil, hybridRaw, ModeWeighted); err == nil || !contains(err.Error(), "只有混合智能路由可以配置混合评分规则") {
		t.Fatalf("err=%v", err)
	}
	// weighted 无配置回落 cost_first 默认对象（config_json 仍判 NULL）。
	normal, hybrid, err := normalizeConfigForWrite(nil, nil, ModeWeighted)
	if err != nil || hybrid != nil || normal == nil || normal.SchedulingPreference != defaultNormalSchedulingPreference {
		t.Fatalf("weighted 无配置回落 cost_first 默认: %v %v %v", normal, hybrid, err)
	}
	if _, _, err := normalizeConfigForWrite(nil, hybridRaw, ModeNormal); err == nil || !contains(err.Error(), "普通路由不能配置混合评分规则") {
		t.Fatalf("err=%v", err)
	}
	if _, _, err := normalizeConfigForWrite(normalRaw, nil, ModeHybridSmart); err == nil {
		t.Fatal("hybrid 缺混合配置必须报错")
	}
}

func TestWlRouteStrategyConfigJSONAndParse(t *testing.T) {
	t.Run("cost_first 存 NULL", func(t *testing.T) {
		normal := &NormalRoutingConfig{SchedulingPreference: "cost_first"}
		if stored := routeStrategyConfigJSON(normal, nil); stored.Valid {
			t.Fatalf("cost_first 必须存 NULL: %v", stored)
		}
		if stored := routeStrategyConfigJSON(nil, nil); stored.Valid {
			t.Fatalf("全空必须存 NULL: %v", stored)
		}
	})
	t.Run("speed_first 落盘", func(t *testing.T) {
		deadline := 20000
		normal := &NormalRoutingConfig{SchedulingPreference: "speed_first", FirstByteDeadlineMs: &deadline}
		stored := routeStrategyConfigJSON(normal, nil)
		if !stored.Valid || !contains(stored.String, "speed_first") {
			t.Fatalf("stored=%v", stored)
		}
		parsedNormal, parsedHybrid, err := parseStoredConfig(stored)
		if err != nil || parsedNormal == nil || parsedHybrid != nil {
			t.Fatalf("normal=%+v hybrid=%v err=%v", parsedNormal, parsedHybrid, err)
		}
		if parsedNormal.SchedulingPreference != "speed_first" || *parsedNormal.FirstByteDeadlineMs != 20000 {
			t.Fatalf("normal=%+v", parsedNormal)
		}
	})
	t.Run("hybrid 落盘并修复", func(t *testing.T) {
		hybrid, err := normalizeHybridRoutingConfig(wlValidHybridRaw())
		if err != nil {
			t.Fatal(err)
		}
		stored := routeStrategyConfigJSON(nil, hybrid)
		parsedNormal, parsedHybrid, err := parseStoredConfig(stored)
		if err != nil || parsedNormal != nil || parsedHybrid == nil {
			t.Fatalf("normal=%v hybrid=%+v err=%v", parsedNormal, parsedHybrid, err)
		}
		if parsedHybrid.ScoringModel != "judge-model" {
			t.Fatalf("hybrid=%+v", parsedHybrid)
		}
	})
	t.Run("无效 JSON", func(t *testing.T) {
		if _, _, err := parseStoredConfig(sql.NullString{String: "{broken", Valid: true}); err == nil {
			t.Fatal("坏 JSON 必须报错")
		}
	})
	t.Run("空值", func(t *testing.T) {
		normal, hybrid, err := parseStoredConfig(sql.NullString{})
		if err != nil || normal != nil || hybrid != nil {
			t.Fatalf("%v %v %v", normal, hybrid, err)
		}
	})
	t.Run("普通配置损坏", func(t *testing.T) {
		raw := `{"normalRoutingConfig":{"schedulingPreference":"bogus"}}`
		if _, _, err := parseStoredConfig(sql.NullString{String: raw, Valid: true}); err == nil {
			t.Fatal("损坏普通配置必须报错")
		}
	})
	t.Run("混合配置损坏", func(t *testing.T) {
		raw := `{"hybridRoutingConfig":{"scoringModel":""}}`
		if _, _, err := parseStoredConfig(sql.NullString{String: raw, Valid: true}); err == nil {
			t.Fatal("损坏混合配置必须报错")
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
