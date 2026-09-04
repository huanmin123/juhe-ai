// Package chat migrates the my-chat domain (Node backend/src/modules/chat/
// plus the chat-* storage repositories under backend/src/storage/) into the
// Go gateway. The slice covers the owner-scoped conversation/turn/message
// lifecycle, the storage-quota windows, the context compaction state machine
// and the SSE generation pipeline behind POST .../stream, with the same
// routes, payloads, pagination cursors, revision counters and Chinese error
// strings as the Node implementation.
//
// External collaborators are ports satisfied at the composition root: the
// gateway runtime snapshot (accounts + model catalog), the gateway API-key
// validator (group bindings + image-generation flag), the chat API-key
// provider and the internal gateway dispatcher (Node dispatchChatGatewayRequest).
// Only internal/chat files are owned here; kernel/authsys/apikeys/crypto are
// import-only.
package chat

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Store is the dual-mode chat persistence (SQLite + PostgreSQL) mirroring
// chat.repository.ts / chat-context.repository.ts / chat-assets.repository.ts /
// chat-image-generations.repository.ts. The Node code qualifies chat tables
// with dialect.qualifyTable('juhe_chat', name); PostgreSQL keeps the juhe_chat
// schema prefix, SQLite uses bare table names.
type Store struct {
	db    *sql.DB
	pg    bool
	now   func() time.Time
	newID func(prefix string) string
}

// NewStore builds the chat store. now is the injected clock (time injection
// contract); newID generates prefixed ids (chat_<prefix>_<32 hex> by default).
func NewStore(db *sql.DB, postgres bool, now func() time.Time, newID func(prefix string) string) (*Store, error) {
	if db == nil {
		return nil, errors.New("chat store requires a database")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = func(prefix string) string { return chatID(prefix) }
	}
	return &Store{db: db, pg: postgres, now: now, newID: newID}, nil
}

// Postgres reports the active dialect (dual-mode tests assert both).
func (s *Store) Postgres() bool { return s.pg }

// DB exposes the underlying handle for route-level composition.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) table(name string) string {
	if s.pg {
		return "juhe_chat." + name
	}
	return name
}

func (s *Store) bind(query string) string {
	if !s.pg {
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

// lockSuffix mirrors `tx.driver === 'postgres' ? ' FOR UPDATE' : ''`.
func (s *Store) lockSuffix() string {
	if s.pg {
		return " FOR UPDATE"
	}
	return ""
}

func ensureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// isoMillis mirrors Node Date.toISOString() millisecond precision.
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func (s *Store) nowTime() time.Time { return s.now() }

// nowISO mirrors new Date().toISOString() at call sites.
func (s *Store) nowISO() string { return isoMillis(s.now()) }

// chatID mirrors chatId(): chat_<prefix>_<uuid hex without dashes>.
func chatID(prefix string) string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return "chat_" + prefix + "_" + hex.EncodeToString(buf)
}

// addDays mirrors addDays(now, days) (UTC calendar-day arithmetic, ISO out).
func addDays(value string, days int, label string) (string, error) {
	parsed, err := parseRFC3339Instant(value)
	if err != nil {
		return "", &DomainError{Message: label + "必须是带 Z 或数值 offset 的 RFC3339 时间"}
	}
	return isoMillis(parsed.UTC().AddDate(0, 0, days)), nil
}

var rfc3339InstantPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$`)

// parseRFC3339Instant mirrors shared/rfc3339.ts parseRfc3339Instant: RFC3339
// with a mandatory offset; bare datetimes are rejected instead of being read
// in the local timezone.
func parseRFC3339Instant(value string) (time.Time, error) {
	match := rfc3339InstantPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return time.Time{}, errors.New("invalid")
	}
	year, _ := strconv.Atoi(match[1])
	month, _ := strconv.Atoi(match[2])
	day, _ := strconv.Atoi(match[3])
	hour, _ := strconv.Atoi(match[4])
	minute, _ := strconv.Atoi(match[5])
	second, _ := strconv.Atoi(match[6])
	if month < 1 || month > 12 || day < 1 || day > daysInMonth(year, month) {
		return time.Time{}, errors.New("invalid")
	}
	if hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, errors.New("invalid")
	}
	offset := match[8]
	location := time.UTC
	if offset != "Z" {
		offsetHours, _ := strconv.Atoi(offset[1:3])
		offsetMinutes, _ := strconv.Atoi(offset[4:6])
		if offsetHours > 23 || offsetMinutes > 59 {
			return time.Time{}, errors.New("invalid")
		}
		location = time.FixedZone("", (offsetHours*60+offsetMinutes)*60*sign(offset[0]))
	}
	fraction := match[7]
	nanos := 0
	if fraction != "" {
		padded := fraction + "000000000"
		nanos, _ = strconv.Atoi(padded[:9])
	}
	parsed := time.Date(year, time.Month(month), day, hour, minute, second, nanos, location)
	if parsed.Year() != year || parsed.Month() != time.Month(month) || parsed.Day() != day {
		return time.Time{}, errors.New("invalid")
	}
	return parsed, nil
}

func sign(b byte) int {
	if b == '-' {
		return -1
	}
	return 1
}

func daysInMonth(year, month int) int {
	switch month {
	case 1, 3, 5, 7, 8, 10, 12:
		return 31
	case 4, 6, 9, 11:
		return 30
	}
	if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
		return 29
	}
	return 28
}

// requireRFC3339Instant mirrors requiredRfc3339Instant: canonicalized UTC
// RFC3339 with millisecond precision or a labeled error.
func requireRFC3339Instant(value string, label string) (string, error) {
	parsed, err := parseRFC3339Instant(value)
	if err != nil {
		return "", &DomainError{Message: label + "必须是带 Z 或数值 offset 的 RFC3339 时间"}
	}
	return isoMillis(parsed), nil
}

// canonicalRFC3339 returns the canonical instant, if valid.
func canonicalRFC3339(value string) (string, bool) {
	parsed, err := parseRFC3339Instant(value)
	if err != nil {
		return "", false
	}
	return isoMillis(parsed), true
}

// rfc3339Millis mirrors rfc3339InstantMilliseconds.
func rfc3339Millis(value string) (int64, bool) {
	parsed, err := parseRFC3339Instant(value)
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// DomainError marks non-public server faults surfaced as
// internal_generation_failed with a sanitized diagnostic detail (Node plain
// `throw new Error(...)` inside the chat domain).
type DomainError struct{ Message string }

func (e *DomainError) Error() string { return e.Message }

// nullText renders a nullable column value ("nullable()" in Node).
func nullText(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	return &value.String
}

// sqlText wraps a *string for storage (nil → NULL).
func sqlText(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func placeholders(count int) string {
	pieces := make([]string, count)
	for i := range pieces {
		pieces[i] = "?"
	}
	return strings.Join(pieces, ",")
}
