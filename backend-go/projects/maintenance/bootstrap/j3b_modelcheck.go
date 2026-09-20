package bootstrap

// 2026-09-20 零配置自动认领扩展：J3b 模型检测 owner 专属 SQLite 库的自举。
// 与六库 ensure 同属受控导出面——本包只暴露存储自举入口，DDL 单一事实源
// 仍在 internal/j3bmodelcheck（含 run/observation/trust aggregation 列升级
// 与契约完整性校验），gateway 不复制任何 DDL。

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3bmodelcheck"
)

// EnsureSQLiteJ3bModelCheck 幂等检查或补齐 J3b 专属库 schema：契约已完整时
// 不执行任何写事务。调用方负责以可创建方式打开文件（bootstrap.OpenSQLiteFile，
// mode=rwc + WAL/busy_timeout pragma）。
func EnsureSQLiteJ3bModelCheck(ctx context.Context, db *sql.DB) error {
	if _, err := j3bmodelcheck.RunSQLite(ctx, db, true); err != nil {
		return fmt.Errorf("ensure J3b model-check sqlite schema: %w", err)
	}
	return nil
}
