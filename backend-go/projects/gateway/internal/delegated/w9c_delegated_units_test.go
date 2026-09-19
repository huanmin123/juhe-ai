package delegated

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// apikeypatch.go pure helpers.
// ---------------------------------------------------------------------------

func TestW9CJSONTypeName(t *testing.T) {
	type unknown struct{}
	cases := []struct {
		raw  any
		want string
	}{
		{nil, "null"},
		{true, "boolean"},
		{float64(3), "number"},
		{"s", "string"},
		{[]any{"a"}, "array"},
		{map[string]any{}, "object"},
		{unknown{}, "unknown"},
	}
	for _, tc := range cases {
		if got := jsonTypeName(tc.raw); got != tc.want {
			t.Fatalf("jsonTypeName(%#v) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	if got := zodTypeError(1.5); got != "Expected string, received number" {
		t.Fatalf("zodTypeError = %q", got)
	}
}

func TestW9CParseApiKeyPatchArms(t *testing.T) {
	// Non-string expectedRevision.
	if _, ok, issue := parseApiKeyPatch(map[string]any{"expectedRevision": float64(7)}); ok || issue != "Expected string, received number" {
		t.Fatalf("number revision issue = %q ok=%v", issue, ok)
	}
	// status null / non-string / invalid enum.
	if _, ok, issue := parseApiKeyPatch(map[string]any{"expectedRevision": "r", "status": nil}); ok || issue != "Expected 'active' | 'disabled', received null" {
		t.Fatalf("null status issue = %q", issue)
	}
	if _, ok, issue := parseApiKeyPatch(map[string]any{"expectedRevision": "r", "status": float64(1)}); ok || issue != "Expected 'active' | 'disabled', received number" {
		t.Fatalf("number status issue = %q", issue)
	}
	if _, ok, issue := parseApiKeyPatch(map[string]any{"expectedRevision": "r", "status": "paused"}); ok || issue != "Invalid enum value. Expected 'active' | 'disabled', received 'paused'" {
		t.Fatalf("bad enum issue = %q", issue)
	}
	// name non-string and blank.
	if _, ok, issue := parseApiKeyPatch(map[string]any{"expectedRevision": "r", "name": []any{}}); ok || issue != "Expected string, received array" {
		t.Fatalf("array name issue = %q", issue)
	}
	if _, ok, issue := parseApiKeyPatch(map[string]any{"expectedRevision": "r", "name": "   "}); ok || issue != zodBlank {
		t.Fatalf("blank name issue = %q", issue)
	}
	// routeStrategyId non-string / blank / trim.
	if _, ok, issue := parseApiKeyPatch(map[string]any{"expectedRevision": "r", "routeStrategyId": true}); ok || issue != "Expected string, received boolean" {
		t.Fatalf("bool strategy issue = %q", issue)
	}
	if _, ok, issue := parseApiKeyPatch(map[string]any{"expectedRevision": "r", "routeStrategyId": " "}); ok || issue != zodBlank {
		t.Fatalf("blank strategy issue = %q", issue)
	}
	// Unknown keys render the strict-object copy and block the patch.
	_, ok, issue := parseApiKeyPatch(map[string]any{
		"expectedRevision": " r ", "name": "n", "extra1": 1, "extra2": 2,
	})
	if ok || !strings.HasPrefix(issue, "Unrecognized key(s) in object: ") {
		t.Fatalf("unknown keys must block: ok=%v issue=%q", ok, issue)
	}
	input, ok, issue := parseApiKeyPatch(map[string]any{
		"expectedRevision": " r ", "name": " n ",
	})
	if !input.HasName || input.Name != "n" || input.ExpectedRevision != "r" {
		t.Fatalf("input = %+v", input)
	}
	// Status-only and strategy-only inputs parse.
	input, ok, _ = parseApiKeyPatch(map[string]any{"expectedRevision": "r", "status": "active"})
	if !ok || !input.HasStatus || input.Status != "active" {
		t.Fatalf("status-only input = %+v ok=%v", input, ok)
	}
	input, ok, _ = parseApiKeyPatch(map[string]any{"expectedRevision": "r", "routeStrategyId": "rst-9"})
	if !ok || !input.HasRouteStrategyID || input.RouteStrategyID != "rst-9" {
		t.Fatalf("strategy-only input = %+v ok=%v", input, ok)
	}
}

func TestW9COptionalTrimmedString(t *testing.T) {
	if _, present, issue := optionalTrimmedString(float64(2)); present || issue != "Expected string, received number" {
		t.Fatalf("non-string = %q", issue)
	}
	if _, present, issue := optionalTrimmedString("  "); present || issue != zodBlank {
		t.Fatalf("blank = %q", issue)
	}
	text, present, issue := optionalTrimmedString(" rst-1 ")
	if !present || issue != "" || text != "rst-1" {
		t.Fatalf("trimmed = %q present=%v issue=%q", text, present, issue)
	}
}

func TestW9CHourlyQuotaWindowHoursArms(t *testing.T) {
	if _, ok := hourlyQuotaWindowHours(sql.NullString{}); ok {
		t.Fatal("invalid null string must not yield a window")
	}
	if _, ok := hourlyQuotaWindowHours(sql.NullString{String: "", Valid: true}); ok {
		t.Fatal("empty json must not yield a window")
	}
	if _, ok := hourlyQuotaWindowHours(sql.NullString{String: "{invalid", Valid: true}); ok {
		t.Fatal("malformed json must not yield a window")
	}
	if _, ok := hourlyQuotaWindowHours(sql.NullString{String: `{}`, Valid: true}); ok {
		t.Fatal("missing hourly must not yield a window")
	}
	if _, ok := hourlyQuotaWindowHours(sql.NullString{String: `{"hourly":{"enabled":false,"hours":5}}`, Valid: true}); ok {
		t.Fatal("disabled hourly must not yield a window")
	}
	if _, ok := hourlyQuotaWindowHours(sql.NullString{String: `{"hourly":{"enabled":true}}`, Valid: true}); ok {
		t.Fatal("missing hours must not yield a window")
	}
	if _, ok := hourlyQuotaWindowHours(sql.NullString{String: `{"hourly":{"enabled":true,"hours":0}}`, Valid: true}); ok {
		t.Fatal("zero hours must not yield a window")
	}
	if _, ok := hourlyQuotaWindowHours(sql.NullString{String: `{"hourly":{"enabled":true,"hours":1000}}`, Valid: true}); ok {
		t.Fatal("hours beyond the 720 cap must not yield a window")
	}
	hours, ok := hourlyQuotaWindowHours(sql.NullString{String: `{"hourly":{"enabled":true,"hours":5}}`, Valid: true})
	if !ok || hours != 5 {
		t.Fatalf("valid hours = %d ok=%v", hours, ok)
	}
}

func TestW9CIsDuplicateKeyNameError(t *testing.T) {
	if isDuplicateKeyNameError(nil) {
		t.Fatal("nil error is not a duplicate")
	}
	for _, message := range []string{
		"UNIQUE constraint failed: api_keys.system_account_id",
		"duplicate key value violates unique constraint api_keys_name",
		"SQLSTATE 23505",
	} {
		if !isDuplicateKeyNameError(errors.New(message)) {
			t.Fatalf("%q must be detected as duplicate", message)
		}
	}
	if isDuplicateKeyNameError(errors.New("boom")) {
		t.Fatal("generic error is not a duplicate")
	}
}

func TestW9CNextApiKeyRevision(t *testing.T) {
	if _, err := nextApiKeyRevision("not-a-time", time.Now()); err == nil {
		t.Fatal("malformed revision must fail")
	}
	// Now wins.
	now := time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)
	revision, err := nextApiKeyRevision("2026-01-10T08:30:00Z", now)
	if err != nil || revision != "2026-01-10T09:00:00.000000Z" {
		t.Fatalf("revision = %q err=%v", revision, err)
	}
	// Monotonic floor: previous + 1ms.
	revision, err = nextApiKeyRevision("2030-01-01T00:00:00Z", now)
	if err != nil || revision != "2030-01-01T00:00:00.001000Z" {
		t.Fatalf("monotonic revision = %q err=%v", revision, err)
	}
	if revisionFromMillis(0) != "1970-01-01T00:00:00.000000Z" {
		t.Fatalf("revisionFromMillis(0) = %q", revisionFromMillis(0))
	}
}

// ---------------------------------------------------------------------------
// requestlimits.go pure helpers.
// ---------------------------------------------------------------------------

func TestW9CParseUserRequestLimitOverride(t *testing.T) {
	if parseUserRequestLimitOverride(nil) != nil {
		t.Fatal("nil raw must yield nil")
	}
	if parseUserRequestLimitOverride(&sql.NullString{}) != nil {
		t.Fatal("invalid raw must yield nil")
	}
	if parseUserRequestLimitOverride(&sql.NullString{String: "", Valid: true}) != nil {
		t.Fatal("empty raw must yield nil")
	}
	if parseUserRequestLimitOverride(&sql.NullString{String: "{bad", Valid: true}) != nil {
		t.Fatal("malformed json must yield nil")
	}
	if parseUserRequestLimitOverride(&sql.NullString{String: `{"perMinute":-1}`, Valid: true}) != nil {
		t.Fatal("negative window must yield nil")
	}
	if parseUserRequestLimitOverride(&sql.NullString{String: `{"perMinute":2000000000}`, Valid: true}) != nil {
		t.Fatal("window beyond the cap must yield nil")
	}
	if parseUserRequestLimitOverride(&sql.NullString{String: `{"expiresOn":"2026-01-01"}`, Valid: true}) != nil {
		t.Fatal("expiry without windows must yield nil")
	}
	if parseUserRequestLimitOverride(&sql.NullString{String: `{"perDay":5,"expiresOn":"01/2026"}`, Valid: true}) != nil {
		t.Fatal("malformed expiry must yield nil")
	}
	override := parseUserRequestLimitOverride(&sql.NullString{String: `{"perDay":5,"expiresOn":"2026-01-01"}`, Valid: true})
	if override == nil || override.PerDay == nil || *override.PerDay != 5 || override.ExpiresOn != "2026-01-01" {
		t.Fatalf("override = %+v", override)
	}
}

func TestW9CValidOverrideExpiresOn(t *testing.T) {
	for _, bad := range []string{"", "2026-1-1", "2026/01/01", "2026-01-01T00:00:00Z", "abcdefgh"} {
		if validOverrideExpiresOn(bad) {
			t.Fatalf("%q must be invalid", bad)
		}
	}
	if !validOverrideExpiresOn("2026-01-31") {
		t.Fatal("2026-01-31 must be valid")
	}
	if !validOverrideExpiresOn("2028-02-29") {
		t.Fatal("2028-02-29 must be valid (leap year)")
	}
}

func TestW9CSettingIntValueAndError(t *testing.T) {
	value, err := settingIntValue("42")
	if err != nil || value != 42 {
		t.Fatalf("settingIntValue = %d, %v", value, err)
	}
	if _, err := settingIntValue("abc"); err == nil {
		t.Fatal("non-numeric setting must fail")
	}
	if got := errSettingInvalid("perDay").Error(); got != "系统设置 perDay 无效" {
		t.Fatalf("errSettingInvalid = %q", got)
	}
}

func TestW9CResolveEffectiveUserRequestLimits(t *testing.T) {
	env := newEnv(t)
	deps := env.deps
	base := globalRequestLimitSettings{PerMinute: 10, PerDay: 100, PerWeek: 1000, PerMonth: 10000, Timezone: "UTC"}

	// No override: everything global.
	limits := deps.resolveEffectiveUserRequestLimits(base, nil)
	if limits.PerMinute.Limit != 10 || limits.PerMinute.Source != "global" || limits.OverrideOn {
		t.Fatalf("global limits = %+v", limits)
	}
	// Active override.
	day := 5
	limits = deps.resolveEffectiveUserRequestLimits(base, &userRequestLimitOverride{PerDay: &day})
	if !limits.OverrideOn || limits.PerDay.Limit != 5 || limits.PerDay.Source != "user" {
		t.Fatalf("override limits = %+v", limits)
	}
	if limits.PerMinute.Source != "global" {
		t.Fatalf("unoverridden window must stay global: %+v", limits.PerMinute)
	}
	// Expired override stays inactive but reports the expiry.
	limits = deps.resolveEffectiveUserRequestLimits(base, &userRequestLimitOverride{PerDay: &day, ExpiresOn: "2000-01-01"})
	if limits.OverrideOn || limits.OverrideExOn != "2000-01-01" {
		t.Fatalf("expired override = %+v", limits)
	}
	// Future expiry keeps the override active.
	future := deps.clock().AddDate(1, 0, 0).Format("2006-01-02")
	limits = deps.resolveEffectiveUserRequestLimits(base, &userRequestLimitOverride{PerDay: &day, ExpiresOn: future})
	if !limits.OverrideOn || limits.OverrideExOn != future {
		t.Fatalf("future override = %+v", limits)
	}
}

func TestW9CRequestLimitBucketAndTotals(t *testing.T) {
	env := newEnv(t)
	deps := env.deps
	nowMs := time.Date(2026, 1, 10, 8, 30, 0, 0, time.UTC).UnixMilli() // Saturday
	bucket := deps.requestLimitBucket("perMinute", "UTC", "acc-1", nowMs)
	if bucket.bucket != "29467230" || bucket.resetsAtMs != (29467230+1)*60_000 {
		t.Fatalf("minute bucket = %+v", bucket)
	}
	day := deps.requestLimitBucket("perDay", "UTC", "acc-1", nowMs)
	if day.bucket != "2026-01-10" {
		t.Fatalf("day bucket = %+v", day)
	}
	week := deps.requestLimitBucket("perWeek", "UTC", "acc-1", nowMs)
	if week.bucket != "2026-01-05" { // Monday of that week
		t.Fatalf("week bucket = %+v", week)
	}
	month := deps.requestLimitBucket("perMonth", "UTC", "acc-1", nowMs)
	if month.bucket != "2026-01" || month.resetsAtMs != nowMs+31*86_400_000 {
		t.Fatalf("month bucket = %+v", month)
	}
	if deps.redisNamespace() != "juhe" {
		t.Fatalf("default namespace = %q", deps.redisNamespace())
	}

	// Usage reader unconfigured: totals degrade without error.
	if value, ok, err := deps.requestLimitTotal(context.Background(), "k"); err != nil || ok || value != 0 {
		t.Fatalf("unconfigured usage = %v %v %v", value, ok, err)
	}
}

// ---------------------------------------------------------------------------
// profiles / routes helpers.
// ---------------------------------------------------------------------------

func TestW9CProfileDisplayNameError(t *testing.T) {
	for _, value := range []string{"", "  "} {
		if err := profileDisplayNameError(value); err == nil {
			t.Fatalf("blank %q must fail", value)
		}
	}
	if err := profileDisplayNameError("has space"); err == nil || err.Error() != "用户名称不能包含空格" {
		t.Fatalf("whitespace err = %v", err)
	}
	if err := profileDisplayNameError("ok-name"); err != nil {
		t.Fatalf("valid name err = %v", err)
	}
}

func TestW9CEnsureDelegatedCtx(t *testing.T) {
	if ensureDelegatedCtx(nil) == nil {
		t.Fatal("nil ctx must be replaced with Background")
	}
	ctx := context.Background()
	if ensureDelegatedCtx(ctx) != ctx {
		t.Fatal("live ctx must pass through")
	}
}

func TestW9CStatsTableDialects(t *testing.T) {
	env := newEnv(t)
	if got := env.deps.statsTable("usage_quota_hourly_window_dirty_scopes"); got != "usage_quota_hourly_window_dirty_scopes" {
		t.Fatalf("sqlite statsTable = %q", got)
	}
	pg := &Deps{PGDialect: true}
	if got := pg.statsTable("dirty"); got != "juhe_stats.dirty" {
		t.Fatalf("pg statsTable = %q", got)
	}
}

func TestW9CRateLimiterAndClockOverride(t *testing.T) {
	if NewDelegatedRateLimiter() == nil {
		t.Fatal("default limiter must build")
	}
	restore := SetNow(func() time.Time { return time.UnixMilli(1234) })
	if NewDelegatedRateLimiter() == nil {
		t.Fatal("override limiter must build")
	}
	deps := &Deps{}
	if deps.clock().UnixMilli() != 1234 {
		t.Fatalf("package clock override ignored: %v", deps.clock())
	}
	restore()
	if deps.clock().UnixMilli() < 1_500_000_000_000 {
		t.Fatalf("restored clock must be wall time: %v", deps.clock())
	}
	if deps.clock().UnixMilli() < 1 {
		t.Fatal("clock must always tick")
	}
}

func TestW9CContextFromAndQueryHelpers(t *testing.T) {
	request, _ := http.NewRequest(http.MethodGet, "http://gw/x", nil)
	if ContextFrom(request) != nil {
		t.Fatal("request without access context must yield nil")
	}
	values := url.Values{}
	if _, ok := positiveQueryInteger(values, "page"); ok {
		t.Fatal("missing param must not parse")
	}
	values.Set("page", "abc")
	if _, ok := positiveQueryInteger(values, "page"); ok {
		t.Fatal("non-digit param must not parse")
	}
	values.Set("page", "0")
	if _, ok := positiveQueryInteger(values, "page"); ok {
		t.Fatal("zero param must not parse")
	}
	values.Set("page", " 3 ")
	if value, ok := positiveQueryInteger(values, "page"); !ok || value != 3 {
		t.Fatalf("trimmed param = %d %v", value, ok)
	}
	values.Set("page", "99999999999999999999")
	if _, ok := positiveQueryInteger(values, "page"); ok {
		t.Fatal("overflow param must not parse")
	}
}

func TestW9CGroupBodyHelpers(t *testing.T) {
	body := map[string]any{"name": "n", "enabled": true, "desc": nil, "bad": 7}
	if !strictBody(map[string]any{"name": "n", "enabled": true, "desc": nil}, "name", "enabled", "desc") {
		t.Fatal("body keys within allowed set must pass")
	}
	if strictBody(body, "name") {
		t.Fatal("unexpected key must fail strictBody")
	}
	if value, ok := bodyStringField(body, "name"); !ok || value != "n" {
		t.Fatalf("bodyStringField = %q %v", value, ok)
	}
	if _, ok := bodyStringField(body, "missing"); ok {
		t.Fatal("missing key must not parse")
	}
	if _, ok := bodyStringField(body, "desc"); ok {
		t.Fatal("nil value must not parse")
	}
	if _, ok := bodyStringField(body, "bad"); ok {
		t.Fatal("non-string must not parse")
	}
	if _, present, ok := bodyOptionalString(body, "desc"); present || !ok {
		t.Fatal("nil optional string must be absent but ok")
	}
	if _, present, ok := bodyOptionalString(body, "bad"); !present || ok {
		t.Fatal("non-string optional must be present but not ok")
	}
	if value, present, ok := bodyOptionalString(body, "name"); !present || !ok || value != "n" {
		t.Fatalf("optional string = %q %v %v", value, present, ok)
	}
	if _, present, ok := bodyOptionalBool(body, "enabled"); !present || !ok || !bodyBoolValue(body) {
		t.Fatal("optional bool must parse true")
	}
	if _, present, ok := bodyOptionalBool(body, "missing"); present || !ok {
		t.Fatal("missing bool must be absent but ok")
	}
	if _, present, ok := bodyOptionalBool(map[string]any{"x": "yes"}, "x"); !present || ok {
		t.Fatal("non-bool optional must be present but not ok")
	}
}

func bodyBoolValue(body map[string]any) bool {
	value, _, _ := bodyOptionalBool(body, "enabled")
	return value
}

func TestW9CParseGroupPatchArms(t *testing.T) {
	if _, ok := parseGroupPatch(map[string]any{"name": 7}); ok {
		t.Fatal("non-string name must fail")
	}
	if _, ok := parseGroupPatch(map[string]any{"name": " "}); ok {
		t.Fatal("blank name must fail")
	}
	if _, ok := parseGroupPatch(map[string]any{"providerCode": nil}); !ok {
		t.Fatal("nil providerCode must be skipped, not fail")
	}
	if _, ok := parseGroupPatch(map[string]any{"providerCode": 3}); ok {
		t.Fatal("non-string providerCode must fail")
	}
	if _, ok := parseGroupPatch(map[string]any{"description": []any{}}); ok {
		t.Fatal("non-string description must fail")
	}
	if _, ok := parseGroupPatch(map[string]any{"enabled": "yes"}); ok {
		t.Fatal("non-bool enabled must fail")
	}
	if _, ok := parseGroupPatch(map[string]any{"groupType": "weird"}); ok {
		t.Fatal("unknown groupType must fail")
	}
	if _, ok := parseGroupPatch(map[string]any{"schedulingPolicy": "fast"}); ok {
		t.Fatal("non-object schedulingPolicy must fail")
	}
	input, ok := parseGroupPatch(map[string]any{
		"name": "n", "providerCode": "openai", "description": "d", "enabled": true,
		"groupType": "personal", "schedulingPolicy": map[string]any{"mode": "weighted"},
	})
	if !ok || input.Name == nil || input.ProviderCode == nil || input.Description == nil ||
		input.Enabled == nil || input.GroupType == nil || input.SchedulingPolicy == nil {
		t.Fatalf("full input = %+v ok=%v", input, ok)
	}
}

func TestW9CParseStrategyMutationArms(t *testing.T) {
	// Unknown field.
	if _, ok, _ := parseStrategyMutation(map[string]any{"name": "s", "nope": 1}, true); ok {
		t.Fatal("unknown field must fail")
	}
	// Create requires a name.
	if _, ok, message := parseStrategyMutation(map[string]any{"groupBindings": []any{bindingEntry()}}, true); ok || message != "策略路由参数无效" {
		t.Fatalf("missing create name = %v %q", ok, message)
	}
	// Blank name.
	if _, ok, _ := parseStrategyMutation(map[string]any{"name": " "}, true); ok {
		t.Fatal("blank name must fail")
	}
	// Bad mode / status.
	if _, ok, _ := parseStrategyMutation(map[string]any{"name": "s", "mode": "turbo"}, false); ok {
		t.Fatal("unknown mode must fail")
	}
	if _, ok, _ := parseStrategyMutation(map[string]any{"name": "s", "status": "paused"}, false); ok {
		t.Fatal("unknown status must fail")
	}
	// Description length in runes.
	long := make([]rune, 201)
	for i := range long {
		long[i] = '字'
	}
	if _, ok, _ := parseStrategyMutation(map[string]any{"name": "s", "description": string(long)}, false); ok {
		t.Fatal("201-rune description must fail")
	}
	// Bindings: not a list, empty list, oversized list, bad entries.
	if _, ok, _ := parseStrategyMutation(map[string]any{"name": "s", "groupBindings": "x"}, false); ok {
		t.Fatal("non-list bindings must fail")
	}
	if _, ok, _ := parseStrategyMutation(map[string]any{"name": "s", "groupBindings": []any{}}, false); ok {
		t.Fatal("empty bindings must fail")
	}
	many := make([]any, 21)
	for i := range many {
		many[i] = bindingEntry()
	}
	if _, ok, _ := parseStrategyMutation(map[string]any{"name": "s", "groupBindings": many}, false); ok {
		t.Fatal("21 bindings must fail")
	}
	if _, ok, _ := parseStrategyMutation(map[string]any{"name": "s", "groupBindings": []any{"x"}}, false); ok {
		t.Fatal("non-object binding must fail")
	}
	if _, ok, _ := parseStrategyMutation(map[string]any{"name": "s", "groupBindings": []any{map[string]any{"nope": 1}}}, false); ok {
		t.Fatal("unknown binding keys must fail")
	}
	if _, ok, _ := parseStrategyMutation(map[string]any{"name": "s", "groupBindings": []any{map[string]any{"groupId": "  "}}}, false); ok {
		t.Fatal("blank groupId must fail")
	}
	if _, ok, _ := parseStrategyMutation(map[string]any{"name": "s", "groupBindings": []any{map[string]any{"groupId": "g", "priority": 0}}}, false); ok {
		t.Fatal("priority < 1 must fail")
	}
	if _, ok, _ := parseStrategyMutation(map[string]any{"name": "s", "groupBindings": []any{map[string]any{"groupId": "g", "priority": 1.5}}}, false); ok {
		t.Fatal("fractional priority must fail")
	}
	if _, ok, _ := parseStrategyMutation(map[string]any{"name": "s", "groupBindings": []any{map[string]any{"groupId": "g", "weight": 101}}}, false); ok {
		t.Fatal("weight > 100 must fail")
	}
	if _, ok, _ := parseStrategyMutation(map[string]any{"name": "s", "groupBindings": []any{map[string]any{"groupId": "g", "status": "off"}}}, false); ok {
		t.Fatal("bad binding status must fail")
	}
	// Routing configs: null clears, non-object fails, object parses.
	if _, ok, _ := parseStrategyMutation(map[string]any{"name": "s", "normalRoutingConfig": 3}, false); ok {
		t.Fatal("non-object normal config must fail")
	}
	input, ok, _ := parseStrategyMutation(map[string]any{
		"name": "s", "description": "  d  ",
		"groupBindings":       []any{map[string]any{"groupId": " grp ", "weight": float64(50)}},
		"normalRoutingConfig": nil,
	}, false)
	if !ok {
		t.Fatal("valid strategy payload must parse")
	}
	if input.Description == nil || *input.Description != "d" {
		t.Fatalf("description = %+v", input.Description)
	}
	if len(input.Bindings) != 1 || input.Bindings[0].GroupID != "grp" || input.Bindings[0].Status != "active" ||
		input.Bindings[0].Priority != nil || input.Bindings[0].Weight == nil {
		t.Fatalf("bindings = %+v", input.Bindings)
	}
	if !input.HasNormal || input.NormalConfig != nil {
		t.Fatalf("normal config = %+v", input.NormalConfig)
	}
	// Create with no bindings yields the dedicated message.
	if _, ok, message := parseStrategyMutation(map[string]any{"name": "s"}, true); ok || message != "策略路由至少需要绑定一个分组" {
		t.Fatalf("create-without-bindings = %v %q", ok, message)
	}
	// strategyMutation projection fills the fallback priority.
	mutation := strategyMutation(input)
	if len(mutation.Bindings) != 1 || *mutation.Bindings[0].Priority != 1 {
		t.Fatalf("mutation bindings = %+v", mutation.Bindings)
	}
	if !mutation.HasNormalConfig || normalConfigRaw(input) != nil {
		t.Fatalf("normal raw = %v", normalConfigRaw(input))
	}
}

func bindingEntry() map[string]any {
	return map[string]any{"groupId": "grp-1"}
}
