package accountquality

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// QuotaRecoveryMode 与 Node ApiKeyQuotaRecoveryMode 一致。
type QuotaRecoveryMode string

const (
	QuotaRecoveryGeneric       QuotaRecoveryMode = "generic"
	QuotaRecoveryExplicitReset QuotaRecoveryMode = "explicit_reset"
)

// QuotaRecoveryHintSource 与 Node ApiKeyQuotaRecoveryHintSource 一致。
type QuotaRecoveryHintSource string

const (
	HintSourceResetAt        QuotaRecoveryHintSource = "reset_at"
	HintSourceRetryAfter     QuotaRecoveryHintSource = "retry_after"
	HintSourceProviderHeader QuotaRecoveryHintSource = "provider_header"
)

// QuotaRecoveryHint 等价 Node ApiKeyQuotaRecoveryHint。
type QuotaRecoveryHint struct {
	Mode          QuotaRecoveryMode
	CooldownUntil string
	Source        QuotaRecoveryHintSource
}

// ---------------------------------------------------------------------------
// 系统额度不足规则（account-error-policy-system-rules.ts 的代码内注册表）

var insufficientQuotaStableCodes = map[string]struct{}{}
var insufficientQuotaTextMarkers = []string{
	"余额不足",
	"额度不足",
	"insufficient balance",
	"insufficient quota",
	"subscription quota insufficient",
	"credit balance too low",
	"wallet balance exhausted",
}
var nonQuota403ErrorIdentifiers = map[string]struct{}{
	"content_policy_violation": {},
	"content_policy_blocked":   {},
	"prompt_guard_blocked":     {},
	"client_restricted":        {},
	"permission_denied":        {},
	"access_denied":            {},
	"forbidden":                {},
}

func init() {
	for _, code := range []string{
		"insufficient_user_quota",
		"insufficient_quota",
		"insufficient_balance",
		"quota_exceeded",
		"quota_exhausted",
		"default_group_global_quota_exhausted",
		"billing_hard_limit_reached",
		"wallet_balance_exhausted",
		"pre_consume_token_quota_failed",
	} {
		insufficientQuotaStableCodes[normalizeErrorIdentifier(code)] = struct{}{}
	}
}

func normalizeErrorIdentifier(value string) string {
	// Node: (value ?? '').trim().toLowerCase().replace(/[\s-]+/g, '_')
	trimmed := strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	prevSep := false
	for _, r := range trimmed {
		if r == ' ' || r == '-' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' {
			if !prevSep {
				b.WriteByte('_')
			}
			prevSep = true
			continue
		}
		prevSep = false
		b.WriteRune(r)
	}
	return b.String()
}

