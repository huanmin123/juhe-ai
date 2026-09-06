package manualtest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
)

// DraftSnapshot 对齐 Node AccountTestDraftSnapshot 的 worker 消费字段投影
// （storage/account-test-tasks.repository.ts 的 v1 信封解密产物）。
type DraftSnapshot struct {
	ID                        string         `json:"id"`
	StateTargetAccountID      string         `json:"stateTargetAccountId,omitempty"`
	OwnerSystemAccountID      string         `json:"ownerSystemAccountId"`
	GroupID                   string         `json:"groupId"`
	GroupName                 string         `json:"groupName,omitempty"`
	ProviderCode              string         `json:"providerCode"`
	ProviderProtocolProfileID string         `json:"providerProtocolProfileId,omitempty"`
	ProtocolCode              string         `json:"protocolCode,omitempty"`
	ProtocolVersion           string         `json:"protocolVersion,omitempty"`
	Name                      string         `json:"name"`
	Type                      string         `json:"type"`
	Credentials               map[string]any `json:"credentials"`
	ClientCompatibility       string         `json:"clientCompatibility"`
	SupportedModels           []string       `json:"supportedModels,omitempty"`
	HealthCheckModel          string         `json:"healthCheckModel"`
	HealthCheckEndpointMode   string         `json:"healthCheckEndpointMode"`
}

// DecryptDraft 等价 Node accountTestDraftSnapshot：v1 信封解密 + 规范化；
// 解密失败或形状不合法返回 (nil, nil)（Node catch → undefined → 任务回退
// 保存账户测试路径）。
func DecryptDraft(secret string, envelope string) (*DraftSnapshot, error) {
	var parsed any
	if err := oauthrefresh.DecryptJSON(secret, envelope, &parsed); err != nil {
		return nil, err
	}
	return normalizeDraftSnapshot(parsed), nil
}

// isValidDraftClientCompatibility 对齐 Node accountClientCompatibility
// （account-test-tasks.repository.ts:1672-1675）：openai_standard |
// codex_responses 之外一律作废。
func isValidDraftClientCompatibility(value string) bool {
	return value == "openai_standard" || value == "codex_responses"
}

// isValidDraftHealthCheckEndpointMode 对齐 Node
// accountHealthCheckEndpointModeValue / ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES
// （account-test-tasks.repository.ts:1774-1779、
// domain/account-health-check-endpoint-mode.ts）。
func isValidDraftHealthCheckEndpointMode(value string) bool {
	switch value {
	case "images_json",
		"chat_json", "chat_sse",
		"responses_json", "responses_sse",
		"messages_json", "messages_sse",
		"generate_content_json", "generate_content_sse",
		"interactions_json", "interactions_sse":
		return true
	}
	return false
}

// normalizeDraftSnapshot 对齐 normalizeAccountTestDraftSnapshot：必填字段缺失、
// credentials 非对象，或 clientCompatibility / healthCheckEndpointMode 不在
// Node 枚举内（accountClientCompatibility /
// accountHealthCheckEndpointModeValue 归一为 undefined）返回 nil——整份草稿
// 作废，任务回退保存账户测试路径（D3：worker 侧信封枚举校验）。
func normalizeDraftSnapshot(value any) *DraftSnapshot {
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	text := func(key string) string {
		if raw, exists := record[key]; exists {
			if decoded, isText := raw.(string); isText {
				return strings.TrimSpace(decoded)
			}
		}
		return ""
	}
	draft := &DraftSnapshot{
		ID:                        text("id"),
		StateTargetAccountID:      text("stateTargetAccountId"),
		OwnerSystemAccountID:      text("ownerSystemAccountId"),
		GroupID:                   text("groupId"),
		GroupName:                 text("groupName"),
		ProviderCode:              text("providerCode"),
		ProviderProtocolProfileID: text("providerProtocolProfileId"),
		ProtocolCode:              text("protocolCode"),
		ProtocolVersion:           text("protocolVersion"),
		Name:                      text("name"),
		Type:                      text("type"),
		ClientCompatibility:       text("clientCompatibility"),
		SupportedModels:           stringListValue(record["supportedModels"]),
		HealthCheckModel:          text("healthCheckModel"),
		HealthCheckEndpointMode:   text("healthCheckEndpointMode"),
	}
	if credentials, ok := record["credentials"].(map[string]any); ok {
		draft.Credentials = credentials
	}
	if draft.ID == "" || draft.OwnerSystemAccountID == "" || draft.GroupID == "" ||
		draft.ProviderCode == "" || draft.Name == "" || draft.Type == "" ||
		draft.HealthCheckModel == "" || draft.Credentials == nil {
		return nil
	}
	if !isValidDraftClientCompatibility(draft.ClientCompatibility) ||
		!isValidDraftHealthCheckEndpointMode(draft.HealthCheckEndpointMode) {
		return nil
	}
	return draft
}

func stringListValue(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	models := make([]string, 0, len(list))
	for _, item := range list {
		text, ok := item.(string)
		if !ok {
			continue
		}
		normalized := strings.TrimSpace(text)
		if normalized == "" {
			continue
		}
		models = append(models, normalized)
	}
	if len(models) == 0 {
		return nil
	}
	return models
}

