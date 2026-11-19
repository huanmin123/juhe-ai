// 分组策略解析与 Key 目标决策纯函数（API Key 单 IP 并发限制设计 §2.1/§2.4/
// §4.2）。本文件只做内存解析、映射与决策，不触库；Plan/Apply/Verify 编排见
// migration.go。
package keyipmigration

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
)

// 分组策略键词表（revision 25 完成态只允许单键 defaultSoftConcurrency；
// clientIpConcurrencyLimit 是 revision 22 遗留键，迁移时读取并从分组删除）。
const (
	PolicyKeyDefaultSoftConcurrency   = "defaultSoftConcurrency"
	PolicyKeyClientIPConcurrencyLimit = "clientIpConcurrencyLimit"
)

// 策略合法范围（API Key 单 IP 并发限制设计 §2.1/§2.4）。
const (
	SoftLimitMax = 1_000_000
	IPLimitMax   = 1_000_000
	// DisabledKeyIPLimit 是 Key 级单 IP 上限的「不限制」值，与分组
	// defaultSoftConcurrency 的 -1 语义一致。0 是 revision 25 未发布版本用过
	// 的临时关闭值，迁移按未设置归一为 -1。
	DisabledKeyIPLimit = -1
)

// needs_review 原因词表（manifest/报告固定口径；apply 阶段任一行命中即阻断，
// CLI 退出码 3，upgrade 步骤 Verify 失败 → exit 3）。
const (
	ReviewMalformedJSON                 = "malformed_json"                      // JSON 损坏或非对象
	ReviewUnknownKey                    = "unknown_key"                         // defaultSoftConcurrency/clientIpConcurrencyLimit 之外的键
	ReviewMissingDefaultSoftConcurrency = "missing_default_soft_concurrency"    // 非空策略缺 defaultSoftConcurrency
	ReviewInvalidDefaultSoftConcurrency = "invalid_default_soft_concurrency"    // 阈值非整数或不在 -1 / 1..1000000
	ReviewInvalidClientIPConcurrency    = "invalid_client_ip_concurrency_limit" // IP 限制非整数或不在 0..1000000
	ReviewGroupMissing                  = "group_missing"                       // 绑定/成员引用的分组不存在
	ReviewUnknownGroupKind              = "unknown_group_kind"                  // group_kind 不是 standard/super
	ReviewRouteStrategyMissing          = "route_strategy_missing"              // Key 的 route_strategy_id 不存在
	ReviewOwnerMismatch                 = "system_account_mismatch"             // Key 与策略/绑定 owner 不一致（分组跨 owner 属合法授权，不在此列）
	ReviewNestedSuperMember             = "nested_super_member"                 // 超级分组成员仍是超级分组（只允许一层）
	ReviewSuperGroupMemberMissing       = "super_group_member_missing"          // 超级分组成员行引用缺失分组
	ReviewSuperGroupMemberBlocked       = "super_group_member_blocked"          // 成员策略本身需要人工复核
	ReviewSuperGroupMemberMismatch      = "super_group_member_policy_mismatch"  // 成员 defaultSoftConcurrency 不一致
	ReviewSourceGroupInvalid            = "source_group_invalid"                // 可达分组策略畸形/需要复核
	ReviewMultipleIPLimits              = "multiple_ip_limits"                  // 多个不同旧 IP 值且当前 Key 无人工裁决值
	ReviewExistingKeyLimitConflict      = "existing_key_limit_conflict"         // 当前 Key 值不等于任何观测值
	ReviewInvalidKeyLimitValue          = "invalid_key_limit_value"             // 当前 Key 值不是 -1 或 1..1000000
)

// 计划行动作词表。
const (
	ActionUnchanged   = "unchanged"
	ActionUpdate      = "update"
	ActionPreexisting = "preexisting" // Key 已有非零值，按 preexisting 语义保留
	ActionNeedsReview = "needs_review"
)

// ConflictPolicy 是多个不同旧 IP 值时的收敛策略（设计 §4.2 规则 4）。
type ConflictPolicy string

const (
	ConflictPolicyBlock ConflictPolicy = "block"
	ConflictPolicyMin   ConflictPolicy = "min"
)

// NormalizeConflictPolicy 归一 --conflict-policy 取值；空值默认 block。
func NormalizeConflictPolicy(raw string) (ConflictPolicy, bool) {
	switch ConflictPolicy(strings.ToLower(strings.TrimSpace(raw))) {
	case "", ConflictPolicyBlock:
		return ConflictPolicyBlock, true
	case ConflictPolicyMin:
		return ConflictPolicyMin, true
	default:
		return "", false
	}
}

