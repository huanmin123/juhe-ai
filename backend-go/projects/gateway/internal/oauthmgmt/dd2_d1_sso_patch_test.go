package oauthmgmt

// DD2-D1 钉住测试：grok sso-to-oauth 必须应用 credentialsPatch——patch 后置
// 合并（mergePatchLast，patch 优先）与 create-from-code/refresh 的 exchange 臂
// 同语义；非法 patch 400 credentialsPatch 无效；无 patch 载荷保持 token 构造
// 值不变。复用 scripted SSO 传输（进程内完成设备流），DB 流程测试。

import (
	"net/http"
	"strings"
	"testing"
)

func dd2D1ArmSSO(t *testing.T, env *testEnv) {
	t.Helper()
	env.sso.steps = []SSODeviceResponse{
		ssoStep(http.StatusOK, nil, "<html>accounts</html>"),
		ssoStep(http.StatusOK, nil, `{"device_code":"dc-dd2","user_code":"UC-DD2","verification_uri_complete":"https://auth.x.ai/device?user_code=UC-DD2","interval":1,"expires_in":600}`),
		ssoStep(http.StatusOK, nil, "<html>device page</html>"),
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/consent"}, ""),
		ssoStep(http.StatusOK, nil, "<html>consent</html>"),
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/done"}, ""),
		ssoStep(http.StatusOK, nil, "<html>done</html>"),
		ssoStep(http.StatusOK, nil, grokTokenPayload("grok-access-dd2")),
	}
	env.exchanger.respond = staticToken(`{}`)
}

func TestDD2D1SSOToOAuthAppliesCredentialsPatch(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	// 带 credentialsPatch 的 SSO 导入：patch 覆盖 token 构造值，token 键保留。
	// 三臂各用不同 SSO Cookie：同 token 短窗重复导入会命中指纹幂等 409。
	dd2D1ArmSSO(t, env)
	code, imported := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoTokens":["sso=patch-arm"],"credentialsPatch":{"base_url":"https://patch.example/v1","error_handling_rule_overrides":[{"system_rule_id":"system.upstream_insufficient_quota","action":"delete"}]}}`)
	if code != http.StatusOK {
		t.Fatalf("sso-to-oauth: %d %v", code, imported)
	}
	result := dataMap(t, imported)
	if result["createdCount"] != float64(1) {
		t.Fatalf("sso result: %v", result)
	}
	accountID := result["createdIds"].([]any)[0].(string)
	credentials := env.accountCredentials(t, accountID)
	if credentials["base_url"] != "https://patch.example/v1" {
		t.Fatalf("patch 必须覆盖 token 构造的 base_url：%v", credentials["base_url"])
	}
	if credentials["access_token"] != "grok-access-dd2" {
		t.Fatalf("patch 合并不得丢弃 token 构造键：%v", credentials["access_token"])
	}
	overrides, ok := credentials["error_handling_rule_overrides"].([]any)
	if !ok || len(overrides) != 1 {
		t.Fatalf("patch 内系统规则覆盖必须透传：%v", credentials["error_handling_rule_overrides"])
	}

	// 无 patch 载荷：base_url 保持 token 构造值（既有契约不变）。
	dd2D1ArmSSO(t, env)
	code, imported = env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoTokens":["sso=plain-arm"]}`)
	if code != http.StatusOK {
		t.Fatalf("sso-to-oauth（无 patch）: %d %v", code, imported)
	}
	accountID = dataMap(t, imported)["createdIds"].([]any)[0].(string)
	if got := env.accountCredentials(t, accountID)["base_url"]; got != GrokOAuthBaseURL {
		t.Fatalf("无 patch 时 base_url 应为 token 构造值：%v", got)
	}

	// 非法 patch：strictBody 收键但 safePatch 解析失败 → 400 credentialsPatch 无效。
	dd2D1ArmSSO(t, env)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoTokens":["sso=invalid-arm"],"credentialsPatch":"x"}`)
	if code != http.StatusBadRequest || !strings.Contains(payload["message"].(string), "credentialsPatch") {
		t.Fatalf("非法 patch 必须 400 credentialsPatch 无效：%d %v", code, payload)
	}
}
