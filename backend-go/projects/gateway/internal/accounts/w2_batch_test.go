package accounts

// W2 批量编辑测试：BatchUpdate 全链路（字段覆盖、CAS 冲突、无变化、权限）、
// validateBatchUpdateValue 字段校验矩阵与错误处理策略规则归一
// （batch.go + error_policy.go 的批量入口分支）。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// w2BatchAccounts 创建两个可批量编辑的账户并返回 id 与当前修订号。
func w2BatchAccounts(t *testing.T, env *testEnv, adminID string) (string, string, int64, int64) {
	t.Helper()
	env.seedProviderAndDefaultGroup(t, adminID)
	ids := []string{}
	for _, name := range []string{"批量甲", "批量乙"} {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload(name))
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", name, code, payload)
		}
		ids = append(ids, dataMap(t, payload)["id"].(string))
	}
	revision := func(id string) int64 {
		var value int64
		if err := env.db.QueryRow(`SELECT config_revision FROM accounts WHERE id = ?`, id).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	return ids[0], ids[1], revision(ids[0]), revision(ids[1])
}

// w2BatchUpdate 是 Store.BatchUpdate 的便捷包装。
func w2BatchUpdate(t *testing.T, env *testEnv, adminID string, input BatchUpdateInput) (*BatchUpdateResult, error) {
	t.Helper()
	return env.store.BatchUpdate(context.Background(), input, AccessScope{ViewerID: adminID, IsAdmin: true})
}

func w2EnabledFields(pairs map[string]any) map[string]BatchUpdateField {
	fields := map[string]BatchUpdateField{}
	for name, value := range pairs {
		fields[name] = BatchUpdateField{Enabled: true, Value: value}
	}
	return fields
}

func TestW2BatchUpdateFullFieldSuccess(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	seedImportProxy(t, env, "pp-batch", adminID, "批量代理")
	first, second, rev1, rev2 := w2BatchAccounts(t, env, adminID)

	input := BatchUpdateInput{
		Targets: []BatchUpdateTarget{{AccountID: first, ConfigRevision: rev1}, {AccountID: second, ConfigRevision: rev2}},
		Updates: w2EnabledFields(map[string]any{
			"concurrencyLimit":        float64(88),
			"priority":                float64(7),
			"superPriorityEnabled":    true,
			"proxyProfileId":          "pp-batch",
			"notes":                   "批量备注",
			"healthCheckModel":        "gpt-4.1",
			"supportedModels":         []any{"gpt-4o-mini", "gpt-4.1"},
			"tags":                    []any{"新标签"},
			"accountExpiresAt":        "2030-01-01T00:00:00Z",
			"supportedEndpointModes":  []any{"chat_json", "chat_sse"},
			"serviceTierOverride":     "priority",
			"reasoningEffortOverride": "high",
		}),
	}
	result, err := w2BatchUpdate(t, env, adminID, input)
	if err != nil {
		t.Fatalf("BatchUpdate 错误：%v", err)
	}
	changed := strings.Join(result.ChangedFields, ",")
	for _, expected := range []string{"concurrencyLimit", "priority", "superPriorityEnabled", "proxyProfileId",
		"notes", "healthCheckModel", "tags", "accountExpiresAt", "supportedEndpointModes",
		"serviceTierOverride", "reasoningEffortOverride"} {
		if !strings.Contains(changed, expected) {
			t.Fatalf("变更字段缺少 %s：%v", expected, result.ChangedFields)
		}
	}
	if result.Items[0].ConfigRevision != rev1+1 || result.Items[1].ConfigRevision != rev2+1 {
		t.Fatalf("修订号未递增：%+v", result.Items)
	}
	if result.OwnerSystemAccountID != adminID {
		t.Fatalf("归属不一致：%s", result.OwnerSystemAccountID)
	}

	// DB 状态：调度字段、代理绑定、分组绑定与凭据覆盖。
	var concurrency, priority int
	var super bool
	if err := env.db.QueryRow(`SELECT concurrency_limit, priority, super_priority_enabled FROM accounts WHERE id = ?`, first).
		Scan(&concurrency, &priority, &super); err != nil {
		t.Fatal(err)
	}
	if concurrency != 88 || priority != 7 || !super {
		t.Fatalf("调度字段不一致：%d %d %v", concurrency, priority, super)
	}
	if got := env.queryCell(t, `SELECT proxy_profile_id FROM accounts WHERE id = ?`, first); got != "pp-batch" {
		t.Fatalf("代理绑定不一致：%s", got)
	}
	if got := env.queryCell(t, `SELECT local_priority FROM group_accounts WHERE account_id = ?`, first); got != "7" {
		t.Fatalf("分组本地优先级未同步：%s", got)
	}
	if got := env.queryCell(t, `SELECT notes FROM accounts WHERE id = ?`, first); got != "批量备注" {
		t.Fatalf("备注不一致：%s", got)
	}
	if got := env.queryCell(t, `SELECT last_error_code FROM accounts WHERE id = ?`, first); got != "" {
		t.Fatalf("未过期账户不应触发过期停用：%s", got)
	}
	var sealed string
	if err := env.db.QueryRow(`SELECT credentials_encrypted FROM accounts WHERE id = ?`, first).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	var credentials Credentials
	if err := DecryptJSON(testSecret, sealed, &credentials); err != nil {
		t.Fatalf("凭据解密失败：%v", err)
	}
	if credentials["service_tier_override"] != "priority" || credentials["reasoning_effort_override"] != "high" {
		t.Fatalf("覆盖字段未写入凭据：%v", credentials)
	}
	modes, ok := credentials["supported_endpoint_modes"].([]any)
	if !ok || len(modes) != 2 || modes[0] != "chat_json" || modes[1] != "chat_sse" {
		t.Fatalf("端点形态未写入凭据：%v", credentials["supported_endpoint_modes"])
	}
}

