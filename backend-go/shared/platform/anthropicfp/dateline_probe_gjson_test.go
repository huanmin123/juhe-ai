package anthropicfp

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

// 本文件是 gjson v1.20.1 定位能力探针：NormalizeDateline 的字节级改写依赖
// 「Result.Index 指向 body 内原始 token 的开引号字节偏移、Result.Raw 为含
// 引号的原始编码文本」这一 gjson 版本行为契约。探针钉住该契约——嵌套路径、
// 数组下标、ForEvery 链式 Get、重复键、既有转义、缩进空白六类场景下，
// body[Index:Index+len(Raw)] 必须与 Raw 逐字节一致（token 完整恢复）。
// gjson 升级若改变该行为，本文件测试失败即为升级审查信号。

// probeTokenRecovery 断言一个字符串型 Result 可以从 body 中按 Index 完整
// 恢复原始 token，且 token 首尾都是引号（引号内内容即改写行走范围）。
func probeTokenRecovery(t *testing.T, body []byte, res gjson.Result, label string) {
	t.Helper()
	if res.Type != gjson.String {
		t.Fatalf("%s: Type = %v, want String", label, res.Type)
	}
	if res.Index <= 0 {
		t.Fatalf("%s: Index = %d, want > 0（指针差值定位失败）", label, res.Index)
	}
	if len(res.Raw) < 2 || res.Raw[0] != '"' || res.Raw[len(res.Raw)-1] != '"' {
		t.Fatalf("%s: Raw 缺引号: %q", label, res.Raw)
	}
	tokenEnd := res.Index + len(res.Raw)
	if tokenEnd > len(body) {
		t.Fatalf("%s: token 越界 Index=%d len(Raw)=%d len(body)=%d", label, res.Index, len(res.Raw), len(body))
	}
	if body[res.Index] != '"' || body[tokenEnd-1] != '"' {
		t.Fatalf("%s: body[Index..] 首尾不是引号: %q", label, body[res.Index:min(tokenEnd, res.Index+32)])
	}
	if !bytes.Equal(body[res.Index:tokenEnd], []byte(res.Raw)) {
		t.Fatalf("%s: token 恢复不一致:\nbody = %q\nRaw  = %q", label,
			body[res.Index:tokenEnd], res.Raw)
	}
}

// TestProbeGjsonIndexNestedPaths 覆盖 dateline 扫描实际使用的四类路径：
// 顶层字符串（system）、对象数组内字段（system.0.text）、消息字符串
// content（messages.0.content）与消息数组 content 内字段
// （messages.1.content.2.text）。
func TestProbeGjsonIndexNestedPaths(t *testing.T) {
	// 顶层字符串 system + messages 两种 content 形态共用一个体。
	body := []byte(`{"model":"claude","system":"sys-text",` +
		`"messages":[` +
		`{"role":"user","content":"msg0-content"},` +
		`{"role":"user","content":[{"type":"text","text":"block0"},{"type":"image"},{"type":"text","text":"block2"}]}` +
		`]}`)
	for _, tc := range []struct{ path, wantDecoded string }{
		{"system", "sys-text"},
		{"messages.0.content", "msg0-content"},
		{"messages.1.content.2.text", "block2"}, // 前面有非字符串块
	} {
		res := gjson.GetBytes(body, tc.path)
		probeTokenRecovery(t, body, res, tc.path)
		if res.Str != tc.wantDecoded {
			t.Fatalf("%s: Str = %q, want %q", tc.path, res.Str, tc.wantDecoded)
		}
	}

	// system 为对象数组形态时的下标路径（system 是数组，须独立体）。
	arrayBody := []byte(`{"model":"claude","system":[` +
		`{"type":"text","text":"block0"},{"type":"image"},{"type":"text","text":"block2"}` +
		`],"messages":[]}`)
	for _, tc := range []struct{ path, wantDecoded string }{
		{"system.0.text", "block0"}, // 首块前的 "type" 字段含引号，验证跨 token 偏移
		{"system.2.text", "block2"}, // 数组内非字符串块之后
	} {
		res := gjson.GetBytes(arrayBody, tc.path)
		probeTokenRecovery(t, arrayBody, res, tc.path)
		if res.Str != tc.wantDecoded {
			t.Fatalf("%s: Str = %q, want %q", tc.path, res.Str, tc.wantDecoded)
		}
	}
}

