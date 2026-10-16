// Package capabilitydomain 实现 AI 账户能力域拆分（PLAN-20261011T141004000Z）
// 的共享域模型与阶段记录面：
//   - 域枚举与「对话 modes」锚集合（前后端镜像，计划 §推导规则）；
//   - ruleVersion=1 推导规则（按序命中即停）；
//   - 凭据声明（解密后 credentials JSON 的 supported_endpoint_modes）边界解析：
//     缺省/空数组/null/非数组/未知模式不猜域，计入失败清单；
//   - 业务库 global_settings 专用 key account_capability_domain_migration 的
//     阶段记录（m1_running → m1_complete → m2_running → m2_finalized）：
//     只读读取（gateway/jobs 启动门禁）与条件更新写原语（maintenance 迁移
//     执行器，一次客户端命令一个阶段跃迁、单执行者）。
//
// 本包原为 maintenance internal/capabilitydomain（2026-10 M2 下沉）：gateway
// 与 jobs 启动门禁需要跨模块读取阶段记录，Go internal 规则不允许；下沉后
// 解析语义只此一份，maintenance 的迁移执行器（分批事务、失败清单、两遍法
// 实例继承）仍留在 maintenance internal/capabilitydomain。
//
// 一次客户端命令一个阶段跃迁；网关写入口的严格声明校验（声明集必须 ⊆ 域
// 允许集）与网关派发零改动属于 gateway/jobs 侧交付，不在本包范围。
package capabilitydomain

import "fmt"

// Domain 是账户能力域的落库值（accounts.capability_domain）。
type Domain string

const (
	DomainChat  Domain = "chat"
	DomainVideo Domain = "video"
	DomainAudio Domain = "audio"
	DomainImage Domain = "image"
)

// Valid 判定域值是否在四值词表内。
func (d Domain) Valid() bool {
	switch d {
	case DomainChat, DomainVideo, DomainAudio, DomainImage:
		return true
	}
	return false
}

// RuleVersion 是推导规则的固定版本（PLAN §阶段记录与重跑边界）：阶段记录
// 与本版本不一致的行视为记录损坏，拒绝执行。v8-A 对话域纯粹化只移动规则
// ①-⑤ 的命中集成员、不改版本号：存量域已物化落库、不重推导（契约明文），
// 语义差异仅影响导入/批量新建户，由计划 §对话域纯粹化记录该裁量。
const RuleVersion = 1

// chatModes 是「对话 modes」锚集合（v8-A 对话域纯粹化后 13 值，计划
// §推导规则 + §对话域纯粹化；前端 accountCapabilityDomains.ts 与网关词表
// 镜像，本包不 import 两侧实现，以计划文本为唯一事实源）。v8-A 把
// audio_transcription_json 与 realtime_session 移出对话域（归音频域）；
// images_json 本就不在锚集（v8-A 后对话域不再追加，图像 lane 一律归图像域）。
var chatModes = []string{
	"chat_json",
	"chat_sse",
	"responses_json",
	"responses_sse",
	"messages_json",
	"messages_sse",
	"generate_content_json",
	"generate_content_sse",
	"interactions_json",
	"interactions_sse",
	"count_tokens",
	"embed_content",
	"message_token_counting",
}

// mediaModes 是媒体侧全部 lane 值：图像 lane、语音合成、视频任务四值、
// 长音频任务四值与 v8-A 自对话域移入的短转写/实时会话。KnownModes =
// chatModes ∪ mediaModes，与网关四档案族词表并集闭合
// （chainOpenAIEndpointModeValues / chainAnthropicEndpointModes /
// chainGeminiDefaultModes / chainHybridEndpointModes 的并集恰为这 25 值；
// v8-A 只在对话/媒体两侧间移动成员，并集不变）。
var mediaModes = []string{
	"images_json",
	"audio_speech",
	"video_create",
	"video_get",
	"video_content",
	"video_cancel",
	"audio_job_create",
	"audio_job_get",
	"audio_job_content",
	"audio_job_cancel",
	"audio_transcription_json",
	"realtime_session",
}