func TestW2BatchUpdateExpiredPackageDisables(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	first, second, rev1, rev2 := w2BatchAccounts(t, env, adminID)

	// 过期套餐触发自动停用链：status/schedulable/last_error_* 全部落库。
	input := BatchUpdateInput{
		Targets: []BatchUpdateTarget{{AccountID: first, ConfigRevision: rev1}, {AccountID: second, ConfigRevision: rev2}},
		Updates: w2EnabledFields(map[string]any{"accountExpiresAt": "2020-01-01T00:00:00Z"}),
	}
	result, err := w2BatchUpdate(t, env, adminID, input)
	if err != nil {
		t.Fatalf("BatchUpdate 错误：%v", err)
	}
	changed := strings.Join(result.ChangedFields, ",")
	for _, expected := range []string{"accountExpiresAt", "status", "schedulable"} {
		if !strings.Contains(changed, expected) {
			t.Fatalf("过期链变更字段缺少 %s：%v", expected, result.ChangedFields)
		}
	}
	row := env.queryCell(t, `SELECT status || '|' || schedulable || '|' || COALESCE(last_error_code,'') FROM accounts WHERE id = ?`, first)
	if row != "disabled|0|account_expired" {
		t.Fatalf("过期停用状态不一致：%s", row)
	}
}

