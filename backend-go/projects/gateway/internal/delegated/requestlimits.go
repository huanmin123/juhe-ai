// GET /request-limits for the delegated API: the Node requestLimitSnapshot
// (delegated-api.routes.ts) over resolveEffectiveUserRequestLimits
// (domain/user-request-limits.ts). Global limits come from the system
// settings store, the per-user override from system_accounts
// request_limits_json, and the per-bucket usage totals from the runtime
// state Redis (HGET <key> __total via the UsageReader port). Redis
// unavailable or unconfigured mirrors the Node degrade: usageStatus
// "unavailable" with null used/remaining.
package delegated

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// userRequestLimitWindows mirrors USER_REQUEST_LIMIT_WINDOWS.
var userRequestLimitWindows = [4]string{"perMinute", "perDay", "perWeek", "perMonth"}

const (
	maxUserRequestLimitValue = 1_000_000_000
	redisReadDeadlineMs      = 750
)

// userRequestLimitOverride mirrors UserRequestLimits from
// system_accounts.request_limits_json.
type userRequestLimitOverride struct {
	PerMinute *int   `json:"perMinute"`
	PerDay    *int   `json:"perDay"`
	PerWeek   *int   `json:"perWeek"`
	PerMonth  *int   `json:"perMonth"`
	ExpiresOn string `json:"expiresOn"`
}

// parseUserRequestLimitOverride mirrors parseUserRequestLimitsJson over the
// profile row's request_limits_json: invalid payloads degrade to "no
// override" instead of erroring.
func parseUserRequestLimitOverride(raw *sql.NullString) *userRequestLimitOverride {
	if raw == nil || !raw.Valid || raw.String == "" {
		return nil
	}
	var override userRequestLimitOverride
	if err := json.Unmarshal([]byte(raw.String), &override); err != nil {
		return nil
	}
	for _, value := range []*int{override.PerMinute, override.PerDay, override.PerWeek, override.PerMonth} {
		if value == nil {
			continue
		}
		if *value < 0 || *value > maxUserRequestLimitValue {
			return nil
		}
	}
	if !hasOverrideWindow(&override) {
		return nil
	}
	if override.ExpiresOn != "" && !validOverrideExpiresOn(override.ExpiresOn) {
		return nil
	}
	return &override
}

// globalRequestLimitSettings mirrors GlobalUserRequestLimitSettings.
type globalRequestLimitSettings struct {
	PerMinute int
	PerDay    int
	PerWeek   int
	PerMonth  int
	Timezone  string
}

// effectiveRequestLimit mirrors effectiveValue.
type effectiveRequestLimit struct {
	Limit  int
	Source string // "global" | "user"
}

type effectiveRequestLimits struct {
	PerMinute    effectiveRequestLimit
	PerDay       effectiveRequestLimit
	PerWeek      effectiveRequestLimit
	PerMonth     effectiveRequestLimit
	Timezone     string
	OverrideExOn string
	OverrideOn   bool
}

// getRequestLimitsSnapshot renders the Node requestLimitSnapshot payload.
func (d *Deps) getRequestLimitsSnapshot(w http.ResponseWriter, r *http.Request) {
	systemAccountID, _ := access(r)
	account, err := d.findProfileByID(r.Context(), systemAccountID)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if account == nil {
		kernel.WriteError(w, http.StatusNotFound, "用户不存在")
		return
	}
	snapshot, err := d.requestLimitSnapshot(r.Context(), account)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, snapshot, "")
}

// requestLimitSnapshot mirrors requestLimitSnapshot async.
func (d *Deps) requestLimitSnapshot(ctx context.Context, account *profile) (map[string]any, error) {
	settings, err := d.loadGlobalRequestLimitSettings(ctx)
	if err != nil {
		return nil, err
	}
	overrides := parseUserRequestLimitOverride(&account.RequestLimitsJSON)
	limits := d.resolveEffectiveUserRequestLimits(settings, overrides)
	now := d.clock()
	nowMs := now.UnixMilli()

	// finite: windows whose effective limit is positive are usage-tracked.
	finite := []string{}
	for _, window := range userRequestLimitWindows {
		if effectiveLimitFor(limits, window).Limit > 0 {
			finite = append(finite, window)
		}
	}

	usageStatus := "not_tracked"
	totals := map[string]float64{}
	if len(finite) > 0 {
		estimated := true
		for _, window := range finite {
			bucket := d.requestLimitBucket(window, limits.Timezone, account.ID, nowMs)
			value, ok, err := d.requestLimitTotal(ctx, bucket.redisKey)
			if err != nil || !ok {
				estimated = false
				continue
			}
			totals[window] = value
		}
		if estimated {
			usageStatus = "estimated"
		} else {
			usageStatus = "unavailable"
			totals = map[string]float64{}
		}
	}
	unavailable := usageStatus == "unavailable"

	windows := map[string]any{}
	for _, window := range userRequestLimitWindows {
		effective := effectiveLimitFor(limits, window)
		bucket := d.requestLimitBucket(window, limits.Timezone, account.ID, nowMs)
		unlimited := effective.Limit == 0
		var used, remaining any
		if !unlimited && !unavailable {
			value := totals[window]
			used = value
			remaining = math.Max(0, float64(effective.Limit)-value)
		}
		windows[window] = map[string]any{
			"limit":        effective.Limit,
			"limitMode":    map[bool]string{true: "unlimited", false: "limited"}[unlimited],
			"usageTracked": !unlimited,
			"used":         used,
			"remaining":    remaining,
			"source":       effective.Source,
			"resetsAt":     isoMillis(time.UnixMilli(bucket.resetsAtMs)),
		}
	}
	snapshot := map[string]any{
		"windows":       windows,
		"usageStatus":   usageStatus,
		"asOf":          isoMillis(now),
		"timezone":      limits.Timezone,
		"overrideActive": limits.OverrideOn,
	}
	if limits.OverrideExOn != "" {
		snapshot["overrideExpiresOn"] = limits.OverrideExOn
	}
	return snapshot, nil
}

