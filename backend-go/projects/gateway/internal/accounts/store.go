package accounts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
)

// ConflictError maps to the Node route family 409 paths: the owner-scoped
// duplicate account name error.
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// ValidationError maps to the Node 400 mutation message set surfaced through
// the body validation and repository normalization layers.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// RevisionConflictError maps to AccountManagementPatchRevisionConflictError /
// the lock config-revision CAS failures: the route family renders 409 with the
// Node copy ('账户配置已被其他操作更新，请刷新后重试' for the patch, the lock
// copies for the lock family).
type RevisionConflictError struct{ Message string }

func (e *RevisionConflictError) Error() string { return e.Message }

// RevisionConflictMessage mirrors the accounts.routes.ts PATCH catch copy.
const RevisionConflictMessage = "账户配置已被其他操作更新，请刷新后重试"

// Lock messages mirror account-lock.routes.ts.
const (
	lockNotFoundMessage       = "账户不存在或无权操作"
	lockConfigConflictMessage = "账户配置已发生并发变更，请刷新列表后重试"
	lockStateConflictMessage  = "账户锁死状态已发生并发变更，请刷新列表后重试"
)

// Tag messages mirror account-tags.routes.ts.
const (
	tagNotFoundMessage   = "标签不存在"
	tagInUseMessage      = "标签已绑定账户，不能删除"
	maxTagsPerAccount    = 24
	maxTagNameLength     = 40
	maxAccountNameLength = 128
)

// AccessScope mirrors storage/access-scope.ts for the accounts slice: admins
// see every row unless a systemAccountId filter narrows the view; users are
// pinned to their own rows (forceSelfAccessScope).
type AccessScope struct {
	ViewerID string
	IsAdmin  bool
	FilterID string
}

// manageableID mirrors manageableSystemAccountId: admins pass the filter
// through (possibly empty = unscoped), non-admins are pinned to themselves.
func (a AccessScope) manageableID() string {
	if a.IsAdmin {
		return a.FilterID
	}
	return a.ViewerID
}

func (a AccessScope) canAccessAll() bool { return a.IsAdmin }

// viewerID mirrors userVisibleSystemAccountId: the filter for scoped admins,
// the caller otherwise.
func (a AccessScope) viewerID() string {
	if id := a.manageableID(); id != "" {
		return id
	}
	return a.ViewerID
}

// ownerID mirrors writeSystemAccountId: the account stamped on newly created
// rows (explicit group ownership may override it in Create).
func (a AccessScope) ownerID() (string, error) {
	if a.ViewerID != "" {
		return a.ViewerID, nil
	}
	return "", &ValidationError{Message: "缺少系统账户上下文"}
}

// Store is the dual-mode accounts persistence (SQLite + PostgreSQL). secret
// is the Node runtimeConfig.secret material: accounts.credentials_encrypted
// rows written by Node must stay decryptable.
type Store struct {
	db     *sql.DB
	pg     bool
	secret string
	now    func() time.Time
	newI   func(prefix string) string
	// authorized is the M10 authorized-instance reader (authz slice, narrow
	// interface). Nil until SetAuthorizedReader / Deps.Mount wires it.
	authorized AuthorizedAccountReader
	// invalidator is the batch-edit post-commit cache invalidation port
	// (batch_effects.go). Nil until SetCacheInvalidator wires it; a nil port
	// keeps the batch self-contained.
	invalidator CacheInvalidator
}

// NewStore builds the store.
func NewStore(db *sql.DB, postgres bool, secret string, now func() time.Time, newID func(string) string) (*Store, error) {
	if db == nil {
		return nil, errors.New("accounts store requires a database")
	}
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("accounts store requires the runtime secret")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = func(prefix string) string { return newRandomID(prefix) }
	}
	return &Store{db: db, pg: postgres, secret: secret, now: now, newI: newID}, nil
}

