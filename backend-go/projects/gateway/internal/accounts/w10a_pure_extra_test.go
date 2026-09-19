package accounts

// w10a 纯函数与 SQL 臂第二批：schedule 日期范围、凭据可选字段辅助、YAML
// 值树归一化、clone 投影与余额配置、batch 加载/覆盖辅助、test 会话取消原因
// 与任务投影、Key 池不可验证消息、错误码列表、store 占位符改写。纯函数直测
// + 真实 sqlite 的映射加载。

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestW10ANormalizeScheduleDateRange(t *testing.T) {
	if result, err := normalizeScheduleDateRange(nil); err != nil || result != nil {
		t.Fatalf("nil 输入应返回 nil：%v %v", result, err)
	}
	if _, err := normalizeScheduleDateRange("x"); err == nil || err.Error() != "API Key 时间计划生效日期范围无效" {
		t.Fatalf("非对象输入应拒绝：%v", err)
	}
	if _, err := normalizeScheduleDateRange(map[string]any{"bogus": 1}); err == nil {
		t.Fatal("未知键应拒绝")
	}
	if _, err := normalizeScheduleDateRange(map[string]any{"startDate": "2026-13-01", "endDate": "2026-12-01"}); err == nil {
		t.Fatal("非法开始日期应拒绝")
	}
	if _, err := normalizeScheduleDateRange(map[string]any{"startDate": "2026-01-02", "endDate": "2026-01-01"}); err == nil {
		t.Fatal("开始晚于结束应拒绝")
	}
	if result, err := normalizeScheduleDateRange(map[string]any{"startDate": nil, "endDate": nil}); err != nil || result != nil {
		t.Fatalf("双空日期应返回 nil：%v %v", result, err)
	}
	result, err := normalizeScheduleDateRange(map[string]any{"startDate": "2026-01-01", "endDate": "2026-02-01"})
	if err != nil || result == nil || result.StartDate != "2026-01-01" || result.EndDate != "2026-02-01" {
		t.Fatalf("合法范围应通过：%+v %v", result, err)
	}
	// normalizeDateKey 分支：非字符串与非日期格式。
	if _, err := normalizeDateKey(3, "开始日期"); err == nil {
		t.Fatal("非字符串日期应拒绝")
	}
	if _, err := normalizeDateKey("not-a-date", "例外日期"); err == nil {
		t.Fatal("格式错误日期应拒绝")
	}
	if value, err := normalizeDateKey(" 2026-09-16 ", "例外日期"); err != nil || value != "2026-09-16" {
		t.Fatalf("合法日期应归一化：%q %v", value, err)
	}
	if value, err := normalizeDateKey(nil, "例外日期"); err != nil || value != "" {
		t.Fatalf("nil 日期应返回空：%q %v", value, err)
	}
}

