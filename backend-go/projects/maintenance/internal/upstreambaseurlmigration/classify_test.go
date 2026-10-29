package upstreambaseurlmigration

import (
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// TestClassifyBaseURL 是分类规则的表驱动主测（连接行视角；账户行走同一
// ClassifyBaseURL，仅 preset 提示来源不同，见 TestAccountPresetHint）。
func TestClassifyBaseURL(t *testing.T) {
	fill := func(preset string) string { return contracts.PresetDefaultBaseURL(preset) }
	cases := []struct {
		name             string
		preset           string
		raw              string
		wantDisposition  Disposition
		wantNew          string
		wantChangeReason ChangeReason
		wantReview       ReviewReason
	}{
		// ── openai 族 ────────────────────────────────────────────────
		{name: "openai/空值回填registry默认", preset: contracts.PresetGPTOpenAIV1, raw: "",
			wantDisposition: DispositionFill, wantNew: "https://api.openai.com/v1", wantChangeReason: ReasonFillRegistryDefault},
		{name: "openai/已合规不变", preset: contracts.PresetGPTOpenAIV1, raw: "https://api.openai.com/v1",
			wantDisposition: DispositionUnchanged},
		{name: "openai/尾斜杠规整", preset: contracts.PresetGPTOpenAIV1, raw: "https://api.openai.com/v1/",
			wantDisposition: DispositionRewrite, wantNew: "https://api.openai.com/v1", wantChangeReason: ReasonNormalizeSlash},
		{name: "openai/版本根大写按合规保留", preset: contracts.PresetGPTOpenAIV1, raw: "https://api.openai.com/V1",
			wantDisposition: DispositionUnchanged},
		{name: "openai/裸origin补v1", preset: contracts.PresetDeepSeekOpenAIV1, raw: "https://api.deepseek.com",
			wantDisposition: DispositionRewrite, wantNew: "https://api.deepseek.com/v1", wantChangeReason: ReasonAppendVersionRoot},
		{name: "openai/已带v1不变", preset: contracts.PresetDeepSeekOpenAIV1, raw: "https://api.deepseek.com/v1",
			wantDisposition: DispositionUnchanged},
		{name: "openai/generic空值无默认进复核", preset: contracts.PresetGenericOpenAIV1, raw: "",
			wantDisposition: DispositionReview, wantReview: ReviewNoDefault},
		{name: "openai/generic裸origin补v1", preset: contracts.PresetGenericOpenAIV1, raw: "https://proxy.example.com",
			wantDisposition: DispositionRewrite, wantNew: "https://proxy.example.com/v1", wantChangeReason: ReasonAppendVersionRoot},
		{name: "openai/generic已合规不变", preset: contracts.PresetGenericOpenAIV1, raw: "https://proxy.example.com/v1",
			wantDisposition: DispositionUnchanged},
		{name: "openai/具体接口路径进复核", preset: contracts.PresetGenericOpenAIV1, raw: "https://proxy.example.com/openai",
			wantDisposition: DispositionReview, wantReview: ReviewNonconforming},
		{name: "openai/glm裸origin补paas_v4而非v1", preset: contracts.PresetGLMGeneralOpenAIV1, raw: "https://open.bigmodel.cn",
			wantDisposition: DispositionRewrite, wantNew: "https://open.bigmodel.cn/api/paas/v4", wantChangeReason: ReasonAppendVersionRoot},
		{name: "openai/glm默认值带尾斜杠等值规整", preset: contracts.PresetGLMGeneralOpenAIV1, raw: "https://open.bigmodel.cn/api/paas/v4/",
			wantDisposition: DispositionRewrite, wantNew: "https://open.bigmodel.cn/api/paas/v4", wantChangeReason: ReasonNormalizeSlash},
		{name: "openai/glm_coding裸origin补coding根", preset: contracts.PresetGLMCodingOpenAIV1, raw: "https://open.bigmodel.cn",
			wantDisposition: DispositionRewrite, wantNew: "https://open.bigmodel.cn/api/coding/paas/v4", wantChangeReason: ReasonAppendVersionRoot},
		{name: "openai/gemini_openai_chat裸origin补v1beta/openai", preset: contracts.PresetGeminiOpenAIChatV1Beta, raw: "https://generativelanguage.googleapis.com",
			wantDisposition: DispositionRewrite, wantNew: "https://generativelanguage.googleapis.com/v1beta/openai", wantChangeReason: ReasonAppendVersionRoot},
		{name: "openai/minimax裸origin补v1", preset: contracts.PresetMiniMaxMedia, raw: "https://api.minimax.chat",
			wantDisposition: DispositionRewrite, wantNew: "https://api.minimax.chat/v1", wantChangeReason: ReasonAppendVersionRoot},
		{name: "openai/接口路径后缀进复核", preset: contracts.PresetGPTOpenAIV1, raw: "https://api.openai.com/v1/chat",
			wantDisposition: DispositionReview, wantReview: ReviewNonconforming},
		{name: "openai/非URL进复核", preset: contracts.PresetGPTOpenAIV1, raw: "not a url",
			wantDisposition: DispositionReview, wantReview: ReviewInvalidURL},
		{name: "openai/带query进复核", preset: contracts.PresetGPTOpenAIV1, raw: "https://api.openai.com/v1?x=1",
			wantDisposition: DispositionReview, wantReview: ReviewInvalidURL},
		// ── anthropic 族 ─────────────────────────────────────────────
		{name: "anthropic/空值回填registry默认", preset: contracts.PresetAnthropicV1, raw: "",
			wantDisposition: DispositionFill, wantNew: "https://api.anthropic.com/v1", wantChangeReason: ReasonFillRegistryDefault},
		{name: "anthropic/已合规不变", preset: contracts.PresetAnthropicV1, raw: "https://api.anthropic.com/v1",
			wantDisposition: DispositionUnchanged},
		{name: "anthropic/大小写不敏感后缀合规", preset: contracts.PresetAnthropicV1, raw: "https://api.anthropic.com/V1",
			wantDisposition: DispositionUnchanged},
		{name: "anthropic/旧deepseek默认路径进复核（契约：其他→复核桶）", preset: contracts.PresetDeepSeekAnthropicV1, raw: "https://api.deepseek.com/anthropic",
			wantDisposition: DispositionReview, wantReview: ReviewNonconforming},
		{name: "anthropic/新deepseek默认合规", preset: contracts.PresetDeepSeekAnthropicV1, raw: "https://api.deepseek.com/anthropic/v1",
			wantDisposition: DispositionUnchanged},
		{name: "anthropic/旧glm_coding默认路径进复核", preset: contracts.PresetGLMCodingAnthropicV1, raw: "https://open.bigmodel.cn/api/anthropic",
			wantDisposition: DispositionReview, wantReview: ReviewNonconforming},
		{name: "anthropic/裸origin补v1", preset: contracts.PresetAnthropicV1, raw: "https://proxy.example.com",
			wantDisposition: DispositionRewrite, wantNew: "https://proxy.example.com/v1", wantChangeReason: ReasonAppendVersionRoot},
		// ── gemini 原生族 ────────────────────────────────────────────
		{name: "gemini/空值回填registry默认", preset: contracts.PresetGeminiNativeV1Beta, raw: "",
			wantDisposition: DispositionFill, wantNew: "https://generativelanguage.googleapis.com/v1beta", wantChangeReason: ReasonFillRegistryDefault},
		{name: "gemini/裸origin补v1beta", preset: contracts.PresetGeminiNativeV1Beta, raw: "https://generativelanguage.googleapis.com",
			wantDisposition: DispositionRewrite, wantNew: "https://generativelanguage.googleapis.com/v1beta", wantChangeReason: ReasonAppendVersionRoot},
		{name: "gemini/已合规不变", preset: contracts.PresetGeminiNativeV1Beta, raw: "https://generativelanguage.googleapis.com/v1beta",
			wantDisposition: DispositionUnchanged},
		{name: "gemini/generic已合规不变", preset: contracts.PresetGenericGeminiV1Beta, raw: "https://proxy.example.com/v1beta",
			wantDisposition: DispositionUnchanged},
		// ── qwen ─────────────────────────────────────────────────────
		{name: "qwen/compatible-mode后缀剥回origin", preset: contracts.PresetQwenDashScope, raw: "https://dashscope.aliyuncs.com/compatible-mode/v1",
			wantDisposition: DispositionRewrite, wantNew: "https://dashscope.aliyuncs.com", wantChangeReason: ReasonStripQwenSuffix},
		{name: "qwen/裸origin即目标形态不变", preset: contracts.PresetQwenDashScope, raw: "https://dashscope.aliyuncs.com",
			wantDisposition: DispositionUnchanged},
		{name: "qwen/空值不变", preset: contracts.PresetQwenDashScope, raw: "",
			wantDisposition: DispositionUnchanged},
		{name: "qwen/自定义origin带后缀剥回origin", preset: contracts.PresetQwenDashScope, raw: "https://proxy.example.com/compatible-mode/v1/",
			wantDisposition: DispositionRewrite, wantNew: "https://proxy.example.com", wantChangeReason: ReasonStripQwenSuffix},
		{name: "qwen/其他路径进复核", preset: contracts.PresetQwenDashScope, raw: "https://dashscope.aliyuncs.com/api/v1",
			wantDisposition: DispositionReview, wantReview: ReviewNonconforming},
		// ── volcengine_ark ───────────────────────────────────────────
		{name: "ark/空值回填registry默认", preset: contracts.PresetVolcengineArk, raw: "",
			wantDisposition: DispositionFill, wantNew: "https://ark.cn-beijing.volces.com/api/v3", wantChangeReason: ReasonFillRegistryDefault},
		{name: "ark/裸origin补api/v3", preset: contracts.PresetVolcengineArk, raw: "https://ark.cn-beijing.volces.com",
			wantDisposition: DispositionRewrite, wantNew: "https://ark.cn-beijing.volces.com/api/v3", wantChangeReason: ReasonAppendVersionRoot},
		{name: "ark/已合规不变", preset: contracts.PresetVolcengineArk, raw: "https://ark.cn-beijing.volces.com/api/v3",
			wantDisposition: DispositionUnchanged},
		{name: "ark/agent plan 官方根已合规不变", preset: contracts.PresetVolcengineArk, raw: "https://ark.cn-beijing.volces.com/api/plan/v3",
			wantDisposition: DispositionUnchanged},
		{name: "ark/coding plan 官方根已合规不变", preset: contracts.PresetVolcengineArk, raw: "https://ark.cn-beijing.volces.com/api/coding/v3",
			wantDisposition: DispositionUnchanged},
		{name: "openai/方舟agent plan根（deepseek档案挂载）已合规不变", preset: contracts.PresetDeepSeekOpenAIV1, raw: "https://ark.cn-beijing.volces.com/api/plan/v3",
			wantDisposition: DispositionUnchanged},
		{name: "ark/其他版本路径进复核", preset: contracts.PresetVolcengineArk, raw: "https://ark.cn-beijing.volces.com/api/v2",
			wantDisposition: DispositionReview, wantReview: ReviewNonconforming},
		// ── 无规则家族 ───────────────────────────────────────────────
		{name: "openspeech/registry默认值保留", preset: contracts.PresetVolcengineOpenSpeech, raw: "https://openspeech.bytedance.com",
			wantDisposition: DispositionUnchanged},
		{name: "openspeech/非默认值进复核", preset: contracts.PresetVolcengineOpenSpeech, raw: "https://custom.example.com",
			wantDisposition: DispositionReview, wantReview: ReviewUnknownPreset},
		{name: "openspeech/空值进复核", preset: contracts.PresetVolcengineOpenSpeech, raw: "",
			wantDisposition: DispositionReview, wantReview: ReviewUnknownPreset},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := ClassifyBaseURL(testCase.preset, fill(testCase.preset), testCase.raw)
			if got.Disposition != testCase.wantDisposition {
				t.Fatalf("Disposition = %q, want %q（ %+v）", got.Disposition, testCase.wantDisposition, got)
			}
			if got.NewValue != testCase.wantNew {
				t.Fatalf("NewValue = %q, want %q", got.NewValue, testCase.wantNew)
			}
			if got.ChangeReason != testCase.wantChangeReason {
				t.Fatalf("ChangeReason = %q, want %q", got.ChangeReason, testCase.wantChangeReason)
			}
			if got.ReviewReason != testCase.wantReview {
				t.Fatalf("ReviewReason = %q, want %q", got.ReviewReason, testCase.wantReview)
			}
		})
	}
}

