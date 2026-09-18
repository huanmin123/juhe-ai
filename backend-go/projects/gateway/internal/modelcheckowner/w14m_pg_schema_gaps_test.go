package modelcheckowner

// w14m 覆盖率补强（PostgreSQL 门控）：CheckBusinessPostgresSchema 的行级
// 扫描/迭代/关闭/查询错误臂。注入驱动包装 pgx stdlib；真实数据放行，仅对
// 命中 pattern 的查询注入。隔离铁律沿用 w11e：只操作共享临时库内 w14m_bs_pg
// schema，连接串不落日志。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func w14mOpenFailCoverDB(t *testing.T) *sql.DB {
	t.Helper()
	w14mSharedFailpoint() // 确保驱动已注册
	url := w11eCoverPostgresURL(t)
	db, err := sql.Open("w14mfailpg", url)
	if err != nil {
		t.Fatalf("打开注入覆盖库失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), w11ePgBudget)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Skipf("注入覆盖库不可达（跳过）: %v", err)
	}
	t.Cleanup(func() { _ = db.Close(); w14mPgFP.disarm() })
	return db
}

func TestW14MCheckBusinessPostgresSchemaRowErrorArms(t *testing.T) {
	realDB := w11eOpenCoverDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), w11ePgBudget)
	defer cancel()

	schema := "w14m_bs_pg"
	w11eRecreateSchema(t, realDB, schema, w11eNewPlan())

	failDB := w14mOpenFailCoverDB(t)

	cases := []struct {
		name    string
		pattern string
		mode    string
		need    string
	}{
		{"tablesScan", "relkind::text", "scan", "scan Business PostgreSQL tables"},
		{"tablesIterate", "relkind::text", "nexterr", "iterate Business PostgreSQL tables"},
		{"columnsScan", "FROM information_schema.columns", "scan", "scan Business PostgreSQL columns"},
		{"columnsIterate", "FROM information_schema.columns", "nexterr", "iterate Business PostgreSQL columns"},
		{"constraintsScan", "con.contype IN ('p','u')", "scan", "scan Business PostgreSQL constraints"},
		{"constraintsIterate", "con.contype IN ('p','u')", "nexterr", "iterate Business PostgreSQL constraints"},
		{"constraintsQuery", "con.contype IN ('p','u')", "query", "list Business PostgreSQL constraints"},
		{"foreignKeysScan", "con.contype='f'", "scan", "scan Business PostgreSQL foreign keys"},
		{"foreignKeysIterate", "con.contype='f'", "nexterr", "iterate Business PostgreSQL foreign keys"},
		{"foreignKeysQuery", "con.contype='f'", "query", "list Business PostgreSQL foreign keys"},
		{"indexesScan", "FROM pg_catalog.pg_index", "scan", "scan Business PostgreSQL indexes"},
		{"indexesIterate", "FROM pg_catalog.pg_index", "nexterr", "iterate Business PostgreSQL indexes"},
		{"indexesQuery", "FROM pg_catalog.pg_index", "query", "list Business PostgreSQL indexes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			switch tc.mode {
			case "scan":
				w14mPgFP.armScan(tc.pattern)
			case "nexterr":
				w14mPgFP.armNextErr(tc.pattern)
			default:
				w14mPgFP.arm(tc.pattern)
			}
			defer w14mPgFP.disarm()
			err := CheckBusinessPostgresSchema(ctx, failDB, schema)
			if err == nil || !strings.Contains(err.Error(), tc.need) {
				t.Fatalf("err = %v, want 包含 %q", err, tc.need)
			}
		})
	}
	_ = time.Second
}
