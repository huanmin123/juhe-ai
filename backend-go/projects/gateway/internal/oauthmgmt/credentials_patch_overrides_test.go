package oauthmgmt

// DD-E4 深挖复审修复批钉面：B5 同族 strict-body 缺口——前端创建向导「错误与
// 响应规则」卡对 oauth/google_oauth 建户提交 error_handling_rule_overrides
// （credentialsPatch 内），四家 patch 解析器曾按 default 分支拒绝 → 整请求
// 400，而 accounts 写入面（normalizeAccountCredentialPolicies）接受该键，
// 门禁与写入面矛盾。修复后钉面断言：
//   - 四家解析器接受该键并原样透传（与 error_handling_rules 等既有 policy
//     键同款「接受不预检」语义，值域校验属 accounts 写入面）；
//   - 混入未知键仍拒绝（strict 语义保持，不因新键放宽）；
//   - patch 透传键经 mergePatch 合成进凭据后，能通过建户持久化到达点的
//     accounts 写入面同一归一化门（NormalizeAccountCredentialsForWrite），
//     error_handling_rule_overrides 以归一化形状留在加密前凭据内——oauthmgmt
//     组装的 patch 与 accounts 写面期望一致。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
)

// oauthErrorPolicyOverrides 构造前端提交形状的系统规则覆盖（replace + delete
// 各一条，值与字段集与 accounts 写入面 normalizeAccountErrorPolicyOverrides
// 的归一化输出对齐）。
func oauthErrorPolicyOverrides() []any {
	return []any{
		map[string]any{"system_rule_id": "system.upstream_insufficient_quota", "action": "replace", "rule_index": float64(0)},
		map[string]any{"system_rule_id": "system.upstream_insufficient_quota", "action": "delete"},
	}
}

// TestOAuthCredentialsPatchParsersAcceptErrorHandlingRuleOverrides：四家解析器
// 接受 error_handling_rule_overrides 且原样透传（含 nil → 写入面空列表语义）。
func TestOAuthCredentialsPatchParsersAcceptErrorHandlingRuleOverrides(t *testing.T) {
	overrides := oauthErrorPolicyOverrides()
	for name, parse := range map[string]func(map[string]any) (map[string]any, bool){
		"openai":    parseOpenAICredentialsPatch,
		"anthropic": parseAnthropicCredentialsPatch,
		"gemini":    parseGeminiCredentialsPatch,
		"grok":      parseGrokCredentialsPatch,
	} {
		patch, ok := parse(map[string]any{"error_handling_rule_overrides": overrides})
		if !ok {
			t.Fatalf("%s parser must accept error_handling_rule_overrides（B5 同族缺口）", name)
		}
		passed, isList := patch["error_handling_rule_overrides"].([]any)
		if !isList || len(passed) != 2 {
			t.Fatalf("%s passthrough shape: %#v", name, patch["error_handling_rule_overrides"])
		}
		// nil 透传：写入面对 nil 归一化为空列表，parser 层不预检。
		if patch, ok := parse(map[string]any{"error_handling_rule_overrides": nil}); !ok || patch["error_handling_rule_overrides"] != nil {
			t.Fatalf("%s nil passthrough: %v %#v", name, ok, patch["error_handling_rule_overrides"])
		}
	}
}

// TestOAuthCredentialsPatchUnknownKeyStillRejected：新键放行后未知键仍拒绝
// （strict 语义保持）。
func TestOAuthCredentialsPatchUnknownKeyStillRejected(t *testing.T) {
	for name, parse := range map[string]func(map[string]any) (map[string]any, bool){
		"openai":    parseOpenAICredentialsPatch,
		"anthropic": parseAnthropicCredentialsPatch,
		"gemini":    parseGeminiCredentialsPatch,
		"grok":      parseGrokCredentialsPatch,
	} {
		if _, ok := parse(map[string]any{"error_handling_rule_overrides": []any{}, "mystery": 1}); ok {
			t.Fatalf("%s parser must still reject unknown keys", name)
		}
	}
}

// TestOAuthPatchOverridesAlignedWithAccountsWriteFace：patch 透传键经建户链
// 同款 merge 合成进凭据后，通过 accounts 写入面归一化门（CreateOAuthAccount →
// accounts.Create 的持久化到达点同一函数 NormalizeAccountCredentialsForWrite）
// ——oauth / google_oauth 两账户类型的归一化凭据均含归一化后的
// error_handling_rule_overrides（加密前形状）。
func TestOAuthPatchOverridesAlignedWithAccountsWriteFace(t *testing.T) {
	overrides := oauthErrorPolicyOverrides()
	// 建户链 merge 语义样本：openai 是 {...patch, ...token}，其余三家是
	// {...token, ...patch}——两个方向下覆盖键都保留（token 凭据不含该键）。
	// base_url 为写入面必填键，按各构建器默认形态补齐。
	openAICredentials := mergePatchOpenAI(
		mustParsePatch(t, parseOpenAICredentialsPatch, map[string]any{"error_handling_rule_overrides": overrides}),
		map[string]any{"access_token": "t", "refresh_token": "r", "base_url": "https://api.openai.com/v1"},
	)
	otherCredentials := mergePatchLast(
		map[string]any{"access_token": "t", "refresh_token": "r", "base_url": "https://upstream.example"},
		mustParsePatch(t, parseAnthropicCredentialsPatch, map[string]any{"error_handling_rule_overrides": overrides}),
	)
	// google_oauth 形态：写入面要求 refresh token 配套 client_id + client_secret。
	googleCredentials := mergePatchLast(
		map[string]any{"access_token": "t", "refresh_token": "r", "base_url": "https://upstream.example",
			"client_id": "cid", "client_secret": "csecret"},
		mustParsePatch(t, parseGeminiCredentialsPatch, map[string]any{"error_handling_rule_overrides": overrides}),
	)
	for _, tc := range []struct {
		name        string
		accountType string
		credentials accounts.Credentials
	}{
		{"openai_oauth", "oauth", openAICredentials},
		{"anthropic_oauth", "oauth", otherCredentials},
		{"gemini_google_oauth", "google_oauth", googleCredentials},
		{"grok_oauth", "oauth", otherCredentials},
	} {
		normalized, err := accounts.NormalizeAccountCredentialsForWrite(tc.accountType, tc.credentials, nil)
		if err != nil {
			t.Fatalf("%s: 写入面归一化必须接受 patch 覆盖键: %v", tc.name, err)
		}
		stored, isList := normalized["error_handling_rule_overrides"].([]any)
		if !isList || len(stored) != 2 {
			t.Fatalf("%s: 归一化凭据缺 error_handling_rule_overrides: %#v", tc.name, normalized["error_handling_rule_overrides"])
		}
		replace, isMap := stored[0].(map[string]any)
		if !isMap || replace["action"] != "replace" || replace["rule_index"] != float64(0) {
			t.Fatalf("%s: replace 覆盖归一化形状: %#v", tc.name, stored[0])
		}
		del, isMap := stored[1].(map[string]any)
		if !isMap || del["action"] != "delete" {
			t.Fatalf("%s: delete 覆盖归一化形状: %#v", tc.name, stored[1])
		}
	}
}

// mustParsePatch 断言 patch 解析成功并返回输出。
func mustParsePatch(t *testing.T, parse func(map[string]any) (map[string]any, bool), patch map[string]any) map[string]any {
	t.Helper()
	output, ok := parse(patch)
	if !ok {
		t.Fatalf("patch parse must pass: %#v", patch)
	}
	return output
}
