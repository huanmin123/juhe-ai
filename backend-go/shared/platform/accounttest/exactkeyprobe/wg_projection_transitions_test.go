// 本文件由 jobs internal/accounthealth/wg_projection_transitions_test.go 拆分而来（成对关系见 probe.go 包注释）：
// 承载随探针执行器闭包下沉的被移函数测试；同源文件的留守测试仍在 jobs 包内，
// 两侧不重复、不丢失。
package exactkeyprobe

import (
	"testing"
	"time"
)

// TestVerifyResponseSSEDispatch 覆盖 verifyResponse 的 SSE 分派（走顶层
// switch 而非各 verifier 直测）。
func TestVerifyResponseSSEDispatch(t *testing.T) {
	goodChat := "data: {\"choices\":[{\"delta\":{\"content\":\"juhe\"}}]}\n\ndata: [DONE]\n\n"
	if err := verifyResponse(wgSSEInput("chat_sse", "openai", "profile_openai_openai_v1"), []byte(goodChat)); err != nil {
		t.Fatalf("chat_sse 分派: %v", err)
	}
	goodResponses := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"juhe\"}\n\n" +
		"data: {\"type\":\"response.completed\"}\n\n"
	if err := verifyResponse(wgSSEInput("responses_sse", "openai", "profile_openai_openai_v1"), []byte(goodResponses)); err != nil {
		t.Fatalf("responses_sse 分派: %v", err)
	}
	goodMessages := "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"juhe\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
	if err := verifyResponse(wgSSEInput("messages_sse", "anthropic", "profile_anthropic_anthropic_v1"), []byte(goodMessages)); err != nil {
		t.Fatalf("messages_sse 分派: %v", err)
	}
	goodGemini := "data: {\"candidates\":[{\"finishReason\":\"STOP\",\"content\":{\"parts\":[{\"text\":\"juhe\"}]}}]}\n\n"
	if err := verifyResponse(wgSSEInput("generate_content_sse", "gemini", "profile_gemini_native_v1beta"), []byte(goodGemini)); err != nil {
		t.Fatalf("generate_content_sse 分派: %v", err)
	}
	goodInteractions := "data: {\"status\":\"completed\"}\n\ndata: {\"interaction\":{\"output\":\"juhe\"}}\n\ndata: [DONE]\n\n"
	if err := verifyResponse(wgSSEInput("interactions_sse", "gemini", "profile_gemini_native_v1beta"), []byte(goodInteractions)); err != nil {
		t.Fatalf("interactions_sse 分派: %v", err)
	}
	// OAuth 过期分支（validateInput）。
	oauth := wgSSEInput("responses_sse", "gpt", "profile_gpt_openai_v1")
	oauth.Type = "oauth"
	oauth.OAuthExpiresAt = ptrTime(time.Now().Add(-time.Minute))
	if err := validateInput(oauth, ProbeOptions{}); err == nil {
		t.Fatal("OAuth token 过期必须报错")
	}
}
