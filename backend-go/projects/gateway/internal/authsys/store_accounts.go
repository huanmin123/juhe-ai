// Package authsys owns the Go gateway session/authentication vertical slice:
// auth routes (captcha/login/logout/me/profile/change-password/temporary
// access tokens), session middleware (requireSession/requireAdmin/
// requireSuperAdmin/forceSelfAccessScope), development auto-login, and the
// system-accounts management CRUD. Session/credential primitives are reused
// from businessauth/modelcheckauth, which already implement the Node
// PBKDF2 and session fence contracts.
package authsys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// ConflictError maps to the Node 409 responses.
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// ValidationError maps to Node 409/400 messages that pass through verbatim.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// AccountSummary mirrors Node SystemAccountSummary (domain/types.ts).
type AccountSummary struct {
	ID                     string             `json:"id"`
	Username               string             `json:"username"`
	DisplayName            string             `json:"displayName"`
	Description            *string            `json:"description,omitempty"`
	Role                   string             `json:"role"`
	Status                 string             `json:"status"`
	MustChangePassword     bool               `json:"mustChangePassword"`
	ImageGenerationEnabled bool               `json:"imageGenerationEnabled"`
	AIAccountLimit         *int               `json:"aiAccountLimit,omitempty"`
	RequestLimits          *UserRequestLimits `json:"requestLimits,omitempty"`
	LastLoginAt            *string            `json:"lastLoginAt,omitempty"`
	CreatedAt              string             `json:"createdAt"`
	UpdatedAt              string             `json:"updatedAt"`
}

// UserRequestLimits mirrors parseUserRequestLimitsJson.
type UserRequestLimits struct {
	PerMinute *int    `json:"perMinute,omitempty"`
	PerDay    *int    `json:"perDay,omitempty"`
	PerWeek   *int    `json:"perWeek,omitempty"`
	PerMonth  *int    `json:"perMonth,omitempty"`
	ExpiresOn *string `json:"expiresOn,omitempty"`
}

// AccountListItem mirrors SystemAccountListItem (editVersion = updated_at).
type AccountListItem struct {
	AccountSummary
	CreatedAt   string `json:"-"`
	UpdatedAt   string `json:"-"`
	EditVersion string `json:"editVersion"`
}

// AccountMutationResult mirrors SystemAccountMutationResult.
type AccountMutationResult struct {
	ID                                      string          `json:"id"`
	UpdatedAt                               string          `json:"updatedAt"`
	DisplayName                             *string         `json:"displayName,omitempty"`
	Description                             json.RawMessage `json:"description,omitempty"`
	Role                                    *string         `json:"role,omitempty"`
	Status                                  *string         `json:"status,omitempty"`
	MustChangePassword                      *bool           `json:"mustChangePassword,omitempty"`
	ImageGenerationEnabled                  *bool           `json:"imageGenerationEnabled,omitempty"`
	AIAccountLimit                          json.RawMessage `json:"aiAccountLimit,omitempty"`
	RequestLimits                           json.RawMessage `json:"requestLimits,omitempty"`
	APIKeyValidationCacheInvalidationFailed bool            `json:"apiKeyValidationCacheInvalidationFailed,omitempty"`
}

// AccountStore owns dual-mode system_accounts persistence beyond the session
// primitives that modelcheckauth already provides.
type AccountStore struct {
	db   *sql.DB
	mode modelcheckauth.Mode
	now  func() time.Time
}

func NewAccountStore(db *sql.DB, mode modelcheckauth.Mode, now func() time.Time) (*AccountStore, error) {
	if db == nil || (mode != modelcheckauth.SQLite && mode != modelcheckauth.Postgres) {
		return nil, errors.New("invalid account store")
	}
	if now == nil {
		now = time.Now
	}
	return &AccountStore{db: db, mode: mode, now: now}, nil
}

func ensureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (s *AccountStore) table(name string) string {
	if s.mode == modelcheckauth.Postgres {
		return "juhe_business." + name
	}
	return name
}