func TestW10ACredentialOptionalHelpers(t *testing.T) {
	// optionalCredentialToken 全分支。
	if value, err := optionalCredentialToken(optionalValue{}, "提示词模板"); err != nil || value != "" {
		t.Fatalf("缺失字段应返回空：%q %v", value, err)
	}
	if value, err := optionalCredentialToken(optionalValue{present: true}, "提示词模板"); err != nil || value != "" {
		t.Fatalf("null 值应返回空：%q %v", value, err)
	}
	if value, err := optionalCredentialToken(credentialField(map[string]any{"k": ""}, "k"), "提示词模板"); err != nil || value != "" {
		t.Fatalf("空字符串应返回空：%q %v", value, err)
	}
	if _, err := optionalCredentialToken(credentialField(map[string]any{"k": 3}, "k"), "提示词模板"); err == nil {
		t.Fatal("非字符串应拒绝")
	}
	if value, err := optionalCredentialToken(credentialField(map[string]any{"k": "  abc  "}, "k"), "提示词模板"); err == nil || value != "" {
		t.Fatalf("带空白应拒绝：%q %v", value, err)
	}
	if _, err := optionalCredentialToken(credentialField(map[string]any{"k": "a b"}, "k"), "提示词模板"); err == nil {
		t.Fatal("含空格应拒绝")
	}
	if value, err := optionalCredentialToken(credentialField(map[string]any{"k": "abc-123"}, "k"), "提示词模板"); err != nil || value != "abc-123" {
		t.Fatalf("合法 token 应通过：%q %v", value, err)
	}

	// copyOptionalCredentialNonNegativeInteger 全分支。
	output := Credentials{}
	if err := copyOptionalCredentialNonNegativeInteger(map[string]any{}, output, "n", "字段"); err != nil {
		t.Fatalf("缺失字段应通过：%v", err)
	}
	if err := copyOptionalCredentialNonNegativeInteger(map[string]any{"n": nil}, output, "n", "字段"); err != nil {
		t.Fatalf("null 应通过：%v", err)
	}
	if err := copyOptionalCredentialNonNegativeInteger(map[string]any{"n": ""}, output, "n", "字段"); err != nil {
		t.Fatalf("空串应通过：%v", err)
	}
	if err := copyOptionalCredentialNonNegativeInteger(map[string]any{"n": float64(3)}, output, "n", "字段"); err != nil || output["n"] != float64(3) {
		t.Fatalf("合法数字应拷贝：%v %v", output["n"], err)
	}
	if err := copyOptionalCredentialNonNegativeInteger(map[string]any{"n": float64(-1)}, output, "n", "字段"); err == nil {
		t.Fatal("负数应拒绝")
	}
	if err := copyOptionalCredentialNonNegativeInteger(map[string]any{"n": float64(1.5)}, output, "n", "字段"); err == nil {
		t.Fatal("非整数应拒绝")
	}
	out2 := Credentials{}
	if err := copyOptionalCredentialNonNegativeInteger(map[string]any{"n": " 42 "}, out2, "n", "字段"); err != nil || out2["n"] != "42" {
		t.Fatalf("数字字符串应拷贝：%v %v", out2["n"], err)
	}
	if err := copyOptionalCredentialNonNegativeInteger(map[string]any{"n": "x"}, output, "n", "字段"); err == nil {
		t.Fatal("非数字字符串应拒绝")
	}

	// credentialsDeepEqual 与 assertAccountCredentialsJSONSize 分支。
	if !credentialsDeepEqual(Credentials{"a": "b"}, Credentials{"a": "b"}) {
		t.Fatal("相同凭据应相等")
	}
	if credentialsDeepEqual(nil, Credentials{"a": "b"}) {
		t.Fatal("nil 与非空记录不应相等")
	}
	if !credentialsDeepEqual(nil, Credentials{}) {
		t.Fatal("nil 与空记录应相等（密封列不存 null）")
	}
	big := Credentials{"k": strings.Repeat("x", 200)}
	_ = big
	if err := assertAccountCredentialsJSONSize(Credentials{"k": "v"}); err != nil {
		t.Fatalf("小凭据应通过：%v", err)
	}
	huge := Credentials{"k": strings.Repeat("x", 300000)}
	if err := assertAccountCredentialsJSONSize(huge); err == nil {
		t.Fatal("超大凭据应拒绝")
	}
}

