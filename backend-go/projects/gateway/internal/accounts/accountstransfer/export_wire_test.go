package accountstransfer

import (
	"encoding/json"
	"strings"
	"testing"
)

// 上游接口能力设计 §3（五域退役）：导出 wire 的五域连接（无 modes 行）必须
// 省略 endpointModes 键（omitempty），对话域连接保留该键——保证五域导出文件
// 可被 accountscore.ParseConnectionInput 原样接受（缺键放行），导出再导入回路
// 不因空数组断裂。
func TestExportConnectionEndpointModesOmittedWhenEmpty(t *testing.T) {
	empty, err := json.Marshal(map[string]any{"row": ExportConnection{
		ConnectionPresetCode: "profile_volcengine_ark_v1",
		BaseURL:              "https://ark.cn-beijing.volces.com/api/v3",
		EndpointModes:        nil,
		Enabled:              true,
	}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(empty), "endpointModes") {
		t.Fatalf("空 endpointModes 应省略键，实际 %s", empty)
	}

	chat, err := json.Marshal(map[string]any{"row": ExportConnection{
		ConnectionPresetCode: "profile_qwen_openai_v1",
		BaseURL:              "https://dashscope.aliyuncs.com/compatible-mode/v1",
		EndpointModes:        []string{"chat_json", "chat_sse"},
		Enabled:              true,
	}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(chat), `"endpointModes":["chat_json","chat_sse"]`) {
		t.Fatalf("对话域连接应保留 endpointModes 键，实际 %s", chat)
	}
}
