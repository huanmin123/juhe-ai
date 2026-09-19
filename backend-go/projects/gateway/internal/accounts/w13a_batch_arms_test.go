package accounts

// w13a batch.go 未覆盖臂补齐：批量更新的字段矩阵（凭据覆盖、健康检查模型/
// 形态、模型映射、代理解析、优先级互斥、过期时间、时间计划、备注、调度列）
// 与 batchUpdateBody/validateBatchUpdateValue 解析臂、错误类型与批次副作用。

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"
)

// w13aBatchAccounts 建一对可批量编辑的 openai 账户，返回目标列表。
func w13aBatchAccounts(t *testing.T, env *testEnv, suffix string) (adminID string, targets []BatchUpdateTarget) {
	t.Helper()
	adminID = env.login(t, "root", "root-pass", "super_admin")
	now := "2026-09-17T00:00:00.000Z"
	env.exec(t, `INSERT OR IGNORE INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-openai-compat', 'openai', 'OpenAI 兼容', 1, '["gpt-4o-mini"]', ?, ?)`, now, now)
	env.exec(t, `INSERT OR IGNORE INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('profile_openai_openai_v1', 'openai', 'OpenAI 兼容协议', 1, 'openai', 'v1',
		'https://api.openai.com/v1', 'gpt-4o-mini', '["api_key","oauth"]', '[]', ?, ?)`, now, now)
	env.seedAccount(t, "acc-w13a-b1"+suffix, adminID, "w13a-b1"+suffix, "active")
	env.seedAccount(t, "acc-w13a-b2"+suffix, adminID, "w13a-b2"+suffix, "active")
	env.exec(t, `INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
		VALUES ('acc-w13a-b1`+suffix+`', 'openai', 'gpt-4o-mini', '2026-09-17T00:00:00.000Z'),
		       ('acc-w13a-b2`+suffix+`', 'openai', 'gpt-4o-mini', '2026-09-17T00:00:00.000Z')`)
	// 凭据覆盖批次的归一化要求 base_url，种子里补全。
	for _, id := range []string{"acc-w13a-b1" + suffix, "acc-w13a-b2" + suffix} {
		sealed, err := EncryptJSON(testSecret, Credentials{"api_key": "sk-w13a-" + id, "base_url": "https://api.openai.com/v1"})
		if err != nil {
			t.Fatal(err)
		}
		env.exec(t, `UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`, sealed, id)
	}
	revision := func(id string) int64 {
		var revision int64
		if err := env.db.QueryRow(`SELECT config_revision FROM accounts WHERE id = ?`, id).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		return revision
	}
	targets = []BatchUpdateTarget{
		{AccountID: "acc-w13a-b1" + suffix, ConfigRevision: revision("acc-w13a-b1" + suffix)},
		{AccountID: "acc-w13a-b2" + suffix, ConfigRevision: revision("acc-w13a-b2" + suffix)},
	}
	return adminID, targets
}

