package gatewayquota

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	// tzdata embeds the IANA zone database so time.LoadLocation resolves
	// zone names identically to Node's Intl.DateTimeFormat on every host
	// (Windows dev boxes and distroless containers included).
	_ "time/tzdata"
)

// StatsTimezoneProvider mirrors usageStatsTimezone()/usageStatsTimezoneAsync:
// the configured stats timezone used by every stat key.
type StatsTimezoneProvider interface {
	// StatsTimezone returns the parsed timezone location.
	StatsTimezone(ctx context.Context) (*time.Location, error)
}

// normalizeUsageStatsTimezone mirrors normalizeUsageStatsTimezone.
func normalizeUsageStatsTimezone(value any) (string, error) {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", errors.New("统计时区必须是非空字符串")
	}
	timezone := strings.TrimSpace(text)
	if _, err := time.LoadLocation(timezone); err != nil {
		return "", fmt.Errorf("统计时区不存在：%s", timezone)
	}
	return timezone, nil
}

// DBTimezoneSource is the database-backed usageStatsTimezone reader with the
// Node 60s TTL cache (usageStatsTimezoneCacheTtlMs). Only successful loads
// are cached, exactly like Node.
type DBTimezoneSource struct {
	db *sql.DB
	pg bool
	// clock injects now (tests); nil falls back to time.Now.
	now func() time.Time

	mu        sync.Mutex
	cached    *time.Location
	expiresAt time.Time
}

// NewDBTimezoneSource builds the source over the business database.
func NewDBTimezoneSource(db *sql.DB, postgres bool, now func() time.Time) (*DBTimezoneSource, error) {
	if db == nil {
		return nil, errors.New("gatewayquota timezone source requires a database")
	}
	if now == nil {
		now = time.Now
	}
	return &DBTimezoneSource{db: db, pg: postgres, now: now}, nil
}

const usageStatsTimezoneCacheTTL = 60 * time.Second

// StatsTimezone mirrors usageStatsTimezone()/usageStatsTimezoneAsync: read
// system_settings('sys_admin','usageStatsTimezone'), JSON-decode, normalize,
// cache 60s on success. Missing row/empty value -> 系统设置缺少
// usageStatsTimezone; parse/normalize failures are wrapped as 系统设置
// usageStatsTimezone 无效：<原因>.
func (s *DBTimezoneSource) StatsTimezone(ctx context.Context) (*time.Location, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && s.now().Before(s.expiresAt) {
		return s.cached, nil
	}
	var valueJSON sql.NullString
	err := s.db.QueryRowContext(ctx, bindPlaceholders(s.pg,
		"SELECT value_json FROM "+statsBusinessTable(s.pg, "system_settings")+
			" WHERE system_account_id = ? AND key = 'usageStatsTimezone' LIMIT 1"),
		"sys_admin").Scan(&valueJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("系统设置缺少 usageStatsTimezone")
	}
	if err != nil {
		return nil, err
	}
	if !valueJSON.Valid || valueJSON.String == "" {
		return nil, errors.New("系统设置缺少 usageStatsTimezone")
	}
	var decoded any
	if err := json.Unmarshal([]byte(valueJSON.String), &decoded); err != nil {
		return nil, fmt.Errorf("系统设置 usageStatsTimezone 无效：%s", err.Error())
	}
	timezone, err := normalizeUsageStatsTimezone(decoded)
	if err != nil {
		return nil, fmt.Errorf("系统设置 usageStatsTimezone 无效：%s", err.Error())
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("系统设置 usageStatsTimezone 无效：%s", err.Error())
	}
	s.cached = location
	s.expiresAt = s.now().Add(usageStatsTimezoneCacheTTL)
	return location, nil
}

// staticTimezoneSource is the fixed timezone provider used by callers that
// resolve the zone once (and by tests).
type staticTimezoneSource struct{ location *time.Location }

// NewStaticTimezoneSource returns a provider always yielding loc.
func NewStaticTimezoneSource(loc *time.Location) (StatsTimezoneProvider, error) {
	if loc == nil {
		return nil, errors.New("gatewayquota static timezone source requires a location")
	}
	return &staticTimezoneSource{location: loc}, nil
}

func (s *staticTimezoneSource) StatsTimezone(context.Context) (*time.Location, error) {
	return s.location, nil
}

// dateKey mirrors usage-stats-helpers dateKey: YYYY-MM-DD in the zone.
func dateKey(t time.Time, location *time.Location) string {
	year, month, day := t.In(location).Date()
	return fmt.Sprintf("%04d-%02d-%02d", year, int(month), day)
}

// monthKey mirrors monthKey: YYYY-MM in the zone.
func monthKey(t time.Time, location *time.Location) string {
	year, month, _ := t.In(location).Date()
	return fmt.Sprintf("%04d-%02d", year, int(month))
}

// weekKey mirrors weekKey: the Monday-start week date key of the zoned
// calendar date. Weekday and week arithmetic are pure calendar operations on
// (year, month, day), so they are computed at UTC noon to stay independent
// of the host zone and DST transitions.
func weekKey(t time.Time, location *time.Location) string {
	year, month, day := t.In(location).Date()
	weekday := time.Date(year, month, day, 12, 0, 0, 0, time.UTC).Weekday()
	daysSinceMonday := (int(weekday) + 6) % 7 // Monday=0 ... Sunday=6
	weekStart := time.Date(year, month, day, 12, 0, 0, 0, time.UTC).AddDate(0, 0, -daysSinceMonday)
	wYear, wMonth, wDay := weekStart.Date()
	return fmt.Sprintf("%04d-%02d-%02d", wYear, int(wMonth), wDay)
}
