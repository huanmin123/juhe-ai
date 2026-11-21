package authzgovernance_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/authzgovernance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
	"github.com/huanminabc/juhe-ai/backend-go-platform/authzrevision"
)

func execP2Fixture(ctx context.Context, t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("p2 fixture failed: %v\n%s", err, query)
	}
}

func applyP2Migration(ctx context.Context, t *testing.T, db *sql.DB) authzgovernance.Result {
	t.Helper()
	result, err := authzgovernance.Apply(ctx, db, authzgovernance.Options{
		Secret: testSecret,
		Now:    time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return result
}

func canonicalSnapshotRevision(secret string, parts []string) string {
	sort.Strings(parts)
	mac := hmac.New(sha256.New, []byte(secret))
	for _, part := range parts {
		mac.Write([]byte(part))
		mac.Write([]byte{0})
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// The group settings snapshot_revision must be the canonical HMAC over the
// accepted snapshot rows, including revision_version — the value the gateway
// later compares for group material changes.
func TestApplyGroupSnapshotRevisionMatchesCanonicalHMACOnPostgres(t *testing.T) {
	db := prepareAuthzSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	applyP2Migration(ctx, t, db)

	revision, version, _, err := authzrevision.Compute(ctx, db, "acc-owner", testSecret)
	if err != nil {
		t.Fatalf("compute account revision: %v", err)
	}
	wantRevision := canonicalSnapshotRevision(testSecret, []string{
		"acc-owner\x00" + revision + "\x00" + strconv.Itoa(version),
	})

	var gotRevision, gotAccountRevision string
	var gotVersion int
	if err := db.QueryRowContext(ctx, `SELECT snapshot_revision
		FROM juhe_business.group_authorization_settings WHERE authorization_id = 'rt-group'`).
		Scan(&gotRevision); err != nil {
		t.Fatalf("read settings snapshot revision: %v", err)
	}
	if gotRevision != wantRevision {
		t.Fatalf("snapshot_revision = %q, want canonical HMAC %q", gotRevision, wantRevision)
	}
	if err := db.QueryRowContext(ctx, `SELECT source_revision, revision_version
		FROM juhe_business.resource_authorization_group_accounts
		WHERE authorization_id = 'rt-group' AND account_id = 'acc-owner'`).
		Scan(&gotAccountRevision, &gotVersion); err != nil {
		t.Fatalf("read snapshot account revision: %v", err)
	}
	if gotAccountRevision != revision || gotVersion != version {
		t.Fatalf("snapshot account revision = %q/%d, want %q/%d",
			gotAccountRevision, gotVersion, revision, version)
	}
}

// superseded/covered_by_team rows become dormant (manual) or active (team).
// The expand-phase accepted-revision CHECK is enforced on that UPDATE, so the
// backfill must give these rows their accepted revision before the status map.
func TestApplyMapsCoveredByTeamSourcesWithAcceptedRevisionCheckOnPostgres(t *testing.T) {
	db := prepareAuthzSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.system_teams
		(id, name, status, created_by, created_at, updated_at)
		VALUES ('team-covered', 'team covered', 'active', 'sys-owner', $1, $1)`, testCreatedAt)

	for _, fixture := range []struct {
		key        string
		sourceType string
		teamID     sql.NullString
	}{
		{key: "manual", sourceType: "manual"},
		{key: "team", sourceType: "team", teamID: sql.NullString{String: "team-covered", Valid: true}},
	} {
		groupID := "grp-covered-" + fixture.key
		runtimeID := "rt-covered-" + fixture.key
		grantID := "grant-covered-" + fixture.key
		sourceID := "src-covered-" + fixture.key
		execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.groups
			(id, system_account_id, name, enabled, group_kind, created_at, updated_at)
			VALUES ($1, 'sys-owner', $1, 1, 'standard', $2, $2)`, groupID, testCreatedAt)
		execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.resource_authorizations
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
			 status, created_by, created_at, updated_at)
			VALUES ($1, 'group', $2, 'sys-owner', 'sys-grantee', 'active', 'sys-owner', $3, $3)`,
			runtimeID, groupID, testCreatedAt)
		granteeType := "system_account"
		granteeUser := "sys-grantee"
		if fixture.sourceType == "team" {
			granteeType = "team"
			granteeUser = ""
		}
		execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.resource_authorization_grants
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
			 grantee_system_account_id, grantee_team_id, status, created_by, created_at, updated_at)
			VALUES ($1, 'group', $2, 'sys-owner', $3, NULLIF($4, ''), NULLIF($5, ''), 'active', 'sys-owner', $6, $6)`,
			grantID, groupID, granteeType, granteeUser, fixture.teamID.String, testCreatedAt)
		execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.resource_authorization_sources
			(id, authorization_id, source_type, source_team_id, status, ended_reason,
			 created_by, created_at, updated_at)
			VALUES ($1, $2, $3, NULLIF($4, ''), 'superseded', 'covered_by_team', 'sys-owner', $5, $5)`,
			sourceID, runtimeID, fixture.sourceType, fixture.teamID.String, testCreatedAt)
	}

	applyP2Migration(ctx, t, db)

	for _, fixture := range []struct {
		key        string
		sourceType string
		wantStatus string
	}{
		{key: "manual", sourceType: "manual", wantStatus: "dormant"},
		{key: "team", sourceType: "team", wantStatus: "active"},
	} {
		sourceID := "src-covered-" + fixture.key
		grantID := "grant-covered-" + fixture.key
		var status, acceptedRevision, observedRevision string
		var acceptedVersion, revisionVersion int
		var mappedGrantID sql.NullString
		var endedReason sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT status, COALESCE(accepted_source_revision, ''),
				COALESCE(observed_source_revision, ''), COALESCE(accepted_source_revision_version, 0),
				revision_version, grant_id, ended_reason
			FROM juhe_business.resource_authorization_sources WHERE id = $1`, sourceID).
			Scan(&status, &acceptedRevision, &observedRevision, &acceptedVersion, &revisionVersion,
				&mappedGrantID, &endedReason); err != nil {
			t.Fatalf("read %s: %v", sourceID, err)
		}
		if status != fixture.wantStatus {
			t.Fatalf("%s status = %q, want %q", sourceID, status, fixture.wantStatus)
		}
		if acceptedRevision == "" || observedRevision == "" || acceptedVersion == 0 || revisionVersion == 0 {
			t.Fatalf("%s live source missing accepted revision: accepted=%q observed=%q acceptedVersion=%d revisionVersion=%d",
				sourceID, acceptedRevision, observedRevision, acceptedVersion, revisionVersion)
		}
		if !mappedGrantID.Valid || mappedGrantID.String != grantID {
			t.Fatalf("%s grant_id = %v, want %s", sourceID, mappedGrantID, grantID)
		}
		if endedReason.Valid {
			t.Fatalf("%s live mapped source must clear ended_reason, got %q", sourceID, endedReason.String)
		}
	}
}

