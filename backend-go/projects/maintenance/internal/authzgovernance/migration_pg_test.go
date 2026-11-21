package authzgovernance_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/authzgovernance"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

const (
	testSecret    = "authz-migration-test-secret"
	testCreatedAt = "2026-01-01T00:00:00Z"
)

func prepareAuthzSchema(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := schema.EnsurePostgres(ctx, db); err != nil {
			return err
		}
		seedAuthzFixtures(ctx, t, db)
		return nil
	})
}

func seedAuthzFixtures(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed fixture failed: %v\n%s", err, query)
		}
	}
	exec(`ALTER TABLE juhe_business.resource_authorization_sources
		DROP CONSTRAINT IF EXISTS resource_authorization_sources_accepted_revision_check`)
	exec(`INSERT INTO juhe_business.vendors (id, code, name, enabled, created_at, updated_at)
		VALUES ('vendor-gpt', 'gpt', 'GPT', 1, $1, $1)`, testCreatedAt)
	for _, id := range []string{"sys-owner", "sys-grantee"} {
		exec(`INSERT INTO juhe_business.system_accounts
			(id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES ($1, $1, $1, 'user', 'active', 'x', $2, $2)`, id, testCreatedAt)
	}
	sealedCredentials, err := accountcrypto.EncryptJSON(testSecret, map[string]any{"api_key": "sk-owner"})
	if err != nil {
		t.Fatalf("seal fixture credentials: %v", err)
	}
	exec(`INSERT INTO juhe_business.accounts
		(id, system_account_id, vendor_code, connection_driver_code, protocol_code, protocol_version,
		 name, type, status, credentials_encrypted, capability_domain, created_at, updated_at)
		VALUES ('acc-owner', 'sys-owner', 'gpt', 'gpt', 'openai', 'v1',
		 'owner account', 'api_key', 'active', $2, 'chat', $1, $1)`, testCreatedAt, sealedCredentials)
	exec(`INSERT INTO juhe_business.groups
		(id, system_account_id, name, enabled, group_kind, created_at, updated_at)
		VALUES ('grp-owner', 'sys-owner', 'owner group', 1, 'standard', $1, $1)`, testCreatedAt)
	exec(`INSERT INTO juhe_business.group_accounts
		(system_account_id, group_id, account_id, enabled, created_at, updated_at)
		VALUES ('sys-owner', 'grp-owner', 'acc-owner', 1, $1, $1)`, testCreatedAt)
	exec(`INSERT INTO juhe_business.resource_authorizations
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
		 status, created_by, created_at, updated_at)
		VALUES ('rt-group', 'group', 'grp-owner', 'sys-owner', 'sys-grantee',
		 'active', 'sys-owner', $1, $1)`, testCreatedAt)
	exec(`INSERT INTO juhe_business.resource_authorizations
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
		 status, created_by, created_at, updated_at)
		VALUES ('rt-account', 'account', 'acc-owner', 'sys-owner', 'sys-grantee',
		 'active', 'sys-owner', $1, $1)`, testCreatedAt)
	exec(`INSERT INTO juhe_business.resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
		 grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('grant-group', 'group', 'grp-owner', 'sys-owner', 'system_account',
		 'sys-grantee', 'active', 'sys-owner', $1, $1)`, testCreatedAt)
	exec(`INSERT INTO juhe_business.resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
		 grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('grant-account', 'account', 'acc-owner', 'sys-owner', 'system_account',
		 'sys-grantee', 'active', 'sys-owner', $1, $1)`, testCreatedAt)
	exec(`INSERT INTO juhe_business.resource_authorization_sources
		(id, authorization_id, source_type, status, created_by, created_at, updated_at)
		VALUES ('src-group', 'rt-group', 'manual', 'active', 'sys-owner', $1, $1)`, testCreatedAt)
	exec(`INSERT INTO juhe_business.resource_authorization_sources
		(id, authorization_id, source_type, status, created_by, created_at, updated_at)
		VALUES ('src-account', 'rt-account', 'manual', 'active', 'sys-owner', $1, $1)`, testCreatedAt)
	// Recreate the expand-phase NOT VALID constraint so Apply can validate it.
	exec(`ALTER TABLE juhe_business.resource_authorization_sources
		ADD CONSTRAINT resource_authorization_sources_accepted_revision_check
		CHECK (status NOT IN ('active','dormant') OR (accepted_source_revision IS NOT NULL AND accepted_source_revision_version IS NOT NULL)) NOT VALID`)
}

