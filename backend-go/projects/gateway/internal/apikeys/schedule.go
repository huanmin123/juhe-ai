package apikeys

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Availability schedule model mirrors storage/api-key-availability-schedule.ts
// and the stored JSON shape (enabled/timezone/mode/windows/dateRange/
// exceptions) written by apiKeyAvailabilityScheduleJson.

// ScheduleWindow is one allowed window; DaysOfWeek is 1..7 (Monday..Sunday).
type ScheduleWindow struct {
	DaysOfWeek []int  `json:"daysOfWeek"`
	Start      string `json:"start"`
	End        string `json:"end"`
}

// ScheduleException overrides a single date with allow windows or a deny.
type ScheduleException struct {
	Date    string           `json:"date"`
	Action  string           `json:"action"`
	Windows []ScheduleWindow `json:"windows,omitempty"`
}

// ScheduleDateRange bounds the whole schedule to a date interval.
type ScheduleDateRange struct {
	StartDate string `json:"startDate,omitempty"`
	EndDate   string `json:"endDate,omitempty"`
}

// AvailabilitySchedule is the stored/normalized schedule document.
type AvailabilitySchedule struct {
	Enabled    bool                `json:"enabled"`
	Timezone   string              `json:"timezone"`
	Mode       string              `json:"mode"`
	Windows    []ScheduleWindow    `json:"windows"`
	DateRange  *ScheduleDateRange  `json:"dateRange,omitempty"`
	Exceptions []ScheduleException `json:"exceptions,omitempty"`
}

const (
	scheduleModeAllowWindows = "allow_windows"
	scheduleExceptionAllow   = "allow"
	scheduleExceptionDeny    = "deny"

	maxScheduleWindows       = 32
	maxScheduleExceptions    = 128
	scheduleNextCheckHorizon = 14 // days of boundary candidates
	scheduleNextCheckFollow  = 7  // days fallback when no boundary exists
)

var (
	scheduleTimePattern = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)
	scheduleDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// allDaysOfWeek mirrors allDaysOfWeek (Monday..Sunday).
func allDaysOfWeek() []int { return []int{1, 2, 3, 4, 5, 6, 7} }

// NormalizeSchedule mirrors normalizeApiKeyAvailabilitySchedule: nil input
// (field absent or null) yields nil; anything else must be a schedule object
// with enabled=true, mode=allow_windows and at least one window.
func NormalizeSchedule(input any) (*AvailabilitySchedule, error) {
	return normalizeScheduleWithDefault(input, fallbackScheduleTimezone)
}

// normalizeScheduleWithDefault threads the default-timezone resolver through
// the normalization (Node resolves it lazily inside normalizeScheduleTimezone
// via defaultScheduleTimezone → usageStatsTimezone; laziness matters because
// the resolver hits the business database).
func normalizeScheduleWithDefault(input any, defaultTimezone func() string) (*AvailabilitySchedule, error) {
	if input == nil {
		return nil, nil
	}
	object, ok := input.(map[string]any)
	if !ok {
		return nil, &ValidationError{Message: "API Key 时间计划参数无效"}
	}
	if err := assertOnlyKeys(object, []string{"enabled", "timezone", "mode", "windows", "dateRange", "exceptions"}, "API Key 时间计划"); err != nil {
		return nil, err
	}
	if enabled, ok := object["enabled"].(bool); !ok || !enabled {
		return nil, &ValidationError{Message: "API Key 时间计划启用状态必须为 true"}
	}
	if mode, _ := object["mode"].(string); mode != scheduleModeAllowWindows {
		return nil, &ValidationError{Message: "API Key 时间计划模式必须为 allow_windows"}
	}
	timezone, err := normalizeScheduleTimezone(object["timezone"], objectKeyPresent(object, "timezone"), defaultTimezone)
	if err != nil {
		return nil, err
	}
	windows, err := normalizeScheduleWindows(object["windows"], true)
	if err != nil {
		return nil, err
	}
	if len(windows) == 0 {
		return nil, &ValidationError{Message: "API Key 时间计划至少需要一个允许时段"}
	}
	dateRange, err := normalizeScheduleDateRange(object["dateRange"], objectKeyPresent(object, "dateRange"))
	if err != nil {
		return nil, err
	}
	exceptions, err := normalizeScheduleExceptions(object["exceptions"], objectKeyPresent(object, "exceptions"))
	if err != nil {
		return nil, err
	}
	return &AvailabilitySchedule{
		Enabled:    true,
		Timezone:   timezone,
		Mode:       scheduleModeAllowWindows,
		Windows:    windows,
		DateRange:  dateRange,
		Exceptions: exceptions,
	}, nil
}

