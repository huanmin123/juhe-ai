package authz

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

type fixture struct {
	store *Store
	db    *sql.DB
	now   time.Time
}

var ddl = []string{
	`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, deleted_at TEXT, authorization_instance_authorization_id TEXT, account_expires_at TEXT)`,
	`CREATE TABLE IF NOT EXISTS system_teams (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, status TEXT NOT NULL DEFAULT 'active', created_by TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS system_team_members (id TEXT PRIMARY KEY, team_id TEXT NOT NULL, system_account_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', joined_at TEXT NOT NULL, removed_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS groups (id TEXT PRIMARY KEY, name TEXT NOT NULL, system_account_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active')`,
	`CREATE TABLE IF NOT EXISTS resource_authorizations (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, resource_owner_system_account_id TEXT NOT NULL, grantee_system_account_id TEXT NOT NULL, scope TEXT NOT NULL DEFAULT 'use', status TEXT NOT NULL DEFAULT 'active', effective_source_type TEXT, effective_source_team_id TEXT, activated_at TEXT, last_source_changed_at TEXT, remark TEXT, expires_at TEXT, limits_json TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, revoked_reason TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS resource_authorization_sources (id TEXT PRIMARY KEY, authorization_id TEXT NOT NULL, source_type TEXT NOT NULL, source_team_id TEXT, status TEXT NOT NULL DEFAULT 'active', activated_at TEXT, ended_at TEXT, ended_reason TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS resource_authorization_grants (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, resource_owner_system_account_id TEXT NOT NULL, grantee_type TEXT NOT NULL, grantee_system_account_id TEXT, grantee_team_id TEXT, scope TEXT NOT NULL DEFAULT 'use', status TEXT NOT NULL DEFAULT 'active', remark TEXT, expires_at TEXT, limits_json TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, updated_at TEXT NOT NULL)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorizations_user_unique ON resource_authorizations(resource_type, resource_id, grantee_system_account_id)`,
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := sql.Open("sqlite", "file:authz-fixture-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range ddl {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Unix(1_750_000_000, 0).UTC()
	store, err := NewStore(db, false, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{store: store, db: db, now: now}
}

func (f *fixture) seedAccount(t *testing.T, id, status string) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, 'user', ?, 'pbkdf2$sha512$120000$abc$def', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, id, id, status); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) seedTeamWithMember(t *testing.T, teamID, memberID string) {
	t.Helper()
	now := f.now.UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(`INSERT INTO system_teams (id, name, status, created_by, created_at, updated_at)
		VALUES (?, ?, 'active', 'owner', ?, ?) ON CONFLICT(id) DO NOTHING`, teamID, "Team "+teamID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_at, updated_at)
		VALUES (?, ?, ?, 'active', ?, ?, ?)`, "teammem_"+memberID, teamID, memberID, now, now, now); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) seedGroup(t *testing.T, groupID, ownerID string) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES (?, ?, ?, 'active')`,
		groupID, "Group "+groupID, ownerID); err != nil {
		t.Fatal(err)
	}
}

