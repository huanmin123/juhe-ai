// managed_profile_dto_derived_test.go 钉创建端点 profile 身份的最终契约：
// providerProtocolProfileId 入参键已删除（managedFields 词表外），profile 一律
// 由创建链按 §1.1 registry 派生（managedProfileID -> profileIDForProvider）。
// 可观察 oracle：缺省建户成功即派生正确——resolveProviderProfile 对空入参必然
// 400（等值门）；显式提交该键按 strict-body 400 参数无效拒绝（B4⑩ 本体删除，
// 用户已裁决不做向后兼容），旧客户端载荷不再平滑。
package oauthmgmt

import (
	"net/http"
	"testing"
)

func TestManagedCreateProfileIDDTODerivedOnly(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.exchanger.respond = staticToken(openAITokenPayload("dto-derived"))

	// 缺省：不提交 profile 键，服务端按 registry 派生 gpt 的唯一合法档案身份
	// （等值门放行），建户成功。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"rt-derived","name":"DTO 缺省派生"}`)
	if code != http.StatusCreated {
		t.Fatalf("缺省派生建户: %d %v", code, payload)
	}

	// 显式提交该键（合法值也一样）：managedFields 词表外，strict-body 400。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"rt-explicit","providerProtocolProfileId":"profile_gpt_openai_v1","name":"DTO 显式键"}`)
	if code != http.StatusBadRequest || payload["message"] != "OpenAI 刷新令牌参数无效" {
		t.Fatalf("显式 profile 键: %d %v", code, payload)
	}
}

func TestManagedCreateProfileIDDTOGeminiDerivesNative(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)
	env.exchanger.respond = staticToken(`{"access_token":"gem-dto-access","refresh_token":"gem-dto-refresh","expires_in":3600,"token_type":"Bearer"}`)

	// gemini 缺省派生值必须等于 native pin：等值门只在派生正确时放行。
	// 显式 projectId 走零探测路径（T65-② 起 code_assist/google_one 未带
	// project 的建户会先做 Code Assist 探测/onboarding，与本测钉的派生契约
	// 无关）。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-refresh-token",
		`{"refreshToken":"rt-gem-dto","projectId":"dto-gem-proj","name":"DTO gemini 派生"}`)
	if code != http.StatusCreated {
		t.Fatalf("gemini 缺省派生建户: %d %v", code, payload)
	}

	// gemini 显式提交非 native 档案：同样先撞 strict-body 词表校验 → 400。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-refresh-token",
		`{"refreshToken":"rt-gem-wrong","providerProtocolProfileId":"profile_gemini_openai_chat_v1beta"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("gemini 显式 profile 键: %d %v", code, payload)
	}
}