// TestClassifyBaseURLIdempotent 对全部改写/回填结果重跑分类，必须 unchanged
// （幂等契约：正式执行可重跑）。
func TestClassifyBaseURLIdempotent(t *testing.T) {
	fill := func(preset string) string { return contracts.PresetDefaultBaseURL(preset) }
	cases := []struct{ preset, raw string }{
		{contracts.PresetGPTOpenAIV1, ""},
		{contracts.PresetDeepSeekOpenAIV1, "https://api.deepseek.com"},
		{contracts.PresetGenericOpenAIV1, "https://proxy.example.com"},
		{contracts.PresetGLMGeneralOpenAIV1, "https://open.bigmodel.cn"},
		{contracts.PresetGLMGeneralOpenAIV1, "https://open.bigmodel.cn/api/paas/v4/"},
		{contracts.PresetGLMCodingOpenAIV1, "https://open.bigmodel.cn"},
		{contracts.PresetGeminiOpenAIChatV1Beta, "https://generativelanguage.googleapis.com"},
		{contracts.PresetMiniMaxMedia, "https://api.minimax.chat"},
		{contracts.PresetAnthropicV1, ""},
		{contracts.PresetAnthropicV1, "https://proxy.example.com"},
		{contracts.PresetGeminiNativeV1Beta, "https://generativelanguage.googleapis.com"},
		{contracts.PresetQwenDashScope, "https://dashscope.aliyuncs.com/compatible-mode/v1"},
		{contracts.PresetQwenDashScope, "https://proxy.example.com/compatible-mode/v1/"},
		{contracts.PresetVolcengineArk, ""},
		{contracts.PresetVolcengineArk, "https://ark.cn-beijing.volces.com"},
	}
	for _, testCase := range cases {
		first := ClassifyBaseURL(testCase.preset, fill(testCase.preset), testCase.raw)
		if first.Disposition != DispositionRewrite && first.Disposition != DispositionFill {
			t.Fatalf("%s/%s 首次分类 = %q，期望 rewrite/fill", testCase.preset, testCase.raw, first.Disposition)
		}
		second := ClassifyBaseURL(testCase.preset, fill(testCase.preset), first.NewValue)
		if second.Disposition != DispositionUnchanged {
			t.Fatalf("%s/%s 幂等重跑 = %q（new=%q），期望 unchanged", testCase.preset, testCase.raw, second.Disposition, first.NewValue)
		}
	}
}