// ParseScheduleJSON mirrors parseApiKeyAvailabilityScheduleJson (stored rows
// are re-normalized on read; invalid rows surface as errors). Only falsy
// values (empty string) mean "no schedule": whitespace-only storage is a
// corruption signal and fails the read exactly like Node's JSON.parse throw.
func ParseScheduleJSON(raw string) (*AvailabilitySchedule, error) {
	return parseScheduleJSONWithDefault(raw, fallbackScheduleTimezone)
}

// parseScheduleJSON re-parses a stored schedule with the store-resolved
// default timezone. The resolver stays lazy: it must only run when a stored
// row actually lacks a timezone (reading it eagerly would nest a
// system_settings query inside open list cursors / write transactions on the
// single-connection SQLite pool). Both the JSON syntax error and the
// normalization error surface as plain errors (Node throws raw errors here;
// the routes render them as 500, never as the input-validation 400 set).
func (s *Store) parseScheduleJSON(raw string) (*AvailabilitySchedule, error) {
	return parseScheduleJSONWithDefault(raw, s.defaultScheduleTimezone)
}

func parseScheduleJSONWithDefault(raw string, defaultTimezone func() string) (*AvailabilitySchedule, error) {
	if raw == "" {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, fmt.Errorf("存储的 API Key 时间计划 JSON 解析失败: %w", err)
	}
	schedule, err := normalizeScheduleWithDefault(decoded, defaultTimezone)
	if err != nil {
		return nil, fmt.Errorf("存储的 API Key 时间计划无效: %w", err)
	}
	return schedule, nil
}

