package statsagg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
)

// 使用记录统计消费水位（BUG-0345 根治）：
//
// 旧口径按 `(created_at, id)` 顺序游标消费，隐含假设「游标越过一行时该行已落库」。
// 该假设不成立——`created_at` 是请求终态时刻（逻辑时间），落库是物理写入，两者
// 差值上限约等于请求墙钟预算加投递余量（生产实测最大 299,163ms），任何固定缓冲
// 窗口都可能被越过，且越过即永久漏计。
//
// 新口径消费 `usage_records` 上的水位列：NULL = 未被该统计消费过。聚合器每批读
// 「未标记的最旧行」，在同一个事务里写入统计行并标记本批，消费因此与记录晚到
// 多久无关，且重复消费幂等（统计写入本身就是 UPSERT）。
//
// 两个聚合器（usage-stats-aggregation / client-ip-stats-aggregation）消费同一张
// 源表、各自维护独立游标，所以各需要一个水位列；共用一列会让先跑的那个把行藏
// 起来，另一个永久看不到这些行。

// UsageRecordConsumptionColumn 是 usage_records 统计消费水位列。
// 取值只能是下面两个编译期常量，列名直接拼进 SQL，不接受外部输入。
type UsageRecordConsumptionColumn string

const (
	// UsageRecordConsumptionUsageStats 对应 usage-stats-aggregation。
	UsageRecordConsumptionUsageStats UsageRecordConsumptionColumn = "usage_stats_aggregated_at"
	// UsageRecordConsumptionClientIPStats 对应 client-ip-stats-aggregation。
	UsageRecordConsumptionClientIPStats UsageRecordConsumptionColumn = "client_ip_stats_aggregated_at"
)

// usageRecordsTableSQL 是消费水位读写的源表限定名（与 Dialect.UsageRecordsTable 一致；
// 此处固定为常量，避免水位列名与表名同时来自运行时输入）。
const usageRecordsTableSQL = "juhe_usage.usage_records"

func (c UsageRecordConsumptionColumn) valid() bool {
	switch c {
	case UsageRecordConsumptionUsageStats, UsageRecordConsumptionClientIPStats:
		return true
	}
	return false
}

// SQLExecutor 让标记既能在聚合事务内执行（*sql.Tx），也能在诊断路径执行（*sql.DB）。
type SQLExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// SQLRowQueryer 是单行读取的最小接口。
type SQLRowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// MarkUsageRecordsConsumed 把本批使用记录标记为已被 column 对应的统计消费。
//
// 必须与聚合写在同一个事务里：聚合失败回滚时标记一并回滚，本批会被重新选中，
// 不会出现「已标记但没进统计」的空洞。
//
// 按 `id` 定位行而非 `(created_at, id)`：行结构里的 CreatedAt 已被规范化为毫秒
// 精度规范形式，与库内原始 text 不保证逐字节相等，用它会匹配不到行、导致同一批
// 被无限重放。`id` 由一次物理写入生成一次（`usage_<bucket>_sNN_<writeMillis>_<UUID>`），
// 生产 306,548 行零重复，作为行标识精确且稳定。
func MarkUsageRecordsConsumed(ctx context.Context, exec SQLExecutor, column UsageRecordConsumptionColumn, ids []string, consumedAt string) error {
	if !column.valid() {
		return fmt.Errorf("未知的使用记录统计消费列: %q", column)
	}
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return nil
	}
	query := sqldialect.BindSQL(`UPDATE ` + usageRecordsTableSQL +
		` SET ` + string(column) + ` = ? WHERE id = ANY(?::text[])`)
	if _, err := exec.ExecContext(ctx, query, consumedAt, unique); err != nil {
		return fmt.Errorf("标记使用记录统计消费水位失败: %w", err)
	}
	return nil
}

