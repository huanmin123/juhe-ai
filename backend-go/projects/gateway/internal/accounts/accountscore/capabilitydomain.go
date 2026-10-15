package accountscore

import "strings"

// 账户能力域（AI 账户能力域拆分，PLAN-20261011T141004000Z）：域枚举与列
// CHECK 四值一致（maintenance/internal/schema accounts DDL 为权威 DDL）。
// M1 是纯读通道：域创建时确定、不可更改（D3），这里只承载读侧过滤契约，
// 不承载任何保存规则（M2 交付）。
const (
	// CapabilityDomainChat 对话域，也是列的 NOT NULL DEFAULT。
	CapabilityDomainChat = "chat"
	// CapabilityDomainVideo 视频域。
	CapabilityDomainVideo = "video"
	// CapabilityDomainAudio 音频域。
	CapabilityDomainAudio = "audio"
	// CapabilityDomainImage 图像域。
	CapabilityDomainImage = "image"
)

// AccountCapabilityDomainSet 是 domain 列表过滤参数的合法取值集（四枚举）。
var AccountCapabilityDomainSet = map[string]bool{
	CapabilityDomainChat:  true,
	CapabilityDomainVideo: true,
	CapabilityDomainAudio: true,
	CapabilityDomainImage: true,
}

// NormalizeAccountCapabilityDomain 返回裁剪后的域值；合法时 ok=true。
// 空串视为「不过滤」，原样返回且 ok=true。
func NormalizeAccountCapabilityDomain(value string) (domain string, ok bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", true
	}
	return trimmed, AccountCapabilityDomainSet[trimmed]
}

// SkipsConversationHealthStateMachine 报告能力域是否不参与对话健康检查状态
// 机（媒体账户生命周期定案表）：audio/video 域账户无后台探针执行、不产生
// 上游计费，health_check_endpoint_mode 仅为记录值（见
// DefaultHealthCheckEndpointModeForDomain）——创建默认 active、编辑连接变化
// 不降级 pending_test、人工恢复（ClearFailureState）直达 active、冷却到期后
// 媒体请求可再次准入。image 与 chat 域返回 false（现行状态机行为不变；image
// 虽属媒体域，仍走对话健康检查生命周期）。
func SkipsConversationHealthStateMachine(domain string) bool {
	switch domain {
	case CapabilityDomainAudio, CapabilityDomainVideo:
		return true
	}
	return false
}
