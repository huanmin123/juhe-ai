package accounts

// list_runtime_overlay.go — 账户管理列表的运行态 overlay（缺陷修复批次一）：
// 管理面/用户面账户列表此前从不返回 runtimeAvailability / circuitSummary /
// apiKeyRuntime / effectiveAvailability 的运行态分支，被熔断或运行态抑制的
// 账户在列表上显示为"可调度"（基线 available），前端
// accountFormatters.ts / accountStatusPresentation.ts / accountListMutations.ts
// / accountRules.ts / AccountUsageCell.vue 消费的运行态展示与"重新验证
// Key 池"入口不可见。本文件把三路运行态事实叠加到 ListPage 行上，并重算
// effectiveAvailability 的 api_key_pool / runtime 分支。
//
// 批次边界（后续批次在此文件与装配侧继续，不回改本批语义）：
//   - 第二批：availabilityPresentation 投影 + 探针 tooltip（probePresentation /
//     lastObservation / traceId）——本批 DTO 刻意不含 probePresentation 键
//     （前端一旦收到该键，内部 schedule 形状即为必填）；
//   - 第三批：/my-accounts PG 投影的同一 overlay。
//
// 数据源（装配见 cmd/juhe-ai-gateway/compose_account_runtime_overlay.go）：
//   - runtime ← 网关进程内 LocalSuppressionStore 快照 + 配置策略避让状态合并；
//   - circuit ← account_circuit_incidents 控制面持久账本（公共摘要 reducer）；
//   - apiKeyRuntime ← accountkeystates 池摘要。
// 任一端口 nil 或读失败都按先例降级：字段缺席、warn 留痕、不阻断页面
// （hydrateBalanceSnapshots / hydrateCurrentConcurrency 同款失败语义）。

import (
	"context"
	"log/slog"
)

// AccountRuntimeAvailabilityPublic mirrors AccountRuntimeAvailability
// （frontend/src/types/domain/accounts.ts AccountRuntimeAvailabilityStatus）：
// status 必填，reason/since 可选。第二批之前不携带 probePresentation。
type AccountRuntimeAvailabilityPublic struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Since  string `json:"since,omitempty"`
}

// AccountCircuitSummaryPublic mirrors PublicAccountCircuitSummary（status=
// normal|verifying|avoided|recovering）。status=normal 的账户不输出本字段
// （前端按缺席过滤 normal）。
type AccountCircuitSummaryPublic struct {
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	Since       string `json:"since,omitempty"`
	NextCheckAt string `json:"nextCheckAt,omitempty"`
}

// AccountApiKeyRuntimeSummaryPublic mirrors
// AccountApiKeyRuntimePublicSummary：前 8 个数值/布尔字段必填（始终序列化），
// nextProbeAt 可省略。
type AccountApiKeyRuntimeSummaryPublic struct {
	Total                int    `json:"total"`
	Active               int    `json:"active"`
	TemporaryUnavailable int    `json:"temporaryUnavailable"`
	RateLimited          int    `json:"rateLimited"`
	Error                int    `json:"error"`
	Disabled             int    `json:"disabled"`
	Unavailable          int    `json:"unavailable"`
	AllUnavailable       bool   `json:"allUnavailable"`
	NextProbeAt          string `json:"nextProbeAt,omitempty"`
}

// RuntimeAvailabilitySource is the runtime overlay read port behind the list
// runtimeAvailability overlay：按账户运行态键（owner 行=账户 id；authorized
// 实例行=`{id}:authorized:{systemAccountId}:{boundGroupId}:{authorizationId}`）
// 批量读当前网关的抑制/降级/避让可用性。Nil keeps the fields absent.
type RuntimeAvailabilitySource interface {
	LoadRuntimeAvailabilityByRuntimeKeys(ctx context.Context, runtimeKeys []string) (map[string]AccountRuntimeAvailabilityPublic, error)
}

// CircuitSummarySource is the account circuit incident summary read port
// behind the list circuitSummary overlay（同一运行态键空间）。读失败由调用方
// 降级为字段缺席。Nil keeps the fields absent.
type CircuitSummarySource interface {
	LoadCircuitSummariesByRuntimeKeys(ctx context.Context, runtimeKeys []string) (map[string]AccountCircuitSummaryPublic, error)
}

// APIKeyRuntimeSummarySource is the API Key pool summary read port behind the
// list apiKeyRuntime overlay（键为账户 id；authorized 实例行解析到来源账户
// id）。非池账户允许缺席（调用方按 map 命中投影）。Nil keeps the field absent.
type APIKeyRuntimeSummarySource interface {
	LoadAPIKeyRuntimeSummariesByAccountIds(ctx context.Context, accountIDs []string) (map[string]AccountApiKeyRuntimeSummaryPublic, error)
}

// SetRuntimeAvailabilitySource wires the runtime overlay read port
// post-construction (setter parity with SetConcurrencyReader). A nil source
// keeps the fields absent.
func (s *Store) SetRuntimeAvailabilitySource(source RuntimeAvailabilitySource) {
	s.runtimeAvailabilitySource = source
}

