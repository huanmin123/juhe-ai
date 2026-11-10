// 分组调度策略映射纯函数（普通分组可选调度策略设计 §2.2/§5.2）。本文件只做
// 内存分类与映射，不触库；Plan/Apply/Verify 编排见 migration.go。
//
// 映射与 gateway internal/groups 的 mapLegacySchedulingPolicy 读兼容口径同源
// （跨项目禁止 import，语义在此独立声明，漂移由两侧测试钉住）：
//   - defaultSoftConcurrency ∈ [1, min(1000000, globalMax)) 保留原值；
//     缺失、<=0 或 >=globalMax → -1（旧默认 globalMax 等价于硬上限，不迁移
//     为阈值）；
//   - clientIpConcurrencyLimit 保留（0..1000000）；
//   - 两项都默认 → scheduling_policy_json = NULL。
package groupspolicymigration

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
)

// GroupTypePersonal 是迁移后的唯一分组类型；LegacyGroupTypeHighConcurrency
// 是退役类型，只作为迁移输入识别，任何路径都不再写回。
const (
	GroupTypePersonal              = "personal"
	LegacyGroupTypeHighConcurrency = "high_concurrency"
)

// DefaultGlobalConcurrencyMax 与 gateway internal/groups
// defaultGlobalConcurrencyMax 同源：旧高并发策略注入的进程配置
// JUHE_AI_CONCURRENCY_GLOBAL_MAX 的默认值。CLI 以 --global-max 显式提供
// （契约 §5.2：不把默认值硬编码为业务常量），upgrade 步骤无人工输入面，
// 用本常量并在报告/manifest 中回显实际值。
const DefaultGlobalConcurrencyMax = 5000

// MaxGlobalConcurrencyMax 与 gateway JUHE_AI_CONCURRENCY_GLOBAL_MAX 的
// integerConfig 上限 50000 同源（runtime.ts:410）；CLI/upgrade env 越界视为
// 配置错误，避免用不可能在 gateway 出现的值平移迁移切点。
const MaxGlobalConcurrencyMax = 50000

// 两键词表（契约 §2.2）。
const (
	PolicyKeyDefaultSoftConcurrency   = "defaultSoftConcurrency"
	PolicyKeyClientIPConcurrencyLimit = "clientIpConcurrencyLimit"
)

// 两键合法范围上限（契约 §2.2：阈值 -1 或 1..1000000；IP 限制 0..1000000）。
const (
	softLimitMax = 1_000_000
	ipLimitMax   = 1_000_000
)

// legacyPolicyKeys 是旧 16 键高并发策略的完整词表（契约 §2.3 退役字段清单 +
// 两个现役键）。词表之外的键属手工写入的未知数据 → needs_review，不做猜测。
var legacyPolicyKeys = [16]string{
	"mode",
	"defaultSoftConcurrency",
	"fastFirstEnabled",
	"fallbackOnQueueEnabled",
	"breakAffinityOnSoftLimit",
	"breakAffinityOnQueueWaitMs",
	"slowRequestThresholdMs",
	"firstOutputSlowThresholdMs",
	"recentTimeoutWindowSeconds",
	"recentTimeoutPenaltyThreshold",
	"maxQueueWaitMs",
	"maxQueueSize",
	"perApiKeyQueueLimit",
	"clientIpConcurrencyLimit",
	"clientIpConcurrencyOverflowMode",
	"imageLaneMaxConcurrency",
}

// needs_review 原因词表（报告/manifest 固定口径；apply 阶段任一行命中即阻断，
// 退出码 3，契约 §5.2）。
const (
	ReviewMalformedJSON      = "malformed_json"             // JSON 损坏或非对象
	ReviewUnknownKey         = "unknown_key"                // 16 键词表之外的键
	ReviewUnknownGroupType   = "unknown_group_type"         // 非 personal / high_concurrency
	ReviewInvalidValue       = "invalid_value"              // 两键值非整数或越界
	ReviewMissingPolicy      = "missing_policy"             // high_concurrency 行策略列为 NULL
	ReviewSuperGroupNoMember = "super_group_no_member"      // 超级分组无成员行
	ReviewSuperGroupMember   = "super_group_member_blocked" // 首成员行需要人工复核
)

// TwoKeyPolicy 是映射后的两键目标值。
type TwoKeyPolicy struct {
	SoftConcurrency int // -1 关闭；生效阈值 = min(值, 账户硬并发上限)
	ClientIPLimit   int // 0 关闭；超限立即 429
}

