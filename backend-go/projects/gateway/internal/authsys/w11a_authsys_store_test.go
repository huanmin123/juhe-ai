package authsys

// w11a coverage arms (part 2): AccountStore Create/Patch/ListPage/ListOptions
// error paths and mutation arms, plus the default-resource ensurer failures.

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

func w11aNewStore(t *testing.T) (*AccountStore, *sql.DB) {
	t.Helper()
	db := newContractTestDB(t)
	store, err := NewAccountStore(db, modelcheckauth.SQLite, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return store, db
}

func w11aCreate(t *testing.T, store *AccountStore, username string) AccountListItem {
	t.Helper()
	item, err := store.Create(nil, CreateInput{Username: username, DisplayName: username + "_name", Password: "w11a-pass"})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

// ---------------------------------------------------------------------------
// Create arms.
// ---------------------------------------------------------------------------

func TestW11ACreateArms(t *testing.T) {
	t.Run("image_flag_and_defaults_failure", func(t *testing.T) {
		store, db := w11aNewStore(t)
		store.SetDefaultResourceEnsurer(NewSQLDefaultResources(store, wlStringSealer{}))
		enabled := true
		if _, err := store.Create(nil, CreateInput{Username: "w11a-user", DisplayName: "w11a_user", Password: "w11a-pass", ImageGenerationEnabled: &enabled}); err != nil {
			t.Fatal(err)
		}
		// The same store with the groups relation missing fails inside the
		// default-resource bootstrap.
		if _, err := db.Exec(`DROP TABLE groups`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Create(nil, CreateInput{Username: "w11a-user2", DisplayName: "w11a_user2", Password: "w11a-pass"}); err == nil {
			t.Fatal("default resources failure must fail Create")
		}
	})

	t.Run("broken_and_closed_db", func(t *testing.T) {
		store, db := w11aNewStore(t)
		if _, err := db.Exec(`DROP TABLE system_accounts`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Create(nil, CreateInput{Username: "w11a-user", DisplayName: "w11a_user", Password: "w11a-pass"}); err == nil {
			t.Fatal("broken schema must fail Create")
		}
		closed, cdb := w11aNewStore(t)
		if err := cdb.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := closed.Create(nil, CreateInput{Username: "w11a-user", DisplayName: "w11a_user", Password: "w11a-pass"}); err == nil {
			t.Fatal("closed db must fail Create")
		}
	})

	t.Run("insert_trigger_failure", func(t *testing.T) {
		store, db := w11aNewStore(t)
		if _, err := db.Exec(`CREATE TRIGGER w11a_block_insert BEFORE INSERT ON system_accounts
			BEGIN SELECT RAISE(FAIL, 'w11a blocked'); END`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Create(nil, CreateInput{Username: "w11a-user", DisplayName: "w11a_user", Password: "w11a-pass"}); err == nil {
			t.Fatal("insert failure must fail Create")
		}
	})
}

// ---------------------------------------------------------------------------
// Patch arms.
// ---------------------------------------------------------------------------

func TestW11APatchValidationArms(t *testing.T) {
	store, _ := w11aNewStore(t)
	item := w11aCreate(t, store, "w11a-user")
	version := func() string {
		found, err := store.FindByID(nil, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		return found.UpdatedAt
	}

	blank := ""
	if _, err := store.Patch(nil, item.ID, PatchInput{ExpectedUpdatedAt: version(), DisplayName: &blank}); err == nil || !strings.Contains(err.Error(), "用户名称不能为空") {
		t.Fatalf("blank displayName = %v", err)
	}
	spaced := "a b"
	if _, err := store.Patch(nil, item.ID, PatchInput{ExpectedUpdatedAt: version(), DisplayName: &spaced}); err == nil || !strings.Contains(err.Error(), "空格") {
		t.Fatalf("spaced displayName = %v", err)
	}
	emptyPassword := ""
	if _, err := store.Patch(nil, item.ID, PatchInput{ExpectedUpdatedAt: version(), Password: &emptyPassword}); err == nil || !strings.Contains(err.Error(), "登录密码不能为空") {
		t.Fatalf("blank password = %v", err)
	}
	spacedPassword := "a bc"
	if _, err := store.Patch(nil, item.ID, PatchInput{ExpectedUpdatedAt: version(), Password: &spacedPassword}); err == nil || !strings.Contains(err.Error(), "登录密码不能包含空格") {
		t.Fatalf("spaced password = %v", err)
	}
	badRole := "guest"
	if _, err := store.Patch(nil, item.ID, PatchInput{ExpectedUpdatedAt: version(), Role: &badRole}); err == nil || !strings.Contains(err.Error(), "角色无效") {
		t.Fatalf("bad role = %v", err)
	}
	badStatus := "frozen"
	if _, err := store.Patch(nil, item.ID, PatchInput{ExpectedUpdatedAt: version(), Status: &badStatus}); err == nil || !strings.Contains(err.Error(), "状态无效") {
		t.Fatalf("bad status = %v", err)
	}
	badLimit := -3
	if _, err := store.Patch(nil, item.ID, PatchInput{ExpectedUpdatedAt: version(), AIAccountLimitPresent: true, AIAccountLimit: &badLimit}); err == nil || !strings.Contains(err.Error(), "AI 账户上限") {
		t.Fatalf("bad ai limit = %v", err)
	}
	negative := -1
	if _, err := store.Patch(nil, item.ID, PatchInput{ExpectedUpdatedAt: version(), RequestLimitsPresent: true, RequestLimits: &UserRequestLimits{PerMinute: &negative}}); err == nil {
		t.Fatal("bad request limits must fail")
	}
	// Missing row → nil, nil.
	result, err := store.Patch(nil, "w11a-missing", PatchInput{ExpectedUpdatedAt: version(), Status: strPtrW11A("disabled")})
	if err != nil || result.ID != "" {
		t.Fatalf("missing row = %+v, %v", result, err)
	}
}

func TestW11APatchMutationArms(t *testing.T) {
	t.Run("happy_must_change_and_limits", func(t *testing.T) {
		store, db := w11aNewStore(t)
		item := w11aCreate(t, store, "w11a-user")
		found, err := store.FindByID(nil, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		flag := false
		limit := 7
		perMinute := 5
		if _, err := store.Patch(nil, item.ID, PatchInput{
			ExpectedUpdatedAt: found.UpdatedAt, MustChangePassword: &flag,
			AIAccountLimitPresent: true, AIAccountLimit: &limit,
			RequestLimitsPresent: true, RequestLimits: &UserRequestLimits{PerMinute: &perMinute},
		}); err != nil {
			t.Fatal(err)
		}
		updated, err := store.FindByID(nil, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if updated.MustChangePassword || updated.AIAccountLimit == nil || *updated.AIAccountLimit != 7 {
			t.Fatalf("updated = %+v", updated)
		}
		_ = db
	})

	t.Run("stale_password_hash_verify_failure", func(t *testing.T) {
		store, db := w11aNewStore(t)
		item := w11aCreate(t, store, "w11a-user")
		if _, err := db.Exec(`UPDATE system_accounts SET password_hash = 'bad-format' WHERE id = ?`, item.ID); err != nil {
			t.Fatal(err)
		}
		found, err := store.FindByID(nil, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		newPassword := "w11a-new-password"
		if _, err := store.Patch(nil, item.ID, PatchInput{ExpectedUpdatedAt: found.UpdatedAt, Password: &newPassword}); err == nil || !strings.Contains(err.Error(), "hash") {
			t.Fatalf("stale hash = %v", err)
		}
	})

	t.Run("update_trigger_failure", func(t *testing.T) {
		store, db := w11aNewStore(t)
		item := w11aCreate(t, store, "w11a-user")
		found, err := store.FindByID(nil, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TRIGGER w11a_block_update BEFORE UPDATE ON system_accounts
			BEGIN SELECT RAISE(FAIL, 'w11a blocked'); END`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Patch(nil, item.ID, PatchInput{ExpectedUpdatedAt: found.UpdatedAt, Status: strPtrW11A("disabled")}); err == nil {
			t.Fatal("update failure must fail Patch")
		}
	})

	t.Run("session_cleanup_failure", func(t *testing.T) {
		store, db := w11aNewStore(t)
		item := w11aCreate(t, store, "w11a-user")
		found, err := store.FindByID(nil, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DROP TABLE system_sessions`); err != nil {
			t.Fatal(err)
		}
		newPassword := "w11a-new-password"
		if _, err := store.Patch(nil, item.ID, PatchInput{ExpectedUpdatedAt: found.UpdatedAt, Password: &newPassword}); err == nil {
			t.Fatal("session cleanup failure must fail Patch")
		}
	})

	t.Run("broken_schema", func(t *testing.T) {
		store, db := w11aNewStore(t)
		item := w11aCreate(t, store, "w11a-user")
		found, err := store.FindByID(nil, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DROP TABLE system_accounts`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Patch(nil, item.ID, PatchInput{ExpectedUpdatedAt: found.UpdatedAt, Status: strPtrW11A("disabled")}); err == nil {
			t.Fatal("broken schema must fail Patch")
		}
	})

	t.Run("pg_dialect_find_for_update", func(t *testing.T) {
		db := newContractTestDB(t)
		pgStore, err := NewAccountStore(db, modelcheckauth.Postgres, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, _, err := pgStore.findRowForUpdate(context.Background(), tx, "id = ?", "w11a-none"); err == nil {
			t.Fatal("FOR UPDATE must fail on SQLite")
		}
	})
}

// ---------------------------------------------------------------------------
// ListPage / ListOptions / UpdatePassword arms.
// ---------------------------------------------------------------------------

func TestW11AListArms(t *testing.T) {
	store, db := w11aNewStore(t)
	first := w11aCreate(t, store, "w11a-user")
	second := w11aCreate(t, store, "w11a-user2")

	// Clamp arms + hasMore.
	items, total, hasMore, err := store.ListPage(nil, "", 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || total != 2 || hasMore {
		t.Fatalf("clamped page = %d items, total %d, hasMore %v", len(items), total, hasMore)
	}
	_, _, smallMore, err := store.ListPage(nil, "", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !smallMore {
		t.Fatal("pageSize 1 over two accounts must report hasMore")
	}

	// Options clamps and ID hygiene.
	ids := []string{"", first.ID, first.ID, second.ID}
	for i := 0; i < 60; i++ {
		ids = append(ids, "w11a-filler-"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	options, err := store.ListOptions(nil, ids, "", 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(options) > 50 {
		t.Fatalf("options size = %d", len(options))
	}
	keywordOptions, err := store.ListOptions(nil, nil, "w11a-user", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(keywordOptions) == 0 {
		t.Fatal("keyword options must not be empty")
	}

	// Disabled accounts stay listed but flagged with a disabled reason.
	if _, err := db.Exec(`UPDATE system_accounts SET status='disabled' WHERE id = ?`, second.ID); err != nil {
		t.Fatal(err)
	}
	mixed, err := store.ListOptions(nil, []string{first.ID, second.ID}, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(mixed) != 2 || mixed[0].ID != first.ID || mixed[0].DisabledReason != nil || mixed[1].DisabledReason == nil {
		t.Fatalf("mixed options = %+v", mixed)
	}

	// Broken schema fails both reads.
	if _, err := db.Exec(`DROP TABLE system_accounts`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.ListPage(nil, "", 1, 20); err == nil {
		t.Fatal("broken schema must fail ListPage")
	}
	if _, err := store.ListOptions(nil, []string{first.ID}, "", 50); err == nil {
		t.Fatal("broken schema must fail ListOptions")
	}
}

func TestW11AUpdatePasswordArms(t *testing.T) {
	store, db := w11aNewStore(t)
	item := w11aCreate(t, store, "w11a-user")
	if _, err := store.UpdatePassword(nil, item.ID, "w11a-new-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE system_accounts`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdatePassword(nil, item.ID, "w11a-again"); err == nil {
		t.Fatal("broken schema must fail UpdatePassword")
	}
}

// ---------------------------------------------------------------------------
// Default-resource ensurer failure arms.
// ---------------------------------------------------------------------------

func w11aEnsurerFixture(t *testing.T) (*SQLDefaultResources, *sql.DB) {
	t.Helper()
	store, db := w11aNewStore(t)
	return NewSQLDefaultResources(store, wlStringSealer{}), db
}

func w11aRunEnsurer(t *testing.T, e *SQLDefaultResources, db *sql.DB) error {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	return e.EnsureDefaultResources(context.Background(), tx, "w11a-acc", time.Now().UTC().Format(time.RFC3339Nano))
}

func TestW11AEnsureDefaultResourcesFailureArms(t *testing.T) {
	t.Run("groups_query_failure", func(t *testing.T) {
		e, db := w11aEnsurerFixture(t)
		if _, err := db.Exec(`DROP TABLE groups`); err != nil {
			t.Fatal(err)
		}
		if err := w11aRunEnsurer(t, e, db); err == nil {
			t.Fatal("missing groups must fail")
		}
	})

	t.Run("groups_insert_failure", func(t *testing.T) {
		e, db := w11aEnsurerFixture(t)
		if _, err := db.Exec(`CREATE TRIGGER w11a_block_group BEFORE INSERT ON groups
			BEGIN SELECT RAISE(FAIL, 'w11a blocked'); END`); err != nil {
			t.Fatal(err)
		}
		if err := w11aRunEnsurer(t, e, db); err == nil {
			t.Fatal("group insert failure must fail")
		}
	})

	t.Run("strategies_query_failure", func(t *testing.T) {
		e, db := w11aEnsurerFixture(t)
		if _, err := db.Exec(`DROP TABLE route_strategies`); err != nil {
			t.Fatal(err)
		}
		if err := w11aRunEnsurer(t, e, db); err == nil {
			t.Fatal("missing strategies must fail")
		}
	})

	t.Run("strategies_insert_failure", func(t *testing.T) {
		e, db := w11aEnsurerFixture(t)
		if _, err := db.Exec(`CREATE TRIGGER w11a_block_strategy BEFORE INSERT ON route_strategies
			BEGIN SELECT RAISE(FAIL, 'w11a blocked'); END`); err != nil {
			t.Fatal(err)
		}
		if err := w11aRunEnsurer(t, e, db); err == nil {
			t.Fatal("strategy insert failure must fail")
		}
	})

	t.Run("binding_insert_failure", func(t *testing.T) {
		e, db := w11aEnsurerFixture(t)
		if _, err := db.Exec(`DROP TABLE route_strategy_groups`); err != nil {
			t.Fatal(err)
		}
		if err := w11aRunEnsurer(t, e, db); err == nil {
			t.Fatal("binding insert failure must fail")
		}
	})

	t.Run("api_keys_failure", func(t *testing.T) {
		e, db := w11aEnsurerFixture(t)
		if _, err := db.Exec(`DROP TABLE api_keys`); err != nil {
			t.Fatal(err)
		}
		if err := w11aRunEnsurer(t, e, db); err == nil {
			t.Fatal("missing api_keys must fail")
		}
	})

	t.Run("name_ladder_millisecond_arm", func(t *testing.T) {
		e, db := w11aEnsurerFixture(t)
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		// Pre-fill every "<base> N" candidate for the chat key name so the
		// ladder falls through to the millisecond fallback.
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec(`INSERT INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, created_at, updated_at)
			VALUES ('w11a-grp', 'w11a-acc', 'g', 'gpt', '', 1, 1, ?, ?)`, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO route_strategies (id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at)
			VALUES ('w11a-st', 'w11a-acc', 's', '', 'normal', 'active', 1, NULL, ?, ?)`, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
			VALUES ('w11a-bind', 'w11a-st', 'w11a-acc', 'w11a-grp', 1, 1, 'active', ?, ?)`, now, now); err != nil {
			t.Fatal(err)
		}
		occupy := func(id, name string) {
			t.Helper()
			if _, err := tx.Exec(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix, key_secret_encrypted, status, is_default, purpose, expires_at, quota_limits_json, availability_schedule_json, availability_schedule_next_check_at, created_at, updated_at)
				VALUES (?, 'w11a-acc', 'w11a-st', ?, '', ?, 'sk-w11a', 'w11a', 'sealed', 'active', 0, 'general', NULL, NULL, NULL, NULL, ?, ?)`,
				id, name, "hash-"+id, now, now); err != nil {
				t.Fatal(err)
			}
		}
		occupy("w11a-key-1", defaultChatAPIKeyName)
		for i := 2; i <= 1000; i++ {
			occupy("w11a-key-"+itoa(i), defaultChatAPIKeyName+" "+itoa(i))
		}
		if err := e.EnsureDefaultResources(context.Background(), tx, "w11a-acc", now); err != nil {
			t.Fatalf("millisecond fallback run failed: %v", err)
		}
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE system_account_id='w11a-acc' AND purpose='chat'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("chat fallback keys = %d", count)
		}
		var chatName string
		if err := tx.QueryRow(`SELECT name FROM api_keys WHERE system_account_id='w11a-acc' AND purpose='chat'`).Scan(&chatName); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(chatName, defaultChatAPIKeyName+" ") {
			t.Fatalf("chat fallback name = %q", chatName)
		}
	})
}

// The nil ensurer still guards EnsureDefaultResources directly.
func TestW11AEnsureDefaultResourcesNilSealer(t *testing.T) {
	store, db := w11aNewStore(t)
	e := NewSQLDefaultResources(store, nil)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := e.EnsureDefaultResources(context.Background(), tx, "w11a-acc", "now"); err == nil || !strings.Contains(err.Error(), "sealer") {
		t.Fatalf("nil sealer = %v", err)
	}
}