func TestW2BatchUpdateNoChangeAndConflict(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	first, second, rev1, rev2 := w2BatchAccounts(t, env, adminID)

	t.Run("全部字段无变化", func(t *testing.T) {
		input := BatchUpdateInput{
			Targets: []BatchUpdateTarget{{AccountID: first, ConfigRevision: rev1}, {AccountID: second, ConfigRevision: rev2}},
			Updates: w2EnabledFields(map[string]any{"concurrencyLimit": float64(5000)}),
		}
		result, err := w2BatchUpdate(t, env, adminID, input)
		if err != nil {
			t.Fatalf("无变化批量不应报错：%v", err)
		}
		if len(result.ChangedFields) != 0 {
			t.Fatalf("无变化字段集不一致：%v", result.ChangedFields)
		}
		for _, item := range result.Items {
			if item.ConfigRevision != rev1 && item.ConfigRevision != rev2 {
				t.Fatalf("无变化时修订号不应递增：%+v", result.Items)
			}
		}
		var revision int64
		if err := env.db.QueryRow(`SELECT config_revision FROM accounts WHERE id = ?`, first).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if revision != rev1 {
			t.Fatalf("无变化时 DB 修订号不应递增：%d", revision)
		}
	})
	t.Run("CAS 修订冲突", func(t *testing.T) {
		// 先推进一次修订，再用旧修订号提交，触发 CAS 冲突。
		advance := BatchUpdateInput{
			Targets: []BatchUpdateTarget{{AccountID: first, ConfigRevision: rev1}, {AccountID: second, ConfigRevision: rev2}},
			Updates: w2EnabledFields(map[string]any{"notes": "推进修订"}),
		}
		if _, err := w2BatchUpdate(t, env, adminID, advance); err != nil {
			t.Fatalf("推进修订失败：%v", err)
		}
		input := BatchUpdateInput{
			Targets: []BatchUpdateTarget{{AccountID: first, ConfigRevision: rev1}, {AccountID: second, ConfigRevision: rev2}},
			Updates: w2EnabledFields(map[string]any{"concurrencyLimit": float64(77)}),
		}
		_, err := w2BatchUpdate(t, env, adminID, input)
		if err == nil {
			t.Fatal("过期修订号应冲突")
		}
		conflict, ok := err.(*batchVersionConflictError)
		// prepared 顺序不定：冲突可能先落在任一个使用旧修订号的目标上。
		if !ok || (conflict.AccountID != first && conflict.AccountID != second) {
			t.Fatalf("冲突错误语义不一致：%T %v", err, err)
		}
		if !strings.Contains(err.Error(), "账户配置已发生变化，请刷新后重试") {
			t.Fatalf("冲突消息不一致：%v", err)
		}
	})
}

func TestW2BatchUpdateValidationAndAccess(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	first, second, rev1, rev2 := w2BatchAccounts(t, env, adminID)

	targets := []BatchUpdateTarget{{AccountID: first, ConfigRevision: rev1}, {AccountID: second, ConfigRevision: rev2}}

	t.Run("目标数量与重复", func(t *testing.T) {
		_, err := w2BatchUpdate(t, env, adminID, BatchUpdateInput{Targets: targets[:1],
			Updates: w2EnabledFields(map[string]any{"notes": "x"})})
		if err == nil || err.Error() != batchRangeMessage {
			t.Fatalf("单目标应拒绝：%v", err)
		}
		duplicated := []BatchUpdateTarget{targets[0], targets[0]}
		_, err = w2BatchUpdate(t, env, adminID, BatchUpdateInput{Targets: duplicated,
			Updates: w2EnabledFields(map[string]any{"notes": "x"})})
		if err == nil || err.Error() != batchDuplicateMessage {
			t.Fatalf("重复目标应拒绝：%v", err)
		}
	})
	t.Run("无启用字段", func(t *testing.T) {
		_, err := w2BatchUpdate(t, env, adminID, BatchUpdateInput{Targets: targets,
			Updates: map[string]BatchUpdateField{"notes": {Enabled: false, Value: "x"}}})
		if err == nil || err.Error() != "请至少选择一项需要覆盖的配置" {
			t.Fatalf("无启用字段应拒绝：%v", err)
		}
	})
	t.Run("账户不可见", func(t *testing.T) {
		ghost := []BatchUpdateTarget{{AccountID: "acc-ghost-1", ConfigRevision: 1}, {AccountID: "acc-ghost-2", ConfigRevision: 1}}
		_, err := w2BatchUpdate(t, env, adminID, BatchUpdateInput{Targets: ghost,
			Updates: w2EnabledFields(map[string]any{"notes": "x"})})
		accessError, ok := err.(*batchAccessError)
		if !ok || accessError.Message != batchAccessDefaultMessage {
			t.Fatalf("不可见账户错误语义不一致：%T %v", err, err)
		}
	})
	t.Run("用户无作用域", func(t *testing.T) {
		// 用户（非 admin）无过滤范围时 scope 为自身；自身无这些账户 → 404 语义。
		userID := env.login(t, "batch-user", "batch-pass", "user")
		_, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{Targets: targets,
			Updates: w2EnabledFields(map[string]any{"notes": "x"})}, AccessScope{ViewerID: userID})
		// 用户 scope 钉在自身：他人账户不可见，走默认 404 语义文案。
		accessError, ok := err.(*batchAccessError)
		if !ok || accessError.Message != batchAccessDefaultMessage {
			t.Fatalf("用户侧错误不一致：%T %v", err, err)
		}
	})
	t.Run("凭据校验失败中止", func(t *testing.T) {
		_, err := w2BatchUpdate(t, env, adminID, BatchUpdateInput{Targets: targets,
			Updates: w2EnabledFields(map[string]any{"healthCheckModel": "not-in-list"})})
		if err == nil || !strings.Contains(err.Error(), "检查模型必须属于最终支持模型") {
			t.Fatalf("检查模型校验不一致：%v", err)
		}
		// 事务回滚：修订号不变。
		var revision int64
		if err := env.db.QueryRow(`SELECT config_revision FROM accounts WHERE id = ?`, first).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if revision != rev1 {
			t.Fatalf("失败批量不应递增修订号：%d", revision)
		}
	})
}

