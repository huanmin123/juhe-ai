package modelcheckowner

// effectiveProxyClient 池/直接档案分支测试（代理池设计 §5.2/§7.5）：J3b 只读
// 解析必须在绑定快照变化时 fail-closed 或按 binding targetID 重取，不得用
// 调用方旧列直连。夹具沿用 businessSourceContractDDL（已含池三表/健康列）。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func seedJ3BProxyProfile(t *testing.T, db *sql.DB, id string, enabled bool) {
	t.Helper()
	envelope := testCredentialEnvelope(t, "secret", `{"password":"proxypass"}`)
	if _, err := db.Exec(`INSERT INTO proxy_profiles
		(id,system_account_id,name,type,host,port,username,password_encrypted,enabled,pool_health_status,pool_health_failure_streak)
		VALUES (?, 'sys-1', ?, 'http', '127.0.0.1', 18080, 'proxyuser', ?, ?, 'healthy', 0)`,
		id, id, envelope, enabled); err != nil {
		t.Fatalf("seed proxy profile: %v", err)
	}
}

func seedJ3BProxyPool(t *testing.T, db *sql.DB, poolID, profileID string, enabled bool) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO proxy_pools (id,system_account_id,name,strategy,enabled,created_at,updated_at)
		VALUES (?, 'sys-1', ?, 'round_robin', ?, now(), now())`, poolID, poolID, enabled); err != nil {
		t.Fatalf("seed proxy pool: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO proxy_pool_members (pool_id,proxy_profile_id,priority,weight,enabled,created_at,updated_at)
		VALUES (?, ?, 0, 1, TRUE, now(), now())`, poolID, profileID); err != nil {
		t.Fatalf("seed proxy pool member: %v", err)
	}
}

// updateJ3BAccountProxy 只改账户代理绑定列；调用方参数保持 NULL 以模拟
// “账户行读取后绑定已变化”的双快照窗口。
func updateJ3BAccountProxy(t *testing.T, db *sql.DB, accountID, setSQL string, args ...any) {
	t.Helper()
	query := "UPDATE accounts SET " + setSQL + " WHERE id = ?"
	if _, err := db.Exec(query, append(args, accountID)...); err != nil {
		t.Fatalf("update account proxy binding: %v", err)
	}
}

func resolveJ3BEffectiveClient(t *testing.T, source *BusinessTargetSource, accountID string) error {
	t.Helper()
	_, err := source.effectiveProxyClient(context.Background(), accountID,
		sql.NullString{}, sql.NullBool{}, sql.NullString{}, sql.NullString{}, sql.NullInt64{}, sql.NullString{}, sql.NullString{})
	return err
}

func TestJ3BEffectiveProxyClientBindingBranches(t *testing.T) {
	db, _ := w14mBusinessDB(t)
	w14mSeedPlainAccount(t, db, "acct-1")
	source := w11eNewSource(t, db)

	// 无绑定：保持既有直连语义（nil client）。
	if err := resolveJ3BEffectiveClient(t, source, "acct-1"); err != nil {
		t.Fatalf("no-binding resolve: %v", err)
	}

	// 直接档案：调用方列为 NULL（旧快照），binding 已指向 p-direct；必须按
	// targetID 重取并返回代理 client，不得直连。
	seedJ3BProxyProfile(t, db, "p-direct", true)
	updateJ3BAccountProxy(t, db, "acct-1", "proxy_profile_id = ?", "p-direct")
	if err := resolveJ3BEffectiveClient(t, source, "acct-1"); err != nil {
		t.Fatalf("direct profile resolve: %v", err)
	}

	// 直接档案停用：fail closed。
	if _, err := db.Exec(`UPDATE proxy_profiles SET enabled = FALSE WHERE id = 'p-direct'`); err != nil {
		t.Fatal(err)
	}
	if err := resolveJ3BEffectiveClient(t, source, "acct-1"); err == nil ||
		!strings.Contains(err.Error(), "proxy profile is unavailable") {
		t.Fatalf("disabled direct profile must fail closed: %v", err)
	}
	if _, err := db.Exec(`UPDATE proxy_profiles SET enabled = TRUE WHERE id = 'p-direct'`); err != nil {
		t.Fatal(err)
	}

	// 池绑定 + 固化租约：只读解析使用租约成员。
	seedJ3BProxyProfile(t, db, "p-pool", true)
	seedJ3BProxyPool(t, db, "pool-1", "p-pool", true)
	if _, err := db.Exec(`INSERT INTO account_proxy_leases
		(account_id,pool_id,proxy_profile_id,assignment_reason,assigned_at,updated_at)
		VALUES ('acct-1','pool-1','p-pool','initial',now(),now())`); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	updateJ3BAccountProxy(t, db, "acct-1", "proxy_profile_id = NULL, proxy_pool_id = ?", "pool-1")
	if err := resolveJ3BEffectiveClient(t, source, "acct-1"); err != nil {
		t.Fatalf("pool lease resolve: %v", err)
	}

	// 池停用：fail closed（只读分支不修复租约、不直连）。
	if _, err := db.Exec(`UPDATE proxy_pools SET enabled = FALSE WHERE id = 'pool-1'`); err != nil {
		t.Fatal(err)
	}
	if err := resolveJ3BEffectiveClient(t, source, "acct-1"); err == nil ||
		!strings.Contains(err.Error(), "proxy pool unavailable") {
		t.Fatalf("disabled pool must fail closed: %v", err)
	}
	if _, err := db.Exec(`UPDATE proxy_pools SET enabled = TRUE WHERE id = 'pool-1'`); err != nil {
		t.Fatal(err)
	}

	// 租约缺失：只读分支不得自选成员，必须报池不可用。
	if _, err := db.Exec(`DELETE FROM account_proxy_leases WHERE account_id = 'acct-1'`); err != nil {
		t.Fatal(err)
	}
	if err := resolveJ3BEffectiveClient(t, source, "acct-1"); err == nil ||
		!strings.Contains(err.Error(), "proxy pool unavailable") {
		t.Fatalf("missing lease must fail closed: %v", err)
	}

	// 双绑定（数据损坏）：binding conflict 优先，不直连。
	updateJ3BAccountProxy(t, db, "acct-1", "proxy_profile_id = ?, proxy_pool_id = ?", "p-direct", "pool-1")
	if err := resolveJ3BEffectiveClient(t, source, "acct-1"); err == nil ||
		!strings.Contains(err.Error(), "proxy target conflict") {
		t.Fatalf("conflicting binding must fail closed: %v", err)
	}
}
