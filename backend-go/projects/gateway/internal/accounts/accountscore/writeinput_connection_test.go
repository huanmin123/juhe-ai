package accountscore

import (
	"strings"
	"testing"
)

// 上游接口能力设计 §3（五域退役）：连接 endpointModes 键缺失放行（五域连接
// 无声明），键存在时仍必须是非空字符串数组（对话域语义保留，resolveConnectionSet
// 分域承担非空与组合校验）。
func TestParseConnectionInputEndpointModesKeyOptional(t *testing.T) {
	base := map[string]any{
		"connectionPresetCode": "profile_qwen_openai_v1",
		"baseUrl":              "https://dashscope.aliyuncs.com/compatible-mode/v1",
		"enabled":              true,
	}

	// 五域连接：缺键放行，EndpointModes 为 nil。
	connection, ok := ParseConnectionInput(cloneConnectionObject(base))
	if !ok {
		t.Fatalf("缺 endpointModes 键应放行（五域连接）")
	}
	if len(connection.EndpointModes) != 0 {
		t.Fatalf("缺键时 EndpointModes 应为空，实际 %v", connection.EndpointModes)
	}

	// 对话域连接：非空数组放行且解析正确。
	withModes := cloneConnectionObject(base)
	withModes["endpointModes"] = []any{"chat_json", "chat_sse"}
	connection, ok = ParseConnectionInput(withModes)
	if !ok {
		t.Fatalf("非空 endpointModes 应放行（对话域连接）")
	}
	if got := strings.Join(connection.EndpointModes, ","); got != "chat_json,chat_sse" {
		t.Fatalf("endpointModes 解析不符，实际 %v", connection.EndpointModes)
	}

	// 键存在但为空数组：仍拒绝（对话域显式空集语义保留）。
	empty := cloneConnectionObject(base)
	empty["endpointModes"] = []any{}
	if _, ok := ParseConnectionInput(empty); ok {
		t.Fatalf("显式空 endpointModes 数组应拒绝")
	}

	// 键存在但非数组：仍拒绝。
	nonArray := cloneConnectionObject(base)
	nonArray["endpointModes"] = "chat_json"
	if _, ok := ParseConnectionInput(nonArray); ok {
		t.Fatalf("非数组 endpointModes 应拒绝")
	}

	// 未知键：仍拒绝。
	unknown := cloneConnectionObject(base)
	unknown["unexpected"] = 1
	if _, ok := ParseConnectionInput(unknown); ok {
		t.Fatalf("未知键应拒绝")
	}
}

func cloneConnectionObject(base map[string]any) map[string]any {
	out := make(map[string]any, len(base)+1)
	for key, value := range base {
		out[key] = value
	}
	return out
}