func TestW2ValidateBatchUpdateValueMatrix(t *testing.T) {
	// validateBatchUpdateValue 的所有拒绝都返回统一的 400 文案。
	cases := []struct {
		name   string
		field  string
		value  any
		reject bool
	}{
		{"tags 非数组", "tags", "x", true},
		{"proxyProfileId 非字符串", "proxyProfileId", 3, true},
		{"proxyProfileId 空白", "proxyProfileId", "  ", true},
		{"concurrencyLimit 非法", "concurrencyLimit", "x", true},
		{"concurrencyLimit 为零", "concurrencyLimit", float64(0), true},
		{"priority 负数", "priority", float64(-1), true},
		{"superPriorityEnabled 非布尔", "superPriorityEnabled", "yes", true},
		{"fallbackEnabled 非布尔", "fallbackEnabled", 1, true},
		{"accountExpiresAt 非字符串", "accountExpiresAt", 3, true},
		{"availabilitySchedule 非对象", "availabilitySchedule", []any{}, true},
		{"notes 非字符串", "notes", 9, true},
		{"errorHandlingRules 非数组", "errorHandlingRules", "x", true},
		{"responseInspectionRules 非数组", "responseInspectionRules", 7, true},
		{"supportedModels 空数组", "supportedModels", []any{}, true},
		{"supportedModels 空白项", "supportedModels", []any{"  "}, true},
		{"healthCheckModel 空白", "healthCheckModel", "  ", true},
		{"healthCheckEndpointMode 非法", "healthCheckEndpointMode", "grpc", true},
		{"modelMappings 非数组", "modelMappings", "x", true},
		{"serviceTierOverride 非法", "serviceTierOverride", "bogus", true},
		{"reasoningEffortOverride 非法", "reasoningEffortOverride", "ultra", true},
		{"未知字段直通", "mystery", 1, false},
		{"supportedEndpointModes 值合法性延后", "supportedEndpointModes", []any{"grpc"}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			message := validateBatchUpdateValue(testCase.field, testCase.value)
			if testCase.reject && message != batchFieldsPrompt {
				t.Fatalf("期望拒绝文案 %q，实际 %q", batchFieldsPrompt, message)
			}
			if !testCase.reject && message != "" {
				t.Fatalf("期望放行，实际 %q", message)
			}
		})
	}
	// nil 语义：可空字段放行，必填字段拒绝。
	for _, field := range []string{"proxyProfileId", "accountExpiresAt", "availabilitySchedule",
		"serviceTierOverride", "reasoningEffortOverride"} {
		if message := validateBatchUpdateValue(field, nil); message != "" {
			t.Fatalf("可空字段 nil 应放行：%s -> %q", field, message)
		}
	}
	for _, field := range []string{"tags", "concurrencyLimit", "priority", "superPriorityEnabled",
		"notes", "errorHandlingRules", "responseInspectionRules", "supportedModels",
		"healthCheckModel", "healthCheckEndpointMode", "modelMappings"} {
		if message := validateBatchUpdateValue(field, nil); message != batchFieldsPrompt {
			t.Fatalf("必填字段 nil 应拒绝：%s -> %q", field, message)
		}
	}
	// 合法值放行。
	if message := validateBatchUpdateValue("serviceTierOverride", "flex"); message != "" {
		t.Fatalf("合法服务层级应放行：%q", message)
	}
	if message := validateBatchUpdateValue("reasoningEffortOverride", "xhigh"); message != "" {
		t.Fatalf("合法推理力度应放行：%q", message)
	}
	if message := validateBatchUpdateValue("healthCheckEndpointMode", "chat_sse"); message != "" {
		t.Fatalf("合法检查形态应放行：%q", message)
	}
}

