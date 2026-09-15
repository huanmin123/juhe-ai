package accountcleanup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// w7aCleanupStore 与 cleanupTestStore 等价，但 resource_authorizations 补齐
// 生产契约列 resource_owner_system_account_id（既有夹具缺列，实例分支 join 需要）。
func w7aCleanupStore(t *testing.T, gate OwnerGate) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:w7a-cleanup-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE accounts (
			id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL,
			authorization_instance_authorization_id TEXT,
			authorization_instance_source_account_id TEXT,
			deleted_at TEXT, deleted_by TEXT, updated_at TEXT NOT NULL, created_at TEXT NOT NULL,
			status TEXT NOT NULL, schedulable INTEGER NOT NULL, cooldown_until TEXT
		)`,
		`CREATE TABLE resource_authorizations (
			id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL,
			resource_owner_system_account_id TEXT,
			grantee_system_account_id TEXT, status TEXT NOT NULL,
			effective_source_type TEXT, effective_source_team_id TEXT,
			revoked_by TEXT, revoked_at TEXT, revoked_reason TEXT,
			last_source_changed_at TEXT, updated_at TEXT
		)`,
		`CREATE TABLE resource_authorization_sources (
			authorization_id TEXT NOT NULL, source_type TEXT NOT NULL, source_team_id TEXT,
			status TEXT NOT NULL, ended_at TEXT, ended_reason TEXT,
			revoked_by TEXT, revoked_at TEXT, updated_at TEXT
		)`,
		`CREATE TABLE resource_authorization_grants (
			id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL,
			resource_owner_system_account_id TEXT, grantee_type TEXT,
			grantee_system_account_id TEXT, status TEXT NOT NULL,
			revoked_by TEXT, revoked_at TEXT, updated_at TEXT
		)`,
		`CREATE TABLE group_accounts (account_id TEXT, account_authorization_id TEXT)`,
		`CREATE TABLE request_quota_hourly_window_scope_bindings (
			source_type TEXT, source_id TEXT, scope_type TEXT, scope_id TEXT
		)`,
		`CREATE TABLE account_supported_models (account_id TEXT)`,
		`CREATE TABLE account_model_mappings (account_id TEXT)`,
		`CREATE TABLE account_tag_bindings (account_id TEXT)`,
		`CREATE TABLE account_name_search_terms (account_id TEXT)`,
		`CREATE TABLE account_name_search_documents (account_id TEXT)`,
		`CREATE TABLE account_api_key_runtime_states (account_id TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	store, err := New(db, SQLite, "", gate)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC) }
	return store, db
}

// ---------------------------------------------------------------------------
// 构造函数、别名与契约
// ---------------------------------------------------------------------------

func TestW7AConstructorArmsAndAliases(t *testing.T) {
	if _, err := New(nil, SQLite, "", OwnerGate{}); err == nil {
		t.Fatal("nil db must be rejected")
	}
	if _, err := NewStore(nil, SQLite, "", OwnerGate{}); err == nil {
		t.Fatal("nil db store must be rejected")
	}
	db0, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db0.Close()
	if _, err := NewStore(db0, "mysql", "", OwnerGate{}); !errors.Is(err, ErrInvalidMode) {
		t.Fatalf("mode=%v", err)
	}
	store, db := w7aCleanupStore(t, OwnerGate{})
	// PostgreSQL 默认 schema 合法（不触库）。
	pg, err := New(db, Postgres, "", OwnerGate{})
	if err != nil || pg.schema != "juhe_business" {
		t.Fatalf("pg schema=%q err=%v", pg.schema, err)
	}
	result, err := store.CleanupExpiredDeletedAccounts(context.Background(), CleanupInput{})
	if !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("alias gate=%v", err)
	}
	if result.CutoffDeletedAt != "" {
		t.Fatalf("result=%+v", result)
	}
}

func TestW7ACheckContractMissingRelation(t *testing.T) {
	store, db := w7aCleanupStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if _, err := db.Exec(`DROP TABLE account_name_search_documents`); err != nil {
		t.Fatal(err)
	}
	err := store.CheckContract(context.Background())
	if err == nil || !strings.Contains(err.Error(), "account_name_search_documents") {
		t.Fatalf("contract err=%v", err)
	}
}