// ScheduleJSON mirrors apiKeyAvailabilityScheduleJson: nil → NULL column.
func ScheduleJSON(schedule *AvailabilitySchedule) (string, bool) {
	if schedule == nil {
		return "", false
	}
	encoded, err := json.Marshal(schedule)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

// ScheduleStatus mirrors apiKeyAvailabilityScheduleStatus: nil when the
// schedule is absent/disabled, otherwise active/disabled for `now`.
func ScheduleStatus(schedule *AvailabilitySchedule, now time.Time) (string, bool) {
	if schedule == nil || !schedule.Enabled {
		return "", false
	}
	if scheduleAllows(schedule, scheduleZonedParts(now, schedule.Timezone)) {
		return "active", true
	}
	return "disabled", true
}

func normalizeScheduleTimezone(value any, present bool, defaultTimezone func() string) (string, error) {
	if !present {
		// Node: timezone === undefined → defaultScheduleTimezone(). Resolved
		// lazily so read/write paths never nest a settings query.
		return defaultTimezone(), nil
	}
	if value == nil {
		// Optional-but-not-nullable: explicit JSON null is rejected by the
		// zod route schema ("Expected string, received null").
		return "", &ValidationError{Message: patchTypeIssue("string", nil)}
	}
	text, ok := value.(string)
	if !ok {
		return "", &ValidationError{Message: patchTypeIssue("string", value)}
	}
	timezone := strings.TrimSpace(text)
	if timezone == "" {
		return "", &ValidationError{Message: "API Key 时间计划时区不能为空"}
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return "", &ValidationError{Message: "API Key 时间计划时区无效"}
	}
	return timezone, nil
}

// fallbackScheduleTimezone mirrors DEFAULT_USAGE_STATS_TIMEZONE: the
// deployment timezone when resolvable through the tz database, otherwise UTC
// (Node: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC').
func fallbackScheduleTimezone() string {
	if name := time.Local.String(); name != "" && name != "Local" {
		if _, err := time.LoadLocation(name); err == nil {
			return name
		}
	}
	return "UTC"
}

func normalizeScheduleWindows(input any, requireDays bool) ([]ScheduleWindow, error) {
	list, ok := input.([]any)
	if !ok {
		return nil, &ValidationError{Message: "API Key 时间计划时段无效"}
	}
	if len(list) > maxScheduleWindows {
		return nil, &ValidationError{Message: fmt.Sprintf("API Key 时间计划最多支持 %d 个时段", maxScheduleWindows)}
	}
	windows := make([]ScheduleWindow, 0, len(list))
	for _, item := range list {
		window, err := normalizeScheduleWindow(item, requireDays)
		if err != nil {
			return nil, err
		}
		windows = append(windows, window)
	}
	return windows, nil
}

func normalizeScheduleWindow(input any, requireDays bool) (ScheduleWindow, error) {
	object, ok := input.(map[string]any)
	if !ok {
		return ScheduleWindow{}, &ValidationError{Message: "API Key 时间计划时段无效"}
	}
	keys := []string{"start", "end"}
	if requireDays {
		keys = []string{"daysOfWeek", "start", "end"}
	}
	if err := assertOnlyKeys(object, keys, "API Key 时间计划时段"); err != nil {
		return ScheduleWindow{}, err
	}
	start, err := normalizeScheduleTime(object["start"], "开始时间")
	if err != nil {
		return ScheduleWindow{}, err
	}
	end, err := normalizeScheduleTime(object["end"], "停止时间")
	if err != nil {
		return ScheduleWindow{}, err
	}
	if start == end {
		return ScheduleWindow{}, &ValidationError{Message: "API Key 时间计划开始时间和停止时间不能相同"}
	}
	if !requireDays {
		return ScheduleWindow{DaysOfWeek: allDaysOfWeek(), Start: start, End: end}, nil
	}
	days, err := normalizeDaysOfWeek(object["daysOfWeek"])
	if err != nil {
		return ScheduleWindow{}, err
	}
	return ScheduleWindow{DaysOfWeek: days, Start: start, End: end}, nil
}

func normalizeScheduleTime(value any, label string) (string, error) {
	text, ok := value.(string)
	if !ok || !scheduleTimePattern.MatchString(strings.TrimSpace(text)) {
		return "", &ValidationError{Message: "API Key 时间计划" + label + "格式应为 HH:mm"}
	}
	return strings.TrimSpace(text), nil
}

func normalizeDaysOfWeek(value any) ([]int, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, &ValidationError{Message: "API Key 时间计划重复日期无效"}
	}
	seen := map[int]bool{}
	days := []int{}
	for _, item := range list {
		day, isNumber := item.(float64)
		if !isNumber || day != float64(int(day)) || int(day) < 1 || int(day) > 7 {
			return nil, &ValidationError{Message: "API Key 时间计划重复日期无效"}
		}
		number := int(day)
		if !seen[number] {
			seen[number] = true
			days = append(days, number)
		}
	}
	if len(days) == 0 {
		return nil, &ValidationError{Message: "API Key 时间计划至少需要选择一个重复日期"}
	}
	sort.Ints(days)
	return days, nil
}

func normalizeScheduleDateRange(input any, present bool) (*ScheduleDateRange, error) {
	if !present {
		return nil, nil
	}
	if input == nil {
		// Optional-but-not-nullable: explicit null is a zod-level rejection.
		return nil, &ValidationError{Message: patchTypeIssue("object", nil)}
	}
	object, ok := input.(map[string]any)
	if !ok {
		return nil, &ValidationError{Message: "API Key 时间计划生效日期范围无效"}
	}
	if err := assertOnlyKeys(object, []string{"startDate", "endDate"}, "API Key 时间计划生效日期范围"); err != nil {
		return nil, err
	}
	startDate, err := normalizeDateKey(object["startDate"], objectKeyPresent(object, "startDate"), "开始日期")
	if err != nil {
		return nil, err
	}
	endDate, err := normalizeDateKey(object["endDate"], objectKeyPresent(object, "endDate"), "结束日期")
	if err != nil {
		return nil, err
	}
	if startDate != "" && endDate != "" && startDate > endDate {
		return nil, &ValidationError{Message: "API Key 时间计划开始日期不能晚于结束日期"}
	}
	if startDate == "" && endDate == "" {
		return nil, nil
	}
	return &ScheduleDateRange{StartDate: startDate, EndDate: endDate}, nil
}

func normalizeScheduleExceptions(input any, present bool) ([]ScheduleException, error) {
	if !present {
		return nil, nil
	}
	if input == nil {
		// Optional-but-not-nullable: explicit null is a zod-level rejection.
		return nil, &ValidationError{Message: patchTypeIssue("array", nil)}
	}
	list, ok := input.([]any)
	if !ok {
		return nil, &ValidationError{Message: "API Key 时间计划例外日期无效"}
	}
	if len(list) > maxScheduleExceptions {
		return nil, &ValidationError{Message: fmt.Sprintf("API Key 时间计划最多支持 %d 个例外日期", maxScheduleExceptions)}
	}
	exceptions := make([]ScheduleException, 0, len(list))
	for _, item := range list {
		exception, err := normalizeScheduleException(item)
		if err != nil {
			return nil, err
		}
		exceptions = append(exceptions, exception)
	}
	if len(exceptions) == 0 {
		return nil, nil
	}
	return exceptions, nil
}

