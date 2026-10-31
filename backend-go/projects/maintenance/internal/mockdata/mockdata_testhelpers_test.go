package mockdata

// mockdata 测试共享助手：覆盖测试的摘要路径与 juhe_j3b schema 预置。
// （原 TestMain 的 chat 资产根 Setenv 已随 my-chat 会话面退役移除——演练场
// 设计 §8。）

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// coverageSummaryPath 返回一个临时摘要路径；Run 把真实域接线快照写进去，
// VerifyCoverage 从同一路径读回（与 CLI 的 --mockdata-summary-file 同路径
// 语义）。
func coverageSummaryPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "mockdata-summary.json")
}

// ensureJ3BSchemaOnly 只创建 juhe_j3b schema（不建表）：模拟「受控数据库流程
// 预置了 schema、表由 j3bmodelcheck bootstrap 建立」的生产前置。Run 内的
// ensureJ3BOwnerStore 会在 schema 存在时执行受控建表。
func ensureJ3BSchemaOnly(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+SchemaJ3B)
	return err
}
