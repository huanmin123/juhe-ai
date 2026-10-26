package mockdata

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// usage_records 与 chat_messages 的日分区确保链（PG 存储契约）：
//
//   - juhe_usage.usage_records 按 created_at（text）RANGE 分区，语句文本与
//     jobs/internal/usagewriter/partitions.go 逐字对齐：
//     CREATE TABLE IF NOT EXISTS juhe_usage."usage_records_YYYYMMDD"
//     PARTITION OF juhe_usage.usage_records FOR VALUES FROM ('S') TO ('E')。
//   - juhe_chat.chat_messages 同构，语句文本与
//     gateway/internal/chat/postgres_partitions.go 对齐，且与 gateway 一样
//     预建「当日 + 次日」两个分区（消息轮跨日时下一轮写入不再撞缺分区）。
//
// 跨进程幂等依赖 IF NOT EXISTS；进程内由备忘把同一日期键的 DDL 收敛为一次，
// 失败不进备忘，下一次调用自动重试。分区清理（DETACH/DROP）归 jobs
// dataretention，本文件只补创建方向。

const (
	usageRecordPartitionPrefix = "usage_records_"
	chatMessagePartitionPrefix = "chat_messages_"
)

// partitionDateKeyPattern 匹配 created_at 文本前缀的 YYYY-MM-DD 日期部分
// （created_at 是 text 列，分区键是它的文本前缀语义，不做时区换算）。
var partitionDateKeyPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})`)

// partitionDDLExecutor 是分区 DDL 的执行面；*sql.DB 与 *sql.Tx 都满足。
type partitionDDLExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// partitionMemo 是进程内的已建分区备忘（日期键 → 已建）。
type partitionMemo struct {
	mu   sync.Mutex
	keys map[string]bool
}

// ensure 在同一日期键上至多执行一次 create；失败不进备忘。
func (m *partitionMemo) ensure(dateKey string, create func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.keys == nil {
		m.keys = map[string]bool{}
	}
	if m.keys[dateKey] {
		return nil
	}
	if err := create(); err != nil {
		return err
	}
	m.keys[dateKey] = true
	return nil
}

// partitionDateKeyFromISO 从 created_at 文本取 YYYYMMDD 日期键：前缀必须是
// 真实 UTC 日历日期。偏移时刻（如 2026-09-09T23:00:00-05:00）按文本前缀归
// 20260909——与分区键的文本前缀语义一致。
func partitionDateKeyFromISO(value string) (string, bool) {
	match := partitionDateKeyPattern.FindStringSubmatch(strings.TrimSpace(value))
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

// partitionBounds 返回日期键的 [startDate, endDate) 文本边界。
func partitionBounds(dateKey string) (startDate, endDate string, err error) {
	normalized, ok := normalizePartitionDateKey(dateKey)
	if !ok {
		return "", "", fmt.Errorf("分区日期无效：%s", dateKey)
	}
	startDate = normalized[0:4] + "-" + normalized[4:6] + "-" + normalized[6:8]
	parsed, parseErr := time.Parse("2006-01-02", startDate)
	if parseErr != nil {
		return "", "", fmt.Errorf("分区日期无效：%s", dateKey)
	}
	endDate = parsed.UTC().AddDate(0, 0, 1).Format("2006-01-02")
	return startDate, endDate, nil
}

// normalizePartitionDateKey 要求恰好 8 位数字且是真实日历日期。
func normalizePartitionDateKey(value string) (string, bool) {
	normalized := strings.TrimSpace(value)
	if len(normalized) != 8 {
		return "", false
	}
	for index := 0; index < len(normalized); index++ {
		if normalized[index] < '0' || normalized[index] > '9' {
			return "", false
		}
	}
	dateKey, ok := partitionDateKeyFromISO(
		normalized[0:4] + "-" + normalized[4:6] + "-" + normalized[6:8])
	if !ok || dateKey != normalized {
		return "", false
	}
	return normalized, true
}

// ensureUsageRecordPartitions 对批内全部 created_at 建缺失的日分区（按首次
// 出现顺序去重）。无效 created_at 静默跳过（沿 jobs 写入路径语义：行随后会
// 撞 INSERT 的 no-partition 原始错误），DDL 失败中止整批。
func ensureUsageRecordPartitions(ctx context.Context, db partitionDDLExecutor, memo *partitionMemo, createdAts []string) error {
	seen := map[string]bool{}
	var dateKeys []string
	for _, createdAt := range createdAts {
		dateKey, ok := partitionDateKeyFromISO(createdAt)
		if !ok || seen[dateKey] {
			continue
		}
		seen[dateKey] = true
		dateKeys = append(dateKeys, dateKey)
	}
	for _, dateKey := range dateKeys {
		statement, err := usageRecordPartitionDDL(dateKey)
		if err != nil {
			return err
		}
		if err := memo.ensure(dateKey, func() error {
			_, execErr := db.ExecContext(ctx, statement)
			return execErr
		}); err != nil {
			return err
		}
	}
	return nil
}

// usageRecordPartitionDDL 生成单日分区语句（与 jobs usagewriter 逐字对齐）。
func usageRecordPartitionDDL(dateKey string) (string, error) {
	normalized, ok := normalizePartitionDateKey(dateKey)
	if !ok {
		return "", fmt.Errorf("使用记录分区日期无效：%s", dateKey)
	}
	startDate, endDate, err := partitionBounds(normalized)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`
      CREATE TABLE IF NOT EXISTS juhe_usage.%s
      PARTITION OF juhe_usage.usage_records
      FOR VALUES FROM ('%s') TO ('%s')
    `, quotePGIdentifier(usageRecordPartitionPrefix+normalized), startDate, endDate), nil
}

// ensureChatMessagePartitions 为一条消息的 created_at 建「当日 + 次日」分区
// （与 gateway chat 写入路径一致：轮次跨日时下一个分区已就位）。
func ensureChatMessagePartitions(ctx context.Context, db partitionDDLExecutor, memo *partitionMemo, createdAt string) error {
	current, ok := partitionDateKeyFromISO(createdAt)
	if !ok {
		return fmt.Errorf("AI 问答消息时间无效：%s", createdAt)
	}
	startDate, _, err := partitionBounds(current)
	if err != nil {
		return err
	}
	parsed, parseErr := time.Parse("2006-01-02", startDate)
	if parseErr != nil {
		return fmt.Errorf("AI 问答消息时间无效：%s", createdAt)
	}
	next := parsed.UTC().AddDate(0, 0, 1).Format("20060102")
	for _, dateKey := range []string{current, next} {
		statement, err := chatMessagePartitionDDL(dateKey)
		if err != nil {
			return err
		}
		if err := memo.ensure(dateKey, func() error {
			_, execErr := db.ExecContext(ctx, statement)
			return execErr
		}); err != nil {
			return err
		}
	}
	return nil
}

// chatMessagePartitionDDL 生成单日分区语句（与 gateway postgres_partitions.go
// 逐字对齐）。
func chatMessagePartitionDDL(dateKey string) (string, error) {
	normalized, ok := normalizePartitionDateKey(dateKey)
	if !ok {
		return "", fmt.Errorf("AI 问答消息分区日期无效：%s", dateKey)
	}
	startDate, endDate, err := partitionBounds(normalized)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`
      CREATE TABLE IF NOT EXISTS juhe_chat.%s
      PARTITION OF juhe_chat.chat_messages
      FOR VALUES FROM ('%s') TO ('%s')
    `, quotePGIdentifier(chatMessagePartitionPrefix+normalized), startDate, endDate), nil
}