// OldestPendingUsageRecordCreatedAt 返回 column 对应统计仍未消费的最旧记录时间。
// 走 `(created_at, id) WHERE <column> IS NULL` 部分索引的首端，代价与积压量无关。
// ok=false 表示已无待消费记录。
func OldestPendingUsageRecordCreatedAt(ctx context.Context, q SQLRowQueryer, column UsageRecordConsumptionColumn) (string, bool, error) {
	if !column.valid() {
		return "", false, fmt.Errorf("未知的使用记录统计消费列: %q", column)
	}
	query := sqldialect.BindSQL(`SELECT MIN(created_at) FROM ` + usageRecordsTableSQL +
		` WHERE ` + string(column) + ` IS NULL`)
	var oldest sql.NullString
	if err := q.QueryRowContext(ctx, query).Scan(&oldest); err != nil {
		return "", false, fmt.Errorf("读取待统计消费的最旧使用记录失败: %w", err)
	}
	if !oldest.Valid || strings.TrimSpace(oldest.String) == "" {
		return "", false, nil
	}
	return oldest.String, true, nil
}

// BootstrapUsageRecordConsumption 在「水位列首次启用」时结算存量行，返回 true
// 表示本轮执行了结算、调用方本轮不再消费。
//
// 为什么必须有这一步：统计写入是增量累加（`request_count = 旧值 + 增量`），
// 加列后存量行全是 NULL，若直接把全表当待消费，会把已经在统计里的历史行再加
// 一遍。结算口径取「旧游标已消费到的最晚一行」——游标之前的行按旧口径已经进过
// 统计，标记为已消费；游标之后的行为待消费，正常消费。
//
// 旧游标遗漏过的晚到行（BUG-0345 的历史损失）在这里同样被结算为已消费：它们
// 没有进过统计，但已无法与「进过统计」区分，宁可保留已知的历史缺口，也不冒
// 全量重复计数的风险。历史缺口另行按 bug 档案处理。
//
// 游标不存在（全新库）时不做结算：此时统计面为空，全部记录都该被消费。
func BootstrapUsageRecordConsumption(ctx context.Context, q sqlExecutorQueryer, column UsageRecordConsumptionColumn, consumedAt, cursorCreatedAt, cursorID string) (bool, error) {
	if !column.valid() {
		return false, fmt.Errorf("未知的使用记录统计消费列: %q", column)
	}
	if strings.TrimSpace(cursorCreatedAt) == "" {
		return false, nil
	}
	// 存在任一已标记行即说明水位列已启用过，正常消费。未启用的库这里会扫到表尾，
	// 启用后第一页就能命中，作为常驻检查的代价可以忽略。
	var probe int
	err := q.QueryRowContext(ctx, sqldialect.BindSQL(`SELECT 1 FROM `+usageRecordsTableSQL+
		` WHERE `+string(column)+` IS NOT NULL LIMIT 1`)).Scan(&probe)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("探测使用记录统计消费水位失败: %w", err)
	}
	// 游标 id 缺失时按「created_at <= 游标」（含边界毫秒）结算：宁可把边界毫秒里
	// 一条未聚合的行当作已消费，也不能把它留给下一轮重复累加。
	bind := sqldialect.BindSQL
	var query string
	var args []any
	if strings.TrimSpace(cursorID) == "" {
		query = bind(`UPDATE ` + usageRecordsTableSQL +
			` SET ` + string(column) + ` = ?` +
			` WHERE ` + string(column) + ` IS NULL AND created_at <= ?`)
		args = []any{consumedAt, cursorCreatedAt}
	} else {
		query = bind(`UPDATE ` + usageRecordsTableSQL +
			` SET ` + string(column) + ` = ?` +
			` WHERE ` + string(column) + ` IS NULL` +
			` AND (created_at < ? OR (created_at = ? AND id <= ?))`)
		args = []any{consumedAt, cursorCreatedAt, cursorCreatedAt, cursorID}
	}
	if _, err := q.ExecContext(ctx, query, args...); err != nil {
		return false, fmt.Errorf("结算存量使用记录统计消费水位失败: %w", err)
	}
	return true, nil
}

// sqlExecutorQueryer 是标记/结算需要的最小读写接口（*sql.Tx / *sql.DB 均满足）。
type sqlExecutorQueryer interface {
	SQLExecutor
	SQLRowQueryer
}