func normalizeScheduleException(input any) (ScheduleException, error) {
	object, ok := input.(map[string]any)
	if !ok {
		return ScheduleException{}, &ValidationError{Message: "API Key 时间计划例外日期无效"}
	}
	if err := assertOnlyKeys(object, []string{"date", "action", "windows"}, "API Key 时间计划例外日期"); err != nil {
		return ScheduleException{}, err
	}
	date, err := normalizeDateKey(object["date"], objectKeyPresent(object, "date"), "例外日期")
	if err != nil {
		return ScheduleException{}, err
	}
	if date == "" {
		return ScheduleException{}, &ValidationError{Message: "API Key 时间计划例外日期不能为空"}
	}
	action, _ := object["action"].(string)
	if action != scheduleExceptionAllow && action != scheduleExceptionDeny {
		return ScheduleException{}, &ValidationError{Message: "API Key 时间计划例外动作无效"}
	}
	windows, windowsPresent := object["windows"]
	if action == scheduleExceptionDeny && windowsPresent {
		if windows == nil {
			// zod rejects the null array before superRefine runs.
			return ScheduleException{}, &ValidationError{Message: patchTypeIssue("array", nil)}
		}
		// windows === undefined is the only allowed deny shape.
		return ScheduleException{}, &ValidationError{Message: "API Key 时间计划拒绝例外不能配置允许时段"}
	}
	if action == scheduleExceptionAllow {
		if !windowsPresent {
			return ScheduleException{}, &ValidationError{Message: "API Key 时间计划允许例外至少需要一个允许时段"}
		}
		if windows == nil {
			return ScheduleException{}, &ValidationError{Message: patchTypeIssue("array", nil)}
		}
		allowWindows, windowErr := normalizeScheduleWindows(windows, false)
		if windowErr != nil {
			return ScheduleException{}, windowErr
		}
		if len(allowWindows) == 0 {
			return ScheduleException{}, &ValidationError{Message: "API Key 时间计划允许例外至少需要一个允许时段"}
		}
		stripped := make([]ScheduleWindow, 0, len(allowWindows))
		for _, window := range allowWindows {
			stripped = append(stripped, ScheduleWindow{Start: window.Start, End: window.End})
		}
		return ScheduleException{Date: date, Action: action, Windows: stripped}, nil
	}
	return ScheduleException{Date: date, Action: action}, nil
}

func normalizeDateKey(value any, present bool, label string) (string, error) {
	if !present {
		return "", nil
	}
	if value == nil {
		return "", &ValidationError{Message: patchTypeIssue("string", nil)}
	}
	text, ok := value.(string)
	if !ok || !scheduleDatePattern.MatchString(strings.TrimSpace(text)) {
		return "", &ValidationError{Message: "API Key 时间计划" + label + "格式应为 YYYY-MM-DD"}
	}
	date := strings.TrimSpace(text)
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil || parsed.UTC().Format("2006-01-02") != date {
		return "", &ValidationError{Message: "API Key 时间计划" + label + "无效"}
	}
	return date, nil
}

// objectKeyPresent distinguishes a missing JSON key (undefined in Node) from
// an explicit null: Go maps collapse both to a nil lookup.
func objectKeyPresent(object map[string]any, key string) bool {
	_, exists := object[key]
	return exists
}

func assertOnlyKeys(value map[string]any, allowed []string, label string) error {
	set := map[string]bool{}
	for _, key := range allowed {
		set[key] = true
	}
	for key := range value {
		if !set[key] {
			return &ValidationError{Message: label + "包含不支持字段：" + key}
		}
	}
	return nil
}

// scheduleAllows mirrors isCurrentTimeAllowedBySchedule: at least one window
// occurrence (regular or allow-exception) covers the zoned current minute.
func scheduleAllows(schedule *AvailabilitySchedule, current zonedParts) bool {
	return len(allowedScheduleOccurrences(schedule, current)) > 0
}

