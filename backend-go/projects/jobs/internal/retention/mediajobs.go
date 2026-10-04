package retention

// media-jobs-retention（媒体设计 §8.2 清理任务 / §8.2 MediaJobRetentionTTL）：
// media_jobs 异步媒体任务行的 TTL 保留清理。Go 新增任务（归档 Node 无对应
// scheduled job，媒体域 M2 引入的新表）。语义：
//   - 每轮扫 created_at 早于 now-7d 的超期行（毫秒 RFC3339 UTC 文本列，
//     同格式字典序 = 时间序）；
//   - 未终态行（queued/in_progress）先置 expired（终态化——本地终态语义，
//     契约 §2.6：上游 404 后不伪造失败，TTL 到期由本地收敛）；UPDATE 带
//     非终态守卫，网关并发终态回填先落时不再覆盖（终态只落一次）；
//   - 超期行中的终态/已 expired 行删除——先标后删同轮连续执行，DELETE
//     只删终态行（未终态残留留给下一轮终态化后再删），批次独立限流；
//     过期清理不产生计费（媒体设计 §10）；
//   - 分批限流（默认每轮每阶段 ≤500 行）+ 零任务快速返回（一轮 SELECT
//     空即退出，不扫第二遍）。
//
// 表定义在 maintenance schema（pg_schema_business_tables.go /
// sqlite_schema_business.go），jobs 侧只读写不建表；表缺席（老库未跑
// maintenance）视为零任务快速返回，不报错。

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// MediaJobsRetentionJob 是 media-jobs-retention 的任务体（组合根经
// scheduleWiredJob 装配；DB 缺席显式报错不静默）。
type MediaJobsRetentionJob struct {
	// Store 是 media_jobs 清理仓储；nil 显式报错。
	Store MediaJobsStore
	// Clock 注入时间源（测试可固定）。
	Clock func() time.Time
	// Logger 缺省 slog.Default()。
	Logger *slog.Logger
	// BatchSize 每阶段每轮行上限（缺省 mediaJobsRetentionBatchSize）。
	BatchSize int
}

// MediaJobsStore 是 media_jobs 清理仓储端口（双模 SQL：SQLite `media_jobs`
// / PG `juhe_business.media_jobs`；与 gatewaymedia 仓储同表同方言约定——
// jobs 项目不 import gateway 模块，表名/状态词表在此镜像）。
type MediaJobsStore interface {
	// ExpireNonTerminalBefore 把 created_at 早于 expiredBefore 的未终态行
	//（queued/in_progress）置 expired（带非终态守卫，不覆盖网关并发落的
	// 终态），返回受影响行数（≤ limit）。
	ExpireNonTerminalBefore(ctx context.Context, expiredBefore string, limit int) (int64, error)
	// DeleteExpiredBefore 删除 created_at 早于 expiredBefore 的行中已处
	// 终态（completed/failed/cancelled/expired）的行，返回删除行数（≤
	// limit）；未终态行先经终态化再删，不直接删除。
	DeleteExpiredBefore(ctx context.Context, expiredBefore string, limit int) (int64, error)
}

// mediaJobsRetentionBatchSize 是单轮单阶段行上限（任务族惯例量级：清理
// 行集小——7 天 TTL 的任务行量远低于 usage records）。
const mediaJobsRetentionBatchSize = 500

// MediaJobsRetentionTTL 与 gatewaymedia.MediaJobRetentionTTL 同值（7 天：
// openai content 上游时效约 1 小时、GCS 签名 URL 约 2 天，7 天覆盖全部在册
// 厂商的产物时效余量）；两侧常量镜像，jobs 不 import gateway 模块。
const MediaJobsRetentionTTL = 7 * 24 * time.Hour

// mediaJobNonTerminalStatuses 是未终态状态集（media_jobs.status 词表：
// queued/in_progress 非终态；completed/failed/cancelled/expired 终态）。
var mediaJobNonTerminalStatuses = []string{"queued", "in_progress"}

// Run 执行一轮清理：先终态化未终态超期行，再删除全部超期行。表缺席
// （老库未跑 maintenance 迁移）按零任务快速返回（Info 告知一次语义由调用
// 方日志承担）；零任务时第一阶段零行即继续第二阶段一次（覆盖"上轮已
// 终态化未删尽"的行），同样零行即止。
func (j *MediaJobsRetentionJob) Run(ctx context.Context) (MediaJobsRetentionResult, error) {
	if j.Store == nil {
		return MediaJobsRetentionResult{}, fmt.Errorf("retention media jobs store 未初始化")
	}
	batchSize := j.BatchSize
	if batchSize <= 0 {
		batchSize = mediaJobsRetentionBatchSize
	}
	clock := j.Clock
	if clock == nil {
		clock = time.Now
	}
	expiredBefore := clock().UTC().Add(-MediaJobsRetentionTTL).Format("2006-01-02T15:04:05.000Z")
	result := MediaJobsRetentionResult{}
	expired, err := j.Store.ExpireNonTerminalBefore(ctx, expiredBefore, batchSize)
	if err != nil {
		if IsMediaJobsTableMissing(err) {
			return result, nil
		}
		return result, fmt.Errorf("终态化过期媒体任务失败: %w", err)
	}
	result.ExpiredNonTerminal = expired
	deleted, err := j.Store.DeleteExpiredBefore(ctx, expiredBefore, batchSize)
	if err != nil {
		if IsMediaJobsTableMissing(err) {
			return result, nil
		}
		return result, fmt.Errorf("删除过期媒体任务失败: %w", err)
	}
	result.DeletedRows = deleted
	if result.ExpiredNonTerminal > 0 || result.DeletedRows > 0 {
		j.logger().Info("媒体任务保留清理完成",
			"event", "media_jobs_retention_cleanup",
			"expiredNonTerminal", result.ExpiredNonTerminal,
			"deletedRows", result.DeletedRows,
			"retentionTTL", MediaJobsRetentionTTL.String(),
			"expiredBefore", expiredBefore)
	}
	return result, nil
}