func TestW7AContextCancelledBeforeCleanup(t *testing.T) {
	store, _ := w7aCleanupStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Cleanup(ctx, CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z"}); err == nil {
		t.Fatal("cancelled context must abort cleanup")
	}
}

// ---------------------------------------------------------------------------
// 记录围栏分支
// ---------------------------------------------------------------------------

func TestW7AFenceReaderErrorAndGarbageStatus(t *testing.T) {
	t.Run("reader-error", func(t *testing.T) {
		store, db := w7aCleanupStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
		seedW7ADeletedRoot(t, db)
		result, err := store.Cleanup(context.Background(), CleanupInput{
			CutoffDeletedAt: "2026-02-01T00:00:00.000Z",
			RecordFence: RecordFenceReaderFunc(func(context.Context, CleanupTarget) (RecordFence, error) {
				return RecordFence{}, w7aErrBoom
			}),
		})
		if err != nil || result.Failed != 1 || len(result.Failures) != 1 || len(result.RecordCleanupTargets) != 1 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if result.Failures[0].Stage != "read_record_fence" || !strings.Contains(result.Failures[0].Error, w7aErrBoom.Error()) {
			t.Fatalf("failures=%+v", result.Failures)
		}
	})
	t.Run("garbage-status", func(t *testing.T) {
		store, db := w7aCleanupStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
		seedW7ADeletedRoot(t, db)
		result, err := store.Cleanup(context.Background(), CleanupInput{
			CutoffDeletedAt: "2026-02-01T00:00:00.000Z",
			RecordFence: RecordFenceReaderFunc(func(context.Context, CleanupTarget) (RecordFence, error) {
				return RecordFence{Status: RecordFenceStatus("bogus"), Token: "t"}, nil
			}),
		})
		if err != nil || result.Failed != 1 || !strings.Contains(result.Failures[0].Error, ErrInvalidFence.Error()) {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	// 未配置读取器时的 nil-func 防御臂。
	reader := RecordFenceReaderFunc(nil)
	fence, err := reader.ReadRecordFence(context.Background(), CleanupTarget{})
	if err != nil || fence.Status != RecordFenceUnknown {
		t.Fatalf("nil reader fence=%+v err=%v", fence, err)
	}
}

// 围栏读取器中途取消上下文：下一个候选必须以 ctx 错误终止整个清理。
func TestW7AContextCancelledMidIteration(t *testing.T) {
	store, db := w7aCleanupStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	seedW7ADeletedRoot(t, db)
	seedW7ADeletedRoot(t, db, "root2", "child2")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := store.Cleanup(ctx, CleanupInput{
		CutoffDeletedAt: "2026-02-01T00:00:00.000Z",
		RecordFence: RecordFenceReaderFunc(func(context.Context, CleanupTarget) (RecordFence, error) {
			cancel()
			return RecordFence{Status: RecordFenceCleared, Token: "t"}, nil
		}),
	})
	if err == nil {
		t.Fatal("mid-iteration cancellation must abort cleanup")
	}
}

// ---------------------------------------------------------------------------
// 孤儿实例：账户型授权资源撤销全链路
// ---------------------------------------------------------------------------

func TestW7AOrphanRevokesAccountAuthorizationChain(t *testing.T) {
	store, db := w7aCleanupStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	old := "2026-08-01T00:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('orphan','sys','auth-o',NULL,NULL,?,?,'active',1),('target','sys',NULL,NULL,?,?,?,'disabled',0)`, old, old, old, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations(id,resource_type,resource_id,resource_owner_system_account_id,grantee_system_account_id,status)
		VALUES ('auth-o','account','target','sys','grantee','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorization_sources(authorization_id,source_type,source_team_id,status) VALUES ('auth-o','manual',NULL,'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorization_grants(id,resource_type,resource_id,resource_owner_system_account_id,grantee_type,grantee_system_account_id,status)
		VALUES ('grant-t','account','target','sys','system_account','grantee','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO request_quota_hourly_window_scope_bindings(source_type,source_id,scope_type,scope_id) VALUES ('resource_authorization_grant','grant-t','account_authorization','auth-o')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,account_authorization_id) VALUES (NULL,'auth-o')`); err != nil {
		t.Fatal(err)
	}
	result, err := store.Cleanup(context.Background(), CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z"})
	if err != nil || result.OrphanedAuthorizationInstances != 1 || result.Attempted != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM resource_authorizations WHERE id='auth-o'`).Scan(&status); err != nil || status != "revoked" {
		t.Fatalf("auth status=%s err=%v", status, err)
	}
	var grantStatus string
	if err := db.QueryRow(`SELECT status FROM resource_authorization_grants WHERE id='grant-t'`).Scan(&grantStatus); err != nil || grantStatus != "revoked" {
		t.Fatalf("grant status=%s err=%v", grantStatus, err)
	}
	var bindings int
	if err := db.QueryRow(`SELECT COUNT(*) FROM request_quota_hourly_window_scope_bindings`).Scan(&bindings); err != nil || bindings != 0 {
		t.Fatalf("quota bindings=%d err=%v", bindings, err)
	}
	var groups int
	if err := db.QueryRow(`SELECT COUNT(*) FROM group_accounts`).Scan(&groups); err != nil || groups != 0 {
		t.Fatalf("group bindings=%d err=%v", groups, err)
	}
}

// ---------------------------------------------------------------------------
// softDeleteOrphan 直测防御臂
// ---------------------------------------------------------------------------

func TestW7ASoftDeleteOrphanGuardArms(t *testing.T) {
	store, db := w7aCleanupStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	old := "2026-08-01T00:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('gone','sys','auth-g',NULL,?,?,?,'disabled',0)`, old, old, old); err != nil {
		t.Fatal(err)
	}
	// auth 无效：直接跳过。
	changed, err := store.softDeleteOrphan(ctx, accountRow{ID: "x"})
	if err != nil || changed {
		t.Fatalf("invalid auth changed=%v err=%v", changed, err)
	}
	// 行不存在：ErrNoRows 静默跳过。
	changed, err = store.softDeleteOrphan(ctx, accountRow{ID: "ghost", SystemAccountID: "sys", AuthorizationInstanceAuthID: nullStringV("auth-g")})
	if err != nil || changed {
		t.Fatalf("missing row changed=%v err=%v", changed, err)
	}
	// 已删除：幂等跳过。
	changed, err = store.softDeleteOrphan(ctx, accountRow{ID: "gone", SystemAccountID: "sys", AuthorizationInstanceAuthID: nullStringV("auth-g"), DeletedAt: nullStringV(old), UpdatedAt: nullStringV(old)})
	if err != nil || changed {
		t.Fatalf("deleted row changed=%v err=%v", changed, err)
	}
	// updated_at 变化：CAS 失败。
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('stale','sys','auth-s',NULL,?,?,'active',0)`, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.softDeleteOrphan(ctx, accountRow{ID: "stale", SystemAccountID: "sys", AuthorizationInstanceAuthID: nullStringV("auth-s"), UpdatedAt: nullStringV("2026-08-02T00:00:00.000Z")}); !errors.Is(err, ErrCAS) {
		t.Fatalf("stale CAS=%v", err)
	}
}

// ---------------------------------------------------------------------------
// 实例候选与活跃实例过滤
// ---------------------------------------------------------------------------

func TestW7AInstanceCandidateFullDelete(t *testing.T) {
	store, db := w7aCleanupStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	// 父账户存活、子实例已删除：实例分支候选可整体清除。
	old := "2026-01-05T00:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('parent','sys',NULL,NULL,NULL,?,?,'active',1),('child','sys','auth-c','parent',?,?,?,'disabled',0)`, old, old, old, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations(id,resource_type,resource_id,resource_owner_system_account_id,grantee_system_account_id,status) VALUES ('auth-c','account','parent','sys','grantee','revoked')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorization_sources(authorization_id,source_type,source_team_id,status) VALUES ('auth-c','manual','team-9','revoked'),('auth-c','manual','team-1','revoked')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorization_grants(id,resource_type,resource_id,resource_owner_system_account_id,grantee_type,grantee_system_account_id,status)
		VALUES ('grant-c','account','parent','sys','system_account','grantee','active')`); err != nil {
		t.Fatal(err)
	}
	result, err := store.Cleanup(context.Background(), CleanupInput{
		CutoffDeletedAt: "2026-02-01T00:00:00.000Z",
		RecordFence: RecordFenceReaderFunc(func(context.Context, CleanupTarget) (RecordFence, error) {
			return RecordFence{Status: RecordFenceCleared, Token: "t"}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Completed != 1 || result.PhysicallyDeletedAccounts != 1 || result.PhysicallyDeletedAuthorizations != 1 || result.PhysicallyDeletedGrants != 1 {
		t.Fatalf("instance result=%+v", result)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("remaining accounts=%d err=%v", count, err)
	}
	var grants int
	if err := db.QueryRow(`SELECT COUNT(*) FROM resource_authorization_grants`).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("grants=%d err=%v", grants, err)
	}
}

// 晚于 cutoff 删除的实例必须让根候选以 business_cas 失败，而不是越界误删。
func TestW7ALateSiblingBlocksRootDelete(t *testing.T) {
	store, db := w7aCleanupStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	rootOld := "2026-01-05T00:00:00.000Z"
	kidLate := "2026-03-01T00:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('root','sys',NULL,NULL,?,?,?,'disabled',0),('kid','sys','auth-k','root',?,?,?,'disabled',0)`, rootOld, rootOld, rootOld, kidLate, kidLate, kidLate); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations(id,resource_type,resource_id,resource_owner_system_account_id,grantee_system_account_id,status) VALUES ('auth-k','account','root','sys','grantee','revoked')`); err != nil {
		t.Fatal(err)
	}
	result, err := store.Cleanup(context.Background(), CleanupInput{
		CutoffDeletedAt: "2026-02-01T00:00:00.000Z",
		RecordFence: RecordFenceReaderFunc(func(context.Context, CleanupTarget) (RecordFence, error) {
			return RecordFence{Status: RecordFenceCleared, Token: "t"}, nil
		}),
	})
	if err != nil || result.Failed != 1 || len(result.Failures) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Failures[0].Stage != "business_cas" {
		t.Fatalf("failures=%+v", result.Failures)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("accounts=%d err=%v", count, err)
	}
}

// ---------------------------------------------------------------------------
// 删除阶段错误与直测防御臂
// ---------------------------------------------------------------------------

func TestW7ADeleteBusinessInvalidFenceAndListError(t *testing.T) {
	store, db := w7aCleanupStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	// 空候选直接拒绝。
	if _, err := store.deleteBusiness(ctx, candidate{row: accountRow{}}, "2026-02-01T00:00:00.000Z"); !errors.Is(err, ErrInvalidFence) {
		t.Fatalf("invalid fence=%v", err)
	}
	// 列表查询失败必须包装上抛。
	if _, err := db.Exec(`DROP TABLE accounts`); err != nil {
		t.Fatal(err)
	}
	_, err := store.Cleanup(ctx, CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z"})
	if err == nil || !strings.Contains(err.Error(), "list orphaned authorization instances") {
		t.Fatalf("list error=%v", err)
	}
}

func TestW7AHelperArms(t *testing.T) {
	if got := unique([]string{" a ", "a", "", "b"}); len(got) != 2 || got[0] != "a" {
		t.Fatalf("unique=%v", got)
	}
	if got := appendUnique([]string{"x"}, "x"); len(got) != 1 {
		t.Fatal("appendUnique duplicate")
	}
	if got := appendUnique([]string{"x"}, " "); len(got) != 1 {
		t.Fatal("appendUnique blank")
	}
	if got := chunks([]string{"a", "b", "c"}, 2); len(got) != 2 || len(got[0]) != 2 || len(got[1]) != 1 {
		t.Fatalf("chunks=%v", got)
	}
	if got := chunks(nil, 0); got != nil {
		t.Fatalf("chunks nil=%v", got)
	}
	if placeholders(0) != "NULL" || len(placeholders(3)) != 5 {
		t.Fatal("placeholders arms")
	}
	if !sameNullString(nullStringV("a"), nullStringV("a")) || sameNullString(nullStringV("a"), nullStringV("b")) ||
		sameNullString(nullStringV("a"), sql.NullString{}) || !sameNullString(sql.NullString{}, sql.NullString{}) {
		t.Fatal("sameNullString arms")
	}
	if got := mapKeys(map[string]string{"k": "v"}); len(got) != 1 {
		t.Fatalf("mapKeys=%v", got)
	}
	store, _ := w7aCleanupStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if got := store.bind("a=? b=?"); got != "a=? b=?" {
		t.Fatal("sqlite bind changed")
	}
	if got := store.table("accounts"); got != "accounts" {
		t.Fatal("sqlite table changed")
	}
}

// seedW7ADeletedRoot 额外播种一套已删除根 + 已删除实例（含授权与来源）。
func seedW7ADeletedRoot(t *testing.T, db *sql.DB, ids ...string) {
	t.Helper()
	rootID, childID := "root", "child"
	if len(ids) >= 1 {
		rootID = ids[0]
	}
	if len(ids) >= 2 {
		childID = ids[1]
	}
	old := "2026-01-05T00:00:00.000Z"
	if _, err := db.Exec(fmt.Sprintf(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('%s','sys',NULL,NULL,?,?,?, 'disabled',0),('%s','sys','auth-%s','%s',?,?,?, 'disabled',0)`, rootID, childID, childID, rootID), old, old, old, old, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`INSERT INTO resource_authorizations(id,resource_type,resource_id,grantee_system_account_id,status) VALUES ('auth-%s','account','%s','grantee','revoked')`, childID, rootID)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`INSERT INTO resource_authorization_sources(authorization_id,source_type,source_team_id,status) VALUES ('auth-%s','manual','team-%s','revoked')`, childID, childID)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`INSERT INTO group_accounts(account_id,account_authorization_id) VALUES ('%s',NULL),(NULL,'auth-%s')`, rootID, childID)); err != nil {
		t.Fatal(err)
	}
}