func TestApplyLegacyGovernanceBackfillOnPostgres(t *testing.T) {
	db := prepareAuthzSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	before, err := authzgovernance.Satisfied(ctx, db, testSecret)
	if err != nil {
		t.Fatalf("Satisfied before migration: %v", err)
	}
	if before {
		t.Fatal("fresh legacy fixture must not satisfy revision 28 before migration")
	}

	result, err := authzgovernance.Apply(ctx, db, authzgovernance.Options{
		Secret: testSecret,
		Now:    time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.SourcesMapped != 2 || result.SourcesRevisionBackfilled != 2 {
		t.Fatalf("unexpected source backfill result: %+v", result)
	}
	if result.GrantsLegacyMarked != 2 || result.LegacyNotifications != 2 {
		t.Fatalf("unexpected grant/notification result: %+v", result)
	}
	if result.GroupSnapshotsCreated < 1 || result.GroupSettingsBackfilled != 1 {
		t.Fatalf("unexpected group snapshot result: %+v", result)
	}

	var acceptanceMode string
	var acceptedAt sql.NullString
	var legacyReviewDeadline sql.NullString
	var sourceRevision sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT acceptance_mode, accepted_at,
			legacy_review_deadline, source_revision
		FROM juhe_business.resource_authorization_grants WHERE id = 'grant-group'`).
		Scan(&acceptanceMode, &acceptedAt, &legacyReviewDeadline, &sourceRevision); err != nil {
		t.Fatalf("read migrated grant: %v", err)
	}
	if acceptanceMode != "legacy" || !acceptedAt.Valid || !legacyReviewDeadline.Valid || !sourceRevision.Valid {
		t.Fatalf("grant legacy fields not backfilled: mode=%s accepted=%v deadline=%v revision=%v",
			acceptanceMode, acceptedAt, legacyReviewDeadline, sourceRevision)
	}

	var sourceGrantID string
	var sourceAcceptedRevision sql.NullString
	var sourceAcceptedVersion sql.NullInt64
	var sourceRevisionVersion int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(grant_id, ''), accepted_source_revision,
			accepted_source_revision_version, revision_version
		FROM juhe_business.resource_authorization_sources WHERE id = 'src-group'`).
		Scan(&sourceGrantID, &sourceAcceptedRevision, &sourceAcceptedVersion, &sourceRevisionVersion); err != nil {
		t.Fatalf("read migrated source: %v", err)
	}
	if sourceGrantID != "grant-group" || !sourceAcceptedRevision.Valid || !sourceAcceptedVersion.Valid || sourceRevisionVersion < 1 {
		t.Fatalf("source revision fields not backfilled: grant=%s revision=%v version=%v revisionVersion=%d",
			sourceGrantID, sourceAcceptedRevision, sourceAcceptedVersion, sourceRevisionVersion)
	}

	var accountSourceGrantID string
	var accountSourceRevision sql.NullString
	var accountAcceptedVersion sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(grant_id, ''), accepted_source_revision,
			accepted_source_revision_version
		FROM juhe_business.resource_authorization_sources WHERE id = 'src-account'`).
		Scan(&accountSourceGrantID, &accountSourceRevision, &accountAcceptedVersion); err != nil {
		t.Fatalf("read migrated account source: %v", err)
	}
	if accountSourceGrantID != "grant-account" || !accountSourceRevision.Valid || !accountAcceptedVersion.Valid {
		t.Fatalf("account source revision fields not backfilled: grant=%s revision=%v version=%v",
			accountSourceGrantID, accountSourceRevision, accountAcceptedVersion)
	}

	var enabled int
	var snapshotMode string
	var snapshotRevision sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT enabled, snapshot_mode, snapshot_revision
		FROM juhe_business.group_authorization_settings WHERE authorization_id = 'rt-group'`).
		Scan(&enabled, &snapshotMode, &snapshotRevision); err != nil {
		t.Fatalf("read group settings: %v", err)
	}
	if enabled != 1 || snapshotMode != "normal" || !snapshotRevision.Valid {
		t.Fatalf("legacy active group must keep enabled=1 with snapshot: enabled=%d mode=%s revision=%v",
			enabled, snapshotMode, snapshotRevision)
	}
	var snapshotAccounts int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_group_accounts
		WHERE authorization_id = 'rt-group'`).Scan(&snapshotAccounts); err != nil {
		t.Fatalf("count snapshot accounts: %v", err)
	}
	if snapshotAccounts != 1 {
		t.Fatalf("snapshot account count = %d, want 1", snapshotAccounts)
	}

	var notifications int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.authorization_notifications
		WHERE type = 'legacy_review_required' AND system_account_id = 'sys-grantee'`).Scan(&notifications); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if notifications != 2 {
		t.Fatalf("legacy review notification count = %d, want 2", notifications)
	}

	after, err := authzgovernance.Satisfied(ctx, db, testSecret)
	if err != nil {
		t.Fatalf("Satisfied after migration: %v", err)
	}
	if !after {
		t.Fatal("migrated fixture must satisfy revision 28")
	}
	if err := authzgovernance.Verify(ctx, db, testSecret); err != nil {
		t.Fatalf("Verify after migration: %v", err)
	}

	// Idempotency: a second apply must not duplicate snapshots or notifications.
	if _, err := authzgovernance.Apply(ctx, db, authzgovernance.Options{
		Secret: testSecret,
		Now:    time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	var snapshotRows, notificationRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_group_accounts
		WHERE authorization_id = 'rt-group'`).Scan(&snapshotRows); err != nil {
		t.Fatalf("count snapshot rows after second apply: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.authorization_notifications
		WHERE type = 'legacy_review_required' AND system_account_id = 'sys-grantee'`).Scan(&notificationRows); err != nil {
		t.Fatalf("count notifications after second apply: %v", err)
	}
	if snapshotRows != 1 || notificationRows != 2 {
		t.Fatalf("second apply duplicated rows: snapshots=%d notifications=%d", snapshotRows, notificationRows)
	}
}