func TestW10ANormalizeYAMLValueAndKeys(t *testing.T) {
	if yamlKeyString("plain") != "plain" {
		t.Fatal("字符串键应原样返回")
	}
	if yamlKeyString(42) != "42" || yamlKeyString(true) != "true" {
		t.Fatal("非字符串键应按 JS 强制转换")
	}
	input := map[any]any{
		"count":   3,
		"enabled": true,
		"nested":  map[any]any{"when": time.Date(2026, 9, 16, 8, 0, 0, 0, time.UTC)},
		"items":   []any{int64(1), uint64(2), float32(1.5)},
	}
	normalized := normalizeYAMLValue(input)
	object, ok := normalized.(map[string]any)
	if !ok {
		t.Fatalf("应返回 JSON 形态 map：%T", normalized)
	}
	if object["count"] != float64(3) {
		t.Fatalf("整数应转 float64：%T", object["count"])
	}
	if object["enabled"] != true {
		t.Fatalf("布尔应保留：%T", object["enabled"])
	}
	if _, ok := object["nested"].(map[string]any); !ok {
		t.Fatalf("嵌套 map 应转换：%T", object["nested"])
	}
	items, ok := object["items"].([]any)
	if !ok || items[0] != float64(1) || items[1] != float64(2) || items[2] != float64(1.5) {
		t.Fatalf("列表元素应转 float64：%v", object["items"])
	}
	nested := object["nested"].(map[string]any)
	if nested["when"] != "2026-09-16T08:00:00.000Z" {
		t.Fatalf("时间应渲染 ISO 字符串：%v", nested["when"])
	}
}

func TestW10ANewTagID(t *testing.T) {
	id := NewTagID()
	if !strings.HasPrefix(id, "acctag_") {
		t.Fatalf("Tag ID 前缀不一致：%s", id)
	}
	if NewAccountID() == "" {
		t.Fatal("Account ID 不应为空")
	}
}

func TestW10ABatchAndCloneErrorTypes(t *testing.T) {
	accessErr := &batchAccessError{Message: batchSameScopeMessage}
	if accessErr.Error() != batchSameScopeMessage || !accessErr.SameScope() {
		t.Fatalf("batchAccessError 语义不一致：%s", accessErr.Error())
	}
	if (&batchAccessError{Message: batchAccessDefaultMessage}).SameScope() {
		t.Fatal("默认消息不应标记同作用域")
	}
	versionErr := &batchVersionConflictError{AccountID: "acc-1"}
	if versionErr.Error() != "账户配置已发生变化，请刷新后重试：acc-1" {
		t.Fatalf("版本冲突消息不一致：%s", versionErr.Error())
	}
	forbidden := &cloneInteractionForbiddenError{Message: "授权实例不能克隆"}
	if forbidden.Error() != "授权实例不能克隆" {
		t.Fatalf("克隆禁止消息不一致：%s", forbidden.Error())
	}
	if (&cloneInteractionConflictError{}).Error() != "账户配置已发生变化，请重试" {
		t.Fatal("克隆冲突消息不一致")
	}
}