func TestW13ABatchUpdateFieldMatrix(t *testing.T) {
	env := newTestEnv(t)
	adminID, targets := w13aBatchAccounts(t, env, "")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	field := func(name string, value any) BatchUpdateField {
		return BatchUpdateField{Enabled: true, Value: value}
	}

	// 基础字段矩阵：并发、优先级、备注、降级备用、过期清除。
	result, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"concurrencyLimit": field("concurrencyLimit", float64(9)),
			"priority":         field("priority", float64(3)),
			"notes":            field("notes", "w13a 批量备注"),
			"fallbackEnabled":  field("fallbackEnabled", true),
			"accountExpiresAt": field("accountExpiresAt", nil),
			"tags":             field("tags", []any{"w13a-batch-tag"}),
		},
	}, scope)
	if err != nil {
		t.Fatalf("基础批量应成功：%v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("批量结果项不一致：%+v", result.Items)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM accounts WHERE concurrency_limit = 9`); got != 2 {
		t.Fatalf("并发限制应批量落库：%d", got)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM accounts WHERE notes = 'w13a 批量备注'`); got != 2 {
		t.Fatalf("备注应批量落库：%d", got)
	}

	// 后续批次都需要最新版本号。
	refresh := func() {
		t.Helper()
		for index := range targets {
			var revision int64
			if err := env.db.QueryRow(`SELECT config_revision FROM accounts WHERE id = ?`, targets[index].AccountID).Scan(&revision); err != nil {
				t.Fatal(err)
			}
			targets[index].ConfigRevision = revision
		}
	}

	// 超级优先 + 降级备用同开 → 拒绝（batch.go 1235-1237 else 臂）。
	refresh()
	_, err = env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"superPriorityEnabled": field("superPriorityEnabled", true),
			"fallbackEnabled":      field("fallbackEnabled", true),
		},
	}, scope)
	if err == nil || !strings.Contains(err.Error(), "超级优先和降级备用不能同时开启") {
		t.Fatalf("超级优先+降级应拒绝：%v", err)
	}
	// 单开超级优先：已在开启降级的行上自动关闭降级（互斥的自动让位臂）。
	refresh()
	_, err = env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"superPriorityEnabled": field("superPriorityEnabled", true),
		},
	}, scope)
	if err != nil {
		t.Fatalf("单开超级优先应成功：%v", err)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM accounts WHERE super_priority_enabled = 1 AND fallback_enabled = 0`); got != 2 {
		t.Fatalf("超级优先应自动关闭降级：%d", got)
	}

	// 时间计划：非法值（错误文案换牌）与合法值（含下一检查点）。
	refresh()
	_, err = env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"availabilitySchedule": field("availabilitySchedule", "bogus"),
		},
	}, scope)
	if err == nil || !strings.Contains(err.Error(), "账户时间计划") {
		t.Fatalf("非法时间计划应换牌报错：%v", err)
	}
	refresh()
	validSchedule := map[string]any{
		"enabled": true, "timezone": "UTC", "mode": "allow_windows",
		"windows": []any{map[string]any{"daysOfWeek": []any{float64(1), float64(2), float64(3), float64(4), float64(5), float64(6), float64(7)}, "start": "09:00", "end": "18:00"}},
	}
	refresh()
	if _, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"availabilitySchedule": field("availabilitySchedule", validSchedule),
		},
	}, scope); err != nil {
		t.Fatalf("合法时间计划应成功：%v", err)
	}

	// 代理：不存在/停用拒绝，启用代理生效，null 清除。
	refresh()
	_, err = env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"proxyProfileId": field("proxyProfileId", "proxy-w13a-none"),
		},
	}, scope)
	if err == nil || !strings.Contains(err.Error(), "代理不存在或已停用") {
		t.Fatalf("缺失代理应拒绝：%v", err)
	}
	now := "2026-09-17T00:00:00.000Z"
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at)
		VALUES ('proxy-w13a-batch', ?, 'w13a-batch-proxy', 'socks5', 'h', 1080, 1, 'unknown', ?, ?)`, adminID, now, now)
	refresh()
	if _, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"proxyProfileId": field("proxyProfileId", "proxy-w13a-batch"),
		},
	}, scope); err != nil {
		t.Fatalf("启用代理应生效：%v", err)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM accounts WHERE proxy_profile_id = 'proxy-w13a-batch'`); got != 2 {
		t.Fatalf("代理应批量绑定：%d", got)
	}

	// 模型配置：检查模型越界拒绝（不在最终支持模型集合）。
	refresh()
	_, err = env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"healthCheckModel": field("healthCheckModel", "gpt-x"),
		},
	}, scope)
	if err == nil || !strings.Contains(err.Error(), "检查模型必须属于最终支持模型") {
		t.Fatalf("检查模型越界应拒绝：%v", err)
	}

	// 凭据覆盖：支持模型 + 端点能力 + 服务分层 + 推理力度。
	refresh()
	if _, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets,
		Updates: map[string]BatchUpdateField{
			"supportedModels":         field("supportedModels", []any{"gpt-4o-mini", "gpt-4.1"}),
			"healthCheckModel":        field("healthCheckModel", "gpt-4.1"),
			"supportedEndpointModes":  field("supportedEndpointModes", []any{"chat_json"}),
			"serviceTierOverride":     field("serviceTierOverride", "flex"),
			"reasoningEffortOverride": field("reasoningEffortOverride", "high"),
		},
	}, scope); err != nil {
		t.Fatalf("模型配置批量应成功：%v", err)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM account_supported_models WHERE model = 'gpt-4.1'`); got != 2 {
		t.Fatalf("支持模型应批量替换：%d", got)
	}
	sealed := env.queryCell(t, `SELECT credentials_encrypted FROM accounts WHERE id = ?`, "acc-w13a-b1")
	credentials := Credentials{}
	if err := DecryptJSON(testSecret, sealed, &credentials); err != nil {
		t.Fatal(err)
	}
	if credentials["service_tier_override"] != "flex" || credentials["reasoning_effort_override"] != "high" {
		t.Fatalf("凭据覆盖应落库：%v", credentials)
	}
}

func TestW13ABatchUpdateValidationArms(t *testing.T) {
	env := newTestEnv(t)
	adminID, targets := w13aBatchAccounts(t, env, "v")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	enabled := BatchUpdateField{Enabled: true, Value: true}

	// 目标断言：数量不足 / 重复 / 版本非法。
	if err := assertBatchTargets(nil); err == nil {
		t.Fatal("空目标应拒绝")
	}
	if err := assertBatchTargets([]BatchUpdateTarget{
		{AccountID: "a", ConfigRevision: 1}, {AccountID: "a", ConfigRevision: 1},
	}); err == nil {
		t.Fatal("重复目标应拒绝")
	}
	if err := assertBatchTargets([]BatchUpdateTarget{
		{AccountID: "a", ConfigRevision: 0}, {AccountID: "b", ConfigRevision: 1},
	}); err == nil {
		t.Fatal("非法版本应拒绝")
	}
	// 更新为空。
	if _, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{Targets: targets, Updates: map[string]BatchUpdateField{}}, scope); err == nil {
		t.Fatal("空更新应拒绝")
	}
	// 模型配置跨签名：两个不同供应商档案的账户不能批量覆盖模型字段。
	env.seedAccount(t, "acc-w13a-b1x", adminID, "w13a-b1x", "active")
	env.seedAccount(t, "acc-w13a-b2x", adminID, "w13a-b2x", "active")
	revision := func(id string) int64 {
		var value int64
		if err := env.db.QueryRow(`SELECT config_revision FROM accounts WHERE id = ?`, id).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	mixed := []BatchUpdateTarget{
		{AccountID: "acc-w13a-b1x", ConfigRevision: revision("acc-w13a-b1x")},
		{AccountID: "acc-w13a-b2x", ConfigRevision: revision("acc-w13a-b2x")},
	}
	// seedAccount 全部落在 gpt/prof-gpt，第二账户改成 openai 兼容档案。
	env.exec(t, `UPDATE accounts SET provider_code = 'openai', provider_protocol_profile_id = ?
		WHERE id = 'acc-w13a-b2x'`, openAICompatibleProfileID)
	_, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: mixed,
		Updates: map[string]BatchUpdateField{
			"supportedModels": {Enabled: true, Value: []any{"gpt-4o-mini"}},
		},
	}, scope)
	if err == nil || !strings.Contains(err.Error(), "模型与协议配置只能批量覆盖") {
		t.Fatalf("跨签名模型批量应拒绝：%v", err)
	}

	// 过期套餐触发自动停用运行态。
	future := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)
	_, targets2 := w13aBatchAccounts(t, env, "e")
	if _, err := env.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: targets2,
		Updates: map[string]BatchUpdateField{
			"accountExpiresAt": {Enabled: true, Value: future},
		},
	}, scope); err != nil {
		t.Fatalf("过期套餐批量应成功：%v", err)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM accounts WHERE status = 'disabled' AND last_error_code = 'account_expired'`); got != 2 {
		t.Fatalf("过期应自动停用：%d", got)
	}
	_ = enabled
}

