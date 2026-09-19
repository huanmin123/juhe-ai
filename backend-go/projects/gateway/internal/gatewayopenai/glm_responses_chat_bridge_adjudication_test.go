package gatewayopenai

import (
	"encoding/json"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// GLM 三协议适配方案 P1 裁决测试（请求侧）。
//
// 背景：许可层（accounts/model_mapping_protocol_matrix.go）允许 openai 协议
// 账户配置 responses -> chat_completions 模型映射，路径改写
// （mapping.go modelMappedUpstreamPathAndQuery）会把 /responses 改到上游
// /chat/completions；本测试用运行时证据裁决请求体是否真的被桥转换成
// chat completions 格式。若未转换，Responses 格式 body 会原样打到 chat
// 上游（GLM/DeepSeek 等 chat 端点无法解析 input/instructions 字段）。
func TestAdjudicateGLMResponsesToChatBridgeRequestConversion(t *testing.T) {
	// 守护 responses -> chat_completions 请求体桥转换契约（BUG-0178 修复回归）。
	driver := NewDriver()
	input := gatewayproto.BuildUpstreamRequestInput{
		ClientPathAndQuery: "/v1/responses",
		Body:               []byte(`{"model":"client-model","stream":true,"input":"hi","instructions":"be brief"}`),
		UpstreamBaseURL:    "https://open.bigmodel.cn/api/coding/paas/v4",
		ModelMapping: &gatewayproto.ResolvedModelMapping{
			SourceModel:            "client-model",
			SourceEndpointFamily:   FamilyResponses,
			UpstreamModel:          "glm-upstream",
			UpstreamEndpointFamily: FamilyChatCompletions,
		},
	}
	result, err := driver.BuildUpstreamRequest(input)
	if err != nil {
		t.Fatalf("BuildUpstreamRequest error: %v", err)
	}
	if result.PathAndQuery != "/chat/completions" {
		t.Fatalf("路径改写 = %q，期望 /chat/completions（映射未生效）", result.PathAndQuery)
	}
	var body map[string]any
	if err := json.Unmarshal(result.Body, &body); err != nil {
		t.Fatalf("上游 body 非法: %v", err)
	}
	if body["model"] != "glm-upstream" {
		t.Fatalf("上游 model = %v，期望映射覆写为 glm-upstream", body["model"])
	}
	if _, has := body["messages"]; !has {
		t.Fatalf("上游 body 缺少 messages 字段：请求体未被桥转换为 chat completions 格式，body=%s", string(result.Body))
	}
	if _, has := body["input"]; has {
		t.Fatalf("上游 body 仍携带 Responses 格式 input 字段：桥未转换，body=%s", string(result.Body))
	}
	t.Logf("裁决通过：上游收到 chat completions 格式 body=%s", string(result.Body))
}
