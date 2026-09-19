package accounts

// w13g dispatch revision family 推进补测：授权实例家族根解析、导出端口
// AdvanceDispatchRevisionFamily 与幂等重放。
//
// 不可达登记（w13g）：
// - batch_effects.go:137-138 recheckRoot != familyRootID 冲突臂：两次读取
//   走同一事务同一连接，行值在单次调用内无法变化（单连接 SQLite 无注入点）。

import (
	"context"
	"strings"
	"testing"
)

func TestW13GAdvanceDispatchFamily(t *testing.T) {
	env, authzStore, store, ownerID, _ := w13gAdvancedAuthorizedEnv(t)
	// w13gSeedTrafficInstance 内部固定使用团队 team-w13g-tm；成员行主键按
	// memberID 唯一，改用独立成员避免与 team-w13g-adv 冲突。
	tmMember := env.login(t, "fam-member-w13g", "fam-pass", "user")
	env.seedProviderAndDefaultGroup(t, tmMember)
	env.seedTeamMember(t, "team-w13g-tm", ownerID, tmMember)
	instanceID := w13gSeedTrafficInstance(t, env, authzStore, ownerID, tmMember, "acc-w13g-fam-src", "f")
	sourceID := "acc-w13g-fam-src"

	ctx := context.Background()
	// 实例侧推进：家族根落到源账户，源 + 实例各推进一次。
	tx, err := env.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.advanceBatchDispatchRevisionFamily(ctx, tx, batchDispatchRevision{
		AccountID: instanceID, TransitionID: "w13g-family-t1", NowMS: 1760000000000,
	}); err != nil {
		t.Fatalf("家族推进失败：%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var sourceRevision, instanceRevision int
	if err := env.db.QueryRow(`SELECT dispatch_revision FROM accounts WHERE id = ?`, sourceID).Scan(&sourceRevision); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT dispatch_revision FROM accounts WHERE id = ?`, instanceID).Scan(&instanceRevision); err != nil {
		t.Fatal(err)
	}
	if sourceRevision != 2 || instanceRevision != 2 {
		t.Fatalf("家族修订应各 +1：%d %d", sourceRevision, instanceRevision)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_circuit_outbox WHERE event_type = 'dispatch_revision_changed'
		AND account_id IN (?, ?)`, sourceID, instanceID) < 2 {
		t.Fatal("源与实例各应写入一条 outbox 事件")
	}

	// 导出端口：owner 侧直接推进（薄封装）。
	tx2, err := env.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceDispatchRevisionFamily(ctx, tx2, sourceID, "w13g-family-t2", 1760000001000); err != nil {
		t.Fatalf("导出端口推进失败：%v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT dispatch_revision FROM accounts WHERE id = ?`, sourceID).Scan(&sourceRevision); err != nil {
		t.Fatal(err)
	}
	if sourceRevision != 3 {
		t.Fatalf("导出端口应推进源账户：%d", sourceRevision)
	}

	// 不存在的账户 → 明确错误。
	tx3, err := env.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.advanceBatchDispatchRevisionFamily(ctx, tx3, batchDispatchRevision{
		AccountID: "acc-w13g-fam-none", TransitionID: "w13g-family-t3", NowMS: 1760000002000,
	}); err == nil || !strings.Contains(err.Error(), "AI 账户不存在") {
		t.Fatalf("缺失账户应报错：%v", err)
	}
	_ = tx3.Rollback()
}