// SetCircuitSummarySource wires the circuit summary read port. A nil source
// keeps the fields absent.
func (s *Store) SetCircuitSummarySource(source CircuitSummarySource) {
	s.circuitSummarySource = source
}

// SetAPIKeyRuntimeSummarySource wires the API Key pool summary read port. A
// nil source keeps the field absent.
func (s *Store) SetAPIKeyRuntimeSummarySource(source APIKeyRuntimeSummarySource) {
	s.apiKeyRuntimeSummarySource = source
}

// listItemRuntimeKey derives the runtime overlay key for a list row（对齐
// jobs listavailability_hydrate.runtimeKeyOf / Node 同名派生）：owner 行用
// 账户 id；authorized 实例行在绑定齐全（boundGroup 存在且
// bindingSystemAccountID==systemAccountID、授权 id 在位）时派生
// `{id}:authorized:{systemAccountId}:{boundGroupId}:{authorizationId}`，
// 绑定缺失时回落账户 id（与 jobs 同款回落；该形态实例行本就不可调度，
// 回落只影响读键，不改变调度事实）。
func listItemRuntimeKey(item ListItem) string {
	if item.AccessType != "authorized" {
		return item.ID
	}
	if item.OwnerSystemAccountID == "" ||
		item.AccountAuthorizationID == nil || *item.AccountAuthorizationID == "" ||
		item.BoundGroupID == nil || *item.BoundGroupID == "" ||
		item.BindingSystemAccountID == nil || *item.BindingSystemAccountID != item.OwnerSystemAccountID {
		return item.ID
	}
	return item.ID + ":authorized:" + item.OwnerSystemAccountID + ":" + *item.BoundGroupID + ":" + *item.AccountAuthorizationID
}

// listItemAPIKeyRuntimeAccountID resolves the apiKeyRuntime summary lookup id
// （对齐 jobs apiKeyRuntimeAccountIDOf）：授权实例行读来源账户 id，其余读
// 自身 id。
func listItemAPIKeyRuntimeAccountID(item ListItem) string {
	if item.AuthorizationInstanceSourceAccountID != nil && *item.AuthorizationInstanceSourceAccountID != "" {
		return *item.AuthorizationInstanceSourceAccountID
	}
	return item.ID
}

// hydrateRuntimeOverlay overlays the runtime/circuit/apiKeyRuntime facts onto
// the list rows and recomputes effectiveAvailability 的运行态分支。每个端口
// 独立降级：nil 端口或读失败只让对应字段缺席（warn 留痕），页面照常返回。
func (s *Store) hydrateRuntimeOverlay(ctx context.Context, items []ListItem) {
	if len(items) == 0 {
		return
	}
	runtimeKeys := []string{}
	apiKeyIDs := []string{}
	seenRuntimeKeys := map[string]bool{}
	seenAPIKeyIDs := map[string]bool{}
	runtimeKeyByItem := make([]string, len(items))
	apiKeyIDByItem := make([]string, len(items))
	for index := range items {
		runtimeKey := listItemRuntimeKey(items[index])
		runtimeKeyByItem[index] = runtimeKey
		if !seenRuntimeKeys[runtimeKey] {
			seenRuntimeKeys[runtimeKey] = true
			runtimeKeys = append(runtimeKeys, runtimeKey)
		}
		apiKeyID := listItemAPIKeyRuntimeAccountID(items[index])
		apiKeyIDByItem[index] = apiKeyID
		if !seenAPIKeyIDs[apiKeyID] {
			seenAPIKeyIDs[apiKeyID] = true
			apiKeyIDs = append(apiKeyIDs, apiKeyID)
		}
	}
	var runtimeByKeys map[string]AccountRuntimeAvailabilityPublic
	if s.runtimeAvailabilitySource != nil {
		loaded, err := s.runtimeAvailabilitySource.LoadRuntimeAvailabilityByRuntimeKeys(ctx, runtimeKeys)
		if err != nil {
			slog.Warn("账户运行态可用性 hydrate 失败，列表回退无运行态 overlay",
				"event", "account_runtime_availability_hydrate_failed",
				"error", err)
		} else {
			runtimeByKeys = loaded
		}
	}
	var circuitsByKeys map[string]AccountCircuitSummaryPublic
	if s.circuitSummarySource != nil {
		loaded, err := s.circuitSummarySource.LoadCircuitSummariesByRuntimeKeys(ctx, runtimeKeys)
		if err != nil {
			// circuit 摘要读失败降级字段缺席（Node 列表投影 source-unavailable
			// 语义：不带摘要，不失败页面）。
			slog.Warn("账户 circuit 摘要 hydrate 失败，列表回退无 circuitSummary",
				"event", "account_circuit_summary_hydrate_failed",
				"error", err)
		} else {
			circuitsByKeys = loaded
		}
	}
	var apiKeyByIDs map[string]AccountApiKeyRuntimeSummaryPublic
	if s.apiKeyRuntimeSummarySource != nil {
		loaded, err := s.apiKeyRuntimeSummarySource.LoadAPIKeyRuntimeSummariesByAccountIds(ctx, apiKeyIDs)
		if err != nil {
			slog.Warn("账户 API Key 池摘要 hydrate 失败，列表回退无 apiKeyRuntime",
				"event", "account_api_key_runtime_summary_hydrate_failed",
				"error", err)
		} else {
			apiKeyByIDs = loaded
		}
	}
	for index := range items {
		item := &items[index]
		if overlay, ok := runtimeByKeys[runtimeKeyByItem[index]]; ok && overlay.Status != "" {
			value := overlay
			item.RuntimeAvailability = &value
		}
		if summary, ok := circuitsByKeys[runtimeKeyByItem[index]]; ok && summary.Status != "" && summary.Status != "normal" {
			value := summary
			item.CircuitSummary = &value
		}
		if summary, ok := apiKeyByIDs[apiKeyIDByItem[index]]; ok {
			value := summary
			item.APIKeyRuntime = &value
		}
		applyRuntimeOverlayAvailability(item)
	}
}

