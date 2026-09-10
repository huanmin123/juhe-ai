package main

// w1: 跨文件小函数打包测试——dispatch/accounts/error_policy/locks/compose/
// chat_images/prewarm/traffic migration/main 的纯函数与零值安全方法。

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

func TestW1MainEnvHelpers(t *testing.T) {
	t.Setenv("W1_TEST_ENV_VAR", "value")
	if envOrDefault("W1_TEST_ENV_VAR", "fallback") != "value" {
		t.Fatal("有值时必须返回环境值")
	}
	if envOrDefault("W1_TEST_ENV_VAR_MISSING", "fallback") != "fallback" {
		t.Fatal("缺省时必须返回 fallback")
	}
	if got := commaList(" a , b ,, c "); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("commaList = %v", got)
	}
	if got := commaList(""); len(got) != 0 {
		t.Fatalf("空 commaList = %v", got)
	}
	t.Setenv("W1_TEST_ENV_BOOL", "TRUE")
	if !envBool("W1_TEST_ENV_BOOL") {
		t.Fatal("TRUE 必须为真")
	}
	t.Setenv("W1_TEST_ENV_BOOL", "no")
	if envBool("W1_TEST_ENV_BOOL") {
		t.Fatal("no 必须为假")
	}
}

func TestW1LoadSessionRetentionConfig(t *testing.T) {
	values := map[string]string{}
	getenv := func(key string) string { return values[key] }
	interval, limit, err := loadSessionRetentionConfig(getenv)
	if err != nil || interval != 15*time.Minute || limit != 10000 {
		t.Fatalf("默认值 = %v %d %v", interval, limit, err)
	}
	values["JUHE_AI_SESSION_RETENTION_INTERVAL"] = "5m"
	values["JUHE_AI_SESSION_RETENTION_BATCH_SIZE"] = "42"
	interval, limit, err = loadSessionRetentionConfig(getenv)
	if err != nil || interval != 5*time.Minute || limit != 42 {
		t.Fatalf("自定义值 = %v %d %v", interval, limit, err)
	}
	values["JUHE_AI_SESSION_RETENTION_INTERVAL"] = "bogus"
	if _, _, err := loadSessionRetentionConfig(getenv); err == nil {
		t.Fatal("非法 interval 必须报错")
	}
	values["JUHE_AI_SESSION_RETENTION_INTERVAL"] = "5m"
	values["JUHE_AI_SESSION_RETENTION_BATCH_SIZE"] = "0"
	if _, _, err := loadSessionRetentionConfig(getenv); err == nil {
		t.Fatal("非正 batch 必须报错")
	}
	if _, _, err := loadSessionRetentionConfig(nil); err != nil {
		t.Fatalf("nil getter 回落 os.Getenv: %v", err)
	}
}

func TestW1PassiveGatewayHealthHandler(t *testing.T) {
	handler := passiveGatewayHealthHandler(ownermode.Mode{})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/health", nil))
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), `"ready":false`) {
		t.Fatalf("health = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/health", nil))
	if recorder.Code != 404 {
		t.Fatalf("POST health = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/other", nil))
	if recorder.Code != 404 {
		t.Fatalf("other path = %d", recorder.Code)
	}
}

func TestW1ListenLoopbackValidation(t *testing.T) {
	if _, err := listenLoopback("not-an-address"); err == nil {
		t.Fatal("非法地址必须报错")
	}
	if _, err := listenLoopback("localhost:notaport"); err == nil {
		t.Fatal("非法端口必须报错")
	}
	if _, err := listenLoopback("127.0.0.1:0"); err == nil {
		t.Fatal("端口 0 必须报错")
	}
	if _, err := listenLoopback("example.com:8080"); err == nil {
		t.Fatal("非环回 host 必须报错")
	}
	listener, err := listenLoopback("127.0.0.1:0")
	if err != nil {
		t.Fatalf("合法环回地址: %v", err)
	}
	_ = listener.Close()
}

func TestW1DispatchSmallHelpers(t *testing.T) {
	if envBoolOf(" TRUE ") != true || envBoolOf("off") != false {
		t.Fatal("envBoolOf 语义错误")
	}
	if nilStringPtr("  ") != nil {
		t.Fatal("空白 nilStringPtr 必须 nil")
	}
	value := "v"
	if nilStringPtr(value) == nil || *nilStringPtr(value) != "v" {
		t.Fatal("nilStringPtr 值错误")
	}
	if derefStringValue(nil) != "" {
		t.Fatal("nil derefStringValue 必须空")
	}
	int64Value := int64(9)
	if derefInt64Ptr2(nil) != 0 || derefInt64Ptr2(&int64Value) != 9 {
		t.Fatal("derefInt64Ptr2 错误")
	}
	effects := &chainResponseAccountEffects{}
	if err := effects.HandleStreamFailure(gatewayresponse.AccountView{}, "msg", "code", gatewayresponse.StreamFailureContext{}, true); err != nil {
		t.Fatalf("HandleStreamFailure 必须幂等 nil: %v", err)
	}
	if effects.DispatchRequestFailureAccountHealthCheck("gateway", "acc") {
		t.Fatal("响应层健康检查派发必须返回 false")
	}
	effects.ForgetSessionAffinity("session", "acc")
}

