package authz

import (
	"context"
	"database/sql"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// authorizationRFC3339InstantPattern mirrors Node's parseRfc3339Instant:
// absolute offset is mandatory and fractional seconds have at most nanosecond
// precision. A bare local datetime must never acquire this server's timezone.
var authorizationRFC3339InstantPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$`)

// normalizeAuthorizationExpiresAt mirrors
// normalizeResourceAuthorizationExpiresAtInput. It preserves an omitted value
// as NULL and canonicalizes valid input to Node Date#toISOString precision.
func normalizeAuthorizationExpiresAt(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	parsed, valid := parseAuthorizationRFC3339Instant(*value)
	if !valid {
		return nil, failf("过期时间格式不正确")
	}
	canonical := parsed.UTC().Format("2006-01-02T15:04:05.000Z")
	return &canonical, nil
}

func parseAuthorizationRFC3339Instant(value string) (time.Time, bool) {
	text := strings.TrimSpace(value)
	match := authorizationRFC3339InstantPattern.FindStringSubmatch(text)
	if match == nil {
		return time.Time{}, false
	}
	if match[8] != "Z" {
		hour, hourErr := strconv.Atoi(match[8][1:3])
		minute, minuteErr := strconv.Atoi(match[8][4:6])
		if hourErr != nil || minuteErr != nil || hour > 23 || minute > 59 {
			return time.Time{}, false
		}
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func validateAuthorizationCreateExpiresAt(value, accountExpiresAt *string, now time.Time) (*string, error) {
	normalized, err := normalizeAuthorizationExpiresAt(value)
	if err != nil || normalized == nil {
		return normalized, err
	}
	expiresAt, valid := parseAuthorizationRFC3339Instant(*normalized)
	if !valid {
		return nil, failf("过期时间格式不正确")
	}
	if !expiresAt.After(now.UTC()) {
		return nil, failf("授权到期时间不能早于当前时间")
	}
	if accountExpiresAt != nil {
		accountExpiry, valid := parseAuthorizationRFC3339Instant(*accountExpiresAt)
		if !valid {
			return nil, failf("账户到期时间必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
		if expiresAt.After(accountExpiry) {
			return nil, failf("授权到期时间不能晚于账户到期时间")
		}
	}
	return normalized, nil
}

// canonicalizeAuthorizationInstant mirrors canonicalizeRfc3339Instant
// (shared/rfc3339.ts): valid input becomes the UTC millisecond ISO string,
// invalid input yields "".
func canonicalizeAuthorizationInstant(value string) string {
	parsed, valid := parseAuthorizationRFC3339Instant(value)
	if !valid {
		return ""
	}
	return parsed.UTC().Format("2006-01-02T15:04:05.000Z")
}

// instantMilliseconds mirrors rfc3339InstantMilliseconds. The second result is
// false when the value is not a valid absolute instant.
func instantMilliseconds(value string) (int64, bool) {
	parsed, valid := parseAuthorizationRFC3339Instant(value)
	if !valid {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// authorizationExpiresPassed mirrors isResourceAuthorizationExpired
// (resource-authorization-helpers.ts:123-128): a NULL expiry never expires.
func authorizationExpiresPassed(expiresAt string, now time.Time) bool {
	if expiresAt == "" {
		return false
	}
	timestamp, valid := instantMilliseconds(expiresAt)
	if !valid {
		return false
	}
	return timestamp <= now.UnixMilli()
}

// parseTimeOrNow is a defensive fallback for stored bookkeeping values; the
// authorization paths always pass canonical instants.
func parseTimeOrNow(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Now().UTC()
	}
	return parsed
}

// authorizationVersionEqual compares optimistic-lock versions by instant. Node
// canonicalizes the client value at the route (rfc3339InstantSchema) and then
// compares raw strings (:527/565, return.repository.ts:132/173) because both
// sides are millisecond-precision UTC; Go versions may carry nanosecond
// precision, so the same contract is expressed as instant equality.
func authorizationVersionEqual(expected, current string) bool {
	if expected == current {
		return true
	}
	expectedMs, expectedOK := instantMilliseconds(expected)
	currentMs, currentOK := instantMilliseconds(current)
	return expectedOK && currentOK && expectedMs == currentMs
}

// validateAuthorizationExpiresAtForWrite mirrors
// validateResourceAuthorizationExpiresAtAsync (:2654-2679): NULL skips, the
// past is rejected unless the target state is expired, and account resources
// are capped by the source account expiry.
func (s *Store) validateAuthorizationExpiresAtForWrite(ctx context.Context, tx *sql.Tx, resourceType, resourceID string, expiresAt *string, now time.Time, allowExpired bool) error {
	if expiresAt == nil || *expiresAt == "" {
		return nil
	}
	expiresMs, valid := instantMilliseconds(*expiresAt)
	if !valid {
		return failf("授权到期时间必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	if !allowExpired && expiresMs <= now.UnixMilli() {
		return failf("授权到期时间不能早于当前时间")
	}
	if resourceType != "account" {
		return nil
	}
	var accountExpiresAt sql.NullString
	err := tx.QueryRowContext(ctx, s.bind(`SELECT account_expires_at FROM `+s.table("accounts")+`
		WHERE id = ? AND deleted_at IS NULL LIMIT 1`), resourceID).Scan(&accountExpiresAt)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if !accountExpiresAt.Valid || accountExpiresAt.String == "" {
		return nil
	}
	accountMs, valid := instantMilliseconds(accountExpiresAt.String)
	if !valid {
		return failf("账户到期时间必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	if expiresMs > accountMs {
		return failf("授权到期时间不能晚于账户到期时间")
	}
	return nil
}
