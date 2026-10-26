package mockdata

// mockdata 测试共享助手：覆盖测试的摘要路径与 juhe_j3b schema 预置。

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// TestMain 为整个测试二进制固定 chat 资产根：不经过 runMockdataForTest 的
// 直接 Run 调用（各域测试）也不会把资产文件落到包目录的 ./data 回退根。
// 需要隔离根的测试可再用 t.Setenv 覆盖（同测试内后设的胜出）。
func TestMain(m *testing.M) {
	root := filepath.Join(os.TempDir(), "mockdata-test-chat-assets")
	_ = os.Setenv("JUHE_AI_CHAT_ASSETS_ROOT", root)
	os.Exit(m.Run())
}

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