func nullStringV(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

var w7aErrBoom = errors.New("w7a boom")

// ---------------------------------------------------------------------------
// drop 注入：buildCandidate / revokeInstance / deleteBusiness 错误臂
// ---------------------------------------------------------------------------

func TestW7ADefaultCutoffAndNilContractArms(t *testing.T) {
	store, db := w7aCleanupStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	result, err := store.Cleanup(context.Background(), CleanupInput{})
	if err != nil {
		t.Fatal(err)
	}
	want := store.now().UTC().AddDate(0, -1, 0).Format(nodeTimeLayout)
	if result.CutoffDeletedAt != want {
		t.Fatalf("cutoff=%s want=%s", result.CutoffDeletedAt, want)
	}
	if err := (&Store{}).CheckContract(context.Background()); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("nil db contract=%v", err)
	}
	// limit 正好等于根候选数：实例分支查询被跳过。
	seedW7ADeletedRoot(t, db)
	limited, err := store.Cleanup(context.Background(), CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z", Limit: 1})
	if err != nil || limited.Attempted != 1 {
		t.Fatalf("limited result=%+v err=%v", limited, err)
	}
}

func TestW7ABuildTargetFailureStages(t *testing.T) {
	ctx := context.Background()
	t.Run("team-sources", func(t *testing.T) {
		store, db := w7aCleanupStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
		seedW7ADeletedRoot(t, db)
		if _, err := db.Exec(`DROP TABLE resource_authorization_sources`); err != nil {
			t.Fatal(err)
		}
		result, err := store.Cleanup(ctx, CleanupInput{
			CutoffDeletedAt: "2026-02-01T00:00:00.000Z",
			RecordFence: RecordFenceReaderFunc(func(context.Context, CleanupTarget) (RecordFence, error) {
				return RecordFence{Status: RecordFenceCleared, Token: "t"}, nil
			}),
		})
		if err != nil || result.Failed != 1 || result.Failures[0].Stage != "build_target" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("grants-non-instance", func(t *testing.T) {
		store, db := w7aCleanupStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
		seedW7ADeletedRoot(t, db)
		if _, err := db.Exec(`DROP TABLE resource_authorization_grants`); err != nil {
			t.Fatal(err)
		}
		result, err := store.Cleanup(ctx, CleanupInput{
			CutoffDeletedAt: "2026-02-01T00:00:00.000Z",
			RecordFence: RecordFenceReaderFunc(func(context.Context, CleanupTarget) (RecordFence, error) {
				return RecordFence{Status: RecordFenceCleared, Token: "t"}, nil
			}),
		})
		if err != nil || result.Failed != 1 || result.Failures[0].Stage != "build_target" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
}

func TestW7ASoftDeleteOrphanRelationFailure(t *testing.T) {
	store, db := w7aCleanupStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	old := "2026-08-01T00:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('orphan','sys','auth-o',NULL,NULL,?,?,'active',1),('target','sys',NULL,NULL,?,?,?,'disabled',0)`, old, old, old, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations(id,resource_type,resource_id,resource_owner_system_account_id,grantee_system_account_id,status)
		VALUES ('auth-o','account','target','sys','grantee','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE account_tag_bindings`); err != nil {
		t.Fatal(err)
	}
	_, err := store.Cleanup(context.Background(), CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z"})
	if err == nil || !strings.Contains(err.Error(), "soft-delete orphaned authorization instance") {
		t.Fatalf("orphan relation failure err=%v", err)
	}
}

func TestW7ARevokeChainFailureArms(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T) (*Store, *sql.DB) {
		store, db := w7aCleanupStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
		old := "2026-08-01T00:00:00.000Z"
		if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
			VALUES ('orphan','sys','auth-o',NULL,NULL,?,?,'active',1),('target','sys',NULL,NULL,?,?,?,'disabled',0)`, old, old, old, old, old); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO resource_authorizations(id,resource_type,resource_id,resource_owner_system_account_id,grantee_system_account_id,status)
			VALUES ('auth-o','account','target','sys','grantee','active')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO resource_authorization_sources(authorization_id,source_type,status) VALUES ('auth-o','manual','active')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO resource_authorization_grants(id,resource_type,resource_id,resource_owner_system_account_id,grantee_type,grantee_system_account_id,status)
			VALUES ('grant-t','account','target','sys','system_account','grantee','active')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO request_quota_hourly_window_scope_bindings(source_type,source_id,scope_type,scope_id) VALUES ('resource_authorization_grant','grant-t','account_authorization','auth-o')`); err != nil {
			t.Fatal(err)
		}
		return store, db
	}
	t.Run("quota-delete", func(t *testing.T) {
		store, db := seed(t)
		if _, err := db.Exec(`DROP TABLE request_quota_hourly_window_scope_bindings`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Cleanup(ctx, CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z"}); err == nil {
			t.Fatal("quota delete failure must abort orphan cleanup")
		}
	})
	t.Run("grants-revoke", func(t *testing.T) {
		store, db := seed(t)
		if _, err := db.Exec(`DROP TABLE resource_authorization_grants`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Cleanup(ctx, CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z"}); err == nil {
			t.Fatal("grants revoke failure must abort orphan cleanup")
		}
	})
	t.Run("sources-revoke", func(t *testing.T) {
		store, db := seed(t)
		if _, err := db.Exec(`DROP TABLE resource_authorization_sources`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Cleanup(ctx, CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z"}); err == nil {
			t.Fatal("sources revoke failure must abort orphan cleanup")
		}
	})
}

func TestW7ADeleteBusinessQuotaFailure(t *testing.T) {
	store, db := w7aCleanupStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	seedW7ADeletedRoot(t, db)
	if _, err := db.Exec(`DROP TABLE request_quota_hourly_window_scope_bindings`); err != nil {
		t.Fatal(err)
	}
	result, err := store.Cleanup(context.Background(), CleanupInput{
		CutoffDeletedAt: "2026-02-01T00:00:00.000Z",
		RecordFence: RecordFenceReaderFunc(func(context.Context, CleanupTarget) (RecordFence, error) {
			return RecordFence{Status: RecordFenceCleared, Token: "t"}, nil
		}),
	})
	if err != nil || result.Failed != 1 || result.Failures[0].Stage != "business_delete" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("accounts=%d err=%v", count, err)
	}
}
