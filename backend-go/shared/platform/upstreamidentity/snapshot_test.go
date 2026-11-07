package upstreamidentity

import (
	"net/http"
	"sync"
	"testing"
)

// 无覆盖时快照各 getter 与 UA 拼接必须和 Effective* 及内置常量逐字一致
//（请求内身份快照默认路径回归锚点）。
func TestSnapshotMatchesEffectiveWithoutOverrides(t *testing.T) {
	resetClientVersionOverrides(t)
	resetClientVersionAutoOverrides(t)
	SetClientVersionOverrides(nil)
	SetClientVersionAutoOverrides(nil)
	snapshot := NewSnapshot()
	if got := snapshot.CodexVersion(); got != EffectiveCodexVersion() || got != builtInCodexVersion {
		t.Fatalf("CodexVersion()=%q, want builtin %q", got, builtInCodexVersion)
	}
	if got := snapshot.ClaudeCodeVersion(); got != EffectiveClaudeCodeVersion() || got != builtInClaudeCodeVersion {
		t.Fatalf("ClaudeCodeVersion()=%q, want builtin %q", got, builtInClaudeCodeVersion)
	}
	if got := snapshot.GeminiCLIVersion(); got != EffectiveGeminiCLIVersion() || got != builtInGeminiCLIVersion {
		t.Fatalf("GeminiCLIVersion()=%q, want builtin %q", got, builtInGeminiCLIVersion)
	}
	if got := snapshot.ZCodeVersion(); got != EffectiveZCodeVersion() || got != ZCodeVersion {
		t.Fatalf("ZCodeVersion()=%q, want builtin %q", got, ZCodeVersion)
	}
	if got := snapshot.GrokCLIVersion(); got != EffectiveGrokCLIVersion() || got != GrokCLIClientVersion {
		t.Fatalf("GrokCLIVersion()=%q, want builtin %q", got, GrokCLIClientVersion)
	}
	if got := snapshot.CodexUserAgent(); got != EffectiveCodexUserAgent() || got != CodexExecUserAgent {
		t.Fatalf("CodexUserAgent()=%q, want %q", got, CodexExecUserAgent)
	}
	if got := snapshot.ClaudeCodeUserAgent(); got != EffectiveClaudeCodeUserAgent() || got != ClaudeCodeUserAgent {
		t.Fatalf("ClaudeCodeUserAgent()=%q, want %q", got, ClaudeCodeUserAgent)
	}
	if got := snapshot.GeminiCLIUserAgent(); got != EffectiveGeminiCLIUserAgent() || got != GeminiCLIUserAgent {
		t.Fatalf("GeminiCLIUserAgent()=%q, want %q", got, GeminiCLIUserAgent)
	}
}

// Set 手动/自动覆盖后快照 getter 与 Effective* 一致（手动 > 自动 > 内置）；
// 构造后的 Set 不影响已构造快照（构造时读取），新快照反映新值。
func TestSnapshotReflectsOverridesAndIgnoresLaterRefresh(t *testing.T) {
	resetClientVersionOverrides(t)
	resetClientVersionAutoOverrides(t)
	SetClientVersionOverrides(map[string]string{
		"claudeCode": "2.2.0",
		"codex":      "0.200.0",
		"grokCLI":    "2.5.0",
	})
	SetClientVersionAutoOverrides(map[string]string{
		"grokCLI":   "1.0.14",
		"geminiCLI": "0.70.1",
	})
	snapshot := NewSnapshot()
	if got := snapshot.ClaudeCodeVersion(); got != "2.2.0" || got != EffectiveClaudeCodeVersion() {
		t.Fatalf("ClaudeCodeVersion()=%q, want manual 2.2.0", got)
	}
	if got := snapshot.CodexVersion(); got != "0.200.0" || got != EffectiveCodexVersion() {
		t.Fatalf("CodexVersion()=%q, want manual 0.200.0", got)
	}
	if got := snapshot.GrokCLIVersion(); got != "2.5.0" || got != EffectiveGrokCLIVersion() {
		t.Fatalf("GrokCLIVersion()=%q, want manual-over-auto 2.5.0", got)
	}
	if got := snapshot.GeminiCLIVersion(); got != "0.70.1" || got != EffectiveGeminiCLIVersion() {
		t.Fatalf("GeminiCLIVersion()=%q, want auto 0.70.1", got)
	}
	if got := snapshot.ZCodeVersion(); got != EffectiveZCodeVersion() || got != ZCodeVersion {
		t.Fatalf("ZCodeVersion()=%q, want builtin %q", got, ZCodeVersion)
	}
	if got := snapshot.CodexUserAgent(); got != EffectiveCodexUserAgent() || got != "codex_exec/0.200.0 (Windows 10.0.22621; x86_64) unknown" {
		t.Fatalf("CodexUserAgent()=%q, want overridden codex UA", got)
	}
	if got := snapshot.ClaudeCodeUserAgent(); got != EffectiveClaudeCodeUserAgent() || got != "claude-cli/2.2.0 (external, cli)" {
		t.Fatalf("ClaudeCodeUserAgent()=%q, want overridden claude UA", got)
	}
	if got := snapshot.GeminiCLIUserAgent(); got != EffectiveGeminiCLIUserAgent() || got != "GeminiCLI/0.70.1 (Windows; AMD64)" {
		t.Fatalf("GeminiCLIUserAgent()=%q, want overridden gemini UA", got)
	}
	// 构造后的 Set 全量替换不影响已构造快照；新快照反映新值。
	SetClientVersionOverrides(nil)
	SetClientVersionAutoOverrides(nil)
	if got := snapshot.ClaudeCodeVersion(); got != "2.2.0" {
		t.Fatalf("constructed snapshot must ignore later refresh, got %q", got)
	}
	if got := NewSnapshot().ClaudeCodeVersion(); got != builtInClaudeCodeVersion {
		t.Fatalf("fresh snapshot after clearing=%q, want builtin %q", got, builtInClaudeCodeVersion)
	}
}