func (s *AccountStore) bind(query string) string {
	if s.mode == modelcheckauth.Postgres {
		return replacePlaceholders(query)
	}
	return query
}

func replacePlaceholders(query string) string {
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

var whitespacePattern = regexp.MustCompile(`\s`)

func hasWhitespace(value string) bool { return whitespacePattern.MatchString(value) }

const (
	accountColumns = `id,username,COALESCE(display_name,''),COALESCE(description,''),role,status,must_change_password,image_generation_enabled,ai_account_limit,COALESCE(request_limits_json,''),COALESCE(last_login_at,''),created_at,updated_at`
)

func (s *AccountStore) scanSummary(scanner interface{ Scan(...any) error }) (AccountSummary, error) {
	var a AccountSummary
	var description, requestLimitsJSON, lastLoginAt string
	var mustChange, imageEnabled int
	var aiLimit sql.NullInt64
	if err := scanner.Scan(&a.ID, &a.Username, &a.DisplayName, &description, &a.Role, &a.Status, &mustChange, &imageEnabled, &aiLimit, &requestLimitsJSON, &lastLoginAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return AccountSummary{}, err
	}
	a.MustChangePassword = mustChange == 1 && !IsAdminRole(a.Role)
	a.ImageGenerationEnabled = imageEnabled == 1
	if description != "" {
		a.Description = &description
	}
	if requestLimitsJSON != "" {
		limits := parseUserRequestLimits(requestLimitsJSON)
		if limits != nil {
			a.RequestLimits = limits
		}
	}
	if aiLimit.Valid {
		limit := int(aiLimit.Int64)
		a.AIAccountLimit = &limit
	}
	if lastLoginAt != "" {
		a.LastLoginAt = &lastLoginAt
	}
	return a, nil
}

func parseUserRequestLimits(raw string) *UserRequestLimits {
	var source map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &source); err != nil || source == nil {
		return nil
	}
	limits := UserRequestLimits{}
	for _, window := range []struct {
		key string
		set func(*int)
	}{
		{key: "perMinute", set: func(value *int) { limits.PerMinute = value }},
		{key: "perDay", set: func(value *int) { limits.PerDay = value }},
		{key: "perWeek", set: func(value *int) { limits.PerWeek = value }},
		{key: "perMonth", set: func(value *int) { limits.PerMonth = value }},
	} {
		value, exists := source[window.key]
		if !exists {
			continue
		}
		limit, err := parseLimitSetting(string(value))
		if err != nil {
			return nil
		}
		window.set(&limit)
	}
	if limits.PerMinute == nil && limits.PerDay == nil && limits.PerWeek == nil && limits.PerMonth == nil {
		return nil
	}
	if value, exists := source["expiresOn"]; exists {
		var expiresOn string
		if err := json.Unmarshal(value, &expiresOn); err != nil {
			return nil
		}
		if expiresOn != "" {
			if !validUserRequestLimitExpiresOn(expiresOn) {
				return nil
			}
			limits.ExpiresOn = &expiresOn
		}
	}
	return &limits
}

// IsAdminRole mirrors domain/types.ts isAdminRole.
func IsAdminRole(role string) bool { return role == "super_admin" || role == "admin" }

