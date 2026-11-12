package groups

import (
	"database/sql"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/dbtime"
)

// apiTimeString 把数据库 text/timestamptz 渲染统一归一到 API 的 UTC 毫秒 Z。
func apiTimeString(value string) string {
	parsed, ok := dbtime.ParseDB(strings.TrimSpace(value))
	if !ok {
		return value
	}
	return dbtime.API(parsed)
}

func apiTimePtr(value sql.NullString) *string {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	normalized := apiTimeString(value.String)
	return &normalized
}
