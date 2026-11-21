// v2 升级执行器测试（pgtestkit 真库，进程内直调；基建与 migration_test.go
// 同包同款）。覆盖五域退役终态与 v2 扫描/升级/核验的兼容（上游接口能力设计
// §3；revision 30 清洗删键后，v2 步骤的 Apply/Verify 不得把「落库五域 +
// 凭据无键」判 modes_missing）：五域行不进失败清单也不进重写计划（现域即
// 目标域）、五域源的授权实例继承源落库域、chat 行凭据无键仍判失败阻断升级。

package capabilitydomain

import (
	"context"
	"testing"
	"time"

	capdomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
)

func v2UpgradeOptions() V2UpgradeOptions {
	return V2UpgradeOptions{Secret: testSecret, Now: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
}

// TestV2UpgradeRetiredDomainTerminal 覆盖五域退役终态主路径：m1 → m2 封存 →
// v2 升级（记录 1→2、五域行零重写、实例继承源落库域）→ v2 核验全通过。
func TestV2UpgradeRetiredDomainTerminal(t *testing.T) {
	db := openMigrationDB(t)
	seedFixtures(t, db, []accountFixture{
		{id: "acct-chat", modesJSON: `["chat_json"]`},
		{id: "acct-image", domain: "image"},                // 退役终态：无键
		{id: "acct-embedding", domain: "embedding"},        // 退役终态：无键
		{id: "inst-of-embedding", sourceID: "acct-embedding"}, // 实例跟随五域源
	})
	if _, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM1)); err != nil {
		t.Fatalf("m1: %v", err)
	}
	if _, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM2Cutover)); err != nil {
		t.Fatalf("m2-cutover: %v", err)
	}

	upgrade, err := UpgradeRuleVersionV2(context.Background(), db, v2UpgradeOptions())
	if err != nil {
		t.Fatalf("v2 upgrade: %v", err)
	}
	if !upgrade.Ready() {
		t.Fatalf("v2 升级应通过（五域退役终态不进失败/混合清单）: failures=%+v mixed=%+v", upgrade.Failures, upgrade.MixedDeclarations)
	}
	// 现行共享包 RuleVersion=2：m2 封存记录直接落 ruleVersion=2，v2 入口走
	// 「已升级跳过」分支（RecordUpgraded=false 是正确幂等语义）。
	if upgrade.Record == nil || upgrade.Record.RuleVersion != 2 || upgrade.Record.Stage != capdomain.StageM2Finalized {
		t.Fatalf("阶段记录应为 ruleVersion=2 且 m2_finalized: %+v", upgrade.Record)
	}
	if upgrade.ReassignedRows != 0 {
		t.Fatalf("五域退役行现域即目标域，不得产生重写: %+v", upgrade.Reassigned)
	}
	if got := accountDomain(t, db, "inst-of-embedding"); got != "embedding" {
		t.Fatalf("实例应继承五域源落库域 embedding，got %q", got)
	}

	// 旧记录通道：把阶段记录改写回 ruleVersion=1（模拟 rev29 之前的历史库），
	// v2 升级应经专用通道升到 2 且五域退役终态同样零失败。
	if _, err := db.Exec(`UPDATE "juhe_business"."global_settings" SET value_json = '{"stage":"m2_finalized","ruleVersion":1,"updatedAt":"2026-10-01T00:00:00Z"}' WHERE key = $1`, capdomain.MigrationSettingKey); err != nil {
		t.Fatalf("rewrite record to v1: %v", err)
	}
	legacy, err := UpgradeRuleVersionV2(context.Background(), db, v2UpgradeOptions())
	if err != nil {
		t.Fatalf("v2 upgrade (legacy record): %v", err)
	}
	if !legacy.Ready() || !legacy.RecordUpgraded {
		t.Fatalf("ruleVersion 1→2 专用通道应升级成功且零失败: failures=%+v mixed=%+v", legacy.Failures, legacy.MixedDeclarations)
	}
	if legacy.ReassignedRows != 0 {
		t.Fatalf("旧记录通道五域退役行同样不得产生重写: %+v", legacy.Reassigned)
	}

	verify, err := VerifyRuleVersionV2(context.Background(), db, v2UpgradeOptions())
	if err != nil {
		t.Fatalf("v2 verify: %v", err)
	}
	if !verify.Ready() {
		t.Fatalf("v2 核验应通过: failures=%+v mixed=%+v", verify.Failures, verify.MixedDeclarations)
	}
	if verify.PendingRows != 0 {
		t.Fatalf("v2 核验 pending 应为 0: %+v", verify)
	}
}

// TestV2UpgradeChatMissingModesStillFails 钉住豁免不外溢：封存后 chat 行凭据
// 被去掉 supported_endpoint_modes 键，v2 升级仍判 modes_missing（chat 声明
// 契约保留现状）。
func TestV2UpgradeChatMissingModesStillFails(t *testing.T) {
	db := openMigrationDB(t)
	seedFixtures(t, db, []accountFixture{
		{id: "acct-chat", modesJSON: `["chat_json"]`},
	})
	if _, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM1)); err != nil {
		t.Fatalf("m1: %v", err)
	}
	if _, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM2Cutover)); err != nil {
		t.Fatalf("m2-cutover: %v", err)
	}
	// 窗口期篡改：chat 凭据删键（模拟 chat 声明丢失）。
	if _, err := db.Exec(`UPDATE "juhe_business"."accounts" SET credentials_encrypted = $1 WHERE id = 'acct-chat'`,
		fixtureCredentials(t, accountFixture{id: "acct-chat"})); err != nil {
		t.Fatalf("strip modes: %v", err)
	}
	upgrade, err := UpgradeRuleVersionV2(context.Background(), db, v2UpgradeOptions())
	if err != nil {
		t.Fatalf("v2 upgrade: %v", err)
	}
	if upgrade.Ready() {
		t.Fatalf("chat 凭据无键必须仍判 modes_missing: %+v", upgrade)
	}
	found := false
	for _, failure := range upgrade.Failures {
		if failure.AccountID == "acct-chat" && failure.Kind == capdomain.FailureModesMissing {
			found = true
		}
	}
	if !found {
		t.Fatalf("failures 缺少 acct-chat(modes_missing): %+v", upgrade.Failures)
	}
}
