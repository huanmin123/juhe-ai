package apikeyschedule

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const scheduleModeWindows = "allow_windows"

type scheduleWindow struct {
	daysOfWeek []int
	start      string
	end        string
	startMin   int
	endMin     int
}

type exceptionWindow struct {
	start    string
	end      string
	startMin int
	endMin   int
}

func ParseJSON(raw *string, now time.Time, defaultTimezone string) (map[string]any, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(*raw)))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("availabilitySchedule JSON 无效: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("availabilitySchedule JSON 必须只包含一个对象")
	}
	if value == nil {
		return nil, nil
	}
	normalized, _, err := Normalize(value, now, defaultTimezone)
	return normalized, err
}

func Normalize(
	record map[string]any,
	now time.Time,
	defaultTimezone string,
) (map[string]any, bool, error) {
	allowedKeys := map[string]bool{
		"enabled":    true,
		"timezone":   true,
		"mode":       true,
		"windows":    true,
		"dateRange":  true,
		"exceptions": true,
	}
	for key := range record {
		if !allowedKeys[key] {
			return nil, false, invalidf("availabilitySchedule 包含未知字段：%s", key)
		}
	}
	enabled, ok := record["enabled"].(bool)
	if !ok || !enabled {
		return nil, false, invalidf("availabilitySchedule.enabled 必须为 true")
	}
	mode, _ := record["mode"].(string)
	if strings.TrimSpace(mode) != scheduleModeWindows {
		return nil, false, invalidf("availabilitySchedule.mode 必须为 allow_windows")
	}
	timezone := strings.TrimSpace(defaultTimezone)
	if timezone == "" {
		timezone = "UTC"
	}
	if raw, exists := record["timezone"]; exists {
		text, ok := raw.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, false, invalidf("availabilitySchedule.timezone 无效")
		}
		timezone = strings.TrimSpace(text)
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, false, invalidf("availabilitySchedule.timezone 无效")
	}
	windows, err := normalizeScheduleWindows(record["windows"])
	if err != nil {
		return nil, false, err
	}
	localNow := now.In(location)
	var dateRange map[string]any
	dateAllowed := true
	if raw, exists := record["dateRange"]; exists {
		dateRange, dateAllowed, err = normalizeScheduleDateRange(raw, localNow)
		if err != nil {
			return nil, false, err
		}
	}
	var exceptions []map[string]any
	exceptionSet := false
	exceptionAllowed := false
	if raw, exists := record["exceptions"]; exists {
		exceptions, exceptionSet, exceptionAllowed, err = normalizeScheduleExceptions(raw, localNow)
		if err != nil {
			return nil, false, err
		}
	}
	allowed := dateAllowed && scheduleWindowsAllow(windows, localNow)
	if exceptionSet {
		allowed = exceptionAllowed
	}

	out := map[string]any{
		"enabled":  true,
		"timezone": timezone,
		"mode":     scheduleModeWindows,
		"windows":  scheduleWindowsOutput(windows),
	}
	if dateRange != nil {
		out["dateRange"] = dateRange
	}
	if exceptions != nil {
		out["exceptions"] = exceptions
	}
	return out, allowed, nil
}

func normalizeScheduleWindows(raw any) ([]scheduleWindow, error) {
	items, ok := raw.([]any)
	if !ok || len(items) < 1 || len(items) > 32 {
		return nil, invalidf("availabilitySchedule.windows 数量无效")
	}
	out := make([]scheduleWindow, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, invalidf("availabilitySchedule.windows 项必须是对象")
		}
		for key := range record {
			if key != "daysOfWeek" && key != "start" && key != "end" {
				return nil, invalidf("availabilitySchedule.windows 包含未知字段：%s", key)
			}
		}
		days, err := normalizeDaysOfWeek(record["daysOfWeek"])
		if err != nil {
			return nil, err
		}
		start, startMin, err := normalizeHHMM(record["start"])
		if err != nil {
			return nil, invalidf("availabilitySchedule.windows.start 无效")
		}
		end, endMin, err := normalizeHHMM(record["end"])
		if err != nil || startMin == endMin {
			return nil, invalidf("availabilitySchedule.windows.end 无效")
		}
		out = append(out, scheduleWindow{
			daysOfWeek: days,
			start:      start,
			end:        end,
			startMin:   startMin,
			endMin:     endMin,
		})
	}
	return out, nil
}