func TestW2ErrorHandlingRuleNormalization(t *testing.T) {
	t.Run("完整限流规则（duration/daily/weekly 三种恢复策略）", func(t *testing.T) {
		rules, err := normalizeAccountErrorHandlingRules([]any{
			map[string]any{"enabled": true, "name": "限流", "priority": float64(1), "action": "rate_limited",
				"status_codes": []any{float64(429)}, "reset_strategy": "duration", "duration_hours": float64(2),
				"description": "限流规则"},
			map[string]any{"enabled": true, "name": "每日", "priority": float64(2), "action": "rate_limited",
				"error_codes": []any{"insufficient_quota"}, "reset_strategy": "daily", "daily_reset_hour": float64(7)},
			map[string]any{"enabled": false, "name": "每周", "priority": float64(3), "action": "retry_next",
				"keywords": []any{"quota"}, "reset_strategy": "weekly", "weekly_reset_day": float64(1),
				"weekly_reset_hour": float64(8)},
		})
		if err != nil {
			t.Fatalf("规则归一失败：%v", err)
		}
		if len(rules) != 3 {
			t.Fatalf("规则数量不一致：%d", len(rules))
		}
		first := rules[0].(map[string]any)
		if first["reset_strategy"] != "duration" || first["duration_hours"] != float64(2) || first["description"] != "限流规则" {
			t.Fatalf("duration 规则不一致：%v", first)
		}
		second := rules[1].(map[string]any)
		if second["daily_reset_hour"] != float64(7) {
			t.Fatalf("daily 规则不一致：%v", second)
		}
	})
	t.Run("校验错误矩阵", func(t *testing.T) {
		cases := []struct {
			name    string
			rule    any
			message string
		}{
			{"非对象", "x", "规则格式无效"},
			{"系统继承规则", map[string]any{"source": "system"}, "不能写入系统继承规则"},
			{"不支持字段", map[string]any{"bogus": 1}, "不支持字段"},
			{"缺 enabled", map[string]any{"name": "n"}, "启用状态"},
			{"缺 name", map[string]any{"enabled": true}, "名称"},
			{"priority 非法", map[string]any{"enabled": true, "name": "n", "priority": float64(0)}, "优先级"},
			{"action 非法", map[string]any{"enabled": true, "name": "n", "priority": float64(1), "action": "boom"}, "错误处理动作"},
			{"无条件", map[string]any{"enabled": true, "name": "n", "priority": float64(1), "action": "retry_next"}, "至少需要一个匹配条件"},
			{"status_codes 非法", map[string]any{"enabled": true, "name": "n", "priority": float64(1), "action": "retry_next", "status_codes": []any{float64(99)}}, "状态码"},
			{"限流缺恢复策略", map[string]any{"enabled": true, "name": "n", "priority": float64(1), "action": "rate_limited", "error_types": []any{"timeout"}}, "恢复策略"},
			{"duration 缺小时", map[string]any{"enabled": true, "name": "n", "priority": float64(1), "action": "rate_limited", "error_types": []any{"timeout"}, "reset_strategy": "duration"}, "恢复小时数"},
			{"daily 小时越界", map[string]any{"enabled": true, "name": "n", "priority": float64(1), "action": "rate_limited", "error_types": []any{"timeout"}, "reset_strategy": "daily", "daily_reset_hour": float64(24)}, "0-23"},
			{"weekly 缺日期", map[string]any{"enabled": true, "name": "n", "priority": float64(1), "action": "rate_limited", "error_types": []any{"timeout"}, "reset_strategy": "weekly", "weekly_reset_hour": float64(8)}, "每周恢复日期"},
			{"weekly 日期越界", map[string]any{"enabled": true, "name": "n", "priority": float64(1), "action": "rate_limited", "error_types": []any{"timeout"}, "reset_strategy": "weekly", "weekly_reset_day": float64(7), "weekly_reset_hour": float64(8)}, "每周恢复日期"},
			{"description 非字符串", map[string]any{"enabled": true, "name": "n", "priority": float64(1), "action": "retry_next", "status_codes": []any{float64(500)}, "description": 1}, "规则描述"},
		}
		for _, testCase := range cases {
			_, err := normalizeAccountErrorHandlingRules([]any{testCase.rule})
			if err == nil {
				t.Fatalf("%s：期望错误", testCase.name)
			}
			if !strings.Contains(err.Error(), testCase.message) {
				t.Fatalf("%s：消息缺少 %q：%v", testCase.name, testCase.message, err)
			}
		}
	})
	t.Run("nil 与非数组", func(t *testing.T) {
		if rules, err := normalizeAccountErrorHandlingRules(nil); err != nil || len(rules) != 0 {
			t.Fatalf("nil 应归一为空列表：%v %v", rules, err)
		}
		if _, err := normalizeAccountErrorHandlingRules("x"); err == nil {
			t.Fatal("非数组应拒绝")
		}
	})
}

