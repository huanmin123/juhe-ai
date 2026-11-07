package gatewayresponse

// 响应侧占位符跨 SSE 事件拼合（T174，设计 §7.2 的事件边界补充）。
//
// 字节级 StreamRewriter 只能拼合连续字节里的 token 两半；上游把一个占位符拆进
// 多个 SSE 事件时，两半之间隔着 data/JSON 帧字节，字节级匹配必然失败。本文件
// 在事件边界做一层有界缓存：
//   - 完整事件中某个 JSON 字符串值以映射 token 的真前缀（如 "[[PH"）结尾时，
//     扣住该事件，暂不写下游；
//   - 后续事件若某个字符串值继续该 token（如 "ONE"、"_1]]"），继续扣住；直到
//     某事件补齐完整 token，在前一事件把前缀替换为真实值、在补齐事件删除
//     占位符剩余字节，再把所有扣住事件交给原 StreamRewriter；
//   - 后续事件没有继续时，扣住事件原样放行，行为与旧版透传一致；最多缓存
//     有界数量的事件，不缓存整条响应。
//
// token 长度由 Mapping.maxTokenLen 有界；事件数/字节数另设硬上限，超限原样
// 放行，保证异常上游不会造成无界内存。

import (
	"bytes"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysecpol"
)

const (
	maxHeldSplitEvents = 16
	maxHeldSplitBytes  = 256 * 1024
)

type splitSegment struct {
	eventIndex int
	start      int
	consume    int
}

type heldSplit struct {
	events           [][]byte
	prefixStart      int
	prefixEnd        int
	candidates       map[string]string // token 剩余片段 → 还原值
	continuationSpan []splitSegment
	bytes            int
}

type securityPolicyStreamRestorer struct {
	mapping  *gatewaysecpol.Mapping
	rewriter *gatewaysecpol.StreamRewriter
	pending  []byte
	held     *heldSplit
}

func newSecurityPolicyStreamRestorer(mapping *gatewaysecpol.Mapping) *securityPolicyStreamRestorer {
	if mapping == nil || mapping.Empty() {
		return nil
	}
	return &securityPolicyStreamRestorer{
		mapping:  mapping,
		rewriter: gatewaysecpol.NewStreamRewriter(mapping),
	}
}

// Transform 消费任意上游分片，返回可写下游的完整事件字节。无可见输出时返回
// nil（首字截止判定按无输出处理）。
func (s *securityPolicyStreamRestorer) Transform(chunk []byte) [][]byte {
	if s == nil {
		return nil
	}
	s.pending = append(s.pending, chunk...)
	var out [][]byte
	for {
		end, complete := sseEventEnd(s.pending, 0)
		if !complete {
			break
		}
		event := append([]byte(nil), s.pending[:end]...)
		s.pending = append(s.pending[:0], s.pending[end:]...)
		out = append(out, s.processEvent(event)...)
	}
	return out
}

// Flush 在上游 EOF 时放行扣住/未闭合事件并冲刷 StreamRewriter 残留。
func (s *securityPolicyStreamRestorer) Flush() [][]byte {
	if s == nil {
		return nil
	}
	var out [][]byte
	if s.held != nil {
		out = append(out, s.feed(s.held.events...)...)
		s.held = nil
	}
	if len(s.pending) > 0 {
		out = append(out, s.feed(s.pending)...)
		s.pending = nil
	}
	return append(out, s.rewriter.Flush())
}

func (s *securityPolicyStreamRestorer) Overflowed() int {
	if s == nil {
		return 0
	}
	return s.rewriter.Overflowed()
}

func (s *securityPolicyStreamRestorer) processEvent(event []byte) [][]byte {
	if s.held != nil {
		if out, handled := s.continueHeld(event); handled {
			return out
		}
		// 后续事件没有继续该 token：扣住事件原样放行，当前事件按普通事件处理。
		out := s.feed(s.held.events...)
		s.held = nil
		return append(out, s.handleEvent(event)...)
	}
	return s.handleEvent(event)
}

func (s *securityPolicyStreamRestorer) handleEvent(event []byte) [][]byte {
	if held := s.detectHeldEvent(event); held != nil {
		s.held = held
		return nil
	}
	return s.feed(event)
}

func (s *securityPolicyStreamRestorer) feed(events ...[]byte) [][]byte {
	var out [][]byte
	for _, event := range events {
		if len(event) == 0 {
			continue
		}
		if rewritten := s.rewriter.Rewrite(event); len(rewritten) > 0 {
			out = append(out, rewritten)
		}
	}
	return out
}

