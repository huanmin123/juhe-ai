package accountscore

import (
	"strings"

	capabilitydomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
)

// 账户能力域（AI 账户能力域拆分，PLAN-20261011T141004000Z；2026-11-21 向量/
// 重排拆分设计 §1 扩六值）：域枚举与列 CHECK 六值一致（maintenance/internal/
// schema accounts DDL 为权威 DDL）。
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
	// CapabilityDomainEmbedding 向量域（向量/重排拆分设计 §3）。
	CapabilityDomainEmbedding = "embedding"
	// CapabilityDomainRerank 重排域（向量/重排拆分设计 §3）。
	CapabilityDomainRerank = "rerank"
)

// AccountCapabilityDomainSet 是 domain 列表过滤参数的合法取值集（六枚举）。
var AccountCapabilityDomainSet = map[string]bool{
	CapabilityDomainChat:      true,
	CapabilityDomainVideo:     true,
	CapabilityDomainAudio:     true,
	CapabilityDomainImage:     true,
	CapabilityDomainEmbedding: true,
	CapabilityDomainRerank:    true,
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

// UsesConnectionHealthCheck 判定账户健康检查是否走连通性探测（连接测试）
// 而非 J1 模型测试：媒体域与向量/重排域恒连接（拆分设计 §7：不进 J1、无人工
// 测试请求模板）；chat 域只有声明真实对话生成形态才走模型测试，
// embeddings/rerank/count_tokens 等输入型账户走连接测试。
func UsesConnectionHealthCheck(domain string, modes []string) bool {
	return capabilitydomain.TestKindForAccount(domain, modes) == capabilitydomain.TestKindConnection
}
