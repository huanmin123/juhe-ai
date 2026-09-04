package authz

import (
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

func validateAuthorizationPatchExpiresAt(value, accountExpiresAt *string, now time.Time, allowExpired bool) (*string, error) {
	normalized, err := normalizeAuthorizationExpiresAt(value)
	if err != nil || normalized == nil {
		return normalized, err
	}
	expiresAt, valid := parseAuthorizationRFC3339Instant(*normalized)
	if !valid {
		return nil, failf("过期时间格式不正确")
	}
	if !allowExpired && !expiresAt.After(now.UTC()) {
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