type zonedParts struct {
	dateKey     string
	dayOfWeek   int
	minuteOfDay int
}

func scheduleZonedParts(now time.Time, timezone string) zonedParts {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		location = time.UTC
	}
	local := now.In(location)
	utcDay := int(time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC).Weekday())
	if utcDay == 0 {
		utcDay = 7
	}
	return zonedParts{
		dateKey:     local.Format("2006-01-02"),
		dayOfWeek:   utcDay,
		minuteOfDay: local.Hour()*60 + local.Minute(),
	}
}

// allowedScheduleOccurrences mirrors allowedScheduleWindowOccurrences: the
// schedule may spill across midnight, so both today and yesterday are start
// candidates.
func allowedScheduleOccurrences(schedule *AvailabilitySchedule, current zonedParts) []occurrence {
	occurrences := []occurrence{}
	for _, startDateKey := range []string{current.dateKey, previousDateKey(current.dateKey)} {
		if !dateInScheduleRange(startDateKey, schedule) {
			continue
		}
		exception := findScheduleException(schedule, startDateKey)
		if exception != nil && exception.Action == scheduleExceptionDeny {
			continue
		}
		if exception != nil && exception.Action == scheduleExceptionAllow {
			for index, window := range exception.Windows {
				if item, ok := windowOccurrence(current, startDateKey, window.Start, window.End,
					fmt.Sprintf("exception:%s:%d", startDateKey, index)); ok {
					occurrences = append(occurrences, item)
				}
			}
			continue
		}
		for index, window := range schedule.Windows {
			if !containsDay(window.DaysOfWeek, dayOfWeekForDateKey(startDateKey)) {
				continue
			}
			if item, ok := windowOccurrence(current, startDateKey, window.Start, window.End,
				fmt.Sprintf("window:%d", index)); ok {
				occurrences = append(occurrences, item)
			}
		}
	}
	return occurrences
}

type occurrence struct {
	startDateKey string
	minuteOfDay  int
	key          string
}

func findScheduleException(schedule *AvailabilitySchedule, dateKey string) *ScheduleException {
	for index := range schedule.Exceptions {
		if schedule.Exceptions[index].Date == dateKey {
			return &schedule.Exceptions[index]
		}
	}
	return nil
}

func windowOccurrence(current zonedParts, startDateKey, startText, endText, token string) (occurrence, bool) {
	start, end := minuteOfDay(startText), minuteOfDay(endText)
	if start < end {
		if current.dateKey == startDateKey && current.minuteOfDay >= start && current.minuteOfDay < end {
			return occurrence{startDateKey: startDateKey, minuteOfDay: start, key: token + ":start:" + startText}, true
		}
		return occurrence{}, false
	}
	// Cross-midnight window: the start side runs into the next date key.
	if current.dateKey == startDateKey && current.minuteOfDay >= start {
		return occurrence{startDateKey: startDateKey, minuteOfDay: start, key: token + ":start:" + startText}, true
	}
	if current.dateKey == nextDateKey(startDateKey) && current.minuteOfDay < end {
		return occurrence{startDateKey: startDateKey, minuteOfDay: start, key: token + ":start:" + startText}, true
	}
	return occurrence{}, false
}

func dateInScheduleRange(dateKey string, schedule *AvailabilitySchedule) bool {
	if schedule.DateRange == nil {
		return true
	}
	if schedule.DateRange.StartDate != "" && dateKey < schedule.DateRange.StartDate {
		return false
	}
	if schedule.DateRange.EndDate != "" && dateKey > schedule.DateRange.EndDate {
		return false
	}
	return true
}

func containsDay(days []int, day int) bool {
	for _, candidate := range days {
		if candidate == day {
			return true
		}
	}
	return false
}

func minuteOfDay(value string) int {
	match := scheduleTimePattern.FindStringSubmatch(value)
	if match == nil {
		return 0
	}
	return int((match[1][0]-'0')*10+(match[1][1]-'0'))*60 + int((match[2][0]-'0')*10+(match[2][1]-'0'))
}

func dateKeyTime(dateKey string) time.Time {
	parsed, _ := time.Parse("2006-01-02", dateKey)
	return parsed.UTC()
}

func dayOfWeekForDateKey(dateKey string) int {
	utcDay := int(dateKeyTime(dateKey).Weekday())
	if utcDay == 0 {
		return 7
	}
	return utcDay
}

