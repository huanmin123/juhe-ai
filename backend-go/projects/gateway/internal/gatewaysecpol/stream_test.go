package gatewaysecpol

import (
	"bytes"
	"strings"
	"testing"
)

// 响应侧还原测试（设计 §7.1 白名单/类型保持 / §7.2 流式跨 chunk 与溢出）。

// newTestMapping 构造测试映射表：两个 string 条目（PHONE_1/2，钉 dedup）与一
// 个 number 条目（AMOUNT_1）。
func newTestMapping(t *testing.T) *Mapping {
	t.Helper()
	m := newMapping()
	if got := m.assign("PHONE", "13812345678", false); got != "[[PHONE_1]]" {
		t.Fatalf("首次编号应为 1，得到 %s", got)
	}
	if got := m.assign("PHONE", "13912345678", false); got != "[[PHONE_2]]" {
		t.Fatalf("第二个编号应为 2，得到 %s", got)
	}
	if got := m.assign("PHONE", "13812345678", false); got != "[[PHONE_1]]" {
		t.Fatalf("同一原文应复用同一编号，得到 %s", got)
	}
	if got := m.assign("AMOUNT", "123456789012345678", true); got != "[[AMOUNT_1]]" {
		t.Fatalf("AMOUNT 编号应为 1，得到 %s", got)
	}
	return m
}

func joinChunks(t *testing.T, r *StreamRewriter, chunks ...string) string {
	t.Helper()
	var out []byte
	for _, chunk := range chunks {
		out = append(out, r.Rewrite([]byte(chunk))...)
	}
	out = append(out, r.Flush()...)
	return string(out)
}

func TestStreamRewriteSingleChunk(t *testing.T) {
	r := NewStreamRewriter(newTestMapping(t))
	out := r.Rewrite([]byte("see [[PHONE_1]] and [[PHONE_2]] now"))
	if string(out) != "see 13812345678 and 13912345678 now" {
		t.Fatalf("完整 token 单 chunk 还原不符：%q", out)
	}
	if tail := r.Flush(); len(tail) != 0 {
		t.Fatalf("无残留时 Flush 应为空，得到 %q", tail)
	}
}

func TestStreamRewriteSplitChunks(t *testing.T) {
	// 验收：[[PH|ONE_1]] 跨 chunk 拆分还原成功（§7.2 核心机制）。
	r := NewStreamRewriter(newTestMapping(t))
	got := joinChunks(t, r, "hello [[PH", "ONE_1]] and [[PH", "ONE_2]]!")
	if got != "hello 13812345678 and 13912345678!" {
		t.Fatalf("跨 chunk 还原不符：%q", got)
	}
}

func TestStreamUnregisteredTokenPassthrough(t *testing.T) {
	// 验收 5：[[PHONE_99]] 不在映射表，原样透传。
	r := NewStreamRewriter(newTestMapping(t))
	got := r.Rewrite([]byte("工单 [[PHONE_99]] 与 [[PHONE_1]]"))
	if string(got) != "工单 [[PHONE_99]] 与 13812345678" {
		t.Fatalf("白名单外 token 应透传：%q", got)
	}
}

func TestStreamOverflowPassthrough(t *testing.T) {
	// §7.2 第 3 条：缓冲超限仍不完整 → 原样放行。
	r := NewStreamRewriter(newTestMapping(t))
	long := "[[" + strings.Repeat("a", 200)
	if got := string(r.Rewrite([]byte(long))); got != long {
		t.Fatalf("超限序列应原样放行：%q", got)
	}
	if got := string(r.Rewrite([]byte("]] tail"))); got != "]] tail" {
		t.Fatalf("超限后的尾随字节应原样放行：%q", got)
	}
	// 超限放行后新 token 照常还原。
	if got := string(r.Rewrite([]byte("[[PHONE_1]]"))); got != "13812345678" {
		t.Fatalf("超限后新 token 应还原：%q", got)
	}
}

func TestStreamTypePreservationQuotedNumber(t *testing.T) {
	// 验收 13：独占叶子 → 裸数字字面量；嵌在更长文本 → 文本还原。
	r := NewStreamRewriter(newTestMapping(t))
	body := `{"a":"[[AMOUNT_1]]","b":"x [[AMOUNT_1]] y","c":"[[PHONE_1]]"}`
	want := `{"a":123456789012345678,"b":"x 123456789012345678 y","c":"13812345678"}`
	if got := string(r.Rewrite([]byte(body))); got != want {
		t.Fatalf("类型保持还原不符：\nwant=%s\ngot =%s", want, got)
	}
}