func TestW10ACloneCredentialProjection(t *testing.T) {
	credentials := Credentials{
		"api_keys":                  []any{"sk-1", "  ", "sk-2"},
		"api_key_strategy":          "weighted_round_robin",
		"api_key_weights":           []any{float64(30), float64(0), float64(500), "x"},
		"base_url":                  "https://api.example.com",
		"supported_endpoint_modes":  []any{"chat_completions", "responses"},
		"client_id":                 "client-1",
		"quota_project_id":          "proj-q",
		"oauth_type":                "code_assist",
		"tier_id":                   "tier-1",
		"project_id":                "proj-1",
		"service_tier_override":     "flex",
		"reasoning_effort_override": "high",
		"error_handling_rules":      []any{map[string]any{"match": "429"}},
		"response_inspection_rules": []any{map[string]any{"code": "5xx"}},
		"quota_recovery_policy":     map[string]any{"enabled": true},
	}
	options := projectCloneCredentialOptions(credentials)
	if options.APIKeyCount == nil || *options.APIKeyCount != 2 {
		t.Fatalf("API Key 计数应跳过空白：%v", options.APIKeyCount)
	}
	if options.APIKeyStrategy == nil || *options.APIKeyStrategy != "weighted_round_robin" {
		t.Fatalf("策略应保留：%v", options.APIKeyStrategy)
	}
	if len(options.APIKeyWeights) != 1 || options.APIKeyWeights[0] != 30 {
		t.Fatalf("非法权重应过滤：%v", options.APIKeyWeights)
	}
	if options.BaseURL == nil || *options.BaseURL != "https://api.example.com" {
		t.Fatalf("Base URL 应保留：%v", options.BaseURL)
	}
	if len(options.SupportedEndpointModes) != 2 {
		t.Fatalf("端点能力应保留：%v", options.SupportedEndpointModes)
	}
	if options.ClientID == nil || options.QuotaProjectID == nil || options.OAuthType == nil ||
		options.TierID == nil || options.ProjectID == nil || options.ServiceTierOverride == nil ||
		options.ReasoningEffortOverride == nil {
		t.Fatalf("可选投影应齐全：%+v", options)
	}
	if len(options.ErrorHandlingRules) != 1 || len(options.ResponseInspectionRules) != 1 {
		t.Fatalf("规则应保留：%v %v", options.ErrorHandlingRules, options.ResponseInspectionRules)
	}
	if options.QuotaRecoveryPolicy == nil {
		t.Fatal("额度恢复策略应保留")
	}
	// 未支持策略与未知 oauth_type 不投影。
	minimal := projectCloneCredentialOptions(Credentials{
		"api_key_strategy": "bogus", "oauth_type": "bogus",
	})
	if minimal.APIKeyStrategy != nil || minimal.OAuthType != nil {
		t.Fatalf("未知枚举不应投影：%v %v", minimal.APIKeyStrategy, minimal.OAuthType)
	}
	if minimal.APIKeyCount != nil {
		t.Fatalf("无 Key 记录计数应为 nil：%v", minimal.APIKeyCount)
	}

	// cloneCredentialAPIKeyCount 全分支。
	if count := cloneCredentialAPIKeyCount(Credentials{"api_keys": []any{strings.Repeat("k", 2)}}); count != 1 {
		t.Fatalf("api_keys 计数错误：%d", count)
	}
	many := []any{}
	for i := 0; i < 60; i++ {
		many = append(many, "sk-"+strings.Repeat("x", 1))
	}
	if count := cloneCredentialAPIKeyCount(Credentials{"api_keys": many}); count != 50 {
		t.Fatalf("计数应封顶 50：%d", count)
	}
	if count := cloneCredentialAPIKeyCount(Credentials{"api_key": "sk-single"}); count != 1 {
		t.Fatalf("单 Key 计数应为 1：%d", count)
	}
	if count := cloneCredentialAPIKeyCount(Credentials{}); count != 0 {
		t.Fatalf("空凭据计数应为 0：%d", count)
	}

	// parseCloneBalanceConfig 全分支。
	if parseCloneBalanceConfig("") != nil || parseCloneBalanceConfig("not-json") != nil ||
		parseCloneBalanceConfig("[1]") != nil || parseCloneBalanceConfig("{}") != nil {
		t.Fatal("非法/空余额配置应返回 nil")
	}
	config := parseCloneBalanceConfig(`{"type":"new_api"}`)
	if config == nil || config["type"] != "new_api" {
		t.Fatalf("合法余额配置应透传：%v", config)
	}
}