func (s *AccountStore) findRow(ctx context.Context, where string, args ...any) (AccountSummary, string, error) {
	query := `SELECT ` + accountColumns + `,password_hash FROM ` + s.table("system_accounts") + ` WHERE ` + where + ` LIMIT 1`
	row := s.db.QueryRowContext(ctx, s.bind(query), args...)
	var a AccountSummary
	var description, requestLimitsJSON, lastLoginAt, passwordHash string
	var mustChange, imageEnabled int
	var aiLimit sql.NullInt64
	if err := row.Scan(&a.ID, &a.Username, &a.DisplayName, &description, &a.Role, &a.Status, &mustChange, &imageEnabled, &aiLimit, &requestLimitsJSON, &lastLoginAt, &a.CreatedAt, &a.UpdatedAt, &passwordHash); err != nil {
		return AccountSummary{}, "", err
	}
	a.MustChangePassword = mustChange == 1 && !IsAdminRole(a.Role)
	a.ImageGenerationEnabled = imageEnabled == 1
	if description != "" {
		a.Description = &description
	}
	if requestLimitsJSON != "" {
		if limits := parseUserRequestLimits(requestLimitsJSON); limits != nil {
			a.RequestLimits = limits
		}
	}
	if aiLimit.Valid {
		limit := int(aiLimit.Int64)
		a.AIAccountLimit = &limit
	}
	if lastLoginAt != "" {
		a.LastLoginAt = &lastLoginAt
	}
	return a, passwordHash, nil
}

// UpdatePassword mirrors updateSystemAccountAsync(password,
// mustChangePassword=false): an unconditional write of the new hash (no
// expectedUpdatedAt) plus revocation of other sessions is done by the caller.
func (s *AccountStore) UpdatePassword(ctx context.Context, id, newPassword string) (AccountSummary, error) {
	ctx = ensureCtx(ctx)
	if newPassword == "" {
		return AccountSummary{}, &ValidationError{Message: "登录密码不能为空"}
	}
	if hasWhitespace(newPassword) {
		return AccountSummary{}, &ValidationError{Message: "登录密码不能包含空格"}
	}
	passwordHash, err := modelcheckauth.HashNodePassword(newPassword)
	if err != nil {
		return AccountSummary{}, err
	}
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("system_accounts")+` SET password_hash = ?, must_change_password = 0, updated_at = ? WHERE id = ?`),
		passwordHash, s.now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return AccountSummary{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return AccountSummary{}, nil
	}
	return s.FindByID(ctx, id)
}

// FindByUsername resolves an account by (case-insensitive) username.
func (s *AccountStore) FindByUsername(ctx context.Context, username string) (AccountSummary, error) {
	ctx = ensureCtx(ctx)
	summary, _, err := s.findRow(ctx, "lower(username)=lower(?)", username)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountSummary{}, nil
	}
	return summary, err
}

// FindByID returns the summary for an account id.
func (s *AccountStore) FindByID(ctx context.Context, id string) (AccountSummary, error) {
	ctx = ensureCtx(ctx)
	summary, _, err := s.findRow(ctx, "id = ?", id)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountSummary{}, nil
	}
	return summary, err
}

// ListPage mirrors listSystemAccountsPageAsync: keyword on username/display
// name, ORDER BY updated_at DESC, id DESC, pageSize+1 probe.
func (s *AccountStore) ListPage(ctx context.Context, keyword string, page, pageSize int) (items []AccountListItem, total int64, hasMore bool, err error) {
	ctx = ensureCtx(ctx)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	where := "1=1"
	args := []any{}
	if keyword != "" {
		where = `(lower(username) LIKE ? OR lower(display_name) LIKE ?)`
		pattern := "%" + strings.ToLower(keyword) + "%"
		args = append(args, pattern, pattern)
	}
	if err := s.db.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM `+s.table("system_accounts")+` WHERE `+where), args...).Scan(&total); err != nil {
		return nil, 0, false, err
	}
	query := `SELECT ` + accountColumns + ` FROM ` + s.table("system_accounts") + ` WHERE ` + where + ` ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, s.bind(query), append(args, pageSize+1, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, false, err
	}
	defer rows.Close()
	for rows.Next() {
		summary, scanErr := s.scanSummary(rows)
		if scanErr != nil {
			return nil, 0, false, scanErr
		}
		items = append(items, AccountListItem{AccountSummary: summary, EditVersion: summary.UpdatedAt, UpdatedAt: summary.UpdatedAt, CreatedAt: summary.CreatedAt})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, err
	}
	hasMore = len(items) > pageSize
	if hasMore {
		items = items[:pageSize]
	}
	return items, total, hasMore, nil
}