func TestW13ABatchUpdateBodyAndValueValidation(t *testing.T) {
	// batchUpdateBody：未知键、目标缺失、非法目标、非法更新。
	if _, message := batchUpdateBody(map[string]any{"bogus": 1}); message == "" {
		t.Fatal("未知键应拒绝")
	}
	if _, message := batchUpdateBody(map[string]any{}); message == "" {
		t.Fatal("缺 targets 应拒绝")
	}
	if _, message := batchUpdateBody(map[string]any{"targets": []any{}}); message == "" {
		t.Fatal("目标过少应拒绝")
	}
	if _, message := batchUpdateBody(map[string]any{"targets": []any{"x"}}); message == "" {
		t.Fatal("目标非对象应拒绝")
	}
	if _, message := batchUpdateBody(map[string]any{"targets": []any{
		map[string]any{"accountId": "a", "bogus": 1}, map[string]any{"accountId": "b", "configRevision": float64(1)},
	}}); message == "" {
		t.Fatal("目标未知键应拒绝")
	}
	if _, message := batchUpdateBody(map[string]any{"targets": []any{
		map[string]any{"accountId": "a", "configRevision": float64(0)}, map[string]any{"accountId": "b", "configRevision": float64(1)},
	}}); message == "" {
		t.Fatal("非法版本应拒绝")
	}
	if _, message := batchUpdateBody(map[string]any{"targets": []any{
		map[string]any{"accountId": "a", "configRevision": float64(1)}, map[string]any{"accountId": "a", "configRevision": float64(1)},
	}}); message == "" {
		t.Fatal("重复目标应拒绝")
	}
	base := func(updates map[string]any) map[string]any {
		return map[string]any{"targets": []any{
			map[string]any{"accountId": "a", "configRevision": float64(1)},
			map[string]any{"accountId": "b", "configRevision": float64(1)},
		}, "updates": updates}
	}
	if _, message := batchUpdateBody(base(nil)); message == "" {
		t.Fatal("缺 updates 应拒绝")
	}
	if _, message := batchUpdateBody(base(map[string]any{"bogus": map[string]any{"enabled": true, "value": 1}})); message == "" {
		t.Fatal("未知更新字段应拒绝")
	}
	if _, message := batchUpdateBody(base(map[string]any{"notes": "x"})); message == "" {
		t.Fatal("更新非对象应拒绝")
	}
	if _, message := batchUpdateBody(base(map[string]any{"notes": map[string]any{"enabled": true, "bogus": 1}})); message == "" {
		t.Fatal("更新未知键应拒绝")
	}
	if _, message := batchUpdateBody(base(map[string]any{"notes": map[string]any{"enabled": "x", "value": 1}})); message == "" {
		t.Fatal("enabled 非布尔应拒绝")
	}
	if _, message := batchUpdateBody(base(map[string]any{"notes": map[string]any{"enabled": true}})); message == "" {
		t.Fatal("enabled 缺 value 应拒绝")
	}
	if _, message := batchUpdateBody(base(map[string]any{"notes": map[string]any{"enabled": true, "value": 1}})); message == "" {
		t.Fatal("notes 非字符串应拒绝")
	}
	input, message := batchUpdateBody(base(map[string]any{
		"notes":            map[string]any{"enabled": false, "value": "x"},
		"concurrencyLimit": map[string]any{"enabled": true, "value": float64(5)},
	}))
	if message != "" || len(input.Targets) != 2 || len(input.Updates) != 2 {
		t.Fatalf("合法批量体应通过：%+v %q", input, message)
	}

	// validateBatchUpdateValue 全字段臂。
	cases := []struct {
		field string
		value any
		want  bool
	}{
		{"tags", "x", false}, {"tags", []any{}, true},
		{"proxyProfileId", "", false}, {"proxyProfileId", nil, true},
		{"concurrencyLimit", float64(0), false}, {"concurrencyLimit", float64(3), true},
		{"priority", float64(-1), false}, {"priority", float64(1), true},
		{"superPriorityEnabled", "x", false}, {"superPriorityEnabled", true, true},
		{"fallbackEnabled", "x", false}, {"fallbackEnabled", false, true},
		{"accountExpiresAt", " ", false}, {"accountExpiresAt", nil, true},
		{"availabilitySchedule", "x", false}, {"availabilitySchedule", map[string]any{}, true},
		{"notes", 3, false}, {"notes", "x", true},
		{"errorHandlingRules", []any{}, true},
		{"responseInspectionRules", "x", false}, {"responseInspectionRules", []any{}, true},
		{"supportedModels", []any{}, false}, {"supportedModels", []any{" m "}, true},
		{"healthCheckModel", " ", false}, {"healthCheckModel", "m", true},
		{"healthCheckEndpointMode", "bogus", false}, {"healthCheckEndpointMode", "chat_sse", true},
		{"modelMappings", "x", false}, {"modelMappings", []any{}, true},
		{"supportedEndpointModes", []any{}, false}, {"supportedEndpointModes", []any{"x"}, true},
		{"serviceTierOverride", 3, false}, {"serviceTierOverride", "bogus", false}, {"serviceTierOverride", "flex", true},
		{"reasoningEffortOverride", 3, false}, {"reasoningEffortOverride", "high", true},
	}
	for _, testCase := range cases {
		if got := validateBatchUpdateValue(testCase.field, testCase.value) == ""; got != testCase.want {
			t.Fatalf("validateBatchUpdateValue(%s, %v)：got %v want %v", testCase.field, testCase.value, got, testCase.want)
		}
	}
}

