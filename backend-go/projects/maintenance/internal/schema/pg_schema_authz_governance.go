// 授权接收方确认与来源治理设计（2026-11-20，schema revision 28）的
// PostgreSQL expand 相 DDL。所有语句保持幂等，只做加法列、新表、新索引和
// 可回滚的约束收紧；历史终态回填、grant_id 匹配、快照建立和 legacy 复核
// 由 maintenance upgrade revision 28 的数据步骤完成。
//
// 约束一律先以 NOT VALID 建立，数据步骤回填后再 VALIDATE：存量授权存在
// 终态行缺少 ended_at、active 来源缺少 accepted_source_revision 等情况，
// 直接 VALIDATE 会阻断结构升级。新库同样执行这些语句，最终形状一致。
package schema

var postgresSchemaAuthzGovernance = []PGStatement{
	// ---------- 系统账户 / 团队逻辑删除 ----------
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-system-accounts-deleted-at",
		SQL:        `ALTER TABLE juhe_business.system_accounts ADD COLUMN IF NOT EXISTS deleted_at text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-system-teams-deleted-at",
		SQL:        `ALTER TABLE juhe_business.system_teams ADD COLUMN IF NOT EXISTS deleted_at text`,
	},
	{
		// 建表内联 UNIQUE 生成 system_accounts_username_key；改为部分唯一索引，
		// 允许逻辑删除后的用户名复用。explicit lower(username) 索引同样替换。
		SchemaName: "juhe_business",
		Source:     "authz-governance-system-accounts-username-unique",
		SQL: `DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class r ON r.oid = c.conrelid
    JOIN pg_namespace n ON n.oid = r.relnamespace
    WHERE n.nspname = 'juhe_business' AND r.relname = 'system_accounts'
      AND c.conname = 'system_accounts_username_key'
  ) THEN
    ALTER TABLE juhe_business.system_accounts DROP CONSTRAINT system_accounts_username_key;
  END IF;