// AccountOption mirrors SystemAccountOptionSummary.
type AccountOption struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	DisabledReason *string `json:"disabledReason,omitempty"`
}

// ListOptions mirrors listSystemAccountOptionsAsync.
func (s *AccountStore) ListOptions(ctx context.Context, ids []string, keyword string, limit int) ([]AccountOption, error) {
	ctx = ensureCtx(ctx)
	if limit < 1 {
		limit = 50
	}
	if limit > 50 {
		limit = 50
	}
	where := "1=1"
	args := []any{}
	if len(ids) > 0 {
		uniqueIDs := make([]string, 0, minInt(len(ids), 50))
		seen := map[string]struct{}{}
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			uniqueIDs = append(uniqueIDs, id)
			if len(uniqueIDs) == 50 {
				break
			}
		}
		if len(uniqueIDs) > 0 {
			where += ` AND id IN (` + placeholders(len(uniqueIDs)) + `)`
			for _, id := range uniqueIDs {
				args = append(args, id)
			}
		}
	}
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		prefix := escapeLikePrefix(keyword) + "%"
		where += ` AND (lower(username) = lower(?) OR lower(username) LIKE lower(?) ESCAPE '\' OR lower(display_name) = lower(?) OR lower(display_name) LIKE lower(?) ESCAPE '\')`
		args = append(args, keyword, prefix, keyword, prefix)
	}
	query := `SELECT id,display_name,status FROM ` + s.table("system_accounts") + ` WHERE ` + where + ` ORDER BY status ASC, display_name ASC, username ASC, id ASC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, s.bind(query), append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	options := []AccountOption{}
	for rows.Next() {
		var option AccountOption
		var status string
		if err := rows.Scan(&option.ID, &option.Name, &status); err != nil {
			return nil, err
		}
		if status != "active" {
			reason := "account_disabled"
			option.DisabledReason = &reason
		}
		options = append(options, option)
	}
	return options, rows.Err()
}

func escapeLikePrefix(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func sameOptionalString(current, next *string) bool {
	if current == nil || next == nil {
		return current == nil && next == nil
	}
	return *current == *next
}

func sameOptionalInt(current, next *int) bool {
	if current == nil || next == nil {
		return current == nil && next == nil
	}
	return *current == *next
}

func nullableStringValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableIntValue(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func mustMarshalJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("null")
	}
	return encoded
}

func placeholders(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

// CreateInput mirrors the create zod schema.
type CreateInput struct {
	Username               string
	DisplayName            string
	Description            *string
	Password               string
	Role                   string // default user
	Status                 string // default active
	MustChangePassword     *bool
	ImageGenerationEnabled *bool
	AIAccountLimit         *int
	RequestLimits          *UserRequestLimits
}

func newID(prefix string) (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(buf), nil
}

// Create mirrors createSystemAccountAsync validation and side effects
// (default group/route strategy/api key creation remains with the group and
// api-key slices; the account row itself is created here).
func (s *AccountStore) Create(ctx context.Context, input CreateInput) (AccountListItem, error) {
	ctx = ensureCtx(ctx)
	if input.Username == "" {
		return AccountListItem{}, &ValidationError{Message: "用户账户不能为空"}
	}
	if input.DisplayName == "" {
		return AccountListItem{}, &ValidationError{Message: "用户名称不能为空"}
	}
	if hasWhitespace(input.Username) {
		return AccountListItem{}, &ValidationError{Message: "用户账户不能包含空格"}
	}
	if hasWhitespace(input.DisplayName) {
		return AccountListItem{}, &ValidationError{Message: "用户名称不能包含空格"}
	}
	if len(input.Password) < 4 {
		return AccountListItem{}, &ValidationError{Message: "登录密码不能少于 4 个字符"}
	}
	if hasWhitespace(input.Password) {
		return AccountListItem{}, &ValidationError{Message: "登录密码不能包含空格"}
	}
	role := input.Role
	if role == "" {
		role = "user"
	}
	if role != "super_admin" && role != "admin" && role != "user" {
		return AccountListItem{}, &ValidationError{Message: "系统账户角色无效"}
	}
	status := input.Status
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "disabled" {
		return AccountListItem{}, &ValidationError{Message: "系统账户状态无效"}
	}
	mustChange := true
	if input.MustChangePassword != nil {
		mustChange = *input.MustChangePassword
	}
	if mustChange && IsAdminRole(role) {
		mustChange = false
	}
	imageEnabled := false
	if input.ImageGenerationEnabled != nil {
		imageEnabled = *input.ImageGenerationEnabled
	}
	if input.AIAccountLimit != nil && (*input.AIAccountLimit < 0 || *input.AIAccountLimit > 1_000_000) {
		return AccountListItem{}, &ValidationError{Message: "AI 账户上限必须是 0 到 1000000 之间的整数"}
	}
	requestLimitsJSON, err := marshalRequestLimits(input.RequestLimits)
	if err != nil {
		return AccountListItem{}, &ValidationError{Message: err.Error()}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AccountListItem{}, err
	}
	defer tx.Rollback()

	var existing string
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("system_accounts")+` WHERE lower(username)=lower(?) LIMIT 1`), input.Username).Scan(&existing); err == nil {
		return AccountListItem{}, &ConflictError{Message: "用户账户已存在"}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AccountListItem{}, err
	}
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("system_accounts")+` WHERE lower(display_name)=lower(?) LIMIT 1`), input.DisplayName).Scan(&existing); err == nil {
		return AccountListItem{}, &ConflictError{Message: "用户名称已存在"}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AccountListItem{}, err
	}

	id, err := newID("sysacc")
	if err != nil {
		return AccountListItem{}, err
	}
	passwordHash, err := modelcheckauth.HashNodePassword(input.Password)
	if err != nil {
		return AccountListItem{}, err
	}
	nowText := s.now().UTC().Format(time.RFC3339Nano)
	mustChangeInt := boolInt(mustChange)
	imageInt := boolInt(imageEnabled)
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("system_accounts")+` (id,username,display_name,description,role,status,password_hash,must_change_password,image_generation_enabled,ai_account_limit,request_limits_json,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		id, input.Username, input.DisplayName, input.Description, role, status, passwordHash, mustChangeInt, imageInt, input.AIAccountLimit, requestLimitsJSON, nowText, nowText)
	if err != nil {
		return AccountListItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return AccountListItem{}, err
	}
	summary, err := s.FindByID(ctx, id)
	if err != nil {
		return AccountListItem{}, err
	}
	return AccountListItem{AccountSummary: summary, EditVersion: summary.UpdatedAt}, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func marshalRequestLimits(limits *UserRequestLimits) (*string, error) {
	if limits == nil {
		return nil, nil
	}
	normalized := *limits
	for _, window := range []*int{normalized.PerMinute, normalized.PerDay, normalized.PerWeek, normalized.PerMonth} {
		if window != nil && (*window < 0 || *window > 1_000_000_000) {
			return nil, errors.New("用户限制窗口必须是 0 到 1000000000 之间的整数")
		}
	}
	if normalized.PerMinute == nil && normalized.PerDay == nil && normalized.PerWeek == nil && normalized.PerMonth == nil {
		return nil, nil
	}
	if normalized.ExpiresOn != nil {
		if *normalized.ExpiresOn == "" {
			normalized.ExpiresOn = nil
		} else if !validUserRequestLimitExpiresOn(*normalized.ExpiresOn) {
			return nil, errors.New("expiresOn 必须是 YYYY-MM-DD 格式的有效日期")
		}
	}
	encoded, err := json.Marshal(&normalized)
	if err != nil {
		return nil, err
	}
	text := string(encoded)
	return &text, nil
}

