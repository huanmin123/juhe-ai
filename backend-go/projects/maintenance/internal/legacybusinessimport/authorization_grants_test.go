package legacybusinessimport

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// 回归背景（2026-10-24 生产取证）：2026-10-22 结构重建回迁漏掉
// resource_authorization_grants，目标库只剩 runtime 授权而无业务主表行，
// 授权操作列表与团队级联全部失明。本文件锁定 grants 表的回迁映射：
// 保留资源的 grants 原样带入（含 runtime 已消失的终态记录），资源被丢弃的
// grants 与其 runtime 行一起跳过，未知资源类型按 runtime 同款口径报错。
func TestImportAuthorizationsCarriesGrantRows(t *testing.T) {
	source := openLegacyImportSQLite(t)
	target := openLegacyImportSQLite(t)
	tx, err := target.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("开启目标事务: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, row := range []struct{ sql, args string }{
		{`INSERT INTO juhe_business.resource_authorization_grants
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
			 grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at,
			 limits_json, created_by, created_at, revoked_by, revoked_at, updated_at)
			VALUES ('grant_group_kept','group','grp_kept','owner_1','team',
			 NULL,'team_1','use','active',NULL,NULL,NULL,'owner_1','2026-07-01T00:00:00Z',NULL,NULL,'2026-07-01T00:00:00Z')`, ""},
		{`INSERT INTO juhe_business.resource_authorization_grants
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
			 grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at,
			 limits_json, created_by, created_at, revoked_by, revoked_at, updated_at)
			VALUES ('grant_account_kept','account','acc_kept','owner_1','system_account',
			 'grantee_1',NULL,'use','returned','历史归还',NULL,NULL,'owner_1','2026-06-01T00:00:00Z','grantee_1','2026-06-02T00:00:00Z','2026-06-02T00:00:00Z')`, ""},
		{`INSERT INTO juhe_business.resource_authorization_grants
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
			 grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at,
			 limits_json, created_by, created_at, revoked_by, revoked_at, updated_at)
			VALUES ('grant_group_dropped','group','grp_dropped','owner_1','system_account',
			 'grantee_1',NULL,'use','revoked',NULL,NULL,NULL,'owner_1','2026-05-01T00:00:00Z','owner_1','2026-05-02T00:00:00Z','2026-05-02T00:00:00Z')`, ""},
		{`INSERT INTO juhe_business.resource_authorizations
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
			 scope, status, effective_source_type, effective_source_team_id, activated_at,
			 last_source_changed_at, remark, expires_at, limits_json, created_by, created_at,
			 revoked_by, revoked_at, revoked_reason, updated_at)
			VALUES ('rauth_kept','group','grp_kept','owner_1','grantee_1','use','active',
			 'team','team_1','2026-07-01T00:00:00Z','2026-07-01T00:00:00Z',NULL,NULL,NULL,'owner_1',
			 '2026-07-01T00:00:00Z',NULL,NULL,NULL,'2026-07-01T00:00:00Z')`, ""},
		{`INSERT INTO juhe_business.resource_authorizations
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
			 scope, status, effective_source_type, effective_source_team_id, activated_at,
			 last_source_changed_at, remark, expires_at, limits_json, created_by, created_at,
			 revoked_by, revoked_at, revoked_reason, updated_at)
			VALUES ('rauth_dropped','group','grp_dropped','owner_1','grantee_1','use','revoked',
			 NULL,NULL,'2026-05-01T00:00:00Z','2026-05-01T00:00:00Z',NULL,NULL,NULL,'owner_1',
			 '2026-05-01T00:00:00Z','owner_1','2026-05-02T00:00:00Z','manual_revoked','2026-05-02T00:00:00Z')`, ""},
		{`INSERT INTO juhe_business.resource_authorization_sources
			(id, authorization_id, source_type, source_team_id, status, activated_at, ended_at,
			 ended_reason, created_by, created_at, revoked_by, revoked_at, updated_at)
			VALUES ('src_kept','rauth_kept','team','team_1','active','2026-07-01T00:00:00Z',NULL,
			 NULL,'owner_1','2026-07-01T00:00:00Z',NULL,NULL,'2026-07-01T00:00:00Z')`, ""},
		{`INSERT INTO juhe_business.resource_authorization_sources
			(id, authorization_id, source_type, source_team_id, status, activated_at, ended_at,
			 ended_reason, created_by, created_at, revoked_by, revoked_at, updated_at)
			VALUES ('src_dropped','rauth_dropped','manual',NULL,'revoked','2026-05-01T00:00:00Z',
			 '2026-05-02T00:00:00Z','manual_revoked','owner_1','2026-05-01T00:00:00Z','owner_1',
			 '2026-05-02T00:00:00Z','2026-05-02T00:00:00Z')`, ""},
	} {
		if _, err := source.Exec(row.sql); err != nil {
			t.Fatalf("准备源库夹具: %v\n%s", err, row.sql)
		}
	}

	report := Report{Tables: map[string]int{}}
	err = importAuthorizations(context.Background(), source, tx,
		map[string]struct{}{"grp_kept": {}}, map[string]struct{}{"acc_kept": {}},
		map[string]struct{}{"owner_1": {}}, &report)
	if err != nil {
		t.Fatalf("importAuthorizations: %v", err)
	}

	if got := report.Tables["resource_authorization_grants"]; got != 2 {
		t.Fatalf("resource_authorization_grants 报告数 = %d, 期望 2", got)
	}
	if got := report.Tables["resource_authorizations"]; got != 1 {
		t.Fatalf("resource_authorizations 报告数 = %d, 期望 1（grp_dropped 行应跳过）", got)
	}
	if got := report.Tables["resource_authorization_sources"]; got != 1 {
		t.Fatalf("resource_authorization_sources 报告数 = %d, 期望 1", got)
	}

	var teamGrantOwner, teamGrantTeam, teamGrantStatus string
	if err := tx.QueryRow(`SELECT resource_owner_system_account_id, grantee_team_id, status
		FROM juhe_business.resource_authorization_grants WHERE id = 'grant_group_kept'`).
		Scan(&teamGrantOwner, &teamGrantTeam, &teamGrantStatus); err != nil {
		t.Fatalf("团队授权记录应写入目标库: %v", err)
	}
	if teamGrantOwner != "owner_1" || teamGrantTeam != "team_1" || teamGrantStatus != "active" {
		t.Fatalf("团队授权记录列映射不符: owner=%s team=%s status=%s", teamGrantOwner, teamGrantTeam, teamGrantStatus)
	}

	var userGrantGrantee, userGrantStatus, userGrantRemark, userGrantRevokedBy sql.NullString
	if err := tx.QueryRow(`SELECT grantee_system_account_id, status, remark, revoked_by
		FROM juhe_business.resource_authorization_grants WHERE id = 'grant_account_kept'`).
		Scan(&userGrantGrantee, &userGrantStatus, &userGrantRemark, &userGrantRevokedBy); err != nil {
		t.Fatalf("个人授权记录应写入目标库: %v", err)
	}
	if !userGrantGrantee.Valid || userGrantGrantee.String != "grantee_1" ||
		userGrantStatus.String != "returned" || !userGrantRemark.Valid || userGrantRemark.String != "历史归还" ||
		!userGrantRevokedBy.Valid || userGrantRevokedBy.String != "grantee_1" {
		t.Fatalf("个人授权记录可空列映射不符: grantee=%v status=%v remark=%v revoked_by=%v",
			userGrantGrantee, userGrantStatus, userGrantRemark, userGrantRevokedBy)
	}

	var dropped int
	if err := tx.QueryRow(`SELECT count(*) FROM juhe_business.resource_authorization_grants
		WHERE id = 'grant_group_dropped'`).Scan(&dropped); err != nil {
		t.Fatalf("查询被丢弃授权记录: %v", err)
	}
	if dropped != 0 {
		t.Fatalf("资源被丢弃的授权记录不应写入目标库")
	}
}