func TestW10ABatchLoadMappingsAndOverrides(t *testing.T) {
	env := newTestEnv(t)
	now := "2026-09-16T00:00:00.000Z"
	env.exec(t, `INSERT INTO account_model_mappings (account_id, provider_code, source_model, source_endpoint_family,
		upstream_model, upstream_endpoint_family, enabled, created_at, updated_at)
		VALUES ('acc-w10a-map', 'gpt', 'gpt-4o', 'chat_completions', 'gpt-4o-up', 'chat_completions', 1, ?, ?),
		       ('acc-w10a-map', 'gpt', 'gpt-4o-mini', 'chat_completions', 'gpt-4o-mini-up', 'responses', 0, ?, ?)`,
		now, now, now, now)

	empty, err := env.store.loadBatchModelMappings(context.Background(), env.db, []string{})
	if err != nil || len(empty) != 0 {
		t.Fatalf("空 ID 集应返回空 map：%v %v", empty, err)
	}
	loaded, err := env.store.loadBatchModelMappings(context.Background(), env.db, []string{"acc-w10a-map"})
	if err != nil {
		t.Fatalf("映射加载失败：%v", err)
	}
	mappings := loaded["acc-w10a-map"]
	if len(mappings) != 2 {
		t.Fatalf("应加载 2 条映射：%v", loaded)
	}
	if mappings[0].SourceModel != "gpt-4o" || mappings[0].Enabled == nil || !*mappings[0].Enabled {
		t.Fatalf("第一条映射不一致：%+v", mappings[0])
	}
	if mappings[1].UpstreamEndpointFamily != "responses" || mappings[1].Enabled == nil || *mappings[1].Enabled {
		t.Fatalf("第二条映射不一致：%+v", mappings[1])
	}

	// loadTestAccountModelMappings：空模型加载全部，指定模型只匹配 source。
	testLoaded, err := env.store.loadTestAccountModelMappings(context.Background(), env.db, "acc-w10a-map", "")
	if err != nil || len(testLoaded) != 2 {
		t.Fatalf("测试映射全量加载失败：%v %v", testLoaded, err)
	}
	filtered, err := env.store.loadTestAccountModelMappings(context.Background(), env.db, "acc-w10a-map", "gpt-4o")
	if err != nil || len(filtered) != 1 || filtered[0].SourceModel != "gpt-4o" {
		t.Fatalf("测试映射过滤加载失败：%v %v", filtered, err)
	}

	// applyNullableCredentialOverride 全分支。
	credentials := Credentials{"api_key": "sk-keep", "base_url": "https://old"}
	applyNullableCredentialOverride(credentials, map[string]BatchUpdateField{}, "baseUrl", "base_url")
	if credentials["base_url"] != "https://old" {
		t.Fatal("缺失更新不应修改")
	}
	applyNullableCredentialOverride(credentials, map[string]BatchUpdateField{
		"baseUrl": {Enabled: false, Value: "https://new"},
	}, "baseUrl", "base_url")
	if credentials["base_url"] != "https://old" {
		t.Fatal("未启用更新不应修改")
	}
	applyNullableCredentialOverride(credentials, map[string]BatchUpdateField{
		"baseUrl": {Enabled: true, Value: nil},
	}, "baseUrl", "base_url")
	if _, ok := credentials["base_url"]; ok {
		t.Fatal("null 值应删除键")
	}
	applyNullableCredentialOverride(credentials, map[string]BatchUpdateField{
		"baseUrl": {Enabled: true, Value: ""},
	}, "baseUrl", "base_url")
	applyNullableCredentialOverride(credentials, map[string]BatchUpdateField{
		"apiKey": {Enabled: true, Value: "sk-next"},
	}, "apiKey", "api_key")
	if credentials["api_key"] != "sk-next" {
		t.Fatalf("合法值应拷贝：%v", credentials["api_key"])
	}

	// jsonValueDeepEqual 分支。
	if !jsonValueDeepEqual(map[string]any{"a": 1.0}, map[string]any{"a": 1}) {
		t.Fatal("JSON 归一化后应相等")
	}
	if jsonValueDeepEqual(map[string]any{"a": 1}, map[string]any{"a": 2}) {
		t.Fatal("不同值不应相等")
	}
	if jsonValueDeepEqual(make(chan int), 1) {
		t.Fatal("不可序列化值应返回 false")
	}
}