END
$$`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-system-accounts-username-index",
		SQL:        `DROP INDEX IF EXISTS juhe_business.idx_system_accounts_username_unique_lower`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-system-accounts-username-index-rebuild",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_username_active_unique_lower ON juhe_business.system_accounts(lower(username)) WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-system-teams-name-index",
		SQL:        `DROP INDEX IF EXISTS juhe_business.idx_system_teams_name_unique`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-system-teams-name-index-rebuild",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_system_teams_name_active_unique ON juhe_business.system_teams(name) WHERE deleted_at IS NULL`,
	},

	// ---------- grants ----------
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-acceptance-mode",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_grants ADD COLUMN IF NOT EXISTS acceptance_mode text NOT NULL DEFAULT 'legacy'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-accepted-at",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_grants ADD COLUMN IF NOT EXISTS accepted_at text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-accepted-by",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_grants ADD COLUMN IF NOT EXISTS accepted_by text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-team-approved-at",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_grants ADD COLUMN IF NOT EXISTS team_approved_at text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-team-approved-by",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_grants ADD COLUMN IF NOT EXISTS team_approved_by text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-owner-approved-at",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_grants ADD COLUMN IF NOT EXISTS owner_approved_at text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-owner-approved-by",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_grants ADD COLUMN IF NOT EXISTS owner_approved_by text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-source-revision",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_grants ADD COLUMN IF NOT EXISTS source_revision text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-expiry-cycle",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_grants ADD COLUMN IF NOT EXISTS expiry_cycle integer NOT NULL DEFAULT 0`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-pending-expires-at",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_grants ADD COLUMN IF NOT EXISTS pending_expires_at text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-expired-from-status",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_grants ADD COLUMN IF NOT EXISTS expired_from_status text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-ended-at",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_grants ADD COLUMN IF NOT EXISTS ended_at text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-ended-reason",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_grants ADD COLUMN IF NOT EXISTS ended_reason text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-legacy-basis",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_grants ADD COLUMN IF NOT EXISTS legacy_basis text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-legacy-review-deadline",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_grants ADD COLUMN IF NOT EXISTS legacy_review_deadline text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-status-check",
		SQL: `DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class r ON r.oid = c.conrelid
    JOIN pg_namespace n ON n.oid = r.relnamespace
    WHERE n.nspname = 'juhe_business' AND r.relname = 'resource_authorization_grants'
      AND c.conname = 'resource_authorization_grants_status_check'
  ) THEN
    ALTER TABLE juhe_business.resource_authorization_grants
      ADD CONSTRAINT resource_authorization_grants_status_check
      CHECK (status IN ('pending_owner_approval','pending','pending_team_approval','pending_member_acceptance','active','paused','expired','revoked','returned','declined')) NOT VALID;
  END IF;
END
$$`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-acceptance-mode-check",
		SQL: `DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class r ON r.oid = c.conrelid
    JOIN pg_namespace n ON n.oid = r.relnamespace
    WHERE n.nspname = 'juhe_business' AND r.relname = 'resource_authorization_grants'
      AND c.conname = 'resource_authorization_grants_acceptance_mode_check'
  ) THEN
    ALTER TABLE juhe_business.resource_authorization_grants
      ADD CONSTRAINT resource_authorization_grants_acceptance_mode_check
      CHECK (acceptance_mode IN ('recipient','legacy')) NOT VALID;
  END IF;
END
$$`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-pending-expiry-check",
		SQL: `DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class r ON r.oid = c.conrelid
    JOIN pg_namespace n ON n.oid = r.relnamespace
    WHERE n.nspname = 'juhe_business' AND r.relname = 'resource_authorization_grants'
      AND c.conname = 'resource_authorization_grants_pending_expiry_check'
  ) THEN
    ALTER TABLE juhe_business.resource_authorization_grants
      ADD CONSTRAINT resource_authorization_grants_pending_expiry_check
      CHECK (status NOT IN ('pending_owner_approval','pending','pending_team_approval') OR pending_expires_at IS NOT NULL) NOT VALID;
  END IF;
END
$$`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-ended-at-check",
		SQL: `DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class r ON r.oid = c.conrelid
    JOIN pg_namespace n ON n.oid = r.relnamespace
    WHERE n.nspname = 'juhe_business' AND r.relname = 'resource_authorization_grants'
      AND c.conname = 'resource_authorization_grants_ended_at_check'
  ) THEN
    ALTER TABLE juhe_business.resource_authorization_grants
      ADD CONSTRAINT resource_authorization_grants_ended_at_check
      CHECK (status NOT IN ('expired','revoked','returned','declined') OR ended_at IS NOT NULL) NOT VALID;
  END IF;
END
$$`,
	},

	// ---------- runtime ----------
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-runtime-status-reason",
		SQL:        `ALTER TABLE juhe_business.resource_authorizations ADD COLUMN IF NOT EXISTS status_reason text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-runtime-status-changed-at",
		SQL:        `ALTER TABLE juhe_business.resource_authorizations ADD COLUMN IF NOT EXISTS status_changed_at text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-runtime-acceptance-mode",
		SQL:        `ALTER TABLE juhe_business.resource_authorizations ADD COLUMN IF NOT EXISTS acceptance_mode text NOT NULL DEFAULT 'legacy'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-runtime-status-check",
		SQL: `DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class r ON r.oid = c.conrelid
    JOIN pg_namespace n ON n.oid = r.relnamespace
    WHERE n.nspname = 'juhe_business' AND r.relname = 'resource_authorizations'
      AND c.conname = 'resource_authorizations_status_check'
  ) THEN
    ALTER TABLE juhe_business.resource_authorizations
      ADD CONSTRAINT resource_authorizations_status_check
      CHECK (status IN ('pending','active','needs_review','paused','expired','revoked','returned','declined')) NOT VALID;
  END IF;
END
$$`,
	},

	// ---------- sources ----------
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-grant-id",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS grant_id text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-acceptance-mode",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS acceptance_mode text NOT NULL DEFAULT 'legacy'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-accepted-at",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS accepted_at text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-accepted-by",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS accepted_by text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-declined-at",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS declined_at text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-declined-by",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS declined_by text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-returned-at",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS returned_at text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-returned-by",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS returned_by text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-needs-review-at",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS needs_review_at text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-needs-review-reason",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS needs_review_reason text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-needs-review-detail",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS needs_review_detail_json text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-accepted-revision",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS accepted_source_revision text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-observed-revision",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS observed_source_revision text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-accepted-revision-version",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS accepted_source_revision_version integer`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-revision-version",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS revision_version integer NOT NULL DEFAULT 1`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-expiry-cycle",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS expiry_cycle integer NOT NULL DEFAULT 0`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-expired-from-status",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS expired_from_status text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-pending-expires-at",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS pending_expires_at text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-status-check",
		SQL: `DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class r ON r.oid = c.conrelid
    JOIN pg_namespace n ON n.oid = r.relnamespace
    WHERE n.nspname = 'juhe_business' AND r.relname = 'resource_authorization_sources'
      AND c.conname = 'resource_authorization_sources_status_check'
  ) THEN
    ALTER TABLE juhe_business.resource_authorization_sources
      ADD CONSTRAINT resource_authorization_sources_status_check
      CHECK (status IN ('pending','active','dormant','needs_review','revoked','returned','declined','expired','superseded')) NOT VALID;
  END IF;
