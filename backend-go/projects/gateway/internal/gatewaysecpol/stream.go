package gatewaysecpol

import (
	"bytes"
)

// 响应侧占位符还原（设计 §7.1 还原范围与类型保持 / §7.2 流式跨 chunk 匹配）。
// 还原是字节级白名单匹配：只还原映射表中存在的完整 token；不在表内的
// [[LABEL_N]]（如客户端正文自带的字面量）原样透传。已知边界（§7.3）：模型改
// 写占位符、多轮存态旧占位符撞号等场景按设计接受，不围堵。

var (
	sepOpen  = []byte("[[")
	sepClose = []byte("]]")
)

// streamHoldBase 是悬挂缓冲基础上限（设计 §7.2：64 字节 + 本请求最长占位符
// 长度）。
const streamHoldBase = 64

// StreamRewriter 是响应侧占位符感知重写器：字节级、有界悬挂缓冲，逐 chunk
// 消费上游文本流，完整命中映射表的 token 还原为原值。非并发安全——每个文本
// 流挂一个实例。Rewrite 是纯函数式调用：返回处理后字节，内部仅保留悬挂缓冲。
type StreamRewriter struct {
	mapping    *Mapping
	buf        []byte
	maxHold    int
	overflowed int
}

// NewStreamRewriter 创建还原器；mapping 为 nil 或空时 Rewrite 原样透传（纯
// mask 策略零响应侧成本，§7.1）。
func NewStreamRewriter(m *Mapping) *StreamRewriter {
	maxTokenLen := 0
	if m != nil {
		maxTokenLen = m.maxTokenLen
	}
	return &StreamRewriter{mapping: m, maxHold: streamHoldBase + maxTokenLen}
}

// Rewrite 消费一个 chunk，返回可安全放行的处理后字节；潜在不完整 token 前缀
// 留在悬挂缓冲等待后续字节（含潜在独占叶子包裹的开引号与尾部孤立引号）。
func (r *StreamRewriter) Rewrite(chunk []byte) []byte {
	if r.mapping == nil || r.mapping.Empty() {
		return chunk
	}
	r.buf = append(r.buf, chunk...)
	return r.drain(false)
}

// Flush 在流终态（终态事件 / [DONE] / message_stop）冲刷残留缓冲：剩余字节
// 原样放行（未闭合 token 已不可能再补全）。
func (r *StreamRewriter) Flush() []byte {
	if r.mapping == nil || r.mapping.Empty() {
		return nil
	}
	out := r.drain(true)
	out = append(out, r.buf...)
	r.buf = nil
	return out
}

// Overflowed 返回悬挂缓冲超限放弃匹配的累计次数（设计 §7.2 第 3 条
// placeholder_flush_overflow 计数口径）：供响应侧挂重写器的出口记运行日志计
// 数（不含任何内容）。
func (r *StreamRewriter) Overflowed() int {
	return r.overflowed
}

