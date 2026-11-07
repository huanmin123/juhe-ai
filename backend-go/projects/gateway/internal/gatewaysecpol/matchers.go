package gatewaysecpol

import (
	"regexp"
	"sort"
	"strings"
)

// 内置规则匹配层（设计 §3.2：pattern 由代码提供，不可改写；误报防线——
// id_card 走 GB11643 加权校验、bank_card 走 Luhn 校验、phone 走非数字边界）。
// RE2（Go 标准库 regexp）不支持前后瞻断言，数字类边界用 ASCII 词边界 \b 表
// 达：数字与字母/下划线同属词字符，边界断言比「前后非数字」更严——长字母
// 数字混排 token 内的数字串不会误命中，符合误报防线意图。

var (
	// phonePattern 大陆 11 位手机号（1[3-9] 开头），前后非数字边界。
	phonePattern = regexp.MustCompile(`\b1[3-9]\d{9}\b`)
	// idCardPattern 18 位身份证（17 位数字 + 校验位 X|数字），校验位经
	// idCardChecksumValid 验证；小写 x 同样接受（录入形态）。
	idCardPattern = regexp.MustCompile(`\b[0-9]{17}[0-9Xx]\b`)
	// bankCardPattern 13-19 位数字，Luhn 经 luhnValid 验证。
	bankCardPattern = regexp.MustCompile(`\b[0-9]{13,19}\b`)
	// emailPattern 常见 RFC 简化形态（设计 §3.2，接受误报面：@提及、代码地址）。
	emailPattern = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
	// ipv4Pattern 四段 0-255 点分十进制（设计 §3.2 误报提示：版本号/点分数字
	// 如 1.2.3.4 会命中）。
	ipv4Pattern = regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\b`)
	// secretFormsPattern 密钥命名形态：sk- 前缀长 token、Bearer 请求头凭证、
	// AWS AKIA 访问键 ID。
	secretFormsPattern = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9]{20,}|Bearer[ \t]+[A-Za-z0-9._~+/=-]{8,}|AKIA[0-9A-Z]{16})\b`)
	// secretGenericPattern ≥24 位连续混合 token（字母数字下划线连字符），须同
	// 时含字母与数字（mixedTokenValid），纯字母/纯数字串不命中。
	secretGenericPattern = regexp.MustCompile(`[A-Za-z0-9_-]{24,}`)
)

// matchSpan 是一次候选命中（原文本字节区间，左闭右开）。
type matchSpan struct {
	start, end int
}

// ruleMatcher 在单轮文本上产出候选命中：区间互不重叠、按起始位置升序、同起
// 点按规则内确定次序。
type ruleMatcher interface {
	find(text string) []matchSpan
}

// regexMatcher 通用正则匹配器，validate 可选（校验算法过滤误报）。
type regexMatcher struct {
	re       *regexp.Regexp
	validate func(match string) bool
}

func (m *regexMatcher) find(text string) []matchSpan {
	return regexSpans(m.re, text, m.validate)
}

// regexSpans 收集正则全部非重叠命中，validate 非 nil 时逐命中过滤。
func regexSpans(re *regexp.Regexp, text string, validate func(string) bool) []matchSpan {
	indexes := re.FindAllStringIndex(text, -1)
	if len(indexes) == 0 {
		return nil
	}
	spans := make([]matchSpan, 0, len(indexes))
	for _, index := range indexes {
		if validate != nil && !validate(text[index[0]:index[1]]) {
			continue
		}
		spans = append(spans, matchSpan{start: index[0], end: index[1]})
	}
	return spans
}

// idCardWeights / idCardCheckChars 是 GB11643 校验位算法参数（设计 §3.2）。
var idCardWeights = [...]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}

const idCardCheckChars = "10X98765432"

// idCardChecksumValid 验证 18 位身份证 GB11643 加权校验位；校验不过不命中。
func idCardChecksumValid(match string) bool {
	if len(match) != 18 {
		return false
	}
	sum := 0
	for i := 0; i < 17; i++ {
		digit := int(match[i] - '0')
		if digit < 0 || digit > 9 {
			return false
		}
		sum += digit * idCardWeights[i]
	}
	expected := idCardCheckChars[sum%11]
	actual := match[17]
	return actual == expected || (actual == 'x' && expected == 'X')
}

