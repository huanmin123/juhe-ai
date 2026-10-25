package gatewaybody

import (
	"crypto/sha256"
	"encoding/hex"
)

// 内容指纹会话身份（docs/functions/内容指纹会话身份设计.md §4）：无官方会话
// Header 的客户端以 messages 对话序列的前缀锚点派生降级会话身份。本文件提供
// 两块纯能力：有界锚点扫描器（§4.1）与两档指纹纯函数（§4.2）。消费接缝为
// /v1 preauth 会话身份接缝与统一客户端来源标识阶梯，二者经请求级记忆化共享
// 同一份计算。
//
// 扫描器是有界前向扫描，不是完整解析：定位顶层 messages 数组后依序收集元素
// 原始字节——S 段（前导连续 system/developer 元素，developer 视同 system）、
// U1/U2（第一、二条 role=user 元素，U2 允许间隔 assistant/tool）、A1（第一
// 条 role=assistant 元素）。四锚齐备后不再读取元素内容，仅以 skip 级成本
// （skipJSONValue 跳读）走完数组尾部统计元素总数——谱系判别器需要精确的
// messages 数组长度。成本界 O(至终止点的字节前缀 + 数组尾跳读)，长对话尾部
// 消息的内容不被读取、不参与哈希。
//
// canonical 直接使用元素原始字节（不重序列化、不解码）：同一客户端同一会话
// 跨轮重发逐字节稳定，不同会话由内容区分；不要求跨客户端/跨实现的位级规范
// 型（systemAccountID + apiKeyID + clientProfile 已入指纹哈希做隔离）。
//
// 失败语义 fail-closed 到现状：body 非 JSON object、无顶层 messages 数组、
// 被内容检查的元素缺字符串 role、扫描前缀内 JSON 截断/畸形 → 无锚点
// （ok=false），调用方维持无指纹身份，与缺官方 Header 的现状行为一致。

// 内容指纹的两处消费接缝共用的语义 namespace 与派生值前缀（设计 §4.3/§6.2）：
// 审计 SessionID = "cfp1-" + provisionalHex，亲和确认档原始值 = "cfp1c-" +
// confirmedHex，来源档 stableValue = provisionalHex（无前缀裸哈希）。
const (
	ContentFingerprintNamespace       = "content_fingerprint:v1"
	ContentFingerprintSessionIDPrefix = "cfp1-"
	ContentFingerprintConfirmedPrefix = "cfp1c-"
)

// 指纹哈希域分隔词（设计 §4.2 固定式）：临时档 "cfp1"、确认档 "cfp1c"。
const (
	contentFingerprintProvisionalDomain = "cfp1"
	contentFingerprintConfirmedDomain   = "cfp1c"
)

// contentFingerprintAnchorSeparator 是 hashS 多元素拼接分隔字节（0x00）。
const contentFingerprintAnchorSeparator = 0x00

// ConversationAnchors 持有扫描产出的锚点。全部字节切片直接指向扫描输入
// raw 的原字（零复制）；raw 被改写时锚点随之失效。
type ConversationAnchors struct {
	// SystemElements 是 S 段：前导连续 role=system/developer 元素的原始字节，
	// 依序保存；空 S 段合法（会话可无 system 前缀，hashS = 空拼接的 sha256）。
	SystemElements [][]byte
	// FirstUser / FirstAssistant / SecondUser 是 U1 / A1 / U2 的原始字节；
	// nil 表示该锚点在 messages 数组中不存在。
	FirstUser      []byte
	FirstAssistant []byte
	SecondUser     []byte
	// MessageCount 是 messages 数组的元素总数（精确值，供谱系判别器比较
	// 会话演化）。仅在扫描成功时有效。
	MessageCount int
}

// ProvisionalComputable 报告临时指纹是否可计算：U1 存在（S 段允许为空）。
func (a ConversationAnchors) ProvisionalComputable() bool {
	return a.FirstUser != nil
}

// ConfirmedComputable 报告确认指纹是否可计算：U2 存在且 A1 存在（合法对话中
// A1 先于 U2 出现，此处显式要求以钉死形状）。U2 一经出现即永久存在于上下文
// （客户端全量重发，append-only），确认档自该请求起可计算。
func (a ConversationAnchors) ConfirmedComputable() bool {
	return a.FirstUser != nil && a.FirstAssistant != nil && a.SecondUser != nil
}

