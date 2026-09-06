package oauthrefresh

import (
	"context"
	"database/sql"
	"testing"
)

// seedInstanceAccount 建一个授权实例账户（可标 deleted/provider 验证过滤）。
func seedInstanceAccount(t *testing.T, db *sql.DB, id, providerCode, accountType, sourceAccountID, deletedAt string) {
	t.Helper()
	if accountType == "" {
		accountType = "oauth"
	}
	// 空串渲染为 NULL（活跃账户 deleted_at IS NULL 才能命中 fanout 查询）。
	var deletedArg any
	if deletedAt != "" {
		deletedArg = deletedAt
	}
	_, err := db.Exec(`INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted,
		config_revision, dispatch_revision, authorization_instance_source_account_id, deleted_at, updated_at)
		VALUES (?, 'sys_admin', ?, 'profile_gpt_openai_v1', 'openai', 'v1', ?, ?, 'active', 'enc', 3, 5, ?, ?, '')`,
		id, providerCode, id, accountType, sourceAccountID, deletedArg)
	if err != nil {
		t.Fatal(err)
	}
}

func countOutboxRows(t *testing.T, db *sql.DB) (int, string, int) {
	t.Helper()
	var count int
	var reason, eventKind string
	var version int
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(reason), ''), COALESCE(MAX(event_kind), ''), COALESCE(MAX(input_version), 0)
		FROM account_health_jobs_input_outbox`).Scan(&count, &reason, &eventKind, &version); err != nil {
		t.Fatal(err)
	}
	return count, reason + "|" + eventKind, version
}

func TestEnqueueAccountHealthInputsSkipsNonAccountResources(t *testing.T) {
	store, db, _ := newSweepStore(t)
	seedInstanceAccount(t, db, "inst-1", "openai", "oauth", "res-1", "")
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	enqueued, err := store.EnqueueAccountHealthInputsForAuthorizationSourceTx(ctx, tx, "group", "res-1", AuthorizationGrantHealthFanoutReason)
	if err != nil {
		t.Fatal(err)
	}
	if enqueued != 0 {
		t.Fatalf("group 资源不应 fanout：%d", enqueued)
	}
	if count, _, _ := countOutboxRows(t, db); count != 0 {
		t.Fatalf("group 资源不应写 outbox：%d", count)
	}
}

func TestEnqueueAccountHealthInputsFanoutWhitelistAndVersions(t *testing.T) {
	store, db, _ := newSweepStore(t)
	seedInstanceAccount(t, db, "inst-a", "openai", "oauth", "res-1", "")
	seedInstanceAccount(t, db, "inst-b", "anthropic", "api_key", "res-1", "")
	// 排除路径：已删除实例、白名单外 provider、白名单外 type、无来源账户。
	seedInstanceAccount(t, db, "inst-deleted", "openai", "oauth", "res-1", "2026-01-01T00:00:00.000Z")
	seedInstanceAccount(t, db, "inst-provider", "unknown_provider", "oauth", "res-1", "")
	seedInstanceAccount(t, db, "inst-type", "openai", "completion", "res-1", "")
	seedInstanceAccount(t, db, "inst-other", "openai", "oauth", "res-2", "")

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	enqueued, err := store.EnqueueAccountHealthInputsForAuthorizationSourceTx(ctx, tx, "account", "res-1", AuthorizationGrantHealthFanoutReason)
	if err != nil {
		t.Fatal(err)
	}
	if enqueued != 2 {
		t.Fatalf("白名单过滤后应入队 2，得到 %d", enqueued)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	count, kindReason, version := countOutboxRows(t, db)
	if count != 2 {
		t.Fatalf("outbox 行数 = %d, want 2", count)
	}
	if kindReason != "authorization_grant_changed|snapshot" {
		t.Fatalf("reason|kind = %q", kindReason)
	}
	if version != 1 {
		t.Fatalf("首次入队版本 = %d, want 1", version)
	}

	// 二轮 fanout：同账户版本自增（Node reserveAccountHealthJobsInputVersion
	// 预留语义），UNIQUE(account_id, input_version) 不冲突。
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback()
	if _, err := store.EnqueueAccountHealthInputsForAuthorizationSourceTx(ctx, tx2, "account", "res-1", AuthorizationGrantHealthFanoutReason); err != nil {
		t.Fatal(err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}
	count, _, version = countOutboxRows(t, db)
	if count != 4 || version != 2 {
		t.Fatalf("二轮后 outbox=%d maxVersion=%d, want 4/2", count, version)
	}
}

func TestSweepRunsHealthFanoutInsideFinalizer(t *testing.T) {
	store, db, clock := newSweepStore(t)
	seedInstanceAccount(t, db, "inst-a", "openai", "oauth", "acc-src", "")
	// 过期的 account 授权 grant。
	if _, err := db.Exec(`INSERT INTO resource_authorization_grants (id, resource_type, resource_id, owner_system_account_id, grantee_type, grantee_id,
		status, revoked_at, revoked_by, created_by, expires_at, updated_at)
		VALUES ('g-fan', 'account', 'acc-src', 'owner', 'system_account', 'grantee', 'active', NULL, '', 'creator', ?, ?)`,
		isoMillis(clock.time().Add(-timeHour)), isoMillis(defaultNow())); err != nil {
		t.Fatal(err)
	}
	finalizer := authorizationGrantFinalizerForTest(store)
	result, err := store.RunAuthorizationExpirySweep(context.Background(), finalizer, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Expired != 1 {
		t.Fatalf("expired=%d want 1", result.Expired)
	}
	assertGrantStatus(t, db, "g-fan", "expired")
	count, kindReason, _ := countOutboxRows(t, db)
	if count != 1 || kindReason != "authorization_grant_changed|snapshot" {
		t.Fatalf("sweep 事务内 fanout 缺失：count=%d kind=%q", count, kindReason)
	}
	// finalizer 写入失败（版本表缺失）必须使 sweep 本轮失败——Node in-tx
	// 语义：下游副作用写入失败即整个 sweep 事务回滚。
	if _, err := db.Exec(`INSERT INTO resource_authorization_grants (id, resource_type, resource_id, owner_system_account_id, grantee_type, grantee_id,
		status, revoked_at, revoked_by, created_by, expires_at, updated_at)
		VALUES ('g-fan-2', 'account', 'acc-src', 'owner', 'system_account', 'grantee', 'active', NULL, '', 'creator', ?, ?)`,
		isoMillis(clock.time().Add(-timeHour)), isoMillis(defaultNow())); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE account_health_jobs_input_versions`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RunAuthorizationExpirySweep(context.Background(), finalizer, 0); err == nil {
		t.Fatal("fanout 失败必须使 sweep 本轮失败")
	}
}

// authorizationGrantFinalizerForTest 组装与组合根一致的 fanout finalizer。
func authorizationGrantFinalizerForTest(store *Store) GrantFinalizer {
	return FinalizerFunc(func(ctx context.Context, tx *sql.Tx, grant ResourceAuthorizationGrant, _ string) error {
		_, err := store.EnqueueAccountHealthInputsForAuthorizationSourceTx(ctx, tx, grant.ResourceType, grant.ResourceID, AuthorizationGrantHealthFanoutReason)
		return err
	})
}