// isGatewaySupportedDraftProtocol 对齐 isGatewaySupportedProtocolProfile 的
// 协议/版本谓词（openai+v1 | anthropic+v1 | gemini+v1beta）。
func isGatewaySupportedDraftProtocol(protocolCode, protocolVersion string) bool {
	switch normalizeToken(protocolCode) {
	case "openai":
		return normalizeToken(protocolVersion) == "v1"
	case "anthropic":
		return normalizeToken(protocolVersion) == "v1"
	case "gemini":
		return normalizeToken(protocolVersion) == "v1beta"
	default:
		return false
	}
}

func normalizeToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// apiKeyEntries 等价 Node accountApiKeyEntries（api_keys 池优先，回落单
// api_key；去空白去重，index 保留原始下标）。指纹 = HMAC-SHA256(secret, key)。
func apiKeyEntries(secret string, credentials map[string]any) []accountprobe.KeyEntry {
	var rawKeys []any
	if list, ok := credentials["api_keys"].([]any); ok && len(list) > 0 {
		rawKeys = list
	} else {
		rawKeys = []any{credentials["api_key"]}
	}
	entries := make([]accountprobe.KeyEntry, 0, len(rawKeys))
	seen := map[string]bool{}
	for index, value := range rawKeys {
		text, ok := value.(string)
		if !ok {
			continue
		}
		key := strings.TrimSpace(text)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		entries = append(entries, accountprobe.KeyEntry{
			Key:         key,
			Fingerprint: fingerprintAPIKey(secret, key),
			Index:       index,
		})
	}
	return entries
}

func fingerprintAPIKey(secret, key string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}

// selectedAPIKeyForDraft 等价 openAIDraftAccountSecret 的默认凭据分支：
// oauth 取 access_token；google_oauth 取 access_token 回落 refresh_token；
// api_key 取池首把 Key。
func selectedAPIKeyForDraft(draft *DraftSnapshot, entries []accountprobe.KeyEntry) string {
	switch draft.Type {
	case "oauth":
		return credentialText(draft.Credentials, "access_token")
	case "google_oauth":
		if token := credentialText(draft.Credentials, "access_token"); token != "" {
			return token
		}
		return credentialText(draft.Credentials, "refresh_token")
	default:
		if len(entries) > 0 {
			return entries[0].Key
		}
		return ""
	}
}

func credentialText(credentials map[string]any, key string) string {
	if credentials == nil {
		return ""
	}
	if value, ok := credentials[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

// draftBaseURL 等价 openAIDraftAccountSecret 的 base_url 分支
// （stringCredential(credentials.base_url) || 'https://api.openai.com/v1'）。
func draftBaseURL(credentials map[string]any) string {
	if base := credentialText(credentials, "base_url"); base != "" {
		return base
	}
	return "https://api.openai.com/v1"
}

// draftView 由解密草稿组装探针视图（manualtest executor 消费）。
func draftView(secret string, draft *DraftSnapshot, modelOverride, endpointModeOverride string) *accountprobe.View {
	entries := apiKeyEntries(secret, draft.Credentials)
	healthModel := draft.HealthCheckModel
	if strings.TrimSpace(modelOverride) != "" {
		healthModel = strings.TrimSpace(modelOverride)
	}
	healthMode := draft.HealthCheckEndpointMode
	if strings.TrimSpace(endpointModeOverride) != "" {
		healthMode = strings.TrimSpace(endpointModeOverride)
	}
	return &accountprobe.View{
		AccountID:                 draft.ID,
		AccountName:               draft.Name,
		Type:                      draft.Type,
		Status:                    "active",
		ProviderCode:              draft.ProviderCode,
		ProtocolCode:              draft.ProtocolCode,
		ProtocolVersion:           draft.ProtocolVersion,
		ProviderProtocolProfileID: draft.ProviderProtocolProfileID,
		HealthCheckModel:          healthModel,
		HealthCheckEndpointMode:   healthMode,
		SupportedModels:           draft.SupportedModels,
		BaseURL:                   draftBaseURL(draft.Credentials),
		Credentials:               draft.Credentials,
		APIKeyEntries:             entries,
		SelectedAPIKey:            selectedAPIKeyForDraft(draft, entries),
		NormalizeEndpointModes:    normalizeDraftEndpointModes(draft),
	}
}

// normalizeDraftEndpointModes 等价 supportedEndpointModesFromCredentials 的
// worker 侧窄投影（credentials.supported_endpoint_modes 有效值过滤）。
// gateway 写入草稿时已按协议归一化；缺失时由 accountprobe 的
// manualTestEndpointModes 以空集合回落请求形态解析（无可用形态报配置错误）。
func normalizeDraftEndpointModes(draft *DraftSnapshot) map[accountprobe.EndpointMode]bool {
	modes := map[accountprobe.EndpointMode]bool{}
	list, ok := draft.Credentials["supported_endpoint_modes"].([]any)
	if !ok {
		return modes
	}
	for _, item := range list {
		text, isText := item.(string)
		if !isText {
			continue
		}
		switch accountprobe.EndpointMode(text) {
		case accountprobe.ModeChatJSON, accountprobe.ModeChatSSE,
			accountprobe.ModeResponsesJSON, accountprobe.ModeResponsesSSE,
			accountprobe.ModeMessagesJSON, accountprobe.ModeMessagesSSE,
			accountprobe.ModeGenerateContentJSON, accountprobe.ModeGenerateContentSSE,
			accountprobe.ModeInteractionsJSON, accountprobe.ModeInteractionsSSE,
			accountprobe.ModeImagesJSON:
			modes[accountprobe.EndpointMode(text)] = true
		}
	}
	return modes
}

// marshalJSONEnvelope 序列化结果信封（Node JSON.stringify 等价）。
func marshalJSONEnvelope(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