// loadGlobalRequestLimitSettings mirrors getSettingsAsync() cast to
// GlobalUserRequestLimitSettings for the five consumed keys: missing numeric
// settings fall back to 0 (compatibleSystemSettingDefaults); the timezone
// defaults to UTC.
func (d *Deps) loadGlobalRequestLimitSettings(ctx context.Context) (globalRequestLimitSettings, error) {
	settings := globalRequestLimitSettings{Timezone: "UTC"}
	if d.Settings == nil {
		return settings, nil
	}
	for key, target := range map[string]*int{
		"gatewayUserRequestLimitPerMinute": &settings.PerMinute,
		"gatewayUserRequestLimitPerDay":    &settings.PerDay,
		"gatewayUserRequestLimitPerWeek":   &settings.PerWeek,
		"gatewayUserRequestLimitPerMonth":  &settings.PerMonth,
	} {
		raw, err := d.Settings.SettingValue(key)
		if err != nil {
			return globalRequestLimitSettings{}, err
		}
		if raw == "" {
			continue
		}
		value, err := settingIntValue(raw)
		if err != nil || value < 0 || value > maxUserRequestLimitValue {
			return globalRequestLimitSettings{}, errSettingInvalid(key)
		}
		*target = value
	}
	raw, err := d.Settings.SettingValue("usageStatsTimezone")
	if err != nil {
		return globalRequestLimitSettings{}, err
	}
	if raw != "" {
		var timezone string
		if err := json.Unmarshal([]byte(raw), &timezone); err != nil {
			return globalRequestLimitSettings{}, errSettingInvalid("usageStatsTimezone")
		}
		timezone = strings.TrimSpace(timezone)
		if timezone != "" {
			settings.Timezone = timezone
		}
	}
	return settings, nil
}

type settingInvalidError struct{ key string }

func (e *settingInvalidError) Error() string { return "系统设置 " + e.key + " 无效" }

func errSettingInvalid(key string) error { return &settingInvalidError{key: key} }

func settingIntValue(raw string) (int, error) {
	var value float64
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return 0, err
	}
	return int(value), nil
}

// hasOverrideWindow reports whether the override carries any window value.
func hasOverrideWindow(override *userRequestLimitOverride) bool {
	return override.PerMinute != nil || override.PerDay != nil || override.PerWeek != nil || override.PerMonth != nil
}

func validOverrideExpiresOn(value string) bool {
	if len(value) != len("2006-01-02") {
		return false
	}
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}

// resolveEffectiveUserRequestLimits mirrors resolveEffectiveUserRequestLimits:
// the override applies only while active (expiresOn >= local date in the
// stats timezone).
func (d *Deps) resolveEffectiveUserRequestLimits(settings globalRequestLimitSettings, overrides *userRequestLimitOverride) effectiveRequestLimits {
	active := overrides != nil && hasOverrideWindow(overrides) &&
		(overrides.ExpiresOn == "" || localDateKey(settings.Timezone, d.clock()) <= overrides.ExpiresOn)
	effective := overrides
	if !active {
		effective = nil
	}
	limits := effectiveRequestLimits{
		PerMinute: effectiveLimit(settings.PerMinute, effective, "perMinute"),
		PerDay:    effectiveLimit(settings.PerDay, effective, "perDay"),
		PerWeek:   effectiveLimit(settings.PerWeek, effective, "perWeek"),
		PerMonth:  effectiveLimit(settings.PerMonth, effective, "perMonth"),
		Timezone:  settings.Timezone,
		OverrideOn: active,
	}
	if overrides != nil && overrides.ExpiresOn != "" {
		limits.OverrideExOn = overrides.ExpiresOn
	}
	return limits
}

