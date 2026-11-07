package anthropicfp

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

// 撇号变体码点（取证集合）与规范句样本。
const (
	apostropheU2019 = "\u2019" // ’ RIGHT SINGLE QUOTATION MARK
	apostropheU02BC = "\u02bc" // ʼ MODIFIER LETTER APOSTROPHE
	apostropheU02B9 = "\u02b9" // ʹ MODIFIER LETTER PRIME
	apostropheASCII = "'"
)

// TestNormalizeTextVariantSeparatorMatrix 覆盖三变体撇号 × 两分隔符的
// 全组合：非规范句归一化为 ASCII 撇号 + 连字符并记命中；ASCII 撇号 +
// 连字符（已规范）不改写、不记命中。
func TestNormalizeTextVariantSeparatorMatrix(t *testing.T) {
	variants := []struct{ name, apo string }{
		{"u2019", apostropheU2019},
		{"u02bc", apostropheU02BC},
		{"u02b9", apostropheU02B9},
		{"ascii", apostropheASCII},
	}
	separators := []struct{ name, sep string }{
		{"hyphen", "-"},
		{"slash", "/"},
	}
	for _, variant := range variants {
		for _, separator := range separators {
			t.Run(variant.name+"_"+separator.name, func(t *testing.T) {
				sentence := "Today" + variant.apo + "s date is 2026" + separator.sep + "07" + separator.sep + "01."
				canonical := "Today's date is 2026-07-01."
				isCanonical := variant.name == "ascii" && separator.name == "hyphen"

				got, hits := NormalizeText(sentence)
				if isCanonical {
					if got != sentence || hits != nil {
						t.Fatalf("已规范句被改写：got=%q hits=%v", got, hits)
					}
					return
				}
				if got != canonical {
					t.Fatalf("归一化输出 = %q, want %q", got, canonical)
				}
				if len(hits) != 1 || hits[0].Variant != variant.name || hits[0].Separator != separator.sep {
					t.Fatalf("命中记录 = %v, want [{%s %s}]", hits, variant.name, separator.sep)
				}
			})
		}
	}
}

// TestNormalizeDatelineSystemString：system 字符串形态全文清洗，句子外
// 字节不动，且不做整包重序列化（键序保持原样）。
func TestNormalizeDatelineSystemString(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4","system":"You are Claude. Today` + apostropheU2019 + `s date is 2026-07-01.","max_tokens":16}`)

	out, hits, changed := NormalizeDateline(body)
	if !changed {
		t.Fatalf("changed = false, want true")
	}
	if len(hits) != 1 || hits[0].Variant != "u2019" || hits[0].Separator != "-" {
		t.Fatalf("命中记录 = %v, want [{u2019 -}]", hits)
	}
	if got := gjson.GetBytes(out, "system").String(); got != "You are Claude. Today's date is 2026-07-01." {
		t.Fatalf("system = %q", got)
	}
	// 键序与未触碰字段逐字节保留（encoding/json 重序列化会按字母排序键）。
	if !bytes.HasPrefix(out, []byte(`{"model":"claude-sonnet-4","system":`)) {
		t.Fatalf("句子外字节被重排: %s", out)
	}
	if !bytes.HasSuffix(out, []byte(`,"max_tokens":16}`)) {
		t.Fatalf("尾部字节被改动: %s", out)
	}
}

// TestNormalizeDatelineSystemArray：数组形态仅清洗 type=="text" 块的
// text 值；其余块原样保留。
func TestNormalizeDatelineSystemArray(t *testing.T) {
	body := []byte(`{"system":[` +
		`{"type":"text","text":"Intro. Today` + apostropheU2019 + `s date is 2026-07-01."},` +
		`{"type":"other","text":"Skip. Today` + apostropheU02BC + `s date is 2026-07-02."},` +
		`{"type":"text","text":"No dateline here"}` +
		`],"messages":[]}`)

	out, hits, changed := NormalizeDateline(body)
	if !changed {
		t.Fatalf("changed = false, want true")
	}
	if len(hits) != 1 || hits[0].Variant != "u2019" || hits[0].Separator != "-" {
		t.Fatalf("命中记录 = %v, want [{u2019 -}]", hits)
	}
	first := gjson.GetBytes(out, "system.0.text").String()
	if first != "Intro. Today's date is 2026-07-01." {
		t.Fatalf("system.0.text = %q", first)
	}
	// 非 text 块的原始字节必须原样保留（含其中的水印句）。
	if !bytes.Contains(out, []byte(`{"type":"other","text":"Skip. Today`+apostropheU02BC+`s date is 2026-07-02."}`)) {
		t.Fatalf("非 text 块被改写: %s", out)
	}
}

