package usagewriter

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PostgreSQL usage_records 按天分区确保链，对照
// backend/src/storage/postgres-usage-record-partitions.ts：
// 写入前把批内 createdAt 解析为 YYYYMMDD 日期键，对缺失的每日分区执行
// CREATE TABLE IF NOT EXISTS ... PARTITION OF juhe_usage.usage_records
// FOR VALUES FROM ('YYYY-MM-DD') TO ('次日')。SQLite 路径（SqliteShardStore）
// 不分区、不经过本文件。
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
	for _, dateKey := range dateKeys {
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
		if err := ensured.ensure(dateKey, func() error {
			_, execErr := db.ExecContext(ctx, statement)
			return execErr
		}); err != nil {
			return err
		}
	}
	return nil
}

// quotePgIdentifier mirrors quoteIdentifier: double quotes with doubling.
func quotePgIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
