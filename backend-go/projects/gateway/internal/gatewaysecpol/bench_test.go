package gatewaysecpol

// 性能基准（设计《API Key 安全策略与出站内容脱敏设计》§5 预算：
// 1MB body、50 条规则、单核，SanitizeBody 增加耗时应 < 50ms）。
// 运行：go test -bench=. -benchmem -run '^$' ./internal/gatewaysecpol/

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// secBenchRulesJSON 构造 50 条规则的策略：6 内置类型 + 22 custom_regex + 22 custom_keywords。
// action 由参数控制（placeholder 覆盖注入与映射路径；mask 覆盖纯打码路径）。
func secBenchRulesJSON(action string) string {
	var b strings.Builder
	b.WriteString(`{"rules":[`)
	builtin := []struct{ id, label, typ string }{
		{"b1", "PHONE", "phone"},
		{"b2", "ID_CARD", "id_card"},
		{"b3", "BANK_CARD", "bank_card"},
		{"b4", "EMAIL", "email"},
		{"b5", "IPV4", "ipv4"},
		{"b6", "SECRET", "secret"},
	}
	first := true
	add := func(s string) {
		if !first {
			b.WriteString(",")
		}
		first = false
		b.WriteString(s)
	}
	for _, r := range builtin {
		add(fmt.Sprintf(`{"id":%q,"label":%q,"type":%q,"action":%q}`, r.id, r.label, r.typ, action))
	}
	for i := 0; i < 22; i++ {
		pattern := fmt.Sprintf(`"PROJECT-%03d-[A-Z]{4}\\d{3}"`, i)
		add(fmt.Sprintf(`{"id":"rx%d","label":"PROJ%d","type":"custom_regex","action":%q,"pattern":%s}`, i, i, action, pattern))
	}
	for i := 0; i < 22; i++ {
		kw := fmt.Sprintf(`"机密词条%03d号"`, i)
		add(fmt.Sprintf(`{"id":"kw%d","label":"KEYWORD%d","type":"custom_keywords","action":%q,"keywords":[%s]}`, i, i, action, kw))
	}
	b.WriteString(`]}`)
	return b.String()
}

// secBenchParagraph 构造一段无敏感值的混合中英文正文（约 targetBytes 字节）。
func secBenchParagraph(targetBytes int) string {
	base := "这是一段用于性能基准的业务正文，包含订单号 ORD-2026-0001 与交付说明。The deployment window is confirmed; please review the runbook before Friday. "
	var b strings.Builder
	for b.Len() < targetBytes {
		b.WriteString(base)
	}
	return b.String()[:targetBytes]
}

// secBenchChatBody 构造 chat 请求 body：约 totalBytes 的 messages 数组。
// phoneEvery > 0 时每 phoneEvery 字节插入一个大陆手机号（占位 11 字节，长度近似不变）。
func secBenchChatBody(totalBytes, phoneEvery int) []byte {
	const chunk = 4096
	var b strings.Builder
	b.WriteString(`{"model":"gpt-5.5","stream":true,"messages":[`)
	remaining := totalBytes
	index := 0
	for remaining > 200 {
		size := chunk
		if size > remaining-200 {
			size = remaining - 200
		}
		text := secBenchParagraph(size)
		if phoneEvery > 0 {
			// 号码前后包中文标点，保证 \b 词边界成立（贴字母数字属防误报边界，不命中）。
			insert := []rune("；13800138000；")
			runes := []rune(text)
			step := phoneEvery
			if step <= 13 || step >= len(runes) {
				step = len(runes) / 2
			}
			for pos := step; pos+len(insert) <= len(runes); pos += step {
				copy(runes[pos:pos+len(insert)], insert)
			}
			text = string(runes)
		}
		if index > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"role":"user","content":`)
		b.WriteString(strconv.Quote(text))
		b.WriteString(`}`)
		remaining -= size + 60
		index++
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func BenchmarkCompileRules50(b *testing.B) {
	rules := secBenchRulesJSON("placeholder")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Compile(rules); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSanitizeBody1MB_Rules50_NoHit(b *testing.B) {
	policy, err := Compile(secBenchRulesJSON("placeholder"))
	if err != nil {
		b.Fatal(err)
	}
	body := secBenchChatBody(1<<20, 0)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := SanitizeBody(policy, body, FamilyOpenAIChat)
		if result.Applied {
			b.Fatal("无命中场景不应发生替换")
		}
	}
}

func BenchmarkSanitizeBody1MB_Rules50_Hits(b *testing.B) {
	policy, err := Compile(secBenchRulesJSON("placeholder"))
	if err != nil {
		b.Fatal(err)
	}
	body := secBenchChatBody(1<<20, 2048) // ≈512 个手机号
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := SanitizeBody(policy, body, FamilyOpenAIChat)
		if !result.Applied || result.Hits["PHONE"] == 0 {
			b.Fatal("命中场景未生效")
		}
	}
}

func BenchmarkSanitizeBody16KB_Rules50_Typical(b *testing.B) {
	policy, err := Compile(secBenchRulesJSON("placeholder"))
	if err != nil {
		b.Fatal(err)
	}
	body := secBenchChatBody(16<<10, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SanitizeBody(policy, body, FamilyOpenAIChat)
	}
}

func BenchmarkStreamRewrite1MB_Rewritten(b *testing.B) {
	policy, err := Compile(secBenchRulesJSON("placeholder"))
	if err != nil {
		b.Fatal(err)
	}
	body := secBenchChatBody(64<<10, 1024)
	result := SanitizeBody(policy, body, FamilyOpenAIChat)
	if result.Mapping == nil || result.Mapping.Empty() {
		b.Fatal("前置条件失败：映射表为空")
	}
	chunk := make([]byte, 8192)
	for i := range chunk {
		chunk[i] = 'x'
	}
	copy(chunk[100:], []byte("来电 [[PHONE_1]] 已登记"))
	total := (1 << 20) / len(chunk) // 1MB 响应 = 128 个 8KB chunk
	b.SetBytes(int64(total * len(chunk)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rewriter := NewStreamRewriter(result.Mapping)
		for j := 0; j < total; j++ {
			_ = rewriter.Rewrite(chunk)
		}
		_ = rewriter.Flush()
	}
}

func BenchmarkRewriteFull1MB(b *testing.B) {
	policy, err := Compile(secBenchRulesJSON("placeholder"))
	if err != nil {
		b.Fatal(err)
	}
	body := secBenchChatBody(1<<20, 2048)
	result := SanitizeBody(policy, body, FamilyOpenAIChat)
	if result.Mapping == nil || result.Mapping.Empty() {
		b.Fatal("前置条件失败：映射表为空")
	}
	sanitized := result.Body
	b.SetBytes(int64(len(sanitized)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = RewriteFull(result.Mapping, sanitized)
	}
}

// 编译期防呆：确保正则形态与实现一致（与运行时 Compile 校验同源）。
var _ = regexp.MustCompile(`PROJECT-\d{3}`)