func TestCreateGroupAuthorizationLifecycle(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_1", "owner")
	remark := "initial remark"

	// Direct grant of a group to grantee.
	result, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeID: "grantee", Remark: &remark,
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.Item.Status != StatusActive {
		t.Fatalf("create result: %+v", result)
	}

	// Runtime row exists with effective manual source.
	var runtimeStatus, effectiveType string
	if err := f.db.QueryRow(`SELECT status, COALESCE(effective_source_type,'') FROM resource_authorizations
		WHERE resource_type = 'group' AND resource_id = 'grp_1' AND grantee_system_account_id = 'grantee'`).
		Scan(&runtimeStatus, &effectiveType); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != StatusActive || effectiveType != "manual" {
		t.Fatalf("runtime after create: status=%s effective=%s", runtimeStatus, effectiveType)
	}

	// Identical active grant is idempotent, matching Node's created=false/200
	// response rather than treating a retry as a conflict.
	retry, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeID: "grantee", LimitsJSON: func() *string { v := "{}"; return &v }(),
	}, "owner")
	if err != nil || retry.Created || retry.Item.ID != result.Item.ID {
		t.Fatalf("idempotent duplicate result = %+v err=%v", retry, err)
	}

	// A changed active grant remains a conflict.
	changedRemark := "changed"
	_, err = f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeID: "grantee", Remark: &changedRemark,
	}, "owner")
	if err == nil || !strings.Contains(err.Error(), "该资源已授权给该用户") {
		t.Fatalf("changed duplicate grant error = %v", err)
	}

	// Owner cannot be grantee of their own resource.
	_, err = f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeID: "owner",
	}, "owner")
	if err == nil || !strings.Contains(err.Error(), "不能授权给资源所有者自己") {
		t.Fatalf("self grant error = %v", err)
	}

	// Return (grantee side) → returned + runtime terminal.
	mutation, err := f.store.Return(context.Background(), result.Item.ID,
		result.Item.UpdatedAt, "grantee")
	if err != nil {
		t.Fatal(err)
	}
	if mutation.Status != "updated" || mutation.Result.Status != StatusReturned {
		t.Fatalf("return mutation: %+v", mutation)
	}
	if err := f.db.QueryRow(`SELECT status, COALESCE(effective_source_type,''), COALESCE(revoked_reason,'')
		FROM resource_authorizations WHERE resource_type='group' AND resource_id='grp_1' AND grantee_system_account_id='grantee'`).
		Scan(&runtimeStatus, &effectiveType, new(string)); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != StatusReturned || effectiveType != "" {
		t.Fatalf("runtime after return: status=%s effective=%s", runtimeStatus, effectiveType)
	}

	// Reviving a terminal grant reuses the same row and reports created=false
	// with the previous status, matching Node's upsert outcome.
	revived, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil || revived.Created || revived.Item.ID != result.Item.ID || revived.PreviousStatus == nil || *revived.PreviousStatus != StatusReturned {
		t.Fatalf("terminal revival result = %+v err=%v", revived, err)
	}
	var grantCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM resource_authorization_grants
		WHERE resource_type = 'group' AND resource_id = 'grp_1' AND grantee_system_account_id = 'grantee'`).Scan(&grantCount); err != nil {
		t.Fatal(err)
	}
	if grantCount != 1 {
		t.Fatalf("terminal revival grant count = %d, want 1", grantCount)
	}
	var revivedRemark string
	if err := f.db.QueryRow(`SELECT COALESCE(remark,'') FROM resource_authorization_grants WHERE id = ?`, revived.Item.ID).Scan(&revivedRemark); err != nil {
		t.Fatal(err)
	}
	if revivedRemark != remark {
		t.Fatalf("terminal revival remark = %q, want %q", revivedRemark, remark)
	}
}

func TestAuthorizationsProcessingTTLMatchesNode(t *testing.T) {
	if authorizationsProcessingTTL != 120*time.Second {
		t.Fatalf("authorizations processing TTL = %s, want 120s", authorizationsProcessingTTL)
	}
}

func TestTeamGrantEffectiveSourceAndRevoke(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "member1", "active")
	f.seedAccount(t, "member2", "active")
	f.seedTeamWithMember(t, "team_1", "member1")
	f.seedTeamWithMember(t, "team_1", "member2")
	f.seedTeamWithMember(t, "team_1", "owner")
	f.seedGroup(t, "grp_1", "owner")

	result, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "team", GranteeID: "team_1",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}

	// Each member gets a runtime row with effective team source.
	for _, member := range []string{"member1", "member2"} {
		var status, effective string
		if err := f.db.QueryRow(`SELECT status, COALESCE(effective_source_type,'') FROM resource_authorizations
			WHERE grantee_system_account_id = ? AND resource_id = 'grp_1'`, member).Scan(&status, &effective); err != nil {
			t.Fatal(err)
		}
		if status != StatusActive || effective != "team" {
			t.Fatalf("member %s runtime: status=%s effective=%s", member, status, effective)
		}
	}
	var ownerRuntimeCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM resource_authorizations
		WHERE grantee_system_account_id = 'owner' AND resource_id = 'grp_1'`).Scan(&ownerRuntimeCount); err != nil {
		t.Fatal(err)
	}
	if ownerRuntimeCount != 0 {
		t.Fatalf("owner runtime rows after team grant = %d, want 0", ownerRuntimeCount)
	}

	// Owner-side revoke marks the grant and all member runtimes revoked.
	mutation, err := f.store.Revoke(context.Background(), result.Item.ID,
		result.Item.UpdatedAt, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if mutation.Status != "updated" || mutation.Result.Status != StatusRevoked {
		t.Fatalf("revoke mutation: %+v", mutation)
	}
	for _, member := range []string{"member1", "member2"} {
		var status string
		if err := f.db.QueryRow(`SELECT status FROM resource_authorizations
			WHERE grantee_system_account_id = ? AND resource_id = 'grp_1'`, member).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != StatusRevoked {
			t.Fatalf("member %s runtime after revoke = %s", member, status)
		}
	}
}

