package gatewaysecpol

import (
	"bytes"
	"testing"
)

// SanitizeBody 语义测试：编号/复用/跨字段一致、占位符保护、fail-open、数字
// 叶子、协议族字段清单、跨请求编号稳定。

func TestPlaceholderNumberingAndDedup(t *testing.T) {
	policy := singleRule(t, phonePlaceholderRule())
	// 带 system 消息保持消息位置稳定（注入追加到既有 system 末尾）。
	body := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"13812345678 然后 13912345678 然后 13812345678"},{"role":"user","content":"再次 13812345678"}]}`)
	result := SanitizeBody(policy, body, FamilyOpenAIChat)
	root := parseBody(t, result.Body)
	messages := asArray(root.get("messages"))
	if len(messages) != 3 {
		t.Fatalf("消息数应保持 3，得到 %d", len(messages))
	}
	first := asObject(messages[1]).get("content").(string)
	if first != "[[PHONE_1]] 然后 [[PHONE_2]] 然后 [[PHONE_1]]" {
		t.Fatalf("编号/复用不符：%q", first)
	}
	second := asObject(messages[2]).get("content").(string)
	if second != "再次 [[PHONE_1]]" {
		t.Fatalf("跨字段同值应复用同一编号：%q", second)
	}
	if result.Hits["PHONE"] != 4 {
		t.Fatalf("PHONE 命中数应为 4，得到 %d", result.Hits["PHONE"])
	}
	entry, ok := result.Mapping.lookup("[[PHONE_1]]")
	if !ok || entry.value != "13812345678" || entry.isNumber {
		t.Fatalf("映射表 PHONE_1 条目不符: %+v ok=%v", entry, ok)
	}
	entry2, ok := result.Mapping.lookup("[[PHONE_2]]")
	if !ok || entry2.value != "13912345678" {
		t.Fatalf("映射表 PHONE_2 条目不符: %+v ok=%v", entry2, ok)
	}
	hitsCopy := result.Mapping.Hits()
	if hitsCopy["PHONE"] != 4 {
		t.Fatalf("Mapping.Hits 应与聚合一致，得到 %v", hitsCopy)
	}
}

func TestPlaceholderProtection(t *testing.T) {
	// 验收 14：规则 pattern 覆盖占位符子串时不得改写占位符（设计 §5）。
	policy := mustCompile(t, `{"rules":[
		{"id":"r1","type":"phone","action":"placeholder"},
		{"id":"r2","type":"custom_keywords","keywords":["PHONE_1","1"],"action":"mask"},
		{"id":"r3","type":"custom_regex","pattern":"\\[\\[[A-Z_0-9]+\\]\\]","action":"mask"}
	]}`)
	body := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"打 13812345678 求助"}]}`)
	result := SanitizeBody(policy, body, FamilyOpenAIChat)
	root := parseBody(t, result.Body)
	content := asObject(asArray(root.get("messages"))[1]).get("content").(string)
	if content != "打 [[PHONE_1]] 求助" {
		t.Fatalf("占位符被后续规则改写：%q", content)
	}
	if bytes.Contains(result.Body, []byte("****")) {
		t.Fatalf("占位符不应被 mask：\n%s", result.Body)
	}
	if _, ok := result.Mapping.lookup("[[PHONE_1]]"); !ok {
		t.Fatalf("占位符应可还原")
	}
}

func TestMaskOnlyNoMappingAndNoInject(t *testing.T) {
	policy := singleRule(t, phoneMaskRule())
	result := SanitizeBody(policy, []byte(`{"messages":[{"role":"user","content":"打 13812345678"}]}`), FamilyOpenAIChat)
	if result.Mapping != nil {
		t.Fatalf("mask 策略不应产生映射表")
	}
	if result.Hits["PHONE"] != 1 {
		t.Fatalf("命中计数不符: %v", result.Hits)
	}
	if bytes.Contains(result.Body, []byte("【隐私保护说明】")) {
		t.Fatalf("纯 mask 策略不应注入系统提示")
	}
	if !bytes.Contains(result.Body, []byte("138****5678")) {
		t.Fatalf("应包含打码值：\n%s", result.Body)
	}
}

