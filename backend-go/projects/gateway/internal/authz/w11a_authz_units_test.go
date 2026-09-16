package authz

// w11a coverage arms (part 1): pure helpers across expiry parsing, list
// option normalization, usage date math, JSON equality, the stats cache LRU
// and strict-body decoding.

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestW11AStoreConstructorAndDialectArms(t *testing.T) {
	if _, err := NewStore(nil, false, time.Now); err == nil {
		t.Fatal("nil db must fail")
	}
	db, err := sql.Open("sqlite", "file:w11a-unused-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewStore(db, false, nil) // nil clock falls back to time.Now
	if err != nil {
		t.Fatal(err)
	}
	if got := store.table("resource_authorizations"); got != "resource_authorizations" {
		t.Fatalf("sqlite table = %q", got)
	}
	if got := store.bind("a = ? AND b = ?"); got != "a = ? AND b = ?" {
		t.Fatalf("sqlite bind = %q", got)
	}
	pgStore, err := NewStore(db, true, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if got := pgStore.bind("a = ? AND b = ?"); got != "a = $1 AND b = $2" {
		t.Fatalf("pg bind = %q", got)
	}
	if got := ensureCtx(nil); got == nil {
		t.Fatal("ensureCtx(nil) must return a context")
	}
	if got := itoa(42); got != "42" {
		t.Fatalf("itoa = %q", got)
	}
	if got := sqlInOutList("s", 3); got != "s (?, ?, ?)" {
		t.Fatalf("sqlInOutList = %q", got)
	}
	if got := sqlPlaceholders(3); got != "?, ?, ?" {
		t.Fatalf("sqlPlaceholders = %q", got)
	}
}

func TestW11AExpiryHelperArms(t *testing.T) {
	now := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	bad := "not-a-time"
	if _, err := normalizeAuthorizationExpiresAt(&bad); err == nil {
		t.Fatal("malformed expiry must fail")
	}
	if _, valid := parseAuthorizationRFC3339Instant("2026-09-16T00:00:00+99:00"); valid {
		t.Fatal("invalid offset must fail")
	}
	if _, valid := parseAuthorizationRFC3339Instant("2026-09-16T00:00:00"); valid {
		t.Fatal("zoneless instant must fail")
	}
	past := "2020-01-01T00:00:00.000Z"
	if _, err := validateAuthorizationCreateExpiresAt(&past, nil, now); err == nil {
		t.Fatal("past expiry must fail")
	}
	accountExpiry := "2026-12-31T00:00:00.000Z"
	late := "2027-01-01T00:00:00.000Z"
	if _, err := validateAuthorizationCreateExpiresAt(&late, &accountExpiry, now); err == nil {
		t.Fatal("expiry beyond the account expiry must fail")
	}
	badAccountExpiry := "nope"
	if _, err := validateAuthorizationCreateExpiresAt(&accountExpiry, &badAccountExpiry, now); err == nil {
		t.Fatal("malformed account expiry must fail")
	}
	future := "2026-10-01T00:00:00.000Z"
	normalized, err := validateAuthorizationCreateExpiresAt(&future, &accountExpiry, now)
	if err != nil || *normalized != future {
		t.Fatalf("valid expiry = %v, %v", normalized, err)
	}
	if got := canonicalizeAuthorizationInstant("2026-09-16T08:00:00+08:00"); got != "2026-09-16T00:00:00.000Z" {
		t.Fatalf("canonical = %q", got)
	}
	if got := canonicalizeAuthorizationInstant("bad"); got != "" {
		t.Fatalf("invalid canonical = %q", got)
	}
	if _, ok := instantMilliseconds("bad"); ok {
		t.Fatal("invalid instant must not yield millis")
	}
	if !authorizationExpiresPassed("2020-01-01T00:00:00.000Z", now) || authorizationExpiresPassed("", now) {
		t.Fatal("expiry pass logic broken")
	}
	if got := parseTimeOrNow("bad"); !got.Equal(now) {
		// parseTimeOrNow falls back to its own now; both are time.Now-based
		// in this arm only when called without the store clock. Assert non-zero.
		if got.IsZero() {
			t.Fatal("parseTimeOrNow fallback must be non-zero")
		}
	}
	if !authorizationVersionEqual("2026-09-16T00:00:00.000Z", "2026-09-16T00:00:00.000Z") ||
		authorizationVersionEqual("2026-09-16T00:00:00.000Z", "2026-09-16T00:00:00.001Z") {
		t.Fatal("version equality broken")
	}
	if authorizationVersionEqual("bad", "other") {
		t.Fatal("invalid versions must not be equal")
	}
}

func TestW11AJSONEqualityAndTextArms(t *testing.T) {
	if !nullableJSONEqual(nil, "") || nullableJSONEqual(strPtrW11A("x"), "") {
		t.Fatal("nullableJSONEqual nil semantics broken")
	}
	if !nullableJSONEqual(strPtrW11A(`{"a":1}`), `{"a":1}`) || nullableJSONEqual(strPtrW11A(`{"a":1}`), `{"a":2}`) {
		t.Fatal("nullableJSONEqual value semantics broken")
	}
	if !nullableJSONEqual(strPtrW11A("{invalid"), "{invalid") || nullableJSONEqual(strPtrW11A(`{"a":1}`), "{invalid") {
		t.Fatal("nullableJSONEqual invalid-json semantics broken")
	}
	if !nullableTextEqual(nil, sql.NullString{}) || nullableTextEqual(strPtrW11A("a"), sql.NullString{String: "b", Valid: true}) {
		t.Fatal("nullableTextEqual broken")
	}
	stored := sql.NullString{String: `{"a":1}`, Valid: true}
	if !authorizationLimitsSemanticallyEqual(strPtrW11A(`{"a":1}`), stored) ||
		authorizationLimitsSemanticallyEqual(strPtrW11A(`{"a":2}`), stored) {
		t.Fatal("limits semantic equality broken")
	}
	if !authorizationLimitsAssignmentEqual(strPtrW11A(`{"daily":{"enabled":true,"limit":1}}`), sql.NullString{String: `{"daily":{"enabled":true,"limit":1}}`, Valid: true}) {
		t.Fatal("limits assignment equality broken")
	}
	if got := grantLimitsText(sql.NullString{}); got != "" {
		t.Fatalf("empty limits text = %q", got)
	}
	if got := grantNullStringPointer(sql.NullString{String: "x", Valid: true}); got == nil || *got != "x" {
		t.Fatal("grantNullStringPointer broken")
	}
	if got := nullStringPointer(sql.NullString{}); got != nil {
		t.Fatal("nullStringPointer broken")
	}
	if got := limitsPointerOrNil(sql.NullString{String: "1", Valid: true}); got == nil {
		t.Fatal("limitsPointerOrNil broken")
	}
	values := make([]string, 55)
	for i := range values {
		values[i] = "k" + itoa(i)
	}
	if got := normalizeTextList(values); len(got) != 50 {
		t.Fatalf("normalized list = %d", len(got))
	}
	if got := textPrefixUpperBound("\U0010ffff"); got != "\U0010ffff\U0010ffff" {
		t.Fatalf("saturated bound = %q", got)
	}
	if got := textPrefixUpperBound("ab"); got != "ac" {
		t.Fatalf("plain bound = %q", got)
	}
}

func TestW11AUsageDateAndRowHelpers(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	if got := dateKeyAt(now, "UTC"); got != "2026-09-16" {
		t.Fatalf("dateKeyAt = %q", got)
	}
	if got := addCalendarDays("2026-09-16", 5); got != "2026-09-21" {
		t.Fatalf("addCalendarDays = %q", got)
	}
	if got := calendarDaysBetweenInclusive("2026-09-16", "2026-09-18"); got != 3 {
		t.Fatalf("calendarDays = %d", got)
	}
	if got := normalizeUsageStatsRange("", "", "UTC", now); got.EndDate != "2026-09-16" || got.Days < 1 {
		t.Fatalf("default range = %+v", got)
	}
	if page, size := normalizeUsagePageOptions(0, 0); page != 1 || size != 20 {
		t.Fatalf("usage page defaults = %d, %d", page, size)
	}
	if page, size := normalizeUsagePageOptions(-5, 999); page != 1 || size != 200 {
		t.Fatalf("usage page clamps = %d, %d", page, size)
	}
	if page, size := normalizeResourceAuthorizationUsagePageOptions(0, 0); page != 1 || size != 200 {
		t.Fatalf("detail page defaults = %d, %d", page, size)
	}
	if got := pagedTotalUpperBound(2, 20, 15, false); got != 35 {
		t.Fatalf("total upper bound = %d", got)
	}
	if got := clampCalendarDate("2027-01-01", "2026-09-16", "2025-01-01"); got != "2026-09-16" {
		t.Fatalf("clamped date = %q", got)
	}
	if got := clampCalendarDate("2025-06-01", "2026-09-16", "2026-01-01"); got != "2026-01-01" {
		t.Fatalf("clamped early date = %q", got)
	}
	if got := usageTimestampMilliseconds("2026-09-16T00:00:00.000Z"); got <= 0 {
		t.Fatalf("usage millis = %d", got)
	}
	if got := usageTimestampMilliseconds("bad"); got != 0 {
		t.Fatalf("bad usage millis = %d", got)
	}
	left := UsageSummary{RequestCount: 1, TotalTokens: 2}
	right := UsageSummary{RequestCount: 3, TotalTokens: 4}
	merged := addUsageSummaries(&left, &right)
	if merged.RequestCount != 4 || merged.TotalTokens != 6 {
		t.Fatalf("merged summary = %+v", merged)
	}
	if got := mapScopeIDs(nil); len(got) != 0 {
		t.Fatalf("mapScopeIDs(nil) = %v", got)
	}
	if got := uniqueStrings([]string{"a", "a", "b"}); strings.Join(got, ",") != "a,b" {
		t.Fatalf("uniqueStrings = %v", got)
	}
	if got := uniqueStringIDs([]string{"a", "a"}); len(got) != 1 {
		t.Fatalf("uniqueStringIDs = %v", got)
	}
	if got := runtimeIDs(nil); len(got) != 0 {
		t.Fatalf("runtimeIDs(nil) = %v", got)
	}
	if got := nonEmpty([]string{"", "a", ""}); strings.Join(got, ",") != "a" {
		t.Fatalf("nonEmpty = %v", got)
	}
	if got := toStrings([]string{"a"}); len(got) != 1 || got[0] != "a" {
		t.Fatalf("toStrings = %v", got)
	}
	if got := windowRowTeamIDs([]usageWindowRow{{TeamFilterID: "t1"}, {TeamFilterID: "t1"}}); len(got) != 2 {
		t.Fatalf("windowRowTeamIDs = %v", got)
	}
	if got := windowRowGranteeIDs([]usageWindowRow{{GranteeFilterID: "g"}}); len(got) != 1 {
		t.Fatalf("windowRowGranteeIDs = %v", got)
	}
	teams := map[string]string{"t1": "Team One"}
	if names := userUsageTeamNames("t1", teams); len(names) != 1 || names[0] != "Team One" {
		t.Fatalf("userUsageTeamNames = %v", names)
	}
	if names := userUsageTeamNames("missing", teams); len(names) != 0 {
		t.Fatalf("unknown team names = %v", names)
	}
	if _, ok := usageScopeKey(accessInfo{}); ok {
		t.Fatal("empty access must not yield a scope key")
	}
	if clause, args := usageDetailResourcePredicate("group", "grp"); clause == "" || len(args) != 2 {
		t.Fatalf("group predicate = %q, %v", clause, args)
	}
	if got := (&UsageFilters{ResourceType: "group", ResourceID: "g1"}).resourceFilterID(); got != "g1" {
		t.Fatalf("resourceFilterID = %q", got)
	}
	if got := resourceKey("group", "g1"); got != "group:g1" {
		t.Fatalf("resourceKey = %q", got)
	}
	empty := emptyUserRows(UsageStatsRange{StartDate: "2026-09-16"}, 2, 30)
	if empty.Page != 2 || len(empty.Rows) != 0 {
		t.Fatalf("emptyUserRows = %+v", empty)
	}
	emptyTeam := emptyTeamRows(UsageStatsRange{StartDate: "2026-09-16"}, 2, 30)
	if emptyTeam.Page != 2 || len(emptyTeam.Rows) != 0 {
		t.Fatalf("emptyTeamRows = %+v", emptyTeam)
	}
	if got := usageStatsSystemAccountID("account", "owner", "grantee"); got == "" {
		t.Fatal("usageStatsSystemAccountID empty")
	}
	if summary := fullUsageSummary(nil); summary.RequestCount != 0 {
		t.Fatalf("nil full summary = %+v", summary)
	}
}

func strPtrW11A(value string) *string { return &value }

func TestW11AStatsCacheLRUArms(t *testing.T) {
	now := time.Now
	cache := newAuthorizationStatsCache(now)
	stats := ResourceAuthorizationStats{AuthorizationCount: 1}
	cache.set("k1", stats)
	if got, ok := cache.get("k1"); !ok || got.AuthorizationCount != 1 {
		t.Fatalf("cache get = %+v, %v", got, ok)
	}
	// Overfill the LRU: the oldest entry (k1) is evicted.
	for i := 0; i <= authorizationStatsCacheMax; i++ {
		cache.set("w11a-key-"+itoa(i), stats)
	}
	if _, ok := cache.get("k1"); ok {
		t.Fatal("oldest entry must be evicted")
	}
	cache.delete(resourceAuthorizationStatsCacheKey("account", "gone"))
	cache.clear()
	if _, ok := cache.get("w11a-key-0"); ok {
		t.Fatal("cleared cache must miss")
	}
	if chunks := chunkValues([]string{"a", "b", "c"}, 2); len(chunks) != 2 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	if chunks := chunkValues([]string{"a"}, 0); len(chunks) != 1 {
		t.Fatalf("zero-size chunks = %d", len(chunks))
	}
}

func TestW11AStrictBodyDecodeArms(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte("{bad json")))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Length", "9")
	if decodeStrictJSON(recorder, request, &struct{}{}, map[string]bool{"a": true}) {
		t.Fatal("malformed json must fail")
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("malformed json status = %d", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"bogus":1}`)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Length", "11")
	if decodeStrictJSON(recorder, request, &struct{}{}, map[string]bool{"a": true}) {
		t.Fatal("unknown key must fail")
	}

	// Empty body passes through untouched.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Content-Type", "application/json")
	if !decodeStrictJSON(recorder, request, &struct{}{}, map[string]bool{"a": true}) {
		t.Fatal("empty body must pass")
	}

	// parseExpiresAtInput arms.
	if _, present, ok := parseExpiresAtInput([]byte(`  `)); present || !ok {
		t.Fatal("blank raw must be absent")
	}
	if _, _, ok := parseExpiresAtInput([]byte(`5`)); ok {
		t.Fatal("non-string expiresAt must fail")
	}
	if _, _, ok := parseExpiresAtInput([]byte(`""`)); ok {
		t.Fatal("empty string expiresAt must fail")
	}
	if _, _, ok := parseExpiresAtInput([]byte(`"bad"`)); ok {
		t.Fatal("invalid instant must fail")
	}
	value, present, ok := parseExpiresAtInput([]byte(`null`))
	if value != nil || !present || !ok {
		t.Fatalf("null expiresAt = %v, %v, %v", value, present, ok)
	}
}

func TestW11AExpiryForWriteArms(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "w11a-owner", "active")
	f.exec(t, `INSERT INTO accounts (id, system_account_id, resource_owner_system_account_id, name, created_at, updated_at)
		VALUES ('w11a-acct', 'w11a-owner', 'w11a-owner', 'acct', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	ctx := context.Background()
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	future := "2027-01-01T00:00:00.000Z"
	// No account expiry set: any future instant passes.
	if err := f.store.validateAuthorizationExpiresAtForWrite(ctx, tx, "account", "w11a-acct", &future, f.now, false); err != nil {
		t.Fatalf("unbounded account expiry must pass: %v", err)
	}
	if err := f.store.validateAuthorizationExpiresAtForWrite(ctx, tx, "account", "w11a-missing", &future, f.now, false); err != nil {
		t.Fatalf("missing resource must pass: %v", err)
	}
	// Account expiry earlier than the requested instant fails (updates ride
	// the open transaction: the fixture pool allows a single connection).
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET account_expires_at = '2026-12-31T00:00:00.000Z' WHERE id = 'w11a-acct'`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.validateAuthorizationExpiresAtForWrite(ctx, tx, "account", "w11a-acct", &future, f.now, false); err == nil {
		t.Fatal("expiry past the account expiry must fail")
	}
	// A malformed account expiry fails only when an expiry is being written.
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET account_expires_at = 'not-a-time' WHERE id = 'w11a-acct'`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.validateAuthorizationExpiresAtForWrite(ctx, tx, "account", "w11a-acct", &future, f.now, false); err == nil {
		t.Fatal("malformed account expiry must fail the write")
	}
	if err := f.store.validateAuthorizationExpiresAtForWrite(ctx, tx, "account", "w11a-acct", nil, f.now, false); err != nil {
		t.Fatalf("nil expiry with bad account expiry = %v", err)
	}
}

func TestW11ACascadeLimitsAndSprintf(t *testing.T) {
	if cascadeTeamMemberLimit() <= 0 || cascadeTeamGrantLimit() <= 0 || cascadeFanoutLimit() <= 0 {
		t.Fatal("cascade limits must be positive")
	}
	if got := failf("x %s", "y").Error(); got != "x y" {
		t.Fatalf("failf = %q", got)
	}
	if got := sprintf("%d", 7); got != "7" {
		t.Fatalf("sprintf = %q", got)
	}
	if (&Conflict{}).Error() == "" {
		t.Fatal("Conflict error text empty")
	}
	var _ http.HandlerFunc = func(w http.ResponseWriter, r *http.Request) {}
}