// videoModes / audioModes / imageModes 是推导规则 ②③④ 的命中集。v8-A 后
// audioModes 含 audio_transcription_json 与 realtime_session（只声明短转写
// 或实时会话的包归音频域）。
var (
	videoModes = []string{"video_create", "video_get", "video_content", "video_cancel"}
	audioModes = []string{
		"audio_speech",
		"audio_job_create",
		"audio_job_get",
		"audio_job_content",
		"audio_job_cancel",
		"audio_transcription_json",
		"realtime_session",
	}
	imageModes = []string{"images_json"}
)

// knownModeSet 是全部已知 endpoint mode 值；声明中出现集合外的字符串即为
// 未知模式，纳入失败清单（不能用通配符把未知 video_* 自动视为合法能力）。
var knownModeSet = func() map[string]bool {
	set := make(map[string]bool, len(chatModes)+len(mediaModes))
	for _, mode := range chatModes {
		set[mode] = true
	}
	for _, mode := range mediaModes {
		set[mode] = true
	}
	return set
}()

// ChatModes 返回对话 modes 锚集合的副本（报告与测试消费）。
func ChatModes() []string {
	return append([]string{}, chatModes...)
}

// KnownModes 返回全部已知 endpoint mode 值的副本（对话 13 + 媒体 12，
// 并集恒为 25 值）。
func KnownModes() []string {
	return append(append([]string{}, chatModes...), mediaModes...)
}

// DeriveDomain 按 ruleVersion=1 对已验证声明集推导域（按序命中即停）：
//  1. 含对话 modes → chat（规则 ②③④ 均要求不含对话 modes）；
//  2. 不含对话 modes 且含任一 video_create/get/content/cancel → video；
//  3. 不含对话 modes 且含 audio_speech、audio_transcription_json、
//     realtime_session 或任一 audio_job_* → audio（v8-A：短转写与实时会话
//     自对话域移入音频域命中集）；
//  4. 不含对话 modes 且含 images_json → image；
//  5. 其余 → chat（对话域零命中；声明集中落域 lane 之外的值由迁移命令裁剪
//     至域内）。
//
// 输入必须已通过 ParseEndpointModes 的 ModesOK 校验（元素全部在词表内）；
// 授权实例继承（规则 ①）在迁移器层按源账户域落列，不进本函数。
func DeriveDomain(modes []string) Domain {
	if !containsAny(modes, chatModes) {
		if containsAny(modes, videoModes) {
			return DomainVideo
		}
		if containsAny(modes, audioModes) {
			return DomainAudio
		}
		if containsAny(modes, imageModes) {
			return DomainImage
		}
	}
	return DomainChat
}

// LaneModesForDomain 返回域 lane 集的副本（chat=对话 13 值锚集、video=视频
// 四值、audio=语音合成+长音频四值+短转写+实时会话、image=images_json）；
// 未知域返回 nil。迁移命令用它把声明集裁剪至落域 lane 内，网关侧允许集另经
// 供应商矩阵与写侧词表求交（在 accountscore，本包不复制那份语义）。
func LaneModesForDomain(d Domain) []string {
	switch d {
	case DomainChat:
		return ChatModes()
	case DomainVideo:
		return append([]string{}, videoModes...)
	case DomainAudio:
		return append([]string{}, audioModes...)
	case DomainImage:
		return append([]string{}, imageModes...)
	}
	return nil
}

// ModesStatus 是 supported_endpoint_modes 声明的解析结果分类。缺省、空数组、
// null、非数组与含未知值的声明都不能证明与网关运行时口径一致，不猜域：
// 除 ModesOK 外全部计入失败清单（计划 v6 授权边界）。
type ModesStatus int

const (
	// ModesMissing：credentials JSON 中没有 supported_endpoint_modes 键。
	ModesMissing ModesStatus = iota
	// ModesOK：字符串数组且每个元素都在已知词表内（可能为空数组，空数组由
	// 调用方按失败清单处理——ModesEmpty 单列以便报告区分）。
	ModesOK
	// ModesEmpty：显式空数组。
	ModesEmpty
	// ModesNull：显式 null。
	ModesNull
	// ModesNotArray：JSON 类型不是数组。
	ModesNotArray
	// ModesUnknown：数组含非字符串元素或词表外字符串（detail 列出未知值名，
	// 不含任何凭据内容）。
	ModesUnknown
)

