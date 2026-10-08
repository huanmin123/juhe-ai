package upstreamidentity

import (
	"net/http"
	"sync"
	"testing"
)

// resetClientVersionOverrides 恢复内置默认，避免覆盖状态泄漏到同包其他测试。
func resetClientVersionOverrides(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { SetClientVersionOverrides(nil) })
}

// 无覆盖时 effective getter 必须与内置常量逐字一致（默认路径回归锚点）。
func TestEffectiveClientVersionsDefaultToBuiltins(t *testing.T) {
	resetClientVersionOverrides(t)
	SetClientVersionOverrides(nil)
	if got := EffectiveCodexVersion(); got != builtInCodexVersion {
		t.Fatalf("EffectiveCodexVersion()=%q, want %q", got, builtInCodexVersion)
	}
	if got := EffectiveClaudeCodeVersion(); got != builtInClaudeCodeVersion {
		t.Fatalf("EffectiveClaudeCodeVersion()=%q, want %q", got, builtInClaudeCodeVersion)
	}
	if got := EffectiveGeminiCLIVersion(); got != builtInGeminiCLIVersion {
		t.Fatalf("EffectiveGeminiCLIVersion()=%q, want %q", got, builtInGeminiCLIVersion)
	}
	if got := EffectiveZCodeVersion(); got != ZCodeVersion {
		t.Fatalf("EffectiveZCodeVersion()=%q, want %q", got, ZCodeVersion)
	}
	if got := EffectiveGrokCLIVersion(); got != GrokCLIClientVersion {
		t.Fatalf("EffectiveGrokCLIVersion()=%q, want %q", got, GrokCLIClientVersion)
	}
	if got := EffectiveCodexUserAgent(); got != CodexExecUserAgent {
		t.Fatalf("EffectiveCodexUserAgent()=%q, want %q", got, CodexExecUserAgent)
	}
	if got := EffectiveClaudeCodeUserAgent(); got != ClaudeCodeUserAgent {
		t.Fatalf("EffectiveClaudeCodeUserAgent()=%q, want %q", got, ClaudeCodeUserAgent)
	}
	if got := EffectiveGeminiCLIUserAgent(); got != GeminiCLIUserAgent {
		t.Fatalf("EffectiveGeminiCLIUserAgent()=%q, want %q", got, GeminiCLIUserAgent)
	}
	if got := EffectiveClientVersion("unknown"); got != "" {
		t.Fatalf("unknown family must return \"\", got %q", got)
	}
}

// Set 之后五个家族的 getter 与整 UA getter 必须立即反映覆盖（可升可降，
// 非 max 合并）。
func TestSetClientVersionOverridesChangesEffectiveValues(t *testing.T) {
	resetClientVersionOverrides(t)
	overrides := map[string]string{
		"codex":      "0.200.0",
		"claudeCode": "2.2.0",
		"geminiCLI":  "0.70.1",
		"zcode":      "3.9.9",
		"grokCLI":    "1.0.14",
	}
	SetClientVersionOverrides(overrides)
	if got := EffectiveCodexVersion(); got != "0.200.0" {
		t.Fatalf("EffectiveCodexVersion()=%q, want 0.200.0", got)
	}
	if got := EffectiveClaudeCodeVersion(); got != "2.2.0" {
		t.Fatalf("EffectiveClaudeCodeVersion()=%q, want 2.2.0", got)
	}
	if got := EffectiveGeminiCLIVersion(); got != "0.70.1" {
		t.Fatalf("EffectiveGeminiCLIVersion()=%q, want 0.70.1", got)
	}
	if got := EffectiveZCodeVersion(); got != "3.9.9" {
		t.Fatalf("EffectiveZCodeVersion()=%q, want 3.9.9", got)
	}
	if got := EffectiveGrokCLIVersion(); got != "1.0.14" {
		t.Fatalf("EffectiveGrokCLIVersion()=%q, want 1.0.14", got)
	}
	wantCodexUA := "codex_exec/0.200.0 (Windows 10.0.22621; x86_64) unknown"
	if got := EffectiveCodexUserAgent(); got != wantCodexUA {
		t.Fatalf("EffectiveCodexUserAgent()=%q, want %q", got, wantCodexUA)
	}
	if got := EffectiveClaudeCodeUserAgent(); got != "claude-cli/2.2.0 (external, cli)" {
		t.Fatalf("EffectiveClaudeCodeUserAgent()=%q", got)
	}
	if got := EffectiveGeminiCLIUserAgent(); got != "GeminiCLI/0.70.1 (Windows; AMD64)" {
		t.Fatalf("EffectiveGeminiCLIUserAgent()=%q", got)
	}
	// 覆盖也允许降级（非 max 合并）：版本低于内置仍以覆盖为准。
	SetClientVersionOverrides(map[string]string{"codex": "0.1.0"})
	if got := EffectiveCodexVersion(); got != "0.1.0" {
		t.Fatalf("downgrade override must win, got %q", got)
	}
	// 降级后其余家族回退内置（全量替换语义）。
	if got := EffectiveZCodeVersion(); got != ZCodeVersion {
		t.Fatalf("replaced overrides must clear stale families, got %q", got)
	}
}

