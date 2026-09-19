package accountscore

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// IsoMillis mirrors Node toISOString() millisecond precision.
func IsoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000") + "Z"
}

// EnsureCtx mirrors the nil-context guard: the store accepts nil contexts the
// way the Node repository layer did.
func EnsureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// NullPtrString renders NULL/empty SQL text as an omitted JSON field.
func NullPtrString(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	return &value.String
}

// Placeholders renders the ?, ?, ... list for an IN clause.
func Placeholders(count int) string {
	if count <= 0 {
		return "?"
	}
	parts := make([]string, count)
	for index := range parts {
		parts[index] = "?"
	}
	return strings.Join(parts, ", ")
}

// AnySlice widens a string list into the database/sql argument slice.
func AnySlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// ContainsString mirrors the contains helper shared by the validation paths.
func ContainsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// StringSet mirrors the string-list-to-set helper.
func StringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

// NormalizeProviderToken mirrors provider-protocol.ts:113-117 (trim +
// lowercase; Node maps the empty result to undefined, which never equals the
// tokens below — the empty Go string compares the same way).
func NormalizeProviderToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// MinInt / MaxInt mirror the store-wide integer min/max helpers (REFACTOR-0005
// 阶段 B 下沉：余额/重置子域与门面共用).
func MinInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func MaxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// IsAccountExpired mirrors isAccountExpired: an account_expires_at instant at
// or before now marks the account expired; a blank value never expires.
func IsAccountExpired(accountExpiresAt string, now time.Time) bool {
	if strings.TrimSpace(accountExpiresAt) == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, accountExpiresAt)
	return err == nil && parsed.UnixMilli() <= now.UnixMilli()
}

// Itoa64 mirrors the store-wide int64 decimal renderer (newRandomID millis).
func Itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

// SortStrings sorts a string slice in place (insertion sort, the store-wide
// stable-enough ordering helper).
func SortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// BoolInt mirrors the boolean-to-SQL-int helper.
func BoolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// CanonicalRFC3339 mirrors canonicalizeRfc3339Instant (offset required, UTC/Z
// output with millisecond precision)（REFACTOR-0005 阶段 C 下沉：导入/批量域与
// 门面写路径共用）.
func CanonicalRFC3339(value string) (string, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	return IsoMillis(parsed), true
}

// CanonicalizeJSONValue JSON-round-trips arbitrary decoded values so both
// sides of a comparison carry identical shapes (float64, []any,
// map[string]any)（REFACTOR-0005 阶段 C 下沉：批量域深比较与门面凭据归一化
// 共用）.
func CanonicalizeJSONValue(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return value
	}
	return decoded
}

// DuplicateAccountNameError mirrors isDuplicateAccountNameError: the
// owner-scoped duplicate account name conflict classification
// （REFACTOR-0005 阶段 C 下沉：导入执行器与门面 write/patch 链共用）.
func DuplicateAccountNameError(err error, name string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "idx_accounts_owner_name_unique") ||
		strings.Contains(message, "UNIQUE constraint failed: accounts.system_account_id, accounts.name") ||
		strings.Contains(message, "UNIQUE constraint failed: juhe_business.accounts.system_account_id, juhe_business.accounts.name") {
		return &ConflictError{Message: "同一用户下账户名称已存在：" + name}
	}
	return nil
}
