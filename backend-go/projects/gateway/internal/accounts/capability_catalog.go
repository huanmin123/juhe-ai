package accounts

import (
	"sort"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/scenario"
)

// CapabilityCatalog 是账户创建/筛选的唯一只读目录。
// 前端不再维护本地四域矩阵：场景、供应商、档案、账户类型、允许 modes
// 全部从本接口派生。目录只读，不写库、不改变写契约。
type CapabilityCatalog struct {
	Scenarios []CapabilityScenario `json:"scenarios"`
}

// CapabilityScenario 是一类场景及其可用供应商。
type CapabilityScenario struct {
	ScenarioType  string               `json:"scenarioType"`
	Label         string               `json:"label"`
	DispatchClass string               `json:"dispatchClass"`
	Providers     []CapabilityProvider `json:"providers"`
}

// CapabilityProvider 是一个供应商在某场景下的可用档案。
type CapabilityProvider struct {
	ProviderCode string              `json:"providerCode"`
	Profiles     []CapabilityProfile `json:"profiles"`
}

// CapabilityProfile 是一个连接档案在某场景下的能力描述。
type CapabilityProfile struct {
	ProfileID            string   `json:"profileId"`
	ConnectionPreset     string   `json:"connectionPreset"`
	AccountTypes         []string `json:"accountTypes"`
	AllowedEndpointModes []string `json:"allowedEndpointModes"`
	ProbeModes           []string `json:"probeModes,omitempty"`
	TestPolicy           string   `json:"testPolicy"`
	StatusPolicy         string   `json:"statusPolicy"`
}

type catalogProfileKey struct {
	providerCode string
	profileID    string
}

type catalogModeKey struct {
	providerCode string
	accountType  string
	preset       string
	domain       string
}

// BuildCapabilityCatalog 从连接器注册表与场景注册表派生能力目录。
// 不查询数据库：供应商启停由前端已有的 provider definitions 接口负责；
// 本目录只回答“某场景下哪些 vendor/profile/accountType 可建、可选哪些 mode”。
func BuildCapabilityCatalog() CapabilityCatalog {
	modeIndex := map[catalogModeKey][]string{}
	for _, combo := range contracts.AllowedEndpointModeCombos() {
		key := catalogModeKey{
			providerCode: combo.Vendor,
			accountType:  combo.AccountType,
			preset:       combo.Preset,
			domain:       combo.Domain,
		}
		modeIndex[key] = append(modeIndex[key], combo.Mode)
	}

	scenarioProviders := map[string]map[string]map[string]*CapabilityProfile{}
	scenarioOrder := map[string]int{}
	for index, profile := range scenario.All() {
		scenarioOrder[string(profile.Type)] = index
	}
	for _, combo := range contracts.AllowedConnectorCombos() {
		if !scenario.Valid(combo.Domain) {
			continue
		}
		profileID, ok := exactkeyprobe.ConnectionPresetProfileID(combo.Preset)
		if !ok {
			// 注册表 preset 必须有稳定档案 ID；缺失时跳过而不是编造。
			continue
		}
		if scenarioProviders[combo.Domain] == nil {
			scenarioProviders[combo.Domain] = map[string]map[string]*CapabilityProfile{}
		}
		if scenarioProviders[combo.Domain][combo.Vendor] == nil {
			scenarioProviders[combo.Domain][combo.Vendor] = map[string]*CapabilityProfile{}
		}
		profileKey := combo.Vendor + "\x00" + profileID
		profile := scenarioProviders[combo.Domain][combo.Vendor][profileKey]
		if profile == nil {
			profile = &CapabilityProfile{
				ProfileID:        profileID,
				ConnectionPreset: combo.Preset,
				TestPolicy:       string(scenarioTestPolicy(combo.Domain)),
				StatusPolicy:     string(scenarioStatusPolicy(combo.Domain)),
			}
			scenarioProviders[combo.Domain][combo.Vendor][profileKey] = profile
		}
		profile.AccountTypes = appendUnique(profile.AccountTypes, combo.AccountType)
		modes := modeIndex[catalogModeKey{
			providerCode: combo.Vendor,
			accountType:  combo.AccountType,
			preset:       combo.Preset,
			domain:       combo.Domain,
		}]
		for _, mode := range modes {
			profile.AllowedEndpointModes = appendUnique(profile.AllowedEndpointModes, mode)
		}
	}

	catalog := CapabilityCatalog{Scenarios: []CapabilityScenario{}}
	for _, scenarioProfile := range scenario.All() {
		providers := scenarioProviders[string(scenarioProfile.Type)]
		if len(providers) == 0 {
			continue
		}
		entry := CapabilityScenario{
			ScenarioType:  string(scenarioProfile.Type),
			Label:         scenarioProfile.Label,
			DispatchClass: string(scenarioProfile.DispatchClass),
			Providers:     []CapabilityProvider{},
		}
		providerCodes := make([]string, 0, len(providers))
		for providerCode := range providers {
			providerCodes = append(providerCodes, providerCode)
		}
		sort.Strings(providerCodes)
		for _, providerCode := range providerCodes {
			providerEntry := CapabilityProvider{ProviderCode: providerCode, Profiles: []CapabilityProfile{}}
			profileKeys := make([]string, 0, len(providers[providerCode]))
			for profileKey := range providers[providerCode] {
				profileKeys = append(profileKeys, profileKey)
			}
			sort.Strings(profileKeys)
			for _, profileKey := range profileKeys {
				profile := providers[providerCode][profileKey]
				sort.Strings(profile.AccountTypes)
				sort.Slice(profile.AllowedEndpointModes, func(i, j int) bool {
					return endpointModeOrder(profile.AllowedEndpointModes[i]) < endpointModeOrder(profile.AllowedEndpointModes[j])
				})
				profile.ProbeModes = probeModesFor(scenarioProfile.Type, profile.AllowedEndpointModes)
				providerEntry.Profiles = append(providerEntry.Profiles, *profile)
			}
			entry.Providers = append(entry.Providers, providerEntry)
		}
		catalog.Scenarios = append(catalog.Scenarios, entry)
	}
	sort.SliceStable(catalog.Scenarios, func(i, j int) bool {
		return scenarioOrder[catalog.Scenarios[i].ScenarioType] < scenarioOrder[catalog.Scenarios[j].ScenarioType]
	})
	return catalog
}

