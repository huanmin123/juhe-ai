// Package anthropicfp 提供转发链路上 Anthropic 客户端指纹的清洗原语。
//
// 当前暴露 NormalizeDateline：把 Anthropic 请求体中的水印日期句
// "Today<apo>s date is <YYYY><sep><MM><sep><DD>." 归一化为规范形态
// "Today's date is YYYY-MM-DD."（ASCII 撇号 + 连字符）。本包只负责请求体
// 内容归一化；生效范围（anthropic 家族 + oauth 账户）由调用方
// （gatewaydispatch attempt 层）门控。行为依据是对
// Claude Code 客户端在非官方 base URL 下的取证结论：该客户端会在上述
// 句子里用变体撇号码点或斜杠日期分隔符隐写"该客户端连的不是官方端点"
// 的信号；网关原样透传等于把经过中转的证据送回上游。
//
// 实现为「一次装配 + 字节保真」（2026-11-07 重写）：gjson 只用于定位目标
// 字符串字段的原始 token（Result.Index = 开引号在 body 中的字节偏移，
// Result.Raw = 含引号的原始编码文本，可靠性由 dateline_probe_gjson_test.go
// 探针钉住）；改写在引号内原始字节上做 JSON 转义感知行走，把解码文本上的
// 命中区间映射回原始区间，全部改写区间收集后按偏移排序、单次遍历装配、
// 一次分配。未触碰区域（既有 \uXXXX 转义、<>&、无效 UTF-8、缩进）一个
// 字节都不变——字段改写不得经 encoding/json 重编码，否则转义形态漂移本身
// 就是「程序改写」指纹。
//
// 本包为独立实现，只采用取证到的行为事实（目标句形态、变体码点集合、
// 扫描范围），不含任何外部源码。
package anthropicfp

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// datelineProbe 是水印日期句的固定片段；任何命中都必然包含它，
// 作为全文与逐字段的快路径 Necessary 条件。
const datelineProbe = "date is "

// reminderOpenTag 是 messages 扫描范围的门卫片段：messages 内只清洗
// <system-reminder> 包裹的片段。
const reminderOpenTag = "<system-reminder>"

// 水印句匹配模式。撇号组覆盖 ASCII U+0027 与三个取证到的变体码点
// U+2019 / U+02BC / U+02B9；分隔符用两个独立模式分别匹配 "-" 和 "/"，
// 因为 Go regexp（RE2）不支持反向引用，两遍扫描保证句内前后分隔符
// 一致，从而滤掉 "2026-07/01" 这类混合分隔符的普通散文。
var (
	datelineHyphenPattern = regexp.MustCompile(`Today(['’ʼʹ])s date is (\d{4})-(\d{2})-(\d{2})\.`)
	datelineSlashPattern  = regexp.MustCompile(`Today(['’ʼʹ])s date is (\d{4})/(\d{2})/(\d{2})\.`)
	reminderBlockPattern  = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
)

// DatelineHit 记录一次归一化的观测信息：撇号变体与改写前的日期分隔符。
// Variant ∈ {"ascii", "u2019", "u02bc", "u02b9"}；Separator ∈ {"-", "/"}。
type DatelineHit struct {
	Variant   string
	Separator string
}

// canonicalDateline 拼出规范句：ASCII 撇号 + 连字符分隔符。
func canonicalDateline(year, month, day string) string {
	return "Today's date is " + year + "-" + month + "-" + day + "."
}

// apostropheVariantName 把撇号捕获组映射为观测名。
func apostropheVariantName(apostrophe string) string {
	switch apostrophe {
	case "'":
		return "ascii"
	case "’":
		return "u2019"
	case "ʼ":
		return "u02bc"
	case "ʹ":
		return "u02b9"
	default:
		return "ascii"
	}
}

