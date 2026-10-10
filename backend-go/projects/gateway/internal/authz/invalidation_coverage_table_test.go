// Static source contract for the 设计 6.3 "写路径 → 失效覆盖依据" coverage
// table (网关模型列表账户并集设计). The behavior-level half of the contract lives
// in this package (invalidation_test.go, invalidation_coverage_test.go) and in
// internal/business/authorization (authorization_invalidation_test.go); the
// write paths owned by accounts / accountstransfer / routestrategies /
// apikeys / groups cannot be behaviorally injected from this package, so this
// test pins their committed-write invalidation triggers as raw source
// assertions — the documented minimum regression guard: if a refactor drops a
// trigger call or renames a reason, this table fails and the union cache
// would otherwise go stale on that write path.
package authz

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type coverageTableEntry struct {
	// file is relative to this package's directory.
	file string
	// why names the covered write path (设计 6.3 coverage table row).
	why string
	// mustContain pins the post-commit trigger call(s) and reason literal(s).
	mustContain []string
}

var invalidationCoverageTable = []coverageTableEntry{
	{
		file: "../accounts/invalidation.go",
		why:  "账户创建/编辑/删除（管理面写路径）",
		mustContain: []string{
			`accountPatchRuntimeInvalidationReason = "account_management_patch"`,
			`accountCreateRuntimeInvalidationReason = "account_created"`,
			`accountDeleteRuntimeInvalidationReason = "account_deleted"`,
			`InvalidateGatewayRuntime(accountPatchRuntimeInvalidationReason)`,
			`InvalidateGatewayRuntime(accountCreateRuntimeInvalidationReason)`,
			`InvalidateGatewayRuntime(accountDeleteRuntimeInvalidationReason)`,
		},
	},
	{
		file: "../accounts/accountstransfer/batch_effects.go",
		why:  "账户批量编辑",
		mustContain: []string{
			`InvalidateGatewayRuntime("account_batch_updated")`,
		},
	},
	{
		file: "../routestrategies/mutations.go",
		why:  "策略路由创建/更新/删除（绑定：策略→分组）",
		mustContain: []string{
			`s.invalidateRuntime("route_strategy_created")`,
			`s.invalidateRuntime("route_strategy_updated")`,
			`s.invalidateRuntime("route_strategy_deleted")`,
		},
	},
	{
		file: "../apikeys/patch.go",
		why:  "API Key 编辑/配额（绑定：api_keys→策略）",
		mustContain: []string{
			`s.inval.InvalidateRuntime(id, ReasonAPIKeyUpdated)`,
			`s.inval.InvalidateQuota(id, ReasonAPIKeyQuotaUpdated)`,
		},
	},
	{
		file: "../groups/store.go",
		why:  "分组创建/更新/删除（启停 + group_accounts/group_authorization_settings 级联）",
		mustContain: []string{
			`s.invalidateRuntime("group_created")`,
			`s.invalidateRuntime("group_updated")`,
			`s.invalidateRuntime("group_deleted")`,
		},
	},
	{
		file: "mutations.go",
		why:  "授权创建/编辑/撤销/回收 + 到期翻转 sweep（resource_authorizations 状态写）",
		mustContain: []string{
			`s.invalidateAfterBusinessWrite(ctx, invalidationReasonCreated)`,
			`s.invalidateAfterBusinessWrite(ctx, invalidationReasonUpdated)`,
			`s.invalidateAfterBusinessWrite(ctx, invalidationReasonRevoked)`,
			`s.invalidateAfterBusinessWrite(ctx, invalidationReasonReturned)`,
			`s.invalidateAfterBusinessWrite(ctx, invalidationReasonExpired)`,
		},
	},
	{
		file: "return_group.go",
		why:  "分组授权被授权人归还",
		mustContain: []string{
			`s.invalidateAfterBusinessWrite(ctx, invalidationReasonReturned)`,
		},
	},
	{
		file: "team_cascade.go",
		why:  "团队级联独立事务入口（停用撤销/重启用重展开/移除成员撤销）",
		mustContain: []string{
			`s.invalidateAfterBusinessWrite(ctx, invalidationReasonRevoked)`,
			`s.invalidateAfterBusinessWrite(ctx, invalidationReasonUpdated)`,
		},
	},
	{
		file: "expiry_reconcile.go",
		why:  "jobs sweep 到期翻转的 gateway 侧投影收敛重放",
		mustContain: []string{
			`s.invalidateAfterBusinessWrite(ctx, invalidationReasonExpired)`,
		},
	},
	{
		file: "../accounts/m11_authorized_dispatch.go",
		why:  "授权实例调度恢复（账户 status/schedulable/config_revision + group_accounts 绑定）",
		mustContain: []string{
			`s.invalidator.InvalidateGatewayRuntime(accountPatchRuntimeInvalidationReason)`,
		},
	},
	{
		file: "../business/authorization/invalidation.go",
		why:  "business 授权切片 ExpireDue 过期/撤销清理的 K5 总线端口",
		mustContain: []string{
			`invalidationReasonExpired = "resource_authorization_expired"`,
			`inval.TopicGatewayRuntime`,
			`inval.TopicAuthorizationQuota`,
		},
	},
	{
		file: "../business/authorization/authorization.go",
		why:  "business 授权切片 ExpireDue（授权终态物化 + group_accounts/group_authorization_settings 清理）",
		mustContain: []string{
			`s.invalidateAfterExpiryCleanup(r)`,
		},
	},
	{
		file: "../business/account_cleanup/invalidation.go",
		why:  "business 账户清理切片（孤儿实例墓碑/过期账户物理删除，改 accounts/group_accounts/映射与授权行，双 topic 对齐 ExpireDue）；该包当前无生产消费方，端口随 compose 接线生效",
		mustContain: []string{
			`invalidationReasonCleanupApplied = "account_cleanup_applied"`,
			`s.invalidator.Invalidate(inval.TopicGatewayRuntime, invalidationReasonCleanupApplied)`,
			`s.invalidator.Invalidate(inval.TopicAuthorizationQuota, invalidationReasonCleanupApplied)`,
		},
	},
	{
		file: "../business/accounts/invalidation.go",
		why:  "business 账户管理切片（建户/更新/删除写 supportedModels/mappings 与账户行）；该包当前无生产消费方，端口随 compose 接线生效",
		mustContain: []string{
			`invalidationReasonCreated = "account_created"`,
			`invalidationReasonPatched = "account_management_patch"`,
			`invalidationReasonDeleted = "account_deleted"`,
			`s.invalidator.Invalidate(inval.TopicGatewayRuntime, reason)`,
		},
	},
}

func TestGatewayRuntimeInvalidationCoverageTable(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	base := filepath.Dir(thisFile)
	for _, entry := range invalidationCoverageTable {
		content, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(entry.file)))
		if err != nil {
			t.Fatalf("coverage table entry %s (%s): %v", entry.file, entry.why, err)
		}
		for _, needle := range entry.mustContain {
			if !strings.Contains(string(content), needle) {
				t.Errorf("coverage table broken for %s (%s): lost %q — the gateway runtime union cache would go stale on this write path",
					entry.file, entry.why, needle)
			}
		}
	}
}
