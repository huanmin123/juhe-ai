package auditlog

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// 存量 SQLite 库（缺超级分组双层身份列）在启动 EnsureSchema 时必须幂等补列：
// fresh DDL 的 CREATE TABLE IF NOT EXISTS 不会修改已存在表，补列只能靠
// pragma_table_info 守卫（超级分组设计 §4.3）。
func TestEnsureSchemaBackfillsSuperGroupAuditColumns(t *testing.T) {
	cfg := sqliteConfig(t, t.TempDir())
	ctx := context.Background()

	first, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	if err := first.EnsureSchema(ctx); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+cfg.AuditDatabasePath+"?mode=rwc")
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	for _, statement := range []string{
		`ALTER TABLE audit_logs DROP COLUMN route_group_id`,
		`ALTER TABLE audit_log_attempts DROP COLUMN member_group_id`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	restarted, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("restart store: %v", err)
	}
	defer restarted.Close()
	if err := restarted.EnsureSchema(ctx); err != nil {
		t.Fatalf("restart ensure: %v", err)
	}

	probe, err := sql.Open("sqlite", "file:"+cfg.AuditDatabasePath+"?mode=ro")
	if err != nil {
		t.Fatalf("open probe: %v", err)
	}
	defer probe.Close()
	for table, column := range map[string]string{
		"audit_logs":         "route_group_id",
		"audit_log_attempts": "member_group_id",
	} {
		rows, err := probe.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatalf("pragma %s: %v", table, err)
		}
		found := false
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				t.Fatalf("scan pragma %s: %v", table, err)
			}
			if name == column {
				found = true
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("rows %s: %v", table, err)
		}
		rows.Close()
		if !found {
			t.Fatalf("%s.%s 未补列", table, column)
		}
	}
}