// ScanConversationAnchors 从原始请求 body 字节前向扫描对话锚点（设计 §4.1）。
// ok=false 表示无锚点（失败语义见文件头注释，或结构有效但无 U1——无 user
// 消息的 body 与无 messages 同型，不产生指纹身份）。扫描只消费 messages 数
// 组闭合前的字节前缀；数组闭合后 body 的其余部分不被读取、不参与校验（完
// 整 body 的有效性由 driver 阶段的物化路径负责）。顶层同名 messages 键取
// 首个出现的（真实客户端不发送重复键，确定性即可）。
func ScanConversationAnchors(raw []byte) (ConversationAnchors, bool) {
	var anchors ConversationAnchors
	index := skipJSONWhitespace(raw, 0)
	if index >= len(raw) || raw[index] != jsonObjectOpenByte {
		return anchors, false
	}
	index++
	foundMessages := false
	for index < len(raw) && !foundMessages {
		index = skipJSONWhitespace(raw, index)
		if index >= len(raw) {
			return ConversationAnchors{}, false
		}
		if raw[index] == jsonObjectCloseByte {
			// 顶层对象闭合且未出现 messages。
			return ConversationAnchors{}, false
		}
		if raw[index] == jsonCommaByte {
			index++
			continue
		}
		key, ok := readJSONStringToken(raw, index)
		if !ok {
			return ConversationAnchors{}, false
		}
		index = skipJSONWhitespace(raw, key.nextIndex)
		if index >= len(raw) || raw[index] != jsonColonByte {
			return ConversationAnchors{}, false
		}
		index = skipJSONWhitespace(raw, index+1)
		if key.value == "messages" {
			result, ok := scanMessagesArray(raw, index)
			if !ok {
				return ConversationAnchors{}, false
			}
			anchors = result
			foundMessages = true
			break
		}
		skipped := skipJSONValue(raw, index)
		if !skipped.ok {
			return ConversationAnchors{}, false
		}
		index = skipped.nextIndex
	}
	if !foundMessages || !anchors.ProvisionalComputable() {
		return ConversationAnchors{}, false
	}
	return anchors, true
}

// scanMessagesArray 遍历 messages 数组收集锚点。元素内容检查在四锚齐备后停
// 止（尾部元素只以 skipJSONValue 计数，缺 role 不再判失败——设计 §4.1 的成
// 本界要求尾部消息根本不被读取）。数组未闭合（截断）→ ok=false。
func scanMessagesArray(raw []byte, index int) (ConversationAnchors, bool) {
	var anchors ConversationAnchors
	if index >= len(raw) || raw[index] != jsonArrayOpenByte {
		// messages 存在但不是数组：无顶层 messages 数组。
		return anchors, false
	}
	index++
	// sOpen 表示 S 段仍在收集（尚未出现第一个非 system/developer 元素）。
	sOpen := true
	anchorsComplete := func() bool {
		return anchors.FirstUser != nil && anchors.FirstAssistant != nil && anchors.SecondUser != nil
	}
	for index < len(raw) {
		index = skipJSONWhitespace(raw, index)
		if index >= len(raw) {
			return ConversationAnchors{}, false
		}
		if raw[index] == jsonArrayCloseByte {
			return anchors, true
		}
		if raw[index] == jsonCommaByte {
			index++
			continue
		}
		start := index
		skipped := skipJSONValue(raw, index)
		if !skipped.ok {
			return ConversationAnchors{}, false
		}
		index = skipped.nextIndex
		anchors.MessageCount++
		if anchorsComplete() {
			// 四锚齐备：尾部仅跳读计数（skip 级成本），不读取内容。
			continue
		}
		element := raw[start:index]
		role, ok := messageRole(element)
		if !ok {
			return ConversationAnchors{}, false
		}
		switch role {
		case "system", "developer":
			if sOpen {
				anchors.SystemElements = append(anchors.SystemElements, element)
			}
		default:
			sOpen = false
		}
		switch role {
		case "user":
			if anchors.FirstUser == nil {
				anchors.FirstUser = element
			} else if anchors.SecondUser == nil {
				anchors.SecondUser = element
			}
		case "assistant":
			if anchors.FirstAssistant == nil {
				anchors.FirstAssistant = element
			}
		}
	}
	// 数组未闭合即到达 body 末尾：截断。
	return ConversationAnchors{}, false
}

