package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// HealthStatHourFunc maps an observed instant to the Business stats hour key.
// Callers must construct it from the frozen usageStatsTimezone that applies to
// the run; reading time.Local would make retries depend on the process host.
type HealthStatHourFunc func(time.Time) (string, error)

// LoadBusinessHealthStatHour reads the same authoritative Business setting as
// Node. The setting is intentionally loaded through the already validated
// Gateway Business handle; process TZ and environment defaults are not a
// fallback because they would make a retry write a different fact key.
func LoadBusinessHealthStatHour(ctx context.Context, db *sql.DB, postgres bool) (HealthStatHourFunc, error) {
	if db == nil {
		return nil, errors.New("J3b usage stats timezone database is not initialized")
	}
	query := "SELECT value_json FROM system_settings WHERE system_account_id='sys_admin' AND key='usageStatsTimezone' LIMIT 1"
	if postgres {
		query = "SELECT value_json::text FROM juhe_business.system_settings WHERE system_account_id='sys_admin' AND key='usageStatsTimezone' LIMIT 1"
	}
	var raw string
	if err := db.QueryRowContext(ctx, query).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("J3b Business setting usageStatsTimezone is missing")
		}
		return nil, fmt.Errorf("read J3b Business usageStatsTimezone: %w", err)
	}
	var timezone string
	if err := json.Unmarshal([]byte(raw), &timezone); err != nil {
		return nil, fmt.Errorf("parse J3b Business usageStatsTimezone: %w", err)
	}
	return NewHealthStatHourFunc(timezone)
}

// NewHealthStatHourFunc creates the Node-compatible YYYY-MM-DDTHH formatter
// for one explicit IANA timezone. It intentionally has no default timezone.
func NewHealthStatHourFunc(timezone string) (HealthStatHourFunc, error) {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		return nil, errors.New("J3b usage stats timezone is required")
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("J3b usage stats timezone %q is invalid: %w", timezone, err)
	}
	return func(observedAt time.Time) (string, error) {
		if observedAt.IsZero() {
			return "", errors.New("J3b health observed time is required")
		}
		return observedAt.In(location).Format("2006-01-02T15"), nil
	}, nil
}

func (s *Store) formatHealthStatHour(observedAt time.Time) (string, error) {
	if s == nil || s.HealthStatHour == nil {
		return "", errors.New("J3b usage stats timezone is not configured")
	}
	statHour, err := s.HealthStatHour(observedAt)
	if err != nil {
		return "", fmt.Errorf("format J3b health stat hour: %w", err)
	}
	if !validNodeHealthStatHour(statHour) {
		return "", errors.New("J3b health stat hour formatter returned an invalid key")
	}
	return statHour, nil
}

func validNodeHealthStatHour(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != len("2006-01-02T15") {
		return false
	}
	_, err := time.Parse("2006-01-02T15", value)
	return err == nil
}