// ModesReading 是一个账户声明的解析结果。
type ModesReading struct {
	Status ModesStatus
	Modes  []string // 仅 Status==ModesOK（或 ModesEmpty 空切片）时非 nil
	Detail string   // 脱敏细节（未知模式名等），不含解密 JSON
}

// ParseEndpointModes 从解密后的 credentials JSON 解析
// supported_endpoint_modes 声明。缺省与 null 分开分类：两者都进失败清单，
// 但报告 kind 不同，便于操作者定位修复面。
func ParseEndpointModes(credentials map[string]any) ModesReading {
	raw, present := credentials["supported_endpoint_modes"]
	if !present {
		return ModesReading{Status: ModesMissing, Detail: "credentials 缺少 supported_endpoint_modes 键"}
	}
	if raw == nil {
		return ModesReading{Status: ModesNull, Detail: "supported_endpoint_modes 为 null"}
	}
	items, ok := raw.([]any)
	if !ok {
		return ModesReading{Status: ModesNotArray, Detail: fmt.Sprintf("supported_endpoint_modes 类型为 %T，期望数组", raw)}
	}
	modes := make([]string, 0, len(items))
	unknown := []string{}
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			unknown = append(unknown, fmt.Sprintf("%T", item))
			continue
		}
		if !knownModeSet[text] {
			unknown = append(unknown, text)
			continue
		}
		modes = append(modes, text)
	}
	if len(unknown) > 0 {
		return ModesReading{Status: ModesUnknown, Detail: "未知 endpoint mode：" + joinQuoted(unknown)}
	}
	if len(modes) == 0 {
		return ModesReading{Status: ModesEmpty, Detail: "supported_endpoint_modes 为空数组"}
	}
	return ModesReading{Status: ModesOK, Modes: modes}
}

// FailureKind 是失败清单的脱敏分类（JSON 报告值）。
type FailureKind string

const (
	FailureCredentialsDecrypt   FailureKind = "credentials_decrypt_failed"
	FailureModesMissing         FailureKind = "modes_missing"
	FailureModesEmpty           FailureKind = "modes_empty"
	FailureModesNull            FailureKind = "modes_null"
	FailureModesNotArray        FailureKind = "modes_not_array"
	FailureModesUnknown         FailureKind = "modes_unknown_value"
	FailureInstanceNoSource     FailureKind = "instance_source_missing"
	FailureInstanceSourceFailed FailureKind = "instance_source_failed"
)

// Failure 是失败清单的一行：账户 ID、分类与脱敏细节。报告不输出凭据、
// 解密 JSON 或连接串。
type Failure struct {
	AccountID string      `json:"accountId"`
	Kind      FailureKind `json:"kind"`
	Detail    string      `json:"detail,omitempty"`
}

// Error 实现 error，便于失败清单直接阻断阶段完成。
func (f Failure) Error() string {
	if f.Detail == "" {
		return fmt.Sprintf("account %s: %s", f.AccountID, f.Kind)
	}
	return fmt.Sprintf("account %s: %s: %s", f.AccountID, f.Kind, f.Detail)
}

// FailureFor 把声明解析结果映射为失败清单条目（ModesOK 返回 false）。
func FailureFor(accountID string, reading ModesReading) (Failure, bool) {
	var kind FailureKind
	switch reading.Status {
	case ModesOK:
		return Failure{}, false
	case ModesMissing:
		kind = FailureModesMissing
	case ModesEmpty:
		kind = FailureModesEmpty
	case ModesNull:
		kind = FailureModesNull
	case ModesNotArray:
		kind = FailureModesNotArray
	case ModesUnknown:
		kind = FailureModesUnknown
	default:
		kind = FailureModesNotArray
	}
	return Failure{AccountID: accountID, Kind: kind, Detail: reading.Detail}, true
}

func containsAny(values, targets []string) bool {
	for _, value := range values {
		for _, target := range targets {
			if value == target {
				return true
			}
		}
	}
	return false
}

func joinQuoted(values []string) string {
	out := ""
	for i, value := range values {
		if i > 0 {
			out += ", "
		}
		out += "'" + value + "'"
	}
	return out
}
