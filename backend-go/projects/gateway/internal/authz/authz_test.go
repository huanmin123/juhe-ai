package authz

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
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
	f := &fixture{db: db, now: now}
	// The clock reads through the fixture pointer so tests can advance time
	// for expiry state-machine coverage.
	store, err := NewStore(db, false, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	f.store = store
	return f
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

// ---------------------------------------------------------------------------
// Contract alignment tests for the 2026-09-04 independent review verdicts.
// Each test cites the Node evidence lines it pins.
// ---------------------------------------------------------------------------

// ptrString is a small helper for presence-bearing inputs.
func ptrString(value string) *string { return &value }

// Claim #1: patching a paused direct grant back to active must run the full
// grant→runtime sync (patchResourceAuthorizationAsync :809 →
// syncUserGrantRuntimeAsync :1249-1297 → upsertResourceAuthorizationForUser),
// rewriting runtime status, revoked_* and the manual source, instead of relying
// on the standalone refresh CASE that only pins the current status.
func TestPatchResumeSyncsDirectGrantRuntime(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_resume", "owner")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_resume",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	paused := StatusPaused
	if outcome, err := f.store.Patch(context.Background(), created.Item.ID, PatchInput{Status: &paused},
		created.Item.UpdatedAt, "owner"); err != nil || outcome.Status != "updated" {
		t.Fatalf("pause patch: %+v err=%v", outcome, err)
	}
	// The sync writes authorization_paused, then the standalone refresh's
	// manual branch resets revoked_reason to NULL when not expired — the Node
	// end state of a pause (:1300-1313 → manual refresh branch :2101-2128).
	assertRuntime(t, f, "grantee", "grp_resume", StatusPaused, "manual", "")

	active := StatusActive
	outcome, err := f.store.Patch(context.Background(), created.Item.ID, PatchInput{Status: &active},
		runtimeVersion(t, f, created.Item.ID), "owner")
	if err != nil || outcome.Status != "updated" {
		t.Fatalf("resume patch: %+v err=%v", outcome, err)
	}
	// Node upsert (:1256-1270): status active, revoked_* cleared, manual
	// source reactivated.
	assertRuntime(t, f, "grantee", "grp_resume", StatusActive, "manual", "")
	var revokedBy, revokedAt, revokedReason sql.NullString
	if err := f.db.QueryRow(`SELECT revoked_by, revoked_at, revoked_reason FROM resource_authorizations
		WHERE grantee_system_account_id = 'grantee' AND resource_id = 'grp_resume'`).
		Scan(&revokedBy, &revokedAt, &revokedReason); err != nil {
		t.Fatal(err)
	}
	if revokedBy.Valid || revokedAt.Valid || revokedReason.Valid {
		t.Fatalf("resume must clear runtime revoked_*: %v %v %v", revokedBy, revokedAt, revokedReason)
	}
	var sourceStatus string
	if err := f.db.QueryRow(`SELECT s.status FROM resource_authorization_sources s
		INNER JOIN resource_authorizations r ON r.id = s.authorization_id
		WHERE r.grantee_system_account_id = 'grantee' AND s.source_type = 'manual'`).Scan(&sourceStatus); err != nil {
		t.Fatal(err)
	}
	if sourceStatus != "active" {
		t.Fatalf("manual source after resume = %s, want active", sourceStatus)
	}
}

// Claims #1/#2: PATCH expiresAt canonicalizes to UTC milliseconds and the sync
// projects the canonical value onto the runtime row (:1256-1270/:1380-1387).
func TestPatchExpiresAtCanonicalizesAndProjectsRuntime(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_expiry_sync", "owner")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_expiry_sync",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	offsetFuture := "2027-01-01T09:00:00+09:00"
	outcome, err := f.store.Patch(context.Background(), created.Item.ID, PatchInput{
		ExpiresAt: &offsetFuture, ExpiresAtSet: true,
	}, created.Item.UpdatedAt, "owner")
	if err != nil || outcome.Status != "updated" {
		t.Fatalf("expiry patch: %+v err=%v", outcome, err)
	}
	canonical := "2027-01-01T00:00:00.000Z"
	if outcome.Result.ExpiresAt == nil || *outcome.Result.ExpiresAt != canonical {
		t.Fatalf("grant canonical expiry = %v", outcome.Result.ExpiresAt)
	}
	var grantExpires, runtimeExpires string
	if err := f.db.QueryRow(`SELECT g.expires_at, r.expires_at FROM resource_authorization_grants g
		INNER JOIN resource_authorizations r ON r.resource_id = g.resource_id
		WHERE g.id = ? AND r.grantee_system_account_id = 'grantee'`, created.Item.ID).Scan(&grantExpires, &runtimeExpires); err != nil {
		t.Fatal(err)
	}
	if grantExpires != canonical || runtimeExpires != canonical {
		t.Fatalf("canonical expiry projection: grant=%s runtime=%s", grantExpires, runtimeExpires)
	}
}

// Claim #3: expiresAt presence semantics — explicit null clears grant and
// runtime columns, empty string is a format error, absent keeps the value, and
// a same-value patch is unchanged without a version bump (:824-827/:801,
// normalizeResourceAuthorizationExpiresAtInput :2597-2613).
func TestPatchExpiresAtPresenceNullClearAndUnchanged(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_presence", "owner")
	future := "2027-06-01T00:00:00.000Z"
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_presence",
		GranteeType: "system_account", GranteeID: "grantee", ExpiresAt: &future,
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	// Absent expiresAt (status-only patch) keeps the stored value.
	paused := StatusPaused
	outcome, err := f.store.Patch(context.Background(), created.Item.ID, PatchInput{Status: &paused},
		created.Item.UpdatedAt, "owner")
	if err != nil || outcome.Status != "updated" || outcome.Result.ExpiresAt == nil || *outcome.Result.ExpiresAt != future {
		t.Fatalf("status-only patch must keep expiry: %+v err=%v", outcome, err)
	}
	// Empty string → format error (Node normalize throws 过期时间格式不正确).
	empty := ""
	if _, err := f.store.Patch(context.Background(), created.Item.ID, PatchInput{
		ExpiresAt: &empty, ExpiresAtSet: true,
	}, runtimeVersion(t, f, created.Item.ID), "owner"); err == nil || !strings.Contains(err.Error(), "过期时间格式不正确") {
		t.Fatalf("empty expiry error = %v", err)
	}
	// Same-value patch → unchanged, version untouched.
	outcome, err = f.store.Patch(context.Background(), created.Item.ID, PatchInput{
		ExpiresAt: &future, ExpiresAtSet: true,
	}, runtimeVersion(t, f, created.Item.ID), "owner")
	if err != nil || outcome.Status != "unchanged" {
		t.Fatalf("same-value patch: %+v err=%v", outcome, err)
	}
	if runtimeVersion(t, f, created.Item.ID) != outcome.Result.UpdatedAt {
		t.Fatalf("unchanged patch bumped the version")
	}
	// Explicit null clears grant and runtime columns.
	outcome, err = f.store.Patch(context.Background(), created.Item.ID, PatchInput{
		ExpiresAtSet: true,
	}, runtimeVersion(t, f, created.Item.ID), "owner")
	if err != nil || outcome.Status != "updated" {
		t.Fatalf("null-clear patch: %+v err=%v", outcome, err)
	}
	if outcome.Result.ExpiresAt != nil {
		t.Fatalf("null patch must clear the grant expiry: %v", outcome.Result.ExpiresAt)
	}
	var runtimeExpires sql.NullString
	if err := f.db.QueryRow(`SELECT expires_at FROM resource_authorizations
		WHERE grantee_system_account_id = 'grantee' AND resource_id = 'grp_presence'`).Scan(&runtimeExpires); err != nil {
		t.Fatal(err)
	}
	if runtimeExpires.Valid {
		t.Fatalf("null patch must clear the runtime expiry: %v", runtimeExpires)
	}
}

// Claims #4/#10: patch expiry state machine — past expiry forces expired,
// expired→active requires an accompanying expiry, activation revalidates the
// stored expiry, and the account bound caps the patch (:829-838, :867,
// validateResourceAuthorizationExpiresAtAsync :2654-2679).
func TestPatchExpiryStateMachineAndRestoreRules(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_state", "owner")
	hour := time.Hour
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_state",
		GranteeType: "system_account", GranteeID: "grantee",
		ExpiresAt: ptrString(f.now.Add(hour).UTC().Format(time.RFC3339Nano)),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	// Explicit past expiry flips the grant and runtime to expired
	// (Node :832-833 + paused/expired sync :1300-1313).
	past := f.now.Add(-hour).UTC().Format(time.RFC3339Nano)
	outcome, err := f.store.Patch(context.Background(), created.Item.ID, PatchInput{
		ExpiresAt: &past, ExpiresAtSet: true,
	}, created.Item.UpdatedAt, "owner")
	if err != nil || outcome.Status != "updated" || outcome.Result.Status != StatusExpired {
		t.Fatalf("past expiry patch: %+v err=%v", outcome, err)
	}
	assertRuntime(t, f, "grantee", "grp_state", StatusExpired, "manual", "authorization_expired")
	// Expired → active without a new expiry is rejected (Node :829-831).
	if _, err := f.store.Patch(context.Background(), created.Item.ID, PatchInput{
		Status: ptrString(StatusActive),
	}, runtimeVersion(t, f, created.Item.ID), "owner"); err == nil || !strings.Contains(err.Error(), "到期授权恢复时请同时调整过期时间") {
		t.Fatalf("restore without expiry error = %v", err)
	}
	// Expired + future expiry restores active on grant and runtime (Node
	// :836-837 hasExpiresAtInput → 'active').
	future := f.now.Add(3 * hour).UTC().Format(time.RFC3339Nano)
	outcome, err = f.store.Patch(context.Background(), created.Item.ID, PatchInput{
		ExpiresAt: &future, ExpiresAtSet: true,
	}, runtimeVersion(t, f, created.Item.ID), "owner")
	if err != nil || outcome.Status != "updated" || outcome.Result.Status != StatusActive {
		t.Fatalf("restore patch: %+v err=%v", outcome, err)
	}
	assertRuntime(t, f, "grantee", "grp_state", StatusActive, "manual", "")
	// Claim #10: activation revalidates the stored expiry against the current
	// instant (Node :867 validateExpiresAt on activation transitions).
	f.now = f.now.Add(4 * hour)
	if _, err := f.store.Patch(context.Background(), created.Item.ID, PatchInput{
		Status: ptrString(StatusPaused),
	}, runtimeVersion(t, f, created.Item.ID), "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Patch(context.Background(), created.Item.ID, PatchInput{
		Status: ptrString(StatusActive),
	}, runtimeVersion(t, f, created.Item.ID), "owner"); err == nil || !strings.Contains(err.Error(), "授权到期时间不能早于当前时间") {
		t.Fatalf("stale-expiry activation error = %v", err)
	}
	// Extending with a fresh future expiry recovers.
	extended := f.now.Add(3 * hour).UTC().Format(time.RFC3339Nano)
	if outcome, err = f.store.Patch(context.Background(), created.Item.ID, PatchInput{
		Status: ptrString(StatusActive), ExpiresAt: &extended, ExpiresAtSet: true,
	}, runtimeVersion(t, f, created.Item.ID), "owner"); err != nil || outcome.Status != "updated" {
		t.Fatalf("extend+activate patch: %+v err=%v", outcome, err)
	}
	// Account bound: a patch beyond the source account expiry is rejected
	// (validateResourceAuthorizationExpiresAtAsync :2665-2678).
	if _, err := f.db.Exec(accountsFixtureDDL); err != nil {
		t.Fatal(err)
	}
	f.seedSourceAccount(t, "acc_state", "owner")
	accountExpiry := f.now.Add(2 * hour).UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(`UPDATE accounts SET account_expires_at = ? WHERE id = 'acc_state'`, accountExpiry); err != nil {
		t.Fatal(err)
	}
	targetGroupID := "target_group_state"
	accountGrant, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc_state",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: &targetGroupID,
		ExpiresAt:     ptrString(f.now.Add(hour).UTC().Format(time.RFC3339Nano)),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Patch(context.Background(), accountGrant.Item.ID, PatchInput{
		ExpiresAt: ptrString(f.now.Add(3 * hour).UTC().Format(time.RFC3339Nano)), ExpiresAtSet: true,
	}, accountGrant.Item.UpdatedAt, "owner"); err == nil || !strings.Contains(err.Error(), "授权到期时间不能晚于账户到期时间") {
		t.Fatalf("account bound error = %v", err)
	}
}