func scenarioTestPolicy(domain string) scenario.TestPolicy {
	if profile, ok := scenario.Get(domain); ok {
		return profile.TestPolicy
	}
	return scenario.TestConnectivity
}

func scenarioStatusPolicy(domain string) scenario.StatusPolicy {
	if profile, ok := scenario.Get(domain); ok {
		return profile.StatusPolicy
	}
	return scenario.StatusActiveOnly
}

// probeModesFor 返回连通性测试/后台探测的代表 mode。它不参与真实请求的
// 能力声明，只用于测试入口和 J1 探测模式选择；B0 会按 vendor 细化。
func probeModesFor(domain scenario.Type, allowed []string) []string {
	contains := func(mode string) bool {
		for _, candidate := range allowed {
			if candidate == mode {
				return true
			}
		}
		return false
	}
	switch domain {
	case scenario.TypeChat:
		if contains("chat_sse") {
			return []string{"chat_sse"}
		}
		return nil
	case scenario.TypeImage:
		if contains("images_json") {
			return []string{"images_json"}
		}
	case scenario.TypeVideo:
		if contains("video_get") {
			return []string{"video_get"}
		}
		if contains("video_create") {
			return []string{"video_create"}
		}
	case scenario.TypeAudio:
		if contains("audio_speech") {
			return []string{"audio_speech"}
		}
		if contains("audio_job_get") {
			return []string{"audio_job_get"}
		}
	case scenario.TypeEmbedding:
		if contains("embed_content") {
			return []string{"embed_content"}
		}
	case scenario.TypeRerank:
		if contains("rerank_json") {
			return []string{"rerank_json"}
		}
	case scenario.TypeModeration:
		if contains("moderation_json") {
			return []string{"moderation_json"}
		}
	}
	return nil
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// endpointModeOrder 固定目录返回顺序，避免 map 遍历造成前端选项抖动。
func endpointModeOrder(mode string) int {
	order := []string{
		"chat_json", "chat_sse", "responses_json", "responses_sse",
		"messages_json", "messages_sse", "message_token_counting",
		"generate_content_json", "generate_content_sse",
		"interactions_json", "interactions_sse", "count_tokens",
		"embed_content", "images_json", "audio_speech",
		"audio_transcription_json", "video_create", "video_get",
		"video_content", "video_cancel", "audio_job_create",
		"audio_job_get", "audio_job_content", "audio_job_cancel",
		"realtime_session", "rerank_json", "moderation_json",
	}
	for index, candidate := range order {
		if candidate == mode {
			return index
		}
	}
	return len(order)
}
