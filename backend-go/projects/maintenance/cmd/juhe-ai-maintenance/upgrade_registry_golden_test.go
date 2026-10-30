package main

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// 升级步骤注册表 golden（升级兼容与迁移账本设计 §7.4/§8）：钉死
// (revision, step_id, implVersion) 有序对。任何变化（增删步骤、改 ID、提
// ImplVersion）都必须显式更新本文件并在同批递增 contracts.SchemaRevision。
func TestUpgradeStepsRegistryGolden(t *testing.T) {
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
		{7, "chat-account-only-binding", 1},
		{8, "upstream-base-url-version-root", 1},
		{9, "success-cost-backfill", 1},
		{10, "studio-retirement", 1},
		{11, "drop-retired-monitoring-tables", 1},
		{12, "account-default-resources", 1},
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
	if int64(want[len(want)-1].revision) != contracts.SchemaRevision {
		t.Fatalf("最高 revision %d 与 contracts.SchemaRevision %d 不一致（§6.4 同一序号空间）",
			want[len(want)-1].revision, contracts.SchemaRevision)
	}
	// 破坏性步骤标记与设计 §10 一致。
	destructive := map[string]bool{}
	for _, step := range steps {
		destructive[step.ID] = step.Destructive
	}
	for _, id := range []string{"chat-account-only-binding", "studio-retirement", "drop-retired-monitoring-tables"} {
		if !destructive[id] {
			t.Fatalf("步骤 %s 应标记 destructive", id)
		}
	}
	if destructive["capability-domain"] || destructive["scheduling-preferences"] {
		t.Fatal("capability-domain/scheduling-preferences 不应标记 destructive")
	}
}

func TestUpgradeStepsChecksumStable(t *testing.T) {
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
