package contracts

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// testDriverCodes 是全部 10 个 connection driver。
var testDriverCodes = []string{
	DriverGenericHTTP, DriverGPT, DriverXAI, DriverDeepSeek, DriverAnthropic,
	DriverGemini, DriverGLM, DriverMiniMax, DriverVolcengine, DriverQwen,
}

// testAccountTypes 是 3 个 account type；testOAuthTypes 是 4 个 oauth 取值
// （空字符串表达 SQL NULL，加 3 个 google_oauth 值）。
var (
	testAccountTypes = []string{AccountTypeAPIKey, AccountTypeOAuth, AccountTypeGoogleOAuth}
	testOAuthTypes   = []string{"", OAuthTypeCodeAssist, OAuthTypeGoogleOne, OAuthTypeAIStudio}
)

// testPresets 是全部 17 个 connection preset。
var testPresets = []string{
	PresetGenericOpenAIV1, PresetGenericAnthropicV1, PresetGenericGeminiV1Beta,
	PresetGPTOpenAIV1, PresetXAIOpenAIV1, PresetDeepSeekOpenAIV1, PresetDeepSeekAnthropicV1,
	PresetAnthropicV1, PresetGeminiOpenAIChatV1Beta, PresetGeminiNativeV1Beta,
	PresetGLMGeneralOpenAIV1, PresetGLMCodingOpenAIV1, PresetGLMCodingAnthropicV1,
	PresetMiniMaxMedia, PresetVolcengineArk, PresetVolcengineOpenSpeech, PresetQwenDashScope,
}

// testDomains 是 4 个 capability domain。
var testDomains = []string{DomainChat, DomainVideo, DomainAudio, DomainImage}

// trailingFieldPattern 匹配 canonical JSON 中 defaultBaseUrl 之后仍有后续字段的
// 反例（defaultBaseUrl 必须是每元素的末位字段，其后只允许元素收尾 `}`）。
var trailingFieldPattern = regexp.MustCompile(`"defaultBaseUrl":(null|"[^"]*"),`)

func toSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func setsEqual(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for value := range a {
		if _, ok := b[value]; !ok {
			return false
		}
	}
	return true
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// TestConnectorRegistryComboCounts 断言设计 §1.1 :52 的 49 条与按 driver 计数。
func TestConnectorRegistryComboCounts(t *testing.T) {
	combos := AllowedConnectorCombos()
	if len(combos) != 49 {
		t.Fatalf("AllowedConnectorCombos() = %d 条, want 49", len(combos))
	}
	counts := ComboDriverCounts()
	expected := map[string]int{
		DriverGenericHTTP: 8,
		DriverGPT:         5,
		DriverXAI:         3,
		DriverDeepSeek:    2,
		DriverAnthropic:   2,
		DriverGemini:      13,
		DriverGLM:         7,
		DriverMiniMax:     2,
		DriverVolcengine:  4,
		DriverQwen:        3,
	}
	if len(counts) != len(expected) {
		t.Fatalf("ComboDriverCounts() 覆盖 %d 个 driver, want %d", len(counts), len(expected))
	}
	for driver, want := range expected {
		if counts[driver] != want {
			t.Errorf("driver %q 组合数 = %d, want %d", driver, counts[driver], want)
		}
	}
}

// TestConnectorRegistryModeComboCounts 断言设计 §1.1 :54 的 162 条与按 driver 计数，
// 并复核每条 mode combo 都来自同一份 AllowedModes 推导。
func TestConnectorRegistryModeComboCounts(t *testing.T) {
	modeCombos := AllowedEndpointModeCombos()
	if len(modeCombos) != 162 {
		t.Fatalf("AllowedEndpointModeCombos() = %d 条, want 162", len(modeCombos))
	}
	counts := ModeComboDriverCounts()
	expected := map[string]int{
		DriverGenericHTTP: 30,
		DriverGPT:         18,
		DriverXAI:         10,
		DriverDeepSeek:    6,
		DriverAnthropic:   6,
		DriverGemini:      46,
		DriverGLM:         18,
		DriverMiniMax:     5,
		DriverVolcengine:  13,
		DriverQwen:        10,
	}
	if len(counts) != len(expected) {
		t.Fatalf("ModeComboDriverCounts() 覆盖 %d 个 driver, want %d", len(counts), len(expected))
	}
	for driver, want := range expected {
		if counts[driver] != want {
			t.Errorf("driver %q 单 mode 组合数 = %d, want %d", driver, counts[driver], want)
		}
	}
	// 单 mode 展开与 AllowedModes 同源：逐 combo 求和必须等于 162。
	sum := 0
	for _, combo := range AllowedConnectorCombos() {
		sum += len(AllowedModes(combo.Domain, combo.Preset, combo.AccountType, combo.OAuthType))
	}
	if sum != 162 {
		t.Fatalf("49 个组合的 AllowedModes 求和 = %d, want 162", sum)
	}
}

// TestConnectorRegistrySortedStable 断言两条清单的排序稳定性契约。
func TestConnectorRegistrySortedStable(t *testing.T) {
	combos := AllowedConnectorCombos()
	for i := 1; i < len(combos); i++ {
		if connectorComboLess(combos[i-1], combos[i]) == connectorComboLess(combos[i], combos[i-1]) && combos[i-1] != combos[i] {
			t.Fatalf("连接组合清单在位置 %d 违反排序: %+v vs %+v", i, combos[i-1], combos[i])
		}
	}
	modeCombos := AllowedEndpointModeCombos()
	for i := 1; i < len(modeCombos); i++ {
		if endpointModeComboLess(modeCombos[i-1], modeCombos[i]) == endpointModeComboLess(modeCombos[i], modeCombos[i-1]) {
			t.Fatalf("单 mode 组合清单在位置 %d 违反排序", i)
		}
	}
}

// TestConnectorRegistryCartesianProduct 遍历 10 driver × 3 account type × 4 oauth 值 ×
// 17 preset × 4 domain = 8160 组合，断言恰 49 允许、8111 拒绝，且允许集合与
// AllowedConnectorCombos() 精确一致。
func TestConnectorRegistryCartesianProduct(t *testing.T) {
	total := len(testDriverCodes) * len(testAccountTypes) * len(testOAuthTypes) * len(testPresets) * len(testDomains)
	if total != 8160 {
		t.Fatalf("笛卡尔积空间 = %d, want 8160", total)
	}
	allowedKeys := make(map[string]struct{}, 49)
	for _, combo := range AllowedConnectorCombos() {
		allowedKeys[connectorComboKey(combo.Driver, combo.AccountType, combo.OAuthType, combo.Preset, combo.Domain)] = struct{}{}
	}
	allowed, rejected := 0, 0
	for _, driver := range testDriverCodes {
		for _, accountType := range testAccountTypes {
			for _, oauthType := range testOAuthTypes {
				for _, preset := range testPresets {
					for _, domain := range testDomains {
						verdict := ConnectorComboVerdict(driver, accountType, oauthType, preset, domain)
						if verdict != IsAllowedConnectorCombo(driver, accountType, oauthType, preset, domain) {
							t.Fatalf("ConnectorComboVerdict 与 IsAllowedConnectorCombo 不一致: %s/%s/%s/%s/%s",
								driver, accountType, oauthType, preset, domain)
						}
						key := connectorComboKey(driver, accountType, oauthType, preset, domain)
						_, registered := allowedKeys[key]
						if verdict != registered {
							t.Fatalf("判定结果与注册表清单不一致: %s/%s/%s/%s/%s",
								driver, accountType, oauthType, preset, domain)
						}
						if verdict {
							allowed++
						} else {
							rejected++
						}
					}
				}
			}
		}
	}
	if allowed != 49 {
		t.Fatalf("允许组合数 = %d, want 49", allowed)
	}
	if rejected != 8111 {
		t.Fatalf("拒绝组合数 = %d, want 8111", rejected)
	}
}

// TestDomainLanes 断言设计 §2.2 域 lane 的 13/4/7/1 与 25 个唯一 mode。
func TestDomainLanes(t *testing.T) {
	if len(capabilityDomainLanes) != 4 {
		t.Fatalf("域 lane 数 = %d, want 4", len(capabilityDomainLanes))
	}
	expected := map[string][]string{
		DomainChat: {
			ModeChatJSON, ModeChatSSE, ModeResponsesJSON, ModeResponsesSSE,
			ModeMessagesJSON, ModeMessagesSSE, ModeMessageTokenCounting,
			ModeGenerateContentJSON, ModeGenerateContentSSE,
			ModeInteractionsJSON, ModeInteractionsSSE, ModeCountTokens, ModeEmbedContent,
		},
		DomainVideo: {ModeVideoCreate, ModeVideoGet, ModeVideoContent, ModeVideoCancel},
		DomainAudio: {
			ModeAudioSpeech, ModeAudioTranscriptionJSON, ModeAudioJobCreate,
			ModeAudioJobGet, ModeAudioJobContent, ModeAudioJobCancel, ModeRealtimeSession,
		},
		DomainImage: {ModeImagesJSON},
	}
	expectedSizes := map[string]int{DomainChat: 13, DomainVideo: 4, DomainAudio: 7, DomainImage: 1}
	seen := make(map[string]int)
	for domain, want := range expected {
		got, ok := capabilityDomainLanes[domain]
		if !ok {
			t.Fatalf("缺少域 lane %q", domain)
		}
		if len(got) != expectedSizes[domain] {
			t.Errorf("域 %q lane 大小 = %d, want %d", domain, len(got), expectedSizes[domain])
		}
		if !setsEqual(toSet(got), toSet(want)) {
			t.Errorf("域 %q lane 集合不相等: got %v, want %v", domain, got, want)
		}
		for _, mode := range got {
			seen[mode]++
		}
	}
	if len(seen) != 25 {
		t.Fatalf("域 lane 并集 = %d 个唯一 mode, want 25", len(seen))
	}
	for mode, count := range seen {
		if count != 1 {
			t.Errorf("mode %q 出现在 %d 个 lane, want 1", mode, count)
		}
	}
}

// TestPresetVocabularies 按设计 §2.2 的 8 行词表逐 preset 断言集合相等。
func TestPresetVocabularies(t *testing.T) {
	expectedByPreset := map[string]map[string]struct{}{
		// 行 1：*_openai_v1 与 generic_openai_v1。
		PresetGenericOpenAIV1:    toSet(openAIVocabulary),
		PresetGPTOpenAIV1:        toSet(openAIVocabulary),
		PresetXAIOpenAIV1:        toSet(openAIVocabulary),
		PresetDeepSeekOpenAIV1:   toSet(openAIVocabulary),
		PresetGLMGeneralOpenAIV1: toSet(openAIVocabulary),
		PresetGLMCodingOpenAIV1:  toSet(openAIVocabulary),
		// 行 2：gemini_openai_chat_v1beta。
		PresetGeminiOpenAIChatV1Beta: toSet(geminiOpenAIChatVocabulary),
		// 行 3：*_anthropic_v1 与 generic_anthropic_v1。
		PresetGenericAnthropicV1:   toSet(anthropicVocabulary),
		PresetDeepSeekAnthropicV1:  toSet(anthropicVocabulary),
		PresetAnthropicV1:          toSet(anthropicVocabulary),
		PresetGLMCodingAnthropicV1: toSet(anthropicVocabulary),
		// 行 4：gemini_native_v1beta 与 generic_gemini_v1beta。
		PresetGeminiNativeV1Beta:  toSet(geminiNativeVocabulary),
		PresetGenericGeminiV1Beta: toSet(geminiNativeVocabulary),
		// 行 5-8：厂商媒体 preset。
		PresetMiniMaxMedia:         toSet(miniMaxMediaVocabulary),
		PresetVolcengineArk:        toSet(volcengineArkVocabulary),
		PresetVolcengineOpenSpeech: toSet(volcengineOpenSpeechVocabulary),
		PresetQwenDashScope:        toSet(qwenDashScopeVocabulary),
	}
	if len(expectedByPreset) != 17 {
		t.Fatalf("词表断言覆盖 %d 个 preset, want 17", len(expectedByPreset))
	}
	if len(connectorPresetRecords) != 17 {
		t.Fatalf("registry preset record 数 = %d, want 17", len(connectorPresetRecords))
	}
	for _, record := range connectorPresetRecords {
		want, ok := expectedByPreset[record.Code]
		if !ok {
			t.Fatalf("registry 出现未断言的 preset %q", record.Code)
		}
		got := toSet(record.Vocabulary)
		if !setsEqual(got, want) {
			t.Errorf("preset %q 完整词表与设计 §2.2 不相等", record.Code)
		}
	}
	knownModes := make(map[string]struct{})
	for _, lane := range capabilityDomainLanes {
		for _, mode := range lane {
			knownModes[mode] = struct{}{}
		}
	}
	for _, record := range connectorPresetRecords {
		for _, mode := range record.Vocabulary {
			if _, ok := knownModes[mode]; !ok {
				t.Errorf("preset %q 词表包含未知 mode %q", record.Code, mode)
			}
		}
	}
}

// TestPresetAuthStrategies 断言设计 §1.1 :58 的认证策略引用：
// 17 个 preset 的 AuthStrategyKey 非空且属于 4 个合法值；
// generic 三预设精确匹配设计 §1 附加规则 1 的固定认证，其余 preset 为 driver_defined。
func TestPresetAuthStrategies(t *testing.T) {
	legalValues := toSet([]string{
		AuthStrategyGenericBearer,
		AuthStrategyGenericAnthropicHeaders,
		AuthStrategyGenericGoogAPIKey,
		AuthStrategyDriverDefined,
	})
	genericExpected := map[string]string{
		PresetGenericOpenAIV1:     AuthStrategyGenericBearer,
		PresetGenericAnthropicV1:  AuthStrategyGenericAnthropicHeaders,
		PresetGenericGeminiV1Beta: AuthStrategyGenericGoogAPIKey,
	}
	if len(connectorPresetRecords) != 17 {
		t.Fatalf("registry preset record 数 = %d, want 17", len(connectorPresetRecords))
	}
	for _, record := range connectorPresetRecords {
		if record.AuthStrategyKey == "" {
			t.Errorf("preset %q 的认证策略引用为空", record.Code)
			continue
		}
		if _, ok := legalValues[record.AuthStrategyKey]; !ok {
			t.Errorf("preset %q 的认证策略引用 %q 不在 4 个合法值集合内", record.Code, record.AuthStrategyKey)
		}
		if want, ok := genericExpected[record.Code]; ok {
			if record.AuthStrategyKey != want {
				t.Errorf("generic preset %q 的认证策略引用 = %q, want %q", record.Code, record.AuthStrategyKey, want)
			}
			delete(genericExpected, record.Code)
		} else if record.AuthStrategyKey != AuthStrategyDriverDefined {
			t.Errorf("真实厂商 preset %q 的认证策略引用 = %q, want %q", record.Code, record.AuthStrategyKey, AuthStrategyDriverDefined)
		}
	}
	if len(genericExpected) != 0 {
		t.Fatalf("generic 三预设未被 registry 覆盖: %v", genericExpected)
	}
}

// TestDriverNarrowingRules 断言设计 §2.2 收窄规则：gpt.oauth/xai.oauth、glm OpenAI、
// deepseek/glm anthropic 收窄，官方 anthropic 与 generic_anthropic_v1 完整词表，
// gemini google_oauth 同词表不再收窄，generic_http 不收窄。
func TestDriverNarrowingRules(t *testing.T) {
	cases := []struct {
		name        string
		preset      string
		accountType string
		oauthType   string
		want        []string
	}{
		{"gpt oauth 收窄为 responses 双 mode", PresetGPTOpenAIV1, AccountTypeOAuth, "", responsesOnlyVocabulary},
		{"xai oauth 收窄为 responses 双 mode", PresetXAIOpenAIV1, AccountTypeOAuth, "", responsesOnlyVocabulary},
		{"gpt api_key 不收窄", PresetGPTOpenAIV1, AccountTypeAPIKey, "", openAIVocabulary},
		{"generic_openai_v1 不收窄", PresetGenericOpenAIV1, AccountTypeAPIKey, "", openAIVocabulary},
		{"glm general OpenAI 收窄", PresetGLMGeneralOpenAIV1, AccountTypeAPIKey, "", glmOpenAINarrowedVocabulary},
		{"glm coding OpenAI 收窄", PresetGLMCodingOpenAIV1, AccountTypeAPIKey, "", glmOpenAINarrowedVocabulary},
		{"deepseek anthropic 收窄", PresetDeepSeekAnthropicV1, AccountTypeAPIKey, "", messagesOnlyVocabulary},
		{"glm coding anthropic 收窄", PresetGLMCodingAnthropicV1, AccountTypeAPIKey, "", messagesOnlyVocabulary},
		{"官方 anthropic api_key 完整词表", PresetAnthropicV1, AccountTypeAPIKey, "", anthropicVocabulary},
		{"官方 anthropic oauth 完整词表", PresetAnthropicV1, AccountTypeOAuth, "", anthropicVocabulary},
		{"generic_anthropic_v1 完整词表", PresetGenericAnthropicV1, AccountTypeAPIKey, "", anthropicVocabulary},
		{"generic_gemini_v1beta 不收窄", PresetGenericGeminiV1Beta, AccountTypeAPIKey, "", geminiNativeVocabulary},
	}
	for _, tc := range cases {
		got := presetVocabulary(tc.preset, tc.accountType, tc.oauthType)
		if !setsEqual(got, toSet(tc.want)) {
			t.Errorf("%s: presetVocabulary(%q, %q, %q) 与期望词表不相等", tc.name, tc.preset, tc.accountType, tc.oauthType)
		}
	}
	nativeSet := presetVocabulary(PresetGeminiNativeV1Beta, AccountTypeAPIKey, "")
	for _, oauthType := range []string{OAuthTypeCodeAssist, OAuthTypeGoogleOne, OAuthTypeAIStudio} {
		got := presetVocabulary(PresetGeminiNativeV1Beta, AccountTypeGoogleOAuth, oauthType)
		if !setsEqual(got, nativeSet) {
			t.Errorf("gemini google_oauth %q 应与 api_key 同词表（不再收窄）", oauthType)
		}
		if !setsEqual(got, toSet(geminiNativeVocabulary)) {
			t.Errorf("gemini google_oauth %q 应保持 Gemini native 完整词表", oauthType)
		}
	}
}

// TestAllowedModesIntersections 断言交集语义、排序输出与空交集返回空。
func TestAllowedModesIntersections(t *testing.T) {
	emptyCases := []struct {
		domain      string
		preset      string
		accountType string
		oauthType   string
	}{
		{DomainImage, PresetGenericAnthropicV1, AccountTypeAPIKey, ""},
		{DomainChat, PresetMiniMaxMedia, AccountTypeAPIKey, ""},
		{DomainImage, PresetVolcengineOpenSpeech, AccountTypeAPIKey, ""},
		{DomainImage, PresetGeminiNativeV1Beta, AccountTypeAPIKey, ""},
		{"unknown_domain", PresetGenericOpenAIV1, AccountTypeAPIKey, ""},
		{DomainChat, "unknown_preset", AccountTypeAPIKey, ""},
	}
	for _, tc := range emptyCases {
		got := AllowedModes(tc.domain, tc.preset, tc.accountType, tc.oauthType)
		if len(got) != 0 {
			t.Errorf("AllowedModes(%q, %q, %q, %q) = %v, want 空", tc.domain, tc.preset, tc.accountType, tc.oauthType, got)
		}
	}
	positiveCases := []struct {
		name        string
		domain      string
		preset      string
		accountType string
		oauthType   string
		want        []string
	}{
		{"gemini native audio 交集", DomainAudio, PresetGenericGeminiV1Beta, AccountTypeAPIKey, "", []string{ModeAudioSpeech}},
		{"ark chat 交集", DomainChat, PresetVolcengineArk, AccountTypeAPIKey, "", []string{ModeChatJSON, ModeChatSSE}},
		{"glm general chat 收窄后交集", DomainChat, PresetGLMGeneralOpenAIV1, AccountTypeAPIKey, "", []string{ModeChatJSON, ModeChatSSE}},
		{"gpt oauth chat 交集", DomainChat, PresetGPTOpenAIV1, AccountTypeOAuth, "", []string{ModeResponsesJSON, ModeResponsesSSE}},
		{"deepseek 两 preset 合计 6 mode", DomainChat, PresetDeepSeekAnthropicV1, AccountTypeAPIKey, "", []string{ModeMessagesJSON, ModeMessagesSSE}},
	}
	for _, tc := range positiveCases {
		got := AllowedModes(tc.domain, tc.preset, tc.accountType, tc.oauthType)
		if !setsEqual(toSet(got), toSet(tc.want)) || len(got) != len(tc.want) {
			t.Errorf("%s: AllowedModes(%q, %q, %q, %q) = %v, want %v", tc.name, tc.domain, tc.preset, tc.accountType, tc.oauthType, got, tc.want)
			continue
		}
		for i := 1; i < len(got); i++ {
			if got[i-1] >= got[i] {
				t.Errorf("%s: AllowedModes 返回未按升序排序: %v", tc.name, got)
				break
			}
		}
	}
}

// TestPresetDefaultBaseURLs 断言设计 :58-80 的 nullable default_base_url 17 值表：
// preset record 字段逐值精确匹配；generic 三 preset 为空串（registry 表达 SQL NULL）；
// canonical JSON 中 17 个 preset 的 defaultBaseUrl 逐值精确匹配且 generic 三 preset
// 序列化为 JSON null（标量按 canonical bytes 原样写入，不参与排序）。
func TestPresetDefaultBaseURLs(t *testing.T) {
	// 2026-10 上游 Base URL 版本根语义 v2（R9）：default_base_url 一律是
	// 版本根形态——deepseek_openai_v1（/v1）、deepseek_anthropic_v1
	//（/anthropic/v1）、glm_coding_anthropic_v1（/api/anthropic/v1）、
	// volcengine_ark（/api/v3）、gemini_native_v1beta（/v1beta）、
	// minimax_media（/v1）六处由服务根改写为版本根。
	expected := map[string]string{
		PresetGenericOpenAIV1:        "",
		PresetGenericAnthropicV1:     "",
		PresetGenericGeminiV1Beta:    "",
		PresetGPTOpenAIV1:            "https://api.openai.com/v1",
		PresetXAIOpenAIV1:            "https://api.x.ai/v1",
		PresetDeepSeekOpenAIV1:       "https://api.deepseek.com/v1",
		PresetDeepSeekAnthropicV1:    "https://api.deepseek.com/anthropic/v1",
		PresetAnthropicV1:            "https://api.anthropic.com/v1",
		PresetGeminiOpenAIChatV1Beta: "https://generativelanguage.googleapis.com/v1beta/openai",
		PresetGeminiNativeV1Beta:     "https://generativelanguage.googleapis.com/v1beta",
		PresetGLMGeneralOpenAIV1:     "https://open.bigmodel.cn/api/paas/v4",
		PresetGLMCodingOpenAIV1:      "https://open.bigmodel.cn/api/coding/paas/v4",
		PresetGLMCodingAnthropicV1:   "https://open.bigmodel.cn/api/anthropic/v1",
		PresetMiniMaxMedia:           "https://api.minimax.chat/v1",
		PresetVolcengineArk:          "https://ark.cn-beijing.volces.com/api/v3",
		PresetVolcengineOpenSpeech:   "https://openspeech.bytedance.com",
		PresetQwenDashScope:          "https://dashscope.aliyuncs.com",
	}
	if len(expected) != 17 {
		t.Fatalf("default_base_url 断言覆盖 %d 个 preset, want 17", len(expected))
	}
	for _, record := range connectorPresetRecords {
		want, ok := expected[record.Code]
		if !ok {
			t.Fatalf("registry 出现未断言的 preset %q", record.Code)
		}
		if record.DefaultBaseURL != want {
			t.Errorf("preset %q 的 DefaultBaseURL = %q, want %q", record.Code, record.DefaultBaseURL, want)
		}
	}

	// canonical JSON 逐元素断言：defaultBaseUrl 按 preset 精确匹配，generic 三
	// preset 输出 JSON null（指针为 nil），其余为 canonical bytes 原样字符串。
	var elements []registryFingerprintElement
	if err := json.Unmarshal([]byte(registryCanonicalJSON), &elements); err != nil {
		t.Fatalf("canonical JSON 反序列化失败: %v", err)
	}
	if len(elements) != 162 {
		t.Fatalf("canonical JSON 元素数 = %d, want 162", len(elements))
	}
	checked := make(map[string]int, 17)
	for _, element := range elements {
		want, ok := expected[element.Preset]
		if !ok {
			t.Fatalf("canonical JSON 出现未断言的 preset %q", element.Preset)
		}
		if want == "" {
			if element.DefaultBaseURL != nil {
				t.Errorf("preset %q 的 canonical defaultBaseUrl = %q, want JSON null", element.Preset, *element.DefaultBaseURL)
			}
		} else if element.DefaultBaseURL == nil {
			t.Errorf("preset %q 的 canonical defaultBaseUrl = null, want %q", element.Preset, want)
		} else if *element.DefaultBaseURL != want {
			t.Errorf("preset %q 的 canonical defaultBaseUrl = %q, want %q", element.Preset, *element.DefaultBaseURL, want)
		}
		checked[element.Preset]++
	}
	if len(checked) != 17 {
		t.Fatalf("canonical JSON 覆盖 %d 个 preset, want 17", len(checked))
	}
	for _, preset := range testPresets {
		if checked[preset] == 0 {
			t.Errorf("canonical JSON 缺少 preset %q 的元素", preset)
		}
	}
}

// TestPresetDefaultBaseURLAccessor 断言导出访问器 PresetDefaultBaseURL 与
// record 字段同源：17 个 preset 逐值一致（含 generic 三 preset 空串 = SQL
// NULL），未知 preset 返回空串。
func TestPresetDefaultBaseURLAccessor(t *testing.T) {
	for _, record := range connectorPresetRecords {
		if got := PresetDefaultBaseURL(record.Code); got != record.DefaultBaseURL {
			t.Errorf("PresetDefaultBaseURL(%q) = %q, want %q", record.Code, got, record.DefaultBaseURL)
		}
	}
	if len(connectorPresetRecords) != 17 {
		t.Fatalf("registry preset 数 = %d, want 17", len(connectorPresetRecords))
	}
	if got := PresetDefaultBaseURL("no_such_preset"); got != "" {
		t.Errorf("未知 preset 应返回空串, got %q", got)
	}
}

// TestRegistryFingerprint 断言 fingerprint 稳定、64 位小写 hex，
// 且 canonical JSON 为 162 元素紧凑数组、nullable 字段输出 JSON null。
func TestRegistryFingerprint(t *testing.T) {
	first := RegistryFingerprint()
	second := RegistryFingerprint()
	if first != second {
		t.Fatalf("RegistryFingerprint() 两次调用不一致: %q vs %q", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("fingerprint 长度 = %d, want 64", len(first))
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(first) {
		t.Fatalf("fingerprint 不是 64 位小写 hex: %q", first)
	}
	if !strings.HasPrefix(registryCanonicalJSON, `[{"driver":"`) {
		t.Fatalf("canonical JSON 首元素字段顺序不符合设计固定格式: %q", registryCanonicalJSON[:min(len(registryCanonicalJSON), 32)])
	}
	if strings.ContainsAny(registryCanonicalJSON, "\t\r\n") {
		t.Fatalf("canonical JSON 含缩进或换行，违反紧凑分隔要求")
	}
	// 紧凑分隔指 JSON 结构分隔符后无空格；模板值内部的空格（METHOD 与 path 之间）合法。
	if strings.Contains(registryCanonicalJSON, ", ") || strings.Contains(registryCanonicalJSON, ": ") {
		t.Fatalf("canonical JSON 的结构分隔符后含空格，违反紧凑分隔要求")
	}
	if got := strings.Count(registryCanonicalJSON, `{"driver":"`); got != 162 {
		t.Fatalf("canonical JSON 元素数 = %d, want 162", got)
	}
	if !strings.Contains(registryCanonicalJSON, `"vendor":null`) {
		t.Fatalf("canonical JSON 缺少 generic_http 的 vendor null")
	}
	if !strings.Contains(registryCanonicalJSON, `"oauthType":null`) {
		t.Fatalf("canonical JSON 缺少非 google_oauth 行的 oauthType null")
	}
	if !strings.Contains(registryCanonicalJSON, `"surface":null`) {
		t.Fatalf("canonical JSON 缺少纯媒体 preset 的 surface null")
	}
	// defaultBaseUrl 是标量尾字段：每元素恰一次且其后不得再有任何字段（设计
	// :56/:81；不能按 } 切分——模板值含 {id}/{model} 占位符，故用正则断言尾位；
	// generic 三 preset 输出 null，逐值精确断言见 TestPresetDefaultBaseURLs）。
	if got := strings.Count(registryCanonicalJSON, `"defaultBaseUrl":`); got != 162 {
		t.Fatalf("canonical JSON defaultBaseUrl 出现次数 = %d, want 162", got)
	}
	if trailingFieldPattern.MatchString(registryCanonicalJSON) {
		t.Fatalf("canonical JSON 存在 defaultBaseUrl 之后仍有字段或元素未以 defaultBaseUrl 收尾")
	}
	t.Logf("registry fingerprint = %s", first)
}

// TestRegistryInternalConsistency 复核 driver/preset record、组合行、组合清单与
// 单 mode 清单之间的引用闭合和格式约束。
func TestRegistryInternalConsistency(t *testing.T) {
	seenDrivers := make(map[string]bool)
	for _, record := range connectorDriverRecords {
		if seenDrivers[record.Code] {
			t.Fatalf("driver record 重复: %q", record.Code)
		}
		seenDrivers[record.Code] = true
		if record.Code == DriverGenericHTTP && record.Vendor != "" {
			t.Errorf("generic_http 的 vendor 必须为 NULL（空字符串）")
		}
		if record.Code != DriverGenericHTTP && record.Vendor == "" {
			t.Errorf("真实 driver %q 的 vendor 不能为空", record.Code)
		}
		if len(record.CredentialSchemaKeys) != len(record.AccountTypes) {
			t.Errorf("driver %q 的凭据键与 account type 数量不一致", record.Code)
		}
		for accountType, key := range record.CredentialSchemaKeys {
			if key != record.Code+"."+accountType {
				t.Errorf("driver %q 凭据键 %q 不满足 driver.account_type 格式", record.Code, key)
			}
		}
		for _, accountType := range record.AccountTypes {
			if _, ok := record.CredentialSchemaKeys[accountType]; !ok {
				t.Errorf("driver %q 的 account type %q 缺少凭据键", record.Code, accountType)
			}
		}
		if len(record.OAuthTypes) > 0 && !stringSliceContains(record.AccountTypes, AccountTypeGoogleOAuth) {
			t.Errorf("driver %q 声明了 OAuthTypes 但没有 google_oauth account type", record.Code)
		}
	}
	if len(seenDrivers) != 10 {
		t.Fatalf("driver record 数 = %d, want 10", len(seenDrivers))
	}

	templatePattern := regexp.MustCompile(`^[A-Z]+ /`)
	seenPresets := make(map[string]bool)
	knownModes := make(map[string]struct{})
	for _, lane := range capabilityDomainLanes {
		for _, mode := range lane {
			knownModes[mode] = struct{}{}
		}
	}
	for _, record := range connectorPresetRecords {
		if seenPresets[record.Code] {
			t.Fatalf("preset record 重复: %q", record.Code)
		}
		seenPresets[record.Code] = true
		if record.URLBuilderKey != record.Code {
			t.Errorf("preset %q 的 URL builder key 必须等于 preset code", record.Code)
		}
		if record.Surface != "" && record.Surface != SurfaceOpenAIV1 && record.Surface != SurfaceAnthropicV1 && record.Surface != SurfaceGeminiV1Beta {
			t.Errorf("preset %q 的协议面 %q 非法", record.Code, record.Surface)
		}
		if len(record.AllowedBasePathSuffixes) == 0 {
			t.Errorf("preset %q 的 allowed_base_path_suffixes 为空", record.Code)
		}
		for _, suffix := range record.AllowedBasePathSuffixes {
			if suffix != "" && !strings.HasPrefix(suffix, "/") {
				t.Errorf("preset %q 的 suffix %q 必须为空串或以 / 开头", record.Code, suffix)
			}
		}
		if len(record.OperationPathTemplates) == 0 {
			t.Errorf("preset %q 的 operation_path_templates 为空（至少 1 条）", record.Code)
		}
		for _, template := range record.OperationPathTemplates {
			if !templatePattern.MatchString(template) {
				t.Errorf("preset %q 的模板 %q 不满足 METHOD /path 格式", record.Code, template)
			}
		}
		for _, mode := range record.Vocabulary {
			if _, ok := knownModes[mode]; !ok {
				t.Errorf("preset %q 词表包含未知 mode %q", record.Code, mode)
			}
		}
	}
	if len(seenPresets) != 17 {
		t.Fatalf("preset record 数 = %d, want 17", len(seenPresets))
	}

	// 组合行引用闭合：driver/preset 无孤儿记录，account type 必须在 driver record 内注册，
	// OAuthTypes 只允许出现在 google_oauth 行。
	referencedDrivers := make(map[string]bool)
	referencedPresets := make(map[string]bool)
	for _, row := range connectorComboRows {
		driver, ok := connectorDriverRecordIndex[row.Driver]
		if !ok {
			t.Fatalf("组合行引用未注册 driver %q", row.Driver)
		}
		if _, ok := connectorPresetRecordIndex[row.Preset]; !ok {
			t.Fatalf("组合行引用未注册 preset %q", row.Preset)
		}
		referencedDrivers[row.Driver] = true
		referencedPresets[row.Preset] = true
		for _, accountType := range row.AccountTypes {
			if _, ok := driver.CredentialSchemaKeys[accountType]; !ok {
				t.Errorf("driver %q 组合行使用了未注册的 account type %q", row.Driver, accountType)
			}
		}
		if len(row.OAuthTypes) > 0 {
			for _, accountType := range row.AccountTypes {
				if accountType != AccountTypeGoogleOAuth {
					t.Errorf("driver %q 非 google_oauth 行声明了 OAuthTypes", row.Driver)
				}
			}
		}
	}
	for _, driver := range testDriverCodes {
		if !referencedDrivers[driver] {
			t.Errorf("driver record %q 未被任何组合行引用", driver)
		}
	}
	for _, preset := range testPresets {
		if !referencedPresets[preset] {
			t.Errorf("preset record %q 未被任何组合行引用", preset)
		}
	}

	// 组合清单：无重复、身份投影与 record 一致、oauth type 规则成立。
	keySet := make(map[string]struct{})
	for _, combo := range AllowedConnectorCombos() {
		key := connectorComboKey(combo.Driver, combo.AccountType, combo.OAuthType, combo.Preset, combo.Domain)
		if _, dup := keySet[key]; dup {
			t.Fatalf("连接组合重复: %s", key)
		}
		keySet[key] = struct{}{}
		driver := connectorDriverRecordIndex[combo.Driver]
		preset := connectorPresetRecordIndex[combo.Preset]
		if combo.Vendor != driver.Vendor {
			t.Errorf("组合 %s 的 vendor 投影与 driver record 不一致", key)
		}
		if combo.Surface != preset.Surface {
			t.Errorf("组合 %s 的 surface 投影与 preset record 不一致", key)
		}
		if combo.AccountType == AccountTypeGoogleOAuth {
			if combo.OAuthType == "" {
				t.Errorf("google_oauth 组合 %s 的 oauthType 不能为空", key)
			}
			if !stringSliceContains(driver.OAuthTypes, combo.OAuthType) {
				t.Errorf("google_oauth 组合 %s 的 oauthType %q 未注册", key, combo.OAuthType)
			}
		} else if combo.OAuthType != "" {
			t.Errorf("非 google_oauth 组合 %s 的 oauthType 必须为空（SQL NULL）", key)
		}
		if !IsAllowedConnectorCombo(combo.Driver, combo.AccountType, combo.OAuthType, combo.Preset, combo.Domain) {
			t.Errorf("组合 %s 未通过判定索引", key)
		}
	}

	// 单 mode 清单：无重复、mode 属于域 lane 且属于该组合的 AllowedModes。
	modeKeySet := make(map[string]struct{})
	for _, modeCombo := range AllowedEndpointModeCombos() {
		modeKey := connectorComboKey(modeCombo.Driver, modeCombo.AccountType, modeCombo.OAuthType, modeCombo.Preset, modeCombo.Domain) + "\x00" + modeCombo.Mode
		if _, dup := modeKeySet[modeKey]; dup {
			t.Fatalf("单 mode 组合重复: %s", modeKey)
		}
		modeKeySet[modeKey] = struct{}{}
		if !stringSliceContains(capabilityDomainLanes[modeCombo.Domain], modeCombo.Mode) {
			t.Errorf("单 mode 组合 %s 的 mode %q 不属于域 lane", modeKey, modeCombo.Mode)
		}
		allowed := AllowedModes(modeCombo.Domain, modeCombo.Preset, modeCombo.AccountType, modeCombo.OAuthType)
		if !stringSliceContains(allowed, modeCombo.Mode) {
			t.Errorf("单 mode 组合 %s 不在 AllowedModes 结果中", modeKey)
		}
		preset := connectorPresetRecordIndex[modeCombo.Preset]
		if !setsEqual(toSet(modeCombo.AllowedBaseSuffixes), toSet(preset.AllowedBasePathSuffixes)) {
			t.Errorf("单 mode 组合 %s 的 suffix 副本与 preset record 不一致", modeKey)
		}
		if !setsEqual(toSet(modeCombo.OperationTemplates), toSet(preset.OperationPathTemplates)) {
			t.Errorf("单 mode 组合 %s 的 template 副本与 preset record 不一致", modeKey)
		}
	}

	// 公共协议面模板家族：文本 preset 精确使用公共清单；媒体 preset 精确使用
	// 媒体协议契约清单（逐条章节来源见下方断言注释）。模板自上游 Base URL
	// 版本根语义 v2（R2）起不含版本前缀——版本段（/v1、/v1beta、/api/v3）
	// 属于 base_url 版本根；唯一例外是 qwen_dashscope（base_url 语义是
	// origin，服务路径随操作路径登记，R6 保持全路径，并补登记 OpenAI 兼容
	// 模式 chat 模板）。
	openAITemplateSet := toSet(openAIOperationTemplates)
	for _, preset := range []string{
		PresetGenericOpenAIV1, PresetGPTOpenAIV1, PresetXAIOpenAIV1,
		PresetDeepSeekOpenAIV1, PresetGLMGeneralOpenAIV1, PresetGLMCodingOpenAIV1,
	} {
		if !setsEqual(toSet(connectorPresetRecordIndex[preset].OperationPathTemplates), openAITemplateSet) {
			t.Errorf("preset %q 必须精确使用 OpenAI 公共 15 条模板", preset)
		}
	}
	anthropicTemplateSet := toSet(anthropicOperationTemplates)
	for _, preset := range []string{
		PresetGenericAnthropicV1, PresetDeepSeekAnthropicV1, PresetAnthropicV1, PresetGLMCodingAnthropicV1,
	} {
		if !setsEqual(toSet(connectorPresetRecordIndex[preset].OperationPathTemplates), anthropicTemplateSet) {
			t.Errorf("preset %q 必须精确使用 Anthropic 公共 2 条模板", preset)
		}
	}
	geminiTemplateSet := toSet(geminiNativeOperationTemplates)
	for _, preset := range []string{PresetGenericGeminiV1Beta, PresetGeminiNativeV1Beta} {
		if !setsEqual(toSet(connectorPresetRecordIndex[preset].OperationPathTemplates), geminiTemplateSet) {
			t.Errorf("preset %q 必须精确使用 Gemini native 公共 8 条模板", preset)
		}
	}
	if !setsEqual(toSet(connectorPresetRecordIndex[PresetGeminiOpenAIChatV1Beta].OperationPathTemplates), toSet([]string{"POST /chat/completions"})) {
		t.Errorf("preset %q 只应注册 chat completions 模板", PresetGeminiOpenAIChatV1Beta)
	}
	// 媒体 preset 模板精确清单（docs/functions/媒体上游协议契约与Mock上游规格.md
	// 章节逐条注明，版本前缀随 R2 归属 base_url 版本根）：minimax §8.1 视频 +
	// §8.2 TTS；ark §9.1 视频创建与轮询；openspeech §9.2 TTS + 长转写
	// submit/query（无取消端点）；qwen §11 录音识别 + §10.1 万相视频 + 两域
	// 共用 DashScope 轮询 + OpenAI 兼容模式 chat（全路径，R6）。
	mediaTemplateSets := map[string][]string{
		PresetMiniMaxMedia: {
			"POST /video_generation",
			"GET /query/video_generation",
			"POST /t2a_v2",
		},
		PresetVolcengineArk: {
			"POST /contents/generations/tasks",
			"GET /contents/generations/tasks/{id}",
		},
		PresetVolcengineOpenSpeech: {
			"POST /tts/create",
			"POST /auc/bigmodel/submit",
			"POST /auc/bigmodel/query",
		},
		PresetQwenDashScope: {
			"POST /compatible-mode/v1/chat/completions",
			"POST /api/v1/services/audio/asr/transcription",
			"POST /api/v1/services/aigc/video-generation/video-synthesis",
			"GET /api/v1/tasks/{task_id}",
		},
	}
	for _, preset := range []string{PresetMiniMaxMedia, PresetVolcengineArk, PresetVolcengineOpenSpeech, PresetQwenDashScope} {
		want := mediaTemplateSets[preset]
		got := connectorPresetRecordIndex[preset].OperationPathTemplates
		if !setsEqual(toSet(got), toSet(want)) || len(got) != len(want) {
			t.Errorf("媒体 preset %q 模板 = %v, want 契约精确清单 %v", preset, got, want)
		}
	}
}

// TestBasePathValidationTemplates 断言凭据域路径校验数据源（R8）：三个公共
// 协议面已去版本前缀模板的并集副本，共 15+2+8=25 条，不包含 qwen 媒体模板。
func TestBasePathValidationTemplates(t *testing.T) {
	union := BasePathValidationTemplates()
	if len(union) != 25 {
		t.Fatalf("BasePathValidationTemplates() = %d 条, want 25", len(union))
	}
	want := toSet(append(append(append([]string{}, openAIOperationTemplates...), anthropicOperationTemplates...), geminiNativeOperationTemplates...))
	if !setsEqual(toSet(union), want) {
		t.Fatalf("BasePathValidationTemplates() 与三协议面并集不一致")
	}
	if !stringSliceContains(union, "POST /chat/completions") || !stringSliceContains(union, "POST /messages") || !stringSliceContains(union, "POST /models/{model}:generateContent") {
		t.Fatalf("并集应包含三个协议面去版本前缀后的代表模板")
	}
	// 返回副本：修改返回值不得影响 registry 内部数据。
	union[0] = "MUTATED"
	if !setsEqual(toSet(BasePathValidationTemplates()), want) {
		t.Fatalf("BasePathValidationTemplates() 必须返回副本，内部数据被修改")
	}
}