// PatchInput mirrors the management patch zod schema (expectedUpdatedAt
// required, at least one mutation field).
type PatchInput struct {
	ExpectedUpdatedAt      string
	DisplayName            *string
	Description            *string
	Password               *string
	Role                   *string
	Status                 *string
	MustChangePassword     *bool
	ImageGenerationEnabled *bool
	AIAccountLimit         *int
	RequestLimits          *UserRequestLimits
	DescriptionPresent     bool
	AIAccountLimitPresent  bool
	RequestLimitsPresent   bool
}

// normalizeRFC3339 mirrors the Node rfc3339 normalization used for the
// optimistic-concurrency comparison.
func normalizeRFC3339(value string) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", &ValidationError{Message: "系统账户编辑版本格式不正确"}
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}

// Patch mirrors patchSystemAccountManagementAsync: FOR UPDATE lock, expected
// updated_at comparison, only-changed-column writes, super-admin invariant,
// forced logout on password/status changes, and the 409 conflict contract.
func (s *AccountStore) Patch(ctx context.Context, id string, input PatchInput) (AccountMutationResult, error) {
	ctx = ensureCtx(ctx)
	expected, err := normalizeRFC3339(input.ExpectedUpdatedAt)
	if err != nil {
		return AccountMutationResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AccountMutationResult{}, err
	}
	defer tx.Rollback()

	var current AccountSummary
	var passwordHash string
	current, passwordHash, err = s.findRowForUpdate(ctx, tx, "id = ?", id)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountMutationResult{}, nil
	}
	if err != nil {
		return AccountMutationResult{}, err
	}
	// Optimistic concurrency: RFC3339-normalized comparison against the
	// caller's observed edit version (Node patchSystemAccountManagementAsync).
	if normalizeRFC3339Text(current.UpdatedAt) != expected {
		return AccountMutationResult{}, &ConflictError{Message: "系统账户已被其他操作修改，请刷新后重试"}
	}

	mutationResult := AccountMutationResult{ID: current.ID, UpdatedAt: current.UpdatedAt}
	changes := map[string]any{}
	if input.DisplayName != nil && *input.DisplayName != current.DisplayName {
		if *input.DisplayName == "" {
			return AccountMutationResult{}, &ValidationError{Message: "用户名称不能为空"}
		}
		if hasWhitespace(*input.DisplayName) {
			return AccountMutationResult{}, &ValidationError{Message: "用户名称不能包含空格"}
		}
		changes["display_name"] = *input.DisplayName
		value := *input.DisplayName
		mutationResult.DisplayName = &value
	}
	if input.DescriptionPresent {
		var description *string
		if input.Description != nil && strings.TrimSpace(*input.Description) != "" {
			value := strings.TrimSpace(*input.Description)
			description = &value
		}
		if !sameOptionalString(current.Description, description) {
			changes["description"] = nullableStringValue(description)
			mutationResult.Description = json.RawMessage(mustMarshalJSON(description))
		}
	}

	passwordChanged := false
	if input.Password != nil {
		if *input.Password == "" {
			return AccountMutationResult{}, &ValidationError{Message: "登录密码不能为空"}
		}
		if hasWhitespace(*input.Password) {
			return AccountMutationResult{}, &ValidationError{Message: "登录密码不能包含空格"}
		}
		passwordHash, err = modelcheckauth.HashNodePassword(*input.Password)
		if err != nil {
			return AccountMutationResult{}, err
		}
		passwordChanged = true
	}
	role := current.Role
	if input.Role != nil {
		if *input.Role != "super_admin" && *input.Role != "admin" && *input.Role != "user" {
			return AccountMutationResult{}, &ValidationError{Message: "系统账户角色无效"}
		}
		if *input.Role != current.Role {
			role = *input.Role
			changes["role"] = *input.Role
			value := *input.Role
			mutationResult.Role = &value
		}
	}
	status := current.Status
	if input.Status != nil {
		if *input.Status != "active" && *input.Status != "disabled" {
			return AccountMutationResult{}, &ValidationError{Message: "系统账户状态无效"}
		}
		if *input.Status != current.Status {
			status = *input.Status
			changes["status"] = *input.Status
			value := *input.Status
			mutationResult.Status = &value
		}
	}
	requestLimitsJSON := ""
	if input.RequestLimitsPresent {
		encoded, err := marshalRequestLimits(input.RequestLimits)
		if err != nil {
			return AccountMutationResult{}, &ValidationError{Message: err.Error()}
		}
		if encoded != nil {
			requestLimitsJSON = *encoded
		}
		currentJSON := ""
		if current.RequestLimits != nil {
			if encodedCurrent, marshalErr := marshalRequestLimits(current.RequestLimits); marshalErr == nil && encodedCurrent != nil {
				currentJSON = *encodedCurrent
			}
		}
		if currentJSON != requestLimitsJSON {
			changes["request_limits_json"] = nullableStringValue(encoded)
			mutationResult.RequestLimits = json.RawMessage(mustMarshalJSON(parseUserRequestLimits(requestLimitsJSON)))
		}
	}

	// Super-admin invariant: leaving no active super_admin is rejected.
	if current.Role == "super_admin" && (role != "super_admin" || status != "active") {
		var others int
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM `+s.table("system_accounts")+` WHERE role='super_admin' AND status='active' AND id <> ?`), id).Scan(&others); err != nil {
			return AccountMutationResult{}, err
		}
		if others == 0 {
			return AccountMutationResult{}, &ValidationError{Message: "至少保留一个启用的超级管理员"}
		}
	}

	if len(changes) == 0 && !passwordChanged {
		return mutationResult, nil
	}

	assignments := []string{"updated_at = ?"}
	args := []any{}
	newUpdatedAt := nowPlusOneMilli(s.now(), current.UpdatedAt)
	args = append(args, newUpdatedAt)
	setIf := func(column string, value any) {
		assignments = append(assignments, column+" = ?")
		args = append(args, value)
	}
	if value, ok := changes["display_name"]; ok {
		setIf("display_name", value)
	}
	if value, ok := changes["description"]; ok {
		setIf("description", value)
	}
	if value, ok := changes["role"]; ok {
		setIf("role", value)
	}
	if value, ok := changes["status"]; ok {
		setIf("status", value)
	}
	mustChange := boolInt(current.MustChangePassword)
	if input.MustChangePassword != nil {
		// Admin roles force mustChangePassword false (Node effective value).
		effective := *input.MustChangePassword && !IsAdminRole(role)
		if effective != current.MustChangePassword {
			mustChange = boolInt(effective)
			setIf("must_change_password", mustChange)
			changes["must_change_password"] = mustChange
			mutationResult.MustChangePassword = &effective
		}
	}
	imageEnabled := boolInt(current.ImageGenerationEnabled)
	if input.ImageGenerationEnabled != nil && *input.ImageGenerationEnabled != current.ImageGenerationEnabled {
		imageEnabled = boolInt(*input.ImageGenerationEnabled)
		setIf("image_generation_enabled", imageEnabled)
		changes["image_generation_enabled"] = imageEnabled
		value := *input.ImageGenerationEnabled
		mutationResult.ImageGenerationEnabled = &value
	}
	if input.AIAccountLimitPresent {
		if input.AIAccountLimit != nil && (*input.AIAccountLimit < 0 || *input.AIAccountLimit > 1_000_000) {
			return AccountMutationResult{}, &ValidationError{Message: "AI 账户上限必须是 0 到 1000000 之间的整数"}
		}
		if !sameOptionalInt(current.AIAccountLimit, input.AIAccountLimit) {
			changes["ai_account_limit"] = nullableIntValue(input.AIAccountLimit)
			mutationResult.AIAccountLimit = json.RawMessage(mustMarshalJSON(input.AIAccountLimit))
		}
	}
	if value, ok := changes["ai_account_limit"]; ok {
		setIf("ai_account_limit", value)
	}
	if value, ok := changes["request_limits_json"]; ok {
		setIf("request_limits_json", value)
	}
	if passwordChanged {
		setIf("password_hash", passwordHash)
	}
	args = append(args, id, current.UpdatedAt)
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("system_accounts")+` SET `+strings.Join(assignments, ", ")+` WHERE id = ? AND updated_at = ?`), args...)
	if err != nil {
		return AccountMutationResult{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return AccountMutationResult{}, err
	}
	if affected != 1 {
		return AccountMutationResult{}, &ConflictError{Message: "系统账户已被其他操作修改，请刷新后重试"}
	}
	if passwordChanged || (input.Status != nil && *input.Status == "disabled" && current.Status != "disabled") {
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("system_sessions")+` WHERE system_account_id = ?`), id); err != nil {
			return AccountMutationResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return AccountMutationResult{}, err
	}
	mutationResult.UpdatedAt = newUpdatedAt
	return mutationResult, nil
}

func (s *AccountStore) findRowForUpdate(ctx context.Context, tx *sql.Tx, where string, args ...any) (AccountSummary, string, error) {
	query := `SELECT ` + accountColumns + `,password_hash FROM ` + s.table("system_accounts") + ` WHERE ` + where + ` LIMIT 1`
	if s.mode == modelcheckauth.Postgres {
		query += " FOR UPDATE"
	}
	row := tx.QueryRowContext(ctx, s.bind(query), args...)
	var a AccountSummary
	var description, requestLimitsJSON, lastLoginAt, passwordHash string
	var mustChange, imageEnabled int
	var aiLimit sql.NullInt64
	if err := row.Scan(&a.ID, &a.Username, &a.DisplayName, &description, &a.Role, &a.Status, &mustChange, &imageEnabled, &aiLimit, &requestLimitsJSON, &lastLoginAt, &a.CreatedAt, &a.UpdatedAt, &passwordHash); err != nil {
		return AccountSummary{}, "", err
	}
	a.MustChangePassword = mustChange == 1 && !IsAdminRole(a.Role)
	a.ImageGenerationEnabled = imageEnabled == 1
	if description != "" {
		a.Description = &description
	}
	if requestLimitsJSON != "" {
		if limits := parseUserRequestLimits(requestLimitsJSON); limits != nil {
			a.RequestLimits = limits
		}
	}
	if aiLimit.Valid {
		limit := int(aiLimit.Int64)
		a.AIAccountLimit = &limit
	}
	return a, passwordHash, nil
}

// nowPlusOneMilli mirrors max(now, previous+1ms) for updated_at.
func nowPlusOneMilli(now time.Time, previousRFC3339 string) string {
	previous, err := time.Parse(time.RFC3339Nano, previousRFC3339)
	if err != nil {
		return now.UTC().Format(time.RFC3339Nano)
	}
	floor := previous.Add(time.Millisecond)
	if now.Before(floor) {
		return floor.UTC().Format(time.RFC3339Nano)
	}
	return now.UTC().Format(time.RFC3339Nano)
}

// HashNodePassword re-exports the Node-compatible password hash for callers
// outside modelcheckauth.
func HashNodePassword(password string) (string, error) {
	return modelcheckauth.HashNodePassword(password)
}

// SecretHash mirrors hashSecret: sha256 hex of the stored password hash text,
// which Node uses as the credentialRevision session fence.
func SecretHash(passwordHash string) string {
	sum := sha256.Sum256([]byte(passwordHash))
	return hex.EncodeToString(sum[:])
}

// normalizeRFC3339Text compares two RFC3339 instants after normalization so
// SQLite text timestamps and caller-supplied instants compare by value.
func normalizeRFC3339Text(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return parsed.UTC().Format(time.RFC3339Nano)
}