// rewriteDatelinePattern 用单一分隔符模式扫描一遍 text，把每个非规范
// 命中句替换为规范句；已规范句（ASCII 撇号 + 连字符）不改写、不记命中。
// 返回值与输入字符串内容一致时返回原字符串（不改写）。
func rewriteDatelinePattern(text string, pattern *regexp.Regexp, separator string) (string, []DatelineHit) {
	matches := pattern.FindAllStringSubmatchIndex(text, -1)
	if matches == nil {
		return text, nil
	}
	var rebuilt strings.Builder
	rebuilt.Grow(len(text))
	var hits []DatelineHit
	copied := 0
	for _, match := range matches {
		sentence := text[match[0]:match[1]]
		canonical := canonicalDateline(text[match[4]:match[5]], text[match[6]:match[7]], text[match[8]:match[9]])
		if sentence == canonical {
			// ASCII 撇号本就规范：即使命中也不产生改写、不记命中。
			continue
		}
		rebuilt.WriteString(text[copied:match[0]])
		rebuilt.WriteString(canonical)
		copied = match[1]
		hits = append(hits, DatelineHit{
			Variant:   apostropheVariantName(text[match[2]:match[3]]),
			Separator: separator,
		})
	}
	if copied == 0 {
		return text, nil
	}
	rebuilt.WriteString(text[copied:])
	return rebuilt.String(), hits
}

// NormalizeText 清洗一段明文中的全部水印日期句。无命中时原样返回
// 输入字符串、命中列表为 nil。
func NormalizeText(text string) (string, []DatelineHit) {
	if !strings.Contains(text, datelineProbe) {
		return text, nil
	}
	text, hyphenHits := rewriteDatelinePattern(text, datelineHyphenPattern, "-")
	text, slashHits := rewriteDatelinePattern(text, datelineSlashPattern, "/")
	if len(hyphenHits) == 0 && len(slashHits) == 0 {
		return text, nil
	}
	return text, append(hyphenHits, slashHits...)
}

// decodedSpan 是解码文本上的一个待替换区间 [start, end)，replacement 为
// 规范 ASCII 句（纯 ASCII，写入原始字节无需任何 JSON 转义）。
type decodedSpan struct {
	start, end  int
	replacement string
}

// datelineSpan 是 body 绝对字节坐标上的一个待替换区间 [start, end)。
type datelineSpan struct {
	start, end  int
	replacement string
}

// collectSentenceSpans 在 text 上按 NormalizeText 的命中语义收集待替换
// 解码区间（连字符模式先行，与 NormalizeText 的命中追加顺序一致）；
// base 为 text 在外层解码文本中的偏移。已规范句不收集、不记命中。
func collectSentenceSpans(text string, base int) ([]decodedSpan, []DatelineHit) {
	var (
		spans []decodedSpan
		hits  []DatelineHit
	)
	for _, scan := range [...]struct {
		pattern *regexp.Regexp
		sep     string
	}{
		{datelineHyphenPattern, "-"},
		{datelineSlashPattern, "/"},
	} {
		for _, match := range scan.pattern.FindAllStringSubmatchIndex(text, -1) {
			sentence := text[match[0]:match[1]]
			canonical := canonicalDateline(text[match[4]:match[5]], text[match[6]:match[7]], text[match[8]:match[9]])
			if sentence == canonical {
				continue
			}
			spans = append(spans, decodedSpan{
				start:       base + match[0],
				end:         base + match[1],
				replacement: canonical,
			})
			hits = append(hits, DatelineHit{
				Variant:   apostropheVariantName(text[match[2]:match[3]]),
				Separator: scan.sep,
			})
		}
	}
	return spans, hits
}