// snapshotLayerStates 是并发测试写入的两层候选状态（各家族键互斥或同值，
// 保证任意非法混组合无法被某个候选对解释）。
var snapshotLayerStates = struct {
	manual []map[string]string
	auto   []map[string]string
}{
	manual: []map[string]string{
		nil,
		{"claudeCode": "10.0.1"},
		{"grokCLI": "10.0.2"},
	},
	auto: []map[string]string{
		nil,
		{"claudeCode": "11.0.1", "codex": "12.0.1"},
		{"geminiCLI": "13.0.1"},
	},
}

// snapshotMergedValue 按手动 > 自动 > 内置合并候选层状态（与 Effective 合并
// 顺序一致）。
func snapshotMergedValue(manual, auto map[string]string, family string) string {
	if version, ok := manual[family]; ok {
		return version
	}
	if version, ok := auto[family]; ok {
		return version
	}
	return BuiltInClientVersion(family)
}

// snapshotExplainedByCandidatePair 断言五族取值组合恰好等于「同一对候选层
// 状态的合法合并」：存在某个 (manual, auto) 候选对，其三层合并预测与快照
// 五族逐字一致。混组合（五族来自不同层实例）无候选对可解释，即失败。
func snapshotExplainedByCandidatePair(t *testing.T, snapshot Snapshot) {
	t.Helper()
	got := map[string]string{
		clientFamilyCodex:      snapshot.CodexVersion(),
		clientFamilyClaudeCode: snapshot.ClaudeCodeVersion(),
		clientFamilyGeminiCLI:  snapshot.GeminiCLIVersion(),
		clientFamilyZCode:      snapshot.ZCodeVersion(),
		clientFamilyGrokCLI:    snapshot.GrokCLIVersion(),
	}
	for _, manual := range snapshotLayerStates.manual {
		for _, auto := range snapshotLayerStates.auto {
			match := true
			for family, want := range got {
				if snapshotMergedValue(manual, auto, family) != want {
					match = false
					break
				}
			}
			if match {
				return
			}
		}
	}
	t.Errorf("snapshot five-family tuple %v is not a legal merge of one (manual, auto) candidate pair", got)
}

// 并发一致性：一个 goroutine 持续替换两层，另一个反复取 Snapshot，五族值
// 组合必须始终恰好等于「同一对 map 实例的合法合并」（手动 > 自动 > 内置）。
func TestSnapshotConsistentUnderConcurrentOverrideRefresh(t *testing.T) {
	resetClientVersionOverrides(t)
	resetClientVersionAutoOverrides(t)
	SetClientVersionOverrides(snapshotLayerStates.manual[0])
	SetClientVersionAutoOverrides(snapshotLayerStates.auto[0])
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for round := 0; round < 300; round++ {
			SetClientVersionOverrides(snapshotLayerStates.manual[round%len(snapshotLayerStates.manual)])
			SetClientVersionAutoOverrides(snapshotLayerStates.auto[round%len(snapshotLayerStates.auto)])
		}
	}()
	for round := 0; round < 300; round++ {
		snapshotExplainedByCandidatePair(t, NewSnapshot())
	}
	wg.Wait()
}

// OverwriteAnthropicOAuthUserAgent 接缝：命中 Anthropic 家族 OAuth 分支时
// UA 改写为快照派生值；未命中分支与 nil headers 保持沉默。
func TestSnapshotOverwriteAnthropicOAuthUserAgentSeam(t *testing.T) {
	resetClientVersionOverrides(t)
	resetClientVersionAutoOverrides(t)
	SetClientVersionOverrides(map[string]string{"claudeCode": "2.2.0"})
	snapshot := NewSnapshot()

	headers := http.Header{}
	snapshot.OverwriteAnthropicOAuthUserAgent(headers, Input{ProviderCode: "anthropic", CredentialType: "oauth"})
	if got := headers.Get("User-Agent"); got != "claude-cli/2.2.0 (external, cli)" {
		t.Fatalf("anthropic oauth UA=%q, want snapshot claude UA", got)
	}

	headers = http.Header{}
	snapshot.OverwriteAnthropicOAuthUserAgent(headers, Input{ProviderProtocolProfileID: ProfileAnthropicAnthropicV1, CredentialType: "oauth"})
	if got := headers.Get("User-Agent"); got != "claude-cli/2.2.0 (external, cli)" {
		t.Fatalf("profile-matched UA=%q, want snapshot claude UA", got)
	}

	// api_key 不命中 OAuth 分支；GPT 家族不命中 Anthropic 分支：保持沉默。
	for _, input := range []Input{
		{ProviderCode: "anthropic", CredentialType: "api_key"},
		{ProviderCode: "gpt", CredentialType: "oauth"},
	} {
		headers = http.Header{}
		snapshot.OverwriteAnthropicOAuthUserAgent(headers, input)
		if got := headers.Get("User-Agent"); got != "" {
			t.Fatalf("non-matching input %v must stay silent, got UA %q", input, got)
		}
	}
	snapshot.OverwriteAnthropicOAuthUserAgent(nil, Input{ProviderCode: "anthropic", CredentialType: "oauth"})
}