END
$$`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-type-check",
		SQL: `DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class r ON r.oid = c.conrelid
    JOIN pg_namespace n ON n.oid = r.relnamespace
    WHERE n.nspname = 'juhe_business' AND r.relname = 'resource_authorization_sources'
      AND c.conname = 'resource_authorization_sources_type_check'
  ) THEN
    ALTER TABLE juhe_business.resource_authorization_sources
      ADD CONSTRAINT resource_authorization_sources_type_check
      CHECK ((source_type = 'manual' AND source_team_id IS NULL) OR (source_type = 'team' AND source_team_id IS NOT NULL)) NOT VALID;
  END IF;
END
$$`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-dormant-check",
		SQL: `DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class r ON r.oid = c.conrelid
    JOIN pg_namespace n ON n.oid = r.relnamespace
    WHERE n.nspname = 'juhe_business' AND r.relname = 'resource_authorization_sources'
      AND c.conname = 'resource_authorization_sources_dormant_check'
  ) THEN
    ALTER TABLE juhe_business.resource_authorization_sources
      ADD CONSTRAINT resource_authorization_sources_dormant_check
      CHECK (source_type = 'manual' OR status <> 'dormant') NOT VALID;
  END IF;
END
$$`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-accepted-revision-check",
		SQL: `DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class r ON r.oid = c.conrelid
    JOIN pg_namespace n ON n.oid = r.relnamespace
    WHERE n.nspname = 'juhe_business' AND r.relname = 'resource_authorization_sources'
      AND c.conname = 'resource_authorization_sources_accepted_revision_check'
  ) THEN
    ALTER TABLE juhe_business.resource_authorization_sources
      ADD CONSTRAINT resource_authorization_sources_accepted_revision_check
      CHECK (status NOT IN ('active','dormant') OR (accepted_source_revision IS NOT NULL AND accepted_source_revision_version IS NOT NULL)) NOT VALID;
  END IF;
END
$$`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-pending-expiry-check",
		SQL: `DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class r ON r.oid = c.conrelid
    JOIN pg_namespace n ON n.oid = r.relnamespace
    WHERE n.nspname = 'juhe_business' AND r.relname = 'resource_authorization_sources'
      AND c.conname = 'resource_authorization_sources_pending_expiry_check'
  ) THEN
    ALTER TABLE juhe_business.resource_authorization_sources
      ADD CONSTRAINT resource_authorization_sources_pending_expiry_check
      CHECK (status <> 'pending' OR pending_expires_at IS NOT NULL) NOT VALID;
  END IF;
END
$$`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-ended-at-check",
		SQL: `DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class r ON r.oid = c.conrelid
    JOIN pg_namespace n ON n.oid = r.relnamespace
    WHERE n.nspname = 'juhe_business' AND r.relname = 'resource_authorization_sources'
      AND c.conname = 'resource_authorization_sources_ended_at_check'
  ) THEN
    ALTER TABLE juhe_business.resource_authorization_sources
      ADD CONSTRAINT resource_authorization_sources_ended_at_check
      CHECK (status NOT IN ('revoked','returned','declined','expired') OR ended_at IS NOT NULL) NOT VALID;
  END IF;