func TestW13ABatchHelpersAndSideEffects(t *testing.T) {
	// nullableTextEqual：nil 与非 nil、同为 nil。
	if nullableTextEqual(nil, nil) != true {
		t.Fatal("双 nil 应相等")
	}
	a, b := "x", "y"
	if nullableTextEqual(&a, &b) {
		t.Fatal("不同文本不应相等")
	}
	if !nullableTextEqual(&a, &a) {
		t.Fatal("相同文本应相等")
	}
	if nullableTextEqual(&a, nil) {
		t.Fatal("nil 与非 nil 不应相等")
	}
	// batchStatusForcesSchedulableOff。
	for _, status := range []string{"pending_test", "error", "rate_limited", "temporary_unavailable"} {
		if !batchStatusForcesSchedulableOff(status) {
			t.Fatalf("状态 %s 应强制关调度", status)
		}
	}
	if batchStatusForcesSchedulableOff("active") {
		t.Fatal("active 不应强制关调度")
	}
	// accountScheduleError：ValidationError 换牌与非校验错误透传。
	rebranded := accountScheduleError(&ValidationError{Message: "API Key 时间计划无效"})
	if !strings.Contains(rebranded.Error(), "账户时间计划无效") {
		t.Fatalf("错误文案应换牌：%v", rebranded)
	}
	if accountScheduleError(context.DeadlineExceeded) != context.DeadlineExceeded {
		t.Fatal("非校验错误应透传")
	}
	// storedEndpointModes：非数组、去重、非字符串。
	if got := storedEndpointModes("x"); len(got) != 0 {
		t.Fatalf("非数组应为空：%v", got)
	}
	if got := storedEndpointModes([]any{"a", "a", 3, "b"}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("端点能力应去重过滤：%v", got)
	}
	// batchAccessError / batchVersionConflictError 语义（错误族直测）。
	if (&batchAccessError{Message: batchSameScopeMessage}).SameScope() != true {
		t.Fatal("同作用域错误应标记")
	}
	if (&batchVersionConflictError{AccountID: "acc-1"}).Error() == "" {
		t.Fatal("版本冲突错误应有消息")
	}
	// normalizeBatchModelMappings：非数组 / 项非对象 / 字段缺失 / 族非法 /
	// 恒等丢弃 / 重复 source。
	if _, err := normalizeBatchModelMappings("x"); err == nil {
		t.Fatal("非数组应拒绝")
	}
	if _, err := normalizeBatchModelMappings([]any{"x"}); err == nil {
		t.Fatal("项非对象应拒绝")
	}
	if _, err := normalizeBatchModelMappings([]any{map[string]any{}}); err == nil {
		t.Fatal("字段缺失应拒绝")
	}
	if _, err := normalizeBatchModelMappings([]any{map[string]any{
		"sourceModel": "m", "sourceEndpointFamily": "bogus", "upstreamModel": "u", "upstreamEndpointFamily": "responses",
	}}); err == nil {
		t.Fatal("非法族应拒绝")
	}
	identity, err := normalizeBatchModelMappings([]any{map[string]any{
		"sourceModel": "m", "sourceEndpointFamily": "chat_completions", "upstreamModel": "m", "upstreamEndpointFamily": "chat_completions",
	}})
	if err != nil || len(identity) != 0 {
		t.Fatalf("恒等映射应丢弃：%+v %v", identity, err)
	}
	dedup, err := normalizeBatchModelMappings([]any{
		map[string]any{"sourceModel": "m", "sourceEndpointFamily": "chat_completions", "upstreamModel": "u1", "upstreamEndpointFamily": "responses"},
		map[string]any{"sourceModel": "m", "sourceEndpointFamily": "chat_completions", "upstreamModel": "u2", "upstreamEndpointFamily": "responses"},
	})
	if err == nil || !strings.Contains(err.Error(), "不能重复配置") {
		t.Fatalf("重复 source 应拒绝：%+v %v", dedup, err)
	}
	_ = sql.NullString{}
	_ = http.StatusOK
	_ = time.Now
}
