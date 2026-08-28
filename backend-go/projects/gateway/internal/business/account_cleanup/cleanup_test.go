package accountcleanup

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func cleanupTestStore(t *testing.T, gate OwnerGate) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:account-cleanup-"+t.Name()+"?mode=memory&cache=shared")
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

func seedDeletedAccountTree(t *testing.T, db *sql.DB) {
	t.Helper()
	old := "2026-01-01T00:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO accounts
		(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('root','sys',NULL,NULL,?,? ,?, 'disabled',0),
		       ('child','sys','auth-child','root',?,? ,?, 'disabled',0)`, old, old, old, old, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations
		(id,resource_type,resource_id,grantee_system_account_id,status)
		VALUES ('auth-child','account','root','grantee','revoked')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorization_sources
		(authorization_id,source_type,source_team_id,status)
		VALUES ('auth-child','manual','team-1','revoked')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorization_grants
		(id,resource_type,resource_id,resource_owner_system_account_id,grantee_type,grantee_system_account_id,status)
		VALUES ('grant-root','account','root','sys','system_account','grantee','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,account_authorization_id) VALUES ('root',NULL), (NULL,'auth-child')`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"account_supported_models", "account_model_mappings", "account_tag_bindings", "account_name_search_terms", "account_name_search_documents", "account_api_key_runtime_states"} {
		if _, err := db.Exec("INSERT INTO " + table + "(account_id) VALUES ('root'), ('child')"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOwnerGateAndContractAreFailClosed(t *testing.T) {
	store, db := cleanupTestStore(t, OwnerGate{Confirmed: true, SchemaReady: true})
	if _, err := store.Cleanup(context.Background(), CleanupInput{}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("partial owner gate error=%v", err)
	}
	if err := store.CheckContract(context.Background()); err != nil {
		t.Fatalf("contract error=%v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("account count=%d error=%v", count, err)
	}
}

func TestCleanupDefersWithoutExternalRecordFence(t *testing.T) {
	store, db := cleanupTestStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	seedDeletedAccountTree(t, db)
	result, err := store.Cleanup(context.Background(), CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempted != 1 || result.Deferred != 1 || result.Completed != 0 || len(result.RecordCleanupTargets) != 1 {
		t.Fatalf("unexpected result=%+v", result)
	}
	got := result.RecordCleanupTargets[0]
	if got.AccountID != "root" || got.SystemAccountID != "sys" || len(got.RelatedAccountIDs) != 1 || got.RelatedAccountIDs[0] != "child" || len(got.AuthorizationIDs) != 1 || got.AuthorizationIDs[0] != "auth-child" || len(got.TeamScopeIDs) != 1 || got.TeamScopeIDs[0] != "child:team-1" {
		t.Fatalf("unexpected cleanup target=%+v", got)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("deferred cleanup changed accounts count=%d error=%v", count, err)
	}
}

func TestCleanupDeletesBusinessRowsAfterClearedFenceAndReplays(t *testing.T) {
	store, db := cleanupTestStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	seedDeletedAccountTree(t, db)
	fence := RecordFenceReaderFunc(func(_ context.Context, target CleanupTarget) (RecordFence, error) {
		if target.AccountID != "root" {
			t.Fatalf("fence received wrong target=%+v", target)
		}
		return RecordFence{Status: RecordFenceCleared, Token: "fence-1"}, nil
	})
	result, err := store.Cleanup(context.Background(), CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z", RecordFence: fence})
	if err != nil {
		t.Fatal(err)
	}
	if result.Completed != 1 || result.PhysicallyDeletedAccounts != 2 || result.PhysicallyDeletedAuthorizations != 1 || result.PhysicallyDeletedGrants != 1 || result.PhysicallyDeletedGroupBindings != 2 {
		t.Fatalf("unexpected delete result=%+v", result)
	}
	for _, table := range []string{"accounts", "resource_authorizations", "resource_authorization_grants", "group_accounts", "account_supported_models", "account_model_mappings", "account_tag_bindings"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("table=%s count=%d error=%v", table, count, err)
		}
	}
	replay, err := store.Cleanup(context.Background(), CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z", RecordFence: fence})
	if err != nil || replay.Attempted != 0 || replay.Completed != 0 {
		t.Fatalf("replay=%+v error=%v", replay, err)
	}
}

func TestCleanupSoftDeletesOrphanWithCAS(t *testing.T) {
	store, db := cleanupTestStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if _, err := db.Exec(`INSERT INTO accounts
		(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,updated_at,created_at,status,schedulable)
		VALUES ('orphan','sys','missing-auth',NULL,'2026-08-01T00:00:00.000Z','2026-08-01T00:00:00.000Z','active',1)`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"account_tag_bindings", "account_name_search_terms", "account_name_search_documents"} {
		if _, err := db.Exec("INSERT INTO " + table + "(account_id) VALUES ('orphan')"); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.Cleanup(context.Background(), CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z"})
	if err != nil || result.OrphanedAuthorizationInstances != 1 || result.Attempted != 0 {
		t.Fatalf("unexpected orphan result=%+v error=%v", result, err)
	}
	var status, deletedBy string
	var deletedAt sql.NullString
	if err := db.QueryRow(`SELECT status,deleted_by,deleted_at FROM accounts WHERE id='orphan'`).Scan(&status, &deletedBy, &deletedAt); err != nil {
		t.Fatal(err)
	}
	if status != "disabled" || deletedBy != "sys_admin" || !deletedAt.Valid {
		t.Fatalf("orphan was not tombstoned status=%q deletedBy=%q deletedAt=%v", status, deletedBy, deletedAt)
	}
	for _, table := range []string{"account_tag_bindings", "account_name_search_terms", "account_name_search_documents"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table + " WHERE account_id='orphan'").Scan(&count); err != nil || count != 0 {
			t.Fatalf("orphan cleanup table=%s count=%d error=%v", table, count, err)
		}
	}
}

func TestInvalidFenceIsReportedAndRetained(t *testing.T) {
	store, db := cleanupTestStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	seedDeletedAccountTree(t, db)
	result, err := store.Cleanup(context.Background(), CleanupInput{
		CutoffDeletedAt: "2026-02-01T00:00:00.000Z",
		RecordFence: RecordFenceReaderFunc(func(context.Context, CleanupTarget) (RecordFence, error) {
			return RecordFence{Status: RecordFenceCleared}, nil
		}),
	})
	if err != nil || result.Failed != 1 || len(result.Failures) != 1 || result.Deferred != 0 {
		t.Fatalf("unexpected invalid fence result=%+v error=%v", result, err)
	}
	if !strings.Contains(result.Failures[0].Error, ErrInvalidFence.Error()) {
		t.Fatalf("failure did not retain original fence error=%+v", result.Failures)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("invalid fence changed accounts count=%d error=%v", count, err)
	}
}

func TestCleanupRejectsStaleBusinessCAS(t *testing.T) {
	store, db := cleanupTestStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	seedDeletedAccountTree(t, db)
	result, err := store.Cleanup(context.Background(), CleanupInput{
		CutoffDeletedAt: "2026-02-01T00:00:00.000Z",
		RecordFence: RecordFenceReaderFunc(func(context.Context, CleanupTarget) (RecordFence, error) {
			if _, updateErr := db.Exec(`UPDATE accounts SET updated_at='2026-01-02T00:00:00.000Z' WHERE id='root'`); updateErr != nil {
				t.Fatalf("advance account version: %v", updateErr)
			}
			return RecordFence{Status: RecordFenceCleared, Token: "fence-1"}, nil
		}),
	})
	if err != nil || result.Failed != 1 || len(result.Failures) != 1 || !strings.Contains(result.Failures[0].Error, ErrCAS.Error()) {
		t.Fatalf("unexpected stale CAS result=%+v error=%v", result, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("stale CAS changed accounts count=%d error=%v", count, err)
	}
}

func TestCleanupRollsBackBusinessMutationOnDeleteError(t *testing.T) {
	store, db := cleanupTestStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	seedDeletedAccountTree(t, db)
	if _, err := db.Exec(`DROP TABLE account_model_mappings`); err != nil {
		t.Fatal(err)
	}
	result, err := store.Cleanup(context.Background(), CleanupInput{
		CutoffDeletedAt: "2026-02-01T00:00:00.000Z",
		RecordFence: RecordFenceReaderFunc(func(context.Context, CleanupTarget) (RecordFence, error) {
			return RecordFence{Status: RecordFenceCleared, Token: "fence-1"}, nil
		}),
	})
	if err != nil || result.Failed != 1 || len(result.Failures) != 1 {
		t.Fatalf("unexpected rollback result=%+v error=%v", result, err)
	}
	var accountCount, modelCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&accountCount); err != nil || accountCount != 2 {
		t.Fatalf("account mutation was not rolled back count=%d error=%v", accountCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_supported_models`).Scan(&modelCount); err != nil || modelCount != 2 {
		t.Fatalf("relation mutation was not rolled back count=%d error=%v", modelCount, err)
	}
}

func TestPostgresQualificationAndPlaceholders(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := New(db, Postgres, "juhe_business", OwnerGate{})
	if err != nil {
		t.Fatal(err)
	}
	query := store.bind(`SELECT id FROM ` + store.table("accounts") + ` WHERE id=? AND deleted_at<=? LIMIT ?`)
	for _, want := range []string{"juhe_business.accounts", "id=$1", "deleted_at<=$2", "LIMIT $3"} {
		if !strings.Contains(query, want) {
			t.Fatalf("postgres query missing %q: %s", want, query)
		}
	}
	if strings.Contains(query, "?") {
		t.Fatalf("postgres query retained question-mark placeholder: %s", query)
	}
	if _, err := New(db, Postgres, "bad.schema", OwnerGate{}); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("invalid schema error=%v", err)
	}
}

func TestCleanupLimitMatchesNodeBounds(t *testing.T) {
	for input, want := range map[int]int{0: defaultCleanupLimit, -1: 1, 1: 1, maxCleanupLimit + 1: maxCleanupLimit} {
		if got := normalizeLimit(input); got != want {
			t.Fatalf("normalizeLimit(%d)=%d want=%d", input, got, want)
		}
	}
}