func TestW1AccountsSmallHelpers(t *testing.T) {
	if strPtr("x") == nil || *strPtr("x") != "x" {
		t.Fatal("strPtr 错误")
	}
	proxy := chainProxyUnavailable("不可用")
	if proxy.unavailable == nil || !*proxy.unavailable || proxy.errorMessage == nil || *proxy.errorMessage != "不可用" {
		t.Fatalf("chainProxyUnavailable = %+v", proxy)
	}
	if absInt64(-5) != 5 || absInt64(7) != 7 || absInt64(0) != 0 {
		t.Fatal("absInt64 错误")
	}
	token := chainLockRandomToken()
	if len(token) != 32 {
		t.Fatalf("随机 token 长度 = %d，want 32 hex", len(token))
	}
	if token == chainLockRandomToken() {
		t.Fatal("随机 token 必须变化")
	}
	if truncateUTF8("abcdef", 3) != "abc" || truncateUTF8("ab", 3) != "ab" {
		t.Fatal("truncateUTF8 错误")
	}
}

func TestW1ErrorPolicySmallHelpers(t *testing.T) {
	if _, err := readHour(float64(13), "小时"); err != nil {
		t.Fatalf("readHour 13: %v", err)
	}
	for _, bad := range []any{float64(24), float64(-1), float64(1.5), "x"} {
		if _, err := readHour(bad, "小时"); err == nil {
			t.Fatalf("readHour(%v) 必须报错", bad)
		}
	}
	if _, err := readWeekday(float64(6), "星期"); err != nil {
		t.Fatalf("readWeekday 6: %v", err)
	}
	for _, bad := range []any{float64(7), float64(-1), float64(0.5), 42} {
		if _, err := readWeekday(bad, "星期"); err == nil {
			t.Fatalf("readWeekday(%v) 必须报错", bad)
		}
	}
	if _, err := quotaRecoveryIntegerInRange(float64(5), 1, 10, "阈值"); err != nil {
		t.Fatalf("in range: %v", err)
	}
	if _, err := quotaRecoveryIntegerInRange(float64(11), 1, 10, "阈值"); err == nil {
		t.Fatal("超范围必须报错")
	}
}

func TestW1ErrorPolicyEffectsBridgeSQL(t *testing.T) {
	bridge := &chainErrorPolicyEffectsBridge{pg: false}
	if got := bridge.bindingExistsSQL(nil); got != "" {
		t.Fatalf("nil binding SQL = %q", got)
	}
	if bindingExistsParams(nil) != nil {
		t.Fatal("nil binding params 必须为 nil")
	}
	binding := &authorizedBindingTarget{SystemAccountID: "sys_1", GroupID: "grp_1", AccountAuthorizationID: "authz_1"}
	sqlText := bridge.bindingExistsSQL(binding)
	if !strings.Contains(sqlText, "group_accounts") || !strings.Contains(sqlText, "?") {
		t.Fatalf("sqlite SQL = %q", sqlText)
	}
	params := bindingExistsParams(binding)
	if len(params) != 3 || params[0] != "sys_1" || params[1] != "grp_1" || params[2] != "authz_1" {
		t.Fatalf("params = %v", params)
	}
	pgBridge := &chainErrorPolicyEffectsBridge{pg: true}
	if !strings.Contains(pgBridge.bindingExistsSQL(binding), "juhe_business.group_accounts") {
		t.Fatal("pg 表前缀缺失")
	}
	// bind：postgres 占位符重写。
	if got := pgBridge.bind("a = ? AND b = ?"); got != "a = $1 AND b = $2" {
		t.Fatalf("pg bind = %q", got)
	}
	if got := bridge.bind("a = ?"); got != "a = ?" {
		t.Fatalf("sqlite bind = %q", got)
	}
	// invalidateRuntime：nil bus 安全。
	bridge.invalidateRuntime("reason")
}

func TestW1ComposeSmallHelpers(t *testing.T) {
	request := httptest.NewRequest("GET", "/v1/chat/completions?q=1", nil)
	if got := pathWithoutQueryOf(request); got != "/v1/chat/completions" {
		t.Fatalf("path = %q", got)
	}
	emptyRequest := httptest.NewRequest("GET", "/v1/chat/completions", nil)
	emptyRequest.URL.Path = ""
	if got := pathWithoutQueryOf(emptyRequest); got != "/v1/chat/completions" {
		t.Fatalf("空 path 回落 = %q", got)
	}
	first := chainNewAuditID(gatewaypreauth.SystemClock{})
	second := chainNewAuditID(gatewaypreauth.SystemClock{})
	if first == "" || second == "" || first == second {
		t.Fatalf("audit id 必须唯一: %q vs %q", first, second)
	}
	if got := minOf(3, 7); got != 3 {
		t.Fatalf("minOf(3,7) = %d", got)
	}
	if got := chatOriginalMimeType("png"); got != "image/png" {
		t.Fatalf("png mime = %q", got)
	}
	if got := chatOriginalMimeType("unknown"); got != "" {
		t.Fatalf("未知格式 = %q", got)
	}
}

func TestW1TrafficMigrationScope(t *testing.T) {
	if trafficMigrationScope(nil) != nil {
		t.Fatal("nil scope 必须返回 nil")
	}
	scope := trafficMigrationScope(&accounts.TrafficMigrationScope{SystemAccountID: "sys_1", GroupID: "grp_1"})
	if scope.SystemAccountID != "sys_1" || scope.GroupID != "grp_1" {
		t.Fatalf("scope = %+v", scope)
	}
}

func TestW1WriteChainNotFound(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeChainNotFound(recorder)
	if recorder.Code != 404 || recorder.Body.String() != `{"message":"资源不存在"}` {
		t.Fatalf("404 契约 = %d %s", recorder.Code, recorder.Body.String())
	}
}
