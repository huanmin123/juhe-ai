package accounts

// w13a routes.go 未覆盖臂补齐（一）：body 解析纯函数全分支（createBody/
// patchBody/tagsPatchBody/lockBody）、查询参数解析（sorts/ids/status/limit/
// integer）、操作日志脱敏投影（safeChange/normalizeSafeValue）、mutation
// guard 指纹与 writeError 错误族映射。HTTP 处理器臂见 w13a_routes_handlers_test.go。

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestW13ACreateBodyValidationArms(t *testing.T) {
	valid := func(overrides map[string]any) map[string]any {
		body := map[string]any{
			"providerCode": "gpt", "providerProtocolProfileId": "prof-gpt",
			"name": "w13a", "type": "api_key",
		}
		for key, value := range overrides {
			if value == nil {
				delete(body, key)
				continue
			}
			body[key] = value
		}
		return body
	}
	if _, message := createBody(valid(nil)); message != "" {
		t.Fatalf("最小合法体应通过：%s", message)
	}
	cases := []struct {
		name      string
		overrides map[string]any
	}{
		{"缺失 providerCode", map[string]any{"providerCode": nil}},
		{"缺失 profile", map[string]any{"providerProtocolProfileId": nil}},
		{"缺失名称", map[string]any{"name": nil}},
		{"缺失类型", map[string]any{"type": nil}},
		{"未知键", map[string]any{"bogus": 1}},
		{"supportedModels 非数组", map[string]any{"supportedModels": "x"}},
		{"supportedModels 空数组", map[string]any{"supportedModels": []any{}}},
		{"healthCheckModel 非字符串", map[string]any{"healthCheckModel": 3}},
		{"healthCheckEndpointMode 非字符串", map[string]any{"healthCheckEndpointMode": 3}},
		{"modelMappings 非数组", map[string]any{"modelMappings": "x"}},
		{"modelMappings 项非对象", map[string]any{"modelMappings": []any{"x"}}},
		{"tags 非数组", map[string]any{"tags": "x"}},
		{"concurrencyLimit 非数字", map[string]any{"concurrencyLimit": "x"}},
		{"priority 非数字", map[string]any{"priority": "x"}},
		{"superPriorityEnabled 非布尔", map[string]any{"superPriorityEnabled": "x"}},
		{"fallbackEnabled 非布尔", map[string]any{"fallbackEnabled": "x"}},
		{"proxyProfileId 非字符串", map[string]any{"proxyProfileId": 3}},
		{"groupId 非字符串", map[string]any{"groupId": 3}},
		{"accountExpiresAt 非字符串", map[string]any{"accountExpiresAt": 3}},
		{"balanceQueryEnabled 非布尔", map[string]any{"balanceQueryEnabled": "x"}},
		{"notes 非字符串", map[string]any{"notes": 3}},
	}
	for _, testCase := range cases {
		if _, message := createBody(valid(testCase.overrides)); message == "" {
			t.Fatalf("%s 应拒绝", testCase.name)
		}
	}
	// balanceQueryConfig 显式 null（nil 值不会被 valid 辅助删除的路径）。
	nullConfig := valid(nil)
	nullConfig["balanceQueryConfig"] = nil
	if _, message := createBody(nullConfig); message == "" {
		t.Fatal("balanceQueryConfig null 应拒绝")
	}
	// modelMappings 项未知键。
	if _, message := createBody(valid(map[string]any{"modelMappings": []any{map[string]any{"bogus": 1}}})); message == "" {
		t.Fatal("modelMappings 未知键应拒绝")
	}
	// modelMappings 超过 500 条。
	many := []any{}
	for i := 0; i < 501; i++ {
		many = append(many, map[string]any{"sourceModel": "m", "sourceEndpointFamily": "chat_completions",
			"upstreamModel": "u", "upstreamEndpointFamily": "chat_completions", "enabled": true})
	}
	if _, message := createBody(valid(map[string]any{"modelMappings": many})); message == "" {
		t.Fatal("modelMappings 超 500 应拒绝")
	}
	// 余额：非法配置、开启缺配置、开启合法配置。
	if _, message := createBody(valid(map[string]any{"balanceQueryConfig": map[string]any{"bogus": 1}})); message == "" {
		t.Fatal("非法余额配置应拒绝")
	}
	if _, message := createBody(valid(map[string]any{"balanceQueryEnabled": true})); message == "" {
		t.Fatal("开启余额缺配置应拒绝")
	}
	// 余额：开启 + 合法配置 + 有效凭据（ValidateAccountBalanceCapability 需要 Key）。
	withKeys := valid(map[string]any{"credentials": map[string]any{"api_key": "sk-live-w13a"}})
	withKeys["balanceQueryEnabled"] = true
	withKeys["balanceQueryConfig"] = map[string]any{"adapter": "builtin", "preferredBuiltinAdapter": "newapi"}
	if _, message := createBody(withKeys); message != "" {
		t.Fatalf("合法余额配置应通过：%s", message)
	}
	// temporaryUnavailableContinuousProbeEnabled 非布尔。
	if _, message := createBody(valid(map[string]any{"temporaryUnavailableContinuousProbeEnabled": "x"})); message == "" {
		t.Fatal("探活开关非布尔应拒绝")
	}
	// availabilitySchedule 透传。
	input, message := createBody(valid(map[string]any{"availabilitySchedule": map[string]any{"bogus": 1}}))
	if message != "" || input.AvailabilitySchedule == nil {
		t.Fatalf("时间计划应透传给 store 校验：%v %s", input.AvailabilitySchedule, message)
	}
	// credentials 缺失时保持 nil。
	if input, message = createBody(valid(nil)); input.Credentials != nil || message != "" {
		t.Fatalf("无凭据应保持 nil：%v %s", input.Credentials, message)
	}
}