func TestW10ATestSessionCancelReasonAndTask(t *testing.T) {
	canceled := &testSessionRow{Status: TestSessionCanceled, CancelReason: sql.NullString{String: "用户停止", Valid: true}}
	if testSessionCancelReason(canceled) != "用户停止" {
		t.Fatal("取消原因应透传")
	}
	canceledNoReason := &testSessionRow{Status: TestSessionCanceled}
	if testSessionCancelReason(canceledNoReason) != "已停止测试" {
		t.Fatalf("取消默认原因不一致：%q", testSessionCancelReason(canceledNoReason))
	}
	expired := &testSessionRow{Status: TestSessionExpired, CancelReason: sql.NullString{String: "过期详情", Valid: true}}
	if testSessionCancelReason(expired) != "过期详情" {
		t.Fatal("过期原因应透传")
	}
	expiredNoReason := &testSessionRow{Status: TestSessionExpired}
	if testSessionCancelReason(expiredNoReason) != "账户测试会话已过期" {
		t.Fatalf("过期默认原因不一致：%q", testSessionCancelReason(expiredNoReason))
	}
	completed := &testSessionRow{Status: TestSessionCompleted}
	if testSessionCancelReason(completed) != "账户测试会话已结束" {
		t.Fatalf("结束默认原因不一致：%q", testSessionCancelReason(completed))
	}
	running := &testSessionRow{Status: TestSessionRunning}
	if testSessionCancelReason(running) != "" {
		t.Fatalf("运行中不应有取消原因：%q", testSessionCancelReason(running))
	}

	// toTask：状态消息优先、错误消息兜底、结果 JSON、queued 截止回退。
	row := &testTaskRow{
		ID: "task-1", SessionID: sql.NullString{String: "sess-1", Valid: true},
		AccountID: "acc-1", AccountName: "账户", ProviderCode: "gpt",
		ProviderProfileID: "prof-1", ProtocolCode: "openai", ProtocolVersion: "v1",
		AccountType: "api_key", Status: TestTaskSuccess,
		Model:            sql.NullString{String: "gpt-4o-mini", Valid: true},
		TestEndpointMode: sql.NullString{String: "chat_json", Valid: true},
		CreatedAt:        "2026-09-16T00:00:00.000Z", QueuedAt: "2026-09-16T00:00:00.000Z",
		UpdatedAt:     "2026-09-16T00:00:01.000Z",
		StatusMessage: sql.NullString{String: "成功", Valid: true},
		ErrorMessage:  sql.NullString{String: "错误", Valid: true},
		ResultJSON:    sql.NullString{String: `{"accountId":"acc-1","message":"成功"}`, Valid: true},
	}
	task := row.ToTask()
	if task.ID != "task-1" || task.Status != TestTaskSuccess || task.Message == nil || *task.Message != "成功" {
		t.Fatalf("任务投影不一致：%+v", task)
	}
	if string(task.Result) != `{"accountId":"acc-1","message":"成功"}` {
		t.Fatalf("结果应透传：%s", string(task.Result))
	}
	if task.QueuedDeadlineAt == "" {
		t.Fatal("queued 截止不应为空")
	}
	errorOnly := &testTaskRow{
		Status: TestTaskFailed, QueuedAt: "2026-09-16T00:00:00.000Z",
		ErrorMessage: sql.NullString{String: "失败详情", Valid: true},
	}
	if errorOnly.ToTask().Message == nil || *errorOnly.ToTask().Message != "失败详情" {
		t.Fatal("错误消息应兜底")
	}
	invalidResult := &testTaskRow{
		Status: TestTaskFailed, QueuedAt: "bad-time",
		ResultJSON: sql.NullString{String: "not-json", Valid: true},
	}
	broken := invalidResult.ToTask()
	if broken.Message != nil {
		t.Fatalf("无消息不应设置 Message：%v", broken.Message)
	}
	if broken.QueuedDeadlineAt != "bad-time" {
		t.Fatalf("非法 queuedAt 应原样透传：%q", broken.QueuedDeadlineAt)
	}
}