func normalizeDaysOfWeek(raw any) ([]int, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, invalidf("daysOfWeek 必须是非空数组")
	}
	seen := map[int]struct{}{}
	out := make([]int, 0, len(items))
	for _, item := range items {
		value, err := normalizedInteger(item, 1, 7)
		if err != nil {
			return nil, invalidf("daysOfWeek 必须是 1-7 的整数")
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	for i := 0; i < len(out)-1; i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

func normalizeScheduleDateRange(raw any, now time.Time) (map[string]any, bool, error) {
	record, ok := raw.(map[string]any)
	if !ok {
		return nil, false, invalidf("availabilitySchedule.dateRange 必须是对象")
	}
	for key := range record {
		if key != "startDate" && key != "endDate" {
			return nil, false, invalidf("dateRange 包含未知字段：%s", key)
		}
	}
	var startText string
	var startDate *time.Time
	var err error
	if rawStart, exists := record["startDate"]; exists {
		startText, startDate, err = requiredDate(rawStart)
		if err != nil {
			return nil, false, err
		}
	}
	var endText string
	var endDate *time.Time
	if rawEnd, exists := record["endDate"]; exists {
		endText, endDate, err = requiredDate(rawEnd)
		if err != nil {
			return nil, false, err
		}
	}
	if startDate != nil && endDate != nil && startDate.After(*endDate) {
		return nil, false, invalidf("dateRange.startDate 不能晚于 endDate")
	}
	today := dateOnly(now)
	allowed := (startDate == nil || !today.Before(*startDate)) &&
		(endDate == nil || !today.After(*endDate))
	out := map[string]any{}
	if startText != "" {
		out["startDate"] = startText
	}
	if endText != "" {
		out["endDate"] = endText
	}
	if len(out) == 0 {
		return nil, allowed, nil
	}
	return out, allowed, nil
}

func normalizeScheduleExceptions(raw any, now time.Time) ([]map[string]any, bool, bool, error) {
	items, ok := raw.([]any)
	if !ok || len(items) > 128 {
		return nil, false, false, invalidf("availabilitySchedule.exceptions 数量无效")
	}
	out := make([]map[string]any, 0, len(items))
	today := now.Format("2006-01-02")
	activeSet := false
	activeAllowed := false
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, false, false, invalidf("exceptions 项必须是对象")
		}
		for key := range record {
			if key != "date" && key != "action" && key != "windows" {
				return nil, false, false, invalidf("exceptions 包含未知字段：%s", key)
			}
		}
		dateText, _, err := requiredDate(record["date"])
		if err != nil {
			return nil, false, false, err
		}
		action, ok := record["action"].(string)
		if !ok || (action != "allow" && action != "deny") {
			return nil, false, false, invalidf("exceptions.action 无效")
		}
		itemOut := map[string]any{"date": dateText, "action": action}
		if action == "deny" {
			if _, exists := record["windows"]; exists {
				return nil, false, false, invalidf("deny exception 不能包含 windows")
			}
			if dateText == today {
				activeSet = true
				activeAllowed = false
			}
			out = append(out, itemOut)
			continue
		}
		windows, err := normalizeExceptionWindows(record["windows"])
		if err != nil {
			return nil, false, false, err
		}
		itemOut["windows"] = exceptionWindowsOutput(windows)
		if dateText == today {
			activeSet = true
			activeAllowed = exceptionWindowsAllow(windows, now)
		}
		out = append(out, itemOut)
	}
	if len(out) == 0 {
		return nil, activeSet, activeAllowed, nil
	}
	return out, activeSet, activeAllowed, nil
}

func normalizeExceptionWindows(raw any) ([]exceptionWindow, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 || len(items) > 32 {
		return nil, invalidf("allow exception windows 数量无效")
	}
	out := make([]exceptionWindow, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, invalidf("exception windows 项必须是对象")
		}
		for key := range record {
			if key != "start" && key != "end" {
				return nil, invalidf("exception windows 包含未知字段：%s", key)
			}
		}
		start, startMin, err := normalizeHHMM(record["start"])
		if err != nil {
			return nil, invalidf("exception windows.start 无效")
		}
		end, endMin, err := normalizeHHMM(record["end"])
		if err != nil || startMin == endMin {
			return nil, invalidf("exception windows.end 无效")
		}
		out = append(out, exceptionWindow{
			start:    start,
			end:      end,
			startMin: startMin,
			endMin:   endMin,
		})
	}
	return out, nil
}