func TestW13APatchBodyValidationArms(t *testing.T) {
	base := map[string]any{"expectedConfigRevision": float64(1)}
	if _, message := patchBody(base); message != "" {
		t.Fatalf("最小合法体应通过：%s", message)
	}
	cases := []struct {
		name string
		body map[string]any
	}{
		{"未知键", map[string]any{"expectedConfigRevision": float64(1), "bogus": 1}},
		{"版本缺失", map[string]any{}},
		{"版本为零", map[string]any{"expectedConfigRevision": float64(0)}},
		{"版本非整数", map[string]any{"expectedConfigRevision": 1.5}},
		{"版本非数字", map[string]any{"expectedConfigRevision": "1"}},
		{"name 非字符串", map[string]any{"expectedConfigRevision": float64(1), "name": 3}},
		{"notes 非字符串", map[string]any{"expectedConfigRevision": float64(1), "notes": 3}},
		{"status 非字符串", map[string]any{"expectedConfigRevision": float64(1), "status": 3}},
		{"concurrencyLimit 非数字", map[string]any{"expectedConfigRevision": float64(1), "concurrencyLimit": "x"}},
		{"priority 非数字", map[string]any{"expectedConfigRevision": float64(1), "priority": "x"}},
		{"superPriorityEnabled 非布尔", map[string]any{"expectedConfigRevision": float64(1), "superPriorityEnabled": "x"}},
		{"fallbackEnabled 非布尔", map[string]any{"expectedConfigRevision": float64(1), "fallbackEnabled": "x"}},
		{"schedulable 非布尔", map[string]any{"expectedConfigRevision": float64(1), "schedulable": "x"}},
		{"credentials 非对象", map[string]any{"expectedConfigRevision": float64(1), "credentials": "x"}},
		{"credentials 与 patch 冲突", map[string]any{"expectedConfigRevision": float64(1),
			"credentials": map[string]any{}, "credentialsPatch": map[string]any{}}},
		{"credentialsPatch 非对象", map[string]any{"expectedConfigRevision": float64(1), "credentialsPatch": "x"}},
		{"supportedModels 非数组", map[string]any{"expectedConfigRevision": float64(1), "supportedModels": "x"}},
		{"supportedModels 空数组", map[string]any{"expectedConfigRevision": float64(1), "supportedModels": []any{}}},
		{"healthCheckModel 非字符串", map[string]any{"expectedConfigRevision": float64(1), "healthCheckModel": 3}},
		{"healthCheckEndpointMode 非字符串", map[string]any{"expectedConfigRevision": float64(1), "healthCheckEndpointMode": 3}},
		{"tags 非数组", map[string]any{"expectedConfigRevision": float64(1), "tags": "x"}},
		{"accountExpiresAt 非字符串", map[string]any{"expectedConfigRevision": float64(1), "accountExpiresAt": 3}},
		{"modelMappings 非数组", map[string]any{"expectedConfigRevision": float64(1), "modelMappings": "x"}},
		{"modelMappings 项非对象", map[string]any{"expectedConfigRevision": float64(1), "modelMappings": []any{"x"}}},
		{"modelMappings 未知键", map[string]any{"expectedConfigRevision": float64(1),
			"modelMappings": []any{map[string]any{"bogus": 1}}}},
		{"proxyProfileId 非字符串", map[string]any{"expectedConfigRevision": float64(1), "proxyProfileId": 3}},
		{"groupId null", map[string]any{"expectedConfigRevision": float64(1), "groupId": nil}},
		{"groupId 非字符串", map[string]any{"expectedConfigRevision": float64(1), "groupId": 3}},
		{"balanceQueryEnabled 非布尔", map[string]any{"expectedConfigRevision": float64(1), "balanceQueryEnabled": "x"}},
		{"balanceQueryConfig null", map[string]any{"expectedConfigRevision": float64(1), "balanceQueryConfig": nil}},
		{"balanceQueryConfig 非法", map[string]any{"expectedConfigRevision": float64(1),
			"balanceQueryConfig": map[string]any{"bogus": 1}}},
		{"temporaryProbe 非布尔", map[string]any{"expectedConfigRevision": float64(1),
			"temporaryUnavailableContinuousProbeEnabled": "x"}},
	}
	for _, testCase := range cases {
		if _, message := patchBody(testCase.body); message == "" {
			t.Fatalf("%s 应拒绝", testCase.name)
		}
	}
	// 通过臂：合法字段解析进 input。
	input, message := patchBody(map[string]any{
		"expectedConfigRevision": float64(2), "name": "n", "notes": " ", "status": "active",
		"credentialsPatch": map[string]any{"api_key": "sk"}, "availabilitySchedule": map[string]any{},
		"accountExpiresAt": nil, "proxyProfileId": nil, "clearFailureState": true,
	})
	if message != "" || input.ExpectedConfigRevision != 2 || !input.CredentialsPatch ||
		!input.AvailabilitySchedulePresent || !input.AccountExpiresAtPresent ||
		input.AccountExpiresAt != nil || input.ProxyProfileID != nil || !input.ProxyProfileIDPresent ||
		!input.ClearFailureState {
		t.Fatalf("合法 patch 体解析不一致：%+v %s", input, message)
	}
}