// storedPolicy 是对一行分组策略列的严格解析结果。
type storedPolicy struct {
	Null   bool   // NULL/空串：等价 defaultSoftConcurrency=-1
	Soft   int    // -1 或 1..1000000
	IP     int    // 0..1000000；仅在 HasIP 时有效
	HasIP  bool   // 旧键 clientIpConcurrencyLimit 是否存在
	Reason string // 非空表示畸形/非法，需要人工复核
}

// ParseStoredPolicy 严格解析分组策略列（revision 25 迁移输入）：
//   - NULL/空串 → Null=true, Soft=-1；
//   - 非空必须是 JSON 对象，只允许 defaultSoftConcurrency 与（历史）
//     clientIpConcurrencyLimit 两个键；
//   - defaultSoftConcurrency 必填，取值 -1 或 1..1000000，值原样保留
//     （不再做 globalMax 映射：revision 22 已完成历史映射）；
//   - clientIpConcurrencyLimit 可选，取值 0..1000000（0 视为无值）。
func ParseStoredPolicy(raw sql.NullString) storedPolicy {
	text := ""
	if raw.Valid {
		text = strings.TrimSpace(raw.String)
	}
	if text == "" {
		return storedPolicy{Null: true, Soft: -1}
	}
	var decoded any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		return storedPolicy{Reason: ReviewMalformedJSON}
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return storedPolicy{Reason: ReviewMalformedJSON}
	}
	policy := storedPolicy{Soft: -1}
	seenSoft := false
	for key, value := range object {
		switch key {
		case PolicyKeyDefaultSoftConcurrency:
			number, ok := policyInteger(value)
			if !ok || (number != -1 && (number < 1 || number > SoftLimitMax)) {
				return storedPolicy{Reason: ReviewInvalidDefaultSoftConcurrency}
			}
			policy.Soft = number
			seenSoft = true
		case PolicyKeyClientIPConcurrencyLimit:
			number, ok := policyInteger(value)
			if !ok || number < 0 || number > IPLimitMax {
				return storedPolicy{Reason: ReviewInvalidClientIPConcurrency}
			}
			policy.IP = number
			policy.HasIP = true
		default:
			return storedPolicy{Reason: ReviewUnknownKey}
		}
	}
	if !seenSoft {
		return storedPolicy{Reason: ReviewMissingDefaultSoftConcurrency}
	}
	return policy
}

// policyInteger 与 gateway groups.policyInteger 同源：JSON number 解码后的
// float64 必须是整数值；其余数值类型直通。
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

// SingleKeyPolicyJSON 渲染 revision 25 完成态单键策略。
// soft=-1 时返回空串（调用方应写 SQL NULL）。
func SingleKeyPolicyJSON(soft int) string {
	if soft == -1 {
		return ""
	}
	encoded, err := json.Marshal(map[string]any{PolicyKeyDefaultSoftConcurrency: soft})
	if err != nil {
		// 只含一个整数字段的 map 序列化不可失败；失败属内部错误，由迁移命令
		// 以运行失败暴露，不静默写坏数据。
		panic("keyipmigration: 序列化单键策略失败: " + err.Error())
	}
	return string(encoded)
}

// singleKeyPolicyMatches 判定策略列是否已是目标单键形状（严格：只允许
// defaultSoftConcurrency 键；soft=-1 时目标为 SQL NULL）。
func singleKeyPolicyMatches(raw sql.NullString, soft int) bool {
	text := ""
	if raw.Valid {
		text = strings.TrimSpace(raw.String)
	}
	if soft == -1 {
		return text == ""
	}
	if text == "" {
		return false
	}
	var decoded any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		return false
	}
	object, ok := decoded.(map[string]any)
	if !ok || len(object) != 1 {
		return false
	}
	value, present := object[PolicyKeyDefaultSoftConcurrency]
	if !present {
		return false
	}
	number, ok := policyInteger(value)
	return ok && number == soft
}

// keyDecision 是单个 API Key 的目标决策结果。
type keyDecision struct {
	Target   int    // 目标 client_ip_concurrency_limit；needs_review 时保留当前值
	Action   string // update/unchanged/preexisting/needs_review
	Reason   string // needs_review 原因
	Conflict *ConflictRow
	Notes    []string
}