func TestApplyBlocksAmbiguousLegacySourcesOnPostgres(t *testing.T) {
	db := prepareAuthzSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// A second grant for the same group/grantee whose lifecycle overlaps the
	// orphan source makes grant_id unmatchable; the migration must block
	// instead of guessing.
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
		 grantee_system_account_id, status, created_by, created_at, ended_at, updated_at)
		VALUES ('grant-group-dup', 'group', 'grp-owner', 'sys-owner', 'system_account',
		 'sys-grantee', 'expired', 'sys-owner', '2025-01-01T00:00:00Z', '2026-06-01T00:00:00Z', $1)`,
		testCreatedAt); err != nil {
		t.Fatalf("insert overlapping grant: %v", err)
	}

	_, err := authzgovernance.Apply(ctx, db, authzgovernance.Options{Secret: testSecret})
	if !errors.Is(err, authzgovernance.ErrNeedsReview) {
		t.Fatalf("ambiguous source must return ErrNeedsReview, got %v", err)
	}

	var grantID sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT grant_id FROM juhe_business.resource_authorization_sources
		WHERE id = 'src-group'`).Scan(&grantID); err != nil {
		t.Fatalf("read source after blocked migration: %v", err)
	}
	if grantID.Valid {
		t.Fatalf("blocked migration must roll back grant_id backfill, got %q", grantID.String)
	}
	var settings int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.group_authorization_settings
		WHERE authorization_id = 'rt-group'`).Scan(&settings); err != nil {
		t.Fatalf("count settings after blocked migration: %v", err)
	}
	if settings != 0 {
		t.Fatalf("blocked migration must not write group settings, got %d", settings)
	}
}
