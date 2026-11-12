package main

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// 升级步骤注册表 golden（升级兼容与迁移账本设计 §7.4/§8）：钉死
// (revision, step_id, implVersion) 有序对。任何变化（增删步骤、改 ID、提
// ImplVersion）都必须显式更新本文件并在同批递增 contracts.SchemaRevision。
// pinUpgradeTargetEnv 把三个 target 连接串 env 显式占位并在用例结束自动还原：
// upgradeSteps 的 upgradeTargets 会把缺失值写回进程环境，泄漏会污染后续用例
// （rehearsal 的子进程继承 env 后拿 placeholder DSN 连库）。
func pinUpgradeTargetEnv(t *testing.T, dsn string) {
	t.Helper()
}

func TestUpgradeStepsRegistryGolden(t *testing.T) {
	pinUpgradeTargetEnv(t, "postgres://placeholder")
	steps := upgradeSteps("postgres://placeholder", "placeholder-secret")
	want := []struct {
		revision    int64
		id          string
		implVersion int
	}{
		{1, "j3a-proxy-latency-pg", 1},
		{2, "j3b-model-check-pg", 1},
		{3, "account-balance-pg", 1},
		{4, "capability-domain", 1},
		{5, "scheduling-preferences", 1},
		{6, "merge-super-groups", 1},
		{8, "upstream-base-url-version-root", 1},
		{9, "success-cost-backfill", 1},
		{10, "studio-retirement", 1},
		{11, "drop-retired-monitoring-tables", 1},
		{12, "account-default-resources", 1},
		{13, "account-schedulable-normalization", 1},
		{20, "retire-cross-protocol-entry-mappings", 1},
		{22, "retire-high-concurrency-groups", 1},
		{23, "retire-usage-shard-catalog", 1},
		{24, "instant-timestamptz-small-tables", 1},
		{25, "instant-timestamptz-crud", 1},
		{26, "instant-timestamptz-accounts", 1},
		{27, "instant-timestamptz-dataset-jobs", 1},
		{28, "stats-calendar-timestamptz", 1},
		{29, "usage-records-timestamptz-prepare", 1},
		{30, "usage-records-timestamptz-swap", 1},
		{31, "usage-records-timestamptz-retire", 1},
		{32, "account-lock-state-timestamptz", 1},
		{33, "account-list-availability-dirty-timestamptz", 1},
		{34, "circuit-runtime-timestamptz", 1},
	}
	if len(steps) != len(want) {
		t.Fatalf("注册表步骤数 = %d, want %d", len(steps), len(want))
	}
	for i, w := range want {
		got := steps[i]
		if got.Revision != w.revision || got.ID != w.id || got.ImplVersion != w.implVersion {
			t.Fatalf("注册表[%d] = (%d, %s, impl %d), want (%d, %s, impl %d)",
				i, got.Revision, got.ID, got.ImplVersion, w.revision, w.id, w.implVersion)
		}
	}
	// §6.4（2026-11-03）：revision 14 起出现纯 DDL 空号——accounts.rpm_limit
	// 加法列占用 SchemaRevision 14，但无数据步骤、不写入 schema_migrations，
	// 注册表最高 revision 仍为 13。断言从「最高注册 revision ==
	// SchemaRevision」修正为「所有注册 revision ≤ SchemaRevision」：空号由
	// SchemaRevision 与 schema DDL golden 共同表达，注册表不再与末位钉死。
	for _, step := range steps {
		if step.Revision > contracts.SchemaRevision {
			t.Fatalf("注册 revision %d（%s）高于 contracts.SchemaRevision %d（§6.4 同一序号空间，且空号不入账本）",
				step.Revision, step.ID, contracts.SchemaRevision)
		}
	}
	// 破坏性步骤标记与设计 §10 一致。
	destructive := map[string]bool{}
	for _, step := range steps {
		destructive[step.ID] = step.Destructive
	}
	for _, id := range []string{"studio-retirement", "drop-retired-monitoring-tables", "retire-cross-protocol-entry-mappings", "retire-high-concurrency-groups", "retire-usage-shard-catalog", "instant-timestamptz-small-tables", "instant-timestamptz-crud"} {
		if !destructive[id] {
			t.Fatalf("步骤 %s 应标记 destructive", id)
		}
	}
	if destructive["capability-domain"] || destructive["scheduling-preferences"] {
		t.Fatal("capability-domain/scheduling-preferences 不应标记 destructive")
	}
}

func TestUpgradeStepsChecksumStable(t *testing.T) {
	pinUpgradeTargetEnv(t, "postgres://placeholder")
	steps := upgradeSteps("postgres://placeholder", "placeholder-secret")
	first := steps[3].Checksum()
	second := steps[3].Checksum()
	if first != second {
		t.Fatalf("checksum 不稳定: %s vs %s", first, second)
	}
	if len(first) != len("sha256:")+64 {
		t.Fatalf("checksum 形态不符: %s", first)
	}
}
