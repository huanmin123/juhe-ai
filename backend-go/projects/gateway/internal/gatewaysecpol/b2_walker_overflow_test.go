package gatewaysecpol

import (
	"bytes"
	"strings"
	"testing"
)

// 批B2 walker/重写器增补测试：chat reasoning_content 叶子、responses input
// 顶层字符串形态（任务契约「walker 补两字段」）与 StreamRewriter 溢出计数
// （Overflowed，供响应侧 placeholder_flush_overflow 日志）。

func TestSanitizeOpenAIChatReasoningContent(t *testing.T) {
	// 设计 §5「客户端回传的推理/思考文本字段同样属扫描对象」：chat 的
	// messages[].reasoning_content 是字符串叶子，随消息文本一并扫描替换。
	policy := singleRule(t, phonePlaceholderRule())
	body := []byte(`{"messages":[
		{"role":"system","content":"sys"},
		{"role":"assistant","content":"答案","reasoning_content":"用户来电 13812345678，需回拨"},
		{"role":"user","content":"打 13812345678"}
	]}`)
	result := SanitizeBody(policy, body, FamilyOpenAIChat)
	root := parseBody(t, result.Body)
	messages := asArray(root.get("messages"))
	reasoning := asObject(messages[1]).get("reasoning_content").(string)
	if reasoning != "用户来电 [[PHONE_1]]，需回拨" {
		t.Fatalf("reasoning_content 应被扫描替换：%q", reasoning)
	}
	content := asObject(messages[2]).get("content").(string)
	if content != "打 [[PHONE_1]]" {
		t.Fatalf("content 替换不符：%q", content)
	}
	// 同一原文跨字段复用同一编号（§2 编号稳定契约）。
	if result.Hits["PHONE"] != 2 {
		t.Fatalf("PHONE 命中数应为 2，得到 %d", result.Hits["PHONE"])
	}
}

func TestSanitizeOpenAIResponsesTopLevelInputString(t *testing.T) {
	// OpenAI Responses API 的 input 允许顶层字符串形态：按字符串叶子扫描。
	policy := singleRule(t, phonePlaceholderRule())
	body := []byte(`{"model":"gpt-test","input":"联系 13812345678 确认"}`)
	result := SanitizeBody(policy, body, FamilyOpenAIResponses)
	if !bytes.Contains(result.Body, []byte(`"input":"联系 [[PHONE_1]] 确认"`)) {
		t.Fatalf("顶层字符串 input 应被扫描替换：\n%s", result.Body)
	}
	if result.Hits["PHONE"] != 1 {
		t.Fatalf("PHONE 命中数应为 1，得到 %d", result.Hits["PHONE"])
	}
	if !result.Applied {
		t.Fatalf("有替换时 Applied 应为 true")
	}
	// 数组形态回归：字符串形态补丁不得破坏既有 input 数组扫描。
	arrayBody := []byte(`{"input":[{"type":"message","content":[{"type":"input_text","text":"打 13912345678"}]}]}`)
	arrayResult := SanitizeBody(policy, arrayBody, FamilyOpenAIResponses)
	if !bytes.Contains(arrayResult.Body, []byte("[[PHONE_1]]")) {
		t.Fatalf("数组形态应照常替换：\n%s", arrayResult.Body)
	}
}

func TestStreamRewriterOverflowCounter(t *testing.T) {
	// §7.2 第 3 条计数口径：每次缓冲超限放弃匹配 Overflowed +1；完整命中与
	// 未超限悬挂不计入。
	r := NewStreamRewriter(newTestMapping(t))
	if r.Overflowed() != 0 {
		t.Fatalf("初始溢出计数应为 0，得到 %d", r.Overflowed())
	}
	long := "[[" + strings.Repeat("a", 200)
	if got := string(r.Rewrite([]byte(long))); got != long {
		t.Fatalf("超限序列应原样放行：%q", got)
	}
	if r.Overflowed() != 1 {
		t.Fatalf("超限一次后计数应为 1，得到 %d", r.Overflowed())
	}
	// 跨 chunk 正常悬挂（未超限）不计溢出。
	_ = r.Rewrite([]byte("ok [[PH"))
	_ = r.Rewrite([]byte("ONE_2]] done"))
	if r.Overflowed() != 1 {
		t.Fatalf("正常悬挂不应计入溢出，得到 %d", r.Overflowed())
	}
	// 再次超限累计 +1。
	_ = r.Rewrite([]byte("[[" + strings.Repeat("b", 200)))
	if r.Overflowed() != 2 {
		t.Fatalf("两次超限后计数应为 2，得到 %d", r.Overflowed())
	}
}
