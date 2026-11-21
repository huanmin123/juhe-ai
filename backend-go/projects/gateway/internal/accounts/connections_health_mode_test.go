package accounts

import (
	"strings"
	"testing"
)

func TestValidateModelTestHealthEndpointMode(t *testing.T) {
	// 模型测试账户只允许 10 个真实对话生成形态。
	for _, mode := range []string{
		"chat_json", "chat_sse", "responses_json", "responses_sse",
		"messages_json", "messages_sse", "generate_content_json", "generate_content_sse",
		"interactions_json", "interactions_sse",
	} {
		if err := validateModelTestHealthEndpointMode(mode, false); err != nil {
			t.Fatalf("模型测试账户形态 %s 应通过：%v", mode, err)
		}
	}
	for _, mode := range []string{"embeddings_json", "rerank_json", "count_tokens", "embed_content", "images_json", "audio_speech"} {
		err := validateModelTestHealthEndpointMode(mode, false)
		if err == nil || !strings.Contains(err.Error(), "必须是真实对话生成形态") {
			t.Fatalf("模型测试账户形态 %s 应拒绝，got %v", mode, err)
		}
	}
	// 连接测试账户的检查形态只是记录值，不受生成形态约束。
	for _, mode := range []string{"embeddings_json", "count_tokens", "images_json", "audio_job_create"} {
		if err := validateModelTestHealthEndpointMode(mode, true); err != nil {
			t.Fatalf("连接测试账户形态 %s 应通过：%v", mode, err)
		}
	}
}
