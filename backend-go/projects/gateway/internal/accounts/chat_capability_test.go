package accounts

// 对话批（火山方舟/通义百炼对话承接）纯函数直测：档案 chat 承接判定、
// volcengine/qwen 写侧端点模式词表收 chat 对、协议矩阵 chat 映射放行。

import (
	"strings"
	"testing"
)

func chatCapabilityProfile(providerCode, profileID string) protocolPredicateInput {
	return protocolPredicateInput{
		providerCode:              providerCode,
		protocolCode:              openAIProtocolCode,
		protocolVersion:           openAIProtocolVersion,
		providerProtocolProfileID: profileID,
	}
}

func TestChatCapabilityVolcengineQwenProfilesAdmitChatProtocolModels(t *testing.T) {
	cases := []struct {
		name     string
		profile  protocolPredicateInput
		protocol string
		want     bool
	}{
		{name: "volcengine chat model", profile: chatCapabilityProfile("volcengine", volcengineOpenAIV1ProfileID), protocol: "chat_completions", want: true},
		{name: "volcengine video model", profile: chatCapabilityProfile("volcengine", volcengineOpenAIV1ProfileID), protocol: "video", want: true},
		{name: "volcengine audio_speech model", profile: chatCapabilityProfile("volcengine", volcengineOpenAIV1ProfileID), protocol: "audio_speech", want: true},
		{name: "volcengine messages model", profile: chatCapabilityProfile("volcengine", volcengineOpenAIV1ProfileID), protocol: "messages", want: false},
		{name: "qwen chat model", profile: chatCapabilityProfile("qwen", qwenOpenAIV1ProfileID), protocol: "chat_completions", want: true},
		{name: "qwen video model", profile: chatCapabilityProfile("qwen", qwenOpenAIV1ProfileID), protocol: "video", want: true},
		{name: "qwen audio_transcription model", profile: chatCapabilityProfile("qwen", qwenOpenAIV1ProfileID), protocol: "audio_transcription", want: true},
		{name: "qwen audio_speech model rejected", profile: chatCapabilityProfile("qwen", qwenOpenAIV1ProfileID), protocol: "audio_speech", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := providerModelSupportsProtocolProfile([]string{tc.protocol}, tc.profile); got != tc.want {
				t.Fatalf("providerModelSupportsProtocolProfile(%q) = %v, want %v", tc.protocol, got, tc.want)
			}
		})
	}
}