func TestW2MarshalJSONValueAndHelpers(t *testing.T) {
	data, err := json.Marshal(map[string]any{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	// marshalJSONValue 对可序列化值返回 JSON 文本，失败时返回空串。
	if got := marshalJSONValue(map[string]any{"a": 1}); !strings.Contains(got, `"a"`) {
		t.Fatalf("序列化结果不一致：%q", got)
	}
	if got := marshalJSONValue(make(chan int)); got != "" {
		t.Fatalf("不可序列化值应返回空串：%q", got)
	}
	_ = data

	// intValue/boolValue 的类型回退。
	if intValue("x", 3) != 3 || intValue(float64(5), 3) != 5 {
		t.Fatal("intValue 回退不一致")
	}
	if boolValue("x", true) != true || boolValue(false, true) != false {
		t.Fatal("boolValue 回退不一致")
	}
	// batchAccessError 的同作用域变体。
	if !(&batchAccessError{Message: "x 同一系统账户作用域 y"}).sameScope() {
		t.Fatal("同作用域判定不一致")
	}
	if (&batchAccessError{Message: "plain"}).sameScope() {
		t.Fatal("普通错误不应命中同作用域")
	}
	// batchStatusForcesSchedulableOff 的状态表。
	for _, status := range []string{"pending_test", "error", "rate_limited", "temporary_unavailable"} {
		if !batchStatusForcesSchedulableOff(status) {
			t.Fatalf("状态应强制关闭调度：%s", status)
		}
	}
	if batchStatusForcesSchedulableOff("active") {
		t.Fatal("active 不应强制关闭调度")
	}
	// accountScheduleError 的文案转换。
	err = accountScheduleError(&ValidationError{Message: "API Key 时间计划模式无效"})
	if err.Error() != "账户时间计划模式无效" {
		t.Fatalf("文案转换不一致：%v", err)
	}
	_ = time.Now
}
