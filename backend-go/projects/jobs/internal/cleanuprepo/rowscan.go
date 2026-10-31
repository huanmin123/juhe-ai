package cleanuprepo

// cleanuprepo 各仓储共用的行读取/扫描助手。原与 chat 清理链同文件
// （cleanuprepo/chat.go，已随 chat_* 表退役删除）；这些助手是
// deleteaccount/recordcleanup/statssubtract 等存活仓储的共享基础设施，
// 迁至本文件独立存活。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// randomHex32 是随机十六进制 ID 的可替换入口（cryptoRead 为 cleanuprepo.go
// 的可注入封装；测试注入确定性序列）。
var randomHex32 = newRandomHex32

func newRandomHex32() string {
	buffer := make([]byte, 16)
	if _, err := cryptoRead(buffer); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", buffer)
}

// ---- 行读取辅助 ----

type row map[string]any

func textOf(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	case time.Time:
		return typed.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	}
	return fmt.Sprintf("%v", value)
}

func optionalText(value any) string {
	text := textOf(value)
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return text
}

func numberOf(value any) float64 {
	switch typed := value.(type) {
	case nil:
		return 0
	case float64:
		return typed
	case int64:
		return float64(typed)
	case int:
		return float64(typed)
	case []byte:
		return parseNumber(string(typed))
	case string:
		return parseNumber(typed)
	}
	return 0
}

func parseNumber(value string) float64 {
	var out float64
	_, err := fmt.Sscanf(strings.TrimSpace(value), "%g", &out)
	if err != nil {
		return 0
	}
	return out
}

func queryRows(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string, args ...any) ([]row, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var output []row
	for rows.Next() {
		scan := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range scan {
			pointers[index] = &scan[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		current := row{}
		for index, column := range columns {
			current[column] = scan[index]
		}
		output = append(output, current)
	}
	return output, rows.Err()
}

func queryOne(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string, args ...any) (*row, error) {
	rows, err := queryRows(ctx, q, query, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

func execChangedQ(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, query string, args ...any) (int64, error) {
	result, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return changes(result)
}

// parseInstantMust 解析失败时返回零值时间（调用方保证输入已过 parseInstant
// 校验；测试助手场景）。
func parseInstantMust(value string) time.Time {
	parsed, _ := parseInstant(value)
	return parsed
}
