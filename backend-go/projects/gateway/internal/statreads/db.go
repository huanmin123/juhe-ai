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
	return queryRowsContext(r.Context(), d.Stats, query, args...)
}

// queryBusiness runs a read against the business database.
func (d *Deps) queryBusiness(r *http.Request, query string, args ...any) ([]Row, error) {
	return queryRowsContext(r.Context(), d.Business, query, args...)
}

// queryStatsCtx / queryBusinessCtx accept an explicit context (background
// callers such as preload helpers).
func (d *Deps) queryStatsCtx(ctx context.Context, query string, args ...any) ([]Row, error) {
	return queryRowsContext(ctx, d.Stats, query, args...)
}

func (d *Deps) queryBusinessCtx(ctx context.Context, query string, args ...any) ([]Row, error) {
	return queryRowsContext(ctx, d.Business, query, args...)
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
