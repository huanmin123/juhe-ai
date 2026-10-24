package accountbalance

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// BootstrapPostgres 的 nil-db 守卫分支不依赖任何连接。
func TestBootstrapPostgresNilDB(t *testing.T) {
	if _, err := BootstrapPostgres(context.Background(), nil); err == nil {
		t.Fatal("nil db 必须报错")
	}
}

// w1 J2 外部预置入口的 PG 用例。迁 pgtestkit（PLAN-20261024T051925945Z 切片，
// §4.2 裁量：smoke 迁离 JUHE_AI_J2_PG_SMOKE_ADMIN_URL 门禁，runtimelog/
// operationlog 先例已接受）：juhe_jobs schema 仍由 prepare 按外部受控流程
// 同语义预置，bootstrap 绝不自建 schema；一次性临时库由 kit 创建/删除，
// 断言逐字节保持。
func TestBootstrapPostgresProvisionsAndVerifies(t *testing.T) {
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, "CREATE SCHEMA juhe_jobs")
		return err
	})
	ctx := context.Background()
	report, err := BootstrapPostgres(ctx, testDB.DB)
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
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: testDB.DSN})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatalf("CheckSchema after bootstrap: %v", err)
	}
	// 幂等可重入：二次运行必须同样成功且仍就绪。
	second, err := BootstrapPostgres(ctx, testDB.DB)
	if err != nil {
		t.Fatalf("bootstrap re-run: %v", err)
	}
	if !second.Ready() {
		t.Fatalf("二次 bootstrap 必须就绪: %+v", second)
	}
}

// 缺 juhe_jobs schema 时必须 fail-closed 报错，且不留任何部分产物
// （不自建 schema、不建表）。pgtestkit 空库（无 prepare）即原负臂一次性
// 临时库形态（§4.2：PG 不可达时显式失败，原 PingContext+Skip 臂随之收敛）。
func TestBootstrapPostgresFailsClosedWithoutSchema(t *testing.T) {
	db := pgtestkit.New(t)
	ctx := context.Background()
	_, err := BootstrapPostgres(ctx, db)
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
