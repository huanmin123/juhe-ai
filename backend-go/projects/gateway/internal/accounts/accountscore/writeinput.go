package accountscore

import "strings"

// Write-path input contracts shared between the facade write surface and the
// accountstransfer subdomain (REFACTOR-0005 阶段 C 下沉). These are pure DTO
// definitions — the normalization/validation logic that consumes them stays in
// the facade (设计 v2：写入侧归一化留门面) and reaches the transfer subdomain
// through the Deps function ports.

// CreationStatus mirrors accountCreationStatusInput: the user-facing creation
// choice plus the derived guarded write flags (Node overrides the body fields
// with these in accounts.routes.ts).
type CreationStatus struct {
	Status                 string
	SkipInitialHealthCheck bool
	Schedulable            bool
}

// AccountCreationStatusInput mirrors accountCreationStatusInput.
func AccountCreationStatusInput(value any) CreationStatus {
	status := "pending_test"
	if text, ok := value.(string); ok && (text == "active" || text == "disabled") {
		status = text
	}
	return CreationStatus{
		Status:                 status,
		SkipInitialHealthCheck: status == "active",
		Schedulable:            status == "active",
	}
}

// ConnectionInput 是创建/PATCH/导入共享的连接行载荷（供应商与上游协议面重构
// 设计 §8）：connectionPresetCode 是账户内自然键（连接 ID 永不由客户端提交，
// 服务端确定性生成），baseUrl 走 §2.1 registry 数据驱动 validator，
// endpointModes 必须非空且 ⊆ AllowedModes(domain, preset, account_type,
// oauth_type)，enabled 必须显式提交。ProtocolSurfaceCode 是 §8 的可选回显字段
// （纯媒体连接为空）；非空时必须等于 registry preset surface。
type ConnectionInput struct {
	ConnectionPresetCode string
	ProtocolSurfaceCode  string
	BaseURL              string
	EndpointModes        []string
	Enabled              bool
}

// ParseConnectionInput 解析 §8 connections[] 元素（camelCase 键集：必填
// connectionPresetCode/baseUrl/endpointModes/enabled + 可选 protocolSurfaceCode
// 回显；未知键拒绝）。管理面路由、导入计划与公开面共用本解析器，不造第二份。
func ParseConnectionInput(object map[string]any) (ConnectionInput, bool) {
	text := func(value any) string {
		if text, ok := value.(string); ok {
			return strings.TrimSpace(text)
		}
		return ""
	}
	connection := ConnectionInput{
		ConnectionPresetCode: text(object["connectionPresetCode"]),
		ProtocolSurfaceCode:  text(object["protocolSurfaceCode"]),
		BaseURL:              text(object["baseUrl"]),
	}
	if connection.ConnectionPresetCode == "" || connection.BaseURL == "" {
		return ConnectionInput{}, false
	}
	enabled, ok := object["enabled"].(bool)
	if !ok {
		return ConnectionInput{}, false
	}
	connection.Enabled = enabled
	rawModes, ok := object["endpointModes"].([]any)
	if !ok || len(rawModes) == 0 {
		return ConnectionInput{}, false
	}
	for _, mode := range rawModes {
		modeText := text(mode)
		if modeText == "" {
			return ConnectionInput{}, false
		}
		connection.EndpointModes = append(connection.EndpointModes, modeText)
	}
	for key := range object {
		switch key {
		case "connectionPresetCode", "protocolSurfaceCode", "baseUrl", "endpointModes", "enabled":
		default:
			return ConnectionInput{}, false
		}
	}
	return connection, true
}