// TestProbeGjsonIndexForEachChainedGet 探针 ForEach 遍历 + 链式 Get 的
// Index 是否仍为 body 绝对偏移（NormalizeDateline 实际定位方式：
// messages.ForEach → message.Get("content") → content.ForEach →
// block.Get("text")）。
func TestProbeGjsonIndexForEachChainedGet(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"user","content":"plain-content"},` +
		`{"role":"user","content":[{"type":"text"},{"type":"text","text":"deep-text"}]}` +
		`]}`)
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		t.Fatalf("messages.IsArray() = false")
	}
	found := 0
	messages.ForEach(func(_, message gjson.Result) bool {
		content := message.Get("content")
		switch {
		case content.Type == gjson.String:
			probeTokenRecovery(t, body, content, "messages[i].content(string)")
			found++
		case content.IsArray():
			blockIndex := -1
			content.ForEach(func(_, block gjson.Result) bool {
				blockIndex++
				text := block.Get("text")
				if text.Exists() && text.Type == gjson.String {
					probeTokenRecovery(t, body, text,
						"messages[i].content[j].text（链式 ForEach）")
					found++
				}
				return true
			})
		}
		return true
	})
	if found != 2 {
		t.Fatalf("探针命中 %d 个字符串字段, want 2", found)
	}
}

// TestProbeGjsonIndexDuplicateKeys 探针重复键（2026-11-07 实测）：gjson
// v1.20.1 简单路径取**首次出现**的值，且 Index 指向同一次出现的 token。
// 重复键在 JSON 中属未定义行为（真实客户端不产生）；本实现按 Index 原地
// 改写「读取到的那个 token」，读哪个改哪个，不存在读写分叉。
func TestProbeGjsonIndexDuplicateKeys(t *testing.T) {
	body := []byte(`{"system":"first-occurrence","messages":[],"system":"second-occurrence"}`)
	res := gjson.GetBytes(body, "system")
	probeTokenRecovery(t, body, res, "system(重复键)")
	if res.Str != "first-occurrence" {
		t.Fatalf("重复键取值 = %q, want first-occurrence（gjson 首次出现语义变化，需同步审查改写定位）", res.Str)
	}
	if !bytes.Contains(body[res.Index:res.Index+len(res.Raw)], []byte("first-occurrence")) {
		t.Fatalf("Index 指向的 token 不是返回值所在出现: %q", body[res.Index:res.Index+len(res.Raw)])
	}

	// 块内重复 "text" 键同口径（首次出现）。
	body2 := []byte(`{"system":[{"type":"text","text":"text-first","text":"text-second"}]}`)
	res2 := gjson.GetBytes(body2, "system.0.text")
	probeTokenRecovery(t, body2, res2, "system.0.text(重复键)")
	if res2.Str != "text-first" {
		t.Fatalf("块内重复键取值 = %q, want text-first", res2.Str)
	}
}

// TestProbeGjsonIndexEscapedContent 探针含既有转义的字段：Raw 保留原始
// 转义字节（\uXXXX 不展开），Str 为解码文本，Index 仍指向开引号。
func TestProbeGjsonIndexEscapedContent(t *testing.T) {
	body := []byte(`{"system":"a\"b\\c\u4e2d<e>&f"}`)
	res := gjson.GetBytes(body, "system")
	probeTokenRecovery(t, body, res, "system(转义)")
	if string(body[res.Index:res.Index+len(res.Raw)]) != `"a\"b\\c\u4e2d<e>&f"` {
		t.Fatalf("Raw 未保留原始转义字节: %q", body[res.Index:res.Index+len(res.Raw)])
	}
	if res.Str != "a\"b\\c中<e>&f" {
		t.Fatalf("Str 解码 = %q, want %q", res.Str, "a\"b\\c中<e>&f")
	}
}

// TestProbeGjsonIndexIndented 探针缩进/空白体：token 定位不受格式化空白
// 影响（生产 body 是紧凑形态，此探针保证定位逻辑对任意 JSON 形态稳健）。
func TestProbeGjsonIndexIndented(t *testing.T) {
	body := []byte("{\n  \"system\": [\n    {\"type\": \"text\", \"text\": \"indented\"}\n  ]\n}")
	res := gjson.GetBytes(body, "system.0.text")
	probeTokenRecovery(t, body, res, "system.0.text(缩进)")
	if res.Str != "indented" {
		t.Fatalf("Str = %q", res.Str)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
