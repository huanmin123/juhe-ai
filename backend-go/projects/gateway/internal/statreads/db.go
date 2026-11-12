package statreads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Row is a generic result row: the driver-decoded column map. Column values
// keep the driver's natural types; the accessors normalize them like Node's
// Number()/String() coercions do (SQLite returns int64/float64/string, pgx
// may return numeric as string and timestamps as time.Time).
type Row map[string]any

func (r Row) value(key string) any { return r[key] }

func (r Row) text(key string) string { return toText(r[key]) }

// calendarDBValue converts an API calendar key to the value PostgreSQL
// expects for the column: timestamp(0) needs full seconds, date month keys
// need the first day.
func calendarDBValue(column, value string) string {
	switch {
	case column == "stat_month" && len(value) == 7:
		return value + "-01"
	case column == "stat_hour" && len(value) == 13:
		return value[:10] + " " + value[11:13] + ":00:00"
	case column == "stat_minute" && len(value) == 16:
		return value[:10] + " " + value[11:16] + ":00"
	default:
		return value
	}
}

// calendarText normalizes a driver-decoded calendar column to its API text:
// pgx returns date/timestamp values as time.Time, while the read contracts keep
// "YYYY-MM-DD", "YYYY-MM-DDTHH" and "YYYY-MM".
func (r Row) calendarText(key, layout string) string {
	if typed, ok := r[key].(time.Time); ok {
		return typed.Format(layout)
	}
	value := toText(r[key])
	switch layout {
	case "2006-01-02":
		if len(value) >= 10 {
			return value[:10]
		}
	case "2006-01-02T15":
		if len(value) >= 13 {
			return value[:10] + "T" + value[11:13]
		}
	case "2006-01":
		if len(value) >= 7 {
			return value[:7]
		}
	}
	return value
}

func (r Row) day(key string) string { return r.calendarText(key, "2006-01-02") }

func (r Row) hour(key string) string { return r.calendarText(key, "2006-01-02T15") }

func (r Row) month(key string) string { return r.calendarText(key, "2006-01") }

func (r Row) nullDay(key string) *string {
	if r[key] == nil {
		return nil
	}
	value := r.day(key)
	return &value
}

func (r Row) nullText(key string) *string {
	if r[key] == nil {
		return nil
	}
	value := toText(r[key])
	return &value
}

func (r Row) number(key string) float64 { return numberOrZero(r[key]) }

func (r Row) nullNumber(key string) *int64 {
	if r[key] == nil {
		return nil
	}
	number, ok := toFloat(r[key])
	if !ok {
		return nil
	}
	rounded := int64(mathRound(number))
	return &rounded
}

func (r Row) nullFloat(key string) *float64 {
	if r[key] == nil {
		return nil
	}
	number, ok := toFloat(r[key])
	if !ok {
		return nil
	}
	return &number
}

func (r Row) boolLike(key string) bool {
	switch typed := r[key].(type) {
	case bool:
		return typed
	case int64:
		return typed == 1
	case float64:
		return typed != 0
	case string:
		return typed == "1" || strings.EqualFold(typed, "true")
	default:
		return false
	}
}

func toText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	case time.Time:
		// Node keeps RFC3339 strings; normalize driver timestamps to the
		// millisecond UTC form of toISOString().
		return typed.UTC().Format("2006-01-02T15:04:05.000Z")
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprintf("%v", typed)
	}
}

// queryStats runs a read against the stats database.
func (d *Deps) queryStats(r *http.Request, query string, args ...any) ([]Row, error) {
	return queryRowsContext(r.Context(), d.Stats, d.bind(query), args...)
}

// queryBusiness runs a read against the business database.
func (d *Deps) queryBusiness(r *http.Request, query string, args ...any) ([]Row, error) {
	return queryRowsContext(r.Context(), d.Business, d.bind(query), args...)
}

// bind rewrites `?` placeholders to `$n` for PostgreSQL. statreads ports the
// Node SQL with `?` placeholders while pgx does not bind them natively
// (ISSUE-005 同类：未绑定占位符直达 pgx 触发 500)。2026-09-25 E2E 发现
// usage-records / usage-overview 在生产 PG 500 后收敛到本包统一出口绑定。
func (d *Deps) bind(query string) string {
	if strings.Contains(query, "instr(") {
		// SQLite 专有 instr(X, Y) → PG strpos(X, Y)（语义等价）。
		query = strings.ReplaceAll(query, "instr(", "strpos(")
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + strconv.Itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

// queryStatsCtx / queryBusinessCtx accept an explicit context (background
// callers such as preload helpers).
func (d *Deps) queryStatsCtx(ctx context.Context, query string, args ...any) ([]Row, error) {
	return queryRowsContext(ctx, d.Stats, d.bind(query), args...)
}

func (d *Deps) queryBusinessCtx(ctx context.Context, query string, args ...any) ([]Row, error) {
	return queryRowsContext(ctx, d.Business, d.bind(query), args...)
}

func queryRowsContext(ctx context.Context, db DB, query string, args ...any) ([]Row, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	collected := []Row{}
	scan := make([]any, len(columns))
	for rows.Next() {
		values := make([]any, len(columns))
		for index := range values {
			values[index] = new(any)
		}
		for index := range values {
			scan[index] = values[index]
		}
		if err := rows.Scan(scan...); err != nil {
			return nil, err
		}
		row := Row{}
		for index, column := range columns {
			row[column] = deref(values[index])
		}
		collected = append(collected, row)
	}
	return collected, rows.Err()
}

func deref(value any) any {
	if typed, ok := value.(*any); ok {
		return *typed
	}
	return value
}

// firstRow returns (row, true) when the query yields at least one row.
func firstRow(rows []Row, err error) (Row, bool, error) {
	if err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	return rows[0], true, nil
}

var errInvalidValueJSON = errors.New("value_json 解析失败")

func parseJSON(raw string, target any) error {
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		return fmt.Errorf("%w：%s", errInvalidValueJSON, err.Error())
	}
	return nil
}

func atoiDefault(text string, fallback int) int {
	parsed, err := strconv.Atoi(text)
	if err != nil {
		return fallback
	}
	return parsed
}
