package statreads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DB is the minimal database surface the statreads stores consume (either a
// *sql.DB over the shared pool / SQLite file or a transaction handle in
// tests).
type DB interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

type nullText struct {
	Valid  bool
	String string
}

func (n *nullText) Scan(value any) error {
	if value == nil {
		n.Valid, n.String = false, ""
		return nil
	}
	switch typed := value.(type) {
	case string:
		n.Valid, n.String = true, typed
	case []byte:
		n.Valid, n.String = true, string(typed)
	default:
		return errors.New("statreads: value_json 不是文本")
	}
	return nil
}

func (n nullText) unmarshal(target any) error {
	return json.Unmarshal([]byte(n.String), target)
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// accountUsageStatsMaxRangeDays mirrors ACCOUNT_USAGE_STATS_MAX_RANGE_DAYS.
const accountUsageStatsMaxRangeDays = 31

// fixedRangeWindowDays mirrors FIXED_RANGE_WINDOW_DAYS.
const fixedRangeWindowDays = 31

// hourMS / dayMS mirror HOUR_MS / DAY_MS.
const (
	hourMS = int64(60 * 60 * 1000)
	dayMS  = int64(24 * hourMS)
)

var dateKeyPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// Range mirrors AccountUsageStatsRange.
type Range struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	Days      int    `json:"days"`
	MaxDays   int    `json:"maxDays"`
}

func parseDateKeyParts(value string) (int, int, int, bool) {
	if !dateKeyPattern.MatchString(value) {
		return 0, 0, 0, false
	}
	year, _ := strconv.Atoi(value[0:4])
	month, _ := strconv.Atoi(value[5:7])
	day, _ := strconv.Atoi(value[8:10])
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if date.Year() != year || int(date.Month()) != month || date.Day() != day {
		return 0, 0, 0, false
	}
	return year, month, day, true
}

// dateKeyTime parses a validated YYYY-MM-DD key into the UTC midnight instant.
func dateKeyTime(value string) (time.Time, bool) {
	year, month, day, ok := parseDateKeyParts(value)
	if !ok {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), true
}

// dateKeyIn mirrors dateKey(date, timezone): the timezone-local calendar key.
func dateKeyIn(instant time.Time, location *time.Location) string {
	local := instant.In(location)
	return local.Format("2006-01-02")
}

// hourKeyIn mirrors hourKey(date, timezone).
func hourKeyIn(instant time.Time, location *time.Location) string {
	return instant.In(location).Format("2006-01-02T15")
}

// timezoneLocation resolves and caches a validated timezone per request path.
func timezoneLocation(ctx context.Context, source TimezoneSource) (*time.Location, error) {
	name, err := source(ctx)
	if err != nil {
		return nil, err
	}
	return time.LoadLocation(name)
}

// normalizeRange mirrors normalizeAccountUsageStatsRange: calendar clamping to
// the 31-day window ending today (timezone aware).
func normalizeRange(inputStart, inputEnd string, todayKey string) Range {
	todayY, todayM, todayD, _ := parseDateKeyParts(todayKey)
	today := time.Date(todayY, time.Month(todayM), todayD, 0, 0, 0, 0, time.UTC)
	earliestSupported := today.AddDate(0, 0, -(accountUsageStatsMaxRangeDays - 1))
	end := today
	if key, ok := queryDateKey(inputEnd); ok {
		end = key
	}
	if end.After(today) {
		end = today
	}
	if end.Before(earliestSupported) {
		end = earliestSupported
	}
	start := today
	if key, ok := queryDateKey(inputStart); ok {
		start = key
	}
	if start.After(today) {
		start = today
	}
	if start.Before(earliestSupported) {
		start = earliestSupported
	}
	if start.After(end) {
		start = end
	}
	earliestStart := end.AddDate(0, 0, -(accountUsageStatsMaxRangeDays - 1))
	if start.Before(earliestStart) {
		start = earliestStart
	}
	return Range{
		StartDate: start.Format("2006-01-02"),
		EndDate:   end.Format("2006-01-02"),
		Days:      daysBetweenInclusive(start, end),
		MaxDays:   accountUsageStatsMaxRangeDays,
	}
}

func queryDateKey(raw string) (time.Time, bool) {
	return dateKeyTime(strings.TrimSpace(raw))
}

// daysBetweenInclusive mirrors daysBetweenInclusive (calendar days).
func daysBetweenInclusive(start, end time.Time) int {
	return int(end.Sub(start).Hours()/24) + 1
}

// dateKeysInRange mirrors dateKeysInRange (bounded to 31 keys).
func dateKeysInRange(r Range) []string {
	startY, startM, startD, okStart := parseDateKeyParts(r.StartDate)
	endY, endM, endD, okEnd := parseDateKeyParts(r.EndDate)
	if !okStart || !okEnd {
		return nil
	}
	start := time.Date(startY, time.Month(startM), startD, 0, 0, 0, 0, time.UTC)
	end := time.Date(endY, time.Month(endM), endD, 0, 0, 0, 0, time.UTC)
	if start.After(end) {
		return nil
	}
	days := daysBetweenInclusive(start, end)
	if days > accountUsageStatsMaxRangeDays {
		days = accountUsageStatsMaxRangeDays
	}
	keys := make([]string, 0, days)
	for index := 0; index < days; index++ {
		keys = append(keys, start.AddDate(0, 0, index).Format("2006-01-02"))
	}
	return keys
}

// nextCalendarDateKey mirrors nextCalendarDateKey (timezone-free date +1d).
func nextCalendarDateKey(value string) string {
	date, ok := dateKeyTime(value)
	if !ok {
		return value
	}
	return date.AddDate(0, 0, 1).Format("2006-01-02")
}

// fixedUsageStatsDefaultRange mirrors fixedUsageStatsDefaultRange: the 31-day
// window ending today.
func fixedUsageStatsDefaultRange(todayKey string) Range {
	end, ok := dateKeyTime(todayKey)
	if !ok {
		return Range{StartDate: todayKey, EndDate: todayKey, Days: 1, MaxDays: fixedRangeWindowDays}
	}
	start := end.AddDate(0, 0, -(fixedRangeWindowDays - 1))
	return Range{
		StartDate: start.Format("2006-01-02"),
		EndDate:   end.Format("2006-01-02"),
		Days:      fixedRangeWindowDays,
		MaxDays:   fixedRangeWindowDays,
	}
}

// startOfZonedDateKeyIso mirrors startOfZonedDateKeyIso: the UTC instant where
// the given calendar day begins in the timezone (binary search over epoch ms,
// DST safe). Returns "" when the key cannot be resolved.
func startOfZonedDateKeyIso(dateKeyValue string, location *time.Location) string {
	base, ok := dateKeyTime(dateKeyValue)
	if !ok {
		return ""
	}
	utcStart := base.UnixMilli()
	low, high := utcStart-48*hourMS, utcStart+48*hourMS
	for guard := 0; guard < 8 && dateKeyAt(low, location) >= dateKeyValue; guard++ {
		high = low
		low -= 48 * hourMS
	}
	for guard := 0; guard < 8 && dateKeyAt(high, location) < dateKeyValue; guard++ {
		low = high + 1
		high += 48 * hourMS
	}
	if dateKeyAt(high, location) < dateKeyValue {
		return ""
	}
	for low < high {
		mid := (low + high) / 2
		if dateKeyAt(mid, location) >= dateKeyValue {
			high = mid
		} else {
			low = mid + 1
		}
	}
	return time.UnixMilli(low).UTC().Format("2006-01-02T15:04:05.000Z")
}

func dateKeyAt(epochMillis int64, location *time.Location) string {
	return time.UnixMilli(epochMillis).In(location).Format("2006-01-02")
}

// hourBucketsForRange mirrors hourBucketsForRange: every YYYY-MM-DDThh hour of
// each day in the range.
func hourBucketsForRange(r Range) []string {
	dates := dateKeysInRange(r)
	if len(dates) == 0 {
		return nil
	}
	buckets := make([]string, 0, len(dates)*24)
	for _, date := range dates {
		for hour := 0; hour < 24; hour++ {
			buckets = append(buckets, date+"T"+pad2(hour))
		}
	}
	return buckets
}

// hourBucketsUntilNow mirrors hourBucketsUntilNow: the last `hours` local hour
// buckets ending at the current hour.
func hourBucketsUntilNow(hours int, now time.Time, location *time.Location) []string {
	size := hours
	if size < 1 {
		size = 1
	}
	buckets := make([]string, 0, size)
	for index := 0; index < size; index++ {
		buckets = append(buckets, hourKeyIn(now.Add(time.Duration(-(size-1-index))*time.Hour), location))
	}
	return buckets
}

// rangeWindowKey mirrors rangeWindowKey: "startDate:endDate".
func rangeWindowKey(r Range) string { return r.StartDate + ":" + r.EndDate }

// averageFromSum mirrors averageFromSum: rounded mean, undefined when the
// count is zero.
func averageFromSum(sum, count any) *int64 {
	sumF := numberOrZero(sum)
	countF := numberOrZero(count)
	if countF > 0 {
		rounded := int64(mathRound(sumF / countF))
		return &rounded
	}
	return nil
}

// maxFromCountedMetric mirrors maxFromCountedMetric.
func maxFromCountedMetric(value any, count int64) *int64 {
	number := numberOrZero(value)
	if count > 0 {
		rounded := int64(mathRound(number))
		if rounded < 0 {
			rounded = 0
		}
		return &rounded
	}
	return nil
}

func numberOrZero(value any) float64 {
	if value == nil {
		return 0
	}
	if number, ok := toFloat(value); ok {
		return number
	}
	return 0
}

func numberOrUndefined(value any) *float64 {
	if value == nil {
		return nil
	}
	if number, ok := toFloat(value); ok {
		return &number
	}
	return nil
}

func toFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case int64:
		return float64(typed), true
	case int:
		return float64(typed), true
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	case []byte:
		return toFloat(string(typed))
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func mathRound(value float64) float64 {
	if value < 0 {
		return -float64(int64(-value + 0.5))
	}
	return float64(int64(value + 0.5))
}

func pad2(value int) string {
	if value < 10 {
		return "0" + strconv.Itoa(value)
	}
	return strconv.Itoa(value)
}

// ---------------------------------------------------------------------------
// Query parameter mirrors (shared/query-values.ts).
// ---------------------------------------------------------------------------

// optionalQueryText mirrors optionalQueryText (trimmed first value).
func optionalQueryText(values url.Values, key string) string {
	return strings.TrimSpace(values.Get(key))
}

// integerQueryValue mirrors integerQueryValue: absent or non-integer -> 0
// sentinel interpreted as undefined by the callers.
func integerQueryValue(values url.Values, key string) (int, bool) {
	text := strings.TrimSpace(values.Get(key))
	if text == "" {
		return 0, false
	}
	parsed, err := strconv.Atoi(text)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// finiteNumberQueryValue mirrors finiteNumberQueryValue.
func finiteNumberQueryValue(values url.Values, key string) (float64, bool) {
	text := strings.TrimSpace(values.Get(key))
	if text == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// parseAccountIDs mirrors stats.routes.ts parseAccountIds: comma-splitting,
// trim, de-duplication in first-seen order.
func parseAccountIDs(rawValues []string) []string {
	seen := map[string]bool{}
	ids := []string{}
	for _, rawValue := range rawValues {
		for _, item := range strings.Split(rawValue, ",") {
			id := strings.TrimSpace(item)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}