// Live legacy sources whose parent grant is already terminal must become
// terminal too. Expired parents additionally clear the accepted revision so a
// later extend restores the source through needs_review instead of reviving
// stale material.
func TestApplyMapsTerminalParentSourcesOnPostgres(t *testing.T) {
	db := prepareAuthzSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	terminalGrants := []struct {
		id     string
		status string
	}{
		{"grant-parent-expired", "expired"},
		{"grant-parent-returned", "returned"},
		{"grant-parent-revoked", "revoked"},
	}
	for _, grant := range terminalGrants {
		groupID := "grp-parent-" + grant.status
		runtimeID := "rt-parent-" + grant.status
		execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.groups
			(id, system_account_id, name, enabled, group_kind, created_at, updated_at)
			VALUES ($1, 'sys-owner', $1, 1, 'standard', $2, $2)`, groupID, testCreatedAt)
		execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.resource_authorizations
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
			 status, created_by, created_at, updated_at)
			VALUES ($1, 'group', $2, 'sys-owner', 'sys-grantee', 'active', 'sys-owner', $3, $3)`,
			runtimeID, groupID, testCreatedAt)
		execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.resource_authorization_grants
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
			 grantee_system_account_id, status, created_by, created_at, ended_at, ended_reason, updated_at)
			VALUES ($1, 'group', $2, 'sys-owner', 'system_account',
			 'sys-grantee', $3, 'sys-owner', $4, $4, 'legacy_terminal', $4)`,
			grant.id, groupID, grant.status, testCreatedAt)
		execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.resource_authorization_sources
			(id, grant_id, authorization_id, source_type, status, accepted_source_revision,
			 accepted_source_revision_version, observed_source_revision, revision_version,
			 created_by, created_at, updated_at)
			VALUES ($1, $2, $3, 'manual', 'active', 'legacy-revision', 1, 'legacy-revision', 1,
			 'sys-owner', $4, $4)`, "src-parent-"+grant.status, grant.id, runtimeID, testCreatedAt)
	}

	applyP2Migration(ctx, t, db)

	for _, grant := range terminalGrants {
		sourceID := "src-parent-" + grant.status
		var status, endedReason string
		var endedAt, acceptedRevision sql.NullString
		var expiredFrom sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT status, ended_reason, ended_at,
				accepted_source_revision, expired_from_status
			FROM juhe_business.resource_authorization_sources WHERE id = $1`, sourceID).
			Scan(&status, &endedReason, &endedAt, &acceptedRevision, &expiredFrom); err != nil {
			t.Fatalf("read %s: %v", sourceID, err)
		}
		if status != grant.status {
			t.Fatalf("%s status = %q, want %q", sourceID, status, grant.status)
		}
		wantReason := "authorization_" + grant.status
		if endedReason != wantReason {
			t.Fatalf("%s ended_reason = %q, want %q", sourceID, endedReason, wantReason)
		}
		if !endedAt.Valid || endedAt.String == "" {
			t.Fatalf("%s must get ended_at from the source timestamps", sourceID)
		}
		if grant.status == "expired" {
			if acceptedRevision.Valid {
				t.Fatalf("%s expired source must clear accepted revision, got %q", sourceID, acceptedRevision.String)
			}
			if !expiredFrom.Valid || expiredFrom.String != "active" {
				t.Fatalf("%s expired_from_status = %v, want active", sourceID, expiredFrom)
			}
		}
	}
}

// A legacy active group runtime without accounts still gets settings: the
// canonical empty snapshot hash, enabled=1 and no snapshot rows.
func TestApplyCreatesCanonicalEmptyGroupSnapshotOnPostgres(t *testing.T) {
	db := prepareAuthzSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.groups
		(id, system_account_id, name, enabled, group_kind, created_at, updated_at)
		VALUES ('grp-empty', 'sys-owner', 'empty group', 1, 'standard', $1, $1)`, testCreatedAt)
	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.resource_authorizations
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
		 status, created_by, created_at, updated_at)
		VALUES ('rt-empty', 'group', 'grp-empty', 'sys-owner', 'sys-grantee',
		 'active', 'sys-owner', $1, $1)`, testCreatedAt)
	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
		 grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('grant-empty', 'group', 'grp-empty', 'sys-owner', 'system_account',
		 'sys-grantee', 'active', 'sys-owner', $1, $1)`, testCreatedAt)
	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.resource_authorization_sources
		(id, authorization_id, source_type, status, accepted_source_revision,
		 accepted_source_revision_version, observed_source_revision, revision_version,
		 created_by, created_at, updated_at)
		VALUES ('src-empty', 'rt-empty', 'manual', 'active', 'legacy-revision', 1, 'legacy-revision', 1,
		 'sys-owner', $1, $1)`, testCreatedAt)

	result := applyP2Migration(ctx, t, db)
	if result.GroupSnapshotsCreated < 1 || result.GroupSettingsBackfilled < 2 {
		t.Fatalf("empty group snapshot not applied: %+v", result)
	}

	var enabled int
	var snapshotMode string
	var snapshotRevision string
	if err := db.QueryRowContext(ctx, `SELECT enabled, snapshot_mode, snapshot_revision
		FROM juhe_business.group_authorization_settings WHERE authorization_id = 'rt-empty'`).
		Scan(&enabled, &snapshotMode, &snapshotRevision); err != nil {
		t.Fatalf("read empty group settings: %v", err)
	}
	wantRevision := canonicalSnapshotRevision(testSecret, nil)
	if enabled != 1 || snapshotMode != "normal" || snapshotRevision != wantRevision {
		t.Fatalf("empty group settings = enabled %d mode %q revision %q, want 1/normal/%q",
			enabled, snapshotMode, snapshotRevision, wantRevision)
	}
	var snapshotRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_group_accounts
		WHERE authorization_id = 'rt-empty'`).Scan(&snapshotRows); err != nil {
		t.Fatalf("count empty snapshot rows: %v", err)
	}
	if snapshotRows != 0 {
		t.Fatalf("empty group snapshot rows = %d, want 0", snapshotRows)
	}
}

