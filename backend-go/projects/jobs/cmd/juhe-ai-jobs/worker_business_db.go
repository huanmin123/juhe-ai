package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"
)

// businessDB 是组合根侧的双模业务库句柄（Node getBusinessDatabase 的 Go 等价）。
// SQLite 模式直开 JUHE_AI_DATABASE_PATH；PostgreSQL 模式经 pgpool 打开
// （业务表位于 juhe_business schema，由 qualify 提供）。
type businessDB struct {
	db       *sql.DB
	pool     *pgpool.Handle
	postgres bool
}

func openBusinessDB(a *workerAssembly, label string) (*businessDB, error) {
	if a.config.Driver == "postgres" {
		handle, err := a.acquirePool(a.config.PostgresURL, label)
		if err != nil {
			return nil, err
		}
		return &businessDB{db: handle.DB(), pool: handle, postgres: true}, nil
	}
	db, err := a.openSQLite(a.config.BusinessSQLitePath, label)
	if err != nil {
		return nil, err
	}
	return &businessDB{db: db, postgres: false}, nil
}

func (b *businessDB) close() error {
	if b == nil || b.db == nil {
		return nil
	}
	if b.pool != nil {
		return b.pool.Close()
	}
	return b.db.Close()
}

// table 限定业务表名（PG 走 juhe_business schema；SQLite 单库直名）。
func (b *businessDB) table(name string) string {
	if b.postgres {
		return "juhe_business." + name
	}
	return name
}

// statsTable 限定统计表名（对应 account-quality/balance 的 stats 库约定）。
func statsTable(postgres bool, name string) string {
	if postgres {
		return "juhe_stats." + name
	}
	return name
}

// timeParam 返回与方言匹配的时间绑定值：PG 用 time.Time（pgx 原生
// timestamptz），SQLite 用 RFC3339Nano UTC 文本（Node nowIso 等价）。
func timeParam(postgres bool, t time.Time) any {
	if postgres {
		return t
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// textParam 返回与方言匹配的文本绑定值。
func textParam(v string) any { return v }

// boolLit 返回布尔字面量（PG boolean / SQLite integer）。
func boolLit(postgres bool, value bool) string {
	if postgres {
		if value {
			return "TRUE"
		}
		return "FALSE"
	}
	if value {
		return "1"
	}
	return "0"
}

// scanNullTime 扫描可空时间列：PG 直接返回 time.Time；SQLite 解析文本。
func scanNullTime(postgres bool, value any) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	if postgres {
		if t, ok := value.(time.Time); ok {
			return &t, nil
		}
		if text, ok := value.([]byte); ok {
			parsed, err := time.Parse(time.RFC3339Nano, string(text))
			if err != nil {
				return nil, err
			}
			return &parsed, nil
		}
	}
	switch typed := value.(type) {
	case time.Time:
		return &typed, nil
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, typed)
		if err != nil {
			return nil, fmt.Errorf("解析业务库时间戳失败: %w", err)
		}
		return &parsed, nil
	case []byte:
		parsed, err := time.Parse(time.RFC3339Nano, string(typed))
		if err != nil {
			return nil, fmt.Errorf("解析业务库时间戳失败: %w", err)
		}
		return &parsed, nil
	}
	return nil, fmt.Errorf("业务库时间戳类型无效: %T", value)
}

// ensureAccountsBalanceColumns 校验业务库 accounts 表具备余额探测契约列
// （对齐 Node 仓储依赖的冻结 schema；缺失即 fail closed，由调用方登记
// disabled，不阻塞其他 job）。
func ensureAccountsBalanceColumns(ctx context.Context, b *businessDB) error {
	var count int
	if b.postgres {
		check := `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'juhe_business' AND table_name = 'accounts' AND column_name = ANY($1)`
		if err := b.db.QueryRowContext(ctx, check, []string{
			"balance_query_enabled", "balance_query_config_json", "balance_query_next_refresh_at",
			"config_revision", "credentials_encrypted", "proxy_profile_id", "dispatch_revision",
		}).Scan(&count); err != nil {
			return err
		}
	} else {
		check := `SELECT COUNT(*) FROM pragma_table_info('accounts') WHERE name IN ('balance_query_enabled','balance_query_config_json','balance_query_next_refresh_at','config_revision','credentials_encrypted','proxy_profile_id','dispatch_revision')`
		if err := b.db.QueryRowContext(ctx, check).Scan(&count); err != nil {
			return err
		}
	}
	if count < 7 {
		return fmt.Errorf("业务库 accounts 表缺少余额探测契约列（found %d/7）", count)
	}
	return nil
}
