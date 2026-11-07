package anthropicfp

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

// 本文件钉住 2026-11-07 重写后的字节保真契约：改写只替换命中句的原始
// 字节，字段内其余字节（既有 \uXXXX 转义、\" 转义、<>& 字面量、无效
// UTF-8）逐字节不变。旧实现（sjson.SetBytes 写回）会把改写字段经
// encoding/json 重编码：所有 <>& 变成 \u003c\u003e\u0026、既有转义与
// 无效 UTF-8 被规范化——JSON 语义等价但字节形态漂移，等于把「程序改写」
// 指纹写进最敏感字段。以下测试对旧实现全部失败。

// TestNormalizeDatelineByteFidelity：改写字段同时含 <system-reminder>
// 字面量、既有 \uXXXX 转义、\" 转义、&、无效 UTF-8 裸字节；断言除命中句
// 外全部字节与输入逐字节相同（整输出精确比对）。
func TestNormalizeDatelineByteFidelity(t *testing.T) {
	// 原始字段字节：转义形态的中文 + reminder 字面量 + 水印句（变体撇号
	// + 斜杠分隔符）+ 尾部无效 UTF-8 裸字节 0xff。
	rawField := `keep\"quoted\" & <t> \u4e2d\u6587` +
		`<system-reminder>Today’s date is 2026/07/01.</system-reminder>` +
		` tail ` + "\xff"
	body := []byte(`{"messages":[{"role":"user","content":"` + rawField + `"}]}`)

	want := []byte(`{"messages":[{"role":"user","content":"` +
		`keep\"quoted\" & <t> \u4e2d\u6587` +
		`<system-reminder>Today's date is 2026-07-01.</system-reminder>` +
		` tail ` + "\xff" + `"}]}`)

	out, hits, changed := NormalizeDateline(body)
	if !changed {
		t.Fatalf("changed = false, want true")
	}
	if len(hits) != 1 || hits[0].Variant != "u2019" || hits[0].Separator != "/" {
		t.Fatalf("命中记录 = %v, want [{u2019 /}]", hits)
	}
	if !bytes.Equal(out, want) {
		t.Fatalf("字节保真破坏：\nout  = %q\nwant = %q", out, want)
	}
	// S1 反指纹断言：改写后不得出现 \u003c 形态的 HTML 转义（旧实现必然引入）。
	if bytes.Contains(out, []byte(`\u003c`)) || bytes.Contains(out, []byte(`\u0026`)) {
		t.Fatalf("改写字段出现 encoding/json HTML 转义指纹: %s", out)
	}
	// S2 断言：无效 UTF-8 裸字节未被重编码为 U+FFFD。
	if !bytes.Contains(out, []byte("\xff")) {
		t.Fatalf("无效 UTF-8 裸字节丢失（被重编码为 U+FFFD）: %s", out)
	}
	// JSON 语义校验：解码值与预期一致。
	if got := gjson.GetBytes(out, "messages.0.content").String(); got != "keep\"quoted\" & <t> 中文<system-reminder>Today's date is 2026-07-01.</system-reminder> tail \xff" {
		t.Fatalf("解码值 = %q", got)
	}
}

// TestNormalizeDatelineEscapedSentenceByteFidelity：水印句自身含既有
// \uXXXX 转义（撇号写成 \u2019）时命中（解码后匹配，与旧实现语义一致），
// 命中区间按原始字节替换——6 字节的 \u2019 转义与句子其余部分一起被
// 规范 ASCII 句替换，句外转义保真。
func TestNormalizeDatelineEscapedSentenceByteFidelity(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"<system-reminder>Today\u2019s date is 2026-07-01.</system-reminder>"}]}`)

	out, hits, changed := NormalizeDateline(body)
	if !changed {
		t.Fatalf("changed = false, want true")
	}
	if len(hits) != 1 || hits[0].Variant != "u2019" || hits[0].Separator != "-" {
		t.Fatalf("命中记录 = %v, want [{u2019 -}]", hits)
	}
	want := []byte(`{"messages":[{"role":"user","content":"<system-reminder>Today's date is 2026-07-01.</system-reminder>"}]}`)
	if !bytes.Equal(out, want) {
		t.Fatalf("转义句改写不一致：\nout  = %s\nwant = %s", out, want)
	}
}

// TestNormalizeDatelineSameFieldMultipleHits：同一 text 字段内两句水印
// 一次装配正确——两段命中区间替换，中间片段与边界逐字节保留，命中按
// 出现顺序记录。
func TestNormalizeDatelineSameFieldMultipleHits(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"A Today’s date is 2026-07-01. middle Todayʼs date is 2026/08/02. B"}]}`)

	out, hits, changed := NormalizeDateline(body)
	if !changed {
		t.Fatalf("changed = false, want true")
	}
	want := []DatelineHit{{Variant: "u2019", Separator: "-"}, {Variant: "u02bc", Separator: "/"}}
	if len(hits) != 2 || hits[0] != want[0] || hits[1] != want[1] {
		t.Fatalf("命中记录 = %v, want %v", hits, want)
	}
	wantOut := []byte(`{"system":[{"type":"text","text":"A Today's date is 2026-07-01. middle Today's date is 2026-08-02. B"}]}`)
	if !bytes.Equal(out, wantOut) {
		t.Fatalf("多命中装配不一致：\nout  = %s\nwant = %s", out, wantOut)
	}
}