func TestSanitizeFailOpen(t *testing.T) {
	policy := singleRule(t, phonePlaceholderRule())
	t.Run("畸形内层 JSON 跳过计数", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"打 13812345678"},{"role":"assistant","tool_calls":[{"function":{"name":"f","arguments":"{broken"}}]}]}`)
		result := SanitizeBody(policy, body, FamilyOpenAIChat)
		if result.Skips != 1 {
			t.Fatalf("Skips 应为 1，得到 %d", result.Skips)
		}
		if !bytes.Contains(result.Body, []byte("{broken")) {
			t.Fatalf("解析失败节点应原样保留：\n%s", result.Body)
		}
		if !bytes.Contains(result.Body, []byte("[[PHONE_1]]")) {
			t.Fatalf("其余节点应照常替换：\n%s", result.Body)
		}
		if !result.Applied {
			t.Fatalf("有替换时 Applied 应为 true")
		}
	})
	t.Run("非 JSON body 原样", func(t *testing.T) {
		raw := []byte(`oops{`)
		result := SanitizeBody(policy, raw, FamilyOpenAIChat)
		if !bytes.Equal(result.Body, raw) || result.Applied || result.Skips != 0 {
			t.Fatalf("非 JSON body 应整体 fail-open: body=%s applied=%v skips=%d", result.Body, result.Applied, result.Skips)
		}
	})
	t.Run("顶层非 JSON 对象原样", func(t *testing.T) {
		raw := []byte(`[1,2]`)
		result := SanitizeBody(policy, raw, FamilyOpenAIChat)
		if !bytes.Equal(result.Body, raw) || result.Applied {
			t.Fatalf("顶层非对象应整体 fail-open: body=%s applied=%v", result.Body, result.Applied)
		}
	})
	t.Run("未知协议族原样", func(t *testing.T) {
		raw := []byte(`{"text":"13812345678"}`)
		result := SanitizeBody(policy, raw, "bogus")
		if !bytes.Equal(result.Body, raw) || result.Applied {
			t.Fatalf("未知协议族应整体 fail-open: body=%s applied=%v", result.Body, result.Applied)
		}
	})
}

func TestDigitLeaves(t *testing.T) {
	// 验收 13：工具参数数字叶子命中 → 字符串占位符 + 映射记录原始类型 number；
	// 未命中数字字节不动。
	policy := singleRule(t, phonePlaceholderRule())
	body := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"saveContact","arguments":"{\"phone\":13812345678,\"count\":12345,\"label\":\"备注 13812345678\"}"}}]}]}`)
	result := SanitizeBody(policy, body, FamilyOpenAIChat)
	wantArgs := `{"phone":"[[PHONE_1]]","count":12345,"label":"备注 [[PHONE_1]]"}`
	if !bytes.Contains(result.Body, []byte(`"arguments":`+mustJSONString(wantArgs))) {
		t.Fatalf("数字叶子替换结果不符：\n%s", result.Body)
	}
	entry, ok := result.Mapping.lookup("[[PHONE_1]]")
	if !ok || !entry.isNumber || entry.value != "13812345678" {
		t.Fatalf("映射应记录原始类型 number: %+v ok=%v", entry, ok)
	}
	if result.Hits["PHONE"] != 2 {
		t.Fatalf("PHONE 命中数应为 2，得到 %d", result.Hits["PHONE"])
	}
}