// messageRole 读取单个 messages 元素顶层 "role" 属性的字符串值。元素必须是
// JSON object 且携带字符串 role，否则 ok=false（元素缺字符串 role，失败语
// 义之一）。仅读元素自身字节切片，不影响外层扫描游标。
func messageRole(element []byte) (string, bool) {
	index := skipJSONWhitespace(element, 0)
	if index >= len(element) || element[index] != jsonObjectOpenByte {
		return "", false
	}
	index++
	for index < len(element) {
		index = skipJSONWhitespace(element, index)
		if index >= len(element) {
			return "", false
		}
		if element[index] == jsonObjectCloseByte {
			return "", false
		}
		if element[index] == jsonCommaByte {
			index++
			continue
		}
		key, ok := readJSONStringToken(element, index)
		if !ok {
			return "", false
		}
		index = skipJSONWhitespace(element, key.nextIndex)
		if index >= len(element) || element[index] != jsonColonByte {
			return "", false
		}
		index = skipJSONWhitespace(element, index+1)
		if key.value == "role" {
			value, ok := readJSONStringToken(element, index)
			if !ok {
				return "", false
			}
			return value.value, true
		}
		skipped := skipJSONValue(element, index)
		if !skipped.ok {
			return "", false
		}
		index = skipped.nextIndex
	}
	return "", false
}

// ProvisionalFingerprintHex 计算临时指纹（设计 §4.2 固定式）：
// hex(sha256("cfp1" 0x00 systemAccountID 0x00 apiKeyID 0x00 clientProfile
// 0x00 hashS 0x00 hashU1))，其中 hashS = 各 S 元素字节以 0x00 分隔依序拼接
// 后 sha256，hashU1 = U1 元素字节 sha256。锚点不可计算（无 U1）时返回空串。
func ProvisionalFingerprintHex(systemAccountID, apiKeyID, clientProfile string, anchors ConversationAnchors) string {
	if !anchors.ProvisionalComputable() {
		return ""
	}
	return contentFingerprintHex(
		contentFingerprintProvisionalDomain,
		systemAccountID, apiKeyID, clientProfile,
		hashAnchorSegments(anchors.SystemElements),
		hashAnchorBytes(anchors.FirstUser),
	)
}

// ConfirmedFingerprintHex 计算确认指纹（设计 §4.2 固定式）：同型 "cfp1c"
// 前缀 + hashA1 + hashU2 追加。锚点不可计算（缺 U1/A1/U2 任一）时返回空串。
func ConfirmedFingerprintHex(systemAccountID, apiKeyID, clientProfile string, anchors ConversationAnchors) string {
	if !anchors.ConfirmedComputable() {
		return ""
	}
	return contentFingerprintHex(
		contentFingerprintConfirmedDomain,
		systemAccountID, apiKeyID, clientProfile,
		hashAnchorSegments(anchors.SystemElements),
		hashAnchorBytes(anchors.FirstUser),
		hashAnchorBytes(anchors.FirstAssistant),
		hashAnchorBytes(anchors.SecondUser),
	)
}

// contentFingerprintHex 组装固定式：domain 后每段以 0x00 分隔，末段为各锚点
// 的 32 字节 sha256 摘要（原始摘要字节入哈希，hex 只出现在最终输出）。
func contentFingerprintHex(domain, systemAccountID, apiKeyID, clientProfile string, anchorHashes ...[]byte) string {
	h := sha256.New()
	h.Write([]byte(domain))
	for _, part := range [][]byte{
		[]byte(systemAccountID),
		[]byte(apiKeyID),
		[]byte(clientProfile),
	} {
		h.Write([]byte{contentFingerprintAnchorSeparator})
		h.Write(part)
	}
	for _, hash := range anchorHashes {
		h.Write([]byte{contentFingerprintAnchorSeparator})
		h.Write(hash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// hashAnchorSegments 计算多元素 S 段哈希：各元素以 0x00 分隔依序拼接后
// sha256；空 S 段 = 空拼接的 sha256。
func hashAnchorSegments(elements [][]byte) []byte {
	h := sha256.New()
	for i, element := range elements {
		if i > 0 {
			h.Write([]byte{contentFingerprintAnchorSeparator})
		}
		h.Write(element)
	}
	return h.Sum(nil)
}

// hashAnchorBytes 计算单锚点元素字节的 sha256。
func hashAnchorBytes(anchor []byte) []byte {
	sum := sha256.Sum256(anchor)
	return sum[:]
}