// 空 map 与 nil 都是清空回内置。
func TestSetClientVersionOverridesEmptyMapClearsToBuiltins(t *testing.T) {
	resetClientVersionOverrides(t)
	SetClientVersionOverrides(map[string]string{"codex": "9.9.9"})
	if got := EffectiveCodexVersion(); got != "9.9.9" {
		t.Fatalf("setup failed: EffectiveCodexVersion()=%q", got)
	}
	SetClientVersionOverrides(map[string]string{})
	if got := EffectiveCodexVersion(); got != builtInCodexVersion {
		t.Fatalf("empty map must restore builtin, got %q", got)
	}
	SetClientVersionOverrides(map[string]string{"zcode": "1.0.0"})
	SetClientVersionOverrides(nil)
	if got := EffectiveZCodeVersion(); got != ZCodeVersion {
		t.Fatalf("nil map must restore builtin, got %q", got)
	}
}

// 未知键与非法 semver 值忽略，不污染合法键。
func TestSetClientVersionOverridesIgnoresUnknownKeysAndInvalidValues(t *testing.T) {
	resetClientVersionOverrides(t)
	SetClientVersionOverrides(map[string]string{
		"codex":      "1.2.3",
		"unknown":    "4.5.6",
		"claudeCode": "2.1.285-x", // 预发行后缀不合法
		"geminiCLI":  "0.61",      // 两段
		"zcode":      "",          // 空
		"grokCLI":    "1.0.13.0",  // 四段
		"":           "0.0.1",     // 空键
	})
	if got := EffectiveCodexVersion(); got != "1.2.3" {
		t.Fatalf("valid codex override must apply, got %q", got)
	}
	if got := EffectiveClientVersion("unknown"); got != "" {
		t.Fatalf("unknown family must stay builtin-empty, got %q", got)
	}
	if got := EffectiveClaudeCodeVersion(); got != builtInClaudeCodeVersion {
		t.Fatalf("invalid claudeCode value must be ignored, got %q", got)
	}
	if got := EffectiveGeminiCLIVersion(); got != builtInGeminiCLIVersion {
		t.Fatalf("invalid geminiCLI value must be ignored, got %q", got)
	}
	if got := EffectiveZCodeVersion(); got != ZCodeVersion {
		t.Fatalf("empty zcode value must be ignored, got %q", got)
	}
	if got := EffectiveGrokCLIVersion(); got != GrokCLIClientVersion {
		t.Fatalf("four-segment grokCLI value must be ignored, got %q", got)
	}
}

// Set 拷贝存储：调用方在 Set 后修改原 map 不得影响已存覆盖。
func TestSetClientVersionOverridesCopiesInput(t *testing.T) {
	resetClientVersionOverrides(t)
	overrides := map[string]string{"codex": "0.159.4"}
	SetClientVersionOverrides(overrides)
	overrides["codex"] = "0.999.9"
	overrides["zcode"] = "0.0.0"
	if got := EffectiveCodexVersion(); got != "0.159.4" {
		t.Fatalf("stored override must be a copy, got %q", got)
	}
	if got := EffectiveZCodeVersion(); got != ZCodeVersion {
		t.Fatalf("post-Set mutation must not add families, got %q", got)
	}
}