// TotalHits 是原始命中总数（防护次数口径，设计 §4.3.1）：每次实际替换 +1、
// 不去重；mask 与 placeholder 都计；fail-open 跳过不计；未命中为 0。
func TestSanitizeTotalHitsRawCount(t *testing.T) {
	t.Run("同值多次出现按次数累计", func(t *testing.T) {
		policy := singleRule(t, phonePlaceholderRule())
		body := []byte(`{"messages":[{"role":"user","content":"13812345678 然后 13812345678 然后 13812345678"}]}`)
		result := SanitizeBody(policy, body, FamilyOpenAIChat)
		if result.TotalHits != 3 {
			t.Fatalf("同值出现 3 次应计 3（不去重），得到 %d", result.TotalHits)
		}
		if result.Hits["PHONE"] != 3 {
			t.Fatalf("Hits 聚合应同为 3，得到 %d", result.Hits["PHONE"])
		}
	})
	t.Run("mask 与 placeholder 都计", func(t *testing.T) {
		policy := mustCompile(t, `{"rules":[
			{"id":"r1","type":"phone","action":"placeholder"},
			{"id":"r2","type":"email","action":"mask"}
		]}`)
		body := []byte(`{"messages":[{"role":"user","content":"打 13812345678，邮箱 someone@example.com 备用"}]}`)
		result := SanitizeBody(policy, body, FamilyOpenAIChat)
		if result.TotalHits != 2 {
			t.Fatalf("mask+placeholder 各一次应合计 2，得到 %d", result.TotalHits)
		}
		if result.Hits["PHONE"] != 1 || result.Hits["EMAIL"] != 1 {
			t.Fatalf("Hits 分桶不符: %v", result.Hits)
		}
	})
	t.Run("fail-open 跳过不计", func(t *testing.T) {
		policy := singleRule(t, phonePlaceholderRule())
		body := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"function":{"name":"f","arguments":"{broken"}}]},{"role":"user","content":"打 13812345678"}]}`)
		result := SanitizeBody(policy, body, FamilyOpenAIChat)
		if result.Skips != 1 {
			t.Fatalf("Skips 应为 1，得到 %d", result.Skips)
		}
		if result.TotalHits != 1 {
			t.Fatalf("fail-open 跳过不得计入 TotalHits（应为 1），得到 %d", result.TotalHits)
		}
	})
	t.Run("未命中与零开销路径为 0", func(t *testing.T) {
		policy := singleRule(t, phonePlaceholderRule())
		result := SanitizeBody(policy, []byte(`{"messages":[{"role":"user","content":"没有敏感值"}`+"}"), FamilyOpenAIChat)
		if result.TotalHits != 0 {
			t.Fatalf("未命中应为 0，得到 %d", result.TotalHits)
		}
		if empty := SanitizeBody(nil, []byte(`{"a":1}`), FamilyOpenAIChat); empty.TotalHits != 0 {
			t.Fatalf("空策略应为 0，得到 %d", empty.TotalHits)
		}
	})
}

func TestOpenAIResponsesFields(t *testing.T) {
	policy := singleRule(t, phonePlaceholderRule())
	body := []byte(`{"instructions":"你是助手","input":[{"role":"user","content":[{"type":"input_text","text":"我的手机号 13812345678"}]},{"type":"function_call_output","call_id":"c1","output":"{\"result\":\"回执 13912345678\"}"},{"type":"function_call_output","call_id":"c2","output":"纯文本 13712345678"}]}`)
	result := SanitizeBody(policy, body, FamilyOpenAIResponses)
	if result.Skips != 0 {
		t.Fatalf("字符串内容 output 不应计 skips，得到 %d", result.Skips)
	}
	if !bytes.Contains(result.Body, []byte(`"text":"我的手机号 [[PHONE_1]]"`)) {
		t.Fatalf("input[].content[].text 应被替换：\n%s", result.Body)
	}
	if !bytes.Contains(result.Body, []byte(`"output":`+mustJSONString(`{"result":"回执 [[PHONE_2]]"}`))) {
		t.Fatalf("function_call_output.output 内层 JSON 应被替换：\n%s", result.Body)
	}
	if !bytes.Contains(result.Body, []byte(`"output":`+mustJSONString(`纯文本 [[PHONE_3]]`))) {
		t.Fatalf("output 字符串内容应被替换：\n%s", result.Body)
	}
	if !bytes.Contains(result.Body, []byte(mustJSONString("你是助手\n\n"+PlaceholderGuidance))) {
		t.Fatalf("instructions 应追加注入文案：\n%s", result.Body)
	}
	restored := RewriteFull(result.Mapping, result.Body)
	for _, want := range []string{"13812345678", "13912345678", "13712345678"} {
		if !bytes.Contains(restored, []byte(want)) {
			t.Fatalf("还原后应含原文 %s：\n%s", want, restored)
		}
	}
}

func TestAnthropicFields(t *testing.T) {
	policy := singleRule(t, phonePlaceholderRule())
	body := []byte(`{"model":"claude-3","system":"系统提示","messages":[{"role":"assistant","content":[{"type":"text","text":"查询 13812345678"},{"type":"tool_use","id":"t1","name":"lookup","input":{"phone":13912345678,"note":"备注 13712345678"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"回执 13612345678"}]}]}`)
	result := SanitizeBody(policy, body, FamilyAnthropic)
	if !bytes.Contains(result.Body, []byte(mustJSONString("系统提示\n\n"+PlaceholderGuidance))) {
		t.Fatalf("system 字符串应追加注入文案：\n%s", result.Body)
	}
	if !bytes.Contains(result.Body, []byte(`"text":"查询 [[PHONE_1]]"`)) {
		t.Fatalf("text 块应被替换：\n%s", result.Body)
	}
	if !bytes.Contains(result.Body, []byte(`"input":{"phone":"[[PHONE_2]]","note":"备注 [[PHONE_3]]"}`)) {
		t.Fatalf("tool_use.input 应被替换：\n%s", result.Body)
	}
	entry, ok := result.Mapping.lookup("[[PHONE_2]]")
	if !ok || !entry.isNumber || entry.value != "13912345678" {
		t.Fatalf("input 数字叶子映射应记录 number: %+v ok=%v", entry, ok)
	}
	if !bytes.Contains(result.Body, []byte(`"content":"回执 [[PHONE_4]]"`)) {
		t.Fatalf("tool_result.content 应被替换：\n%s", result.Body)
	}
	if !bytes.Contains(result.Body, []byte(`"model":"claude-3"`)) {
		t.Fatalf("model 不应改动：\n%s", result.Body)
	}
	if result.Hits["PHONE"] != 4 {
		t.Fatalf("PHONE 命中数应为 4，得到 %d", result.Hits["PHONE"])
	}
}

func TestGeminiFields(t *testing.T) {
	policy := singleRule(t, phonePlaceholderRule())
	body := []byte(`{"systemInstruction":{"parts":[{"text":"系统"}]},"contents":[{"role":"user","parts":[{"text":"号码 13812345678"},{"functionCall":{"name":"f","args":{"tel":13912345678}}},{"functionResponse":{"name":"f","response":{"out":"回执 13712345678"}}}]}]}`)
	result := SanitizeBody(policy, body, FamilyGemini)
	if !bytes.Contains(result.Body, []byte(`"text":"号码 [[PHONE_1]]"`)) {
		t.Fatalf("parts[].text 应被替换：\n%s", result.Body)
	}
	if !bytes.Contains(result.Body, []byte(`"args":{"tel":"[[PHONE_2]]"}`)) {
		t.Fatalf("functionCall.args 数字叶子应被替换：\n%s", result.Body)
	}
	entry, ok := result.Mapping.lookup("[[PHONE_2]]")
	if !ok || !entry.isNumber || entry.value != "13912345678" {
		t.Fatalf("args 数字叶子映射应记录 number: %+v ok=%v", entry, ok)
	}
	if !bytes.Contains(result.Body, []byte(`"response":{"out":"回执 [[PHONE_3]]"}`)) {
		t.Fatalf("functionResponse.response 字符串叶子应被替换：\n%s", result.Body)
	}
	instruction := asObject(rootSystemInstruction(t, result.Body))
	parts := asArray(instruction.get("parts"))
	if len(parts) != 2 {
		t.Fatalf("systemInstruction.parts 应追加一个 part，得到 %d 个", len(parts))
	}
	if text, _ := asObject(parts[1]).get("text").(string); text != PlaceholderGuidance {
		t.Fatalf("追加 part 应为注入文案：%q", text)
	}
	if result.Hits["PHONE"] != 3 {
		t.Fatalf("PHONE 命中数应为 3，得到 %d", result.Hits["PHONE"])
	}
}

func rootSystemInstruction(t *testing.T, raw []byte) any {
	t.Helper()
	return parseBody(t, raw).get("systemInstruction")
}

func TestMediaFields(t *testing.T) {
	policy := singleRule(t, phonePlaceholderRule())
	body := []byte(`{"prompt":"画 13812345678","negative_prompt":"不要 13912345678","text":"补充 13712345678","input":{"prompt":"再来 13612345678"},"instances":[{"prompt":"第三 13512345678"}],"width":1024}`)
	result := SanitizeBody(policy, body, FamilyMedia)
	for _, field := range []string{
		`"prompt":"画 [[PHONE_1]]"`,
		`"negative_prompt":"不要 [[PHONE_2]]"`,
		`"text":"补充 [[PHONE_3]]"`,
		`"prompt":"再来 [[PHONE_4]]"`,
		`"prompt":"第三 [[PHONE_5]]"`,
	} {
		if !bytes.Contains(result.Body, []byte(field)) {
			t.Fatalf("media 字段替换缺失 %s：\n%s", field, result.Body)
		}
	}
	if !bytes.Contains(result.Body, []byte(`"width":1024`)) {
		t.Fatalf("未命中数字字节不应改动：\n%s", result.Body)
	}
	if bytes.Contains(result.Body, []byte("【隐私保护说明】")) {
		t.Fatalf("media 协议族不应注入：\n%s", result.Body)
	}
	if result.Hits["PHONE"] != 5 {
		t.Fatalf("PHONE 命中数应为 5，得到 %d", result.Hits["PHONE"])
	}
}

func TestChatSkippedFieldsUntouched(t *testing.T) {
	// messages[].name 自设计 §5 修订起属扫描字段（见 TestChatNameFieldScanned），
	// 不再列入本用例的不动清单。
	policy := singleRule(t, phonePlaceholderRule())
	body := []byte(`{"model":"gpt-4o-13812345678","metadata":{"trace":"13812345678"},"messages":[{"role":"user","content":[{"type":"text","text":"打 13812345678"},{"type":"image_url","image_url":{"url":"https://img.example/13812345678.png"}}],"tool_call_id":"tc_13812345678"}]}`)
	result := SanitizeBody(policy, body, FamilyOpenAIChat)
	for _, kept := range []string{
		`"model":"gpt-4o-13812345678"`,
		`"trace":"13812345678"`,
		`"url":"https://img.example/13812345678.png"`,
		`"tool_call_id":"tc_13812345678"`,
	} {
		if !bytes.Contains(result.Body, []byte(kept)) {
			t.Fatalf("清单外字段不应改动：%s\n%s", kept, result.Body)
		}
	}
	if !bytes.Contains(result.Body, []byte(`"text":"打 [[PHONE_1]]"`)) {
		t.Fatalf("text 部分应被替换：\n%s", result.Body)
	}
	if result.Hits["PHONE"] != 1 {
		t.Fatalf("只有 text 部分命中，得到 %v", result.Hits)
	}
}

func TestNumberingStableAcrossRequests(t *testing.T) {
	// 验收：编号跨请求稳定（前缀缓存契约 §2）。
	policy := singleRule(t, phonePlaceholderRule())
	body := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"13812345678 / 13912345678"}]}`)
	first := SanitizeBody(policy, body, FamilyOpenAIChat)
	second := SanitizeBody(policy, body, FamilyOpenAIChat)
	if !bytes.Equal(first.Body, second.Body) {
		t.Fatalf("同 body 两次 Sanitize 输出应逐字节一致：\n%s\n---\n%s", first.Body, second.Body)
	}
	e1, _ := first.Mapping.lookup("[[PHONE_1]]")
	e2, _ := second.Mapping.lookup("[[PHONE_2]]")
	if e1.value != "13812345678" || e2.value != "13912345678" {
		t.Fatalf("两次映射应一致: %+v / %+v", e1, e2)
	}
	if !bytes.Equal(RewriteFull(first.Mapping, first.Body), RewriteFull(second.Mapping, second.Body)) {
		t.Fatalf("两次还原结果应一致")
	}
}

func TestSanitizeUnchangedBodyKeepsOriginalBytes(t *testing.T) {
	// 无命中且无注入时返回原 body 字节（逐字节一致）。
	policy := singleRule(t, phoneMaskRule())
	body := []byte(`{"messages":[{"role":"user","content":"no sensitive here"}]}`)
	result := SanitizeBody(policy, body, FamilyOpenAIChat)
	if !bytes.Equal(result.Body, body) || result.Applied {
		t.Fatalf("无命中应返回原字节：body=%s applied=%v", result.Body, result.Applied)
	}
}

func TestChatNameFieldScanned(t *testing.T) {
	// 设计 §5：messages[].name 属扫描字段（「tool_call_id 之外的 name 字段」）；
	// tool_calls[].function.name 是工具名，明确不扫。
	policy := singleRule(t, phonePlaceholderRule())
	body := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","name":"用户13812345678","content":"hello"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get-13812345678","arguments":"{}"}}]}]}`)
	result := SanitizeBody(policy, body, FamilyOpenAIChat)
	root := parseBody(t, result.Body)
	messages := asArray(root.get("messages"))
	if len(messages) != 3 {
		t.Fatalf("消息数应保持 3，得到 %d", len(messages))
	}
	name, _ := asObject(messages[1]).get("name").(string)
	if name != "用户[[PHONE_1]]" {
		t.Fatalf("messages[].name 应被扫描替换：%q", name)
	}
	call := asObject(asArray(asObject(messages[2]).get("tool_calls"))[0])
	fnName, _ := asObject(call.get("function")).get("name").(string)
	if fnName != "get-13812345678" {
		t.Fatalf("tool_calls[].function.name 工具名不应改动：%q", fnName)
	}
	entry, ok := result.Mapping.lookup("[[PHONE_1]]")
	if !ok || entry.value != "13812345678" {
		t.Fatalf("映射应登记 name 中的原文: %+v ok=%v", entry, ok)
	}
	restored := RewriteFull(result.Mapping, result.Body)
	if !bytes.Contains(restored, []byte(`"name":"用户13812345678"`)) {
		t.Fatalf("还原后 name 应含原文手机号：\n%s", restored)
	}
}

func TestAnthropicThinkingBlockScanned(t *testing.T) {
	// 设计 §5 实现约束：客户端回传的 thinking 块思考文本同样属扫描对象。
	policy := singleRule(t, phonePlaceholderRule())
	body := []byte(`{"system":"sys","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"思考中 13812345678"},{"type":"text","text":"回答"}]}]}`)
	result := SanitizeBody(policy, body, FamilyAnthropic)
	root := parseBody(t, result.Body)
	content := asArray(asObject(asArray(root.get("messages"))[0]).get("content"))
	thinking, _ := asObject(content[0]).get("thinking").(string)
	if thinking != "思考中 [[PHONE_1]]" {
		t.Fatalf("thinking 块应被扫描替换：%q", thinking)
	}
	if text, _ := asObject(content[1]).get("text").(string); text != "回答" {
		t.Fatalf("text 块不应改动：%q", text)
	}
	if result.Hits["PHONE"] != 1 {
		t.Fatalf("PHONE 命中数应为 1，得到 %d", result.Hits["PHONE"])
	}
	restored := RewriteFull(result.Mapping, result.Body)
	if !bytes.Contains(restored, []byte(`"thinking":"思考中 13812345678"`)) {
		t.Fatalf("还原后 thinking 应含原文：\n%s", restored)
	}
}

func TestAnthropicRedactedThinkingDataUntouched(t *testing.T) {
	// 设计 §5：redacted_thinking 的 data 为密文字段，不含文本叶子——显式
	// 断言 data 原样不动（不脱敏）且不产生命中计数。
	policy := singleRule(t, phonePlaceholderRule())
	data := "eyJ0eXBlIjoiYmFzZTY0In0=13812345678cipher"
	body := []byte(`{"system":"sys","messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"` + data + `"}]}]}`)
	result := SanitizeBody(policy, body, FamilyAnthropic)
	root := parseBody(t, result.Body)
	content := asArray(asObject(asArray(root.get("messages"))[0]).get("content"))
	got, _ := asObject(content[0]).get("data").(string)
	if got != data {
		t.Fatalf("redacted_thinking data 应原样不动：\nwant=%q\ngot =%q", data, got)
	}
	if len(result.Hits) != 0 {
		t.Fatalf("redacted_thinking data 不得产生命中计数：%v", result.Hits)
	}
}