func previousDateKey(dateKey string) string {
	return dateKeyTime(dateKey).AddDate(0, 0, -1).Format("2006-01-02")
}

func nextDateKey(dateKey string) string {
	return dateKeyTime(dateKey).AddDate(0, 0, 1).Format("2006-01-02")
}

// NextScheduleCheckAt mirrors nextApiKeyAvailabilityScheduleCheckAt: the
// earliest UTC window boundary after `now`, or now + 7 days when the horizon
// has no boundary (for example an empty weekly coverage). Returns valid=false
// for absent/disabled schedules (NULL column).
func NextScheduleCheckAt(schedule *AvailabilitySchedule, now time.Time) (string, bool) {
	if schedule == nil || !schedule.Enabled {
		return "", false
	}
	candidates := scheduleBoundaryUTCTimes(schedule, now)
	next := int64(-1)
	for _, candidate := range candidates {
		if candidate > now.UnixMilli() && (next < 0 || candidate < next) {
			next = candidate
		}
	}
	if next < 0 {
		next = now.UnixMilli() + int64(scheduleNextCheckFollow)*24*60*60*1000
	}
	return isoMillis(time.UnixMilli(next)), true
}

// scheduleBoundaryUTCTimes mirrors scheduleBoundaryUtcTimes.
func scheduleBoundaryUTCTimes(schedule *AvailabilitySchedule, now time.Time) []int64 {
	current := scheduleZonedParts(now, schedule.Timezone)
	start := dateKeyTime(current.dateKey).AddDate(0, 0, -1)
	seen := map[int64]bool{}
	times := []int64{}
	add := func(dateKey string, minute int) {
		if value, ok := zonedLocalMinuteToUTC(dateKey, minute, schedule.Timezone); ok && !seen[value] {
			seen[value] = true
			times = append(times, value)
		}
	}
	for offset := 0; offset <= scheduleNextCheckHorizon+2; offset++ {
		dateKey := start.AddDate(0, 0, offset).Format("2006-01-02")
		if !dateInScheduleRange(dateKey, schedule) {
			continue
		}
		exception := findScheduleException(schedule, dateKey)
		switch {
		case exception != nil && exception.Action == scheduleExceptionAllow:
			for _, window := range exception.Windows {
				add(dateKey, minuteOfDay(window.Start))
				add(windowEndDateKey(dateKey, window.Start, window.End), minuteOfDay(window.End))
			}
		case exception != nil && exception.Action == scheduleExceptionDeny:
			// The whole date is denied: no boundaries.
		default:
			for _, window := range schedule.Windows {
				if !containsDay(window.DaysOfWeek, dayOfWeekForDateKey(dateKey)) {
					continue
				}
				add(dateKey, minuteOfDay(window.Start))
				add(windowEndDateKey(dateKey, window.Start, window.End), minuteOfDay(window.End))
			}
		}
	}
	sort.Slice(times, func(left, right int) bool { return times[left] < times[right] })
	return times
}

func windowEndDateKey(startDateKey, startText, endText string) string {
	if minuteOfDay(startText) < minuteOfDay(endText) {
		return startDateKey
	}
	return nextDateKey(startDateKey)
}

// zonedLocalMinuteToUTC mirrors zonedLocalMinuteToUtcTime: iteratively snaps
// a naive zoned wall-clock minute onto the real UTC instant (DST safe).
func zonedLocalMinuteToUTC(dateKey string, minute int, timezone string) (int64, bool) {
	if _, err := time.LoadLocation(timezone); err != nil {
		return 0, false
	}
	parsed := dateKeyTime(dateKey)
	year, month, day := parsed.Date()
	targetSerial := parsed.UnixMilli()/60000 + int64(minute)
	guess := time.Date(year, month, day, minute/60, minute%60, 0, 0, time.UTC).UnixMilli()
	for attempt := 0; attempt < 4; attempt++ {
		parts := scheduleZonedParts(time.UnixMilli(guess), timezone)
		currentSerial := dateKeyTime(parts.dateKey).UnixMilli()/60000 + int64(parts.minuteOfDay)
		delta := targetSerial - currentSerial
		if delta == 0 {
			return guess, true
		}
		guess += delta * 60000
	}
	verified := scheduleZonedParts(time.UnixMilli(guess), timezone)
	if verified.dateKey == dateKey && verified.minuteOfDay == minute {
		return guess, true
	}
	return 0, false
}
