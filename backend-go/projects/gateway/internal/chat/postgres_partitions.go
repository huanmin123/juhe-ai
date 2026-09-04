package chat

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PostgreSQL chat_messages range partitions mirror
// backend/src/storage/postgres-chat-message-partitions.ts: one daily child
// table per UTC day (plus a look-ahead day), created lazily on turn
// acceptance. SQLite keeps bare table names and never partitions.

const chatMessagePartitionPrefix = "chat_messages_"

// ensuredChatPartitionDateKeys mirrors the module-level ensuredDateKeys Set:
// process-wide memoization of already-issued CREATE TABLE statements.
var ensuredChatPartitionDateKeys sync.Map

// chatMessagePartitionDateKeyFromISO mirrors
// chatMessagePartitionDateKeyFromIso: a YYYY-MM-DD prefix validated as a real
// UTC calendar date, rendered as YYYYMMDD.
func chatMessagePartitionDateKeyFromISO(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) < 10 {
		return "", false
	}
	datePart := trimmed[:10]
	if len(datePart) != 10 || datePart[4] != '-' || datePart[7] != '-' {
		return "", false
	}
	year, errYear := strconv.Atoi(datePart[:4])
	month, errMonth := strconv.Atoi(datePart[5:7])
	day, errDay := strconv.Atoi(datePart[8:10])
	if errYear != nil || errMonth != nil || errDay != nil {
		return "", false
	}
	if month < 1 || month > 12 || day < 1 || day > daysInMonth(year, month) {
		return "", false
	}
	return fmt.Sprintf("%04d%02d%02d", year, month, day), true
}

// postgresChatMessagePartitionName mirrors postgresChatMessagePartitionName.
func postgresChatMessagePartitionName(dateKey string) (string, error) {
	normalized, ok := normalizeChatPartitionDateKey(dateKey)
	if !ok {
		return "", &DomainError{Message: "AI 问答消息分区日期无效：" + dateKey}
	}
	return chatMessagePartitionPrefix + normalized, nil
}

// chatMessagePartitionBounds mirrors chatMessagePartitionBounds: the daily
// [startDate, endDate) range with endDate = startDate + 1 day.
func chatMessagePartitionBounds(dateKey string) (startDate, endDate string, err error) {
	normalized, ok := normalizeChatPartitionDateKey(dateKey)
	if !ok {
		return "", "", &DomainError{Message: "AI 问答消息分区日期无效：" + dateKey}
	}
	startDate = normalized[:4] + "-" + normalized[4:6] + "-" + normalized[6:8]
	parsed, parseErr := time.Parse("2006-01-02", startDate)
	if parseErr != nil {
		return "", "", &DomainError{Message: "AI 问答消息分区日期无效：" + dateKey}
	}
	endDate = parsed.UTC().AddDate(0, 0, 1).Format("2006-01-02")
	return startDate, endDate, nil
}

// ensurePostgresChatMessagePartitions mirrors ensurePostgresChatMessagePartitions:
// no-op outside PostgreSQL; otherwise creates the current and next daily
// partition unless memoized this process.
func (s *Store) ensurePostgresChatMessagePartitions(tx queryer, createdAt string) error {
	if !s.pg {
		return nil
	}
	current, ok := chatMessagePartitionDateKeyFromISO(createdAt)
	if !ok {
		return &DomainError{Message: "AI 问答消息时间无效：" + createdAt}
	}
	startDate, _, err := chatMessagePartitionBounds(current)
	if err != nil {
		// Unreachable after chatMessagePartitionDateKeyFromISO validation;
		// kept for parity with the Node throw path.
		return err
	}
	nextDate, nextErr := time.Parse("2006-01-02", startDate)
	if nextErr != nil {
		return &DomainError{Message: "AI 问答消息时间无效：" + createdAt}
	}
	next := nextDate.UTC().AddDate(0, 0, 1).Format("20060102")
	for _, dateKey := range []string{current, next} {
		if _, ensured := ensuredChatPartitionDateKeys.Load(dateKey); ensured {
			continue
		}
		startDate, endDate, err := chatMessagePartitionBounds(dateKey)
		if err != nil {
			return err
		}
		name, err := postgresChatMessagePartitionName(dateKey)
		if err != nil {
			return err
		}
		statement := fmt.Sprintf(`
      CREATE TABLE IF NOT EXISTS juhe_chat."%s"
      PARTITION OF juhe_chat.chat_messages
      FOR VALUES FROM ('%s') TO ('%s')
    `, name, startDate, endDate)
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
		ensuredChatPartitionDateKeys.Store(dateKey, true)
	}
	return nil
}

// normalizeChatPartitionDateKey mirrors normalizeDateKey: exactly 8 digits and
// a real calendar date.
func normalizeChatPartitionDateKey(value string) (string, bool) {
	if len(value) != 8 {
		return "", false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return "", false
		}
	}
	key, ok := chatMessagePartitionDateKeyFromISO(value[:4] + "-" + value[4:6] + "-" + value[6:8])
	if !ok || key != value {
		return "", false
	}
	return key, true
}
