// Package scenario 是 AI 账户场景类型的唯一注册表。
//
// 一个账户只属于一个场景类型；场景描述账户归属、创建选项、列表筛选、测试
// 策略和生命周期策略，不描述请求协议。请求协议由 endpoint mode / lane 决定。
//
// 本包先注册现有 chat/video/audio/image 四类；B7（embedding/rerank/moderation）
// 与 B8（realtime）只在本文件追加 Profile，不新增菜单、不复制表单/列表分支。
package scenario

// Type 是 accounts.capability_domain 的落库值（历史列名，语义为场景类型）。
type Type string

const (
	TypeChat       Type = "chat"
	TypeVideo      Type = "video"
	TypeAudio      Type = "audio"
	TypeImage      Type = "image"
	TypeEmbedding  Type = "embedding"
	TypeRerank     Type = "rerank"
	TypeModeration Type = "moderation"
	TypeRealtime   Type = "realtime"
)

// DispatchClass 是请求调度类；由请求 lane 决定，不由场景类型决定。
type DispatchClass string

const (
	DispatchConversation DispatchClass = "conversation"
	DispatchStateless    DispatchClass = "stateless"
	DispatchRealtime     DispatchClass = "realtime"
)

// TestPolicy 是人工测试入口的形态。
type TestPolicy string

const (
	// TestModel 保留对话账户现有的“模型 + 协议”测试。
	TestModel TestPolicy = "model"
	// TestConnectivity 只测 Key + Base URL 连通性，不选模型/协议。
	TestConnectivity TestPolicy = "connectivity"
	// TestWSHandshake 是 Realtime 的 WebSocket 握手测试。
	TestWSHandshake TestPolicy = "ws_handshake"
)

// StatusPolicy 是账户生命周期策略。D6 后所有已开放生成式/媒体场景统一
// pending_test_then_probe；active_only 保留给无安全探测端点的新场景（B8
// Realtime 取证前仅人工测试，不用于已注册场景）。
type StatusPolicy string

const (
	StatusPendingTestThenProbe StatusPolicy = "pending_test_then_probe"
	StatusActiveOnly           StatusPolicy = "active_only"
)

// Profile 是一个场景类型的完整描述。字段只放“场景差异”，不放账户内核字段。
type Profile struct {
	Type          Type
	Label         string
	DispatchClass DispatchClass
	TestPolicy    TestPolicy
	StatusPolicy  StatusPolicy
	// FormFields/ListColumns 预留给 B1.2 前端按清单渲染；先保留稳定顺序。
	FormFields  []string
	ListColumns []string
}