func TestImportAuthorizationsRejectsUnknownGrantResourceType(t *testing.T) {
	source := openLegacyImportSQLite(t)
	target := openLegacyImportSQLite(t)
	tx, err := target.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("开启目标事务: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := source.Exec(`INSERT INTO juhe_business.resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
		 grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at,
		 limits_json, created_by, created_at, revoked_by, revoked_at, updated_at)
		VALUES ('grant_bad','widget','w_1','owner_1','system_account',
		 'grantee_1',NULL,'use','active',NULL,NULL,NULL,'owner_1','2026-07-01T00:00:00Z',NULL,NULL,'2026-07-01T00:00:00Z')`); err != nil {
		t.Fatalf("准备源库夹具: %v", err)
	}

	report := Report{Tables: map[string]int{}}
	err = importAuthorizations(context.Background(), source, tx,
		map[string]struct{}{"grp_kept": {}}, map[string]struct{}{"acc_kept": {}},
		map[string]struct{}{"owner_1": {}}, &report)
	if err == nil || !strings.Contains(err.Error(), "未知资源类型") {
		t.Fatalf("未知资源类型应报错并中止回迁, got %v", err)
	}
}

// openLegacyImportSQLite opens a single-connection in-memory SQLite database
// with the legacy-import schema attached as juhe_business, so the hardcoded
// schema-qualified import SQL runs verbatim (the import is PG-only in
// production; the attach keeps the fixture on the repo's sqlite regression
// pattern while exercising the same statements).
func openLegacyImportSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开内存库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// 导入语句使用 schema 限定名，单连接保证 ATTACH 对全部语句生效。
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`ATTACH ':memory:' AS juhe_business`); err != nil {
		t.Fatalf("附加 juhe_business schema: %v", err)
	}
	for _, ddl := range []string{
		`CREATE TABLE juhe_business.resource_authorization_grants (
			id text PRIMARY KEY,
			resource_type text NOT NULL,
			resource_id text NOT NULL,
			resource_owner_system_account_id text NOT NULL,
			grantee_type text NOT NULL,
			grantee_system_account_id text,
			grantee_team_id text,
			scope text NOT NULL DEFAULT 'use',
			status text NOT NULL DEFAULT 'active',
			remark text,
			expires_at text,
			limits_json text,
			created_by text NOT NULL,
			created_at text NOT NULL,
			revoked_by text,
			revoked_at text,
			updated_at text NOT NULL
		)`,
		`CREATE TABLE juhe_business.resource_authorizations (
			id text PRIMARY KEY,
			resource_type text NOT NULL,
			resource_id text NOT NULL,
			resource_owner_system_account_id text NOT NULL,
			grantee_system_account_id text NOT NULL,
			scope text NOT NULL,
			status text NOT NULL,
			effective_source_type text,
			effective_source_team_id text,
			activated_at text,
			last_source_changed_at text,
			remark text,
			expires_at text,
			limits_json text,
			created_by text NOT NULL,
			created_at text NOT NULL,
			revoked_by text,
			revoked_at text,
			revoked_reason text,
			updated_at text NOT NULL
		)`,
		`CREATE TABLE juhe_business.resource_authorization_sources (
			id text PRIMARY KEY,
			authorization_id text NOT NULL,
			source_type text NOT NULL,
			source_team_id text,
			status text NOT NULL,
			activated_at text,
			ended_at text,
			ended_reason text,
			created_by text NOT NULL,
			created_at text NOT NULL,
			revoked_by text,
			revoked_at text,
			updated_at text NOT NULL
		)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("建夹具表: %v", err)
		}
	}
	return db
}