// ApplySystemClientHeaders 各家族分支必须消费覆盖版本（-race 下并发读写
// 不触发 data race）。
func TestApplySystemClientHeadersUsesEffectiveOverriddenVersions(t *testing.T) {
	resetClientVersionOverrides(t)
	SetClientVersionOverrides(map[string]string{
		"codex":      "0.200.0",
		"claudeCode": "2.2.0",
		"geminiCLI":  "0.70.1",
		"zcode":      "3.9.9",
		"grokCLI":    "1.0.14",
	})

	headers := http.Header{}
	ApplySystemClientHeaders(headers, Input{ProviderCode: "gpt", CredentialType: "api_key"})
	if got := headers.Get("User-Agent"); got != "codex_exec/0.200.0 (Windows 10.0.22621; x86_64) unknown" {
		t.Fatalf("GPT family UA must use override, got %q", got)
	}

	headers = http.Header{}
	ApplySystemClientHeaders(headers, Input{ProviderCode: "glm", CredentialType: "api_key"})
	if got := headers.Get("User-Agent"); got != "ZCode/3.9.9" {
		t.Fatalf("GLM family UA must use override, got %q", got)
	}
	if got := headers.Get("X-ZCode-App-Version"); got != "3.9.9" {
		t.Fatalf("X-ZCode-App-Version must use override, got %q", got)
	}

	headers = http.Header{}
	ApplySystemClientHeaders(headers, Input{ProviderCode: "anthropic", CredentialType: "oauth"})
	if got := headers.Get("User-Agent"); got != "claude-cli/2.2.0 (external, cli)" {
		t.Fatalf("Anthropic OAuth UA must use override, got %q", got)
	}

	headers = http.Header{}
	ApplySystemClientHeaders(headers, Input{
		ProviderCode: "gemini", ProviderProtocolProfileID: ProfileGeminiNativeV1Beta,
		CredentialType: "google_oauth", OAuthType: "code_assist",
	})
	if got := headers.Get("User-Agent"); got != "GeminiCLI/0.70.1 (Windows; AMD64)" {
		t.Fatalf("Gemini OAuth UA must use override, got %q", got)
	}

	headers = http.Header{}
	ApplySystemClientHeaders(headers, Input{
		ProviderCode: "xai", ProviderProtocolProfileID: ProfileXAIOpenAIV1,
		CredentialType: "oauth", UpstreamHostname: "cli-chat-proxy.grok.com",
	})
	if got := headers.Get("User-Agent"); got != "xai-grok-workspace/1.0.14" {
		t.Fatalf("Grok UA must use override, got %q", got)
	}
	if got := headers.Get("x-grok-client-version"); got != "1.0.14" {
		t.Fatalf("x-grok-client-version must use override, got %q", got)
	}
}

// 并发读写：一边全量替换覆盖，一边读 getter 与 ApplySystemClientHeaders，
// -race 语义下不得出现 data race。
func TestClientVersionOverridesConcurrentReadWrite(t *testing.T) {
	resetClientVersionOverrides(t)
	SetClientVersionOverrides(map[string]string{"codex": "0.159.3"})
	var wg sync.WaitGroup
	for writer := 0; writer < 2; writer++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			for round := 0; round < 200; round++ {
				SetClientVersionOverrides(map[string]string{
					"codex":      "0.200.0",
					"claudeCode": "2.2.0",
					"geminiCLI":  "0.70.1",
					"zcode":      "3.9.9",
					"grokCLI":    "1.0.14",
				})
				SetClientVersionOverrides(nil)
			}
		}(writer)
	}
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < 400; round++ {
				_ = EffectiveCodexVersion()
				_ = EffectiveClaudeCodeVersion()
				_ = EffectiveGeminiCLIVersion()
				_ = EffectiveZCodeVersion()
				_ = EffectiveGrokCLIVersion()
				_ = EffectiveCodexUserAgent()
				_ = EffectiveClaudeCodeUserAgent()
				_ = EffectiveGeminiCLIUserAgent()
				headers := http.Header{}
				ApplySystemClientHeaders(headers, Input{ProviderCode: "gpt", CredentialType: "api_key"})
				ApplySystemClientHeaders(headers, Input{ProviderCode: "anthropic", CredentialType: "oauth"})
			}
		}()
	}
	wg.Wait()
}