func TestW13ATagsAndLockBodyArms(t *testing.T) {
	if _, message := tagsPatchBody(map[string]any{"expectedConfigRevision": float64(1), "tags": []any{}}); message != "" {
		t.Fatalf("空标签合法：%s", message)
	}
	if _, message := tagsPatchBody(map[string]any{"bogus": 1}); message == "" {
		t.Fatal("未知键应拒绝")
	}
	if _, message := tagsPatchBody(map[string]any{"expectedConfigRevision": float64(0), "tags": []any{}}); message == "" {
		t.Fatal("版本无效应拒绝")
	}
	if _, message := tagsPatchBody(map[string]any{"expectedConfigRevision": float64(1), "tags": "x"}); message == "" {
		t.Fatal("tags 非数组应拒绝")
	}
	manyTags := []any{}
	for i := 0; i < maxTagsPerAccount+1; i++ {
		manyTags = append(manyTags, "t")
	}
	if _, message := tagsPatchBody(map[string]any{"expectedConfigRevision": float64(1), "tags": manyTags}); message == "" {
		t.Fatal("超过 24 个标签应拒绝")
	}

	if _, message := lockBody(map[string]any{"expectedConfigRevision": float64(1)}); message != "" {
		t.Fatalf("最小锁死体应通过：%s", message)
	}
	if _, message := lockBody(map[string]any{"bogus": 1}); message == "" {
		t.Fatal("未知键应拒绝")
	}
	if _, message := lockBody(map[string]any{"expectedConfigRevision": "x"}); message == "" {
		t.Fatal("版本无效应拒绝")
	}
	if _, message := lockBody(map[string]any{"expectedConfigRevision": float64(1), "lockDeathTimeoutSeconds": "x"}); message == "" {
		t.Fatal("死亡窗口非数字应拒绝")
	}
	if _, message := lockBody(map[string]any{"expectedConfigRevision": float64(1), "lockRetryIntervalSeconds": 1.5}); message == "" {
		t.Fatal("重试间隔非整数应拒绝")
	}
}

