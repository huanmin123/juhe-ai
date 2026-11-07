package gatewaysecpol

import (
	"bytes"
	"testing"
)

// 注入形态测试（设计 §6.2 各协议追加位置；注入触发为静态判断）。

func runPlaceholderPolicy(t *testing.T, body []byte, family string) SanitizeResult {
	t.Helper()
	return SanitizeBody(singleRule(t, phonePlaceholderRule()), body, family)
}

func TestInjectChat(t *testing.T) {
	t.Run("无 system 消息首位插入", func(t *testing.T) {
		result := runPlaceholderPolicy(t, []byte(`{"messages":[{"role":"user","content":"hello"}]}`), FamilyOpenAIChat)
		root := parseBody(t, result.Body)
		messages := asArray(root.get("messages"))
		if len(messages) != 2 {
			t.Fatalf("应插入一条 system 消息，得到 %d 条", len(messages))
		}
		first := asObject(messages[0])
		if role, _ := first.get("role").(string); role != "system" {
			t.Fatalf("首位应为 system，得到 %q", role)
		}
		if content, _ := first.get("content").(string); content != PlaceholderGuidance {
			t.Fatalf("插入内容应为注入文案：%q", content)
		}
		if content, _ := asObject(messages[1]).get("content").(string); content != "hello" {
			t.Fatalf("user 消息不应改动：%q", content)
		}
		if !result.Applied {
			t.Fatalf("注入发生时 Applied 应为 true")
		}
	})
	t.Run("有 system 字符串末尾追加", func(t *testing.T) {
		result := runPlaceholderPolicy(t, []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"}]}`), FamilyOpenAIChat)
		root := parseBody(t, result.Body)
		messages := asArray(root.get("messages"))
		if len(messages) != 2 {
			t.Fatalf("不应新插入消息，得到 %d 条", len(messages))
		}
		content, _ := asObject(messages[0]).get("content").(string)
		if content != "sys\n\n"+PlaceholderGuidance {
			t.Fatalf("应追加到首个 system 文本末尾：%q", content)
		}
	})
	t.Run("system content 数组追加 text part", func(t *testing.T) {
		result := runPlaceholderPolicy(t, []byte(`{"messages":[{"role":"system","content":[{"type":"text","text":"sys"}]}]}`), FamilyOpenAIChat)
		root := parseBody(t, result.Body)
		content := asArray(asObject(asArray(root.get("messages"))[0]).get("content"))
		if len(content) != 2 {
			t.Fatalf("应追加一个 text part，得到 %d 个", len(content))
		}
		if text, _ := asObject(content[0]).get("text").(string); text != "sys" {
			t.Fatalf("既有 part 不应改动：%q", text)
		}
		appended := asObject(content[1])
		if partType, _ := appended.get("type").(string); partType != "text" {
			t.Fatalf("追加 part type 应为 text，得到 %q", partType)
		}
		if text, _ := appended.get("text").(string); text != PlaceholderGuidance {
			t.Fatalf("追加 part text 应为注入文案：%q", text)
		}
	})
	t.Run("messages 缺失则创建", func(t *testing.T) {
		result := runPlaceholderPolicy(t, []byte(`{"model":"gpt-4o"}`), FamilyOpenAIChat)
		root := parseBody(t, result.Body)
		messages := asArray(root.get("messages"))
		if len(messages) != 1 {
			t.Fatalf("应创建含一条 system 的 messages，得到 %d 条", len(messages))
		}
		if role, _ := asObject(messages[0]).get("role").(string); role != "system" {
			t.Fatalf("创建消息应为 system")
		}
	})
}

func TestInjectOpenAIResponses(t *testing.T) {
	t.Run("instructions 末尾追加", func(t *testing.T) {
		result := runPlaceholderPolicy(t, []byte(`{"instructions":"你是助手"}`), FamilyOpenAIResponses)
		instructions, _ := parseBody(t, result.Body).get("instructions").(string)
		if instructions != "你是助手\n\n"+PlaceholderGuidance {
			t.Fatalf("instructions 应追加注入文案：%q", instructions)
		}
	})
	t.Run("instructions 缺失则创建", func(t *testing.T) {
		result := runPlaceholderPolicy(t, []byte(`{"model":"gpt-4o"}`), FamilyOpenAIResponses)
		instructions, _ := parseBody(t, result.Body).get("instructions").(string)
		if instructions != PlaceholderGuidance {
			t.Fatalf("instructions 应创建为注入文案：%q", instructions)
		}
	})
}

func TestInjectAnthropic(t *testing.T) {
	t.Run("system 字符串追加", func(t *testing.T) {
		result := runPlaceholderPolicy(t, []byte(`{"system":"sys"}`), FamilyAnthropic)
		system, _ := parseBody(t, result.Body).get("system").(string)
		if system != "sys\n\n"+PlaceholderGuidance {
			t.Fatalf("system 应追加注入文案：%q", system)
		}
	})
	t.Run("blocks 文本级追加到最后一个 text 块", func(t *testing.T) {
		result := runPlaceholderPolicy(t, []byte(`{"system":[{"type":"text","text":"第一段"},{"type":"text","text":"第二段"}]}`), FamilyAnthropic)
		blocks := asArray(parseBody(t, result.Body).get("system"))
		if len(blocks) != 2 {
			t.Fatalf("文本级追加不应新建块，得到 %d 块", len(blocks))
		}
		if text, _ := asObject(blocks[0]).get("text").(string); text != "第一段" {
			t.Fatalf("第一个 text 块不应改动：%q", text)
		}
		if text, _ := asObject(blocks[1]).get("text").(string); text != "第二段\n\n"+PlaceholderGuidance {
			t.Fatalf("应追加到最后一个 text 块文本末尾：%q", text)
		}
	})
	t.Run("无 text 块新建单 text 块且无 cache_control", func(t *testing.T) {
		result := runPlaceholderPolicy(t, []byte(`{"system":[{"type":"image","source":{"type":"base64"}}]}`), FamilyAnthropic)
		blocks := asArray(parseBody(t, result.Body).get("system"))
		if len(blocks) != 2 {
			t.Fatalf("应新建单 text 块，得到 %d 块", len(blocks))
		}
		created := asObject(blocks[1])
		if blockType, _ := created.get("type").(string); blockType != "text" {
			t.Fatalf("新建块应为 text，得到 %q", blockType)
		}
		if text, _ := created.get("text").(string); text != PlaceholderGuidance {
			t.Fatalf("新建块文本应为注入文案：%q", text)
		}
		if created.has("cache_control") {
			t.Fatalf("新建块不应设置 cache_control（§6.2 缓存断点兼容）")
		}
	})
	t.Run("system 缺失创建字符串", func(t *testing.T) {
		result := runPlaceholderPolicy(t, []byte(`{"messages":[]}`), FamilyAnthropic)
		system, _ := parseBody(t, result.Body).get("system").(string)
		if system != PlaceholderGuidance {
			t.Fatalf("system 应创建为注入文案：%q", system)
		}
	})
}

func TestInjectGemini(t *testing.T) {
	t.Run("parts 追加一个 text part", func(t *testing.T) {
		result := runPlaceholderPolicy(t, []byte(`{"systemInstruction":{"parts":[{"text":"sys"}]}}`), FamilyGemini)
		instruction := asObject(parseBody(t, result.Body).get("systemInstruction"))
		parts := asArray(instruction.get("parts"))
		if len(parts) != 2 {
			t.Fatalf("应追加一个 part，得到 %d 个", len(parts))
		}
		if text, _ := asObject(parts[0]).get("text").(string); text != "sys" {
			t.Fatalf("既有 part 不应改动：%q", text)
		}
		if text, _ := asObject(parts[1]).get("text").(string); text != PlaceholderGuidance {
			t.Fatalf("追加 part 应为注入文案：%q", text)
		}
	})
	t.Run("systemInstruction 缺失则创建", func(t *testing.T) {
		result := runPlaceholderPolicy(t, []byte(`{"contents":[]}`), FamilyGemini)
		instruction := asObject(parseBody(t, result.Body).get("systemInstruction"))
		if instruction == nil {
			t.Fatalf("应创建 systemInstruction")
		}
		parts := asArray(instruction.get("parts"))
		if len(parts) != 1 {
			t.Fatalf("应创建含单个 part 的 parts，得到 %d 个", len(parts))
		}
		if text, _ := asObject(parts[0]).get("text").(string); text != PlaceholderGuidance {
			t.Fatalf("创建 part 应为注入文案：%q", text)
		}
	})
}

func TestMediaNoInject(t *testing.T) {
	result := runPlaceholderPolicy(t, []byte(`{"prompt":"画 13812345678"}`), FamilyMedia)
	if bytes.Contains(result.Body, []byte("【隐私保护说明】")) {
		t.Fatalf("media 协议族不注入（§6.2）：\n%s", result.Body)
	}
	if !bytes.Contains(result.Body, []byte("[[PHONE_1]]")) {
		t.Fatalf("替换仍应发生：\n%s", result.Body)
	}
}