// TestAccountPresetHint 钉住账户级 (vendor, protocol) → preset 提示表：家族
// 判定与裸 origin 补根按供应商与协议档案落地。
func TestAccountPresetHint(t *testing.T) {
	cases := []struct {
		vendor, protocol string
		wantPreset       string
	}{
		{"gpt", "openai", contracts.PresetGPTOpenAIV1},
		{"xai", "openai", contracts.PresetXAIOpenAIV1},
		{"deepseek", "openai", contracts.PresetDeepSeekOpenAIV1},
		{"deepseek", "anthropic", contracts.PresetDeepSeekAnthropicV1},
		{"anthropic", "anthropic", contracts.PresetAnthropicV1},
		{"gemini", "gemini", contracts.PresetGeminiNativeV1Beta},
		{"gemini", "openai", contracts.PresetGeminiOpenAIChatV1Beta},
		{"glm", "openai", contracts.PresetGLMGeneralOpenAIV1},
		{"glm", "anthropic", contracts.PresetGLMCodingAnthropicV1},
		{"minimax", "openai", contracts.PresetMiniMaxMedia},
		{"volcengine", "openai", contracts.PresetVolcengineArk},
		{"qwen", "openai", contracts.PresetQwenDashScope},
		{"", "openai", contracts.PresetGenericOpenAIV1},
		{"", "anthropic", contracts.PresetGenericAnthropicV1},
		{"", "gemini", contracts.PresetGenericGeminiV1Beta},
		{"unknown_vendor", "openai", ""},
		{"glm", "unknown_protocol", ""},
	}
	for _, testCase := range cases {
		if got := AccountPresetHint(testCase.vendor, testCase.protocol); got != testCase.wantPreset {
			t.Fatalf("AccountPresetHint(%q, %q) = %q, want %q", testCase.vendor, testCase.protocol, got, testCase.wantPreset)
		}
	}
	// 账户级行为抽检：glm openai 账户裸 origin 按 general 根补；generic 空值
	// 复核；qwen 空值不变；未知组合复核。
	if got := ClassifyBaseURL(AccountPresetHint("glm", "openai"), contracts.PresetDefaultBaseURL(AccountPresetHint("glm", "openai")), "https://open.bigmodel.cn"); got.NewValue != "https://open.bigmodel.cn/api/paas/v4" || got.Disposition != DispositionRewrite {
		t.Fatalf("glm 账户裸 origin 分类 = %+v", got)
	}
	if got := ClassifyBaseURL(AccountPresetHint("", "openai"), contracts.PresetDefaultBaseURL(AccountPresetHint("", "openai")), ""); got.Disposition != DispositionReview || got.ReviewReason != ReviewNoDefault {
		t.Fatalf("generic 账户空值分类 = %+v", got)
	}
	if got := ClassifyBaseURL(AccountPresetHint("qwen", "openai"), contracts.PresetDefaultBaseURL(AccountPresetHint("qwen", "openai")), ""); got.Disposition != DispositionUnchanged {
		t.Fatalf("qwen 账户空值分类 = %+v", got)
	}
	if got := ClassifyBaseURL(AccountPresetHint("unknown", "openai"), "", "https://proxy.example.com/v1"); got.Disposition != DispositionReview || got.ReviewReason != ReviewUnknownPreset {
		t.Fatalf("未知组合分类 = %+v", got)
	}
}
