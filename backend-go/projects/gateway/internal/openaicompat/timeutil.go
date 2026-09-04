package openaicompat

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// rfc3339InstantPattern mirrors shared/rfc3339.ts: offset is mandatory and
// bare datetimes never fall back to the local timezone.
var rfc3339InstantPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$`)

func daysInMonth(year, month int) int {
	// Node: new Date(Date.UTC(year, month, 0)).getUTCDate() (month is 1-based here).
	return time.Date(year, time.Month(month+1), 0, 0, 0, 0, 0, time.UTC).Day()
}

// parseRFC3339Instant mirrors parseRfc3339Instant: strict field validation
// plus the Z / numeric-offset requirement.
func parseRFC3339Instant(value string) (time.Time, bool) {
	match := rfc3339InstantPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return time.Time{}, false
	}
	atoi := func(text string) int {
		number, _ := strconv.Atoi(text)
		return number
	}
	year, month, day := atoi(match[1]), atoi(match[2]), atoi(match[3])
	hour, minute, second := atoi(match[4]), atoi(match[5]), atoi(match[6])
	offset := match[8]
	if month < 1 || month > 12 ||
		day < 1 || day > daysInMonth(year, month) ||
		hour > 23 || minute > 59 || second > 59 ||
		(offset != "Z" && (atoi(offset[1:3]) > 23 || atoi(offset[4:6]) > 59)) {
		return time.Time{}, false
	}
	location := time.FixedZone("", 0)
	if offset != "Z" {
		sign := 1
		if offset[0] == '-' {
			sign = -1
		}
		location = time.FixedZone("", sign*(atoi(offset[1:3])*3600+atoi(offset[4:6])*60))
	}
	parsed := time.Date(year, time.Month(month), day, hour, minute, second, 0, location)
	return parsed, true
}

// rfc3339InstantMilliseconds mirrors rfc3339InstantMilliseconds.
func rfc3339InstantMilliseconds(value string) (int64, bool) {
	parsed, ok := parseRFC3339Instant(value)
	if !ok {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// openAITimestamp mirrors the per-module openAITimestamp helpers: RFC3339
// instants render as OpenAI epoch seconds; invalid input is an invariant
// failure (Node throws a generic Error -> 500).
func openAITimestamp(value string) (int64, error) {
	milliseconds, ok := rfc3339InstantMilliseconds(value)
	if !ok {
		return 0, fmt.Errorf("OpenAI 兼容时间必须是带 Z 或数值 offset 的 RFC3339 时间: %s", value)
	}
	return milliseconds / 1000, nil
}

// isoMillis mirrors Node nowIso() (Date.toISOString millisecond precision).
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// expiresAtFromDays mirrors expiresAtFromDays in vector-stores.routes.ts:
// only positive day counts produce an expiry instant. Node's queryInteger
// already collapsed non-finite input to undefined, so a nil pointer or a
// non-positive value means "no expiry".
func expiresAtFromDays(days *int, now time.Time) *string {
	if days == nil || *days <= 0 {
		return nil
	}
	expiry := now.Add(time.Duration(*days) * 24 * time.Hour)
	text := isoMillis(expiry)
	return &text
}

// randomHex mirrors randomUUID().replace(/-/g, ”) slices: 16 random bytes
// hex-encoded (32 chars).
func randomHex(characters int) string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	encoded := hex.EncodeToString(buf)
	if characters > len(encoded) {
		characters = len(encoded)
	}
	return encoded[:characters]
}

// newOpenAICompatibleFileID mirrors newOpenAICompatibleFileId:
// file-<base36 ms>-<20 hex>.
func newOpenAICompatibleFileID(now time.Time) string {
	return "file-" + strconv.FormatInt(now.UnixMilli(), 36) + "-" + randomHex(20)
}

// newOpenAICompatibleVectorStoreID mirrors newOpenAICompatibleVectorStoreId:
// vs_<base36 ms>_<20 hex>.
func newOpenAICompatibleVectorStoreID(now time.Time) string {
	return "vs_" + strconv.FormatInt(now.UnixMilli(), 36) + "_" + randomHex(20)
}

// newVectorStoreChunkID mirrors the chunk insert id: vschunk_<32 hex>.
func newVectorStoreChunkID() string {
	return "vschunk_" + randomHex(32)
}