func requiredDate(raw any) (string, *time.Time, error) {
	text, ok := raw.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", nil, invalidf("日期必须是 YYYY-MM-DD")
	}
	text = strings.TrimSpace(text)
	parsed, err := time.ParseInLocation("2006-01-02", text, time.UTC)
	if err != nil {
		return "", nil, invalidf("日期必须是 YYYY-MM-DD")
	}
	return text, &parsed, nil
}

func normalizeHHMM(raw any) (string, int, error) {
	text, ok := raw.(string)
	if !ok {
		return "", 0, fmt.Errorf("time must be string")
	}
	text = strings.TrimSpace(text)
	if len(text) != 5 || text[2] != ':' {
		return "", 0, fmt.Errorf("time must be HH:mm")
	}
	hour, err := strconv.Atoi(text[:2])
	if err != nil || hour < 0 || hour > 23 {
		return "", 0, fmt.Errorf("hour invalid")
	}
	minute, err := strconv.Atoi(text[3:])
	if err != nil || minute < 0 || minute > 59 {
		return "", 0, fmt.Errorf("minute invalid")
	}
	return fmt.Sprintf("%02d:%02d", hour, minute), hour*60 + minute, nil
}

func scheduleWindowsAllow(windows []scheduleWindow, now time.Time) bool {
	today := scheduleDay(now)
	yesterday := today - 1
	if yesterday == 0 {
		yesterday = 7
	}
	minute := now.Hour()*60 + now.Minute()
	for _, window := range windows {
		if window.startMin < window.endMin {
			if containsInt(window.daysOfWeek, today) &&
				minute >= window.startMin &&
				minute < window.endMin {
				return true
			}
			continue
		}
		if containsInt(window.daysOfWeek, today) && minute >= window.startMin {
			return true
		}
		if containsInt(window.daysOfWeek, yesterday) && minute < window.endMin {
			return true
		}
	}
	return false
}

func exceptionWindowsAllow(windows []exceptionWindow, now time.Time) bool {
	minute := now.Hour()*60 + now.Minute()
	for _, window := range windows {
		if window.startMin < window.endMin {
			if minute >= window.startMin && minute < window.endMin {
				return true
			}
			continue
		}
		if minute >= window.startMin || minute < window.endMin {
			return true
		}
	}
	return false
}

func scheduleDay(now time.Time) int {
	switch now.Weekday() {
	case time.Monday:
		return 1
	case time.Tuesday:
		return 2
	case time.Wednesday:
		return 3
	case time.Thursday:
		return 4
	case time.Friday:
		return 5
	case time.Saturday:
		return 6
	default:
		return 7
	}
}

func dateOnly(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func scheduleWindowsOutput(windows []scheduleWindow) []map[string]any {
	out := make([]map[string]any, 0, len(windows))
	for _, window := range windows {
		out = append(out, map[string]any{
			"daysOfWeek": window.daysOfWeek,
			"start":      window.start,
			"end":        window.end,
		})
	}
	return out
}

func exceptionWindowsOutput(windows []exceptionWindow) []map[string]any {
	out := make([]map[string]any, 0, len(windows))
	for _, window := range windows {
		out = append(out, map[string]any{
			"start": window.start,
			"end":   window.end,
		})
	}
	return out
}

func normalizedInteger(raw any, minValue int, maxValue int) (int, error) {
	var text string
	switch typed := raw.(type) {
	case json.Number:
		text = typed.String()
	case float64:
		text = strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		text = strconv.Itoa(typed)
	case int64:
		text = strconv.FormatInt(typed, 10)
	default:
		return 0, fmt.Errorf("invalid integer")
	}
	if strings.ContainsAny(text, ".eE") {
		return 0, fmt.Errorf("invalid integer")
	}
	value, err := strconv.Atoi(text)
	if err != nil || value < minValue || value > maxValue {
		return 0, fmt.Errorf("invalid integer")
	}
	return value, nil
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func invalidf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