// continueHeld 尝试用当前事件继续/补齐 held token。返回 handled=true 表示当前
// 事件已被扣住或已完成拼合并输出；false 表示需要按普通事件处理当前事件。
func (s *securityPolicyStreamRestorer) continueHeld(current []byte) ([][]byte, bool) {
	spans := jsonValueStringSpans(current)
	for _, span := range spans {
		content := string(current[span.start:span.end])
		if consume, value, ok := matchSplitCompletion(s.held.candidates, content); ok {
			out := s.completeHeld(current, span.start, consume, value)
			return out, true
		}
	}
	for _, span := range spans {
		content := string(current[span.start:span.end])
		if continuation, ok := matchSplitPartial(s.held.candidates, content); ok {
			nextCandidates := map[string]string{}
			for rest, value := range s.held.candidates {
				if strings.HasPrefix(rest, continuation) {
					nextCandidates[rest[len(continuation):]] = value
				}
			}
			if len(nextCandidates) == 0 {
				return nil, false
			}
			if len(s.held.events)+1 > maxHeldSplitEvents || s.held.bytes+len(current) > maxHeldSplitBytes {
				return nil, false
			}
			s.held.candidates = nextCandidates
			s.held.events = append(s.held.events, append([]byte(nil), current...))
			s.held.continuationSpan = append(s.held.continuationSpan, splitSegment{
				eventIndex: len(s.held.events) - 1,
				start:      span.start,
				consume:    len(content),
			})
			s.held.bytes += len(current)
			return nil, true
		}
	}
	return nil, false
}

func (s *securityPolicyStreamRestorer) completeHeld(current []byte, start, consume int, value string) [][]byte {
	// 先删除前面事件里已确认属于该 token 的续接字节（整段字符串值），再把
	// 第一事件里的前缀替换为真实值。
	for eventIndex := range s.held.events {
		raw := s.held.events[eventIndex]
		var spans []splitSegment
		for _, segment := range s.held.continuationSpan {
			if segment.eventIndex == eventIndex {
				spans = append(spans, segment)
			}
		}
		for _, segment := range spans {
			raw = append(raw[:segment.start], raw[segment.start+segment.consume:]...)
		}
		s.held.events[eventIndex] = raw
	}
	first := s.held.events[0]
	first = append(first[:s.held.prefixStart], append([]byte(value), first[s.held.prefixEnd:]...)...)
	s.held.events[0] = first
	currentOut := append([]byte(nil), current...)
	currentOut = append(currentOut[:start], currentOut[start+consume:]...)
	out := s.feed(s.held.events...)
	out = append(out, s.feed(currentOut)...)
	s.held = nil
	return out
}

// detectHeldEvent 找事件 JSON 字符串值里以映射 token 真前缀结尾的半截占位符。
func (s *securityPolicyStreamRestorer) detectHeldEvent(event []byte) *heldSplit {
	for _, span := range jsonValueStringSpans(event) {
		content := event[span.start:span.end]
		open := bytes.LastIndex(content, []byte("[["))
		if open < 0 {
			continue
		}
		prefix := content[open:]
		if bytes.Contains(prefix, []byte("]]")) {
			continue
		}
		candidates := s.mapping.SplitCandidates(string(prefix))
		if len(candidates) == 0 {
			continue
		}
		return &heldSplit{
			events:      [][]byte{append([]byte(nil), event...)},
			prefixStart: span.start + open,
			prefixEnd:   span.end,
			candidates:  candidates,
			bytes:       len(event),
		}
	}
	return nil
}

// matchSplitCompletion 用当前字符串值补齐 candidate；返回需消费的前缀长度
// 与还原值。多个候选取最长剩余片段，避免短 token 抢先匹配。
func matchSplitCompletion(candidates map[string]string, content string) (consume int, value string, ok bool) {
	for rest, entryValue := range candidates {
		if !strings.HasPrefix(content, rest) {
			continue
		}
		if len(rest) > consume {
			consume = len(rest)
			value = entryValue
			ok = true
		}
	}
	return consume, value, ok
}

// matchSplitPartial 判断当前字符串值是否只是 candidate 剩余片段的下一段；
// 是则返回该段文本，供继续扣住下一事件。
func matchSplitPartial(candidates map[string]string, content string) (string, bool) {
	if content == "" {
		return "", false
	}
	for rest := range candidates {
		if strings.HasPrefix(rest, content) {
			return content, true
		}
	}
	return "", false
}

type jsonStringSpan struct {
	start int
	end   int
}

// jsonValueStringSpans 扫描 JSON 对象字符串值（冒号后的字符串），跳过键。
// 占位符只出现在文本值中；限制为值可避免把键名当 token 续接。支持转义。
func jsonValueStringSpans(raw []byte) []jsonStringSpan {
	var spans []jsonStringSpan
	previousSignificant := byte(0)
	for index := 0; index < len(raw); index++ {
		if raw[index] != '"' {
			if raw[index] != ' ' && raw[index] != '\t' && raw[index] != '\r' && raw[index] != '\n' {
				previousSignificant = raw[index]
			}
			continue
		}
		start := index + 1
		index++
		for index < len(raw) {
			if raw[index] == '\\' {
				index += 2
				continue
			}
			if raw[index] == '"' {
				if previousSignificant == ':' {
					spans = append(spans, jsonStringSpan{start: start, end: index})
				}
				previousSignificant = '"'
				break
			}
			index++
		}
	}
	return spans
}
