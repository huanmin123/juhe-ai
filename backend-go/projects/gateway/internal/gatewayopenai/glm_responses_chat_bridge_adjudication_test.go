package gatewayopenai

import (
	"encoding/json"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// GLM 三协议适配方案 P1 裁决测试（请求侧）——2026-11-07 跨协议模型映射退役后
// 的终态口径。
//
// 背景：矩阵曾允许 openai 协议账户配置 responses -> chat_completions 模型映射
// （GLM/DeepSeek 等 chat 端点承接 Codex），路径改写把 /responses 改到上游
// /chat/completions，请求体经 codex-responses-chat-bridge 转换（BUG-0178 修复
// 回归曾守护该链路）。退役后 7 pair 矩阵只放行协议等价映射，本测试锁定两道
// 终态防线：
//
//  1. 运行时 resolver 不再解析 responses→chat 映射行
//     （IsAdmissibleModelMapping=false）——存量行/误配行不会进入执行面；
//  2. 即便手工构造出该 ResolvedModelMapping（绕过 resolver），BuildUpstreamRequest
//     也不再触发任何桥行为——路径保持 /responses 不改写、body 不转换（仅模型
//     改名），不再有「桥分发表显式报错」的防线（2026-11-07 批次 D 删除桥分发
//     表；跨协议组合在 resolver/写侧即被拒绝，driver 层无桥可触发）。
func TestAdjudicateGLMResponsesToChatBridgeRetired(t *testing.T) {
	// 防线 1：resolver 层拒绝（写侧矩阵已删该 pair，运行时同步收口）。
	resolverAccount := &RuntimeAccount{ModelMappings: []AccountModelMapping{{
		SourceModel:            "client-model",
		SourceEndpointFamily:   FamilyResponses,
		UpstreamModel:          "glm-upstream",
		UpstreamEndpointFamily: FamilyChatCompletions,
	}}}
	if resolved := ResolveAccountModelMapping(resolverAccount, "client-model", FamilyResponses); resolved != nil {
		t.Fatalf("responses→chat 映射行已退役，resolver 不应解析: %+v", resolved)
	}
	if IsAdmissibleModelMapping(resolverAccount.ModelMappings[0], resolverAccount) {
		t.Fatal("responses→chat 映射行已退役，IsAdmissibleModelMapping 不应放行")
	}

	// 防线 2：绕过 resolver 的手工构造在请求构建处显式失败。
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
		t.Fatalf("退役组合不得在 driver 层触发桥报错（桥已删除）: %v", err)
	}
	if result.PathAndQuery != "/v1/responses" {
		t.Fatalf("退役跨族映射不得改写上游路径：path = %q", result.PathAndQuery)
	}
	var body map[string]any
	if err := json.Unmarshal(result.Body, &body); err != nil {
		t.Fatalf("出站 body 必须仍是 JSON: %v", err)
	}
	if body["model"] != "glm-upstream" {
		t.Fatalf("模型改名仍应生效: %v", body["model"])
	}
	if _, has := body["input"]; !has {
		t.Fatalf("body 不得被转换成 chat 形态（input 字段必须保留）: %s", string(result.Body))
	}
	if _, has := body["messages"]; has {
		t.Fatalf("body 不得被转换成 chat 形态（不得出现 messages）: %s", string(result.Body))
	}
}
