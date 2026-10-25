package main

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"
)

// businessDB 是组合根侧的业务库句柄（Node getBusinessDatabase 的 Go 等价）。
// PostgreSQL 唯一形态经 pgpool 打开（业务表位于 juhe_business schema，由
// qualify 提供）；sqlite 形态已随存储形态收口物理删除。
type businessDB struct {
	db   *sql.DB
	pool *pgpool.Handle
}

func openBusinessDB(a *workerAssembly, label string) (*businessDB, error) {
	handle, err := a.acquirePool(a.config.PostgresURL, label)
	if err != nil {
		return nil, err
	}
	return &businessDB{db: handle.DB(), pool: handle}, nil
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

// table 限定业务表名（juhe_business schema）。
func (b *businessDB) table(name string) string {
	return "juhe_business." + name
}

// bind 把 ? 占位符改写为 $n（对齐 oauthrefresh.Store.bind 与被删
// healthDispatchBoundary.bind 的约定）。
func (b *businessDB) bind(query string) string {
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + strconv.Itoa(index))
			index++
			continue
		}
		out.WriteByte(query[i])
	}
	return out.String()
}

// statsTable 限定统计表名（对应 account-quality/balance 的 stats 库约定）。
func statsTable(name string) string {
	return "juhe_stats." + name
}

// timeParam 返回 PG 原生时间绑定值（pgx timestamptz）。
func timeParam(t time.Time) any { return t }

// textParam 返回文本绑定值。
func textParam(v string) any { return v }

// boolLit 返回布尔标志字面量。accounts 的布尔标志列（schedulable、
// balance_query_enabled 等）在 PG schema 里是 integer(0/1)——不得输出
// TRUE/FALSE，否则报 operator does not exist: integer = boolean
// （2026-09-25 生产余额自动探测候选扫描修复）。
func boolLit(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

// scanNullTime 扫描可空时间列（PG 直接返回 time.Time）。
func scanNullTime(value any) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
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
	if text, ok := value.(string); ok {
		parsed, err := time.Parse(time.RFC3339Nano, text)
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
	check := `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'juhe_business' AND table_name = 'accounts' AND column_name = ANY($1)`
	if err := b.db.QueryRowContext(ctx, check, []string{
		"balance_query_enabled", "balance_query_config_json", "balance_query_next_refresh_at",
		"config_revision", "credentials_encrypted", "proxy_profile_id", "dispatch_revision",
	}).Scan(&count); err != nil {
		return err
	}
	if count < 7 {
		return fmt.Errorf("业务库 accounts 表缺少余额探测契约列（found %d/7）", count)
	}
	return nil
}