func (j *MediaJobsRetentionJob) logger() *slog.Logger {
	if j.Logger != nil {
		return j.Logger
	}
	return slog.Default()
}

// MediaJobsRetentionResult 是单轮清理计数。
type MediaJobsRetentionResult struct {
	ExpiredNonTerminal int64
	DeletedRows        int64
}

// ---- 双模 SQL 实现（组合根供 worker_retention.go 装配） ----

// MediaJobsSQLStore 是 media_jobs 清理仓储的双模 SQL 实现（沿 cleanuprepo.DB
// 的 DB+Postgres 双字段方言约定；表缺席按零任务处理不报错）。
type MediaJobsSQLStore struct {
	DB       *sql.DB
	Postgres bool
}

func (s *MediaJobsSQLStore) table() string {
	if s.Postgres {
		return "juhe_business.media_jobs"
	}
	return "media_jobs"
}

// ExpireNonTerminalBefore 未终态超期行置 expired（每状态一条 UPDATE，id
// 子查询 LIMIT 限流——SQLite UPDATE 不支持 LIMIT，统一走子查询定位）。
// 外层 WHERE 重复非终态守卫：子查询快照与行锁之间网关可能并发落终态
//（轮询 completed/failed），无守卫的 UPDATE 会把已终态行改写为 expired
//（终态覆盖）；外层条件使并发竞争下未终态判定在写入时刻重新成立。
func (s *MediaJobsSQLStore) ExpireNonTerminalBefore(ctx context.Context, expiredBefore string, limit int) (int64, error) {
	var total int64
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	for _, status := range mediaJobNonTerminalStatuses {
		var result sql.Result
		var err error
		if s.Postgres {
			result, err = s.DB.ExecContext(ctx, fmt.Sprintf(
				`UPDATE %s SET status = 'expired', updated_at = $1
				 WHERE id IN (SELECT id FROM %s WHERE created_at < $2 AND status = $3 LIMIT $4)
				 AND status IN ('queued','in_progress')`,
				s.table(), s.table()), now, expiredBefore, status, limit)
		} else {
			result, err = s.DB.ExecContext(ctx, fmt.Sprintf(
				`UPDATE %s SET status = 'expired', updated_at = ?
				 WHERE id IN (SELECT id FROM %s WHERE created_at < ? AND status = ? LIMIT ?)
				 AND status IN ('queued','in_progress')`,
				s.table(), s.table()), now, expiredBefore, status, limit)
		}
		if err != nil {
			return total, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += affected
	}
	return total, nil
}

// DeleteExpiredBefore 删除超期行（id 子查询 LIMIT 限流）。只删终态/已
// expired 的行：未终态行必须先经 ExpireNonTerminalBefore 终态化（先标后删
// 的次序语义），未被本轮终态化覆盖的超期未终态行（批次限流残留）留给
// 下一轮，不直接删除——避免与网关并发终态回填竞争时删除仍在推进的行。
func (s *MediaJobsSQLStore) DeleteExpiredBefore(ctx context.Context, expiredBefore string, limit int) (int64, error) {
	var result sql.Result
	var err error
	if s.Postgres {
		result, err = s.DB.ExecContext(ctx, fmt.Sprintf(
			`DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE created_at < $1
			 AND status IN ('completed','failed','cancelled','expired') LIMIT $2)`,
			s.table(), s.table()), expiredBefore, limit)
	} else {
		result, err = s.DB.ExecContext(ctx, fmt.Sprintf(
			`DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE created_at < ?
			 AND status IN ('completed','failed','cancelled','expired') LIMIT ?)`,
			s.table(), s.table()), expiredBefore, limit)
	}
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// IsMediaJobsTableMissing 报告错误是否为 media_jobs 表缺席（老库零任务
// 快速返回的判据；错误文本判 no such table / does not exist，与
// retentionSettingsRuntime 的 isMissingSystemSettingsTable 同模式）。
func IsMediaJobsTableMissing(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such table: media_jobs") ||
		strings.Contains(message, "media_jobs\" does not exist") ||
		strings.Contains(message, "media_jobs does not exist")
}