// Claim #5: versions are validated and canonicalized at the route, and the
// store CAS compares instants so an equivalent offset of the same instant is
// accepted (rfc3339InstantSchema '授权配置版本格式不正确', routes :33-35).
func TestPatchVersionInstantCASAndRouteNormalization(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_version", "owner")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_version",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := normalizeMutationVersion("not-a-time"); ok {
		t.Fatalf("invalid version accepted")
	}
	if _, ok := normalizeMutationVersion("   "); ok {
		t.Fatalf("blank version accepted")
	}
	if canonical, ok := normalizeMutationVersion(" 2027-01-01T09:00:00+09:00 "); !ok || canonical != "2027-01-01T00:00:00.000Z" {
		t.Fatalf("canonical version = %q ok=%v", canonical, ok)
	}
	// The stored version expressed with an equivalent offset must pass the CAS.
	version := created.Item.UpdatedAt
	parsed, err := time.Parse(time.RFC3339Nano, version)
	if err != nil {
		t.Fatal(err)
	}
	offsetVersion := parsed.In(time.FixedZone("UTC+8", 8*3600)).Format("2006-01-02T15:04:05.000Z07:00")
	paused := StatusPaused
	outcome, err := f.store.Patch(context.Background(), created.Item.ID, PatchInput{Status: &paused},
		canonicalizeAuthorizationInstant(offsetVersion), "owner")
	if err != nil || outcome.Status != "updated" {
		t.Fatalf("equivalent-offset CAS patch: %+v err=%v", outcome, err)
	}
	// A genuinely stale version conflicts.
	if outcome, err = f.store.Patch(context.Background(), created.Item.ID, PatchInput{Status: &paused},
		created.Item.UpdatedAt, "owner"); err != nil || outcome.Status != "conflict" {
		t.Fatalf("stale version patch: %+v err=%v", outcome, err)
	}
}