END
$$`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-grant-id-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_sources_grant ON juhe_business.resource_authorization_sources(grant_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-pending-expiry-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_sources_pending_expiry ON juhe_business.resource_authorization_sources(status, pending_expires_at) WHERE status = 'pending' AND pending_expires_at IS NOT NULL`,
	},

	// ---------- group settings ----------
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-group-settings-enabled-default",
		SQL:        `ALTER TABLE juhe_business.group_authorization_settings ALTER COLUMN enabled SET DEFAULT 0`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-group-settings-snapshot-revision",
		SQL:        `ALTER TABLE juhe_business.group_authorization_settings ADD COLUMN IF NOT EXISTS snapshot_revision text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-group-settings-snapshot-mode",
		SQL:        `ALTER TABLE juhe_business.group_authorization_settings ADD COLUMN IF NOT EXISTS snapshot_mode text NOT NULL DEFAULT 'normal'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-group-settings-snapshot-mode-check",
		SQL: `DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class r ON r.oid = c.conrelid
    JOIN pg_namespace n ON n.oid = r.relnamespace
    WHERE n.nspname = 'juhe_business' AND r.relname = 'group_authorization_settings'
      AND c.conname = 'group_authorization_settings_snapshot_mode_check'
  ) THEN
    ALTER TABLE juhe_business.group_authorization_settings
      ADD CONSTRAINT group_authorization_settings_snapshot_mode_check
      CHECK (snapshot_mode IN ('normal','legacy_oversized')) NOT VALID;
  END IF;
END
$$`,
	},

	// ---------- 新表 ----------
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-group-snapshot-table",
		SQL: `CREATE TABLE IF NOT EXISTS juhe_business.resource_authorization_group_accounts (
      authorization_id text NOT NULL,
      account_id text NOT NULL,
      source_revision text NOT NULL,
      revision_version integer NOT NULL DEFAULT 1,
      accepted_at text NOT NULL,
      updated_at text NOT NULL,
      PRIMARY KEY (authorization_id, account_id),
      FOREIGN KEY (authorization_id) REFERENCES juhe_business.resource_authorizations(id) ON DELETE RESTRICT,
      FOREIGN KEY (account_id) REFERENCES juhe_business.accounts(id) ON DELETE RESTRICT
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-group-snapshot-account-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_group_accounts_account ON juhe_business.resource_authorization_group_accounts(account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-blocks-table",
		SQL: `CREATE TABLE IF NOT EXISTS juhe_business.resource_authorization_blocks (
      id text PRIMARY KEY,
      blocker_system_account_id text NOT NULL,
      blocked_system_account_id text,
      blocked_resource_type text,
      blocked_resource_id text,
      reason text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      CHECK ((blocked_resource_type IS NULL AND blocked_resource_id IS NULL) OR (blocked_resource_type IS NOT NULL AND blocked_resource_id IS NOT NULL)),
      CHECK (blocked_resource_type IS NULL OR blocked_system_account_id IS NOT NULL),
      FOREIGN KEY (blocker_system_account_id) REFERENCES juhe_business.system_accounts(id) ON DELETE RESTRICT,
      FOREIGN KEY (blocked_system_account_id) REFERENCES juhe_business.system_accounts(id) ON DELETE RESTRICT
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-blocks-grantor-index",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_blocks_grantor_unique ON juhe_business.resource_authorization_blocks(blocker_system_account_id, blocked_system_account_id) WHERE blocked_resource_type IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-blocks-resource-index",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_blocks_resource_unique ON juhe_business.resource_authorization_blocks(blocker_system_account_id, blocked_resource_type, blocked_resource_id) WHERE blocked_resource_type IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-notifications-table",
		SQL: `CREATE TABLE IF NOT EXISTS juhe_business.authorization_notifications (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      type text NOT NULL,
      grant_id text,
      source_id text,
      payload_json text,
      dedupe_key text NOT NULL,
      read_at text,
      created_at text NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES juhe_business.system_accounts(id) ON DELETE RESTRICT
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-notifications-dedupe-index",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_authorization_notifications_dedupe ON juhe_business.authorization_notifications(dedupe_key)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-notifications-read-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_authorization_notifications_inbox ON juhe_business.authorization_notifications(system_account_id, read_at, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-suspensions-table",
		SQL: `CREATE TABLE IF NOT EXISTS juhe_business.system_account_authorization_suspensions (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      suspended_by text NOT NULL,
      reason text,
      suspended_at text NOT NULL,
      lifted_at text,
      lifted_by text,
      FOREIGN KEY (system_account_id) REFERENCES juhe_business.system_accounts(id) ON DELETE RESTRICT
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-suspensions-lookup-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_account_authorization_suspensions_lookup ON juhe_business.system_account_authorization_suspensions(system_account_id, lifted_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-suspensions-active-index",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_system_account_authorization_suspensions_active_unique ON juhe_business.system_account_authorization_suspensions(system_account_id) WHERE lifted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-cooldowns-table",
		SQL: `CREATE TABLE IF NOT EXISTS juhe_business.resource_authorization_offer_cooldowns (
      id text PRIMARY KEY,
      grantor_system_account_id text NOT NULL,
      grantee_type text NOT NULL,
      grantee_system_account_id text,
      grantee_team_id text,
      member_system_account_id text,
      resource_type text NOT NULL,
      resource_id text NOT NULL,
      until_at text NOT NULL,
      reason text,
      created_at text NOT NULL,
      CHECK (
        (grantee_type = 'system_account' AND grantee_system_account_id IS NOT NULL AND grantee_team_id IS NULL AND member_system_account_id IS NULL)
        OR (grantee_type = 'team' AND grantee_system_account_id IS NULL AND grantee_team_id IS NOT NULL AND member_system_account_id IS NULL)
        OR (grantee_type = 'team_member' AND grantee_system_account_id IS NULL AND grantee_team_id IS NOT NULL AND member_system_account_id IS NOT NULL)
      )
    )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-cooldowns-personal-index",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_offer_cooldowns_personal_unique ON juhe_business.resource_authorization_offer_cooldowns(grantor_system_account_id, grantee_system_account_id, resource_type, resource_id) WHERE grantee_type = 'system_account'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-cooldowns-team-index",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_offer_cooldowns_team_unique ON juhe_business.resource_authorization_offer_cooldowns(grantor_system_account_id, grantee_team_id, resource_type, resource_id) WHERE grantee_type = 'team'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-cooldowns-team-member-index",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_offer_cooldowns_team_member_unique ON juhe_business.resource_authorization_offer_cooldowns(grantor_system_account_id, grantee_team_id, member_system_account_id, resource_type, resource_id) WHERE grantee_type = 'team_member'`,
	},

	// ---------- 授权业务/运行时候选索引 ----------
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-owner-status-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_owner_status ON juhe_business.resource_authorization_grants(resource_owner_system_account_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-pending-expiry-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_pending_expiry ON juhe_business.resource_authorization_grants(status, pending_expires_at) WHERE pending_expires_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-expiry-cycle-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_status_expires ON juhe_business.resource_authorization_grants(status, expires_at) WHERE expires_at IS NOT NULL`,
	},

	// 旧 active-only 唯一索引必须显式 DROP 后重建；CREATE UNIQUE INDEX IF NOT EXISTS
	// 不会替换同名旧定义。新键覆盖全部非终态状态。
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-drop-grants-active-user-index",
		SQL:        `DROP INDEX IF EXISTS juhe_business.idx_resource_authorization_grants_active_user_unique`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-drop-grants-active-team-index",
		SQL:        `DROP INDEX IF EXISTS juhe_business.idx_resource_authorization_grants_active_team_unique`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-drop-sources-active-manual-index",
		SQL:        `DROP INDEX IF EXISTS juhe_business.idx_resource_authorization_sources_active_manual_unique`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-drop-sources-active-team-index",
		SQL:        `DROP INDEX IF EXISTS juhe_business.idx_resource_authorization_sources_active_team_unique`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-open-user-index",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_grants_open_user_unique ON juhe_business.resource_authorization_grants(resource_type, resource_id, grantee_system_account_id) WHERE grantee_type = 'system_account' AND status IN ('pending_owner_approval','pending','active','paused')`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-open-team-index",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_grants_open_team_unique ON juhe_business.resource_authorization_grants(resource_type, resource_id, grantee_team_id) WHERE grantee_type = 'team' AND status IN ('pending_owner_approval','pending_team_approval','pending_member_acceptance','active','paused')`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-open-manual-index",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_sources_open_manual_unique ON juhe_business.resource_authorization_sources(authorization_id) WHERE source_type = 'manual' AND status IN ('pending','active','dormant','needs_review')`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-open-team-index",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_sources_open_team_unique ON juhe_business.resource_authorization_sources(authorization_id, source_team_id) WHERE source_type = 'team' AND status IN ('pending','active','dormant','needs_review')`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-runtime-status-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorizations_status_owner ON juhe_business.resource_authorizations(status, resource_owner_system_account_id)`,
	},
}