// newRandomID mirrors Node newId(prefix): "{prefix}_{millis}_{8 hex}".
func newRandomID(prefix string) string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return prefix + "_" + itoa64(time.Now().UnixMilli()) + "_" + hex.EncodeToString(buf)[:8]
}

func itoa64(v int64) string {
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

func (s *Store) table(name string) string {
	if s.pg {
		return "juhe_business." + name
	}
	return name
}

// bind rewrites ? placeholders into $N for PostgreSQL.
func (s *Store) bind(query string) string {
	if !s.pg {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

func itoa(v int) string {
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

func ensureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// isoMillis mirrors Node toISOString() millisecond precision.
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000") + "Z"
}

// canonicalRFC3339 mirrors canonicalizeRfc3339Instant (offset required, UTC/Z
// output with millisecond precision).
func canonicalRFC3339(value string) (string, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	return isoMillis(parsed), true
}

// normalizeAccountNameSearchText mirrors normalizeAccountNameSearchText:
// NFKC + trim.
func normalizeAccountNameSearchText(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(norm.NFKC.String(text))
}

// accountNameSearchQueryTerms mirrors accountNameSearchQueryTerms: the 3-gram
// set of the normalized keyword.
func accountNameSearchQueryTerms(keyword any) []string {
	normalized := normalizeAccountNameSearchText(keyword)
	runes := []rune(normalized)
	if normalized == "" || len(runes) > maxAccountNameLength {
		return nil
	}
	length := minInt(3, len(runes))
	if len(runes) < length {
		return nil
	}
	seen := map[string]bool{}
	terms := []string{}
	for index := 0; index+length <= len(runes); index++ {
		term := string(runes[index : index+length])
		if strings.TrimSpace(term) != "" && !seen[term] {
			seen[term] = true
			terms = append(terms, term)
		}
	}
	return terms
}

// buildAccountNameSearchTerms mirrors buildAccountNameSearchTerms: the 1..3
// gram set written into account_name_search_terms.
func buildAccountNameSearchTerms(name string) []string {
	normalized := normalizeAccountNameSearchText(name)
	if normalized == "" {
		return nil
	}
	runes := []rune(normalized)
	seen := map[string]bool{}
	terms := []string{}
	for length := 1; length <= 3; length++ {
		for index := 0; index+length <= len(runes); index++ {
			term := string(runes[index : index+length])
			if strings.TrimSpace(term) != "" && !seen[term] {
				seen[term] = true
				terms = append(terms, term)
			}
		}
	}
	return terms
}

// replaceAccountNameSearchTerms mirrors replaceAccountNameSearchTerms.
func (s *Store) replaceAccountNameSearchTerms(ctx context.Context, q queryer, accountID, systemAccountID, name, createdAt string) error {
	if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_name_search_terms")+` WHERE account_id = ?`), accountID); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_name_search_documents")+` WHERE account_id = ?`), accountID); err != nil {
		return err
	}
	normalizedName := normalizeAccountNameSearchText(name)
	if normalizedName == "" {
		return nil
	}
	if _, err := q.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_name_search_documents")+`
		(account_id, system_account_id, normalized_name, updated_at) VALUES (?, ?, ?, ?)`),
		accountID, systemAccountID, normalizedName, createdAt); err != nil {
		return err
	}
	for _, term := range buildAccountNameSearchTerms(name) {
		if _, err := q.ExecContext(ctx, s.bind(s.insertIgnore(`INSERT INTO `+s.table("account_name_search_terms")+`
			(account_id, system_account_id, term, created_at) VALUES (?, ?, ?, ?)`,
			` ON CONFLICT (account_id, term) DO NOTHING`)),
			accountID, systemAccountID, term, createdAt); err != nil {
			return err
		}
	}
	return nil
}

// insertIgnore adapts an INSERT statement to the SQLite INSERT OR IGNORE
// shorthand (PostgreSQL receives an explicit ON CONFLICT clause).
func (s *Store) insertIgnore(statement, conflictClause string) string {
	if !s.pg {
		return strings.Replace(statement, "INSERT INTO", "INSERT OR IGNORE INTO", 1)
	}
	return statement + conflictClause
}

// deleteAccountNameSearchTermsForAccounts mirrors
// deleteAccountNameSearchTermsForAccounts.
func (s *Store) deleteAccountNameSearchTermsForAccounts(ctx context.Context, q queryer, accountIDs []string) {
	for _, id := range accountIDs {
		_, _ = q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_name_search_terms")+` WHERE account_id = ?`), id)
		_, _ = q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_name_search_documents")+` WHERE account_id = ?`), id)
	}
}

// ownerEffectiveStatusSQL mirrors the owner branch of
// accountManagementEffectiveStatusSql: expiry/error → disabled, non-active
// statuses surface verbatim, live cooldown → temporary_unavailable, paused
// scheduling → disabled. The comparison instant is inlined as a quoted ISO
// literal exactly like Node (safe: the value comes from the store clock).
func ownerEffectiveStatusSQL(alias, nowLiteral string) string {
	if alias == "" {
		alias = "accounts"
	}
	return `CASE
		WHEN ` + alias + `.last_error_code = 'account_expired'
			OR (` + alias + `.account_expires_at IS NOT NULL AND ` + alias + `.account_expires_at <= ` + nowLiteral + `)
		THEN 'disabled'
		WHEN ` + alias + `.status <> 'active' THEN ` + alias + `.status
		WHEN ` + alias + `.cooldown_until IS NOT NULL AND ` + alias + `.cooldown_until > ` + nowLiteral + ` THEN 'temporary_unavailable'
		WHEN ` + alias + `.schedulable <> 1 THEN 'disabled'
		ELSE ` + alias + `.status
	END`
}

// sqlQuoteISO renders an ISO instant as a single-quoted SQL literal.
func sqlQuoteISO(iso string) string {
	return "'" + strings.ReplaceAll(iso, "'", "''") + "'"
}

// statusRankSQL mirrors the accountManagementListOrderClause status CASE.
func statusRankSQL(effective string) string {
	return `CASE ` + effective + `
		WHEN 'active' THEN 1
		WHEN 'temporary_unavailable' THEN 2
		WHEN 'rate_limited' THEN 3
		WHEN 'pending_test' THEN 4
		WHEN 'quality_isolated' THEN 5
		WHEN 'error' THEN 6
		WHEN 'disabled' THEN 7
		ELSE 8
	END`
}

// accountStatusFilterValues mirrors accountStatusFilterValues: comma
// separated, 'all' dropped, deduplicated.
func accountStatusFilterValues(status string) []string {
	if strings.TrimSpace(status) == "" {
		return nil
	}
	seen := map[string]bool{}
	values := []string{}
	for _, item := range strings.Split(status, ",") {
		text := strings.TrimSpace(item)
		if text == "" || text == "all" || seen[text] {
			continue
		}
		seen[text] = true
		values = append(values, text)
	}
	return values
}

// normalizeTextList mirrors normalizeTextList: trimmed, deduplicated, sorted,
// capped.
func normalizeTextList(values []string, cap int) []string {
	if len(values) == 0 {
		return nil
	}
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
	sortStrings(out)
	if len(out) > cap {
		out = out[:cap]
	}
	return out
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func textPrefixUpperBound(value string) string {
	runes := []rune(value)
	for index := len(runes) - 1; index >= 0; index-- {
		if runes[index] < 0x10ffff {
			runes[index]++
			return string(runes[:index+1])
		}
	}
	return value + "\uffff"
}

// nullPtrString renders NULL/empty SQL text as an omitted JSON field.
func nullPtrString(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	return &value.String
}

// queryer abstracts *sql.DB / *sql.Tx so transactional paths never touch
// s.db while a transaction holds the single SQLite test connection.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}
