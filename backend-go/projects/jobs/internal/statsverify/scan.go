package statsverify

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// SQL value coercion helpers. The PostgreSQL mode reads the externally
// provisioned juhe_stats/juhe_business schemas where timestamps are
// TIMESTAMPTZ and money columns are NUMERIC; the SQLite mode stores the
// RFC3339 text Node writes. All helpers accept either representation.

// sqlTime converts a scanned timestamp cell into a UTC time.
func sqlTime(value any) (time.Time, error) {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC(), nil
	case sql.NullTime:
		if !typed.Valid {
			return time.Time{}, errors.New("时间单元格为空")
		}
		return typed.Time.UTC(), nil
	case string:
		if typed == "" {
			return time.Time{}, errors.New("时间单元格为空")
		}
		if t, err := time.Parse(time.RFC3339Nano, typed); err == nil {
			return t.UTC(), nil
		}
		return time.Time{}, fmt.Errorf("时间单元格不是 RFC3339: %q", typed)
	case []byte:
		return sqlTime(string(typed))
	default:
		return time.Time{}, fmt.Errorf("时间单元格类型无效: %T", value)
	}
}

// sqlText converts a scanned timestamp or text cell into the canonical
// RFC3339 UTC millisecond text Node persists ("2006-01-02T15:04:05.000Z").
func sqlText(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case []byte:
		return string(typed), nil
	case time.Time:
		return NowIso(typed), nil
	case sql.NullString:
		if !typed.Valid {
			return "", nil
		}
		return typed.String, nil
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("文本单元格类型无效: %T", value)
	}
}

func sqlInt(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int32:
		return int64(typed), nil
	case int:
		return int64(typed), nil
	case float64:
		return int64(typed), nil
	case float32:
		return int64(typed), nil
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		if err != nil {
			return 0, err
		}
		return parsed, nil
	case []byte:
		return sqlInt(string(typed))
	case sql.NullInt64:
		if !typed.Valid {
			return 0, nil
		}
		return typed.Int64, nil
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("整数单元格类型无效: %T", value)
	}
}

func sqlFloat(value any) (float64, error) {
	switch typed := value.(type) {
	case float64:
		return typed, nil
	case float32:
		return float64(typed), nil
	case int64:
		return float64(typed), nil
	case int32:
		return float64(typed), nil
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		if err != nil {
			return 0, err
		}
		return parsed, nil
	case []byte:
		return sqlFloat(string(typed))
	case sql.NullFloat64:
		if !typed.Valid {
			return 0, nil
		}
		return typed.Float64, nil
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("数值单元格类型无效: %T", value)
	}
}

func sqlStringPtr(value any) (*string, error) {
	if value == nil {
		return nil, nil
	}
	text, err := sqlText(value)
	if err != nil {
		return nil, err
	}
	return &text, nil
}

func sqlIntPtr(value any) (*int, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := sqlInt(value)
	if err != nil {
		return nil, err
	}
	casted := int(parsed)
	return &casted, nil
}

func sqlFloatPtr(value any) (*float64, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := sqlFloat(value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

// optionalTimestampText validates an optional RFC3339 cursor value the way
// optionalCursorTimestamp / optionalSuppliedTimestamp do: empty SQL NULL
// becomes "", real values must parse.
func optionalTimestampText(value any, label string) (string, error) {
	if value == nil {
		return "", nil
	}
	text, err := sqlText(value)
	if err != nil {
		return "", fmt.Errorf("%s: %w", label, err)
	}
	if text == "" {
		return "", nil
	}
	if _, err := ParseRFC3339(text, label); err != nil {
		return "", err
	}
	return text, nil
}
