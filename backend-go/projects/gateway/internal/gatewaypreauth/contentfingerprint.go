package gatewaypreauth

import (
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
)

// 内容指纹会话身份的请求级记忆化（docs/functions/内容指纹会话身份设计.md
// §4.3）：锚点扫描与两档指纹结果按 clientProfile 记忆化在 GatewayRequest
// 上，会话身份接缝（cmd 的 sessionIdentityAdapter）与统一客户端来源标识阶梯
// （gatewaycodex.ResolveGatewayClientSourceIdentity）共享同一份计算——每请求
// 每个 profile 至多执行一次扫描与哈希。记忆化只缓存派生值，不缓存谱系判定
// 结果：LRU 判定只在亲和确认档产出点（adapter 侧）发生，来源阶梯只消费
// provisionalHex。
//
// 载荷是纯字符串/标量字段；本包不依赖 gatewaysession，两接缝各自取用。

// ContentFingerprintMemo 是单 profile 的记忆化载荷。
type ContentFingerprintMemo struct {
	// Profile 是记忆化键（检测画像）；键不匹配的读取视为未命中。
	Profile string
	// AnchorsOK 表示锚点可用（body 可扫描出 messages 锚点，临时指纹可派生）。
	AnchorsOK bool
	// MessageCount 是 messages 数组元素总数（谱系判别器输入）；仅 AnchorsOK
	// 时有效。
	MessageCount int
	// ProvisionalHex 是临时指纹 hex（审计 SessionID 与来源档 stableValue 的
	// 派生输入，无前缀裸哈希）；仅 AnchorsOK 时有效。
	ProvisionalHex string
	// ConfirmedHex 是确认指纹 hex；仅 ConfirmedComputable 时有效。
	ConfirmedHex string
	// ConfirmedComputable 表示确认档锚点 {S,U1,A1,U2} 齐备。
	ConfirmedComputable bool
}

// ContentFingerprintMemoFor 返回该 profile 的记忆化；未命中时从 RawBody 前向
// 扫描计算并写回（同请求同 profile 的后续消费方直接读，不重复扫描）。
// Body/RawBody 为 nil（未捕获）或扫描失败 → 无锚点载荷照常写回，后续消费方
// 命中同一份"无锚点"结论。指纹输入含 systemAccountID/apiKeyID，本请求内恒
// 定（认证先于 preflight 解析完成），故记忆化键只需 profile。
func (req *GatewayRequest) ContentFingerprintMemoFor(clientProfile, systemAccountID, apiKeyID string) ContentFingerprintMemo {
	// 惰性初始化非原子：调用方须在请求生命周期内顺序调用（现状成立：
	// preflight 两次 strategy 解析、失败时刻来源重解析、派发克隆均为先后
	// 发生）；并发首调不构成数据正确性问题（各建各的 store、结果确定性
	// 同源），但会产生重复计算。
	if req.contentFingerprintMemos == nil {
		req.contentFingerprintMemos = &contentFingerprintMemoStore{}
	}
	store := req.contentFingerprintMemos
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.items == nil {
		store.items = make(map[string]ContentFingerprintMemo)
	}
	if memo, ok := store.items[clientProfile]; ok {
		return memo
	}
	memo := computeContentFingerprintMemo(req, clientProfile, systemAccountID, apiKeyID)
	store.items[clientProfile] = memo
	return memo
}

// computeContentFingerprintMemo 执行一次有界锚点扫描与两档指纹计算（设计
// §4.1/§4.2）。零 ParseRequestJSONBody、零完整物化：只读 RawBody 原始字节。
func computeContentFingerprintMemo(req *GatewayRequest, clientProfile, systemAccountID, apiKeyID string) ContentFingerprintMemo {
	memo := ContentFingerprintMemo{Profile: clientProfile}
	if req == nil || req.Body == nil || len(req.Body.RawBody) == 0 {
		return memo
	}
	anchors, ok := gatewaybody.ScanConversationAnchors(req.Body.RawBody)
	if !ok {
		return memo
	}
	memo.AnchorsOK = true
	memo.MessageCount = anchors.MessageCount
	memo.ProvisionalHex = gatewaybody.ProvisionalFingerprintHex(systemAccountID, apiKeyID, clientProfile, anchors)
	if anchors.ConfirmedComputable() {
		memo.ConfirmedComputable = true
		memo.ConfirmedHex = gatewaybody.ConfirmedFingerprintHex(systemAccountID, apiKeyID, clientProfile, anchors)
	}
	return memo
}
