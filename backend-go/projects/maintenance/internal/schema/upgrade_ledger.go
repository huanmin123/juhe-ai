// Package schema 内的升级账本 DDL（升级兼容与迁移账本设计 §7.1）。
//
// 本文件是自研 DDL，不进 pg_schema_business_tables.go 等 Node 生成清单（那里
// 的 golden 钉死 Node 输出，见 623/624 事故核对基准）；账本 DDL 由本文件的
// EnsureUpgradeLedger 在 EnsurePostgres 六个 schema 组之后执行，并由
// UpgradeLedgerGoldenTest 单独钉死文本。时间列跟随 juhe_business 主流 text
// ISO-8601 UTC 毫秒惯例。
package schema

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

// UpgradeLedgerStatements 是账本两表的幂等 DDL（顺序即执行顺序）。
func UpgradeLedgerStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS juhe_business.schema_state (
  id              smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  schema_revision bigint NOT NULL,
  schema_digest   text NOT NULL,
  app_version     text NOT NULL,
  converged_at    text NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS juhe_business.schema_migrations (
  revision     bigint PRIMARY KEY,
  step_id      text NOT NULL UNIQUE,
  title        text NOT NULL,
  target       text NOT NULL CHECK (target IN ('primary','j3a','j3b','account_balance')),
  destructive  boolean NOT NULL DEFAULT false,
  status       text NOT NULL CHECK (status IN ('running','applied')),
  checksum     text NOT NULL,
  impl_version integer NOT NULL DEFAULT 1,
  adopted      boolean NOT NULL DEFAULT false,
  started_at   text,
  applied_at   text,
  duration_ms  integer,
  app_version  text,
  detail_json  text
)`,
	}
}

// EnsureUpgradeLedger 在主库上幂等创建账本两表（upgrade 与 ensure-schema 均调用）。
// 首句先建 schema（fresh 空库上 upgrade 先于 EnsurePostgres 执行本函数）。
func EnsureUpgradeLedger(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_business`); err != nil {
		return fmt.Errorf("upgrade ledger 建 schema 失败: %w", err)
	}
	for _, statement := range UpgradeLedgerStatements() {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("upgrade ledger DDL 失败: %w", err)
		}
	}
	return nil
}

// PostgresSchemaDigest 返回全量 DDL 清单（含分相归类与账本 DDL）的稳定摘要，
// 供 schema_state.schema_digest 记录「库被哪个结构版本收敛过」。语句清单或
// 分类变化都会改变摘要——这是 §7.4 golden 之外的第二道防线。
func PostgresSchemaDigest() string {
	hash := sha256.New()
	for _, statement := range postgresSchemaStatements {
		fmt.Fprintf(hash, "%s\x1f%s\x1f%s\x1e", statement.SchemaName, statement.Source, statement.SQL)
	}
	for _, statement := range UpgradeLedgerStatements() {
		fmt.Fprintf(hash, "upgrade-ledger\x1f%s\x1e", statement)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// PostgresDestructiveStatementCount 统计全量 DDL 中的破坏性语句数
// （DROP TABLE / DROP COLUMN；约束替换、DROP TRIGGER/INDEX 不算——
// §7.3 分类规则）。披露值，不参与确认门禁判定。
func PostgresDestructiveStatementCount() int {
	_, destructive := PostgresStatementCounts()
	return destructive
}

// isDestructiveDDL 按 §7.3 分类规则判定单条语句：直接导致旧二进制读写失败的
// DROP TABLE / DROP COLUMN 判破坏性；其余（CREATE OR REPLACE、TRIGGER/INDEX、
// DO 块内约束替换）非破坏。
func isDestructiveDDL(sql string) bool {
	upper := strings.ToUpper(sql)
	return strings.Contains(upper, "DROP TABLE") || strings.Contains(upper, "DROP COLUMN")
}

// PostgresStatementCounts 返回（总数, 破坏数），供 postflight 执行侧完整性核对
// 与报告披露（§9.4-14：防语句清单版本错配，623/624 事故形态）。
func PostgresStatementCounts() (total, destructive int) {
	for _, statement := range postgresSchemaStatements {
		total++
		if isDestructiveDDL(statement.SQL) {
			destructive++
		}
	}
	return total, destructive
}