// CreateInput is the validated create payload (accountCreateSchema subset the
// store consumes); nil pointers mean the field was absent.
type CreateInput struct {
	ProviderCode              string
	ProviderProtocolProfileID string
	Name                      string
	AccountType               string
	Credentials               Credentials
	SupportedModels           []string
	HealthCheckModel          *string
	HealthCheckEndpointMode   *string
	// HealthCheckConnectionPresetCode 是 §8 健康检查三件套的连接维度入参
	//（camelCase healthCheckConnectionPresetCode）：必须解析到最终连接集合中
	// 一行 enabled 连接（服务端同事务解析为 health_check_connection_id）。
	// 克隆上下文由源账户 health_check_connection_id 反解 preset 注入。
	HealthCheckConnectionPresetCode *string
	// Connections 是 §8 创建载荷的连接集合（camelCase connections）：非空必填
	// （克隆载荷由源账户连接行综合）；写入序 accounts → account_connections →
	// account_endpoint_modes → … → UPDATE health 三字段（§2.1 复合外键事务
	// 闭环）。
	Connections []ConnectionInput
	ModelMappings             []ModelMapping
	Tags                      []string
	Status                    CreationStatus
	// PreservePendingTest keeps an import's explicit pending_test state. Normal
	// media creation uses the domain default active; import is the deliberate
	// exception because imported credentials have not been checked locally.
	PreservePendingTest  bool
	ConcurrencyLimit     *int
	Priority             *int
	SuperPriorityEnabled *bool
	FallbackEnabled      *bool
	ProxyProfileID       *string
	GroupID              *string
	AccountExpiresAt     *string
	AvailabilitySchedule any
	Notes                *string
	BalanceQueryEnabled  bool
	// BalanceQueryConfigCanonical carries the normalized config JSON (the
	// create body parser already ran normalizeAccountBalanceConfig, exactly
	// like the Node route); nil means the request did not include a config.
	BalanceQueryConfigCanonical *string
	// TemporaryUnavailableContinuousProbeEnabled mirrors the
	// normalizeOptionalBooleanInput tri-state: nil = not provided (defaults
	// to enabled), false persists the explicit opt-out.
	TemporaryUnavailableContinuousProbeEnabled *bool
	// ExpeditedRecoveryEnabled mirrors the expedited-recovery tri-state:
	// nil = not provided (persists 0), true persists 1 after the owner quota
	// assertion (AI账户特供快速恢复通道设计 §8.1/§9). Clone contexts and the
	// import planner never set it, so cloned/imported rows stay unmarked.
	ExpeditedRecoveryEnabled *bool
	// CapabilityDomain 是请求解析后的能力域（M2 D6 保存规则）：camelCase
	// capabilityDomain，缺省/空串由写路径按 chat 处理；非法值由入口 400
	// 「能力域无效」。导入执行器按包内推导域写入；授权实例等未赋值调用方
	// 落 chat（与列 NOT NULL DEFAULT 'chat' 同语义）。
	CapabilityDomain string
	// CloneSourceID 是 M2 克隆契约的可选源账户 ID（camelCase cloneSourceId）：
	// 非空时由路由层经 FindCloneContext 加载源账户并断言目标域 = 源域；
	// 不携带 = 普通新建。
	CloneSourceID *string
}

// EndpointModeDefaultContext mirrors the exported credential write context
// (credentials_normalize.go): the resolved provider/protocol identity the
// write-side normalization resolves endpoint modes against. A nil context at
// the facade entry means Node's `{ accountType }` fallback.
type EndpointModeDefaultContext struct {
	ProviderCode              string
	AccountType               string
	ClientCompatibility       string
	ProtocolCode              string
	ProtocolVersion           string
	ProviderProtocolProfileID string
}

// Tag validation limits（原门面 store.go maxTagNameLength/maxTagsPerAccount，
// 导入域标签解析器共用；门面保留同名常量别名）.
const (
	MaxTagNameLength  = 40
	MaxTagsPerAccount = 24
)

// AccountHealthCheckEndpointModes mirrors the allowed health-check endpoint
// mode set（原门面 write.go accountHealthCheckEndpointModes 常量表，导入域字段
// 解析器共用；门面保留同名别名）。M2 媒体生命周期定案：白名单增补
// video_create（DB CHECK 已允许），仅写侧落值词表，不改变任何检查执行路径。
//
// v8-A 对话域纯粹化白名单核对结论（计划 §对话域纯粹化，2026-10-13）：
//   - audio_transcription_json 已在本白名单（M1 同步音频批），v8-A 归音频域
//     lane 集后语义不变——健康检查默认仍落 audio_speech，纯转写账户显式选
//     audio_transcription_json（既有白名单语义，无代码变化）。
//   - realtime_session 有意不加入本白名单：无探针执行器、无人工测试形态
//     （周期候选白名单 J1 与人工测试 options 均不含该值），与 video_create
//     同一处理口径——DB CHECK 词表含该值（maintenance pg_schema_business_
//     tables.go / sqlite_schema_business.go 同形）、写入口不开放落值；v8-A
//     将其归入音频域 lane 集只影响推导与允许集，不开放健康检查形态。
var AccountHealthCheckEndpointModes = map[string]bool{
	"images_json": true, "chat_json": true, "chat_sse": true,
	"responses_json": true, "responses_sse": true,
	"messages_json": true, "messages_sse": true,
	"generate_content_json": true, "generate_content_sse": true,
	"interactions_json": true, "interactions_sse": true,
	// M1 同步音频探针形态（与 accounts.health_check_endpoint_mode CHECK
	// 同步，maintenance pg_schema_business_tables.go）。
	"audio_speech": true, "audio_transcription_json": true,
	// M2 视频域默认落值（仅写侧白名单；J1 候选白名单不含该值，视频域账户
	// 天然豁免周期检查）。
	"video_create": true,
}
