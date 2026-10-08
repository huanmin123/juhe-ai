package upstreamidentity

import "testing"

// resetClientVersionAutoOverrides 清空自动层，避免覆盖状态泄漏到同包其他测试。
func resetClientVersionAutoOverrides(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { SetClientVersionAutoOverrides(nil) })
}

// 三层合并优先级矩阵：手动覆盖 > 自动覆盖 > 内置。逐族同时在位时手动胜出，
// 便捷 getter 与 UA 拼接消费同一合并结果。
func TestEffectiveClientVersionManualOverAutoOverBuiltIn(t *testing.T) {
	resetClientVersionOverrides(t)
	resetClientVersionAutoOverrides(t)
	SetClientVersionOverrides(map[string]string{
		"codex":      "0.300.0",
		"claudeCode": "3.0.0",
		"geminiCLI":  "0.90.0",
		"zcode":      "4.0.0",
		"grokCLI":    "1.1.0",
	})
	SetClientVersionAutoOverrides(map[string]string{
		"codex":      "0.200.0",
		"claudeCode": "2.2.0",
		"geminiCLI":  "0.70.1",
		"zcode":      "3.9.9",
		"grokCLI":    "1.0.14",
	})
	manualWins := map[string]string{
		"codex":      "0.300.0",
		"claudeCode": "3.0.0",
		"geminiCLI":  "0.90.0",
		"zcode":      "4.0.0",
		"grokCLI":    "1.1.0",
	}
	for family, want := range manualWins {
		if got := EffectiveClientVersion(family); got != want {
			t.Fatalf("EffectiveClientVersion(%q)=%q, want manual %q", family, got, want)
		}
	}
	if got := EffectiveCodexVersion(); got != "0.300.0" {
		t.Fatalf("EffectiveCodexVersion()=%q, want 0.300.0", got)
	}
	if got := EffectiveClaudeCodeVersion(); got != "3.0.0" {
		t.Fatalf("EffectiveClaudeCodeVersion()=%q, want 3.0.0", got)
	}
	if got := EffectiveGeminiCLIVersion(); got != "0.90.0" {
		t.Fatalf("EffectiveGeminiCLIVersion()=%q, want 0.90.0", got)
	}
	if got := EffectiveZCodeVersion(); got != "4.0.0" {
		t.Fatalf("EffectiveZCodeVersion()=%q, want 4.0.0", got)
	}
	if got := EffectiveGrokCLIVersion(); got != "1.1.0" {
		t.Fatalf("EffectiveGrokCLIVersion()=%q, want 1.1.0", got)
	}
	// UA getter 同样消费三层合并结果（手动层胜出）。
	if got := EffectiveCodexUserAgent(); got != "codex_exec/0.300.0 (Windows 10.0.22621; x86_64) unknown" {
		t.Fatalf("EffectiveCodexUserAgent()=%q", got)
	}
	if got := EffectiveClaudeCodeUserAgent(); got != "claude-cli/3.0.0 (external, cli)" {
		t.Fatalf("EffectiveClaudeCodeUserAgent()=%q", got)
	}
	if got := EffectiveGeminiCLIUserAgent(); got != "GeminiCLI/0.90.0 (Windows; AMD64)" {
		t.Fatalf("EffectiveGeminiCLIUserAgent()=%q", got)
	}
	// 自动覆盖也允许低于内置（非 max 合并）：等手动层清空后自动层原值胜出。
	SetClientVersionAutoOverrides(map[string]string{"codex": "0.1.0"})
	SetClientVersionOverrides(nil)
	if got := EffectiveCodexVersion(); got != "0.1.0" {
		t.Fatalf("auto override must not be max-merged with builtin, got %q", got)
	}
}

// 清手动层回退自动层：自动层不受手动层替换/清空影响。
func TestClearingManualLayerFallsBackToAutoLayer(t *testing.T) {
	resetClientVersionOverrides(t)
	resetClientVersionAutoOverrides(t)
	SetClientVersionAutoOverrides(map[string]string{
		"codex":      "0.200.0",
		"claudeCode": "2.2.0",
		"geminiCLI":  "0.70.1",
		"zcode":      "3.9.9",
		"grokCLI":    "1.0.14",
	})
	SetClientVersionOverrides(map[string]string{"codex": "0.300.0"})
	if got := EffectiveCodexVersion(); got != "0.300.0" {
		t.Fatalf("setup failed: EffectiveCodexVersion()=%q, want 0.300.0", got)
	}
	SetClientVersionOverrides(nil)
	autoWins := map[string]string{
		"codex":      "0.200.0",
		"claudeCode": "2.2.0",
		"geminiCLI":  "0.70.1",
		"zcode":      "3.9.9",
		"grokCLI":    "1.0.14",
	}
	for family, want := range autoWins {
		if got := EffectiveClientVersion(family); got != want {
			t.Fatalf("EffectiveClientVersion(%q)=%q, want auto %q", family, got, want)
		}
	}
	// 空 map 与 nil 等价（清空手动层），自动层继续生效。
	SetClientVersionOverrides(map[string]string{})
	if got := EffectiveZCodeVersion(); got != "3.9.9" {
		t.Fatalf("empty manual map must fall back to auto, got %q", got)
	}
}