// SystemInsufficientQuotaRuleMatches 是 systemInsufficientQuotaRuleMatches 的
// 逐分支移植（HTTP 402/403 的稳定 code / quota code / 文本标记匹配，以及
// 非 quota 403 标识排除）。
func SystemInsufficientQuotaRuleMatches(statusCode int, errorCode, errorType, searchableText string) bool {
	if statusCode != 402 && statusCode != 403 {
		return false
	}
	code := normalizeErrorIdentifier(errorCode)
	typ := normalizeErrorIdentifier(errorType)
	if _, ok := insufficientQuotaStableCodes[code]; ok {
		return true
	}
	if _, ok := insufficientQuotaStableCodes[typ]; ok {
		return true
	}
	if strings.Contains(code, "quota") || strings.Contains(typ, "quota") {
		return true
	}
	if _, ok := nonQuota403ErrorIdentifiers[code]; ok {
		return false
	}
	if _, ok := nonQuota403ErrorIdentifiers[typ]; ok {
		return false
	}
	if statusCode == 402 && code == "" && typ == "" {
		return true
	}
	text := strings.ToLower(searchableText)
	for _, identifier := range nonQuota403ErrorIdentifiersRaw {
		if strings.Contains(text, strings.ReplaceAll(identifier, "_", " ")) || strings.Contains(text, identifier) {
			return false
		}
	}
	for _, marker := range insufficientQuotaTextMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

var nonQuota403ErrorIdentifiersRaw = []string{
	"content_policy_violation",
	"content_policy_blocked",
	"prompt_guard_blocked",
	"client_restricted",
	"permission_denied",
	"access_denied",
	"forbidden",
}

// ---------------------------------------------------------------------------
// 恢复模式与错误码

// APIKeyQuotaRecoveryModeFromErrorCode 是 apiKeyQuotaRecoveryModeFromErrorCode 的移植。
func APIKeyQuotaRecoveryModeFromErrorCode(value string) (QuotaRecoveryMode, bool) {
	switch value {
	case QuotaRecoveryGenericErrorCode:
		return QuotaRecoveryGeneric, true
	case QuotaRecoveryExplicitErrorCode:
		return QuotaRecoveryExplicitReset, true
	default:
		return "", false
	}
}

// QuotaRecoveryErrorCode 是 quotaRecoveryErrorCode 的移植。
func QuotaRecoveryErrorCode(mode QuotaRecoveryMode) string {
	if mode == QuotaRecoveryExplicitReset {
		return QuotaRecoveryExplicitErrorCode
	}
	return QuotaRecoveryGenericErrorCode
}

// APIKeyQuotaObservationExceeded 是 apiKeyQuotaObservationExceeded 的移植：
// recoveryStartedAt 缺失或不可解析时恒为 false。
func APIKeyQuotaObservationExceeded(recoveryStartedAt string, observedAt time.Time) bool {
	if strings.TrimSpace(recoveryStartedAt) == "" {
		return false
	}
	startedAt, err := time.Parse(time.RFC3339, recoveryStartedAt)
	if err != nil {
		return false
	}
	return observedAt.Sub(startedAt) >= APIKeyQuotaObservationTimeout
}

// ---------------------------------------------------------------------------
// 恢复 hint 提取（api-key-quota-recovery.ts extractApiKeyQuotaRecoveryHint）

// ExtractQuotaRecoveryHint 从上游响应体/响应头提取显式恢复时间。
func ExtractQuotaRecoveryHint(bodyText string, headers map[string]string, now time.Time) *QuotaRecoveryHint {
	bodyValue := parseJSONValue(bodyText)
	absolute := findFirstField(bodyValue, []string{"reset_at", "resetAt", "quota_reset_at", "quotaResetAt"})
	if absoluteAt := parseAbsoluteRecoveryTime(absolute); absoluteAt != nil && absoluteAt.After(now) {
		return &QuotaRecoveryHint{
			Mode:          QuotaRecoveryExplicitReset,
			CooldownUntil: absoluteAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
			Source:        HintSourceResetAt,
		}
	}
	delaySeconds := parsePositiveSeconds(findFirstField(bodyValue, []string{
		"reset_after_seconds",
		"resetAfterSeconds",
		"retry_after_seconds",
		"retryAfterSeconds",
	}))
	if delaySeconds != nil {
		cooldownUntil := now.Add(time.Duration(*delaySeconds) * time.Second)
		return &QuotaRecoveryHint{
			Mode:          QuotaRecoveryExplicitReset,
			CooldownUntil: cooldownUntil.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
			Source:        HintSourceResetAt,
		}
	}
	if headers != nil {
		retryAfter := parseRetryAfter(getHeader(headers, "retry-after"), now)
		if retryAfter != nil {
			return &QuotaRecoveryHint{
				Mode:          QuotaRecoveryExplicitReset,
				CooldownUntil: retryAfter.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
				Source:        HintSourceRetryAfter,
			}
		}
		providerReset := parseProviderResetHeader(
			firstHeader(headers, "x-quota-reset-at", "x-ratelimit-reset", "x-rate-limit-reset"),
			now,
		)
		if providerReset != nil {
			return &QuotaRecoveryHint{
				Mode:          QuotaRecoveryExplicitReset,
				CooldownUntil: providerReset.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
				Source:        HintSourceProviderHeader,
			}
		}
	}
	return nil
}

func getHeader(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func firstHeader(headers map[string]string, names ...string) string {
	for _, name := range names {
		if value := getHeader(headers, name); value != "" {
			return value
		}
	}
	return ""
}

func parseJSONValue(text string) any {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil
	}
	return parsed
}

func findFirstField(value any, names []string) any {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	for _, name := range names {
		if child, present := obj[name]; present {
			return child
		}
	}
	// Node Object.values 顺序与 encoding/json 的 map 遍历均为插入序不可保证，
	// 但字段搜索是存在性语义：递归全部子对象即可。
	for _, child := range obj {
		if found := findFirstField(child, names); found != nil {
			return found
		}
	}
	return nil
}

func parseAbsoluteRecoveryTime(value any) *time.Time {
	var milliseconds float64
	switch v := value.(type) {
	case float64:
		milliseconds = v
		if milliseconds > 0 {
			if milliseconds > 10_000_000_000 {
				// 已是毫秒。
			} else {
				milliseconds *= 1000
			}
			t := msToTime(milliseconds)
			return &t
		}
		return nil
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil
		}
		if matched, err := strconv.ParseFloat(trimmed, 64); err == nil && matched > 0 {
			if matched <= 10_000_000_000 {
				matched *= 1000
			}
			t := msToTime(matched)
			return &t
		}
		normalized, ok := canonicalizeRFC3339(trimmed)
		if !ok {
			return nil
		}
		t, err := time.Parse(time.RFC3339, normalized)
		if err != nil {
			return nil
		}
		return &t
	default:
		return nil
	}
}

