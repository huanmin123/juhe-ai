package accounts

// w14b 纯函数与轻量 DB 臂补齐：store 辅助、时间计划、额度恢复、错误处理
// 规则、响应检查规则、余额配置、上游 Base URL 安全配置、标签、锁归一化、
// YAML 值归一化、用量源绑定、余额快照清理器生命周期等零散未覆盖臂。

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------- store.go

func TestW14BStoreHelpersArms(t *testing.T) {
	if _, err := NewStore(nil, false, testSecret, nil, nil); err == nil || !strings.Contains(err.Error(), "requires a database") {
		t.Fatalf("nil db 应报错：%v", err)
	}
	if _, err := NewStore(&sql.DB{}, false, "  ", nil, nil); err == nil || !strings.Contains(err.Error(), "requires the runtime secret") {
		t.Fatalf("空 secret 应报错：%v", err)
	}
	store, err := NewStore(&sql.DB{}, true, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if store.now().IsZero() {
		t.Fatal("默认时钟应可用")
	}
	if got := itoa64(0); got != "0" {
		t.Fatalf("itoa64(0) = %q", got)
	}
	if got := itoa(0); got != "0" {
		t.Fatalf("itoa(0) = %q", got)
	}
	if ensureCtx(nil) == nil {
		t.Fatal("ensureCtx(nil) 应回退 Background")
	}
	if ensureCtx(context.Background()) == nil {
		t.Fatal("ensureCtx 应原样返回")
	}
	if got := normalizeAccountNameSearchText(123); got != "" {
		t.Fatalf("非字符串归一化应为空：%q", got)
	}
	if got := normalizeAccountNameSearchText("　 Ａｂｃ 　"); got != "Abc" {
		t.Fatalf("NFKC+trim 归一化不符：%q", got)
	}
	if terms := accountNameSearchQueryTerms(""); terms != nil {
		t.Fatalf("空关键字应无词条：%v", terms)
	}
	if terms := accountNameSearchQueryTerms(strings.Repeat("长", maxAccountNameLength+1)); terms != nil {
		t.Fatalf("超长关键字应无词条：%v", terms)
	}
	if terms := buildAccountNameSearchTerms("　"); terms != nil {
		t.Fatalf("全空白名称应无词条：%v", terms)
	}
	termsShort := buildAccountNameSearchTerms("ab")
	wantShort := []string{"a", "b", "ab"}
	if !equalsStringSlice(termsShort, wantShort) {
		t.Fatalf("短名称 1..3 gram 应全集：%v", termsShort)
	}
	// ownerEffectiveStatusSQL 空别名回退 accounts。
	sqlText := ownerEffectiveStatusSQL("", "'2026-01-01T00:00:00.000Z'")
	if !strings.Contains(sqlText, "accounts.last_error_code") {
		t.Fatal("空别名应回退 accounts 表名")
	}
	// normalizeTextList：去空白、去重、排序并截断。
	got := normalizeTextList([]string{"b", " a ", "", "a", "c", "b"}, 2)
	if !equalsStringSlice(got, []string{"a", "b"}) {
		t.Fatalf("normalizeTextList 截断不符：%v", got)
	}
	if normalizeTextList(nil, 3) != nil {
		t.Fatal("空输入应返回 nil")
	}
	if got := textPrefixUpperBound(string(rune(0x10ffff))); got != string(rune(0x10ffff))+"\uffff" {
		t.Fatalf("上界回退符不符：%q", got)
	}
	if got := textPrefixUpperBound("ab"); got != "ac" {
		t.Fatalf("普通上界不符：%q", got)
	}
	// pg Store 的 insertIgnore 走 ON CONFLICT 附加分支。
	if !strings.Contains(store.insertIgnore("INSERT INTO t (a) VALUES (?)", " ON CONFLICT DO NOTHING"), "ON CONFLICT DO NOTHING") {
		t.Fatal("pg insertIgnore 应附加冲突子句")
	}
	if !strings.Contains(store.table("accounts"), "juhe_business.accounts") {
		t.Fatal("pg 表名应加 schema 前缀")
	}
	if got := store.bind("a = ? AND b = ?"); got != "a = $1 AND b = $2" {
		t.Fatalf("pg bind 应改写占位符：%q", got)
	}
}

func TestW14BAccessScopeArms(t *testing.T) {
	if _, err := (AccessScope{}).ownerID(); err == nil {
		t.Fatal("空 scope ownerID 应报错")
	}
	if _, err := (AccessScope{ViewerID: "u1"}).ownerID(); err != nil {
		t.Fatalf("带 viewer 的 ownerID 应成功：%v", err)
	}
	scope1 := AccessScope{ViewerID: "u1", IsAdmin: true}
	if got := scope1.manageableID(); got != "" {
		t.Fatalf("无 filter 的管理员 manageableID 应为空：%q", got)
	}
	scope2 := AccessScope{ViewerID: "u1", IsAdmin: true, FilterID: "u2"}
	if got := scope2.viewerID(); got != "u2" {
		t.Fatalf("管理员 viewerID 应取 filter：%q", got)
	}
	if _, err := tagOwnerSystemAccountID(AccessScope{}); err == nil {
		t.Fatal("空 scope 标签归属应报错")
	}
	if got, err := tagOwnerSystemAccountID(AccessScope{ViewerID: "u1", IsAdmin: true}); err != nil || got != "u1" {
		t.Fatalf("管理员无 filter 标签归属应回 viewer：%q %v", got, err)
	}
}

func equalsStringSlice(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------- schedule.go

func TestW14BScheduleNormalizeArms(t *testing.T) {
	invalid := []string{
		`[]`,
		`{"enabled":true,"unknown":1}`,
		`{"unknown":1}`,
		`{"enabled":false}`,
		`{"enabled":"yes"}`,
		`{"enabled":true,"timezone":"Not/AZone"}`,
		`{"enabled":true,"timezone":""}`,
		`{"enabled":true,"timezone":3}`,
		`{"enabled":true,"windows":"daily"}`,
		`{"enabled":true,"mode":"allow_windows","windows":[]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[3]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"start":"09:00","end":"09:00"}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"start":"9am","end":"10:00"}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"start":"09:00","end":"10:00","extra":1}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"start":"09:00","end":"18:00","daysOfWeek":"mon"}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"start":"09:00","end":"18:00","daysOfWeek":[]}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"start":"09:00","end":"18:00","daysOfWeek":[0]}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"start":"09:00","end":"18:00","daysOfWeek":[1,"x"]}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"start":"09:00","end":"18:00"}],"dateRange":{"startDate":"2026-02-01","endDate":"2026-01-01"}}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"start":"09:00","end":"18:00"}],"dateRange":"x"}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"start":"09:00","end":"18:00"}],"exceptions":"x"}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"start":"09:00","end":"18:00"}],"exceptions":[3]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"start":"09:00","end":"18:00"}],"exceptions":[{"date":"","action":"allow","windows":[]}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"start":"09:00","end":"18:00"}],"exceptions":[{"date":"2026-03-01","action":"bad"}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"start":"09:00","end":"18:00"}],"exceptions":[{"date":"2026-03-01","action":"deny","windows":[{"start":"09:00","end":"10:00"}]}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"start":"09:00","end":"18:00"}],"exceptions":[{"date":"2026-03-01","action":"allow","windows":[]}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"start":"09:00","end":"18:00"}],"exceptions":[{"date":"2026-03-01","action":"allow","windows":[{"start":"09:00","end":"09:00"}]}]}`,
		`{"enabled":true,"mode":"allow_windows","windows":[{"start":"09:00","end":"18:00"}],"exceptions":[{"date":"2026-13-01","action":"deny"}]}`,
	}
	for _, payload := range invalid {
		if _, err := NormalizeSchedule(jsonRaw(t, payload)); err == nil {
			t.Fatalf("非法时间计划应报错：%s", payload)
		}
	}
	// 超出窗口/例exception 上限。
	many := `{"enabled":true,"mode":"allow_windows","windows":[` + repeatJSON(`{"start":"09:00","end":"10:00"}`, maxScheduleWindows+1) + `]}`
	if _, err := NormalizeSchedule(jsonRaw(t, many)); err == nil {
		t.Fatal("窗口超上限应报错")
	}
	manyExc := `{"enabled":true,"mode":"allow_windows","windows":[{"start":"09:00","end":"10:00"}],"exceptions":[` +
		repeatJSON(`{"date":"2026-03-01","action":"deny"}`, maxScheduleExceptions+1) + `]}`
	if _, err := NormalizeSchedule(jsonRaw(t, manyExc)); err == nil {
		t.Fatal("例外超上限应报错")
	}
	// 合法全量：windows + dateRange + deny/allow exceptions。
	okPayload := `{"enabled":true,"timezone":"Asia/Shanghai","mode":"allow_windows",
		"windows":[{"start":"09:00","end":"18:00","daysOfWeek":[1,2]}],
		"dateRange":{"startDate":"2026-01-01","endDate":"2026-12-31"},
		"exceptions":[{"date":"2026-03-01","action":"deny"},{"date":"2026-03-02","action":"allow","windows":[{"start":"10:00","end":"12:00"}]}]}`
	schedule, err := NormalizeSchedule(jsonRaw(t, okPayload))
	if err != nil {
		t.Fatalf("合法时间计划应通过：%v", err)
	}
	if len(schedule.Windows) != 1 || len(schedule.Windows[0].DaysOfWeek) != 2 || len(schedule.Exceptions) != 2 {
		t.Fatalf("归一化结果不符：%+v", schedule)
	}
	if defaultScheduleTimezone() == "" {
		t.Fatal("默认时区不应为空")
	}
	if _, err := ParseScheduleJSON(""); err != nil {
		t.Fatalf("空串应返回 nil 计划与无错：%v", err)
	}
	if _, err := ParseScheduleJSON("{bad"); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
}

func jsonRaw(t *testing.T, payload string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		t.Fatalf("测试 JSON 不合法：%v %s", err, payload)
	}
	return value
}

func repeatJSON(item string, count int) string {
	parts := make([]string, count)
	for i := range parts {
		parts[i] = item
	}
	return strings.Join(parts, ",")
}

// ------------------------------------------------------- quota_recovery.go

func TestW14BQuotaRecoveryArms(t *testing.T) {
	if out, err := normalizeQuotaRecoveryPolicy(nil); err != nil || len(out) != 0 {
		t.Fatalf("nil 策略应为空对象：%v %v", out, err)
	}
	if _, err := normalizeQuotaRecoveryPolicy("x"); err == nil {
		t.Fatal("非对象策略应报错")
	}
	if _, err := normalizeQuotaRecoveryPolicy(map[string]any{"bad": 1}); err == nil {
		t.Fatal("未知字段应报错")
	}
	if _, err := normalizeQuotaRecoverySchedule("x"); err == nil {
		t.Fatal("策略项非对象应报错")
	}
	if _, err := normalizeQuotaRecoverySchedule(map[string]any{}); err == nil {
		t.Fatal("缺 reset_strategy 应报错")
	}
	if _, err := normalizeQuotaRecoveryPolicy(map[string]any{"api_key": map[string]any{"reset_strategy": "duration"}}); err == nil {
		t.Fatal("duration 缺 duration_minutes 应报错")
	}
	if _, err := normalizeQuotaRecoveryPolicy(map[string]any{"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(1)}}); err == nil {
		t.Fatal("duration_minutes 低于下限应报错")
	}
	if _, err := normalizeQuotaRecoveryPolicy(map[string]any{"oauth": map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(24)}}); err == nil {
		t.Fatal("daily_reset_hour 超上限应报错")
	}
	if _, err := normalizeQuotaRecoveryPolicy(map[string]any{"api_key": map[string]any{"reset_strategy": "daily"}}); err == nil {
		t.Fatal("daily 缺 daily_reset_hour 应报错")
	}
	if _, err := normalizeQuotaRecoveryPolicy(map[string]any{"api_key": map[string]any{"reset_strategy": "weekly"}}); err == nil {
		t.Fatal("weekly 缺字段应报错")
	}
	if _, err := normalizeQuotaRecoveryPolicy(map[string]any{"api_key": map[string]any{"reset_strategy": "weekly", "weekly_reset_day": float64(7), "weekly_reset_hour": float64(0)}}); err == nil {
		t.Fatal("weekly_reset_day 超上限应报错")
	}
	if _, err := normalizeQuotaRecoveryPolicy(map[string]any{"api_key": map[string]any{"reset_strategy": "weekly", "weekly_reset_day": float64(1), "weekly_reset_hour": float64(24)}}); err == nil {
		t.Fatal("weekly_reset_hour 超上限应报错")
	}
	if _, err := normalizeQuotaRecoveryPolicy(map[string]any{"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(30), "jitter_minutes": float64(10)}}); err == nil {
		t.Fatal("jitter 非 15 应报错")
	}
	if _, err := normalizeQuotaRecoveryPolicy(map[string]any{"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(30), "timezone": 3}}); err == nil {
		t.Fatal("timezone 非字符串应报错")
	}
	if _, err := normalizeQuotaRecoveryPolicy(map[string]any{"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(30), "timezone": "  "}}); err == nil {
		t.Fatal("timezone 空白应报错")
	}
	if _, err := normalizeQuotaRecoveryPolicy(map[string]any{"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(30), "timezone": "Bad/Zone"}}); err == nil {
		t.Fatal("未知 timezone 应报错")
	}
	okPolicy, err := normalizeQuotaRecoveryPolicy(map[string]any{
		"api_key":      map[string]any{"reset_strategy": "duration", "duration_minutes": float64(45), "timezone": "UTC+08:00", "jitter_minutes": float64(15)},
		"oauth":        map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(3)},
		"google_oauth": map[string]any{"reset_strategy": "weekly", "weekly_reset_day": float64(1), "weekly_reset_hour": float64(4)},
	})
	if err != nil {
		t.Fatalf("完整策略应通过：%v", err)
	}
	if okPolicy["api_key"].(map[string]any)["timezone"] != "UTC+08:00" {
		t.Fatalf("timezone 归一化不符：%v", okPolicy["api_key"])
	}
	// 巨型策略触发尺寸上限。
	huge := map[string]any{"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(30), "timezone": strings.Repeat("长", quotaRecoveryMaxPolicyBytes)}}
	if _, err := normalizeQuotaRecoveryPolicy(huge); err == nil {
		t.Fatal("超大策略应报错")
	}
	// 时区偏移解析臂。
	cases := map[string]bool{
		"UTC": true, "GMT": true, "Asia/Shanghai": true, "UTC+08": true,
		"UTC+08:30": true, "UTC-05:00": true, "UTC+8": false, "UTC+24:00": true,
		"UTC+08:60": false, "UTC+0x:00": false, "UTC+": false, "UTC±08:00": false,
		"UTC+08:": false,
	}
	for name, want := range cases {
		if got := validQuotaRecoveryTimezone(name); got != want {
			t.Fatalf("validQuotaRecoveryTimezone(%q) = %v, want %v", name, got, want)
		}
	}
	if got := jsonEncodeLength(make(chan int)); got != 0 {
		t.Fatalf("不可序列化值长度应为 0：%d", got)
	}
	if got := jsonEncodeLength(map[string]any{"a": 1}); got == 0 {
		t.Fatal("可序列化值长度不应为 0")
	}
	if _, err := quotaRecoveryIntegerInRange("3", 0, 5, "字段"); err == nil {
		t.Fatal("非数字应报错")
	}
}

// -------------------------------------------------------- error_policy.go

func TestW14BErrorPolicyArms(t *testing.T) {
	if out, err := normalizeAccountErrorHandlingRules(nil); err != nil || len(out) != 0 {
		t.Fatalf("nil 规则应为空数组：%v %v", out, err)
	}
	if _, err := normalizeAccountErrorHandlingRules("x"); err == nil {
		t.Fatal("非数组规则应报错")
	}
	if _, err := normalizeAccountErrorHandlingRules([]any{3}); err == nil {
		t.Fatal("非对象规则项应报错")
	}
	if _, err := normalizeAccountErrorHandlingRules([]any{map[string]any{"unknown": 1}}); err == nil {
		t.Fatal("未知字段应报错")
	}
	if _, err := normalizeAccountErrorHandlingRules([]any{map[string]any{"source": "system"}}); err == nil {
		t.Fatal("系统继承规则应拒绝写入")
	}
	if _, err := normalizeAccountErrorHandlingRules([]any{map[string]any{"inherited": true}}); err == nil {
		t.Fatal("inherited=true 应拒绝写入")
	}
	if _, err := normalizeAccountErrorHandlingRules([]any{map[string]any{"editable": false}}); err == nil {
		t.Fatal("editable=false 应拒绝写入")
	}
	badRules := []map[string]any{
		{"status_codes": "x"},
		{"status_codes": []any{float64(200)}},
		{"error_codes": 3},
		{"error_codes": []any{3}},
		{"error_types": []any{3}},
		{"keywords": []any{3}},
		{"keywords": []any{"", "k"}},
		{"description": 3},
		{"priority": "high"},
		{"priority": 1.5},
		{"action": "noop"},
	}
	for index, rule := range badRules {
		base := map[string]any{"enabled": true, "name": "r", "priority": float64(1), "action": "retry_next", "keywords": []any{"k"}}
		for key, value := range rule {
			base[key] = value
		}
		if _, err := normalizeAccountErrorHandlingRules([]any{base}); err == nil {
			t.Fatalf("非法规则 %d 应报错：%+v", index, rule)
		}
	}
	// enabled 但无匹配条件。
	if _, err := normalizeAccountErrorHandlingRules([]any{map[string]any{
		"enabled": true, "name": "r", "priority": float64(1), "action": "retry_next",
	}}); err == nil {
		t.Fatal("启用且无匹配条件的规则应报错")
	}
	okRule, err := normalizeAccountErrorHandlingRules([]any{map[string]any{
		"name": "限流", "enabled": true, "priority": float64(2), "action": "rate_limited",
		"reset_strategy": "weekly", "weekly_reset_day": float64(2), "weekly_reset_hour": float64(5),
		"status_codes": []any{float64(429), float64(429), float64(500)},
		"error_codes":  []any{"ERR_A", "ERR_A", "ERR_B"},
		"error_types":  []any{"rate_limit", "rate_limit"},
		"keywords":     []any{"quota", "quota"},
		"description":  "desc",
	}})
	if err != nil {
		t.Fatalf("合法规则应通过：%v", err)
	}
	first := okRule[0].(map[string]any)
	if len(first["status_codes"].([]any)) != 2 || len(first["error_codes"].([]any)) != 2 {
		t.Fatalf("状态码/错误码去重失败：%+v", first)
	}
	if first["weekly_reset_day"] != float64(2) || first["weekly_reset_hour"] != float64(5) {
		t.Fatalf("weekly 归一化回写失败：%+v", first)
	}
	// duration/daily 恢复策略分支。
	for _, strategy := range []map[string]any{
		{"reset_strategy": "duration", "duration_hours": float64(3)},
		{"reset_strategy": "daily", "daily_reset_hour": float64(6)},
	} {
		base := map[string]any{"enabled": true, "name": "r", "priority": float64(1), "action": "rate_limited", "keywords": []any{"k"}}
		for key, value := range strategy {
			base[key] = value
		}
		if _, err := normalizeAccountErrorHandlingRules([]any{base}); err != nil {
			t.Fatalf("策略 %v 应通过：%v", strategy, err)
		}
	}
	if _, err := normalizeAccountErrorHandlingRules([]any{map[string]any{
		"enabled": true, "name": "r", "priority": float64(1), "action": "rate_limited",
		"keywords": []any{"k"}, "reset_strategy": "duration",
	}}); err == nil {
		t.Fatal("duration 策略缺 duration_hours 应报错")
	}
	if _, err := normalizeAccountErrorHandlingRules([]any{map[string]any{
		"enabled": true, "name": "r", "priority": float64(1), "action": "rate_limited",
		"keywords": []any{"k"}, "reset_strategy": "daily",
	}}); err == nil {
		t.Fatal("daily 策略缺 daily_reset_hour 应报错")
	}
	// textEqual 分支。
	if !textEqual("a", "a") || textEqual("a", "b") || textEqual(3, "a") || !textEqual(true, true) || textEqual(true, false) || textEqual(3.0, 3.0) {
		t.Fatal("textEqual 分支判定不符")
	}
	if out, err := optionalRuleStatusCodes([]any{}, 1); err != nil || out != nil {
		t.Fatalf("空状态码数组应返回 nil：%v %v", out, err)
	}
	if out, err := optionalRuleStringList([]any{}, "标签"); err != nil || out != nil {
		t.Fatalf("空字符串数组应返回 nil：%v %v", out, err)
	}
	if out, err := optionalRuleErrorCodeList([]any{}, "标签"); err != nil || out != nil {
		t.Fatalf("空错误码数组应返回 nil：%v %v", out, err)
	}
	if _, err := requiredRuleBoolean("x", "标签"); err == nil {
		t.Fatal("requiredRuleBoolean 非布尔应报错")
	}
	if _, err := requiredRuleString("  ", "标签"); err == nil {
		t.Fatal("requiredRuleString 空白应报错")
	}
	if _, err := requiredRulePositiveInteger(0, "标签"); err == nil {
		t.Fatal("requiredRulePositiveInteger 0 应报错")
	}
	if _, err := requiredRuleHour(7.5, "标签"); err == nil {
		t.Fatal("requiredRuleHour 小数应报错")
	}
	if _, err := requiredRuleWeekday(7, "标签"); err == nil {
		t.Fatal("requiredRuleWeekday 超界应报错")
	}
	if _, err := requiredRuleAction("noop", 1); err == nil {
		t.Fatal("requiredRuleAction 非法动作应报错")
	}
	if _, err := requiredRuleResetStrategy("noop", 1); err == nil {
		t.Fatal("requiredRuleResetStrategy 非法策略应报错")
	}
	if v, err := requiredRuleHour(float64(6), "标签"); err != nil || v != 6 {
		t.Fatalf("requiredRuleHour 合法值不符：%v %v", v, err)
	}
}

// -------------------------------------------------- response_inspection.go

func TestW14BResponseInspectionArms(t *testing.T) {
	if out, err := normalizeAccountResponseInspectionRules(nil); err != nil || len(out) != 0 {
		t.Fatalf("nil 规则应为空数组：%v %v", out, err)
	}
	if _, err := normalizeAccountResponseInspectionRules("x"); err == nil {
		t.Fatal("非数组应报错")
	}
	tooMany := make([]any, responseInspectionMaxRules+1)
	for i := range tooMany {
		tooMany[i] = map[string]any{"name": "r", "enabled": false}
	}
	if _, err := normalizeAccountResponseInspectionRules(tooMany); err == nil {
		t.Fatal("超上限应报错")
	}
	if _, err := normalizeAccountResponseInspectionRules([]any{3}); err == nil {
		t.Fatal("非对象规则应报错")
	}
	match := map[string]any{"outputTextIncludes": []any{"err"}}
	bad := []map[string]any{
		{"unknown": 1},
		{"enabled": "yes"},
		{"enabled": true},
		{"enabled": true, "name": 3},
		{"enabled": true, "name": "  "},
		{"enabled": true, "name": strings.Repeat("长", responseInspectionNameMaxRunes+1)},
		{"enabled": true, "name": "r", "priority": "1"},
		{"enabled": true, "name": "r", "priority": 1.5},
		{"enabled": true, "name": "r", "priority": float64(responseInspectionPriorityMax + 1)},
		{"enabled": true, "name": "r", "match": "x"},
		{"enabled": true, "name": "r", "match": match},
		{"enabled": true, "name": "r", "match": match, "action": "noop"},
		{"enabled": true, "name": "r", "match": match, "action": "observe", "notes": 3},
		{"enabled": true, "name": "r", "match": match, "action": "observe", "notes": strings.Repeat("长", responseInspectionNotesMaxRunes+1)},
	}
	for index, rule := range bad {
		if _, err := normalizeAccountResponseInspectionRules([]any{rule}); err == nil {
			t.Fatalf("非法响应规则 %d 应报错：%+v", index, rule)
		}
	}
	enabledNoMatcher := map[string]any{"enabled": true, "name": "r", "priority": float64(1), "action": "observe", "match": map[string]any{}}
	if _, err := normalizeAccountResponseInspectionRules([]any{enabledNoMatcher}); err == nil {
		t.Fatal("启用且无匹配条件的规则应报错")
	}
	ok, err := normalizeAccountResponseInspectionRules([]any{map[string]any{
		"enabled": true, "name": "r", "priority": float64(1), "action": "observe",
		"match": map[string]any{"outputTextIncludes": []any{" err "}, "clientProfiles": []any{"codex"}},
		"notes": "备注",
	}})
	if err != nil {
		t.Fatalf("合法响应规则应通过：%v", err)
	}
	ruleMap := ok[0].(map[string]any)
	if !responseInspectionRuleHasMatcher(ruleMap) {
		t.Fatal("含匹配条件的规则应被识别")
	}
	if responseInspectionRuleHasMatcher(map[string]any{"match": map[string]any{}}) {
		t.Fatal("空匹配不应算匹配条件")
	}
	if ruleMap["notes"] != "备注" {
		t.Fatalf("备注应保留：%v", ruleMap["notes"])
	}
	// match 归一化错误臂。
	badMatches := []map[string]any{
		{"clientProfiles": "x"},
		{"clientProfiles": []any{"bad"}},
		{"clientProfiles": []any{make([]string, 7)}},
		{"outputTextIncludes": "x"},
		{"outputTextIncludes": []any{3}},
		{"outputTextIncludes": []any{"", "k"}},
		{"unknownKey": []any{}},
	}
	for index, m := range badMatches {
		if _, err := normalizeResponseInspectionMatch(m); err == nil {
			t.Fatalf("非法 match %d 应报错：%+v", index, m)
		}
	}
	if _, err := normalizeResponseInspectionMatch(3); err == nil {
		t.Fatal("match 非对象应报错")
	}
	longList := make([]any, responseInspectionTextMaxItems+1)
	for i := range longList {
		longList[i] = "k"
	}
	if _, err := normalizeResponseInspectionMatch(map[string]any{"outputTextIncludes": longList}); err == nil {
		t.Fatal("match 列表超上限应报错")
	}
}

// ------------------------------------------------------ balance_config.go

func TestW14BBalanceConfigArms(t *testing.T) {
	if _, err := NormalizeAccountBalanceConfig(map[string]any{"adapter": "official", "custom": map[string]any{}}); err == nil {
		t.Fatal("内置类型携带自定义配置应报错")
	}
	if _, err := normalizeBalanceCustomConfig("x", true); err == nil {
		t.Fatal("自定义配置非对象应报错")
	}
	if keys := EffectiveAccountApiKeys(Credentials{}); len(keys) != 0 {
		t.Fatalf("空凭据无有效键：%v", keys)
	}
	if got := EffectiveAccountApiKeyCount(Credentials{"api_key": "a"}); got != 1 {
		t.Fatalf("单键计数不符：%d", got)
	}
	store := &Store{secret: testSecret}
	if got := store.balanceAPIKeyFingerprint(""); got != "" {
		t.Fatalf("空键指纹应为空：%q", got)
	}
	if got := store.balanceAPIKeyFingerprint("sk-w14b"); got == "" || strings.Contains(got, "sk-w14b") {
		t.Fatalf("指纹应为摘要：%q", got)
	}
	if got := normalizedBalanceBaseURL("  "); got != "" {
		t.Fatalf("空白 base url 应为空：%q", got)
	}
	if got := normalizedBalanceBaseURL(3); got != "" {
		t.Fatalf("非字符串 base url 应为空：%q", got)
	}
	if got := normalizedBalanceBaseURL("https://api.example.com/v1/"); got != "https://api.example.com/v1" {
		t.Fatalf("规范化应去掉尾斜杠：%q", got)
	}
	if got := normalizedBalanceBaseURL("not a url"); got != "not a url" {
		t.Fatalf("非 URL 回退 trim：%q", got)
	}
	if raw, err := canonicalBalanceConfigJSON(map[string]any{"a": float64(1)}); err != nil || raw != `{"a":1}` {
		t.Fatalf("canonical JSON 不符：%q %v", raw, err)
	}
}

// -------------------------------------------------- upstream_base_url.go

func TestW14BUpstreamBaseURLArms(t *testing.T) {
	// 惰性缓存可被测试重置：先探测默认（拒绝私有）配置。
	resetUpstreamSecurityOnce := func(env map[string]string) {
		for key, value := range env {
			t.Setenv(key, value)
		}
		upstreamSecurityOnce = sync.Once{}
		t.Cleanup(func() {
			upstreamSecurityOnce = sync.Once{}
		})
	}
	resetUpstreamSecurityOnce(map[string]string{
		"JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS":     "1",
		"JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST":  " http://127.0.0.1:8080/, , http://10.0.0.9:9000, bad-scheme",
	})
	config := upstreamURLSecurityConfig()
	if !config.allowPrivateBaseUrls {
		t.Fatal("allow env=1 应允许私有上游")
	}
	if len(config.privateBaseUrlAllowlist) != 2 {
		t.Fatalf("allowlist 应去重并丢弃非法项：%v", config.privateBaseUrlAllowlist)
	}
	if err := assertSafeUpstreamBaseURL("http://127.0.0.1:8080/v1"); err != nil {
		t.Fatalf("allow 模式下私有地址应放行：%v", err)
	}
	resetUpstreamSecurityOnce(map[string]string{
		"JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS":    "",
		"JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST": "http://127.0.0.1:8080/",
	})
	if err := assertSafeUpstreamBaseURL("http://127.0.0.1:8080/v1"); err != nil {
		t.Fatalf("allowlist 命中应放行：%v", err)
	}
	if err := assertSafeUpstreamBaseURL("http://localhost:9/v1"); err == nil || !strings.Contains(err.Error(), unsafeUpstreamBaseURLMessage) {
		t.Fatalf("localhost 应被拒绝：%v", err)
	}
	if err := assertSafeUpstreamBaseURL("http://192.168.1.9/v1"); err == nil {
		t.Fatal("私有 IPv4 应被拒绝")
	}
	if err := assertSafeUpstreamBaseURL("http://[::1]/v1"); err == nil {
		t.Fatal("IPv6 回环应被拒绝")
	}
	if isPrivateOrReservedIP("example.com") {
		t.Fatal("普通域名不应被判为私有 IP")
	}
	if isPrivateOrReservedIP("999.1.1.1") {
		t.Fatal("非法 IPv4 不应被判为私有")
	}
	if isPrivateOrReservedIP("[zz::1]") {
		t.Fatal("非法 IPv6 不应被判为私有")
	}
	if isLocalhostHostName("example.local") {
		t.Fatal("非 localhost 后缀不应命中")
	}
	if !isLocalhostHostName("api.localhost") {
		t.Fatal(".localhost 后缀应命中")
	}
	parsed, err := url.Parse("https://api.example.com/v1")
	if err != nil {
		t.Fatal(err)
	}
	if upstreamOriginAllowlisted(parsed, upstreamURLSecurity{}) {
		t.Fatal("空 allowlist 不应命中")
	}
	invalids := []string{
		"https://api.example.com/v1#frag",
		"https://api.example.com//v1",
		"https://api.example.com/v1?q=1",
		"https://api.example.com/v1?",
		"https://user:pass@api.example.com/v1",
		"https:///v1",
		"https://api.example.com/%2f",
		"https://api.example.com/a%zz",
		"https://api.example.com/./",
		"https://api.example.com/../x",
	}
	for _, value := range invalids {
		if _, err := validateOpenAICompatibleBaseURL(value); err == nil {
			t.Fatalf("非法上游 URL 应报错：%s", value)
		}
	}
}

// ---------------------------------------------------------------- tags.go

func TestW14BTagStoreArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	if _, err := env.store.ListTags(context.Background(), AccessScope{}); err == nil {
		t.Fatal("空 scope ListTags 应报错")
	}
	if _, err := env.store.DeleteTag(context.Background(), "", scope); err != nil {
		t.Fatalf("空 tagID 应返回 false,nil：%v", err)
	}
	if _, err := env.store.DeleteTag(context.Background(), "tag-x", AccessScope{}); err == nil {
		t.Fatal("空 scope DeleteTag 应报错")
	}
	env.exec(t, `INSERT INTO account_tags (id, system_account_id, name, created_at, updated_at)
		VALUES ('tag-w14b-free', ?, 'w14b-free', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, adminID)
	env.exec(t, `INSERT INTO account_tags (id, system_account_id, name, created_at, updated_at)
		VALUES ('tag-w14b-bound', ?, 'w14b-bound', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, adminID)
	env.seedAccount(t, "acc-w14b-tag", adminID, "w14b-tag", "active")
	env.exec(t, `INSERT INTO account_tag_bindings (account_id, tag_id, system_account_id, created_at)
		VALUES ('acc-w14b-tag', 'tag-w14b-bound', ?, '2026-01-01T00:00:00.000Z')`, adminID)
	summaries, err := env.store.ListTags(context.Background(), scope)
	if err != nil {
		t.Fatalf("ListTags 应成功：%v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("标签汇总数不符：%+v", summaries)
	}
	boundCount := 0
	for _, summary := range summaries {
		if summary.ID == "tag-w14b-bound" {
			boundCount = summary.AccountCount
		}
	}
	if boundCount != 1 {
		t.Fatalf("绑定标签计数不符：%d", boundCount)
	}
	if _, err := env.store.DeleteTag(context.Background(), "tag-w14b-bound", scope); err == nil {
		t.Fatal("使用中标签删除应报 TagInUseError")
	}
	deleted, err := env.store.DeleteTag(context.Background(), "tag-w14b-free", scope)
	if err != nil || !deleted {
		t.Fatalf("空闲标签删除应成功：%v %v", deleted, err)
	}
	deleted, err = env.store.DeleteTag(context.Background(), "tag-w14b-missing", scope)
	if err != nil || deleted {
		t.Fatalf("缺失标签删除应返回 false：%v %v", deleted, err)
	}
	if err := env.store.replaceAccountNameSearchTerms(context.Background(), env.db, "acc-w14b-tag", adminID, "　", "2026-01-01T00:00:00.000Z"); err != nil {
		t.Fatalf("空名称词条替换应成功返回：%v", err)
	}
	// 非 owner 视角：非管理员只能看到自己的（空列表）。
	userID := env.login(t, "alice-w14b", "alice-pass", "user")
	userScope := AccessScope{ViewerID: userID}
	userTags, err := env.store.ListTags(context.Background(), userScope)
	if err != nil || len(userTags) != 0 {
		t.Fatalf("普通用户标签应为空：%v %v", userTags, err)
	}
}

// ------------------------------------------------------ retryqueue.go 等

func TestW14BRetryQueuePureArms(t *testing.T) {
	if got := max64(3, 2); got != 3 {
		t.Fatalf("max64 左大不符：%d", got)
	}
	if got := max64(1, 2); got != 2 {
		t.Fatalf("max64 右大不符：%d", got)
	}
	queue := newRetryQueue[int]("w14b-test", []int64{}, 0,
		func(item int, attemptIndex int) error { return nil }, retryQueueCallbacks[int]{})
	queue.stop()
	queue.clear()
	queue.waitRunning()
	pending, running := queue.counts()
	if pending != 0 || running != 0 {
		t.Fatalf("空队列计数不符：%d %d", pending, running)
	}
}

// --------------------------------------------------- import_source_yaml.go

func TestW14BYAMLValueNormalization(t *testing.T) {
	if got := normalizeYAMLValue(int32(3)); got != float64(3) {
		t.Fatalf("int32 归一化不符：%v", got)
	}
	if got := normalizeYAMLValue(uint64(9)); got != float64(9) {
		t.Fatalf("uint64 归一化不符：%v", got)
	}
	if got := yamlKeyString(42); got != "42" {
		t.Fatalf("数字键字符串化不符：%q", got)
	}
}

// ------------------------------------------------------- list_usage.go

func TestW14BStatsUsageSourceHelpers(t *testing.T) {
	if _, err := NewStatsUsageSource(nil, false); err == nil {
		t.Fatal("nil db 应报错")
	}
	env := newTestEnv(t)
	source, err := NewStatsUsageSource(env.db, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := source.table("usage_stats_totals"); got != "usage_stats_totals" {
		t.Fatalf("sqlite 表名不应有前缀：%q", got)
	}
	if got := source.bind("a = ? AND b = ?"); got != "a = ? AND b = ?" {
		t.Fatalf("sqlite bind 应原样：%q", got)
	}
	pgSource := &StatsUsageSource{db: env.db, pg: true}
	if got := pgSource.table("usage_stats_totals"); got != "juhe_stats.usage_stats_totals" {
		t.Fatalf("pg 表名应加前缀：%q", got)
	}
	if got := pgSource.bind("a = ? AND b = ?"); got != "a = $1 AND b = $2" {
		t.Fatalf("pg bind 应改写：%q", got)
	}
	empty, err := source.AccountListUsageSummaries(context.Background(), nil, "")
	if err != nil || len(empty) != 0 {
		t.Fatalf("空 scope 汇总应为空：%v %v", empty, err)
	}
	// sqlite 下建出 stats 总量表并命中查询。
	env.exec(t, `CREATE TABLE IF NOT EXISTS usage_stats_totals (system_account_id TEXT, scope_type TEXT, scope_id TEXT, request_count INTEGER, input_tokens INTEGER, output_tokens INTEGER, total_cost_usd REAL)`)
	env.exec(t, `INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, input_tokens, output_tokens, total_cost_usd)
		VALUES ('owner-w14b', 'account', 'acc-w14b-usage', 2, 10, 20, 0.5)`)
	scope := UsageScope{RowKey: "acc-w14b-usage", SystemAccountID: "owner-w14b", ScopeType: usageScopeTypeAccount, ScopeID: "acc-w14b-usage"}
	summaries, err := source.AccountListUsageSummaries(context.Background(), []UsageScope{scope, scope}, "")
	if err != nil {
		t.Fatalf("用量汇总查询应成功：%v", err)
	}
	if summary := summaries["acc-w14b-usage"]; summary.TotalTokens != 30 || summary.RequestCount != 2 {
		t.Fatalf("用量汇总不符：%+v", summaries)
	}
	// statDate 分支换 daily 表：sqlite 缺表应返回错误臂。
	if _, err := source.AccountListUsageSummaries(context.Background(), []UsageScope{scope}, "2026-09-17"); err == nil {
		t.Fatal("缺 daily 表应报错")
	}
}

// --------------------------------------------- balance_snapshot_cleanup.go

func TestW14BBalanceSnapshotCleanerLifecycle(t *testing.T) {
	env := newTestEnv(t)
	env.exec(t, `INSERT INTO account_usage_snapshots (system_account_id, account_id, kind, snapshot_json, updated_at, created_at)
		VALUES ('owner-w14b', 'acc-w14b-snap', 'relay_balance', '{}', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	cleaner := NewStoreBalanceSnapshotCleaner(env.store)
	if cleaner == nil || cleaner.Queue() == nil {
		t.Fatal("清理器与队列应构建成功")
	}
	// nil/缺 store 的入口应安全返回。
	var nilCleaner *StoreBalanceSnapshotCleaner
	nilCleaner.CleanupBalanceSnapshotAfterSave(BalanceSnapshotCleanupRequest{AccountID: "x"})
	(&StoreBalanceSnapshotCleaner{}).CleanupBalanceSnapshotAfterSave(BalanceSnapshotCleanupRequest{AccountID: "x"})
	cleaner.CleanupBalanceSnapshotAfterSave(BalanceSnapshotCleanupRequest{AccountID: "acc-w14b-snap", Reason: "w14b"})
	cleaner.SetClockForTest(func() time.Time { return time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC) })
	cleaner.Close()
	cleaner.Close()
	// 关闭后再入队应被拒绝且不再执行。
	cleaner.CleanupBalanceSnapshotAfterSave(BalanceSnapshotCleanupRequest{AccountID: "acc-w14b-snap", Reason: "w14b-after-close"})
	if remaining := env.count(t, `SELECT COUNT(*) FROM account_usage_snapshots WHERE account_id = 'acc-w14b-snap'`); remaining != 1 {
		t.Fatalf("关闭后不应删除快照：%d", remaining)
	}
}