// TestNormalizeDatelineMessagesString：字符串 content 只清洗
// <system-reminder> 包裹片段，边界外逐字节不动。
func TestNormalizeDatelineMessagesString(t *testing.T) {
	inside := "<system-reminder>Today" + apostropheU2019 + "s date is 2026-07-01.</system-reminder>"
	body := []byte(`{"messages":[{"role":"user","content":"before Today` + apostropheU2019 + `s date is 2026-06-30. mid ` + inside + ` after Today` + apostropheU2019 + `s date is 2026-07-02. tail"}]}`)

	out, hits, changed := NormalizeDateline(body)
	if !changed {
		t.Fatalf("changed = false, want true")
	}
	if len(hits) != 1 || hits[0].Variant != "u2019" || hits[0].Separator != "-" {
		t.Fatalf("命中记录 = %v, want [{u2019 -}]", hits)
	}
	want := "before Today" + apostropheU2019 + "s date is 2026-06-30. mid " +
		"<system-reminder>Today's date is 2026-07-01.</system-reminder>" +
		" after Today" + apostropheU2019 + "s date is 2026-07-02. tail"
	if got := gjson.GetBytes(out, "messages.0.content").String(); got != want {
		t.Fatalf("边界外字节被改写：\ngot  = %q\nwant = %q", got, want)
	}
}