func TestW13AQueryValueHelpers(t *testing.T) {
	// parseSortQuery：合法、逗号分隔、空字段段保留、非法段数与顺序丢弃。
	sorts := parseSortQuery([]string{"name:asc, :desc", "priority:desc", "bogus", "a:b:c", "x:up"})
	if len(sorts) != 3 || sorts[0].Field != "name" || sorts[0].Order != "asc" ||
		sorts[1].Field != "" || sorts[1].Order != "desc" || sorts[2].Field != "priority" {
		t.Fatalf("sorts 解析不一致：%+v", sorts)
	}
	// textListQuery：去空白、丢弃空段。
	if got := textListQuery([]string{" a , ,b ", ","}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("textListQuery 不一致：%v", got)
	}
	// statusQueryValue：逗号合并、all 丢弃、未知值原样保留（store 层再过滤）。
	if got := statusQueryValue("active, all, bogus, disabled"); got != "active,bogus,disabled" {
		t.Fatalf("status 过滤不一致：%q", got)
	}
	if got := statusQueryValue("all"); got != "" {
		t.Fatalf("纯 all 应为空：%q", got)
	}
	// schedulableQueryValue：合法四值与未知值。
	for _, value := range []string{"all", "enabled", "disabled", "cooling"} {
		if schedulableQueryValue(value) != value {
			t.Fatalf("schedulable %s 应透传", value)
		}
	}
	if schedulableQueryValue(" bogus ") != "" {
		t.Fatal("未知 schedulable 应为空")
	}
	// optionLimitValue：空、非法、下界、上界。
	if optionLimitValue("") != maxAccountOptionPageSize || optionLimitValue("abc") != maxAccountOptionPageSize {
		t.Fatal("limit 空值/非法应回退默认")
	}
	if optionLimitValue("0") != 1 || optionLimitValue("999") != maxAccountOptionPageSize {
		t.Fatal("limit 应夹在 1..50")
	}
	// integerQueryValue：空/非法为 0，负数合法。
	if integerQueryValue("") != 0 || integerQueryValue("x") != 0 {
		t.Fatal("integer 空值/非法应为 0")
	}
	if integerQueryValue("-3") != -3 || integerQueryValue(" 7 ") != 7 {
		t.Fatal("integer 负数/空白不一致")
	}
	// parseInteger：负数、非数字。
	if value, err := parseInteger("-12"); err != nil || value != -12 {
		t.Fatalf("负数应解析：%d %v", value, err)
	}
	if _, err := parseInteger("1x"); err == nil {
		t.Fatal("非数字应报错")
	}
}

func TestW13ASafeChangeAndNormalizeArms(t *testing.T) {
	// 敏感字段：只承载标记不承载材料。
	change := safeChange("credentials", "凭据", "sk-old", "sk-new")
	if !change.Sensitive || change.Before != "已设置" || change.After != "已变更" {
		t.Fatalf("敏感变更应脱敏：%+v", change)
	}
	change = safeChange("token", "令牌", nil, "")
	if !change.Sensitive || change.Before != "未设置" || change.After != "未设置" {
		t.Fatalf("空敏感变更应未设置：%+v", change)
	}
	// 非敏感字段：标量截断与序列化。
	if got := normalizeSafeValue(strings.Repeat("x", 201)); got != strings.Repeat("x", 200)+"..." {
		t.Fatalf("长字符串应截断：%d", len(got))
	}
	if normalizeSafeValue(true) != "true" || normalizeSafeValue(false) != "false" {
		t.Fatal("布尔应转文本")
	}
	if normalizeSafeValue(3) != "3" || normalizeSafeValue(int64(4)) != "4" || normalizeSafeValue(1.5) != "1.5" {
		t.Fatal("数值应转文本")
	}
	if normalizeSafeValue(nil) != "" {
		t.Fatal("nil 应为空")
	}
	if got := normalizeSafeValue(map[string]any{"a": 1}); got != `{"a":1}` {
		t.Fatalf("对象应序列化：%q", got)
	}
	if normalizeSafeValue(make(chan int)) != "" {
		t.Fatal("不可序列化应为空")
	}
	// safeCredentialsChange：nil 值 After=未设置。
	if change = safeCredentialsChange(nil); change.After != "未设置" {
		t.Fatalf("nil 凭据应未设置：%+v", change)
	}
	if change = safeCredentialsChange(map[string]any{"api_key": "sk"}); !change.Sensitive || change.After != "已变更" {
		t.Fatalf("凭据变更应脱敏：%+v", change)
	}
}

