// basestore.go owns the shared persistence base for the policyreads domains:
// the store error types, the gateway runtime invalidation port, the dual-mode
// baseStore and small generic helpers used across domains.
package policyreads

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ConflictError maps to Node conflict outcomes rendered as 409 (patch conflicts
// and guarded duplicates across the three domains).
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// ValidationError maps to Node throw-Error paths rendered as 400.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// OidcCiphertextError maps to Node OidcCiphertextError (oauth.go).
type OidcCiphertextError struct{ Message string }

func (e *OidcCiphertextError) Error() string { return e.Message }

// RuntimeInvalidator is the K5 gateway runtime cache invalidation port
// (Node notifyGatewayRuntimeCacheInvalidation). *inval.Bus satisfies it; nil
// keeps the slice self-contained with no-op invalidation.
type RuntimeInvalidator interface {
	Invalidate(topic, reason string)
}

// TopicGatewayRuntime mirrors the Node gateway runtime cache topic constant.
const TopicGatewayRuntime = "topic:gateway_runtime_cache"

// baseStore is the dual-mode (SQLite + PostgreSQL) persistence core shared by
// the three domains.
type baseStore struct {
	db    *sql.DB
	pg    bool
	now   func() time.Time
	newID func(string) string
	inval RuntimeInvalidator
}

func newBaseStore(db *sql.DB, postgres bool, now func() time.Time, newID func(string) string, inval RuntimeInvalidator) (baseStore, error) {
	if db == nil {
		return baseStore{}, errors.New("policyreads store requires a database")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = randomPrefixedID
	}
	return baseStore{db: db, pg: postgres, now: now, newID: newID, inval: inval}, nil
}

func (b *baseStore) table(name string) string {
	if b.pg {
		return "juhe_business." + name
	}
	return name
}

func (b *baseStore) bind(query string) string {
	if !b.pg {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + strconv.Itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

func (b *baseStore) nowISO() string { return isoMillis(b.now()) }

func (b *baseStore) generateID(prefix string) string { return b.newID(prefix) }

// invalidateRuntime mirrors invalidateGatewayRuntimeAfterBusinessWrite.
func (b *baseStore) invalidateRuntime(reason string) {
	if b.inval != nil {
		b.inval.Invalidate(TopicGatewayRuntime, reason)
	}
}

// randomPrefixedID mirrors Node newId(prefix) (random hex suffix).
func randomPrefixedID(prefix string) string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return prefix + "_" + hex.EncodeToString(buf)
}

func randomBase64URLBytes(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return base64RawURL(buf)
}

func base64RawURL(data []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	out := make([]byte, 0, (len(data)*8+5)/6)
	var buffer, bits int
	for _, b := range data {
		buffer = buffer<<8 | int(b)
		bits += 8
		for bits >= 6 {
			bits -= 6
			out = append(out, alphabet[(buffer>>bits)&0x3f])
		}
	}
	if bits > 0 {
		out = append(out, alphabet[(buffer<<(6-bits))&0x3f])
	}
	return string(out)
}

func newUUIDv4() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	h := hex.EncodeToString(buf)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func ensureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// isoMillis mirrors Node nowIso()/toISOString() millisecond precision.
func isoMillis(t time.Time) string {
	return t.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z07:00")
}

var rfc3339InstantPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$`)

// canonicalRFC3339Millis mirrors canonicalizeRfc3339Instant: RFC3339 with a
// mandatory Z or numeric offset, normalized to millisecond-precision UTC.
func canonicalRFC3339Millis(value string) (string, bool) {
	text := strings.TrimSpace(value)
	if !rfc3339InstantPattern.MatchString(text) {
		return "", false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return "", false
	}
	return isoMillis(parsed), true
}

// parseRFC3339Millis mirrors rfc3339InstantMilliseconds.
func parseRFC3339Millis(value string) (int64, bool) {
	text := strings.TrimSpace(value)
	if !rfc3339InstantPattern.MatchString(text) {
		return 0, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// nextRFC3339Millis mirrors nextPolicyUpdatedAt /
// nextExternalIntegrationUpdatedAt: monotonic RFC3339 millis from now.
func nextRFC3339Millis(current string, now time.Time, label string) (string, error) {
	currentMs, ok := parseRFC3339Millis(current)
	if !ok {
		return "", &ValidationError{Message: label + "：" + current}
	}
	next := now.UnixMilli()
	if floor := currentMs + 1; next < floor {
		next = floor
	}
	return isoMillis(time.UnixMilli(next)), nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func ptrString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nullPtrString(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	return &value.String
}

func runeLen(value string) int {
	runes := []rune(value)
	return len(runes)
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "..."
}

// escapeLikePrefix mirrors storage/query-utils.ts escapeLikePrefix.
func escapeLikePrefix(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

// uniqueSortedStrings mirrors [...new Set(values)].sort().
func uniqueSortedStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// safeChangeText mirrors operation-log.service.ts normalizeSafeValue for the
// string-only Go change struct.
func safeChangeText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return truncateRunes(typed, 200)
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	default:
		serialized, err := json.Marshal(value)
		if err != nil {
			return truncateRunes(fmt.Sprintf("%v", value), 500)
		}
		return truncateRunes(string(serialized), 500)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