// drain 处理悬挂缓冲：还原全部完整命中 token，输出可放行前缀。final=true 时
// 不保留任何悬挂（终态冲刷语义）。
func (r *StreamRewriter) drain(final bool) []byte {
	buf := r.buf
	var out []byte
	pos := 0
	for pos < len(buf) {
		rel := bytes.Index(buf[pos:], sepOpen)
		if rel < 0 {
			if final {
				out = append(out, buf[pos:]...)
				pos = len(buf)
				break
			}
			end := holdSafeEnd(buf, pos)
			out = append(out, buf[pos:end]...)
			pos = end
			break
		}
		s := pos + rel
		closeRel := bytes.Index(buf[s+2:], sepClose)
		if closeRel < 0 {
			if final {
				// 终态：未闭合 token 原样放行。
				out = append(out, buf[pos:]...)
				pos = len(buf)
				break
			}
			if len(buf)-s > r.maxHold {
				// §7.2 第 3 条：缓冲超限仍不完整 → 放弃匹配、原样放行；从
				// [[ 之后继续扫描（放行区间内可能存在更短的完整 token）。所
				// 有映射 token 长度 ≤ maxHold，被放弃的序列必然不是映射项。
				r.overflowed++
				out = append(out, buf[pos:s+2]...)
				pos = s + 2
				continue
			}
			hold := s
			if s > 0 && buf[s-1] == '"' {
				if s > 1 && buf[s-2] == '\\' {
					// 转义形态 \"（JSON 字符串内层 JSON）的潜在独占叶子：包裹
					// 字节对（\ + "）一并扣下。
					hold = s - 2
				} else {
					hold = s - 1 // 潜在独占叶子包裹的开引号一并扣下
				}
			}
			out = append(out, buf[pos:hold]...)
			pos = hold
			break
		}
		e := s + 2 + closeRel + 2
		token := string(buf[s:e])
		entry, ok := r.mapping.lookup(token)
		if !ok {
			// 白名单外 token 原样透传（§3.4 / §12 验收 5）。
			out = append(out, buf[pos:e]...)
			pos = e
			continue
		}
		// §7.1 类型保持：独占叶子引号包裹形态且原类型 number → 整个带引号
		// token 替换为裸数字字面量；嵌在更长文本中 → 仅替换 token。包裹形态
		// 两种：普通 JSON 字节 "TOKEN"，以及 JSON 字符串内层转义形态
		// \"TOKEN\"——工具参数 arguments 的真实字节形态（反斜杠在前、引号紧
		// 贴 token），替换须把包裹的转义反斜杠与引号一并消费。
		if entry.isNumber && s > 0 && buf[s-1] == '"' && s > 1 && buf[s-2] == '\\' {
			if e+1 >= len(buf) && !final {
				// 收尾转义引号未到，扣下开引号对等待下一字节再判定。
				out = append(out, buf[pos:s-2]...)
				pos = s - 2
				break
			}
			if e+1 < len(buf) && buf[e] == '\\' && buf[e+1] == '"' {
				out = append(out, buf[pos:s-2]...)
				out = append(out, entry.value...)
				pos = e + 2
				continue
			}
		} else if entry.isNumber && s > 0 && buf[s-1] == '"' {
			if e == len(buf) && !final {
				// 收尾引号未到，扣下开引号等待下一字节再判定。
				out = append(out, buf[pos:s-1]...)
				pos = s - 1
				break
			}
			if e < len(buf) && buf[e] == '"' {
				out = append(out, buf[pos:s-1]...)
				out = append(out, entry.value...)
				pos = e + 1
				continue
			}
		}
		out = append(out, buf[pos:s]...)
		out = append(out, entry.value...)
		pos = e
	}
	if pos == len(buf) {
		r.buf = r.buf[:0]
	} else {
		r.buf = append(r.buf[:0], buf[pos:]...)
	}
	return out
}

// holdSafeEnd 计算无 "[[" 区间内可安全放行的终点：尾部单个 "["（可能与下一
// chunk 组成 "[["）、紧邻其前的开引号（潜在独占包裹）、可能成为下一 chunk
// 开引号的尾部孤立双引号，以及转义开引号对 \"（潜在转义独占叶子），暂扣不
// 发——各项 ≤2 字节，悬挂有界。
func holdSafeEnd(buf []byte, pos int) int {
	end := len(buf)
	if end-1 >= pos && buf[end-1] == '[' {
		end--
		if end-1 >= pos && buf[end-1] == '"' {
			end--
			if end-1 >= pos && buf[end-1] == '\\' {
				end--
			}
		}
		return end
	}
	if end-1 >= pos && buf[end-1] == '"' {
		end--
		if end-1 >= pos && buf[end-1] == '\\' {
			end--
		}
	}
	return end
}

// RewriteFull 对非流式响应整体做占位符还原：白名单匹配映射表内完整 token；
// 独占引号包裹的 number 占位符（"TOKEN" 与 JSON 字符串内层转义形态
// \"TOKEN\"——工具参数 arguments 的真实字节形态）还原为裸数字字面量，其余
// 还原为原值文本。按 token 长度降序替换，避免任何潜在前缀嵌套干扰；不在映
// 射表内的 token 不动。
func RewriteFull(m *Mapping, body []byte) []byte {
	if m == nil || m.Empty() {
		return body
	}
	for _, token := range m.tokensByLengthDesc() {
		entry := m.tokens[token]
		if entry.isNumber {
			escaped := make([]byte, 0, len(token)+4)
			escaped = append(escaped, '\\', '"')
			escaped = append(escaped, token...)
			escaped = append(escaped, '\\', '"')
			body = bytes.ReplaceAll(body, escaped, []byte(entry.value))
			quoted := make([]byte, 0, len(token)+2)
			quoted = append(quoted, '"')
			quoted = append(quoted, token...)
			quoted = append(quoted, '"')
			body = bytes.ReplaceAll(body, quoted, []byte(entry.value))
		}
		body = bytes.ReplaceAll(body, []byte(token), []byte(entry.value))
	}
	return body
}