// collectDatelineSpans 在解码文本上按扫描范围语义收集待替换区间：
// reminderOnly=false 为 NormalizeText 语义（system 字段全文）；
// reminderOnly=true 为 normalizeReminderBlocks 语义（仅
// <system-reminder>...</system-reminder> 包裹片段，片段外一律不动）。
func collectDatelineSpans(decoded string, reminderOnly bool) ([]decodedSpan, []DatelineHit) {
	if reminderOnly {
		if !strings.Contains(decoded, reminderOpenTag) {
			return nil, nil
		}
		blocks := reminderBlockPattern.FindAllStringIndex(decoded, -1)
		if len(blocks) == 0 {
			return nil, nil
		}
		var (
			spans []decodedSpan
			hits  []DatelineHit
		)
		for _, block := range blocks {
			blockSpans, blockHits := collectSentenceSpans(decoded[block[0]:block[1]], block[0])
			spans = append(spans, blockSpans...)
			hits = append(hits, blockHits...)
		}
		return spans, hits
	}
	if !strings.Contains(decoded, datelineProbe) {
		return nil, nil
	}
	return collectSentenceSpans(decoded, 0)
}

// consumeJSONEscape 消费 content[bsPos:] 处的一个反斜杠转义，返回原始
// 消费字节数与解码产出字节数。解码规则逐条镜像 gjson v1.20.1 unescape
// （dateline 命中区间映射必须与 gjson 的 Str 解码逐字节对齐）：简单转义
// 2→1 字节；\uXXXX 经 ParseUint(4 hex) 忽略错误取码元，高位代理后瞻恰好
// 6 字节的 \u 低代理则合并为一枚 run；代理等非法码元经 utf8.EncodeRune
// 落 RuneError（3 字节）。ok=false 表示 gjson unescape 会在此截断
// （尾部孤立反斜杠、\u 后不足 4 个十六进制、未知转义字符）。
func consumeJSONEscape(content []byte, bsPos int) (rawAdv, decAdv int, ok bool) {
	if bsPos+1 >= len(content) {
		return 0, 0, false
	}
	switch content[bsPos+1] {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		return 2, 1, true
	case 'u':
		u := bsPos + 1
		if u+5 > len(content) {
			return 0, 0, false
		}
		r := parseHex4Rune(content[u+1 : u+5])
		adv := 6
		if r >= 0xD800 && r < 0xDC00 {
			// gjson 的代理对前瞻条件：剩余 ≥ 6 字节且为 \u 前缀。
			if u+11 <= len(content) && content[u+5] == '\\' && content[u+6] == 'u' {
				r2 := parseHex4Rune(content[u+7 : u+11])
				if r2 >= 0xDC00 && r2 < 0xE000 {
					r = utf16.DecodeRune(r, r2)
					adv = 12
				}
			}
		}
		return adv, utf8EncodedLen(r), true
	default:
		return 0, 0, false
	}
}

