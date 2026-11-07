package schema

import (
	"strings"
	"testing"
)

// collapseSpaces 把连续空白归一为单个空格（DDL 对齐空格不参与 golden 钉死）。
func collapseSpaces(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// 升级账本 golden（升级兼容与迁移账本设计 §7.4）：钉死账本 DDL 文本、结构
// 摘要与破坏性语句披露值。账本字段变化必须显式更新本文件；账本属于升级
// 编排基础设施，不占用业务 SchemaRevision（业务 schema/步骤变化才递增）。
func TestUpgradeLedgerGolden(t *testing.T) {
	statements := UpgradeLedgerStatements()
	if len(statements) != 4 {
		t.Fatalf("账本 DDL 语句数 = %d, want 4（两表 CREATE + 两表 ALTER 补列）", len(statements))
	}
	if !strings.Contains(statements[0], "CREATE TABLE IF NOT EXISTS juhe_business.schema_state") {
		t.Fatalf("schema_state DDL 首句不符: %s", statements[0])
	}
	if !strings.Contains(statements[1], "CREATE TABLE IF NOT EXISTS juhe_business.schema_migrations") {
		t.Fatalf("schema_migrations DDL 首句不符: %s", statements[1])
	}
	// 多节点发布门禁（设计 §5.2/§6.4）：state 记录最低可读二进制与 expand 完成态。
	for _, must := range []string{
		"min_readable_binary_revision bigint NOT NULL DEFAULT 0",
		"expand_completed boolean NOT NULL DEFAULT true",
	} {
		if !strings.Contains(collapseSpaces(statements[0]), must) {
			t.Fatalf("schema_state DDL 缺少契约片段 %q", must)
		}
	}
	for _, must := range []string{
		"phase text NOT NULL DEFAULT 'expand'",
		"online_safe boolean NOT NULL DEFAULT false",
	} {
		if !strings.Contains(collapseSpaces(statements[1]), must) {
			t.Fatalf("schema_migrations DDL 缺少契约片段 %q", must)
		}
	}
	for _, must := range []string{
		"ALTER TABLE juhe_business.schema_state",
		"ADD COLUMN IF NOT EXISTS min_readable_binary_revision",
		"ADD COLUMN IF NOT EXISTS expand_completed",
	} {
		if !strings.Contains(collapseSpaces(statements[2]), must) {
			t.Fatalf("schema_state ALTER 缺少契约片段 %q", must)
		}
	}
	for _, must := range []string{
		"ALTER TABLE juhe_business.schema_migrations",
		"ADD COLUMN IF NOT EXISTS phase",
		"ADD COLUMN IF NOT EXISTS online_safe",
	} {
		if !strings.Contains(collapseSpaces(statements[3]), must) {
			t.Fatalf("schema_migrations ALTER 缺少契约片段 %q", must)
		}
	}
	// 对齐空格不参与钉死：归一空白后比较契约片段。
	normalized := collapseSpaces(statements[1])
	for _, must := range []string{
		"status text NOT NULL CHECK (status IN ('running','applied'))",
		"impl_version integer NOT NULL DEFAULT 1",
		"adopted boolean NOT NULL DEFAULT false",
		"CHECK (target IN ('primary','j3a','j3b','account_balance'))",
		"revision bigint PRIMARY KEY",
		"step_id text NOT NULL UNIQUE",
		"destructive boolean NOT NULL DEFAULT false",
		"checksum text NOT NULL",
	} {
		if !strings.Contains(normalized, must) {
			t.Fatalf("schema_migrations DDL 缺少契约片段 %q", must)
		}
	}
	// 时间列跟随 juhe_business text 惯例（§7.1）：账本不得引入 timestamptz。
	for _, statement := range statements {
		if strings.Contains(statement, "timestamptz") {
			t.Fatal("账本时间列必须为 text（ISO-8601 UTC 毫秒），不得使用 timestamptz")
		}
	}
}

func TestPostgresSchemaDigestStable(t *testing.T) {
	first := PostgresSchemaDigest()
	second := PostgresSchemaDigest()
	if first != second {
		t.Fatalf("digest 不稳定: %s vs %s", first, second)
	}
	if !strings.HasPrefix(first, "sha256:") || len(first) != len("sha256:")+64 {
		t.Fatalf("digest 形态不符: %s", first)
	}
}

func TestPostgresDestructiveStatementCountGolden(t *testing.T) {
	total, destructive := PostgresStatementCounts()
	if total <= 0 {
		t.Fatalf("全量 DDL 语句总数异常: %d", total)
	}
	// 2026-10-30 B08 撤除原 12 条 DROP COLUMN（juhe_business 9 条 +
	// juhe_codex_context 3 条）后破坏相一度清零；2026-10-30 B09 退役
	// authorization_*_usage_range_windows 两表，当前为 2 条 DROP TABLE
	//（B09 授权范围窗口退役）；如为有意新增破坏性语句，更新本 golden 并
	// 递增 contracts.SchemaRevision。
	if destructive != 2 {
		t.Fatalf("破坏性语句数 = %d, want 2——当前 schema 为 2 条 DROP TABLE（B09 授权范围窗口退役）；如为有意新增破坏性语句，更新本 golden 并递增 contracts.SchemaRevision", destructive)
	}
}