func TestTeamGrantActiveLimitRejectsTwentyFirstGrant(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "member", "active")
	f.seedTeamWithMember(t, "team_1", "member")
	for i := 0; i < MaxTeamActiveGrantCount-1; i++ {
		resourceID := "grp_limit_" + itoa(i)
		f.seedGroup(t, resourceID, "owner")
		if _, err := f.db.Exec(`INSERT INTO resource_authorization_grants
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_team_id,
			 scope, status, created_by, created_at, updated_at)
			VALUES (?, 'group', ?, 'owner', 'team', 'team_1', 'use', 'active', 'owner', ?, ?)`,
			"grant_limit_"+itoa(i), resourceID, f.now.UTC().Format(time.RFC3339Nano), f.now.UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	f.seedGroup(t, "grp_limit_twentieth", "owner")
	twentieth, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_limit_twentieth",
		GranteeType: "team", GranteeID: "team_1",
	}, "owner")
	if err != nil || !twentieth.Created {
		t.Fatalf("20th team grant result = %+v err=%v", twentieth, err)
	}
	f.seedGroup(t, "grp_limit_new", "owner")
	_, err = f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_limit_new",
		GranteeType: "team", GranteeID: "team_1",
	}, "owner")
	if err == nil || !strings.Contains(err.Error(), "最多支持 20 条有效授权") {
		t.Fatalf("21st team grant error = %v", err)
	}
}

func TestTeamGrantRejectsOwnerOnlyTeam(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedTeamWithMember(t, "team_owner_only", "owner")
	f.seedGroup(t, "grp_owner_only", "owner")
	_, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_owner_only",
		GranteeType: "team", GranteeID: "team_owner_only",
	}, "owner")
	if err == nil || !strings.Contains(err.Error(), "团队暂无可授权成员") {
		t.Fatalf("owner-only team error = %v", err)
	}
	var grants int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM resource_authorization_grants WHERE resource_id = 'grp_owner_only'`).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 0 {
		t.Fatalf("owner-only team grants = %d, want 0", grants)
	}
}

func TestCreateRejectsResourceOutsideActorScope(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "other", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_scoped", "owner")
	_, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_scoped",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "other")
	if err == nil || !strings.Contains(err.Error(), "授权资源不存在") {
		t.Fatalf("cross-scope create error = %v", err)
	}
}

func TestPatchRejectsResourceOutsideSelectedOwnerScope(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "other_owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_scope_patch", "owner")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_scope_patch",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	paused := StatusPaused
	outcome, err := f.store.PatchForOwner(context.Background(), created.Item.ID, PatchInput{Status: &paused},
		created.Item.UpdatedAt, "admin", "other_owner")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != "not_found" {
		t.Fatalf("cross-scope patch outcome = %+v, want not_found", outcome)
	}
	var status string
	if err := f.db.QueryRow(`SELECT status FROM resource_authorization_grants WHERE id = ?`, created.Item.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != StatusActive {
		t.Fatalf("cross-scope patch changed grant status to %q", status)
	}
}

func TestCreateNormalizesAndValidatesExpiry(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_expiry", "owner")
	invalid := "2027-01-01T00:00:00"
	_, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_expiry",
		GranteeType: "system_account", GranteeID: "grantee", ExpiresAt: &invalid,
	}, "owner")
	if err == nil || !strings.Contains(err.Error(), "过期时间格式不正确") {
		t.Fatalf("naive expiry error = %v", err)
	}
	past := f.now.Add(-time.Second).Format(time.RFC3339Nano)
	_, err = f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_expiry",
		GranteeType: "system_account", GranteeID: "grantee", ExpiresAt: &past,
	}, "owner")
	if err == nil || !strings.Contains(err.Error(), "授权到期时间不能早于当前时间") {
		t.Fatalf("past expiry error = %v", err)
	}
	offsetFuture := "2027-01-01T09:00:00+09:00"
	result, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_expiry",
		GranteeType: "system_account", GranteeID: "grantee", ExpiresAt: &offsetFuture,
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if result.Item.ExpiresAt == nil || *result.Item.ExpiresAt != "2027-01-01T00:00:00.000Z" {
		t.Fatalf("canonical expiry = %v", result.Item.ExpiresAt)
	}

	// Node validates a source account's own expiry after finding the resource,
	// before it writes any grant or runtime projection.
	if _, err := f.db.Exec(accountsFixtureDDL); err != nil {
		t.Fatal(err)
	}
	f.seedSourceAccount(t, "acc_expiry", "owner")
	accountExpiry := f.now.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(`UPDATE accounts SET account_expires_at = ? WHERE id = 'acc_expiry'`, accountExpiry); err != nil {
		t.Fatal(err)
	}
	tooLate := f.now.Add(3 * time.Hour).UTC().Format(time.RFC3339Nano)
	targetGroupID := "target_group"
	_, err = f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc_expiry",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: &targetGroupID, ExpiresAt: &tooLate,
	}, "owner")
	if err == nil || !strings.Contains(err.Error(), "授权到期时间不能晚于账户到期时间") {
		t.Fatalf("account upper-bound expiry error = %v", err)
	}
	var accountGrants int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM resource_authorization_grants WHERE resource_id = 'acc_expiry'`).Scan(&accountGrants); err != nil {
		t.Fatal(err)
	}
	if accountGrants != 0 {
		t.Fatalf("overlong account-expiry grants = %d, want 0", accountGrants)
	}

	// Node looks up a well-formed input's resource before it checks whether the
	// requested expiry is already past. Keep that externally visible priority.
	_, err = f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "missing_group",
		GranteeType: "system_account", GranteeID: "grantee", ExpiresAt: &past,
	}, "owner")
	if err == nil || !strings.Contains(err.Error(), "授权资源不存在") {
		t.Fatalf("missing resource expiry priority error = %v", err)
	}
}