// luhnValid 验证银行卡号 Luhn 校验和；不过不命中。
func luhnValid(match string) bool {
	sum := 0
	double := false
	for i := len(match) - 1; i >= 0; i-- {
		digit := int(match[i] - '0')
		if digit < 0 || digit > 9 {
			return false
		}
		if double {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
		double = !double
	}
	return sum%10 == 0
}

// mixedTokenValid 校验 ≥24 位连续 token 的字母数字混合门槛（任务契约 §1：
// secret 通用形态须同时含字母与数字）。
func mixedTokenValid(match string) bool {
	hasLetter, hasDigit := false, false
	for i := 0; i < len(match); i++ {
		b := match[i]
		switch {
		case b >= '0' && b <= '9':
			hasDigit = true
		case (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z'):
			hasLetter = true
		}
	}
	return hasLetter && hasDigit
}

// secretMatcher 是 secret 规则的复合匹配器：命名形态（sk-/Bearer/AKIA）与
// ≥24 位混合 token 形态合并、区间重叠时命名形态优先（先收集 + 稳定排序）。
type secretMatcher struct {
	forms   *regexp.Regexp
	generic *regexp.Regexp
}

func (m *secretMatcher) find(text string) []matchSpan {
	spans := regexSpans(m.forms, text, nil)
	spans = append(spans, regexSpans(m.generic, text, mixedTokenValid)...)
	if len(spans) <= 1 {
		return spans
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	merged := spans[:0]
	lastEnd := -1
	for _, span := range spans {
		if lastEnd >= 0 && span.start < lastEnd {
			continue
		}
		merged = append(merged, span)
		lastEnd = span.end
	}
	return merged
}

// keywordsMatcher 是 custom_keywords 字面匹配器（大小写敏感，设计 §3.3）。
// 多关键词候选按（起始位置升序、keywords 数组次序）解决重叠，与正则语义对齐。
type keywordsMatcher struct {
	keywords []string
}

func (m *keywordsMatcher) find(text string) []matchSpan {
	type candidate struct {
		span  matchSpan
		order int
	}
	var all []candidate
	for order, keyword := range m.keywords {
		offset := 0
		for {
			index := strings.Index(text[offset:], keyword)
			if index < 0 {
				break
			}
			pos := offset + index
			all = append(all, candidate{span: matchSpan{start: pos, end: pos + len(keyword)}, order: order})
			offset = pos + len(keyword)
		}
	}
	if len(all) == 0 {
		return nil
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].span.start != all[j].span.start {
			return all[i].span.start < all[j].span.start
		}
		return all[i].order < all[j].order
	})
	spans := make([]matchSpan, 0, len(all))
	lastEnd := -1
	for _, item := range all {
		if lastEnd >= 0 && item.span.start < lastEnd {
			continue
		}
		spans = append(spans, item.span)
		lastEnd = item.span.end
	}
	return spans
}

// overlapsProtected 报告候选命中是否与任何占位符保护区间相交。
func overlapsProtected(protected []matchSpan, candidate matchSpan) bool {
	for _, span := range protected {
		if span.start < candidate.end && candidate.start < span.end {
			return true
		}
	}
	return false
}

// applyRuleToText 对单轮文本应用一条规则（设计 §3.5：规则按数组顺序执行，后
// 者匹配前者替换后的文本；§5 占位符保护：与既有占位符区间重叠的候选一律跳
// 过，placeholder 插入的 token 区间并入保护区间）。返回新文本、更新后的保护
// 区间与命中次数（每次替换计 1）。assign 在 placeholder 动作时提供编号 token
// （映射表登记），mask 动作走 rule.maskFn。
func applyRuleToText(rule *compiledRule, text string, protected []matchSpan, assign func(original string) string) (string, []matchSpan, int) {
	candidates := rule.matcher.find(text)
	kept := candidates[:0]
	for _, candidate := range candidates {
		if !overlapsProtected(protected, candidate) {
			kept = append(kept, candidate)
		}
	}
	if len(kept) == 0 {
		return text, protected, 0
	}

	var builder strings.Builder
	builder.Grow(len(text))
	cursor := 0
	delta := 0
	hits := 0
	newProtected := make([]matchSpan, 0, len(protected)+len(kept))
	protIdx := 0
	for _, candidate := range kept {
		// 换算所有位于当前候选之前的旧保护区间（候选与保护区间不重叠，故旧
		// 区间要么整体在候选左侧，要么整体在右侧）。
		for protIdx < len(protected) && protected[protIdx].end <= candidate.start {
			span := protected[protIdx]
			newProtected = append(newProtected, matchSpan{start: span.start + delta, end: span.end + delta})
			protIdx++
		}
		builder.WriteString(text[cursor:candidate.start])
		original := text[candidate.start:candidate.end]
		replacement := ""
		if rule.action == actionPlaceholder {
			replacement = assign(original)
		} else {
			replacement = rule.maskFn(original)
		}
		newStart := builder.Len()
		builder.WriteString(replacement)
		if rule.action == actionPlaceholder {
			newProtected = append(newProtected, matchSpan{start: newStart, end: builder.Len()})
		}
		delta += len(replacement) - (candidate.end - candidate.start)
		cursor = candidate.end
		hits++
	}
	for protIdx < len(protected) {
		span := protected[protIdx]
		newProtected = append(newProtected, matchSpan{start: span.start + delta, end: span.end + delta})
		protIdx++
	}
	builder.WriteString(text[cursor:])
	return builder.String(), newProtected, hits
}
