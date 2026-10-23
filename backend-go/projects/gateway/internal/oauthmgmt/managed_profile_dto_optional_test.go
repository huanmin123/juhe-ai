// managed_profile_dto_optional_test.go 钉创建端点 providerProtocolProfileId 的
// DTO 收口三态：缺省（registry 派生成功）、显式提交合法值（等值校验通过）、
// 显式提交词表外值（400 供应商协议档案无效）；空白串按缺省派生，非字符串值
// 保持 400 参数无效。带值提交经 guarded 路由，同时回归 guard fingerprint 的
// providerProtocolProfileId 分支。可观察 oracle 说明：缺省建户成功即派生正确
// ——resolveProviderProfile 对空入参必然 400（等值门），M08 写面落库身份由
// connections 推导（入参接受但忽略），不以落库列为准。
package oauthmgmt

import (
	"net/http"
	"testing"
)

func TestManagedCreateProfileIDDTOOptional(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.exchanger.respond = staticToken(openAITokenPayload("dto-optional"))

	// 缺省：不提交 providerProtocolProfileId，服务端按 registry 派生 gpt 的
	// 唯一合法档案身份（等值门放行），建户成功；旧契约下此处为 400 参数无效。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"rt-derived","name":"DTO 缺省派生"}`)
	if code != http.StatusCreated {
		t.Fatalf("缺省派生建户: %d %v", code, payload)
	}

	// 空白串按缺省处理（与本文件空白语义一致），同样落 registry 派生。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"rt-blank","providerProtocolProfileId":"   ","name":"DTO 空白串"}`)
	if code != http.StatusCreated {
		t.Fatalf("空白串派生建户: %d %v", code, payload)
	}

	// 显式提交合法值：等值校验通过，建户成功（经 guarded 路由，回归 guard
	// fingerprint 的 providerProtocolProfileId 分支）。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"rt-explicit","providerProtocolProfileId":"profile_gpt_openai_v1","name":"DTO 显式合法"}`)
	if code != http.StatusCreated {
		t.Fatalf("显式合法建户: %d %v", code, payload)
	}

	// 显式提交词表外值：等值校验拒绝，400 文案保持 resolveProviderProfile 口径。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"rt-bogus","providerProtocolProfileId":"profile-nope"}`)
	if code != http.StatusBadRequest || payload["message"] != "供应商协议档案无效：profile-nope" {
		t.Fatalf("词表外 profile: %d %v", code, payload)
	}
}

func TestManagedCreateProfileIDDTOGeminiDerivesNative(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)
	env.exchanger.respond = staticToken(`{"access_token":"gem-dto-access","refresh_token":"gem-dto-refresh","expires_in":3600,"token_type":"Bearer"}`)

	// gemini 缺省派生值必须等于 native pin：等值门只在派生正确时放行。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-refresh-token",
		`{"refreshToken":"rt-gem-dto","name":"DTO gemini 派生"}`)
	if code != http.StatusCreated {
		t.Fatalf("gemini 缺省派生建户: %d %v", code, payload)
	}

	// gemini 提交非 native 档案：先撞 registry 等值校验 → 400 档案无效。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-refresh-token",
		`{"refreshToken":"rt-gem-wrong","providerProtocolProfileId":"profile_gemini_openai_chat_v1beta"}`)
	if code != http.StatusBadRequest || payload["message"] != "供应商协议档案无效：profile_gemini_openai_chat_v1beta" {
		t.Fatalf("gemini 词表外 profile: %d %v", code, payload)
	}
}