// applyRuntimeOverlayAvailability recomputes the runtime branches of
// effectiveAvailability（对齐 jobs accountEffectiveAvailability 的分支顺序）：
// 实例/授权臂 blocker 优先（base 已在 newListItem 计算完成且不可用即保留）；
// base 可用时先判 API Key 池全不可用，再判运行态抑制。两个分支都不命中时
// 保持 base 原样（含 runtime 缺席与 normal）。
func applyRuntimeOverlayAvailability(item *ListItem) {
	if !item.EffectiveAvailability.Available {
		return
	}
	if summary := item.APIKeyRuntime; summary != nil && summary.AllUnavailable {
		item.EffectiveAvailability = apiKeyPoolUnavailableAvailability(*summary)
		return
	}
	if overlay := item.RuntimeAvailability; overlay != nil {
		if blocker, ok := runtimeAvailabilityBlockerAvailability(overlay.Status, overlay.Reason, item.Status); ok {
			item.EffectiveAvailability = blocker
		}
	}
}

// apiKeyPoolUnavailableAvailability renders the api_key_pool_unavailable
// branch（文案与 jobs/Node 一致；nextProbeAt 空时省略 retryAt）。
func apiKeyPoolUnavailableAvailability(summary AccountApiKeyRuntimeSummaryPublic) EffectiveAvailability {
	reason := "账户内 0 个 API Key 均不可用，后台探测恢复前不会参与调度"
	if summary.Total > 0 {
		reason = "账户内 " + itoa(summary.Total) + " 个 API Key 均不可用，后台探测恢复前不会参与调度"
	}
	var retryAt *string
	if summary.NextProbeAt != "" {
		value := summary.NextProbeAt
		retryAt = &value
	}
	return blockedAvailability("api_key_pool_unavailable", "Key 全部不可用", "red", "api_key_pool", reason, retryAt)
}

// runtimeAvailabilityBlockerAvailability mirrors runtimeAvailabilityBlocker：
// 仅实例状态 active 的行允许运行态分支生效；unknown 状态不产出分支。
// runtime_degraded 不阻断（available=true，色 gold），其余运行态状态产出
// blockerScope=runtime 的 blocked 投影。第二批补 availabilityPresentation，
// 本批只重算 effectiveAvailability。
func runtimeAvailabilityBlockerAvailability(runtimeStatus, runtimeReason, instanceStatus string) (EffectiveAvailability, bool) {
	if runtimeStatus == "" || runtimeStatus == "normal" {
		return EffectiveAvailability{}, false
	}
	if instanceStatus != "active" {
		return EffectiveAvailability{}, false
	}
	switch runtimeStatus {
	case "degraded":
		reason := runtimeReason
		if reason == "" {
			reason = "当前账号近期失败，正常候选不足时才会兜底尝试"
		}
		scope := "runtime"
		return EffectiveAvailability{
			Available: true, Status: "runtime_degraded", Label: "调度降级", Color: "gold",
			BlockerScope: &scope, Reason: &reason,
		}, true
	case "precheck_pending":
		return runtimeBlockedAvailability("runtime_precheck_pending", "待探针确认", "blue", runtimeReason, "当前网关正在执行事前探针确认"), true
	case "local_suppressed":
		return runtimeBlockedAvailability("runtime_local_suppressed", "短暂避让", "gold", runtimeReason, "当前网关短窗口内临时避让该账户"), true
	case "half_open":
		return runtimeBlockedAvailability("runtime_half_open", "半开探测", "blue", runtimeReason, "当前网关已放行一个请求确认账户是否恢复"), true
	case "precheck_failed":
		return runtimeBlockedAvailability("runtime_precheck_failed", "探针确认失败", "gold", runtimeReason, "最近事前探针确认失败，当前网关暂不调度该账户"), true
	}
	return EffectiveAvailability{}, false
}

// runtimeBlockedAvailability renders the blocked runtime branch（reason 取
// overlay 原文，缺省回落 Node 同款文案）。
func runtimeBlockedAvailability(status, label, color, runtimeReason, fallbackReason string) EffectiveAvailability {
	reason := runtimeReason
	if reason == "" {
		reason = fallbackReason
	}
	return blockedAvailability(status, label, color, "runtime", reason, nil)
}