func msToTime(ms float64) time.Time {
	sec := int64(ms / 1000)
	nsec := int64((ms - float64(sec)*1000) * 1e6)
	return time.Unix(sec, nsec)
}

// canonicalizeRFC3339 等价 Node canonicalizeRfc3339Instant 的宽松入口：
// 只接受可被 time.RFC3339 解析的文本。
func canonicalizeRFC3339(value string) (string, bool) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", false
	}
	return t.Format(time.RFC3339), true
}

func parsePositiveSeconds(value any) *int64 {
	switch v := value.(type) {
	case float64:
		if v > 0 {
			seconds := int64(math.Ceil(v))
			return &seconds
		}
		return nil
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil || parsed <= 0 {
			return nil
		}
		seconds := int64(math.Ceil(parsed))
		return &seconds
	default:
		return nil
	}
}

func parseRetryAfter(value string, now time.Time) *time.Time {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if seconds := parsePositiveSeconds(value); seconds != nil {
		t := now.Add(time.Duration(*seconds) * time.Second)
		return &t
	}
	if absolute := parseAbsoluteRecoveryTime(value); absolute != nil && absolute.After(now) {
		return absolute
	}
	if httpDate, err := time.Parse(time.RFC1123, value); err == nil && httpDate.After(now) {
		return &httpDate
	}
	if ms, err := strconv.ParseInt(value, 10, 64); err == nil {
		t := msToTime(float64(ms))
		if t.After(now) {
			return &t
		}
	}
	return nil
}

func parseProviderResetHeader(value string, now time.Time) *time.Time {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if absolute := parseAbsoluteRecoveryTime(value); absolute != nil && absolute.After(now) {
		return absolute
	}
	return nil
}

// ---------------------------------------------------------------------------
// 额度恢复策略与通用冷却时间（quota-recovery-policy.ts）

// QuotaRecoveryStrategy 与 Node 一致。
type QuotaRecoveryStrategy string

const (
	StrategyDuration QuotaRecoveryStrategy = "duration"
	StrategyDaily    QuotaRecoveryStrategy = "daily"
	StrategyWeekly   QuotaRecoveryStrategy = "weekly"
)

// QuotaRecoverySchedule 等价 Node QuotaRecoverySchedule。
type QuotaRecoverySchedule struct {
	ResetStrategy   QuotaRecoveryStrategy
	DurationMinutes *int
	DailyResetHour  *int
	WeeklyResetDay  *int
	WeeklyResetHour *int
	Timezone        string
	JitterMinutes   int
}

// QuotaRecoveryPolicy 等价 Node QuotaRecoveryPolicy。
type QuotaRecoveryPolicy struct {
	APIKey      *QuotaRecoverySchedule
	OAuth       *QuotaRecoverySchedule
	GoogleOAuth *QuotaRecoverySchedule
}

const fixedJitterMinutes = 15
const maxDurationMinutes = 7 * 24 * 60

// DefaultAPIKeyQuotaRecoverySchedule 与 Node DEFAULT_API_KEY_QUOTA_RECOVERY_SCHEDULE 一致。
func DefaultAPIKeyQuotaRecoverySchedule() QuotaRecoverySchedule {
	duration := 60
	return QuotaRecoverySchedule{
		ResetStrategy:   StrategyDuration,
		DurationMinutes: &duration,
		JitterMinutes:   fixedJitterMinutes,
		Timezone:        "UTC",
	}
}

// NormalizeQuotaRecoveryPolicy 是 normalizeQuotaRecoveryPolicy 的移植：
// 仅接受 api_key/oauth/google_oauth 三个键，策略项按 Node 规则校验。
func NormalizeQuotaRecoveryPolicy(value map[string]any) (QuotaRecoveryPolicy, error) {
	var out QuotaRecoveryPolicy
	for key, raw := range value {
		item, ok := raw.(map[string]any)
		if !ok {
			return out, fmt.Errorf("额度恢复策略项必须是对象")
		}
		schedule, err := normalizeQuotaRecoverySchedule(item)
		if err != nil {
			return out, err
		}
		switch key {
		case "api_key":
			out.APIKey = &schedule
		case "oauth":
			out.OAuth = &schedule
		case "google_oauth":
			out.GoogleOAuth = &schedule
		default:
			return out, fmt.Errorf("额度恢复策略字段 %s 不受支持", key)
		}
	}
	return out, nil
}