func TestStreamSplitWithLeadingQuote(t *testing.T) {
	// 引号与 token 拆在两个 chunk（开引号悬挂一字节）。
	r := NewStreamRewriter(newTestMapping(t))
	got := joinChunks(t, r, `{"a":"`, `[[AMOUNT_1]]"}`)
	if got != `{"a":123456789012345678}` {
		t.Fatalf("跨 chunk 引号包裹还原不符：%q", got)
	}
}

func TestStreamSplitInsideQuotedToken(t *testing.T) {
	r := NewStreamRewriter(newTestMapping(t))
	got := joinChunks(t, r, `{"a":"[[AM`, `OUNT_1]]"}`)
	if got != `{"a":123456789012345678}` {
		t.Fatalf("引号内 token 拆分还原不符：%q", got)
	}
}

func TestStreamIncompleteNumberTokenHeldUntilBoundary(t *testing.T) {
	// number token 独占形态收尾引号未到 → 悬挂等待；终态冲刷按非独占处理
	//（引号保留、token 还原）。
	r := NewStreamRewriter(newTestMapping(t))
	if got := string(r.Rewrite([]byte(`"[[AMOUNT_1]]`))); got != "" {
		t.Fatalf("收尾引号未到应整体悬挂，得到 %q", got)
	}
	if got := string(r.Flush()); got != `"123456789012345678` {
		t.Fatalf("终态冲刷应保留原引号并还原 token：%q", got)
	}
}

func TestStreamTrailingPartialEmittedOnFlush(t *testing.T) {
	r := NewStreamRewriter(newTestMapping(t))
	if got := string(r.Rewrite([]byte("tail ["))); got != "tail " {
		t.Fatalf("悬挂前缀应放行：%q", got)
	}
	if got := string(r.Flush()); got != "[" {
		t.Fatalf("终态应冲刷残留缓冲：%q", got)
	}
}

func TestStreamTailQuoteReleasedNextChunk(t *testing.T) {
	// 尾部孤立引号扣发一字节，下一 chunk 无 [[ 时原样放行。
	r := NewStreamRewriter(newTestMapping(t))
	if got := string(r.Rewrite([]byte(`he said "hi"`))); got != `he said "hi` {
		t.Fatalf("尾部引号应扣发：%q", got)
	}
	if got := string(r.Rewrite([]byte(" again"))); got != `" again` {
		t.Fatalf("下一 chunk 应先放行扣发引号：%q", got)
	}
}

func TestStreamNilAndEmptyMappingPassthrough(t *testing.T) {
	nilRewriter := NewStreamRewriter(nil)
	if got := string(nilRewriter.Rewrite([]byte("abc [[PHONE_1]]"))); got != "abc [[PHONE_1]]" {
		t.Fatalf("nil 映射应原样透传：%q", got)
	}
	if got := nilRewriter.Flush(); got != nil {
		t.Fatalf("nil 映射 Flush 应为空，得到 %q", got)
	}
	emptyRewriter := NewStreamRewriter(&Mapping{})
	if got := string(emptyRewriter.Rewrite([]byte("abc"))); got != "abc" {
		t.Fatalf("空映射应原样透传：%q", got)
	}
}