// 清自动层回退内置：自动层替换/清空同样不回退到手动层的历史值。
func TestClearingAutoLayerFallsBackToBuiltIn(t *testing.T) {
	resetClientVersionOverrides(t)
	resetClientVersionAutoOverrides(t)
	SetClientVersionOverrides(nil)
	SetClientVersionAutoOverrides(map[string]string{
		"codex":      "0.200.0",
		"claudeCode": "2.2.0",
		"geminiCLI":  "0.70.1",
		"zcode":      "3.9.9",
		"grokCLI":    "1.0.14",
	})
	if got := EffectiveCodexVersion(); got != "0.200.0" {
		t.Fatalf("setup failed: EffectiveCodexVersion()=%q, want 0.200.0", got)
	}
	SetClientVersionAutoOverrides(nil)
	builtIns := map[string]string{
		"codex":      builtInCodexVersion,
		"claudeCode": builtInClaudeCodeVersion,
		"geminiCLI":  builtInGeminiCLIVersion,
		"zcode":      ZCodeVersion,
		"grokCLI":    GrokCLIClientVersion,
	}
	for family, want := range builtIns {
		if got := EffectiveClientVersion(family); got != want {
			t.Fatalf("EffectiveClientVersion(%q)=%q, want builtin %q", family, got, want)
		}
	}
	// 空 map 与 nil 等价（清空自动层）。
	SetClientVersionAutoOverrides(map[string]string{"zcode": "1.0.0"})
	SetClientVersionAutoOverrides(map[string]string{})
	if got := EffectiveZCodeVersion(); got != ZCodeVersion {
		t.Fatalf("empty auto map must restore builtin, got %q", got)
	}
	// 自动层不泄漏到手动的全量替换之外的其他家族。
	SetClientVersionAutoOverrides(map[string]string{"codex": "0.159.4"})
	SetClientVersionOverrides(map[string]string{"claudeCode": "2.1.999"})
	if got := EffectiveCodexVersion(); got != "0.159.4" {
		t.Fatalf("auto codex must survive manual replace of other family, got %q", got)
	}
	if got := EffectiveClaudeCodeVersion(); got != "2.1.999" {
		t.Fatalf("manual claudeCode must win over builtin, got %q", got)
	}
}

// 自动层非法输入过滤：未知键与非 semver 值忽略（与手动层同语义），不污染合法键。
func TestSetClientVersionAutoOverridesIgnoresUnknownKeysAndInvalidValues(t *testing.T) {
	resetClientVersionOverrides(t)
	resetClientVersionAutoOverrides(t)
	SetClientVersionOverrides(nil)
	SetClientVersionAutoOverrides(map[string]string{
		"codex":      "1.2.3",
		"unknown":    "4.5.6",
		"claudeCode": "2.1.285-x", // 预发行后缀不合法
		"geminiCLI":  "0.61",      // 两段
		"zcode":      "",          // 空
		"grokCLI":    "1.0.13.0",  // 四段
		"":           "0.0.1",     // 空键
	})
	if got := EffectiveCodexVersion(); got != "1.2.3" {
		t.Fatalf("valid codex auto override must apply, got %q", got)
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
	// 合法键过滤后拷贝存储：调用方改原 map 不影响已存自动层。
	auto := map[string]string{"codex": "0.159.4"}
	SetClientVersionAutoOverrides(auto)
	auto["codex"] = "9.9.9"
	auto["zcode"] = "0.0.0"
	if got := EffectiveCodexVersion(); got != "0.159.4" {
		t.Fatalf("stored auto override must be a copy, got %q", got)
	}
	if got := EffectiveZCodeVersion(); got != ZCodeVersion {
		t.Fatalf("post-Set mutation must not add families, got %q", got)
	}
	// 非法输入全被过滤时自动层为空（不产生半合法状态）。
	SetClientVersionAutoOverrides(map[string]string{"codex": "1.2.3"})
	SetClientVersionAutoOverrides(map[string]string{"nope": "1.2.3", "codex": "1.2"})
	if got := EffectiveCodexVersion(); got != builtInCodexVersion {
		t.Fatalf("all-invalid auto map must clear the layer, got %q", got)
	}
}

// BuiltInClientVersion 返回五族内置常量与未知族空串（任务侧单调检查输入）。
func TestBuiltInClientVersionFiveFamiliesAndUnknown(t *testing.T) {
	resetClientVersionOverrides(t)
	resetClientVersionAutoOverrides(t)
	// 覆盖层在位也不影响内置层读取（任务侧必须拿到常量本身）。
	SetClientVersionOverrides(map[string]string{"codex": "0.300.0"})
	SetClientVersionAutoOverrides(map[string]string{"codex": "0.200.0"})
	cases := map[string]string{
		"codex":      builtInCodexVersion,
		"claudeCode": builtInClaudeCodeVersion,
		"geminiCLI":  builtInGeminiCLIVersion,
		"zcode":      ZCodeVersion,
		"grokCLI":    GrokCLIClientVersion,
	}
	for family, want := range cases {
		if got := BuiltInClientVersion(family); got != want {
			t.Fatalf("BuiltInClientVersion(%q)=%q, want %q", family, got, want)
		}
	}
	for _, unknown := range []string{"", "unknown", "Codex"} {
		if got := BuiltInClientVersion(unknown); got != "" {
			t.Fatalf("BuiltInClientVersion(%q)=%q, want empty", unknown, got)
		}
	}
}
