// 授权治理 DDL。revision 28 的授权接收方确认与来源治理 expand 相（审批链、
// 冷却、快照、复核）已于 revision 31 授权治理简化批整体退役：revision 28/29/30
// 均未上生产（仅 dev），照 resource_authorization_blocks 撤销先例，未发布
// revision 的语句直接删除、不留退役语句。当前本文件只保留简化后的授权面：
// grants/sources/runtime 三个 status CHECK 统一收敛为 7 值词表
//（pending/active/paused/expired/revoked/returned/declined），group_accounts
// 原表（实例绑定）在 pg_schema_business_tables.go，不受本批影响。
//
// 存量 dev 库中已建的退役对象（四张表、26 条加列、退役 CHECK 与索引）由
// maintenance upgrade revision 31（authz-claim-simplification）的破坏性步骤
// 在单事务内 DROP 并收敛存量状态。
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
		Source:     "authz-governance-grants-ended-at",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_grants ADD COLUMN IF NOT EXISTS ended_at text`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-ended-reason",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_grants ADD COLUMN IF NOT EXISTS ended_reason text`,
	},
	{
		// revision 31 授权治理简化：grants status 词表收敛为 7 值
		//（pending/active/paused/expired/revoked/returned/declined）。存量 dev 库
		// 的 10 值旧约束由 revision 31 步骤先收敛数据后重建。
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
      CHECK (status IN ('pending','active','paused','expired','revoked','returned','declined')) NOT VALID;
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
		// revision 31 授权治理简化：runtime status 词表收敛为 7 值（删
		// needs_review）。存量 dev 库的 8 值旧约束由 revision 31 步骤先收敛
		// 数据后重建。
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
      CHECK (status IN ('pending','active','paused','expired','revoked','returned','declined')) NOT VALID;
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
		Source:     "authz-governance-sources-expiry-cycle",
		SQL:        `ALTER TABLE juhe_business.resource_authorization_sources ADD COLUMN IF NOT EXISTS expiry_cycle integer NOT NULL DEFAULT 0`,
	},
	{
		// revision 31 授权治理简化：sources status 词表收敛为 7 值（删
		// dormant/needs_review/superseded）。存量 dev 库的 9 值旧约束由
		// revision 31 步骤先收敛数据后重建。
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
      CHECK (status IN ('pending','active','paused','expired','revoked','returned','declined')) NOT VALID;
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

	// ---------- 授权业务/运行时候选索引 ----------
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-owner-status-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_owner_status ON juhe_business.resource_authorization_grants(resource_owner_system_account_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-expiry-cycle-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_status_expires ON juhe_business.resource_authorization_grants(status, expires_at) WHERE expires_at IS NOT NULL`,
	},

	// 旧 active-only 唯一索引必须显式 DROP 后重建；CREATE UNIQUE INDEX IF NOT EXISTS
	// 不会替换同名旧定义。revision 31 起新键覆盖收敛后的全部非终态状态
	//（pending/active/paused）；存量 dev 库的旧谓词唯一索引随 revision 31 步骤
	// 数据收敛后仍为旧定义，IF NOT EXISTS 语义不自动替换（见步骤注释）。
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
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_grants_open_user_unique ON juhe_business.resource_authorization_grants(resource_type, resource_id, grantee_system_account_id) WHERE grantee_type = 'system_account' AND status IN ('pending','active','paused')`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-grants-open-team-index",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_grants_open_team_unique ON juhe_business.resource_authorization_grants(resource_type, resource_id, grantee_team_id) WHERE grantee_type = 'team' AND status IN ('pending','active','paused')`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-open-manual-index",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_sources_open_manual_unique ON juhe_business.resource_authorization_sources(authorization_id) WHERE source_type = 'manual' AND status IN ('pending','active','paused')`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-sources-open-team-index",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_sources_open_team_unique ON juhe_business.resource_authorization_sources(authorization_id, source_team_id) WHERE source_type = 'team' AND status IN ('pending','active','paused')`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "authz-governance-runtime-status-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorizations_status_owner ON juhe_business.resource_authorizations(status, resource_owner_system_account_id)`,
	},
}