// IsDefault 报告两项都为默认值（契约 §2.2：此时存储为 NULL）。
func (p TwoKeyPolicy) IsDefault() bool {
	return p.SoftConcurrency == -1 && p.ClientIPLimit == 0
}

// TargetJSON 渲染存储形状 JSON（map 序列化键序与 gateway 写面
// schedulingPolicyJSON 的 json.Marshal 同形：字母序）。返回值恒非空；
// 默认组合应写 SQL NULL 而不是序列化结果（见 NormalizeRow 的 targetNull）。
func (p TwoKeyPolicy) TargetJSON() string {
	encoded, err := json.Marshal(map[string]any{
		PolicyKeyDefaultSoftConcurrency:   p.SoftConcurrency,
		PolicyKeyClientIPConcurrencyLimit: p.ClientIPLimit,
	})
	if err != nil {
		// 只含两个整数字段的 map 序列化不可失败；失败属内部错误，panic 由
		// 迁移命令以运行失败暴露，不静默写坏数据。
		panic("groupspolicymigration: 序列化两键策略失败: " + err.Error())
	}
	return string(encoded)
}

// MapLegacyPolicy 把旧策略对象映射为两键目标（契约 §5.2，与 gateway
// mapLegacySchedulingPolicy 同源）。返回 ok=false 表示两键值非法
// （非整数或 IP 越界），调用方按 needs_review 处置，不猜测修复。导出给
// legacybusinessimport / mergesupermigration 归一复制面复用。
func MapLegacyPolicy(object map[string]any, globalMax int) (TwoKeyPolicy, bool) {
	threshold := -1
	if raw, present := object[PolicyKeyDefaultSoftConcurrency]; present && raw != nil {
		value, ok := policyInteger(raw)
		if !ok {
			return TwoKeyPolicy{}, false
		}
		// [1, min(1000000, globalMax)) 保留（两端开区间语义：恰好等于上限的
		// 旧值不保留）；否则旧值等价于关闭（-1）。
		if value >= 1 && value < softLimitMax && value < globalMax {
			threshold = value
		}
	}
	ipLimit := 0
	if raw, present := object[PolicyKeyClientIPConcurrencyLimit]; present && raw != nil {
		value, ok := policyInteger(raw)
		if !ok || value < 0 || value > ipLimitMax {
			return TwoKeyPolicy{}, false
		}
		ipLimit = value
	}
	return TwoKeyPolicy{SoftConcurrency: threshold, ClientIPLimit: ipLimit}, true
}

// RowTarget 是 NormalizeRow 的结果：迁移后该行应有的 group_type 与策略列。
// TargetNull=true 表示目标策略列为 SQL NULL。
type RowTarget struct {
	TargetType   string
	TargetPolicy TwoKeyPolicy
	TargetNull   bool
	// QueueOverflow 是旧 clientIpConcurrencyOverflowMode='queue' 行为标注
	//（迁移后超限从排队变为 429，契约 §3.2/§8-2）。
	QueueOverflow bool
	// ImageLane 是旧 imageLaneMaxConcurrency>0 行为标注（迁移后图片请求按
	// 账户硬并发，契约 §3.4/§8-3）。
	ImageLane bool
}

// NormalizeRow 按契约 §5.2 把一行（group_type + 策略列）归一为目标形状；
// 畸形数据返回非空 reviewReason（needs_review），由调用方阻断并保留原值。
// 映射后的目标与现值语义等值时同样返回目标形状（调用方据此判定 unchanged）。
// globalMax <=0 时取 DefaultGlobalConcurrencyMax。
func NormalizeRow(groupType string, raw sql.NullString, globalMax int) (RowTarget, string) {
	if globalMax <= 0 {
		globalMax = DefaultGlobalConcurrencyMax
	}
	text := ""
	if raw.Valid {
		text = strings.TrimSpace(raw.String)
	}
	if text == "" {
		switch groupType {
		case GroupTypePersonal:
			// 已是终态：personal + NULL。
			return RowTarget{TargetType: GroupTypePersonal, TargetPolicy: TwoKeyPolicy{SoftConcurrency: -1, ClientIPLimit: 0}, TargetNull: true}, ""
		case LegacyGroupTypeHighConcurrency:
			// 高并发分组的写入契约是完整 16 键文档（gateway 读路径对 NULL
			// 策略直接报「分组调度策略缺失」）；NULL 属人工编辑过的脏数据，
			// 不猜测迁移目标。
			return RowTarget{}, ReviewMissingPolicy
		default:
			return RowTarget{}, ReviewUnknownGroupType
		}
	}
	var decoded any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		return RowTarget{}, ReviewMalformedJSON
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return RowTarget{}, ReviewMalformedJSON
	}
	if !knownPolicyKeysOnly(object) {
		return RowTarget{}, ReviewUnknownKey
	}
	switch groupType {
	case GroupTypePersonal, LegacyGroupTypeHighConcurrency:
	default:
		return RowTarget{}, ReviewUnknownGroupType
	}
	policy, ok := MapLegacyPolicy(object, globalMax)
	if !ok {
		return RowTarget{}, ReviewInvalidValue
	}
	target := RowTarget{TargetType: GroupTypePersonal, TargetPolicy: policy, TargetNull: policy.IsDefault()}
	if mode, _ := object["clientIpConcurrencyOverflowMode"].(string); mode == "queue" {
		target.QueueOverflow = true
	}
	if lane, ok := policyInteger(object["imageLaneMaxConcurrency"]); ok && lane > 0 {
		target.ImageLane = true
	}
	return target, ""
}

