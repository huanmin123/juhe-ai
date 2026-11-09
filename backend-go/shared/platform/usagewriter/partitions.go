package usagewriter

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// PostgreSQL usage_records 按天分区确保链，对照
// backend/src/storage/postgres-usage-record-partitions.ts：
// 写入前把批内 createdAt 解析为 YYYYMMDD 日期键，对缺失的每日分区执行
// CREATE TABLE IF NOT EXISTS ... PARTITION OF juhe_usage.usage_records
// FOR VALUES FROM ('YYYY-MM-DD') TO ('次日')。
//
// 并发语义与 Node 一致：跨进程依赖 IF NOT EXISTS 幂等；进程内由
// ensuredPartitionDateKeys 备忘 + 串行化把同一日期键的 DDL 收敛为一次，
// 失败不进备忘，下一批自动重试。清理方向（DETACH/DROP）仍由
// jobs/internal/cleanuprepo/dataretention.go 承担，本文件只补创建方向。

// usageRecordPartitionPrefix mirrors usageRecordPartitionPrefix.
const usageRecordPartitionPrefix = "usage_records_"

// usageRecordPartitionDateKeyPattern mirrors the Node
// usageRecordPartitionDateKeyFromIso regex: a YYYY-MM-DD prefix.
var usageRecordPartitionDateKeyPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})`)

// PartitionDDLExecutor ports the Node DatabaseClient.execute surface the
// ensure chain consumes (DDL with no result rows); *sql.DB satisfies it
// directly and tests inject a fake.
type PartitionDDLExecutor interface {
	ExecContext(ctx Ctx, query string, args ...any) (sql.Result, error)
}

// usageRecordPartitionDateKeyFromIso mirrors usageRecordPartitionDateKeyFromIso:
// the YYYY-MM-DD prefix of the trimmed input validated as a real UTC calendar
// date, rendered as YYYYMMDD. Prefix semantics: no timezone conversion, so an
// offset instant like 2026-09-09T23:00:00-05:00 keys as 20260909 — the same
// day the created_at text starts with (the partition key is the text column).
func usageRecordPartitionDateKeyFromIso(value string) (string, bool) {
	match := usageRecordPartitionDateKeyPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return "", false
	}
	year, _ := strconv.Atoi(match[1])
	month, _ := strconv.Atoi(match[2])
	day, _ := strconv.Atoi(match[3])
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if date.Year() != year || int(date.Month()) != month || date.Day() != day {
		return "", false
	}
	return match[1] + match[2] + match[3], true
}

// normalizeUsageRecordPartitionDateKey mirrors normalizeDateKey: exactly 8
// digits and a real calendar date.
func normalizeUsageRecordPartitionDateKey(value string) (string, bool) {
	normalized := strings.TrimSpace(value)
	if len(normalized) != 8 {
		return "", false
	}
	for index := 0; index < len(normalized); index++ {
		if normalized[index] < '0' || normalized[index] > '9' {
			return "", false
		}
	}
	dateKey, ok := usageRecordPartitionDateKeyFromIso(
		normalized[0:4] + "-" + normalized[4:6] + "-" + normalized[6:8])
	if !ok || dateKey != normalized {
		return "", false
	}
	return normalized, true
}

// postgresUsageRecordPartitionName mirrors postgresUsageRecordPartitionName,
// including the Chinese error copy 使用记录分区日期无效.
func postgresUsageRecordPartitionName(dateKey string) (string, error) {
	normalized, ok := normalizeUsageRecordPartitionDateKey(dateKey)
	if !ok {
		return "", fmt.Errorf("使用记录分区日期无效：%s", dateKey)
	}
	return usageRecordPartitionPrefix + normalized, nil
}

// postgresUsageRecordPartitionBounds mirrors postgresUsageRecordPartitionBounds:
// the daily [startDate, endDate) text range with endDate = startDate + 1 day,
// including the Chinese error copy 使用记录分区日期无效.
func postgresUsageRecordPartitionBounds(dateKey string) (startDate string, endDate string, err error) {
	normalized, ok := normalizeUsageRecordPartitionDateKey(dateKey)
	if !ok {
		return "", "", fmt.Errorf("使用记录分区日期无效：%s", dateKey)
	}
	startDate = normalized[0:4] + "-" + normalized[4:6] + "-" + normalized[6:8]
	parsed, parseErr := time.Parse("2006-01-02", startDate)
	if parseErr != nil {
		return "", "", fmt.Errorf("使用记录分区日期无效：%s", dateKey)
	}
	endDate = parsed.UTC().AddDate(0, 0, 1).Format("2006-01-02")
	return startDate, endDate, nil
}

// ensuredPartitionDateKeys mirrors the module-level ensuredPartitionDateKeys
// Set: per-process memoization of already-issued CREATE TABLE statements.
// Node owns the Set module-wide; the Go writer owns exactly one
// PostgresShardStore per process, so a store-scoped memo is the same
// production behavior while staying testable in isolation.
//
// C2-H3：Node 的 Set 永不失效，但 dataretention 的 DETACH/DROP 会删除过期
// 分区——备忘命中的日期键可能已经不存在。因此备忘必须可校验/可失效：
// ensure 前由分区存在性查询复核，INSERT 撞 no-partition 时按日期键 forget
// 后重建（见 store.go）。备忘仅作为"已确认存在"的加速，不再是不可证伪的
// 真相。
type ensuredPartitionDateKeys struct {
	mu   sync.Mutex
	keys map[string]bool
}

// ensure issues create() at most once per date key. The memo check, the DDL
// and the memo update are serialized, so concurrent callers collapse to one
// statement. A failed create() stays unmemoized (Node only adds to the Set
// after success), so the next batch retries.
func (e *ensuredPartitionDateKeys) ensure(dateKey string, create func() error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.keys == nil {
		e.keys = map[string]bool{}
	}
	if e.keys[dateKey] {
		return nil
	}
	if err := create(); err != nil {
		return err
	}
	e.keys[dateKey] = true
	return nil
}

// forget 移除日期键的备忘（分区被清理/INSERT 撞 no partition 时调用），
// 下一次 ensure 重新执行 DDL。
func (e *ensuredPartitionDateKeys) forget(dateKey string) {
	e.mu.Lock()
	delete(e.keys, dateKey)
	e.mu.Unlock()
}

// markEnsured 记录已确认存在的日期键（存在性查询命中且未备忘时使用），
// 使后续批次的同键 DDL 继续走备忘加速。
func (e *ensuredPartitionDateKeys) markEnsured(dateKey string) {
	e.mu.Lock()
	if e.keys == nil {
		e.keys = map[string]bool{}
	}
	e.keys[dateKey] = true
	e.mu.Unlock()
}

// usageRecordPartitionDateKeys 返回批内 distinct 有效日期键（首次出现序）。
func usageRecordPartitionDateKeys(createdAts []string) []string {
	seen := map[string]bool{}
	var dateKeys []string
	for _, createdAt := range createdAts {
		dateKey, ok := usageRecordPartitionDateKeyFromIso(createdAt)
		if !ok || seen[dateKey] {
			continue
		}
		seen[dateKey] = true
		dateKeys = append(dateKeys, dateKey)
	}
	return dateKeys
}

// partitionExistenceQuery 查询每日分区是否仍存在（relispartition 校验它是
// 父表的真实分区，而不是同名普通表）。nil 表示跳过存在性复核（纯 DDL 单测
// 与旧调用面）。
type partitionExistenceQuery func(ctx Ctx, partitionName string) (bool, error)

// ensurePostgresUsageRecordPartitions mirrors ensurePostgresUsageRecordPartitions:
// for each distinct valid createdAt date key (first-appearance order, like
// the Node Set spread), create the daily partition unless memoized. Invalid
// createdAt values are skipped silently (Node filter(Boolean)); such rows
// then fail the INSERT with the original no-partition error, preserving the
// write-path semantics. DDL failures abort the batch with the original error.
func ensurePostgresUsageRecordPartitions(
	ctx Ctx,
	db PartitionDDLExecutor,
	ensured *ensuredPartitionDateKeys,
	createdAts []string,
) error {
	return ensurePostgresUsageRecordPartitionsChecked(ctx, db, ensured, createdAts, nil)
}

// ensurePostgresUsageRecordPartitionsChecked 是带存在性复核的 ensure：
// exists 非 nil 时每个日期键先查分区是否仍存在（清理 job 会 DETACH/DROP
// 过期分区），缺失则使备忘失效并重新 CREATE；命中则标记备忘加速。
func ensurePostgresUsageRecordPartitionsChecked(
	ctx Ctx,
	db PartitionDDLExecutor,
	ensured *ensuredPartitionDateKeys,
	createdAts []string,
	exists partitionExistenceQuery,
) error {
	for _, dateKey := range usageRecordPartitionDateKeys(createdAts) {
		partitionName, err := postgresUsageRecordPartitionName(dateKey)
		if err != nil {
			return err
		}
		startDate, endDate, err := postgresUsageRecordPartitionBounds(dateKey)
		if err != nil {
			return err
		}
		statement := fmt.Sprintf(`
      CREATE TABLE IF NOT EXISTS juhe_usage.%s
      PARTITION OF juhe_usage.usage_records
      FOR VALUES FROM ('%s') TO ('%s')
    `, quotePgIdentifier(partitionName), startDate, endDate)
		if exists != nil {
			present, queryErr := exists(ctx, partitionName)
			if queryErr != nil {
				return queryErr
			}
			if present {
				ensured.markEnsured(dateKey)
				continue
			}
			// 备忘可能仍记着已被 DROP 的分区：清除后强制重建。
			ensured.forget(dateKey)
		}
		if err := ensured.ensure(dateKey, func() error {
			_, execErr := db.ExecContext(ctx, statement)
			if execErr != nil && isBenignPartitionDDLConflict(execErr) {
				// 并发/跨进程同时创建同一分区：IF NOT EXISTS 之外的唯一
				// 索引竞争（pg_type/pg_class）按"目标已达成"处理。
				return nil
			}
			return execErr
		}); err != nil {
			return err
		}
	}
	return nil
}

// isBenignPartitionDDLConflict 判定并发建分区时"目标已由其他进程达成"的
// PostgreSQL 错误：IF NOT EXISTS 覆盖表本身，但 pg_type / pg_class 唯一
// 索引竞争仍可能以 duplicate_table(42P07)、duplicate_object(42710) 或
// unique_violation(23505) 形式出现。这些错误不代表本次写入失败。
func isBenignPartitionDDLConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "42P07", "42710", "23505":
		return true
	default:
		return false
	}
}

// isMissingUsageRecordPartition 判定 INSERT 撞上的"no partition of
// relation ... found for row"（23514 check_violation）：分区在 ensure 之后
// 被保留策略 DROP，或备忘命中了已被清理的分区。调用方应 forget 相应日期
// 键后交由上游重试重建，而不是把整批正常记录拖成死信。
func isMissingUsageRecordPartition(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		return false
	}
	return strings.Contains(pgErr.Message, "no partition of relation") &&
		strings.Contains(pgErr.Message, "found for row")
}

// quotePgIdentifier mirrors quoteIdentifier: double quotes with doubling.
func quotePgIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
