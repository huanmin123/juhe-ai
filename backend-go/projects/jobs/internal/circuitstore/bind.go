package circuitstore

// PG 模式占位符改写：方言共享 SQL 与 SQLite 共用顺序 `?` 占位符，pgx 驱动
// 不做 `?`→`$n` 改写（w15 门控实测 PG 模式报 42601，与 taskruns/cleanuprepo
// 前波同类缺陷）。通过嵌入类型在 Query/Exec 层统一改写，调用点零改动；
// SQLite 模式（postgres=false）为 no-op。本包 SQL 不含 `?` 字符串字面量。

import (
	"context"
	"database/sql"

	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
)

// bindSQL 把顺序 `?` 占位符按出现次序改写为 PostgreSQL 的 $n 序号。
// 实现收敛到 shared/platform/sqldialect（行为逐字节等价）。
func bindSQL(postgres bool, query string) string {
	return sqldialect.BindSQL(postgres, query)
}

// boundDB 嵌入 *sql.DB，对 PG 模式改写方言共享 SQL 的 `?` 占位符。
type boundDB struct {
	*sql.DB
	postgres bool
}

func (b *boundDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return b.DB.QueryContext(ctx, bindSQL(b.postgres, query), args...)
}

func (b *boundDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return b.DB.QueryRowContext(ctx, bindSQL(b.postgres, query), args...)
}

func (b *boundDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return b.DB.ExecContext(ctx, bindSQL(b.postgres, query), args...)
}

func (b *boundDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*boundTx, error) {
	tx, err := b.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &boundTx{Tx: tx, postgres: b.postgres}, nil
}

// boundTx 嵌入 *sql.Tx，对 PG 模式改写事务内方言共享 SQL 的 `?` 占位符。
type boundTx struct {
	*sql.Tx
	postgres bool
}

func (t *boundTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, bindSQL(t.postgres, query), args...)
}

func (t *boundTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, bindSQL(t.postgres, query), args...)
}

func (t *boundTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, bindSQL(t.postgres, query), args...)
}

// txLike 覆盖 *sql.Tx 与 *boundTx（PG 模式占位符改写包装），方言共享 helper
// 对两者透明。
type txLike interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}
