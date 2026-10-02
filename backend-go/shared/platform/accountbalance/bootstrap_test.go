package accountbalance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// BootstrapPostgres 的 nil-db 守卫分支不依赖任何连接。
func TestBootstrapPostgresNilDB(t *testing.T) {
	if _, err := BootstrapPostgres(context.Background(), nil); err == nil {
		t.Fatal("nil db 必须报错")
	}
}

// w1 J2 外部预置入口的 PG 门用例：沿用 postgres_smoke_test.go 的
// JUHE_AI_J2_PG_SMOKE_ADMIN_URL 门控与一次性临时库惯例，只创建/删除本用例
// 命名唯一的临时库，绝不触碰共享库。
func TestBootstrapPostgresProvisionsAndVerifies(t *testing.T) {
	adminURL := strings.TrimSpace(os.Getenv("JUHE_AI_J2_PG_SMOKE_ADMIN_URL"))
	if adminURL == "" {
		t.Skip("JUHE_AI_J2_PG_SMOKE_ADMIN_URL 未设置")
	}
	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("juhe_ai_sub2api_dev_j2_bootstrap_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE "`+name+`"`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`) }()
	targetURL, err := replaceDatabase(adminURL, name)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := sql.Open("pgx", targetURL)
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Close()
	ctx := context.Background()
	// juhe_jobs schema 仍由外部受控流程预置（重装 runbook 的既有语义）。
	if _, err := bootstrap.ExecContext(ctx, "CREATE SCHEMA juhe_jobs"); err != nil {
		t.Fatal(err)
	}
	report, err := BootstrapPostgres(ctx, bootstrap)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if !report.Ready() || !report.SchemaVerified {
		t.Fatalf("bootstrap 后必须就绪: %+v", report)
	}
	if len(report.Tables) != len(balancePGRequiredTables) {
		t.Fatalf("报告表清单必须为四张契约表: %v", report.Tables)
	}
	if report.StatementsApplied != balancePGSchemaStatementCount() {
		t.Fatalf("语句计数不符: got=%d want=%d", report.StatementsApplied, balancePGSchemaStatementCount())
	}
	if _, err := json.Marshal(report); err != nil {
		t.Fatalf("报告必须可 JSON 序列化: %v", err)
	}
	// 生产只读校验路径在预置后必须通过（含列级契约）。
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: targetURL})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatalf("CheckSchema after bootstrap: %v", err)
	}
	// 幂等可重入：二次运行必须同样成功且仍就绪。
	second, err := BootstrapPostgres(ctx, bootstrap)
	if err != nil {
		t.Fatalf("bootstrap re-run: %v", err)
	}
	if !second.Ready() {
		t.Fatalf("二次 bootstrap 必须就绪: %+v", second)
	}
}

// 缺 juhe_jobs schema 时必须 fail-closed 报错，且不留任何部分产物
// （不自建 schema、不建表）。
func TestBootstrapPostgresFailsClosedWithoutSchema(t *testing.T) {
	adminURL := strings.TrimSpace(os.Getenv("JUHE_AI_J2_PG_SMOKE_ADMIN_URL"))
	if adminURL == "" {
		t.Skip("JUHE_AI_J2_PG_SMOKE_ADMIN_URL 未设置")
	}
	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("juhe_ai_sub2api_dev_j2_bootstrap_neg_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE "`+name+`"`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`) }()
	targetURL, err := replaceDatabase(adminURL, name)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", targetURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("PG 临时库不可达: %v", err)
	}
	_, err = BootstrapPostgres(ctx, db)
	if err == nil || !strings.Contains(err.Error(), "缺少外部 bootstrap 创建的 juhe_jobs schema") {
		t.Fatalf("缺 schema 必须 fail-closed: %v", err)
	}
	var schemaExists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname='juhe_jobs')`).Scan(&schemaExists); err != nil {
		t.Fatal(err)
	}
	if schemaExists {
		t.Fatal("bootstrap 不得自建 juhe_jobs schema")
	}
	var tableCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='juhe_jobs'`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 0 {
		t.Fatalf("fail-closed 后不得残留任何表: %d", tableCount)
	}
}