// parseHex4Rune 镜像 gjson runeit（strconv.ParseUint(4 字符, 16, 64) 并
// 忽略错误）：任一字符非十六进制时结果为 0（Go strconv 语法错误返回 0，
// 已实测验证）。
func parseHex4Rune(b []byte) rune {
	var n uint64
	for _, c := range b {
		var d uint64
		switch {
		case c >= '0' && c <= '9':
			d = uint64(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = uint64(c-'A') + 10
		default:
			return 0
		}
		n = n*16 + d
	}
	return rune(n)
}

// utf8EncodedLen 是一枚码元按 gjson 路径（utf8.EncodeRune）产出的 UTF-8
// 字节数；代理等非法码点写 RuneError（3 字节）。
func utf8EncodedLen(r rune) int {
	if n := utf8.RuneLen(r); n > 0 {
		return n
	}
	return 3
}

// mapDecodedOffsets 沿 token 引号内原始字节做转义感知行走（镜像 gjson
// 的解码路径），记录每个目标解码偏移对应的原始偏移。解码偏移 k 对应
// 「第 k 个解码字节在原始字节中的起点」；目标必须落在字符边界上（水印
// 句两端为 ASCII 字面量，天然满足），行走越过目标（目标落在多字节转义
// 产出中间）即判定失步。gjson 的两条解码路径都必须镜像：内容不含反斜杠
// 时 tostr 逐字节原样返回（无截断规则）；含反斜杠时 unescape 在裸控制
// 字符与截断转义处提前停止。任一目标不可达或解码总长与 gjson 的 Str
// 不一致返回 false（调用方整字段跳过，fail-open）。
func mapDecodedOffsets(content []byte, decodedLen int, targets []int) ([]int, bool) {
	offsets := make([]int, len(targets))
	ti := 0
	decPos, rawPos := 0, 0
	escapeMode := bytes.IndexByte(content, '\\') >= 0
	truncated := false
	for !truncated {
		for ti < len(targets) && targets[ti] == decPos {
			offsets[ti] = rawPos
			ti++
		}
		if rawPos >= len(content) {
			break
		}
		if escapeMode {
			switch b := content[rawPos]; {
			case b == '\\':
				rawAdv, decAdv, ok := consumeJSONEscape(content, rawPos)
				if !ok {
					truncated = true
					continue
				}
				rawPos += rawAdv
				decPos += decAdv
			case b < ' ':
				truncated = true
				continue
			default:
				rawPos++
				decPos++
			}
		} else {
			rawPos++
			decPos++
		}
	}
	for ti < len(targets) && targets[ti] == decPos {
		offsets[ti] = rawPos
		ti++
	}
	if ti != len(targets) || decPos != decodedLen {
		return nil, false
	}
	return offsets, true
}

// scanStringField 在一个已定位的 JSON 字符串 token 上执行清洗：从 gjson
// Result 恢复引号内原始字节（body 子切片，零拷贝），在解码文本（Result.Str）
// 上收集命中，再把命中区间映射回原始区间、折算为 body 绝对坐标。定位或
// 映射任一校验失败返回 nil——该字段整字段跳过且不记命中（清洗是附加卫生
// 动作，失败不得阻断转发，与既有写回失败的 fail-open 口径一致）。
func scanStringField(body []byte, res gjson.Result, reminderOnly bool) ([]datelineSpan, []DatelineHit) {
	if res.Type != gjson.String || res.Index <= 0 {
		return nil, nil
	}
	rawLen := len(res.Raw)
	if rawLen < 2 || res.Raw[0] != '"' || res.Raw[rawLen-1] != '"' {
		return nil, nil
	}
	tokenEnd := res.Index + rawLen
	if tokenEnd > len(body) || body[res.Index] != '"' || body[tokenEnd-1] != '"' {
		return nil, nil
	}
	content := body[res.Index+1 : tokenEnd-1]
	decodedSpans, hits := collectDatelineSpans(res.Str, reminderOnly)
	if len(decodedSpans) == 0 {
		return nil, nil
	}
	// 命中区间先按解码位置升序（稳定排序）：collectSentenceSpans 按「先
	// hyphen 轮、后 slash 轮」收集，slash 句先于 hyphen 句时 decodedSpans
	// 乱序；offsets 与 span 必须按同一顺序配对，否则 replacement 会写到
	// 别的命中区间上（BL-1：跨句日期互换的静默损坏）。
	sort.SliceStable(decodedSpans, func(i, j int) bool { return decodedSpans[i].start < decodedSpans[j].start })
	targets := make([]int, 0, 2*len(decodedSpans))
	for _, span := range decodedSpans {
		targets = append(targets, span.start, span.end)
	}
	// 区间互斥保证 targets 严格递增（offsets 按序配对的前提）；不变量被
	// 破坏时 fail-open 整字段跳过，绝不静默错位改写。
	for k := 1; k < len(targets); k++ {
		if targets[k] < targets[k-1] {
			return nil, nil
		}
	}
	offsets, ok := mapDecodedOffsets(content, len(res.Str), targets)
	if !ok {
		return nil, nil
	}
	base := res.Index + 1
	spans := make([]datelineSpan, 0, len(decodedSpans))
	for i, span := range decodedSpans {
		spans = append(spans, datelineSpan{
			start:       base + offsets[2*i],
			end:         base + offsets[2*i+1],
			replacement: span.replacement,
		})
	}
	return spans, hits
}

// assembleNormalizedBody 把全部改写区间按偏移排序后单次遍历装配：区间
// 之间的原始字节逐字节复制，区间替换为规范 ASCII 句。一次分配、输出
// 精确尺寸，复杂度 O(body + 总命中长度)。区间重叠或越界（内部不变量
// 破坏）返回 nil，调用方 fail-open 返回原 body。
func assembleNormalizedBody(body []byte, spans []datelineSpan) []byte {
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	prevEnd := 0
	for _, span := range spans {
		if span.start < prevEnd || span.end < span.start || span.end > len(body) {
			return nil
		}
		prevEnd = span.end
	}
	size := len(body)
	for _, span := range spans {
		size += len(span.replacement) - (span.end - span.start)
	}
	out := make([]byte, 0, size)
	prevEnd = 0
	for _, span := range spans {
		out = append(out, body[prevEnd:span.start]...)
		out = append(out, span.replacement...)
		prevEnd = span.end
	}
	out = append(out, body[prevEnd:]...)
	return out
}

// NormalizeDateline 扫描 Anthropic /v1/messages 请求体并把水印日期句
// 归一化为规范 ASCII 形态。扫描范围镜像真实客户端的落点：
//
//   - system 字段：字符串形态全文清洗；数组形态仅清洗 type=="text"
//     块的 text 值。
//   - messages[i].content：字符串形态仅清洗 <system-reminder> 包裹的
//     片段；数组形态仅清洗 type=="text" 块内被 system-reminder 包裹的
//     片段（system prompt caching 使首轮之后的顶层 system 块不再重发，
//     dateline 实际落在 reminder 内）。
//   - 其余位置（用户正文、tool_use.input、tool_result.content 等）一律
//     不扫描，合法含撇号或斜杠日期的文本永不改写。
//
// 纯函数：不修改输入切片；无改写时返回原切片（identity）、nil 命中、
// false。改写为字节级原位装配：只有命中句的字节被替换，其余全部字节
// （含既有转义与无效 UTF-8）逐字节保真，禁止整包重序列化。
func NormalizeDateline(body []byte) ([]byte, []DatelineHit, bool) {
	if len(body) == 0 || !bytes.Contains(body, []byte(datelineProbe)) {
		return body, nil, false
	}

	var (
		spans []datelineSpan
		hits  []DatelineHit
	)

	// 所有字段都在原始 body 上定位（gjson Index 为绝对偏移），改写区间
	// 收集完毕后一次性装配——不存在旧实现逐字段写回导致的偏移漂移。
	if system := gjson.GetBytes(body, "system"); system.Type == gjson.String {
		fieldSpans, fieldHits := scanStringField(body, system, false)
		spans = append(spans, fieldSpans...)
		hits = append(hits, fieldHits...)
	} else if system.IsArray() {
		system.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() != "text" {
				return true
			}
			text := block.Get("text")
			if text.Exists() && text.Type == gjson.String {
				fieldSpans, fieldHits := scanStringField(body, text, false)
				spans = append(spans, fieldSpans...)
				hits = append(hits, fieldHits...)
			}
			return true
		})
	}

	if messages := gjson.GetBytes(body, "messages"); messages.IsArray() {
		messages.ForEach(func(_, message gjson.Result) bool {
			content := message.Get("content")
			switch {
			case content.Type == gjson.String:
				fieldSpans, fieldHits := scanStringField(body, content, true)
				spans = append(spans, fieldSpans...)
				hits = append(hits, fieldHits...)
			case content.IsArray():
				content.ForEach(func(_, block gjson.Result) bool {
					if block.Get("type").String() != "text" {
						return true
					}
					text := block.Get("text")
					if text.Exists() && text.Type == gjson.String {
						fieldSpans, fieldHits := scanStringField(body, text, true)
						spans = append(spans, fieldSpans...)
						hits = append(hits, fieldHits...)
					}
					return true
				})
			}
			return true
		})
	}

	if len(spans) == 0 {
		return body, nil, false
	}
	out := assembleNormalizedBody(body, spans)
	if out == nil {
		return body, nil, false
	}
	return out, hits, true
}