// A grant whose account has been soft-deleted is an orphan: the grant, source
// and runtime are revoked, recipient group bindings are disabled, and derived
// account instances stop being schedulable.
func TestApplyRevokesOrphanAccountResourcesOnPostgres(t *testing.T) {
	db := prepareAuthzSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	sealedCredentials, err := accountcrypto.EncryptJSON(testSecret, map[string]any{"api_key": "sk-doomed"})
	if err != nil {
		t.Fatalf("seal orphan credentials: %v", err)
	}
	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.accounts
		(id, system_account_id, vendor_code, connection_driver_code, protocol_code, protocol_version,
		 name, type, status, credentials_encrypted, capability_domain, deleted_at, created_at, updated_at)
		VALUES ('acc-doomed', 'sys-owner', 'gpt', 'gpt', 'openai', 'v1',
		 'doomed account', 'api_key', 'active', $2, 'chat', $1, $1, $1)`, testCreatedAt, sealedCredentials)
	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.resource_authorizations
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
		 status, created_by, created_at, updated_at)
		VALUES ('rt-doomed', 'account', 'acc-doomed', 'sys-owner', 'sys-grantee',
		 'active', 'sys-owner', $1, $1)`, testCreatedAt)
	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.accounts
		(id, system_account_id, vendor_code, connection_driver_code, protocol_code, protocol_version,
		 name, type, status, credentials_encrypted, capability_domain, schedulable,
		 authorization_instance_source_account_id, authorization_instance_authorization_id,
		 authorization_instance_owner_system_account_id, created_at, updated_at)
		VALUES ('acc-doomed-inst', 'sys-grantee', 'gpt', 'gpt', 'openai', 'v1',
		 'doomed instance', 'api_key', 'active', $2, 'chat', 1,
		 'acc-doomed', 'rt-doomed', 'sys-owner', $1, $1)`, testCreatedAt, sealedCredentials)
	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.groups
		(id, system_account_id, name, enabled, group_kind, created_at, updated_at)
		VALUES ('grp-grantee', 'sys-grantee', 'grantee group', 1, 'standard', $1, $1)`, testCreatedAt)
	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.group_accounts
		(system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at)
		VALUES ('sys-grantee', 'grp-grantee', 'acc-doomed-inst', 'rt-doomed', 1, $1, $1)`, testCreatedAt)
	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
		 grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('grant-doomed', 'account', 'acc-doomed', 'sys-owner', 'system_account',
		 'sys-grantee', 'active', 'sys-owner', $1, $1)`, testCreatedAt)
	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.resource_authorization_sources
		(id, authorization_id, source_type, status, accepted_source_revision,
		 accepted_source_revision_version, observed_source_revision, revision_version,
		 created_by, created_at, updated_at)
		VALUES ('src-doomed', 'rt-doomed', 'manual', 'active', 'legacy-revision', 1, 'legacy-revision', 1,
		 'sys-owner', $1, $1)`, testCreatedAt)

	applyP2Migration(ctx, t, db)

	var grantStatus, grantReason string
	var grantEndedAt sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT status, ended_reason, ended_at
		FROM juhe_business.resource_authorization_grants WHERE id = 'grant-doomed'`).
		Scan(&grantStatus, &grantReason, &grantEndedAt); err != nil {
		t.Fatalf("read orphan grant: %v", err)
	}
	if grantStatus != "revoked" || grantReason != "resource_deleted" || !grantEndedAt.Valid {
		t.Fatalf("orphan grant = %q/%q/%v, want revoked/resource_deleted/ended_at", grantStatus, grantReason, grantEndedAt)
	}
	var sourceStatus, sourceReason string
	var sourceEndedAt sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT status, ended_reason, ended_at
		FROM juhe_business.resource_authorization_sources WHERE id = 'src-doomed'`).
		Scan(&sourceStatus, &sourceReason, &sourceEndedAt); err != nil {
		t.Fatalf("read orphan source: %v", err)
	}
	if sourceStatus != "revoked" || sourceReason != "resource_deleted" || !sourceEndedAt.Valid {
		t.Fatalf("orphan source = %q/%q/%v, want revoked/resource_deleted/ended_at",
			sourceStatus, sourceReason, sourceEndedAt)
	}
	var runtimeStatus, runtimeReason string
	if err := db.QueryRowContext(ctx, `SELECT status, status_reason
		FROM juhe_business.resource_authorizations WHERE id = 'rt-doomed'`).
		Scan(&runtimeStatus, &runtimeReason); err != nil {
		t.Fatalf("read orphan runtime: %v", err)
	}
	if runtimeStatus != "revoked" || runtimeReason != "resource_deleted" {
		t.Fatalf("orphan runtime = %q/%q, want revoked/resource_deleted", runtimeStatus, runtimeReason)
	}
	var bindingEnabled int
	if err := db.QueryRowContext(ctx, `SELECT enabled FROM juhe_business.group_accounts
		WHERE group_id = 'grp-grantee' AND account_id = 'acc-doomed-inst'`).
		Scan(&bindingEnabled); err != nil {
		t.Fatalf("read orphan binding: %v", err)
	}
	if bindingEnabled != 0 {
		t.Fatalf("orphan recipient binding enabled = %d, want 0", bindingEnabled)
	}
	var instanceStatus string
	var instanceSchedulable int
	if err := db.QueryRowContext(ctx, `SELECT status, schedulable FROM juhe_business.accounts
		WHERE id = 'acc-doomed-inst'`).Scan(&instanceStatus, &instanceSchedulable); err != nil {
		t.Fatalf("read orphan instance: %v", err)
	}
	if instanceStatus != "disabled" || instanceSchedulable != 0 {
		t.Fatalf("orphan instance = %q/%d, want disabled/0", instanceStatus, instanceSchedulable)
	}
}

// Mapping superseded/covered_by_team manual sources to dormant can collide with
// an already active manual source on the same runtime. The expand phase creates
// the open-status unique index before this data step, so the migration must
// preflight that collision and block for review instead of failing halfway
// through the status mapping with a raw duplicate-key error.
func TestApplyBlocksLiveSourceKeyCollisionOnPostgres(t *testing.T) {
	db := prepareAuthzSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.groups
		(id, system_account_id, name, enabled, group_kind, created_at, updated_at)
		VALUES ('grp-collision', 'sys-owner', 'collision', 1, 'standard', $1, $1)`, testCreatedAt)
	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.resource_authorizations
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
		 status, created_by, created_at, updated_at)
		VALUES ('rt-collision', 'group', 'grp-collision', 'sys-owner', 'sys-grantee', 'active', 'sys-owner', $1, $1)`,
		testCreatedAt)
	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
		 grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('grant-collision', 'group', 'grp-collision', 'sys-owner', 'system_account',
		 'sys-grantee', 'active', 'sys-owner', $1, $1)`, testCreatedAt)
	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.resource_authorization_sources
		(id, authorization_id, grant_id, source_type, status, accepted_source_revision,
		 accepted_source_revision_version, created_by, created_at, updated_at)
		VALUES ('src-collision-active', 'rt-collision', 'grant-collision', 'manual', 'active',
		 'seed-revision', 1, 'sys-owner', $1, $1)`, testCreatedAt)
	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.resource_authorization_sources
		(id, authorization_id, grant_id, source_type, status, ended_reason, created_by, created_at, updated_at)
		VALUES ('src-collision-superseded', 'rt-collision', 'grant-collision', 'manual', 'superseded',
		 'covered_by_team', 'sys-owner', $1, $1)`, testCreatedAt)

	result, err := authzgovernance.Apply(ctx, db, authzgovernance.Options{
		Secret: testSecret,
		Now:    time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, authzgovernance.ErrNeedsReview) {
		t.Fatalf("Apply error = %v, want ErrNeedsReview", err)
	}
	reasons := strings.Join(result.BlockedReasons, "\n")
	if !strings.Contains(reasons, "src-collision-active") || !strings.Contains(reasons, "src-collision-superseded") {
		t.Fatalf("blocked reasons must name both colliding sources: %q", reasons)
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM juhe_business.resource_authorization_sources
		WHERE id = 'src-collision-superseded'`).Scan(&status); err != nil {
		t.Fatalf("read blocked source: %v", err)
	}
	if status != "superseded" {
		t.Fatalf("blocked migration must roll back status mapping, got %q", status)
	}
}

// Missing grantee accounts/teams must block with row ids before the RESTRICT
// foreign-key validation, not fail the migration with an opaque FK error.
func TestApplyBlocksForeignKeyOrphanGranteeOnPostgres(t *testing.T) {
	db := prepareAuthzSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	execP2Fixture(ctx, t, db, `ALTER TABLE juhe_business.resource_authorization_grants
		DROP CONSTRAINT IF EXISTS resource_authorization_grants_grantee_system_account_id_fkey`)
	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.groups
		(id, system_account_id, name, enabled, group_kind, created_at, updated_at)
		VALUES ('grp-orphan-grantee', 'sys-owner', 'orphan grantee', 1, 'standard', $1, $1)`, testCreatedAt)
	execP2Fixture(ctx, t, db, `INSERT INTO juhe_business.resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
		 grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('grant-orphan-grantee', 'group', 'grp-orphan-grantee', 'sys-owner', 'system_account',
		 'missing-grantee', 'active', 'sys-owner', $1, $1)`, testCreatedAt)

	result, err := authzgovernance.Apply(ctx, db, authzgovernance.Options{
		Secret: testSecret,
		Now:    time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, authzgovernance.ErrNeedsReview) {
		t.Fatalf("Apply error = %v, want ErrNeedsReview", err)
	}
	reasons := strings.Join(result.BlockedReasons, "\n")
	if !strings.Contains(reasons, "grant-orphan-grantee") || !strings.Contains(reasons, "missing-grantee") {
		t.Fatalf("blocked reasons must identify the orphan grant/grantee: %q", reasons)
	}
}