func TestW10ARevalidateIneligibleMessage(t *testing.T) {
	cases := map[string]string{
		"account_not_active":       "账户当前未启用，不能重新验证 Key 池",
		"account_unschedulable":    "账户当前不可调度，不能重新验证 Key 池",
		"config_revision_conflict": "账户配置已被其他操作更新，请刷新后重试",
		"account_not_found":        "账户不存在或已删除",
		"no_revalidatable_key":     "当前账户没有可重新验证的不可用 Key",
		"bogus":                    "当前账户不是启用中的多 Key API Key 池",
	}
	for reason, want := range cases {
		if got := revalidateIneligibleMessage(reason); got != want {
			t.Fatalf("reason %s：got %q want %q", reason, got, want)
		}
	}
}

func TestW10AOptionalRuleErrorCodeList(t *testing.T) {
	if value, err := optionalRuleErrorCodeList([]any{"429", "insufficient_quota"}, "错误码"); err != nil || len(value) != 2 {
		t.Fatalf("合法错误码应通过：%v %v", value, err)
	}
	if _, err := optionalRuleErrorCodeList([]any{"200"}, "错误码"); err == nil {
		t.Fatal("2xx 成功码应拒绝")
	}
	if _, err := optionalRuleErrorCodeList([]any{"299"}, "错误码"); err == nil {
		t.Fatal("2xx 上界应拒绝")
	}
	if _, err := optionalRuleErrorCodeList([]any{123}, "错误码"); err == nil {
		t.Fatal("非字符串应拒绝")
	}
	if value, err := optionalRuleErrorCodeList(nil, "错误码"); err != nil || value != nil {
		t.Fatalf("nil 输入应返回 nil：%v %v", value, err)
	}
}

func TestW10AStoreBindAndTable(t *testing.T) {
	env := newTestEnv(t)
	pgStore, err := NewStore(env.db, true, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pgStore.table("accounts") != "juhe_business.accounts" {
		t.Fatalf("PG 表前缀不一致：%s", pgStore.table("accounts"))
	}
	bound := pgStore.bind("SELECT * FROM t WHERE a = ? AND b = ? AND c = ?")
	if bound != "SELECT * FROM t WHERE a = $1 AND b = $2 AND c = $3" {
		t.Fatalf("PG 占位符改写不一致：%s", bound)
	}
	if env.store.bind("SELECT ?") != "SELECT ?" {
		t.Fatal("SQLite 不应改写占位符")
	}
	if env.store.table("accounts") != "accounts" {
		t.Fatal("SQLite 无表前缀")
	}
}

func TestW10AUpstreamOriginAllowlist(t *testing.T) {
	parsed, err := url.Parse("https://127.0.0.1:8443/v1")
	if err != nil {
		t.Fatal(err)
	}
	if upstreamOriginAllowlisted(parsed, upstreamURLSecurity{}) {
		t.Fatal("空 allowlist 应拒绝")
	}
	config := upstreamURLSecurity{privateBaseUrlAllowlist: []string{"https://127.0.0.1:8443"}}
	if !upstreamOriginAllowlisted(parsed, config) {
		t.Fatal("命中的 allowlist 应放行")
	}
	other, _ := url.Parse("https://10.0.0.1:8443/v1")
	if upstreamOriginAllowlisted(other, config) {
		t.Fatal("未命中 allowlist 应拒绝")
	}
	// 默认端口省略归一化：http://localhost → http://localhost:80。
	localhost, _ := url.Parse("http://localhost/x")
	normalized, ok := normalizePrivateUpstreamOrigin("http://localhost")
	if !ok || normalized != "http://localhost:80" {
		t.Fatalf("默认端口应补全：%q %v", normalized, ok)
	}
	if _, ok := normalizePrivateUpstreamOrigin("ftp://localhost"); ok {
		t.Fatal("非 http(s) 协议应拒绝")
	}
	if _, ok := normalizePrivateUpstreamOrigin(""); ok {
		t.Fatal("空串应拒绝")
	}
	httpsDefault, ok := normalizePrivateUpstreamOrigin("https://Example.COM")
	if !ok || httpsDefault != "https://example.com:443" {
		t.Fatalf("https 默认端口与主机小写：%q %v", httpsDefault, ok)
	}
	_ = localhost
}