// TestNormalizeDatelineMessagesArray：数组 content 只清洗 text 块内被
// system-reminder 包裹的片段；tool_use.input / tool_result.content /
// reminder 外正文一律不动。
func TestNormalizeDatelineMessagesArray(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"<system-reminder>Today` + apostropheU02B9 + `s date is 2026/07/01.</system-reminder>"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"sh","input":{"command":"echo Today` + apostropheU2019 + `s date is 2026/07/02."}}]},` +
		`{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"t1","content":"Today` + apostropheU2019 + `s date is 2026/07/03."},` +
		`{"type":"text","text":"prose Today` + apostropheU02BC + `s date is 2026/07/04. outside"}` +
		`]}` +
		`]}`)

	out, hits, changed := NormalizeDateline(body)
	if !changed {
		t.Fatalf("changed = false, want true")
	}
	if len(hits) != 1 || hits[0].Variant != "u02b9" || hits[0].Separator != "/" {
		t.Fatalf("命中记录 = %v, want [{u02b9 /}]", hits)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.text").String(); got != "<system-reminder>Today's date is 2026-07-01.</system-reminder>" {
		t.Fatalf("messages.0.content.0.text = %q", got)
	}
	// tool_use.input、tool_result.content 与 reminder 外正文的原始字节不动。
	for _, probe := range []string{
		`"input":{"command":"echo Today` + apostropheU2019 + `s date is 2026/07/02."}`,
		`"content":"Today` + apostropheU2019 + `s date is 2026/07/03."`,
		`"text":"prose Today` + apostropheU02BC + `s date is 2026/07/04. outside"`,
	} {
		if !bytes.Contains(out, []byte(probe)) {
			t.Fatalf("范围外字节被改写，缺失片段: %s", probe)
		}
	}
}

// TestNormalizeDatelineMultipleHits：system 与 messages 多处同时命中，
// 命中数与变体逐一对应。
func TestNormalizeDatelineMultipleHits(t *testing.T) {
	body := []byte(`{"system":"Today` + apostropheU2019 + `s date is 2026-07-01.","messages":[` +
		`{"role":"user","content":"<system-reminder>Today` + apostropheU02BC + `s date is 2026/07/02.</system-reminder>"},` +
		`{"role":"user","content":[{"type":"text","text":"<system-reminder>Today` + apostropheU02B9 + `s date is 2026/07/03.</system-reminder>"}]}` +
		`]}`)

	out, hits, changed := NormalizeDateline(body)
	if !changed {
		t.Fatalf("changed = false, want true")
	}
	want := []DatelineHit{
		{Variant: "u2019", Separator: "-"},
		{Variant: "u02bc", Separator: "/"},
		{Variant: "u02b9", Separator: "/"},
	}
	if len(hits) != len(want) {
		t.Fatalf("命中数 = %d (%v), want %d", len(hits), hits, len(want))
	}
	for i, item := range want {
		if hits[i] != item {
			t.Fatalf("hits[%d] = %+v, want %+v", i, hits[i], item)
		}
	}
	if got := gjson.GetBytes(out, "system").String(); got != "Today's date is 2026-07-01." {
		t.Fatalf("system = %q", got)
	}
	if got := gjson.GetBytes(out, "messages.0.content").String(); got != "<system-reminder>Today's date is 2026-07-02.</system-reminder>" {
		t.Fatalf("messages.0.content = %q", got)
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.text").String(); got != "<system-reminder>Today's date is 2026-07-03.</system-reminder>" {
		t.Fatalf("messages.1.content.0.text = %q", got)
	}
}

// TestNormalizeDatelineIdentity：无改写场景全部恒等返回原切片、nil 命中、
// false（恒等性用 bytes.Equal 断言）。
func TestNormalizeDatelineIdentity(t *testing.T) {
	cases := map[string][]byte{
		"空 body":                    {},
		"非 JSON 体":                  []byte("sorry, today date is unavailable Today" + apostropheU2019 + "s date is 2026-07-01."),
		"无 system/messages 的 JSON":  []byte(`{"model":"m","prompt":"today date is fine"}`),
		"已规范句":                      []byte(`{"system":"Today's date is 2026-07-01.","messages":[{"role":"user","content":"<system-reminder>Today's date is 2026-07-01.</system-reminder>"}]}`),
		"混合分隔符散文":                   []byte(`{"system":"Today` + apostropheU2019 + `s date is 2026-07/01. mixed"}`),
		"messages 字符串无 reminder 包裹": []byte(`{"messages":[{"role":"user","content":"Today` + apostropheU2019 + `s date is 2026-07-01."}]}`),
		"reminder 未闭合":              []byte(`{"messages":[{"role":"user","content":"<system-reminder>Today` + apostropheU2019 + `s date is 2026-07-01. open only"}]}`),
		"仅闭合标签":                     []byte(`{"messages":[{"role":"user","content":"Today` + apostropheU2019 + `s date is 2026-07-01.</system-reminder>"}]}`),
		"tool 块内日期句":                []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"sh","input":{"command":"Today` + apostropheU02BC + `s date is 2026-07-01."}}]}]}`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			out, hits, changed := NormalizeDateline(body)
			if changed || hits != nil {
				t.Fatalf("changed=%v hits=%v, want false/nil", changed, hits)
			}
			if !bytes.Equal(out, body) {
				t.Fatalf("恒等性破坏：out = %s", out)
			}
		})
	}
}

// TestNormalizeDatelineInputUnmodified：纯函数契约——输入切片不被修改。
func TestNormalizeDatelineInputUnmodified(t *testing.T) {
	input := []byte(`{"system":"Today` + apostropheU2019 + `s date is 2026-07-01."}`)
	snapshot := make([]byte, len(input))
	copy(snapshot, input)

	out, _, changed := NormalizeDateline(input)
	if !changed {
		t.Fatalf("changed = false, want true")
	}
	if !bytes.Equal(input, snapshot) {
		t.Fatalf("输入切片被修改: %s", input)
	}
	if bytes.Equal(out, input) {
		t.Fatalf("改写未生效: %s", out)
	}
}