func TestStreamEndToEndWithSanitizeMapping(t *testing.T) {
	// SanitizeBody 产出的映射直接供响应侧还原（同一请求链）。
	policy := singleRule(t, phonePlaceholderRule())
	result := SanitizeBody(policy, []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"打 13812345678"}]}`), FamilyOpenAIChat)
	r := NewStreamRewriter(result.Mapping)
	got := joinChunks(t, r, "确认 [[PH", "ONE_1]] 已收到")
	if got != "确认 13812345678 已收到" {
		t.Fatalf("请求映射应还原响应占位符：%q", got)
	}
}

func TestRewriteFull(t *testing.T) {
	m := newTestMapping(t)
	body := []byte(`前文 [[PHONE_2]] 中段 {"a":"[[AMOUNT_1]]","b":"see [[PHONE_1]] ok"} 尾 [[PHONE_99]]`)
	want := `前文 13912345678 中段 {"a":123456789012345678,"b":"see 13812345678 ok"} 尾 [[PHONE_99]]`
	if got := string(RewriteFull(m, body)); got != want {
		t.Fatalf("RewriteFull 还原不符：\nwant=%s\ngot =%s", want, got)
	}
	if got := string(RewriteFull(nil, body)); got != string(body) {
		t.Fatalf("nil 映射应原样返回")
	}
	if got := string(RewriteFull(newMapping(), body)); got != string(body) {
		t.Fatalf("空映射应原样返回")
	}
}

func TestRewriteFullDigitLeafRoundTrip(t *testing.T) {
	// 验收 13 端到端：请求侧数字叶子 number → string；响应侧独占叶子还原为
	// 裸数字字面量。
	policy := singleRule(t, phonePlaceholderRule())
	sanitized := SanitizeBody(policy, []byte(`{"messages":[{"role":"assistant","tool_calls":[{"function":{"name":"f","arguments":"{\"phone\":13812345678}"}}]}]}`), FamilyOpenAIChat)
	if !bytes.Contains(sanitized.Body, []byte(`"arguments":`+mustJSONString(`{"phone":"[[PHONE_1]]"}`))) {
		t.Fatalf("请求侧数字叶子应替换为字符串占位符：\n%s", sanitized.Body)
	}
	restored := RewriteFull(sanitized.Mapping, []byte(`{"phone":"[[PHONE_1]]"}`))
	if string(restored) != `{"phone":13812345678}` {
		t.Fatalf("独占叶子应还原为裸数字字面量：%s", restored)
	}
}

func TestStreamEscapedNumberLeafBareLiteral(t *testing.T) {
	// 批C：JSON 字符串内层转义形态（工具参数 arguments 在 SSE/非流式响应中的
	// 真实字节形态 \"TOKEN\"）的独占 number 叶子 → 裸数字字面量；转义引号与
	// 反斜杠一并消费，外层字符串解析后即得合法内层 JSON。
	r := NewStreamRewriter(newTestMapping(t))
	body := `{"arguments":"{\"phone\":\"[[AMOUNT_1]]\"}"}`
	want := `{"arguments":"{\"phone\":123456789012345678}"}`
	if got := string(r.Rewrite([]byte(body))); got != want {
		t.Fatalf("转义独占叶子应还原为裸数字字面量：\nwant=%s\ngot =%s", want, got)
	}
}

func TestStreamEscapedNumberLeafSplit(t *testing.T) {
	r := NewStreamRewriter(newTestMapping(t))
	got := joinChunks(t, r, `{"a":"{\"x\":\"`, `[[AMOUNT_1]]\"}`, `"}{"tail":1}`)
	want := `{"a":"{\"x\":123456789012345678}"}{"tail":1}`
	if got != want {
		t.Fatalf("转义叶子跨 chunk 还原不符：\nwant=%s\ngot =%s", want, got)
	}
}

func TestStreamEscapedStringLeafKeepsQuotes(t *testing.T) {
	// string 类型叶子在转义形态中只还原 token，引号保留（文本语义不变）。
	r := NewStreamRewriter(newTestMapping(t))
	body := `{"arguments":"{\"phone\":\"[[PHONE_1]]\"}"}`
	want := `{"arguments":"{\"phone\":\"13812345678\"}"}`
	if got := string(r.Rewrite([]byte(body))); got != want {
		t.Fatalf("转义 string 叶子应保留引号：\nwant=%s\ngot =%s", want, got)
	}
}

func TestRewriteFullEscapedNumberLeaf(t *testing.T) {
	// 批C：非流式工具参数 arguments 的转义独占 number 叶子 → 裸数字字面量。
	m := newTestMapping(t)
	body := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"arguments":"{\"amount\":\"[[AMOUNT_1]]\"}"}}]}}]}`)
	want := `{"choices":[{"message":{"tool_calls":[{"function":{"arguments":"{\"amount\":123456789012345678}"}}]}}]}`
	if got := string(RewriteFull(m, body)); got != want {
		t.Fatalf("RewriteFull 转义叶子还原不符：\nwant=%s\ngot =%s", want, got)
	}
}