// Claim #8: create/patch limits normalize through the shared quota schema and
// the canonical value lands on grant, runtime and the response echo
// (requestQuotaLimitsSchema; write.repository.ts :839-844/:900-910).
func TestCreateAndPatchLimitsNormalizationAndProjection(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_limits", "owner")
	// Unknown field → verbatim normalize message.
	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_limits",
		GranteeType: "system_account", GranteeID: "grantee",
		LimitsJSON: ptrString(`{"foo":{"enabled":true,"limit":1}}`),
	}, "owner"); err == nil || !strings.Contains(err.Error(), "请求额度限制包含不支持字段：foo") {
		t.Fatalf("unknown limits field error = %v", err)
	}
	// Precision beyond 6 decimals is rejected by the schema boundary.
	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_limits",
		GranteeType: "system_account", GranteeID: "grantee",
		LimitsJSON: ptrString(`{"daily":{"enabled":true,"limit":1.23456789}}`),
	}, "owner"); err == nil || !strings.Contains(err.Error(), "日额度金额最多支持 6 位小数") {
		t.Fatalf("limits precision error = %v", err)
	}
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_limits",
		GranteeType: "system_account", GranteeID: "grantee",
		LimitsJSON: ptrString(`{"daily":{"enabled":true,"limit":10.123456}}`),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	// 10.123456 keeps its 6-decimal canonical form end to end.
	canonical := `{"daily":{"enabled":true,"limit":10.123456}}`
	var grantLimits, runtimeLimits sql.NullString
	if err := f.db.QueryRow(`SELECT limits_json FROM resource_authorization_grants WHERE id = ?`, created.Item.ID).Scan(&grantLimits); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT limits_json FROM resource_authorizations
		WHERE grantee_system_account_id = 'grantee' AND resource_id = 'grp_limits'`).Scan(&runtimeLimits); err != nil {
		t.Fatal(err)
	}
	if !grantLimits.Valid || grantLimits.String != canonical {
		t.Fatalf("grant canonical limits = %v", grantLimits)
	}
	if !runtimeLimits.Valid || runtimeLimits.String != canonical {
		t.Fatalf("runtime canonical limits = %v", runtimeLimits)
	}
	if created.Item.Limits == nil {
		t.Fatalf("create response must echo the normalized limits")
	}
	// Empty limits object and null both normalize to NULL.
	patchOutcome, err := f.store.Patch(context.Background(), created.Item.ID, PatchInput{
		LimitsJSON: ptrString(`{}`), LimitsSet: true,
	}, created.Item.UpdatedAt, "owner")
	if err != nil || patchOutcome.Status != "updated" {
		t.Fatalf("empty limits patch: %+v err=%v", patchOutcome, err)
	}
	if patchOutcome.Limits != nil {
		t.Fatalf("empty limits echo must be null: %v", patchOutcome.Limits)
	}
	if err := f.db.QueryRow(`SELECT limits_json FROM resource_authorizations
		WHERE grantee_system_account_id = 'grantee' AND resource_id = 'grp_limits'`).Scan(&runtimeLimits); err != nil {
		t.Fatal(err)
	}
	if runtimeLimits.Valid {
		t.Fatalf("runtime limits must be cleared to NULL: %v", runtimeLimits)
	}
}

// Claim #7 baseline: a limits-only patch on a terminal (revoked) grant must
// not rewrite the terminal runtime row (:1315-1331 writes no limits there).
func TestPatchLimitsKeepsTerminalRuntimeImmutable(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_terminal", "owner")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_terminal",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	mutation, err := f.store.Revoke(context.Background(), created.Item.ID, created.Item.UpdatedAt, "owner")
	if err != nil || mutation.Status != "updated" {
		t.Fatalf("revoke: %+v err=%v", mutation, err)
	}
	var before string
	if err := f.db.QueryRow(`SELECT COALESCE(limits_json,'') FROM resource_authorizations
		WHERE grantee_system_account_id = 'grantee' AND resource_id = 'grp_terminal'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Patch(context.Background(), created.Item.ID, PatchInput{
		LimitsJSON: ptrString(`{"daily":{"enabled":true,"limit":5}}`), LimitsSet: true,
	}, runtimeVersion(t, f, created.Item.ID), "owner"); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := f.db.QueryRow(`SELECT COALESCE(limits_json,'') FROM resource_authorizations
		WHERE grantee_system_account_id = 'grantee' AND resource_id = 'grp_terminal'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("terminal runtime limits changed: %q → %q", before, after)
	}
}

// Claim #11: revoke idempotency — the CAS conflict decision precedes the
// terminal short-circuit (:527-534/565-568), a version-matched revoke of an
// already-revoked grant returns unchanged with the previous status and no
// version bump.
func TestRevokeIdempotencyAndConflictOrdering(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_revoke", "owner")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_revoke",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	mutation, err := f.store.Revoke(context.Background(), created.Item.ID, created.Item.UpdatedAt, "owner")
	if err != nil || mutation.Status != "updated" || mutation.PreviousStatus == nil || *mutation.PreviousStatus != StatusActive {
		t.Fatalf("first revoke: %+v err=%v", mutation, err)
	}
	revokedVersion := mutation.Result.UpdatedAt
	// Version-matched revoke of the revoked grant → unchanged.
	repeat, err := f.store.Revoke(context.Background(), created.Item.ID, revokedVersion, "owner")
	if err != nil || repeat.Status != "unchanged" || repeat.PreviousStatus == nil || *repeat.PreviousStatus != StatusRevoked {
		t.Fatalf("idempotent revoke: %+v err=%v", repeat, err)
	}
	if runtimeVersion(t, f, created.Item.ID) != revokedVersion {
		t.Fatalf("unchanged revoke bumped the version")
	}
	// Stale version → conflict, not not_found (conflict decided first).
	stale, err := f.store.Revoke(context.Background(), created.Item.ID, created.Item.UpdatedAt, "owner")
	if err != nil || stale.Status != "conflict" || stale.CurrentUpdatedAt != revokedVersion {
		t.Fatalf("stale revoke: %+v err=%v", stale, err)
	}
}

// Claim #11: return ordering — conflict before the returned/revoked checks,
// returned + matched version → unchanged, revoked + matched version →
// not_found (return.repository.ts:170-177).
func TestReturnMutationOrderingContract(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_return", "owner")
	f.seedGroup(t, "grp_return_2", "owner")
	f.seedGroup(t, "grp_return_3", "owner")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_return",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	// Stale version on an active grant → conflict.
	stale, err := f.store.Return(context.Background(), created.Item.ID, "2001-01-01T00:00:00Z", "grantee")
	if err != nil || stale.Status != "conflict" {
		t.Fatalf("stale return: %+v err=%v", stale, err)
	}
	returned, err := f.store.Return(context.Background(), created.Item.ID, created.Item.UpdatedAt, "grantee")
	if err != nil || returned.Status != "updated" || returned.PreviousStatus == nil || *returned.PreviousStatus != StatusActive {
		t.Fatalf("return: %+v err=%v", returned, err)
	}
	returnedVersion := returned.Result.UpdatedAt
	// Stale version on the returned grant → conflict (before the unchanged
	// short-circuit).
	stale, err = f.store.Return(context.Background(), created.Item.ID, created.Item.UpdatedAt, "grantee")
	if err != nil || stale.Status != "conflict" {
		t.Fatalf("stale return on returned grant: %+v err=%v", stale, err)
	}
	repeat, err := f.store.Return(context.Background(), created.Item.ID, returnedVersion, "grantee")
	if err != nil || repeat.Status != "unchanged" || repeat.PreviousStatus == nil || *repeat.PreviousStatus != StatusReturned {
		t.Fatalf("idempotent return: %+v err=%v", repeat, err)
	}
	// Revoked grant → not_found.
	second, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_return_2",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := f.store.Revoke(context.Background(), second.Item.ID, second.Item.UpdatedAt, "owner")
	if err != nil || revoked.Status != "updated" {
		t.Fatalf("revoke: %+v err=%v", revoked, err)
	}
	missing, err := f.store.Return(context.Background(), second.Item.ID, revoked.Result.UpdatedAt, "grantee")
	if err != nil || missing.Status != "not_found" {
		t.Fatalf("return on revoked grant: %+v err=%v", missing, err)
	}
	// A grant whose runtime lost its manual source is not returnable
	// (return.repository.ts :144-148).
	third, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_return_3",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`DELETE FROM resource_authorization_sources WHERE authorization_id IN (
		SELECT id FROM resource_authorizations WHERE resource_id = 'grp_return_3')`); err != nil {
		t.Fatal(err)
	}
	noSource, err := f.store.Return(context.Background(), third.Item.ID, third.Item.UpdatedAt, "grantee")
	if err != nil || noSource.Status != "not_found" {
		t.Fatalf("return without manual source: %+v err=%v", noSource, err)
	}
}

// Claim #12: /my-authorizations resolves direction — invalid values are
// rejected (query enum, routes :45), outbound/inbound refine the self scope
// (routes :157-159 → read.repository.ts :285-299), and the admin list still
// ignores the parameter.
func TestMyAuthorizationsDirectionRouteContract(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_dir", "owner")
	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_dir",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	deps := &Deps{Store: f.store}
	request := func(url string, accountID, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(authsys.WithAuthContext(req.Context(), &authsys.AuthContext{SystemAccountID: accountID, Role: role}))
		rec := httptest.NewRecorder()
		deps.list(rec, req, !strings.Contains(url, "/authorizations?") && strings.Contains(url, "my-authorizations"))
		return rec
	}
	if rec := request("/my-authorizations?direction=sideways", "grantee", "user"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid direction status = %d", rec.Code)
	}
	inbound := request("/my-authorizations?direction=inbound", "grantee", "user")
	if inbound.Code != http.StatusOK {
		t.Fatalf("inbound status = %d body=%s", inbound.Code, inbound.Body.String())
	}
	if items := decodeListItems(t, inbound); len(items) != 1 {
		t.Fatalf("grantee inbound items = %d, want 1", len(items))
	}
	outbound := request("/my-authorizations?direction=outbound", "grantee", "user")
	if items := decodeListItems(t, outbound); len(items) != 0 {
		t.Fatalf("grantee outbound items = %d, want 0", len(items))
	}
	ownerOutbound := request("/my-authorizations?direction=outbound", "owner", "user")
	if items := decodeListItems(t, ownerOutbound); len(items) != 1 {
		t.Fatalf("owner outbound items = %d, want 1", len(items))
	}
	// The admin surface validates the enum but does not apply the filter.
	adminFiltered := request("/authorizations?direction=inbound", "owner", "admin")
	if adminFiltered.Code != http.StatusOK {
		t.Fatalf("admin filtered status = %d", adminFiltered.Code)
	}
	if items := decodeListItems(t, adminFiltered); len(items) != 1 {
		t.Fatalf("admin list must ignore direction: items = %d", len(items))
	}
}

// Claims #5/#3/#4 route layer: version normalization, presence refine and the
// strict expire schema are enforced before the store runs.
func TestAuthorizationRouteInputContract(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_route", "owner")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_route",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	deps := &Deps{Store: f.store}
	adminContext := authsys.WithAuthContext(context.Background(), &authsys.AuthContext{SystemAccountID: "owner", Role: "admin"})
	patchRequest := func(expireOnly bool, body string) *httptest.ResponseRecorder {
		url := "/authorizations/" + created.Item.ID
		if expireOnly {
			url += "/expire"
		}
		req := httptest.NewRequest(http.MethodPatch, url, strings.NewReader(body))
		req.SetPathValue("id", created.Item.ID)
		req = req.WithContext(adminContext)
		rec := httptest.NewRecorder()
		deps.patch(rec, req, expireOnly)
		return rec
	}
	if rec := patchRequest(false, `{"expectedUpdatedAt":"nope","status":"paused"}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "授权配置版本格式不正确") {
		t.Fatalf("invalid version response = %d %s", rec.Code, rec.Body.String())
	}
	if rec := patchRequest(false, `{"status":"paused"}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "授权配置版本格式不正确") {
		t.Fatalf("missing version response = %d %s", rec.Code, rec.Body.String())
	}
	if rec := patchRequest(false, `{"expectedUpdatedAt":"2027-01-01T00:00:00Z"}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "请提供要修改的授权内容") {
		t.Fatalf("no-content response = %d %s", rec.Code, rec.Body.String())
	}
	if rec := patchRequest(true, `{"expectedUpdatedAt":"2027-01-01T00:00:00Z","status":"paused"}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "修改授权参数不合法") {
		t.Fatalf("expire-with-status response = %d %s", rec.Code, rec.Body.String())
	}
	// Valid version + explicit null expiry clears through the route.
	body := `{"expectedUpdatedAt":"` + created.Item.UpdatedAt + `","expiresAt":null}`
	if rec := patchRequest(false, body); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"expiresAt":null`) {
		t.Fatalf("null-expiry patch response = %d %s", rec.Code, rec.Body.String())
	}
	// Revoke version validation.
	revokeReq := httptest.NewRequest(http.MethodDelete, "/authorizations/"+created.Item.ID, strings.NewReader(`{"expectedUpdatedAt":"bad"}`))
	revokeReq.SetPathValue("id", created.Item.ID)
	revokeReq = revokeReq.WithContext(adminContext)
	revokeRec := httptest.NewRecorder()
	deps.revoke(revokeRec, revokeReq)
	if revokeRec.Code != http.StatusBadRequest || !strings.Contains(revokeRec.Body.String(), "授权配置版本格式不正确") {
		t.Fatalf("revoke invalid version response = %d %s", revokeRec.Code, revokeRec.Body.String())
	}
}

