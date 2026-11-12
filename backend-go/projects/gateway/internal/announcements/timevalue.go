package announcements

import (
	"database/sql"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/dbtime"
)

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
