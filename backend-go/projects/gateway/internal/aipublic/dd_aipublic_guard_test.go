package aipublic

// 深挖复审修复批钉住测试（DD-A3① / DD-A4）：
//   - DD-A3①：无效 token 认证失败（401）消费逐来源 IP 惩罚窗——连续失败达
//     阈值（10 次/60s 窗）后 429 external_source_rate_limited；单次失败与合法
//     token 流量不受影响（合法 token 键空间独立）。
//   - DD-A4：请求体超 256 KiB 上限渲染与管理面一致的 413 {"message":"请求体
//     过大"}，不读入全量 body；正常 body 无感。
import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestDDA3InvalidTokenPenaltyWindowAtThreshold：无效 token 连续 10 次 → 全部
// 401；第 11 次 → 429 惩罚窗；同 IP 换有效 token → 合法请求仍 200。
func TestDDA3InvalidTokenPenaltyWindowAtThreshold(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedTargetUser("user_dd_a3", "dd_a3_user", "active")
	goodToken := env.seedSource("extsrc_dd_a3", "exttok_dd_a3", "juis_token_dda3aaaaaaaaaaa",
		"active", "active", []string{"juhe_ai_public:group_list:read"}, "[]", "", "")

	for i := 1; i <= invalidTokenPenaltyMaxRequests; i++ {
		status, payload, _ := env.doAuth(http.MethodGet, "/__aipublic__/group/list?targetUsername=dd_a3_user", "", "juis_unknown_invalid")
		if status != http.StatusUnauthorized || payload["code"] != "external_source_unauthorized" {
			t.Fatalf("第 %d 次无效 token：status=%d payload=%v, want 401/external_source_unauthorized（阈值内不得误伤）", i, status, payload["code"])
		}
	}
	status, payload, header := env.doAuth(http.MethodGet, "/__aipublic__/group/list?targetUsername=dd_a3_user", "", "juis_unknown_invalid")
	if status != http.StatusTooManyRequests || payload["code"] != "external_source_rate_limited" {
		t.Fatalf("阈值次无效 token：status=%d payload=%v, want 429/external_source_rate_limited", status, payload["code"])
	}
	if header.Get("Retry-After") == "" {
		t.Fatal("惩罚窗 429 必须携带 Retry-After")
	}
	// 合法 token 键空间独立：同 IP 惩罚窗激活不影响合法调用。
	status, payload, _ = env.doAuth(http.MethodGet, "/__aipublic__/group/list?targetUsername=dd_a3_user", "", goodToken)
	if status != http.StatusOK {
		t.Fatalf("惩罚窗激活后的合法 token：status=%d payload=%v, want 200", status, payload)
	}
}

// TestDDA3SingleInvalidTokenFailureNoPenalty：单次失败不误伤——一次 401 后
// 有效 token 正常通过；403 授权失败（scope 不足）不计数。
func TestDDA3SingleInvalidTokenFailureNoPenalty(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedTargetUser("user_dd_a3b", "dd_a3b_user", "active")
	goodToken := env.seedSource("extsrc_dd_a3b", "exttok_dd_a3b", "juis_token_dda3baaaaaaaaaa",
		"active", "active", []string{"juhe_ai_public:group_list:read"}, "[]", "", "")
	scopelessToken := env.seedSource("extsrc_dd_a3s", "exttok_dd_a3s", "juis_token_dda3saaaaaaaaaa",
		"active", "active", []string{"juhe_ai_public:group_list:read"}, "[]", "", "")

	if status, _, _ := env.doAuth(http.MethodGet, "/__aipublic__/group/list?targetUsername=dd_a3b_user", "", "juis_unknown_once"); status != http.StatusUnauthorized {
		t.Fatalf("单次失败 status=%d, want 401", status)
	}
	if status, _, _ := env.doAuth(http.MethodGet, "/__aipublic__/group/list?targetUsername=dd_a3b_user", "", goodToken); status != http.StatusOK {
		t.Fatalf("单次失败后合法 token status=%d, want 200（不误伤）", status)
	}
	// 403 授权失败不计入惩罚窗：403 后仍走原 403 形态（而非 429）。
	for i := 0; i < invalidTokenPenaltyMaxRequests+3; i++ {
		status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/group/add", `{"name":"x"}`, scopelessToken)
		if status != http.StatusForbidden || payload["code"] != "external_source_scope_forbidden" {
			t.Fatalf("403 授权失败不得计数：status=%d code=%v, want 403/external_source_scope_forbidden", status, payload["code"])
		}
	}
}

// TestDDA4BodyOverLimitReturns413：超限 body → 413 {"message":"请求体过大"}
//（与管理面 DecodeJSON 同款形态）；正常 body 同 token 无感。
func TestDDA4BodyOverLimitReturns413(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedTargetUser("user_dd_a4", "dd_a4_user", "active")
	goodToken := env.seedSource("extsrc_dd_a4", "exttok_dd_a4", "juis_token_dda4aaaaaaaaaaa",
		"active", "active", []string{"juhe_ai_public:group_add:write"}, "[]", "", "")

	oversized := `{"name":"` + strings.Repeat("x", int(publicJSONBodyLimitBytes)) + `"}`
	status, rawPayload, _ := env.doAuth(http.MethodPost, "/__aipublic__/group/add", oversized, goodToken)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限 body status=%d, want 413", status)
	}
	if rawPayload["message"] != "请求体过大" {
		t.Fatalf("超限响应 payload=%v, want message=请求体过大（管理面同款）", rawPayload)
	}
	if _, hasCode := rawPayload["code"]; hasCode {
		t.Fatalf("超限响应不得携带 aipublic code 字段: %v", rawPayload)
	}
	// 正常 body 无感（同 token 通过 guard 与处理器，正常创建 201）。
	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"name":"dd-a4-group","targetUsername":"dd_a4_user"}`, goodToken)
	if status != http.StatusCreated {
		t.Fatalf("正常 body status=%d payload=%v, want 201", status, payload)
	}
	_ = json.Marshal
}