// rowMatchesTarget 判定现值是否已是目标形状（group_type 一致且策略列与目标
// 语义等值）。语义比较经 JSON 规范化（数值统一为 float64 形态），键序与空白
// 差异不触发改写；完成态重跑因此零写入（幂等，契约 §5.3）。
func rowMatchesTarget(groupType string, raw sql.NullString, target RowTarget) bool {
	if groupType != target.TargetType {
		return false
	}
	text := ""
	if raw.Valid {
		text = strings.TrimSpace(raw.String)
	}
	if text == "" {
		return target.TargetNull
	}
	if target.TargetNull {
		// 目标 NULL 而现值非空：一律重写为 NULL。空对象 `{}` 也不是合法的
		// 两键策略（契约 §2.2：非空必须两键齐全），不能当作语义 NULL 跳过。
		return false
	}
	var decoded any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		return false
	}
	var normalized any
	if err := json.Unmarshal([]byte(target.TargetPolicy.TargetJSON()), &normalized); err != nil {
		return false
	}
	return reflect.DeepEqual(decoded, normalized)
}

// rawIsEmptyObject 判定文本是合法 JSON 的空对象（仅保留给历史调用方；
// 空对象不是合法两键策略，迁移会重写为 NULL）。
func rawIsEmptyObject(text string) bool {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		return false
	}
	return len(decoded) == 0
}

// ValidateStoredTwoKeyPolicy 校验存储行是否为完成态形状（契约 §5.3：策略列
// 只含两键或 NULL，且取值合法）。postflight / upgrade Satisfied 与 Verify
// 共用本谓词。非空对象必须两键齐全；空对象不是合法策略。
func ValidateStoredTwoKeyPolicy(raw sql.NullString) bool {
	text := ""
	if raw.Valid {
		text = strings.TrimSpace(raw.String)
	}
	if text == "" {
		return true
	}
	var decoded any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		return false
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return false
	}
	seenSoft, seenIP := false, false
	for key, value := range object {
		switch key {
		case PolicyKeyDefaultSoftConcurrency:
			seenSoft = true
			number, ok := policyInteger(value)
			if !ok || (number != -1 && (number < 1 || number > softLimitMax)) {
				return false
			}
		case PolicyKeyClientIPConcurrencyLimit:
			seenIP = true
			number, ok := policyInteger(value)
			if !ok || number < 0 || number > ipLimitMax {
				return false
			}
		default:
			return false
		}
	}
	return seenSoft && seenIP
}

// knownPolicyKeysOnly 报告对象键集合是否都在 16 键词表内。
func knownPolicyKeysOnly(object map[string]any) bool {
	for key := range object {
		known := false
		for _, candidate := range legacyPolicyKeys {
			if key == candidate {
				known = true
				break
			}
		}
		if !known {
			return false
		}
	}
	return true
}

// policyInteger 与 gateway groups.policyInteger 同源：JSON number 解码后的
// float64 必须是整数值；float32 / 各宽度整数直通。
func policyInteger(raw any) (int, bool) {
	switch value := raw.(type) {
	case float64:
		number := int64(value)
		if float64(number) != value {
			return 0, false
		}
		return int(number), true
	case float32:
		number := int64(value)
		if float32(number) != value {
			return 0, false
		}
		return int(number), true
	case int:
		return value, true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	default:
		return 0, false
	}
}
