// Shared dual-mode SQL plumbing and small value helpers for the aipublic
// package (mirrors the baseStore plumbing of internal/policyreads and the
// generic helpers the other slices keep package-private).
package aipublic

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

var aipublicScopeValues = []string{
	scopeGroupListRead, scopeStrategyListRead, scopeApiKeyListRead, scopeAccountListRead,
	scopeGroupAddWrite, scopeGroupUpdateWrite, scopeGroupDeleteWrite,
	scopeStrategyAddWrite, scopeStrategyUpdateWrite, scopeStrategyDeleteWrite,
	scopeApiKeyAddWrite, scopeApiKeyUpdateWrite, scopeApiKeyDeleteWrite,
	scopeAccountAddWrite, scopeAccountUpdateWrite, scopeAccountDeleteWrite,
}

func scopeSupported(value string) bool {
	return containsString(aipublicScopeValues, value)
}

func (d *Deps) db() *sql.DB { return d.DB }

// bind rewrites ? placeholders for the PostgreSQL dialect ($1, $2, ...),
// mirroring the other slices.
func (d *Deps) bind(query string) string {
	if !d.PGDialect {
		return query
	}
	var builder strings.Builder
	index := 0
	for _, char := range query {
		if char == '?' {
			index++
			builder.WriteString("$" + strconv.Itoa(index))
			continue
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

func ensureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func sortStrings(values []string) {
	sort.Strings(values)
}

func uniqueSortedStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sortStrings(out)
	return out
}

func jsonUnmarshal(data []byte, target any) error {
	return json.Unmarshal(data, target)
}

func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}

// strconvParseFloat mirrors Number(text) for the coerce path (strict float
// syntax, no surrounding whitespace beyond what the caller trimmed).
func strconvParseFloat(text string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(text), 64)
}

// numberToInt converts a decoded JSON float64 into an int when integral.
func numberToInt(value any) (int, bool) {
	number, isNumber := value.(float64)
	if !isNumber || number != float64(int64(number)) {
		return 0, false
	}
	return int(number), true
}

// rfc3339Millis mirrors rfc3339InstantMilliseconds (Z or numeric offset).
func rfc3339Millis(value string) *int64 {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	millis := parsed.UnixMilli()
	return &millis
}

func nullMillis(value sql.NullString) *int64 {
	if !value.Valid {
		return nil
	}
	return rfc3339Millis(value.String)
}

func nullPtrString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func ptrToNullString(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func nullStringText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// normalizedText mirrors normalizedText: trim, empty -> "".
func normalizedText(value any) string {
	text, isString := value.(string)
	if !isString {
		return ""
	}
	trimmed := strings.TrimSpace(text)
	return trimmed
}

// runeLen is the zod length unit (JS .length counts UTF-16 code units; the
// migrated stores approximate with runes the same way the other slices do).
func runeLen(value string) int {
	return len([]rune(value))
}

// sameText mirrors sameText (case-insensitive trimmed equality).
func sameText(left, right string) bool {
	return strings.TrimSpace(strings.ToLower(left)) == strings.TrimSpace(strings.ToLower(right))
}

// normalizedStringList mirrors normalizedStringList: trim, drop blanks,
// dedupe preserving first-seen order.
func normalizedStringList(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

// numberFrom reads an integral number from a decoded JSON value.
func numberFrom(value any) (int, bool) {
	return numberToInt(value)
}

// kernelWriteBadRequest is a tiny indirection so the mock file does not import
// kernel twice (single import site in dto.go).
func kernelWriteBadRequest(w http.ResponseWriter, message string) {
	kernel.WriteBadRequest(w, message)
}

// base64URLEncode mirrors Node randomBytes(n).toString('base64url').
func base64URLEncode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// kernelWriteErrorStatus delegates to the kernel error writer.
func kernelWriteErrorStatus(w http.ResponseWriter, status int, message string) {
	kernel.WriteError(w, status, message)
}
