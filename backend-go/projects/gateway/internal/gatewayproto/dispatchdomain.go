package gatewayproto

// DispatchDomain 是 /v1 派发链的调度域（对话与非对话媒体调度分离设计 §1/§5）：
// 网关保留同一条认证、权限和上游传输基础设施，但调度策略按域分流——
//
//	ConversationDispatch   ：Chat、Responses、Messages、Gemini 对话（文本域）。
//	                         保持会话连续性，消费会话亲和、缓存率排序和复杂恢复。
//	StatelessMediaDispatch ：图像、同步音频、视频创建、长音频创建（媒体创建域）。
//	                         快速找到一个可承接账户；失败时按受理边界有限换号，
//	                         不产生/写回会话亲和、不进入热质量与速度优先、不等待
//	                         账户恢复、不派发请求失败健康检查。
//	DispatchDomainRealtime ：Realtime 连接生命周期能力（设计 §1：不归入媒体创建
//	                         域）。GET /v1/realtime WS 升级在链入口短路进专用
//	                         handler，不进 preflight / 派发循环，本域仅作显式
//	                         词表位。
//
// 域判定是 lane 的纯函数（ResolveDispatchDomain 的 lane 投影）：lane 由
// gatewayopenai.ResolveRequestLane 在请求入口唯一解析（路径族优先 + 模型目录
// 回退），各消费方不再各自维护词表。
type DispatchDomain string

const (
	DispatchDomainConversation   DispatchDomain = "conversation"
	DispatchDomainStatelessMedia DispatchDomain = "stateless_media"
	DispatchDomainRealtime       DispatchDomain = "realtime"
)

// DispatchDomainOfLane 把请求车道投影为调度域。未知值（空串等）按文本对话域
// 处理——历史调用方在 lane 解析前的兜底形态保持对话语义。
func DispatchDomainOfLane(lane RequestLane) DispatchDomain {
	switch lane {
	case LaneImage, LaneAudio, LaneVideo:
		return DispatchDomainStatelessMedia
	case LaneRealtime:
		return DispatchDomainRealtime
	default:
		return DispatchDomainConversation
	}
}
