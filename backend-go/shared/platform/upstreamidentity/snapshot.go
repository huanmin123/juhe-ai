package upstreamidentity

import "net/http"

// Snapshot 是一次上游请求构造的身份原子快照（上游客户端身份版本覆盖文档
// 「请求内身份快照（2026-11-06）」节）：Effective* 每次调用独立读取两个原子
// 层，gateway 60s ticker 与 jobs 设置写入可在同一次上游请求的两次取值之间
// 完成刷新，造成 UA 与 body 版本字段自相矛盾（上游可据此判非正版）。Snapshot
// 在构造时对手动层（clientVersionOverrides）与自动层（clientVersionAutoOverrides）
// 各 Load 一次，五族生效版本与全部 UA 拼接从同一对 map 实例派生；无覆盖时与
// Effective* 逐字一致。快照保证请求内一致；跨请求随热刷新漂移属预期。
// 底层 map 不导出：只经版本 getter 与 UA 拼接消费。适用判据：同一次上游请求
// 构造消费 >=2 个身份值的点必须改用快照；单值调用点继续使用 Effective*。
type Snapshot struct {
	manual map[string]string
	auto   map[string]string
}

// NewSnapshot 构造请求内身份快照：手动层与自动层各读取一次。构造后的
// Set* 全量替换不影响已构造快照（两层为替换式存储的实例引用，快照持有
// 构造时刻的实例）。
func NewSnapshot() Snapshot {
	manual, _ := clientVersionOverrides.Load().(map[string]string)
	auto, _ := clientVersionAutoOverrides.Load().(map[string]string)
	return Snapshot{manual: manual, auto: auto}
}

// snapshotVersion 三层合并（手动 > 自动 > 内置），合并顺序与
// EffectiveClientVersion 一致；nil 层跳过，未知家族返回 ""。
func (s Snapshot) snapshotVersion(family string) string {
	if s.manual != nil {
		if version, ok := s.manual[family]; ok {
			return version
		}
	}
	if s.auto != nil {
		if version, ok := s.auto[family]; ok {
			return version
		}
	}
	return BuiltInClientVersion(family)
}

// 快照版本 getter：五个客户端家族的生效版本（手动覆盖 > 自动覆盖 > 内置）。
func (s Snapshot) CodexVersion() string      { return s.snapshotVersion(clientFamilyCodex) }
func (s Snapshot) ClaudeCodeVersion() string { return s.snapshotVersion(clientFamilyClaudeCode) }
func (s Snapshot) GeminiCLIVersion() string  { return s.snapshotVersion(clientFamilyGeminiCLI) }
func (s Snapshot) ZCodeVersion() string      { return s.snapshotVersion(clientFamilyZCode) }
func (s Snapshot) GrokCLIVersion() string    { return s.snapshotVersion(clientFamilyGrokCLI) }

// CodexUserAgent 按快照版本拼接 Codex exec surface UA（格式与
// EffectiveCodexUserAgent 逐字一致，无覆盖时与 CodexExecUserAgent 常量逐字
// 一致）。
func (s Snapshot) CodexUserAgent() string {
	return "codex_exec/" + s.CodexVersion() + " (Windows 10.0.22621; x86_64) unknown"
}

// ClaudeCodeUserAgent 按快照版本拼接 Claude Code CLI 入口 UA（格式与
// EffectiveClaudeCodeUserAgent 逐字一致；sdk-cli 入口变体在 gatewayanthropic
// 包，格式不同，保持差异）。
func (s Snapshot) ClaudeCodeUserAgent() string {
	return "claude-cli/" + s.ClaudeCodeVersion() + " (external, cli)"
}

// GeminiCLIUserAgent 按快照版本拼接 Gemini CLI UA（格式与
// EffectiveGeminiCLIUserAgent 逐字一致）。
func (s Snapshot) GeminiCLIUserAgent() string {
	return "GeminiCLI/" + s.GeminiCLIVersion() + " (Windows; AMD64)"
}

// OverwriteAnthropicOAuthUserAgent 是「同请求多身份值」消费方的接缝：对命中
// ApplySystemClientHeaders Anthropic 家族 OAuth 分支的输入（matchesAnthropicFamily
// + credential oauth，同一包内私有判定，不重复家族条件），把 User-Agent 原位
// 改写为快照派生 UA，使 UA 与同请求 body 版本字段（如 accountprobe 探针的
// cc_version）同源于同一份快照。调用时序契约：在 ApplySystemClientHeaders
// 之后调用（该调用已按既有语义落齐 anthropic-beta、x-stainless-* 等无版本段
// 的其余身份头，本方法不改）；调用方已设置显式 User-Agent（ApplySystemClientHeaders
// 对其沉默、Anthropic 分支未执行）时，调用方必须跳过本方法以维持「不覆盖调用方
// 身份」契约。未命中分支的输入保持沉默。
func (s Snapshot) OverwriteAnthropicOAuthUserAgent(headers http.Header, input Input) {
	if headers == nil {
		return
	}
	if !matchesAnthropicFamily(normalized(input.ProviderCode), normalized(input.ProviderProtocolProfileID)) ||
		normalized(input.CredentialType) != "oauth" {
		return
	}
	headers.Set("User-Agent", s.ClaudeCodeUserAgent())
}