func assertRuntime(t *testing.T, f *fixture, grantee, resourceID, status, effectiveType, revokedReason string) {
	t.Helper()
	var runtimeStatus, effective string
	var reason sql.NullString
	if err := f.db.QueryRow(`SELECT status, COALESCE(effective_source_type,''), revoked_reason
		FROM resource_authorizations
		WHERE grantee_system_account_id = ? AND resource_id = ?`, grantee, resourceID).
		Scan(&runtimeStatus, &effective, &reason); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != status || effective != effectiveType {
		t.Fatalf("runtime = status %s effective %s, want %s/%s", runtimeStatus, effective, status, effectiveType)
	}
	if revokedReason == "" {
		if reason.Valid {
			t.Fatalf("runtime revoked_reason = %v, want NULL", reason)
		}
	} else if !reason.Valid || reason.String != revokedReason {
		t.Fatalf("runtime revoked_reason = %v, want %s", reason, revokedReason)
	}
}

func runtimeVersion(t *testing.T, f *fixture, grantID string) string {
	t.Helper()
	var updatedAt string
	if err := f.db.QueryRow(`SELECT updated_at FROM resource_authorization_grants WHERE id = ?`, grantID).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	return updatedAt
}

func decodeListItems(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var payload struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Data.Items
}
