package accountscore

import (
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/scenario"
)

// 账户能力域（AI 账户能力域拆分，PLAN-20261011T141004000Z）：域枚举与
// 场景注册表一致（scenario 包是唯一来源）；M1 是纯读通道：域创建时确定、
// 不可更改（D3），这里只承载读侧过滤契约，不承载任何保存规则（M2 交付）。
const (
	// CapabilityDomainChat 对话域，也是列的 NOT NULL DEFAULT。
	CapabilityDomainChat = string(scenario.TypeChat)
	// CapabilityDomainVideo 视频域。
	CapabilityDomainVideo = string(scenario.TypeVideo)
	// CapabilityDomainAudio 音频域。
	CapabilityDomainAudio = string(scenario.TypeAudio)
	// CapabilityDomainImage 图像域。
	CapabilityDomainImage = string(scenario.TypeImage)
	// CapabilityDomainEmbedding 向量域（B7）。
	CapabilityDomainEmbedding = string(scenario.TypeEmbedding)
	// CapabilityDomainRerank 重排域（B7）。
	CapabilityDomainRerank = string(scenario.TypeRerank)
	// CapabilityDomainModeration 安全域（B7）。
	CapabilityDomainModeration = string(scenario.TypeModeration)
)

// AccountCapabilityDomainSet 是 domain 列表过滤参数的合法取值集。
// 由场景注册表派生：新增场景只改 scenario.Profile，不在这里加字面量。
var AccountCapabilityDomainSet = func() map[string]bool {
	set := make(map[string]bool, len(scenario.All()))
	for _, profile := range scenario.All() {
		set[string(profile.Type)] = true
	}
	return set
}()

// NormalizeAccountCapabilityDomain 返回裁剪后的域值；合法时 ok=true。
// 空串视为「不过滤」，原样返回且 ok=true。
func NormalizeAccountCapabilityDomain(value string) (domain string, ok bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", true
	}
	return trimmed, AccountCapabilityDomainSet[trimmed]
}