// TestNormalizeDatelineSurrogatePairOffsets：代理对（surrogate pair）转义
// 字段内命中的偏移正确性——\ud83d\ude00（😀，两个 \uXXXX 转义共 12 原始
// 字节解码 4 解码字节）位于命中句之前，行走必须精确跨过代理对，既不误判
// 偏移也不破坏转义字节。
func TestNormalizeDatelineSurrogatePairOffsets(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"<system-reminder>\ud83d\ude00 Today’s date is 2026-07-01.</system-reminder>"}]}`)

	out, hits, changed := NormalizeDateline(body)
	if !changed {
		t.Fatalf("changed = false, want true")
	}
	if len(hits) != 1 || hits[0].Variant != "u2019" || hits[0].Separator != "-" {
		t.Fatalf("命中记录 = %v, want [{u2019 -}]", hits)
	}
	want := []byte(`{"messages":[{"role":"user","content":"<system-reminder>\ud83d\ude00 Today's date is 2026-07-01.</system-reminder>"}]}`)
	if !bytes.Equal(out, want) {
		t.Fatalf("代理对偏移错位：\nout  = %s\nwant = %s", out, want)
	}
}

// TestNormalizeDatelineLargeBodyPerformance：大 body 性能护栏。构造约
// 8MB、4000 命中的 body（镜像终审实测规模 8.39MB/3961 命中），断言清洗
// 耗时低于 datelinePerfGuardLimit（按构建标签区分：非 race 2s / race 8s，
// 见 perf_guard_limit*_test.go）。基线：修复前 sjson 每命中一次全量 body
// 拷贝实测 12.3s；修复后为 O(body + 总命中长度) 单次装配，非 race 正常
// 耗时 0.1s 以内。
func TestNormalizeDatelineLargeBodyPerformance(t *testing.T) {
	const messageCount = 4000
	filler := strings.Repeat("x", 2100)
	message := `{"role":"user","content":"<system-reminder>` + filler +
		`Today’s date is 2026-07-01.</system-reminder>"}`
	var bodyBuilder strings.Builder
	bodyBuilder.Grow(messageCount * (len(message) + 20))
	bodyBuilder.WriteString(`{"model":"claude-sonnet-4","messages":[`)
	for i := 0; i < messageCount; i++ {
		if i > 0 {
			bodyBuilder.WriteByte(',')
		}
		bodyBuilder.WriteString(message)
	}
	bodyBuilder.WriteString(`]}`)
	body := []byte(bodyBuilder.String())
	if len(body) < 8*1024*1024 {
		t.Fatalf("护栏 body 构造退化：len = %d, want ≥ 8MB", len(body))
	}

	start := time.Now()
	out, hits, changed := NormalizeDateline(body)
	elapsed := time.Since(start)

	if !changed {
		t.Fatalf("changed = false, want true")
	}
	if len(hits) != messageCount {
		t.Fatalf("命中数 = %d, want %d", len(hits), messageCount)
	}
	if elapsed >= datelinePerfGuardLimit {
		t.Fatalf("清洗耗时 %v ≥ 护栏上限 %v（基线：修复前 12.3s @ 8.39MB/3961 命中；修复后目标 <1s）",
			elapsed, datelinePerfGuardLimit)
	}
	// 输出正确性抽查：水印句全部归一化、未残留变体撇号，未触碰的 filler
	// 与首尾消息结构保真。
	if bytes.Contains(out, []byte("Today’s")) {
		t.Fatalf("输出残留未归一化水印句")
	}
	if got := strings.Count(string(out), "Today's date is 2026-07-01."); got != messageCount {
		t.Fatalf("规范句数量 = %d, want %d", got, messageCount)
	}
	first := gjson.GetBytes(out, "messages.0.content").String()
	last := gjson.GetBytes(out, "messages."+strconv.Itoa(messageCount-1)+".content").String()
	for name, got := range map[string]string{"first": first, "last": last} {
		wantText := "<system-reminder>" + filler + "Today's date is 2026-07-01.</system-reminder>"
		if got != wantText {
			t.Fatalf("%s 消息清洗结果不符：got_len=%d want_len=%d", name, len(got), len(wantText))
		}
	}
	t.Logf("8MB/%d 命中清洗耗时 %v（修复前基线 12.3s）", messageCount, elapsed)
}