func TestPatchOptimisticConflictAndExpire(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_1", "owner")

	result, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}

	// Stale version → conflict.
	outcome, err := f.store.Patch(context.Background(), result.Item.ID, PatchInput{
		Status: &[]string{StatusPaused}[0],
	}, "2001-01-01T00:00:00Z", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != "conflict" {
		t.Fatalf("stale patch = %+v", outcome)
	}

	// Pause with the fresh version.
	paused := StatusPaused
	outcome, err = f.store.Patch(context.Background(), result.Item.ID, PatchInput{
		Status: &paused,
	}, result.Item.UpdatedAt, "owner")
	if err != nil || outcome.Status != "updated" {
		t.Fatalf("pause patch: %+v err=%v", outcome, err)
	}

	// Runtime reflects paused.
	var runtimeStatus string
	if err := f.db.QueryRow(`SELECT status FROM resource_authorizations
		WHERE grantee_system_account_id = 'grantee' AND resource_id = 'grp_1'`).Scan(&runtimeStatus); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != StatusPaused {
		t.Fatalf("runtime after pause = %s", runtimeStatus)
	}

	// Expire sweep: force the grant past expiry, then sweep.
	future := f.now.Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(`UPDATE resource_authorization_grants SET expires_at = ?, status = 'active' WHERE id = ?`,
		future, result.Item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE resource_authorizations SET expires_at = ?`, future); err != nil {
		t.Fatal(err)
	}
	count, err := f.store.ExpireSweep(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expire sweep count = %d", count)
	}
	var grantStatus, runtimeAfter string
	if err := f.db.QueryRow(`SELECT status FROM resource_authorization_grants WHERE id = ?`, result.Item.ID).Scan(&grantStatus); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT status FROM resource_authorizations
		WHERE grantee_system_account_id = 'grantee' AND resource_id = 'grp_1'`).Scan(&runtimeAfter); err != nil {
		t.Fatal(err)
	}
	if grantStatus != StatusExpired || runtimeAfter != StatusExpired {
		t.Fatalf("after sweep: grant=%s runtime=%s", grantStatus, runtimeAfter)
	}
}

var _ = modelcheckauth.SQLite