func effectiveLimit(global int, overrides *userRequestLimitOverride, window string) effectiveRequestLimit {
	var user *int
	if overrides != nil {
		switch window {
		case "perMinute":
			user = overrides.PerMinute
		case "perDay":
			user = overrides.PerDay
		case "perWeek":
			user = overrides.PerWeek
		case "perMonth":
			user = overrides.PerMonth
		}
	}
	if user != nil {
		return effectiveRequestLimit{Limit: *user, Source: "user"}
	}
	return effectiveRequestLimit{Limit: global, Source: "global"}
}

func effectiveLimitFor(limits effectiveRequestLimits, window string) effectiveRequestLimit {
	switch window {
	case "perMinute":
		return limits.PerMinute
	case "perDay":
		return limits.PerDay
	case "perWeek":
		return limits.PerWeek
	}
	return limits.PerMonth
}

// requestLimitBucket mirrors requestLimitBucket: per-window bucket id,
// reset instant and the runtime-state Redis key.
type requestLimitBucket struct {
	window     string
	bucket     string
	resetsAtMs int64
	redisKey   string
}

func (d *Deps) requestLimitBucket(window, timezone, systemAccountID string, nowMs int64) requestLimitBucket {
	parts := dateParts(timezone, nowMs)
	dayEpochMs := time.Date(parts.year, time.Month(parts.month), parts.day, 0, 0, 0, 0, time.UTC).UnixMilli()
	weekday := int(time.UnixMilli(dayEpochMs).UTC().Weekday())
	mondayEpochMs := dayEpochMs - int64((weekday+6)%7)*86_400_000
	minute := nowMs / 60_000
	var bucket string
	switch window {
	case "perMinute":
		bucket = strconv.FormatInt(minute, 10)
	case "perDay":
		bucket = parts.formatDate()
	case "perWeek":
		bucket = time.UnixMilli(mondayEpochMs).UTC().Format("2006-01-02")
	default:
		bucket = parts.formatMonth()
	}
	var resetsAtMs int64
	switch window {
	case "perMinute":
		resetsAtMs = (minute + 1) * 60_000
	case "perDay":
		resetsAtMs = nowMs + 86_400_000
	case "perWeek":
		resetsAtMs = nowMs + 7*86_400_000
	default:
		resetsAtMs = nowMs + 31*86_400_000
	}
	return requestLimitBucket{
		window:     window,
		bucket:     bucket,
		resetsAtMs: resetsAtMs,
		redisKey:   d.redisNamespace() + ":gateway:user-request-limit:" + window + ":" + bucket + ":" + systemAccountID,
	}
}

// redisNamespace mirrors runtimeConfig.redis.namespace for the key prefix;
// unconfigured namespace keeps the default juhe prefix used by the runtime.
func (d *Deps) redisNamespace() string {
	if d.RedisNamespace != "" {
		return d.RedisNamespace
	}
	return "juhe"
}

type datePartsValue struct {
	year, month, day int
}

func (p datePartsValue) formatDate() string {
	return pad2(p.year, 4) + "-" + pad2(p.month, 2) + "-" + pad2(p.day, 2)
}

func (p datePartsValue) formatMonth() string {
	return pad2(p.year, 4) + "-" + pad2(p.month, 2)
}

func pad2(value, width int) string {
	text := strconv.Itoa(value)
	for len(text) < width {
		text = "0" + text
	}
	return text
}

// dateParts mirrors dateParts: calendar date of nowMs in the stats timezone.
func dateParts(timezone string, nowMs int64) datePartsValue {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		location = time.UTC
	}
	parts := time.UnixMilli(nowMs).In(location)
	return datePartsValue{year: parts.Year(), month: int(parts.Month()), day: parts.Day()}
}

// localDateKey mirrors localDateKey (override expiry comparison).
func localDateKey(timezone string, now time.Time) string {
	return dateParts(timezone, now.UnixMilli()).formatDate()
}

// requestLimitTotal reads one bucket total through the UsageReader port
// (Node redis pipeline HGET <key> __total with a 750ms deadline). ok=false
// mirrors the unavailable degrade.
func (d *Deps) requestLimitTotal(ctx context.Context, key string) (value float64, ok bool, err error) {
	if d.Usage == nil || d.RedisNamespace == "" {
		return 0, false, nil
	}
	ctx, cancel := context.WithTimeout(ensureDelegatedCtx(ctx), redisReadDeadlineMs*time.Millisecond)
	defer cancel()
	raw, err := d.Usage.RequestLimitTotal(ctx, key)
	if err != nil {
		return 0, false, err
	}
	parsed, parseErr := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if parseErr != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
		// nonNegativeInteger: invalid renders as 0 but still counts as data.
		return 0, true, nil
	}
	return math.Floor(parsed), true, nil
}