func TestW13AGuardFingerprintArms(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/x?systemAccountId=owner-1", nil)
	fingerprint := guardFingerprint(request, "accounts.create")
	if fingerprint["owner"] != "owner-1" || fingerprint["credential"] != "" {
		t.Fatalf("create 指纹应含 owner 与空凭据：%v", fingerprint)
	}
	request = httptest.NewRequest(http.MethodPost, "/x", nil)
	request.Header.Set("Content-Type", "application/json")
	request.Body = http.NoBody
	// kernel.BodyField 依赖已解析的请求体上下文，缺省为 nil → 各文本字段为空。
	if fingerprint = guardFingerprint(request, "accounts.lock"); fingerprint["accountId"] != "" {
		t.Fatalf("lock 指纹应含账户占位：%v", fingerprint)
	}
	// credentialGuardFingerprint：api_keys 池、单 api_key、身份字段回退、空记录。
	pool := credentialGuardFingerprint(map[string]any{"api_keys": []any{" sk-a ", 3, ""}})
	if pool == "" {
		t.Fatal("api_keys 池应产出指纹")
	}
	single := credentialGuardFingerprint(map[string]any{"api_key": "sk-b"})
	if single == "" || single == pool {
		t.Fatal("单 Key 应产出不同指纹")
	}
	identity := credentialGuardFingerprint(map[string]any{"identity_token": "tok-1"})
	if identity == "" {
		t.Fatal("身份回退应产出指纹")
	}
	if credentialGuardFingerprint(map[string]any{"api_keys": []any{}}) != "" {
		t.Fatal("空池应无指纹")
	}
	if credentialGuardFingerprint("not-a-record") != "" {
		t.Fatal("非对象应无指纹")
	}
	// firstNonEmptyText：跳过非字符串与空白。
	if got := firstNonEmptyText(map[string]any{"a": 1, "b": "  ", "c": "v"}, "a", "b", "c"); got != "v" {
		t.Fatalf("firstNonEmptyText 应跳过空白：%q", got)
	}
	if got := firstNonEmptyText(map[string]any{}, "a"); got != "" {
		t.Fatalf("全缺失应为空：%q", got)
	}
}

func TestW13AWriteErrorMapping(t *testing.T) {
	deps := &Deps{}
	render := func(err error) (int, string) {
		recorder := httptest.NewRecorder()
		deps.writeError(recorder, err)
		var payload struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
		return recorder.Code, payload.Message
	}
	if code, message := render(&batchVersionConflictError{AccountID: "acc-1"}); code != http.StatusConflict || message == "" {
		t.Fatalf("batch 版本冲突应 409：%d %q", code, message)
	}
	if code, _ := render(&batchAccessError{Message: batchSameScopeMessage}); code != http.StatusBadRequest {
		t.Fatalf("同作用域 batch 访问错误应 400：%d", code)
	}
	if code, _ := render(&batchAccessError{Message: "x"}); code != http.StatusNotFound {
		t.Fatalf("跨作用域 batch 访问错误应 404：%d", code)
	}
	if code, _ := render(&ConflictError{Message: "冲突"}); code != http.StatusConflict {
		t.Fatalf("ConflictError 应 409：%d", code)
	}
	if code, _ := render(&RevisionConflictError{Message: RevisionConflictMessage}); code != http.StatusConflict {
		t.Fatalf("版本冲突应 409：%d", code)
	}
	if code, _ := render(&RevisionConflictError{Message: lockNotFoundMessage}); code != http.StatusNotFound {
		t.Fatalf("锁死缺失应 404：%d", code)
	}
	if code, _ := render(&ValidationError{Message: "无效"}); code != http.StatusBadRequest {
		t.Fatalf("ValidationError 应 400：%d", code)
	}
	if code, _ := render(&editBasicForbiddenError{}); code != http.StatusForbidden {
		t.Fatalf("编辑禁止应 403：%d", code)
	}
	if code, _ := render(&TagInUseError{}); code != http.StatusBadRequest {
		t.Fatalf("标签占用应 400：%d", code)
	}
	if code, _ := render(errors.New("boom")); code != http.StatusInternalServerError {
		t.Fatalf("未知错误应 500：%d", code)
	}
}