func normalizeQuotaRecoverySchedule(value map[string]any) (QuotaRecoverySchedule, error) {
	strategyRaw, _ := value["reset_strategy"].(string)
	strategy := QuotaRecoveryStrategy(strategyRaw)
	switch strategy {
	case StrategyDuration, StrategyDaily, StrategyWeekly:
	default:
		return QuotaRecoverySchedule{}, fmt.Errorf("额度恢复策略 reset_strategy 必须是 duration、daily 或 weekly")
	}
	out := QuotaRecoverySchedule{ResetStrategy: strategy}
	var err error
	switch strategy {
	case StrategyDuration:
		if out.DurationMinutes, err = integerInRange(value["duration_minutes"], 30, maxDurationMinutes, "duration_minutes"); err != nil {
			return out, err
		}
	case StrategyDaily:
		if out.DailyResetHour, err = integerInRange(value["daily_reset_hour"], 0, 23, "daily_reset_hour"); err != nil {
			return out, err
		}
	case StrategyWeekly:
		if out.WeeklyResetDay, err = integerInRange(value["weekly_reset_day"], 0, 6, "weekly_reset_day"); err != nil {
			return out, err
		}
		if out.WeeklyResetHour, err = integerInRange(value["weekly_reset_hour"], 0, 23, "weekly_reset_hour"); err != nil {
			return out, err
		}
	}
	if raw, present := value["jitter_minutes"]; present {
		jitter, _ := raw.(float64)
		if int(jitter) != fixedJitterMinutes {
			return out, fmt.Errorf("额度恢复策略 jitter_minutes固定15，仅作为兼容字段")
		}
	}
	out.JitterMinutes = fixedJitterMinutes
	timezone := "UTC"
	if raw, present := value["timezone"]; present {
		text, ok := raw.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return out, fmt.Errorf("额度恢复策略 timezone 无效")
		}
		timezone = text
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return out, fmt.Errorf("额度恢复策略 timezone 无效：%s", timezone)
	}
	out.Timezone = strings.TrimSpace(timezone)
	return out, nil
}

func integerInRange(value any, min, max int, label string) (*int, error) {
	number, ok := value.(float64)
	if !ok || number != math.Trunc(number) || int(number) < min || int(number) > max {
		return nil, fmt.Errorf("额度恢复策略 %s 必须是 %d-%d 的整数", label, min, max)
	}
	out := int(number)
	return &out, nil
}

// QuotaRecoveryScheduleForAccount 是 quotaRecoveryScheduleForAccount 的移植。
func QuotaRecoveryScheduleForAccount(policy *QuotaRecoveryPolicy, accountType string) QuotaRecoverySchedule {
	var configured *QuotaRecoverySchedule
	fallback := DefaultOAuthQuotaRecoverySchedule()
	if accountType == "api_key" {
		fallback = DefaultAPIKeyQuotaRecoverySchedule()
	}
	switch accountType {
	case "api_key":
		if policy != nil {
			configured = policy.APIKey
		}
	case "google_oauth":
		if policy != nil {
			configured = policy.GoogleOAuth
		}
	default:
		if policy != nil {
			configured = policy.OAuth
		}
	}
	merged := fallback
	if configured != nil {
		if configured.DurationMinutes != nil {
			merged.DurationMinutes = configured.DurationMinutes
		}
		if configured.DailyResetHour != nil {
			merged.DailyResetHour = configured.DailyResetHour
		}
		if configured.WeeklyResetDay != nil {
			merged.WeeklyResetDay = configured.WeeklyResetDay
		}
		if configured.WeeklyResetHour != nil {
			merged.WeeklyResetHour = configured.WeeklyResetHour
		}
		if configured.Timezone != "" {
			merged.Timezone = configured.Timezone
		}
		merged.ResetStrategy = configured.ResetStrategy
		merged.JitterMinutes = fixedJitterMinutes
	}
	return merged
}

// DefaultOAuthQuotaRecoverySchedule 与 Node DEFAULT_OAUTH_QUOTA_RECOVERY_SCHEDULE 一致。
func DefaultOAuthQuotaRecoverySchedule() QuotaRecoverySchedule {
	hour := 0
	return QuotaRecoverySchedule{
		ResetStrategy:  StrategyDaily,
		DailyResetHour: &hour,
		JitterMinutes:  fixedJitterMinutes,
		Timezone:       "UTC",
	}
}

