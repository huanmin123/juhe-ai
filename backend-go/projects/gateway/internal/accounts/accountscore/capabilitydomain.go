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