func TestChatCapabilityVolcengineQwenEndpointModeNormalization(t *testing.T) {
	newContext := func(providerCode, profileID string) endpointModeDefaultContext {
		return endpointModeDefaultContext{
			providerCode:              providerCode,
			accountType:               "api_key",
			protocolCode:              openAIProtocolCode,
			protocolVersion:           openAIProtocolVersion,
			providerProtocolProfileID: profileID,
		}
	}
	volcengineContext := newContext(volcengineProviderCode, volcengineOpenAIV1ProfileID)
	qwenContext := newContext(qwenProviderCode, qwenOpenAIV1ProfileID)

	// chat 对显式勾选通过（含与媒体模式混选）。
	for name, tc := range map[string]struct {
		context endpointModeDefaultContext
		value   []any
	}{
		"volcengine chat pair":            {context: volcengineContext, value: []any{"chat_json", "chat_sse"}},
		"volcengine chat with video":      {context: volcengineContext, value: []any{"chat_json", "video_create", "video_get"}},
		"volcengine chat with speech":     {context: volcengineContext, value: []any{"chat_sse", "audio_speech"}},
		"qwen chat pair":                  {context: qwenContext, value: []any{"chat_json", "chat_sse"}},
		"qwen chat with audio job":        {context: qwenContext, value: []any{"chat_json", "audio_job_create", "audio_job_get"}},
	} {
		t.Run(name, func(t *testing.T) {
			modes, err := normalizeEndpointModesForWrite(opt(tc.value), tc.context)
			if err != nil {
				t.Fatalf("chat 模式勾选应通过：%v", err)
			}
			if !containsString(modes, "chat_json") && !containsString(modes, "chat_sse") {
				t.Fatalf("结果应保留 chat 模式：%v", modes)
			}
		})
	}

	// 缺省键默认集不变：video_get（零费用只读档），chat 不进默认集。
	for name, context := range map[string]endpointModeDefaultContext{
		"volcengine default": volcengineContext,
		"qwen default":       qwenContext,
	} {
		t.Run(name, func(t *testing.T) {
			modes, err := normalizeEndpointModesForWrite(optionalValue{}, context)
			if err != nil {
				t.Fatalf("缺省键应落默认集：%v", err)
			}
			if len(modes) != 1 || modes[0] != "video_get" {
				t.Fatalf("默认集 = %v, want [video_get]", modes)
			}
		})
	}

	// responses 与未收录模式仍拒绝，文案含对话端点。
	for name, tc := range map[string]struct {
		context endpointModeDefaultContext
		value   []any
		want    string
	}{
		"volcengine responses rejected": {context: volcengineContext, value: []any{"responses_json"}, want: "对话补全端点"},
		// messages_json 不在 openai 族词表——值域校验层先拒（先于 volcengine 收敛层）。
		"volcengine messages rejected": {context: volcengineContext, value: []any{"chat_json", "messages_json"}, want: "包含不支持的能力"},
		"qwen responses rejected":      {context: qwenContext, value: []any{"responses_sse"}, want: "对话补全端点"},
		"qwen audio_speech rejected":   {context: qwenContext, value: []any{"chat_json", "audio_speech"}, want: "对话补全端点"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := normalizeEndpointModesForWrite(opt(tc.value), tc.context)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("应拒绝并含 %q 文案：%v", tc.want, err)
			}
		})
	}
}

func TestChatCapabilityMatrixAllowsChatMappingsForVolcengineQwen(t *testing.T) {
	cases := []struct {
		name    string
		profile protocolPredicateInput
	}{
		{name: "volcengine", profile: chatCapabilityProfile(volcengineProviderCode, volcengineOpenAIV1ProfileID)},
		{name: "qwen", profile: chatCapabilityProfile(qwenProviderCode, qwenOpenAIV1ProfileID)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// chat→chat 映射：openai 档案判定 + chat 白名单 + chat_json 上游能力。
			if err := assertAccountModelMappingProtocolAllowed(ModelMapping{
				SourceEndpointFamily: mappingFamilyChatCompletions,
				UpstreamEndpointFamily: mappingFamilyChatCompletions,
			}, tc.profile, []string{"chat_json"}); err != nil {
				t.Fatalf("chat→chat 映射应通过：%v", err)
			}
			// responses 上游在词表层已拒（responses 模式不可勾选，见上）；矩阵
			// 层 requiresNativeResponses 臂对缺 responses 能力的映射拒绝
			//（responses→responses 规则带 requiresNativeResponses 钉值）。
			if err := assertAccountModelMappingProtocolAllowed(ModelMapping{
				SourceEndpointFamily:   mappingFamilyResponses,
				UpstreamEndpointFamily: mappingFamilyResponses,
			}, tc.profile, nil); err == nil ||
				!strings.Contains(err.Error(), "原生上游") {
				t.Fatalf("responses→responses 缺上游能力应拒绝：%v", err)
			}
		})
	}
	// 媒体映射既有语义不回归：volcengine video_generation 同族映射仍通过。
	volcengineProfile := chatCapabilityProfile(volcengineProviderCode, volcengineOpenAIV1ProfileID)
	if err := assertAccountModelMappingProtocolAllowed(ModelMapping{
		SourceEndpointFamily:   mappingFamilyVideoGeneration,
		UpstreamEndpointFamily: mappingFamilyVideoGeneration,
	}, volcengineProfile, []string{"video_create"}); err != nil {
		t.Fatalf("video_generation 同族映射应通过：%v", err)
	}
}
