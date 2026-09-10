// 处理器残余分支补测：四个资源族 update/del 的停用属主拒绝、账号更新的
// type/状态/时间计划分支、供应商档案容错、分页缺省分支与 capture writer
// 预算截断。目标是用最小用例吃掉分支覆盖长尾。
package aipublic

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// TestWCDisabledOwnerRejectedEverywhere：属主被停用后，四个资源族的
// update/del 全部拒绝（400 目标用户已停用）。
func TestWCDisabledOwnerRejectedEverywhere(t *testing.T) {
	env := newWCLifeEnv(t)
	groupID := wcAddGroup(t, env, "pusher", "停用组")
	strategyID := wcAddStrategy(t, env, "pusher", "停用策略", groupID)

	// 准备一把 key 和一个账号。
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/api-key/add",
		`{"targetUsername":"pusher","name":"停用Key","routeStrategyId":"`+strategyID+`"}`, wcLifeToken)
	if status != http.StatusCreated {
		t.Fatalf("建 key: %d %v", status, payload)
	}
	keyID := payload["data"].(map[string]any)["apiKey"].(map[string]any)["id"].(string)
	accountID := wcAddAccount(t, env, wcAccountBase)

	if _, err := env.db.Exec(`UPDATE system_accounts SET status = 'disabled' WHERE username = 'pusher'`); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"group update", http.MethodPost, Prefix + "/group/update", `{"groupId":"` + groupID + `","name":"x"}`},
		{"group del", http.MethodPost, Prefix + "/group/del", `{"groupId":"` + groupID + `"}`},
		{"strategy update", http.MethodPost, Prefix + "/route-strategy/update", `{"routeStrategyId":"` + strategyID + `","name":"x"}`},
		{"strategy del", http.MethodPost, Prefix + "/route-strategy/del", `{"routeStrategyId":"` + strategyID + `"}`},
		{"api-key update", http.MethodPost, Prefix + "/api-key/update", `{"apiKeyId":"` + keyID + `","name":"x"}`},
		{"api-key del", http.MethodPost, Prefix + "/api-key/del", `{"apiKeyId":"` + keyID + `"}`},
		{"account update", http.MethodPost, Prefix + "/account/update", `{"accountId":"` + accountID + `","name":"x"}`},
		{"account del", http.MethodPost, Prefix + "/account/del", `{"accountId":"` + accountID + `"}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			code, body, _ := env.doAuth(testCase.method, testCase.path, testCase.body, wcLifeToken)
			if code != http.StatusBadRequest || body["message"] != "目标用户已停用：pusher" {
				t.Fatalf("%s: %d %v", testCase.name, code, body)
			}
		})
	}
}

// TestWCGroupAddProviderPrecheck：group add 的供应商预检（停用/未知）。
func TestWCGroupAddProviderPrecheck(t *testing.T) {
	env := newWCLifeEnv(t)
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/group/add",
		`{"targetUsername":"pusher","name":"n","providerCode":"anthropic"}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "供应商已停用：anthropic" {
		t.Fatalf("停用供应商: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/group/add",
		`{"targetUsername":"pusher","name":"n","providerCode":"claude"}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "不支持的供应商：claude" {
		t.Fatalf("未知供应商: %d %v", status, payload)
	}
}

// TestWCAccountUpdateTypeAndSchedule：账号 update 的 type 显式合法、状态
// 回切、优先级与非法时间计划分支。
func TestWCAccountUpdateTypeAndSchedule(t *testing.T) {
	env := newWCLifeEnv(t)
	accountID := wcAddAccount(t, env, wcAccountBody(`"priority":5`))

	// 非法时间计划 → store 层 400（时间计划需要 enabled:true）。
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/account/update",
		`{"accountId":"`+accountID+`","availabilitySchedule":{"mode":"allow_windows"}}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "API Key 时间计划启用状态必须为 true" {
		t.Fatalf("非法时间计划: %d %v", status, payload)
	}

	// type 显式 api_key + 状态回切 + 优先级归零。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/update",
		`{"accountId":"`+accountID+`","type":"api_key","status":"active","priority":0}`, wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("回切: %d %v", status, payload)
	}
	account := payload["data"].(map[string]any)["account"].(map[string]any)
	// update 包络是账号摘要投影，不携带 priority/concurrencyLimit。
	if account["status"] != "active" || account["schedulable"] != true {
		t.Fatalf("回切投影: %v", account)
	}
}

// TestWCAccountAddProfileTypesBrokenJSON：档案 account_types_json 非法时
// 公开推送拒绝（档案不支持 api_key）。
func TestWCAccountAddProfileTypesBrokenJSON(t *testing.T) {
	env := newWCLifeEnv(t)
	wcSeedProfile(t, env, "profile_broken_types", "gpt", true, `{bad json`)
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/account/add",
		strings.Replace(wcAccountBase, `"providerProtocolProfileId":"profile_gpt_openai_v1"`, `"providerProtocolProfileId":"profile_broken_types"`, 1), wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "当前供应商不支持 API Key 账户" {
		t.Fatalf("档案类型损坏: %d %v", status, payload)
	}
	// profile 不属于该供应商。
	wcSeedProfile(t, env, "profile_other_provider", "anthropic", true, `["api_key"]`)
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/add",
		strings.Replace(wcAccountBase, `"providerProtocolProfileId":"profile_gpt_openai_v1"`, `"providerProtocolProfileId":"profile_other_provider"`, 1), wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "协议档案 profile_other_provider 不属于供应商 gpt" {
		t.Fatalf("档案跨供应商: %d %v", status, payload)
	}
	// 停用的档案。
	wcSeedProfile(t, env, "profile_disabled_p", "gpt", false, `["api_key"]`)
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/account/add",
		strings.Replace(wcAccountBase, `"providerProtocolProfileId":"profile_gpt_openai_v1"`, `"providerProtocolProfileId":"profile_disabled_p"`, 1), wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "供应商协议档案已停用：profile_disabled_p" {
		t.Fatalf("停用档案: %d %v", status, payload)
	}
}

// TestWCStoresFailClosed：DB 关闭后目标解析报错（错误分支覆盖）。
func TestWCStoresFailClosed(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedTargetUser("user_x", "xuser", "active")
	accountsStore, err := authsys.NewAccountStore(env.db, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := &Deps{DB: env.db, PGDialect: false, Now: time.Now, SystemAccounts: accountsStore}
	if err := env.db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := deps.requirePublicTarget(t.Context(), "xuser"); err == nil {
		t.Fatalf("DB 关闭后必须报错")
	}
	if _, err := deps.ensureTargetSystemAccount(t.Context(), "xuser", nil); err == nil {
		t.Fatalf("DB 关闭后必须报错")
	}
	if _, err := deps.findGroupOwnerByID(t.Context(), "g1"); err == nil {
		t.Fatalf("DB 关闭后必须报错")
	}
	if _, err := deps.loadProvider(t.Context(), "gpt"); err == nil {
		t.Fatalf("DB 关闭后必须报错")
	}
	if _, err := deps.listGroupRows(t.Context(), "owner", 1, 20, "", ""); err == nil {
		t.Fatalf("DB 关闭后必须报错")
	}
}

// TestWCPagingFallbacks：分页助手的越界/缺省混合分支。
func TestWCPagingFallbacks(t *testing.T) {
	deps := &Deps{}
	if page, size := deps.paging(true, 0, true, -1); page != 1 || size != 20 {
		t.Fatalf("paging 非法显式值回退: %d %d", page, size)
	}
	if page, size := deps.accountPaging(true, -2, false, 0); page != 1 || size != 50 {
		t.Fatalf("accountPaging: %d %d", page, size)
	}
	if page, size := deps.strategyPaging(false, 3, true, -5); page != 1 || size != 50 {
		t.Fatalf("strategyPaging: %d %d", page, size)
	}
	if page, size := deps.apiKeyPaging(true, 2, false, 0); page != 2 || size != 50 {
		t.Fatalf("apiKeyPaging: %d %d", page, size)
	}
}

// TestWCCaptureWriterPartialBudget：预算内截断分支（单次写入超出剩余预算）。
func TestWCCaptureWriterPartialBudget(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &captureResponseWriter{ResponseWriter: recorder, status: http.StatusOK}
	writer.payload = make([]byte, 0, captureSnapshotBudget+10)
	if _, err := writer.Write(make([]byte, captureSnapshotBudget-3)); err != nil {
		t.Fatalf("第一次写入: %v", err)
	}
	// 再写 10 字节：只有 3 字节进入快照，其余仅透传。
	if _, err := writer.Write(make([]byte, 10)); err != nil {
		t.Fatalf("第二次写入: %v", err)
	}
	if len(writer.payload) != captureSnapshotBudget {
		t.Fatalf("快照必须停在预算: %d", len(writer.payload))
	}
}

// TestWCCaptureRequestBodyNil：无 Body 对象的 POST 容错。
func TestWCCaptureRequestBodyNil(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Body = nil
	buffered := bufferCaptureRequestBody(request)
	if buffered == nil || buffered.parseFailed || buffered.raw != nil {
		t.Fatalf("nil Body 必须返回空缓存: %+v", buffered)
	}
}

// TestWCNullableTrimmedBodyFieldAbsent：缺省键直接通过。
func TestWCNullableTrimmedBodyFieldAbsent(t *testing.T) {
	if text, issue := nullableTrimmedBodyField(map[string]any{}, "k", 5); text != nil || issue != "" {
		t.Fatalf("缺省键: %v %q", text, issue)
	}
}

// TestWCListQueryLongValues：列表 query 的长度上限分支。
func TestWCListQueryLongValues(t *testing.T) {
	long120 := strings.Repeat("k", 121)
	if _, issue := parseApiKeyListQuery(mustParseQuery(t, "targetUsername=ab&keyword="+long120)); issue != zodStringMax(120) {
		t.Fatalf("api-key keyword 过长: %q", issue)
	}
	if _, issue := parseStrategyListQuery(mustParseQuery(t, "targetUsername=ab&status=bogus")); issue != zodEnumMessage(strategyStatusOptions, "bogus") {
		t.Fatalf("strategy status 非法: %q", issue)
	}
	if _, issue := parseAccountListQuery(mustParseQuery(t, "targetUsername=ab&type="+strings.Repeat("t", 61))); issue != zodStringMax(60) {
		t.Fatalf("type 过长: %q", issue)
	}
}
