// 引擎的 DB 辅助：账本 DDL 桥接、表存在性探测、PG 错误归类。
package upgradepipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
)

// ensureLedgerDDL 桥接 schema 包的账本 DDL（幂等）。
func ensureLedgerDDL(ctx context.Context, db *sql.DB) error {
	return schema.EnsureUpgradeLedger(ctx, db)
}

// ledgerTableReady 探测账本表是否已存在（缺失 = 首次升级，读侧按空账本处理）。
func ledgerTableReady(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `
SELECT count(*) FROM information_schema.tables
WHERE table_schema = 'juhe_business' AND table_name = 'schema_migrations'`).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("探测 schema_migrations 表失败: %w", err)
	}
	return count > 0, nil
}

// isUndefinedTable 判定 PG 42P01（表不存在）。
func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "42P01"
	}
	return false
}

// EnsureTargetSchema 在步骤 Apply 前确保 target 所需 schema 存在（§8.3：
// CREATE SCHEMA IF NOT EXISTS，已存在时 no-op 不改 owner；§20.3 裁决落地点）。
// 自管连接：一次性 CLI 用后即闭。
func EnsureTargetSchema(ctx context.Context, dsn string, target Target) error {
	schemaName := target.SchemaForTarget()
	if schemaName == "" {
		return nil
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("确保 schema %s 存在失败（打开连接）: %w", schemaName, err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx,
		fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %s AUTHORIZATION CURRENT_ROLE`, schemaName)); err != nil {
		return fmt.Errorf("确保 schema %s 存在失败: %w", schemaName, err)
	}
	return nil
}
