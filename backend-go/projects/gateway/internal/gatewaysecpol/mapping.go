package gatewaysecpol

import (
	"sort"
	"strconv"
	"strings"
)

// 请求级占位符映射表（设计 §2：随请求上下文传播、终态销毁；不落盘、不进
// spool、不进 Redis、不写日志）。非并发安全——只在单 goroutine 的请求链内
// 使用（SanitizeBody 产出、StreamRewriter/RewriteFull 消费）。

// mappingEntry 是单个占位符的还原事实。
type mappingEntry struct {
	value    string // 原文片段
	isNumber bool   // 原始 JSON 类型是否为 number（§7.1 类型保持：独占叶子 → 裸字面量）
}

// Mapping 是 placeholder 命中的映射表：token（[[LABEL_N]]）→ 原文与原始类型，
// 外加本请求的命中计数聚合（mask 与 placeholder 一并计入，与
// SanitizeResult.Hits 同一聚合）。
type Mapping struct {
	tokens      map[string]*mappingEntry
	originals   map[string]int // label \x00 original \x00 n|s → 编号（同一原文片段复用同一编号）
	counters    map[string]int // label → 已分配编号（首次出现顺序从 1 递增）
	hits        map[string]int
	maxTokenLen int
}

func newMapping() *Mapping {
	return &Mapping{
		tokens:    map[string]*mappingEntry{},
		originals: map[string]int{},
		counters:  map[string]int{},
		hits:      map[string]int{},
	}
}

// assign 登记一次 placeholder 命中：同一（label, 原文字节）复用同一编号（设
// 计 §2「同一手机号全文只占一个编号」/ 任务契约「dedup 按原文字节」），新片
// 段按同标签首次出现顺序递增编号。同一原文字节在 string 与 number 两种叶子上
// 都出现时共用编号，原始类型以首次出现为准（还原形态按该类型；已知边界：
// 后出现的另一种类型上下文按首次类型还原）。
func (m *Mapping) assign(label, original string, isNumber bool) string {
	key := label + "\x00" + original
	if number, exists := m.originals[key]; exists {
		return placeholderToken(label, number)
	}
	m.counters[label]++
	number := m.counters[label]
	m.originals[key] = number
	token := placeholderToken(label, number)
	if len(token) > m.maxTokenLen {
		m.maxTokenLen = len(token)
	}
	m.tokens[token] = &mappingEntry{value: original, isNumber: isNumber}
	return token
}

func (m *Mapping) lookup(token string) (*mappingEntry, bool) {
	if m == nil {
		return nil, false
	}
	entry, exists := m.tokens[token]
	return entry, exists
}

// HasSplitPrefix 报告 prefix 是否是某个映射 token 的真前缀（跨 SSE 事件拆
// 分时用于判断是否值得扣住当前事件等待补齐）。
func (m *Mapping) HasSplitPrefix(prefix string) bool {
	return len(m.SplitCandidates(prefix)) > 0
}

// SplitCandidates 返回 prefix 对应的 token 剩余片段 → 还原值映射；空表示
// 没有 token 能由该前缀继续。
func (m *Mapping) SplitCandidates(prefix string) map[string]string {
	candidates := map[string]string{}
	if m == nil || prefix == "" {
		return candidates
	}
	for token, entry := range m.tokens {
		if !strings.HasPrefix(token, prefix) {
			continue
		}
		rest := token[len(prefix):]
		if rest != "" {
			candidates[rest] = entry.value
		}
	}
	return candidates
}

// placeholderToken 统一 token 形态（设计 §3.4：[[LABEL_N]]，双方括号降低与
// 正文冲突概率）。
func placeholderToken(label string, number int) string {
	return "[[" + label + "_" + strconv.Itoa(number) + "]]"
}

// Empty 报告映射表是否没有任何占位符条目。
func (m *Mapping) Empty() bool {
	return m == nil || len(m.tokens) == 0
}

// Hits 返回命中计数聚合的副本（label → 次数，含 mask 与 placeholder；无原文，
// 设计 §8）。
func (m *Mapping) Hits() map[string]int {
	hits := map[string]int{}
	if m == nil {
		return hits
	}
	for label, count := range m.hits {
		hits[label] = count
	}
	return hits
}

// tokensByLengthDesc 返回按 token 长度降序（同长按字典序）的全部 token，供
// RewriteFull 确定性替换。
func (m *Mapping) tokensByLengthDesc() []string {
	tokens := make([]string, 0, len(m.tokens))
	for token := range m.tokens {
		tokens = append(tokens, token)
	}
	sort.Slice(tokens, func(i, j int) bool {
		if len(tokens[i]) != len(tokens[j]) {
			return len(tokens[i]) > len(tokens[j])
		}
		return tokens[i] < tokens[j]
	})
	return tokens
}
