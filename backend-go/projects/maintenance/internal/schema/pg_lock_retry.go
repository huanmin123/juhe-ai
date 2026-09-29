// ensure-schema 语句的瞬时锁冲突重试（生产死锁 2026-09-29 23:15:48 的后续）：
// docker compose 发布序列中 maintenance --ensure-schema 的幂等 DDL（大量
// CREATE OR REPLACE FUNCTION，对 pg_proc 及关联对象取 DDL 级锁）与刚启动的
// jobs 每秒级 projection 写入并发时，PG 报 deadlock detected（SQLSTATE
// 40P01）并回滚该语句的隐式事务，整个 ensure 流程被人工重跑。
//
// 本文件提供有界的语句级重试：
//   - EnsurePostgres 没有整体事务——每条语句是独立的隐式事务（单次
//     ExecContext），失败语句自身已被 PG 回滚，此前成功的语句保持已应用；
//     且每条语句自带幂等护栏（IF NOT EXISTS / DROP+CREATE TRIGGER /
//     guarded DO 块），所以重试单条语句与重跑整个流程等价安全，粒度最简。
//   - 仅瞬时锁冲突类 SQLSTATE 重试（40P01 deadlock_detected、55P03
//     lock_not_available、57014 query_canceled/statement_timeout 类服务端
//     取消）；其他错误立即上抛，行为与引入重试前一致。
//   - 每次重试前打一条 stderr 日志（沿用 maintenance CLI 的 Fprintf(os.Stderr)
//     风格）；重试次数用尽后保留最后一次的原始错误上抛，不吞错。
package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// pgLockRetryMaxRetries 是每条语句在首次失败之外的最大重试次数。
const pgLockRetryMaxRetries = 3

// pgLockRetryBaseDelay 是重试退避的基准等待，按重试序号指数加倍
// （1s、2s、4s）。
const pgLockRetryBaseDelay = time.Second

// pgStatementLogSummaryLimit 是重试日志中语句摘要的最大字符数。
const pgStatementLogSummaryLimit = 80

// pgTransientLockSQLStates 判定为瞬时锁冲突、可安全重试单条幂等 DDL 的
// PG SQLSTATE 集合。
var pgTransientLockSQLStates = map[string]struct{}{
	"40P01": {}, // deadlock_detected
	"55P03": {}, // lock_not_available
	"57014": {}, // query_canceled（statement_timeout 等服务端取消）
}

// pgLockRetrySleep 等待一次重试退避；包级变量供测试替换以消除真实等待。
// ctx 取消时返回 ctx.Err()。
var pgLockRetrySleep = func(ctx context.Context, delay time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

// pgTransientLockSQLState 判断 err（含 %w 包装链）底层是否为可重试的 PG
// 瞬时锁冲突，是则返回其 SQLSTATE。非 PgError、非锁冲突 SQLSTATE 均返回
// false，保持"非锁冲突错误不重试"。
func pgTransientLockSQLState(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return "", false
	}
	if _, ok := pgTransientLockSQLStates[pgErr.Code]; !ok {
		return "", false
	}
	return pgErr.Code, true
}

// pgLockRetryDelay 返回第 attempt 次重试（0 起）前的退避等待时长。
func pgLockRetryDelay(attempt int) time.Duration {
	return pgLockRetryBaseDelay << attempt
}

// pgStatementLogSummary 把语句压缩成单行摘要：折叠连续空白并截断到前
// pgStatementLogSummaryLimit 个字符（截断时追加省略号）。
func pgStatementLogSummary(statement string) string {
	collapsed := strings.Join(strings.Fields(statement), " ")
	runes := []rune(collapsed)
	if len(runes) <= pgStatementLogSummaryLimit {
		return collapsed
	}
	return string(runes[:pgStatementLogSummaryLimit]) + "…"
}

// execPostgresStatementWithLockRetry 执行一条 ensure-schema 语句（execSQL，
// 含 SET search_path 前缀的完整批），失败且底层为瞬时锁冲突 SQLSTATE 时按
// 指数退避有界重试。statementSummary 仅用于重试日志的语句身份摘要（传语句
// 本体而非 SET 前缀，便于人工定位）。
//
// 重试次数用尽（或错误不可重试）时原样返回最后一次执行的错误；退避等待期间
// ctx 被取消时同样返回该原始执行错误（不吞错，等待取消只影响是否继续重试）。
func execPostgresStatementWithLockRetry(ctx context.Context, db *sql.DB, execSQL, statementSummary string) error {
	for attempt := 0; ; attempt++ {
		_, err := db.ExecContext(ctx, execSQL)
		if err == nil {
			return nil
		}
		sqlState, retryable := pgTransientLockSQLState(err)
		if !retryable || attempt >= pgLockRetryMaxRetries {
			return err
		}
		delay := pgLockRetryDelay(attempt)
		fmt.Fprintf(os.Stderr,
			"postgres ensure-schema 语句瞬时锁冲突，准备重试（SQLSTATE=%s，第 %d/%d 次，退避 %s）：%s\n",
			sqlState, attempt+1, pgLockRetryMaxRetries, delay, pgStatementLogSummary(statementSummary))
		if sleepErr := pgLockRetrySleep(ctx, delay); sleepErr != nil {
			return err
		}
	}
}