// profiles 是已开放场景的注册表；顺序即前端筛选/创建选项顺序。
// B7/B8 在此追加 embedding/rerank/moderation/realtime，不改调用方。
var profiles = []Profile{
	{
		Type:          TypeChat,
		Label:         "对话",
		DispatchClass: DispatchConversation,
		TestPolicy:    TestModel,
		StatusPolicy:  StatusPendingTestThenProbe,
		FormFields:    []string{"apiKey", "baseUrl", "name", "group", "tags", "notes", "models", "proxy", "concurrency", "priority", "privilege", "expiry", "schedule", "balance", "errorPolicy", "responseInspection", "healthCheckModel"},
		ListColumns:   []string{"name", "scenario", "status", "priority", "schedule", "concurrency", "balance", "tags", "proxy", "group"},
	},
	{
		Type:          TypeVideo,
		Label:         "视频",
		DispatchClass: DispatchStateless,
		TestPolicy:    TestConnectivity,
		StatusPolicy:  StatusPendingTestThenProbe,
		FormFields:    []string{"apiKey", "baseUrl", "name", "group", "tags", "notes", "models", "proxy", "concurrency", "priority", "privilege", "expiry", "schedule", "balance", "errorPolicy", "probeToggle"},
		ListColumns:   []string{"name", "scenario", "status", "priority", "schedule", "concurrency", "balance", "tags", "proxy", "group"},
	},
	{
		Type:          TypeAudio,
		Label:         "音频",
		DispatchClass: DispatchStateless,
		TestPolicy:    TestConnectivity,
		StatusPolicy:  StatusPendingTestThenProbe,
		FormFields:    []string{"apiKey", "baseUrl", "name", "group", "tags", "notes", "models", "proxy", "concurrency", "priority", "privilege", "expiry", "schedule", "balance", "errorPolicy", "probeToggle"},
		ListColumns:   []string{"name", "scenario", "status", "priority", "schedule", "concurrency", "balance", "tags", "proxy", "group"},
	},
	{
		Type:          TypeImage,
		Label:         "图像",
		DispatchClass: DispatchStateless,
		TestPolicy:    TestConnectivity,
		StatusPolicy:  StatusPendingTestThenProbe,
		FormFields:    []string{"apiKey", "baseUrl", "name", "group", "tags", "notes", "models", "proxy", "concurrency", "priority", "privilege", "expiry", "schedule", "balance", "errorPolicy", "probeToggle"},
		ListColumns:   []string{"name", "scenario", "status", "priority", "schedule", "concurrency", "balance", "tags", "proxy", "group"},
	},
	{
		Type:          TypeEmbedding,
		Label:         "向量",
		DispatchClass: DispatchStateless,
		TestPolicy:    TestConnectivity,
		StatusPolicy:  StatusPendingTestThenProbe,
		FormFields:    []string{"apiKey", "baseUrl", "name", "group", "tags", "notes", "models", "proxy", "concurrency", "priority", "privilege", "expiry", "schedule", "errorPolicy", "probeToggle"},
		ListColumns:   []string{"name", "scenario", "status", "priority", "schedule", "concurrency", "tags", "proxy", "group"},
	},
	{
		Type:          TypeRerank,
		Label:         "重排",
		DispatchClass: DispatchStateless,
		TestPolicy:    TestConnectivity,
		StatusPolicy:  StatusPendingTestThenProbe,
		FormFields:    []string{"apiKey", "baseUrl", "name", "group", "tags", "notes", "models", "proxy", "concurrency", "priority", "privilege", "expiry", "schedule", "errorPolicy", "probeToggle"},
		ListColumns:   []string{"name", "scenario", "status", "priority", "schedule", "concurrency", "tags", "proxy", "group"},
	},
	{
		Type:          TypeModeration,
		Label:         "安全",
		DispatchClass: DispatchStateless,
		TestPolicy:    TestConnectivity,
		StatusPolicy:  StatusPendingTestThenProbe,
		FormFields:    []string{"apiKey", "baseUrl", "name", "group", "tags", "notes", "models", "proxy", "concurrency", "priority", "privilege", "expiry", "schedule", "errorPolicy", "probeToggle"},
		ListColumns:   []string{"name", "scenario", "status", "priority", "schedule", "concurrency", "tags", "proxy", "group"},
	},
}

// All 返回已开放场景的副本（顺序稳定）。
func All() []Profile {
	out := make([]Profile, len(profiles))
	copy(out, profiles)
	return out
}

// Get 按场景类型查找；未注册/未开放返回 false。
func Get(value string) (Profile, bool) {
	for _, profile := range profiles {
		if string(profile.Type) == value {
			return profile, true
		}
	}
	return Profile{}, false
}

// Valid 判定场景类型是否已开放。
func Valid(value string) bool {
	_, ok := Get(value)
	return ok
}

// Label 返回场景用户可见名称；未知场景原样返回。
func Label(value string) string {
	if profile, ok := Get(value); ok {
		return profile.Label
	}
	return value
}

// IsNonChat 判定是否非对话场景。cutover 清理与后台探测都按此口径。
func IsNonChat(value string) bool {
	profile, ok := Get(value)
	return ok && profile.Type != TypeChat
}