// QuotaRecoveryCooldownUntil 是 quotaRecoveryCooldownUntil 的移植：
// 策略边界 + 稳定确定性偏移（被动调度窗口）。
func QuotaRecoveryCooldownUntil(policy *QuotaRecoveryPolicy, accountType, seed string, now time.Time) string {
	schedule := QuotaRecoveryScheduleForAccount(policy, accountType)
	boundary := scheduleBoundary(schedule, now)
	intervalMs := boundary.Sub(now).Milliseconds()
	if intervalMs < 1 {
		intervalMs = 1
	}
	offset := DeterministicOffsetMs(intervalMs, seed)
	until := boundary.Add(time.Duration(offset) * time.Millisecond)
	return until.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func scheduleBoundary(schedule QuotaRecoverySchedule, now time.Time) time.Time {
	if schedule.ResetStrategy == StrategyDuration {
		minutes := 60
		if schedule.DurationMinutes != nil {
			minutes = *schedule.DurationMinutes
		}
		return now.Add(time.Duration(minutes) * time.Minute)
	}
	timezone := schedule.Timezone
	if timezone == "" {
		timezone = "UTC"
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)
	targetHour := 0
	if schedule.ResetStrategy == StrategyWeekly {
		if schedule.WeeklyResetHour != nil {
			targetHour = *schedule.WeeklyResetHour
		}
	} else if schedule.DailyResetHour != nil {
		targetHour = *schedule.DailyResetHour
	}
	dayDelta := 0
	if schedule.ResetStrategy == StrategyWeekly {
		weekday := int(local.Weekday()) // Go: Sunday=0，与 Node 'Sun'=0 一致
		resetDay := 0
		if schedule.WeeklyResetDay != nil {
			resetDay = *schedule.WeeklyResetDay
		}
		dayDelta = ((resetDay-weekday)%7 + 7) % 7
	}
	candidate := time.Date(local.Year(), local.Month(), local.Day()+dayDelta, targetHour, 0, 0, 0, loc)
	if !candidate.After(now) {
		step := 1
		if schedule.ResetStrategy == StrategyWeekly {
			step = 7
		}
		candidate = time.Date(local.Year(), local.Month(), local.Day()+dayDelta+step, targetHour, 0, 0, 0, loc)
	}
	return candidate
}

// DeterministicOffsetMs 是 passiveScheduleDeterministicOffsetMs 的移植：
// FNV-1a（UTF-16 码元）哈希映射到对称窗口，0 改为 1。
func DeterministicOffsetMs(intervalMs int64, seed string) int64 {
	windowMs := JitterWindowMs(intervalMs)
	if windowMs <= 0 {
		return 0
	}
	var hash uint32 = 2166136261
	for _, unit := range utf16CodeUnits(seed) {
		hash ^= uint32(unit)
		hash *= 16777619
	}
	span := uint64(windowMs)*2 + 1
	offset := int64(uint64(hash)%span) - windowMs
	if offset == 0 {
		return 1
	}
	return offset
}

// utf16CodeUnits 与 JS String.prototype.charCodeAt 的码元序列一致。
func utf16CodeUnits(seed string) []uint16 {
	units := make([]uint16, 0, len(seed))
	for _, r := range seed {
		if r > 0xFFFF {
			r -= 0x10000
			units = append(units, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
		} else {
			units = append(units, uint16(r))
		}
	}
	return units
}

// JitterWindowMs 是 passiveScheduleJitterWindowMs 的移植（与
// internal/schedulejitter.Window 的毫秒语义一致，返回毫秒数值）。
func JitterWindowMs(intervalMs int64) int64 {
	interval := intervalMs
	if interval < 1 {
		interval = 1
	}
	var window int64
	switch {
	case interval < 60_000:
		window = 30_000
		if interval/2 < window {
			window = interval / 2
		}
	case interval < 3_600_000:
		window = 30_000
	case interval < 86_400_000:
		window = 1_800_000
	case interval < 7*86_400_000:
		window = 3_600_000
	default:
		window = 8 * 3_600_000
	}
	if maximum := interval / 2; window > maximum {
		window = maximum
	}
	return window
}

// GenericAPIKeyQuotaCooldownUntil 是 genericApiKeyQuotaCooldownUntil 的移植。
func GenericAPIKeyQuotaCooldownUntil(policy *QuotaRecoveryPolicy, seed string, now time.Time) string {
	if seed == "" {
		seed = "system:api_key:generic"
	}
	return QuotaRecoveryCooldownUntil(policy, "api_key", seed, now)
}
