package anthropicfp

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

// 本文件钉住 BL-1（2026-11-07 独立复查证伪）回归：collectSentenceSpans 按
// 「先 hyphen 轮、后 slash 轮」收集命中，decodedSpans 不按解码位置升序；
// scanStringField 必须先按 start 稳定排序再生成 targets，否则 offsets 与
// span 配对错位——slash 句先于 hyphen 句时，两个 replacement 写到对方的
// 命中区间上，输出日期互换（静默损坏，装配校验因错位区间仍互斥不越界而
// 无法拦截）。命中列表顺序保持 collectSentenceSpans 收集序（与 NormalizeText
// 的「先 hyphen 后 slash」追加口径一致），与文本位置序无关。

// slash 句在前、hyphen 句在后、日期不同：逐句归一化正确、日期不互换。
func TestNormalizeDatelineSlashBeforeHyphenOrdering(t *testing.T) {
	body := []byte(`{"system":"Today’s date is 2026/07/01. mid Today’s date is 2026-07-02."}`)

	out, hits, changed := NormalizeDateline(body)
	if !changed {
		t.Fatalf("changed = false, want true")
	}
	want := []byte(`{"system":"Today's date is 2026-07-01. mid Today's date is 2026-07-02."}`)
	if !bytes.Equal(out, want) {
		t.Fatalf("命中区间与 replacement 配对错位（BL-1）：\nout  = %s\nwant = %s", out, want)
	}
	// 命中列表 = 收集序（先 hyphen 轮后 slash 轮），与 NormalizeText 同口径。
	wantHits := []DatelineHit{{Variant: "u2019", Separator: "-"}, {Variant: "u2019", Separator: "/"}}
	if len(hits) != 2 || hits[0] != wantHits[0] || hits[1] != wantHits[1] {
		t.Fatalf("命中记录 = %v, want %v", hits, wantHits)
	}
}

// 三命中交错（slash/hyphen/slash，日期各不相同）：全部按位归一化。
func TestNormalizeDatelineInterleavedTripleHitsOrdering(t *testing.T) {
	body := []byte(`{"system":"A Today’s date is 2026/01/01. B Today’s date is 2026-02-02. C Today’s date is 2026/03/03. D"}`)

	out, hits, changed := NormalizeDateline(body)
	if !changed {
		t.Fatalf("changed = false, want true")
	}
	want := []byte(`{"system":"A Today's date is 2026-01-01. B Today's date is 2026-02-02. C Today's date is 2026-03-03. D"}`)
	if !bytes.Equal(out, want) {
		t.Fatalf("三命中交错配对错位：\nout  = %s\nwant = %s", out, want)
	}
	wantHits := []DatelineHit{
		{Variant: "u2019", Separator: "-"},
		{Variant: "u2019", Separator: "/"},
		{Variant: "u2019", Separator: "/"},
	}
	if len(hits) != 3 || hits[0] != wantHits[0] || hits[1] != wantHits[1] || hits[2] != wantHits[2] {
		t.Fatalf("命中记录 = %v, want %v", hits, wantHits)
	}
}

// messages reminder 路径的交错场景（字符串 content 与数组 content text 各
// 一处，slash/hyphen 交错），日期不互换。
func TestNormalizeDatelineReminderInterleavedOrdering(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"user","content":"<system-reminder>Today’s date is 2026/07/01. mid Today’s date is 2026-07-02.</system-reminder>"},` +
		`{"role":"user","content":[{"type":"text","text":"<system-reminder>Today’s date is 2026-08-03. mid Today’s date is 2026/09/04.</system-reminder>"}]}` +
		`]}`)

	out, hits, changed := NormalizeDateline(body)
	if !changed {
		t.Fatalf("changed = false, want true")
	}
	want := []byte(`{"messages":[` +
		`{"role":"user","content":"<system-reminder>Today's date is 2026-07-01. mid Today's date is 2026-07-02.</system-reminder>"},` +
		`{"role":"user","content":[{"type":"text","text":"<system-reminder>Today's date is 2026-08-03. mid Today's date is 2026-09-04.</system-reminder>"}]}` +
		`]}`)
	if !bytes.Equal(out, want) {
		t.Fatalf("reminder 路径交错配对错位：\nout  = %s\nwant = %s", out, want)
	}
	if len(hits) != 4 {
		t.Fatalf("命中数 = %d, want 4", len(hits))
	}
	// 逐句抽查两条消息的解码值（字节比对之外的语义复核）。
	first := gjson.GetBytes(out, "messages.0.content").String()
	if first != "<system-reminder>Today's date is 2026-07-01. mid Today's date is 2026-07-02.</system-reminder>" {
		t.Fatalf("messages.0.content = %q", first)
	}
	second := gjson.GetBytes(out, "messages.1.content.0.text").String()
	if second != "<system-reminder>Today's date is 2026-08-03. mid Today's date is 2026-09-04.</system-reminder>" {
		t.Fatalf("messages.1.content.0.text = %q", second)
	}
}