// DecideKeyTarget 按设计 §4.2 与 preexisting 语义计算单个 Key 的目标值。
// observed 是可达分组观测到的旧 IP 正值（调用方去重前可含重复）。
//
// 当前值取 -1 表示不限制（新契约）；0 是 revision 25 未发布版本的临时关闭
// 值，按未设置归一为 -1；其余负数或超过 1000000 视为非法存量，进入
// needs_review 由人工裁决。
//
// preexisting 语义（2026-11-19 裁决补充）：当前 Key 值 >0 时——
//   - 等于任一观测正值：视为人工裁决已生效，保留且不算冲突（多值冲突也由此
//     收敛，不再阻断）；
//   - 观测集非空且不等于任何观测值：保留当前值；block 模式记
//     existing_key_limit_conflict 阻断，min 模式只在 manifest 记冲突；
//   - 观测集为空：保留当前值且不记冲突——迁移完成后的幂等重跑、或旧分组本来
//     就没有 IP 值，都不能反过来把人工设置值判成冲突。
//
// 观测为空且 current=-1 → unchanged；current=0 → 归一为 -1（update）。
func DecideKeyTarget(current int, observed []int, policy ConflictPolicy) keyDecision {
	if current < DisabledKeyIPLimit || current > IPLimitMax {
		return keyDecision{Target: current, Action: ActionNeedsReview, Reason: ReviewInvalidKeyLimitValue}
	}
	normalizeZero := current == 0
	if normalizeZero {
		current = DisabledKeyIPLimit
	}
	values := distinctSorted(observed)
	if len(values) == 0 {
		if current == DisabledKeyIPLimit {
			if normalizeZero {
				return keyDecision{Target: DisabledKeyIPLimit, Action: ActionUpdate}
			}
			return keyDecision{Target: DisabledKeyIPLimit, Action: ActionUnchanged}
		}
		return keyDecision{Target: current, Action: ActionPreexisting}
	}
	if current > 0 && containsInt(values, current) {
		// 人工裁决值命中候选集合：保留，不再阻断（多值冲突由人工选择收敛）。
		return keyDecision{Target: current, Action: ActionPreexisting}
	}
	if len(values) == 1 {
		chosen := values[0]
		if current == DisabledKeyIPLimit {
			return keyDecision{Target: chosen, Action: ActionUpdate}
		}
		// current != chosen（current > 0 且不在观测集合）。
		if policy == ConflictPolicyMin {
			return keyDecision{
				Target: current, Action: ActionPreexisting,
				Conflict: &ConflictRow{
					ObservedValues: values, CurrentValue: current, ChosenValue: current,
					Kind: ConflictKindExistingKeyLimit, Resolution: ConflictResolutionPreexistingKept,
				},
			}
		}
		return keyDecision{Target: current, Action: ActionNeedsReview, Reason: ReviewExistingKeyLimitConflict}
	}
	// 多个不同正值。
	if policy == ConflictPolicyMin {
		chosen := values[0]
		decision := keyDecision{Target: chosen, Action: ActionUpdate}
		conflict := ConflictRow{
			ObservedValues: values, CurrentValue: current, ChosenValue: chosen,
			Kind: ConflictKindMultipleGroupValues, Resolution: ConflictResolutionMinSelected,
		}
		if current > 0 {
			// current 与全部观测值都不同：不覆盖人工值，只记录冲突。
			decision = keyDecision{Target: current, Action: ActionPreexisting}
			conflict.ChosenValue = current
			conflict.Resolution = ConflictResolutionPreexistingKept
		}
		decision.Conflict = &conflict
		return decision
	}
	if current > 0 {
		return keyDecision{Target: current, Action: ActionNeedsReview, Reason: ReviewExistingKeyLimitConflict}
	}
	return keyDecision{Target: current, Action: ActionNeedsReview, Reason: ReviewMultipleIPLimits}
}

// 冲突行动作/处置词表。
const (
	ConflictKindMultipleGroupValues   = "multiple_group_values"
	ConflictKindExistingKeyLimit      = "existing_key_limit_conflict"
	ConflictResolutionMinSelected     = "min_selected"
	ConflictResolutionPreexistingKept = "preexisting_kept"
)

// distinctSorted 去重并按升序返回。
func distinctSorted(values []int) []int {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(values))
	out := make([]int, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
