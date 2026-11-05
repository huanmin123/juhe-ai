package vendorinfer

import (
	"reflect"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// testCatalogEntries 是覆盖九家厂商的推断目录样本，字段形式与导入链一致
// （公开名与上游名可能不同）。
var testCatalogEntries = []Entry{
	{Vendor: contracts.VendorGPT, PublicModel: "gpt-4o", UpstreamModel: "gpt-4o-2024-08-06"},
	{Vendor: contracts.VendorGPT, PublicModel: "gpt-4o-mini", UpstreamModel: "gpt-4o-mini-2024-07-18"},
	{Vendor: contracts.VendorXAI, PublicModel: "grok-4", UpstreamModel: "grok-4-0709"},
	{Vendor: contracts.VendorDeepSeek, PublicModel: "deepseek-chat", UpstreamModel: "deepseek-v3"},
	{Vendor: contracts.VendorAnthropic, PublicModel: "claude-sonnet-4", UpstreamModel: "claude-sonnet-4-20250514"},
	{Vendor: contracts.VendorGemini, PublicModel: "gemini-2.5-pro", UpstreamModel: "gemini-2.5-pro-preview-05-06"},
	{Vendor: contracts.VendorGLM, PublicModel: "glm-4.5", UpstreamModel: "glm-4-plus"},
	{Vendor: contracts.VendorMiniMax, PublicModel: "minimax-video-01", UpstreamModel: "MiniMax-Hailuo-02"},
	{Vendor: contracts.VendorVolcengine, PublicModel: "doubao-seed-1.6", UpstreamModel: "doubao-1.5-pro-32k"},
	{Vendor: contracts.VendorQwen, PublicModel: "qwen3-max", UpstreamModel: "qwen-max"},
}

func newTestCatalog() *Catalog {
	return NewCatalog(testCatalogEntries)
}

// TestInferExactMatches 覆盖目录精确命中：公开名与上游名分别命中同一厂商。
func TestInferExactMatches(t *testing.T) {
	catalog := newTestCatalog()
	cases := []struct {
		name  string
		model string
		want  string
	}{
		{"公开名精确命中 gpt", "gpt-4o", contracts.VendorGPT},
		{"上游名精确命中 gpt", "gpt-4o-2024-08-06", contracts.VendorGPT},
		{"公开名精确命中 anthropic", "claude-sonnet-4", contracts.VendorAnthropic},
		{"上游名精确命中 minimax（大小写不敏感）", "minimax-hailuo-02", contracts.VendorMiniMax},
		{"公开名精确命中 volcengine", "doubao-seed-1.6", contracts.VendorVolcengine},
	}
	for _, tc := range cases {
		result := catalog.Infer([]string{tc.model})
		if !result.OK {
			t.Errorf("%s: Infer(%q) 未命中，unmatched=%v conflicts=%v", tc.name, tc.model, result.Unmatched, result.Conflicts)
			continue
		}
		if result.Vendor != tc.want {
			t.Errorf("%s: Infer(%q).Vendor = %q, want %q", tc.name, tc.model, result.Vendor, tc.want)
		}
	}
}

// TestInferCatalogLongestPrefix 覆盖目录前缀命中，且更长名称优先于更短名称。
func TestInferCatalogLongestPrefix(t *testing.T) {
	catalog := newTestCatalog()
	cases := []struct {
		name  string
		model string
		want  string
	}{
		{"公开名前缀", "gpt-4o-latest", contracts.VendorGPT},
		{"上游名前缀", "gpt-4o-mini-2024-07-18-preview", contracts.VendorGPT},
		{"最短命中", "gpt-4", contracts.VendorGPT},
		{"gemini 目录前缀", "gemini-2.5-pro-exp", contracts.VendorGemini},
		{"qwen 目录前缀", "qwen3-max-2025", contracts.VendorQwen},
	}
	for _, tc := range cases {
		result := catalog.Infer([]string{tc.model})
		if !result.OK || result.Vendor != tc.want {
			t.Errorf("%s: Infer(%q) = {%q ok=%v}, want %q", tc.name, tc.model, result.Vendor, result.OK, tc.want)
		}
	}

	// 目录存在 gpt-4o 与 gpt-4o-mini 两个前缀：命中更长者仍应稳定为同一厂商。
	long := catalog.Infer([]string{"gpt-4o-mini-2024-07-18-extended"})
	if !long.OK || long.Vendor != contracts.VendorGPT {
		t.Fatalf("最长前缀应稳定命中 gpt，got {%q ok=%v}", long.Vendor, long.OK)
	}
}

// TestInferStaticPrefixes 覆盖目录未命中时按静态前缀表判定九家厂商。
func TestInferStaticPrefixes(t *testing.T) {
	catalog := NewCatalog(nil)
	cases := []struct {
		name  string
		model string
		want  string
	}{
		{"gpt- 前缀", "gpt-5-preview", contracts.VendorGPT},
		{"o3 前缀", "o3-mini", contracts.VendorGPT},
		{"o4 前缀", "o4-mini", contracts.VendorGPT},
		{"chatgpt 前缀", "chatgpt-4o", contracts.VendorGPT},
		{"codex 前缀", "codex-mini", contracts.VendorGPT},
		{"claude 前缀", "claude-opus-4", contracts.VendorAnthropic},
		{"gemini 前缀", "gemini-3-flash", contracts.VendorGemini},
		{"grok 前缀", "grok-5", contracts.VendorXAI},
		{"deepseek 前缀", "deepseek-reasoner", contracts.VendorDeepSeek},
		{"glm 前缀", "glm-5", contracts.VendorGLM},
		{"chatglm 前缀", "chatglm4", contracts.VendorGLM},
		{"qwen 前缀", "qwen3-coder", contracts.VendorQwen},
		{"qwq 前缀", "qwq-32b", contracts.VendorQwen},
		{"wan 前缀", "wan2.1-t2v", contracts.VendorQwen},
		{"doubao 前缀", "doubao-1.8", contracts.VendorVolcengine},
		{"seedance 前缀", "seedance-1.0-pro", contracts.VendorVolcengine},
		{"seedream 前缀", "seedream-3.0", contracts.VendorVolcengine},
		{"volc 前缀", "volc-ark-model", contracts.VendorVolcengine},
		{"minimax 前缀", "minimax-text-01", contracts.VendorMiniMax},
		{"hailuo 前缀", "hailuo-02", contracts.VendorMiniMax},
		{"abab 前缀", "abab6.5s", contracts.VendorMiniMax},
		{"speech-2 前缀", "speech-2.5-hd", contracts.VendorMiniMax},
	}
	for _, tc := range cases {
		result := catalog.Infer([]string{tc.model})
		if !result.OK || result.Vendor != tc.want {
			t.Errorf("%s: Infer(%q) = {%q ok=%v unmatched=%v}, want %q", tc.name, tc.model, result.Vendor, result.OK, result.Unmatched, tc.want)
		}
	}
}

// TestInferSingleModelConflict 覆盖同一模型命中多个厂商：OK=false 且记入 Conflicts。
func TestInferSingleModelConflict(t *testing.T) {
	catalog := NewCatalog([]Entry{
		{Vendor: contracts.VendorGPT, PublicModel: "shared-model"},
		{Vendor: contracts.VendorXAI, UpstreamModel: "shared-model"},
	})
	result := catalog.Infer([]string{"shared-model"})
	if result.OK {
		t.Fatalf("同名词条跨厂商时必须判定失败，got %+v", result)
	}
	if result.Vendor != "" {
		t.Errorf("冲突时 Vendor 必须为空，got %q", result.Vendor)
	}
	if !reflect.DeepEqual(result.Conflicts, []string{"shared-model"}) {
		t.Errorf("Conflicts = %v, want [shared-model]", result.Conflicts)
	}
	if len(result.Unmatched) != 0 {
		t.Errorf("冲突模型不应同时记为 Unmatched: %v", result.Unmatched)
	}
}

// TestInferCrossModelConflict 覆盖不同模型分别命中不同厂商：整体不收敛，OK=false。
func TestInferCrossModelConflict(t *testing.T) {
	catalog := newTestCatalog()
	result := catalog.Infer([]string{"gpt-4o", "claude-sonnet-4"})
	if result.OK {
		t.Fatalf("跨厂商模型列表必须判定失败，got %+v", result)
	}
	if result.Vendor != "" {
		t.Errorf("冲突时 Vendor 必须为空，got %q", result.Vendor)
	}
	want := []string{"claude-sonnet-4", "gpt-4o"}
	if !reflect.DeepEqual(result.Conflicts, want) {
		t.Errorf("Conflicts = %v, want %v", result.Conflicts, want)
	}
}

// TestInferZeroHit 覆盖零命中：OK=false、Vendor 空、Unmatched 去重排序。
func TestInferZeroHit(t *testing.T) {
	catalog := newTestCatalog()
	result := catalog.Infer([]string{"unknown-alpha", "unknown-beta", "unknown-alpha"})
	if result.OK {
		t.Fatalf("零命中必须判定失败，got %+v", result)
	}
	if result.Vendor != "" {
		t.Errorf("零命中时 Vendor 必须为空，got %q", result.Vendor)
	}
	want := []string{"unknown-alpha", "unknown-beta"}
	if !reflect.DeepEqual(result.Unmatched, want) {
		t.Errorf("Unmatched = %v, want %v", result.Unmatched, want)
	}
	if len(result.Conflicts) != 0 {
		t.Errorf("零命中不应产生 Conflicts: %v", result.Conflicts)
	}
}

// TestInferMixedMatchAndUnmatched 覆盖部分命中：即使命中唯一厂商，只要存在
// 无法判定的模型，OK 也必须为 false。
func TestInferMixedMatchAndUnmatched(t *testing.T) {
	catalog := newTestCatalog()
	result := catalog.Infer([]string{"gpt-4o", "totally-unknown"})
	if result.OK {
		t.Fatalf("存在未命中模型时必须判定失败，got %+v", result)
	}
	if !reflect.DeepEqual(result.Unmatched, []string{"totally-unknown"}) {
		t.Errorf("Unmatched = %v, want [totally-unknown]", result.Unmatched)
	}
}

// TestInferEmptyInput 覆盖空输入：OK=false 且报告切片为空非 nil。
func TestInferEmptyInput(t *testing.T) {
	result := newTestCatalog().Infer(nil)
	if result.OK || result.Vendor != "" {
		t.Fatalf("空输入必须判定失败且 Vendor 为空，got %+v", result)
	}
	if result.Unmatched == nil || len(result.Unmatched) != 0 {
		t.Errorf("Unmatched 必须为空切片，got %v", result.Unmatched)
	}
	if result.Conflicts == nil || len(result.Conflicts) != 0 {
		t.Errorf("Conflicts 必须为空切片，got %v", result.Conflicts)
	}
}

// TestInferDedupesIdenticalModels 覆盖重复模型名：报告不重复列出。
func TestInferDedupesIdenticalModels(t *testing.T) {
	result := newTestCatalog().Infer([]string{"gpt-4o", "gpt-4o"})
	if !result.OK || result.Vendor != contracts.VendorGPT {
		t.Fatalf("重复同厂商模型应判定成功，got %+v", result)
	}
}

// TestInferEmptyCatalog 覆盖空目录：只走静态前缀。
func TestInferEmptyCatalog(t *testing.T) {
	catalog := NewCatalog(nil)
	result := catalog.Infer([]string{"claude-3-5-sonnet", "glm-4"})
	if result.OK || result.Vendor != "" {
		t.Fatalf("跨厂商静态前缀必须判定失败，got %+v", result)
	}
	only := catalog.Infer([]string{"claude-3-5-sonnet"})
	if !only.OK || only.Vendor != contracts.VendorAnthropic {
		t.Fatalf("静态前缀应判定 anthropic，got %+v", only)
	}
}

// TestConnectionPresetOpenAISurface 覆盖 openai_v1 协议面的八家厂商 preset
// （minimax 在 openai_v1 落下媒体 preset）。
func TestConnectionPresetOpenAISurface(t *testing.T) {
	cases := []struct {
		vendor string
		want   string
	}{
		{contracts.VendorGPT, contracts.PresetGPTOpenAIV1},
		{contracts.VendorXAI, contracts.PresetXAIOpenAIV1},
		{contracts.VendorDeepSeek, contracts.PresetDeepSeekOpenAIV1},
		{contracts.VendorGLM, contracts.PresetGLMGeneralOpenAIV1},
		{contracts.VendorGemini, contracts.PresetGeminiOpenAIChatV1Beta},
		{contracts.VendorQwen, contracts.PresetQwenDashScope},
		{contracts.VendorVolcengine, contracts.PresetVolcengineArk},
		{contracts.VendorMiniMax, contracts.PresetMiniMaxMedia},
	}
	for _, tc := range cases {
		got, ok := ConnectionPreset(tc.vendor, contracts.SurfaceOpenAIV1)
		if !ok || got != tc.want {
			t.Errorf("ConnectionPreset(%q, openai_v1) = (%q, %v), want (%q, true)", tc.vendor, got, ok, tc.want)
		}
	}
	if _, ok := ConnectionPreset(contracts.VendorAnthropic, contracts.SurfaceOpenAIV1); ok {
		t.Errorf("anthropic 没有 openai_v1 连接，必须返回 ok=false")
	}
}

// TestConnectionPresetAnthropicSurface 覆盖 anthropic_v1 协议面。
func TestConnectionPresetAnthropicSurface(t *testing.T) {
	cases := []struct {
		vendor string
		want   string
	}{
		{contracts.VendorAnthropic, contracts.PresetAnthropicV1},
		{contracts.VendorDeepSeek, contracts.PresetDeepSeekAnthropicV1},
		{contracts.VendorGLM, contracts.PresetGLMCodingAnthropicV1},
	}
	for _, tc := range cases {
		got, ok := ConnectionPreset(tc.vendor, contracts.SurfaceAnthropicV1)
		if !ok || got != tc.want {
			t.Errorf("ConnectionPreset(%q, anthropic_v1) = (%q, %v), want (%q, true)", tc.vendor, got, ok, tc.want)
		}
	}
	for _, vendor := range []string{contracts.VendorGPT, contracts.VendorXAI, contracts.VendorGemini, contracts.VendorQwen} {
		if _, ok := ConnectionPreset(vendor, contracts.SurfaceAnthropicV1); ok {
			t.Errorf("%s 没有 anthropic_v1 连接，必须返回 ok=false", vendor)
		}
	}
}

// TestConnectionPresetGeminiSurface 覆盖 gemini_v1beta 协议面。
func TestConnectionPresetGeminiSurface(t *testing.T) {
	got, ok := ConnectionPreset(contracts.VendorGemini, contracts.SurfaceGeminiV1Beta)
	if !ok || got != contracts.PresetGeminiNativeV1Beta {
		t.Fatalf("ConnectionPreset(gemini, gemini_v1beta) = (%q, %v), want (%q, true)", got, ok, contracts.PresetGeminiNativeV1Beta)
	}
	for _, vendor := range []string{contracts.VendorGPT, contracts.VendorXAI, contracts.VendorDeepSeek, contracts.VendorAnthropic, contracts.VendorGLM, contracts.VendorMiniMax, contracts.VendorVolcengine, contracts.VendorQwen} {
		if _, ok := ConnectionPreset(vendor, contracts.SurfaceGeminiV1Beta); ok {
			t.Errorf("%s 没有 gemini_v1beta 连接，必须返回 ok=false", vendor)
		}
	}
}

// TestConnectionPresetUnknownInputs 覆盖未知厂商、未知协议面与空输入。
func TestConnectionPresetUnknownInputs(t *testing.T) {
	cases := []struct{ vendor, surface string }{
		{"openai", contracts.SurfaceOpenAIV1},
		{"", contracts.SurfaceOpenAIV1},
		{contracts.VendorGPT, ""},
		{contracts.VendorGPT, "openai_v2"},
		{contracts.VendorAnthropic, "generic"},
	}
	for _, tc := range cases {
		if preset, ok := ConnectionPreset(tc.vendor, tc.surface); ok || preset != "" {
			t.Errorf("ConnectionPreset(%q, %q) = (%q, %v), want (\"\", false)", tc.vendor, tc.surface, preset, ok)
		}
	}
}

// TestConnectionPresetEveryPresetRegistered 复核所有返回的 preset 都在 registry
// 注册（防止 vendorinfer 与 contracts 字面量漂移）。
func TestConnectionPresetEveryPresetRegistered(t *testing.T) {
	vendors := []string{
		contracts.VendorGPT, contracts.VendorXAI, contracts.VendorDeepSeek,
		contracts.VendorAnthropic, contracts.VendorGemini, contracts.VendorGLM,
		contracts.VendorMiniMax, contracts.VendorVolcengine, contracts.VendorQwen,
	}
	surfaces := []string{contracts.SurfaceOpenAIV1, contracts.SurfaceAnthropicV1, contracts.SurfaceGeminiV1Beta}
	for _, vendor := range vendors {
		for _, surface := range surfaces {
			if preset, ok := ConnectionPreset(vendor, surface); ok && !contracts.HasConnectionPreset(preset) {
				t.Errorf("ConnectionPreset(%q, %q) 返回未注册 preset %q", vendor, surface, preset)
			}
		}
	}
}
