package systemaccounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf16"
)

var postgresIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const (
	defaultPageSize    = 20
	maxPageSize        = 100
	listWindowRows     = 1001
	defaultOptionLimit = 50
	maxOptionLimit     = 50
	maxDescriptionLen  = 200
	maxRequestJSONLen  = 64 * 1024
)

type systemAccountRow struct {
	ID                     string
	Username               string
	DisplayName            string
	Description            sql.NullString
	Role                   string
	Status                 string
	PasswordHash           string
	MustChangePassword     any
	ImageGenerationEnabled any
	AIAccountLimit         sql.NullInt64
	RequestLimitsJSON      sql.NullString
	LastLoginAt            sql.NullString
	CreatedAt              string
	UpdatedAt              string
}

// CheckContract only reads pre-existing relations and columns.  Runtime DDL
// is forbidden: a missing relation/column is a deployment contract failure.
func (s *Store) CheckContract(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrOwnerGate
	}
	relations := map[string]string{
		"system_accounts":       `id,username,display_name,description,role,status,password_hash,must_change_password,image_generation_enabled,ai_account_limit,request_limits_json,last_login_at,created_at,updated_at`,
		"system_sessions":       `id,system_account_id,token_hash,expires_at,created_at,last_seen_at`,
		"providers":             `code`,
		"groups":                `id,system_account_id,name,provider_code,description,enabled,is_default,group_type,created_at,updated_at`,
		"route_strategies":      `id,system_account_id,name,description,mode,status,is_default,created_at,updated_at`,
		"route_strategy_groups": `id,route_strategy_id,system_account_id,group_id,priority,weight,status,created_at,updated_at`,
		"api_keys":              `id,system_account_id,route_strategy_id,name,description,key_hash,key_prefix,key_suffix,key_secret_encrypted,status,is_default,purpose,created_at,updated_at`,
	}
	for name, columns := range relations {
		q := "SELECT " + columns + " FROM " + s.table(name) + " LIMIT 0"
		if _, err := s.db.ExecContext(ctx, s.bind(q)); err != nil {
			return fmt.Errorf("%w: verify %s: %v", ErrContract, name, err)
		}
	}
	return nil
}

func (s *Store) requireOwner() error {
	if s == nil || s.db == nil || !s.gate.Ready() {
		return ErrOwnerGate
	}
	return nil
}

func (s *Store) List(ctx context.Context, options ListOptions) (ListResult, error) {
	if err := s.requireOwner(); err != nil {
		return ListResult{}, err
	}
	pageSize := options.PageSize
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	page := options.Page
	if page < 1 {
		page = 1
	}
	maxPage := (listWindowRows - 1) / pageSize
	if maxPage < 1 {
		maxPage = 1
	}
	if page > maxPage {
		page = maxPage
	}
	keyword := strings.TrimSpace(options.Keyword)
	where, args := s.keywordWhere(keyword, "username", "display_name")
	q := `SELECT id,username,display_name,description,role,status,must_change_password,image_generation_enabled,ai_account_limit,request_limits_json,last_login_at,created_at,updated_at
	      FROM ` + s.table("system_accounts") + ` ` + where + `
	      ORDER BY updated_at DESC,id DESC LIMIT ? OFFSET ?`
	args = append(args, pageSize+1, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, s.bind(q), args...)
	if err != nil {
		return ListResult{}, err
	}
	defer rows.Close()
	items := make([]ListItem, 0, pageSize)
	rowCount := 0
	for rows.Next() {
		rowCount++
		row, err := scanSystemAccountPublicRow(rows)
		if err != nil {
			return ListResult{}, err
		}
		if len(items) < pageSize {
			account := row.summary()
			items = append(items, listItemFromAccount(account))
		}
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, err
	}
	hasMore := rowCount > pageSize
	total := (page-1)*pageSize + len(items)
	if hasMore {
		total++
	}
	return ListResult{Items: items, Total: total, HasMore: hasMore, Page: page, PageSize: pageSize}, nil
}

func listItemFromAccount(account SystemAccount) ListItem {
	return ListItem{
		ID: account.ID, Username: account.Username, DisplayName: account.DisplayName,
		Description: account.Description, Role: account.Role, Status: account.Status,
		MustChangePassword: account.MustChangePassword, ImageGenerationEnabled: account.ImageGenerationEnabled,
		AIAccountLimit: account.AIAccountLimit, RequestLimitsJSON: account.RequestLimitsJSON,
		LastLoginAt: account.LastLoginAt, EditVersion: account.UpdatedAt,
	}
}

func (s *Store) Options(ctx context.Context, options OptionListOptions) ([]Option, error) {
	if err := s.requireOwner(); err != nil {
		return nil, err
	}
	limit := options.Limit
	if limit < 1 {
		limit = defaultOptionLimit
	}
	if limit > maxOptionLimit {
		limit = maxOptionLimit
	}
	ids := uniqueStrings(options.IDs)
	clauses := make([]string, 0, 2)
	args := make([]any, 0, len(ids)+3)
	if len(ids) > 0 {
		clauses = append(clauses, "id IN ("+placeholders(len(ids))+")")
		for _, id := range ids {
			args = append(args, id)
		}
	}
	keyword := strings.TrimSpace(options.Keyword)
	if keyword != "" {
		where, keywordArgs := s.keywordWhere(keyword, "username", "display_name")
		clauses = append(clauses, strings.TrimPrefix(strings.TrimSpace(where), "WHERE "))
		args = append(args, keywordArgs...)
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	q := `SELECT id,display_name,status
	      FROM ` + s.table("system_accounts") + ` ` + where + `
	      ORDER BY status ASC,display_name ASC,username ASC,id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.bind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Option, 0, limit)
	for rows.Next() {
		var id, name, status string
		if err := rows.Scan(&id, &name, &status); err != nil {
			return nil, err
		}
		item := Option{ID: id, Name: name}
		if status == "disabled" {
			item.DisabledReason = "account_disabled"
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) Create(ctx context.Context, input CreateInput) (SystemAccount, error) {
	if err := s.requireOwner(); err != nil {
		return SystemAccount{}, err
	}
	normalized, err := normalizeCreate(input)
	if err != nil {
		return SystemAccount{}, err
	}
	if s.cipher == nil {
		return SystemAccount{}, ErrSecretCipher
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SystemAccount{}, err
	}
	defer tx.Rollback()
	now := s.timestamp()
	if err := ensureUniqueCreate(ctx, tx, s, normalized.Username, normalized.DisplayName); err != nil {
		return SystemAccount{}, err
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("system_accounts")+`
      (id,username,display_name,description,role,status,password_hash,must_change_password,image_generation_enabled,ai_account_limit,request_limits_json,created_at,updated_at)
      VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		normalized.ID, normalized.Username, normalized.DisplayName, nullableString(normalized.Description), normalized.Role, normalized.Status,
		normalized.PasswordHash, boolInt(pointerBoolValue(normalized.MustChangePassword)), boolInt(pointerBoolValue(normalized.ImageGenerationEnabled)), nullableInt64(normalized.AIAccountLimit), nullableString(normalized.RequestLimitsJSON), now, now)
	if err != nil {
		return SystemAccount{}, err
	}
	groups, err := s.createDefaultResources(ctx, tx, normalized.ID, now)
	if err != nil {
		return SystemAccount{}, err
	}
	_ = groups // resource IDs are deliberately not part of the management response.
	if err := tx.Commit(); err != nil {
		return SystemAccount{}, err
	}
	return SystemAccount{
		ID: normalized.ID, Username: normalized.Username, DisplayName: normalized.DisplayName,
		Description: normalized.Description, Role: normalized.Role, Status: normalized.Status,
		MustChangePassword: pointerBoolValue(normalized.MustChangePassword), ImageGenerationEnabled: pointerBoolValue(normalized.ImageGenerationEnabled),
		AIAccountLimit: cloneInt64(normalized.AIAccountLimit), RequestLimitsJSON: cloneString(normalized.RequestLimitsJSON),
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (s *Store) PatchCAS(ctx context.Context, id string, patch Patch) (PatchResult, error) {
	if err := s.requireOwner(); err != nil {
		return PatchResult{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" || strings.TrimSpace(patch.ExpectedUpdatedAt) == "" {
		return PatchResult{}, fmt.Errorf("%w: id and expectedUpdatedAt are required", ErrInvalidInput)
	}
	if !patchHasFields(patch) {
		return PatchResult{}, fmt.Errorf("%w: at least one patch field is required", ErrInvalidInput)
	}
	if _, err := parseInstant(patch.ExpectedUpdatedAt); err != nil {
		return PatchResult{}, fmt.Errorf("%w: expectedUpdatedAt: %v", ErrInvalidInput, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PatchResult{}, err
	}
	defer tx.Rollback()
	if patch.Role != nil || patch.Status != nil {
		if err := s.lockSuperAdmins(ctx, tx); err != nil {
			return PatchResult{}, err
		}
	}
	current, err := s.getForUpdate(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return PatchResult{}, ErrNotFound
	}
	if err != nil {
		return PatchResult{}, err
	}
	if !sameInstant(current.UpdatedAt, patch.ExpectedUpdatedAt) {
		return PatchResult{}, fmt.Errorf("%w: expected %s actual %s", ErrCAS, patch.ExpectedUpdatedAt, current.UpdatedAt)
	}
	next, changes, assignments, args, err := s.patchValues(current, patch)
	if err != nil {
		return PatchResult{}, err
	}
	revokeSessions := patchRevokesSessions(changes)
	if (patch.Role != nil || patch.Status != nil) && current.Role == "super_admin" && !(next.Role == "super_admin" && next.Status == "active") {
		var count int
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM `+s.table("system_accounts")+` WHERE id<>? AND role='super_admin' AND status='active'`), id).Scan(&count); err != nil {
			return PatchResult{}, err
		}
		if count < 1 {
			return PatchResult{}, ErrLastSuperAdmin
		}
	}
	if patch.DisplayName != nil {
		if err := ensureUniqueDisplayName(ctx, tx, s, next.DisplayName, id); err != nil {
			return PatchResult{}, err
		}
	}
	revoked := int64(0)
	if len(assignments) > 0 {
		updatedAt := s.nextTimestamp(current.UpdatedAt)
		assignments = append(assignments, "updated_at=?")
		args = append(args, updatedAt, id, current.UpdatedAt)
		q := `UPDATE ` + s.table("system_accounts") + ` SET ` + strings.Join(assignments, ",") + ` WHERE id=? AND updated_at=?`
		result, err := tx.ExecContext(ctx, s.bind(q), args...)
		if err != nil {
			return PatchResult{}, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return PatchResult{}, err
		}
		if n != 1 {
			return PatchResult{}, ErrCAS
		}
		next.UpdatedAt = updatedAt
	}
	if revokeSessions {
		result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("system_sessions")+` WHERE system_account_id=?`), id)
		if err != nil {
			return PatchResult{}, err
		}
		revoked, err = result.RowsAffected()
		if err != nil {
			return PatchResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return PatchResult{}, err
	}
	kind := "no_op"
	if len(changes) > 0 || revoked > 0 {
		kind = "updated"
	}
	return PatchResult{Kind: kind, Account: next, Changes: changes, RevokedSessionCount: revoked}, nil
}

func patchRevokesSessions(changes []Change) bool {
	for _, change := range changes {
		if change.Field == "password" || (change.Field == "status" && change.After == "disabled") {
			return true
		}
	}
	return false
}

func patchHasFields(patch Patch) bool {
	return patch.DisplayName != nil || patch.Description != nil || patch.PasswordHash != nil ||
		patch.Role != nil || patch.Status != nil || patch.MustChangePassword != nil ||
		patch.ImageGenerationEnabled != nil || patch.AIAccountLimit != nil ||
		patch.RequestLimitsJSON != nil
}

func (s *Store) patchValues(current systemAccountRow, patch Patch) (SystemAccount, []Change, []string, []any, error) {
	next := current.summary()
	assignments := make([]string, 0, 9)
	args := make([]any, 0, 10)
	changes := make([]Change, 0, 8)
	if patch.DisplayName != nil {
		value, err := normalizeRequiredText(*patch.DisplayName, "displayName")
		if err != nil {
			return SystemAccount{}, nil, nil, nil, err
		}
		if value != next.DisplayName {
			changes = append(changes, Change{Field: "displayName", Before: next.DisplayName, After: value})
			assignments = append(assignments, "display_name=?")
			args = append(args, value)
			next.DisplayName = value
		}
	}
	if patch.Description != nil {
		value, err := normalizeNullableText(*patch.Description, "description")
		if err != nil {
			return SystemAccount{}, nil, nil, nil, err
		}
		if !sameOptionalString(next.Description, value) {
			changes = append(changes, Change{Field: "description", Before: optionalValue(next.Description), After: optionalValue(value)})
			assignments = append(assignments, "description=?")
			args = append(args, nullableString(value))
			next.Description = value
		}
	}
	if patch.Role != nil {
		if *patch.Role == "super_admin" {
			return SystemAccount{}, nil, nil, nil, fmt.Errorf("%w: super_admin cannot be assigned by management patch", ErrInvalidInput)
		}
		value, err := normalizeRole(*patch.Role, "")
		if err != nil {
			return SystemAccount{}, nil, nil, nil, err
		}
		if value != next.Role {
			changes = append(changes, Change{Field: "role", Before: next.Role, After: value})
			assignments = append(assignments, "role=?")
			args = append(args, value)
			next.Role = value
		}
	}
	if patch.Status != nil {
		value := *patch.Status
		if value != "active" && value != "disabled" {
			return SystemAccount{}, nil, nil, nil, fmt.Errorf("%w: invalid status", ErrInvalidInput)
		}
		if value != next.Status {
			changes = append(changes, Change{Field: "status", Before: next.Status, After: value})
			assignments = append(assignments, "status=?")
			args = append(args, value)
			next.Status = value
		}
	}
	if patch.MustChangePassword != nil || patch.Role != nil {
		value := next.MustChangePassword
		if patch.MustChangePassword != nil {
			value = *patch.MustChangePassword
		}
		if next.Role == "admin" || next.Role == "super_admin" {
			value = false
		}
		if value != next.MustChangePassword {
			changes = append(changes, Change{Field: "mustChangePassword", Before: next.MustChangePassword, After: value})
			assignments = append(assignments, "must_change_password=?")
			args = append(args, boolInt(value))
			next.MustChangePassword = value
		}
	}
	if patch.ImageGenerationEnabled != nil && *patch.ImageGenerationEnabled != next.ImageGenerationEnabled {
		value := *patch.ImageGenerationEnabled
		changes = append(changes, Change{Field: "imageGenerationEnabled", Before: next.ImageGenerationEnabled, After: value})
		assignments = append(assignments, "image_generation_enabled=?")
		args = append(args, boolInt(value))
		next.ImageGenerationEnabled = value
	}
	if patch.AIAccountLimit != nil {
		value, err := normalizeAILimit(*patch.AIAccountLimit)
		if err != nil {
			return SystemAccount{}, nil, nil, nil, err
		}
		if !sameOptionalInt64(next.AIAccountLimit, value) {
			changes = append(changes, Change{Field: "aiAccountLimit", Before: optionalValue(next.AIAccountLimit), After: optionalValue(value)})
			assignments = append(assignments, "ai_account_limit=?")
			args = append(args, nullableInt64(value))
			next.AIAccountLimit = value
		}
	}
	if patch.RequestLimitsJSON != nil {
		value, err := normalizeJSONPointer(*patch.RequestLimitsJSON)
		if err != nil {
			return SystemAccount{}, nil, nil, nil, err
		}
		if !sameOptionalString(next.RequestLimitsJSON, value) {
			changes = append(changes, Change{Field: "requestLimits", Before: optionalValue(next.RequestLimitsJSON), After: optionalValue(value)})
			assignments = append(assignments, "request_limits_json=?")
			args = append(args, nullableString(value))
			next.RequestLimitsJSON = value
		}
	}
	if patch.PasswordHash != nil {
		if strings.TrimSpace(*patch.PasswordHash) == "" {
			return SystemAccount{}, nil, nil, nil, fmt.Errorf("%w: password hash is required", ErrInvalidInput)
		}
		if *patch.PasswordHash != current.PasswordHash {
			assignments = append(assignments, "password_hash=?")
			args = append(args, *patch.PasswordHash)
			changes = append(changes, Change{Field: "password", Before: "未设置", After: "已变更"})
		}
	}
	return next, changes, assignments, args, nil
}

func (s *Store) getForUpdate(ctx context.Context, tx *sql.Tx, id string) (systemAccountRow, error) {
	lock := ""
	if s.mode == Postgres {
		lock = " FOR UPDATE"
	}
	q := `SELECT id,username,display_name,description,role,status,password_hash,must_change_password,image_generation_enabled,ai_account_limit,request_limits_json,last_login_at,created_at,updated_at
	      FROM ` + s.table("system_accounts") + ` WHERE id=? LIMIT 1` + lock
	return scanSystemAccountRow(tx.QueryRowContext(ctx, s.bind(q), id))
}

func (s *Store) lockSuperAdmins(ctx context.Context, tx *sql.Tx) error {
	return s.advisoryLock(ctx, tx, "juhe-ai:system-accounts:active-super-admin")
}

// PostgreSQL advisory locks serialize the two cross-row invariants. SQLite's
// single-writer transaction already supplies the equivalent serialization.
func (s *Store) advisoryLock(ctx context.Context, tx *sql.Tx, key string) error {
	if s.mode != Postgres {
		return nil
	}
	_, err := tx.ExecContext(ctx, s.bind("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))"), key)
	return err
}

func (s *Store) createDefaultResources(ctx context.Context, tx *sql.Tx, accountID, now string) ([]string, error) {
	type groupSeed struct {
		name, provider, description string
	}
	seeds := []groupSeed{
		{"默认 OpenAI 兼容分组", "openai", ""},
		{"默认 GPT 分组", "gpt", ""},
		{"默认 xAI 分组", "xai", ""},
		{"默认 DeepSeek 分组", "deepseek", ""},
		{"默认 Anthropic 分组", "anthropic", ""},
		{"默认 Gemini 分组", "gemini", ""},
		{"默认 GLM 分组", "glm", ""},
		{"默认混合供应商分组", "hybrid", "混合供应商账户保存真实上游凭据和 Base URL，允许账户内配置跨协议入口映射"},
	}
	groupIDs := make(map[string]string, len(seeds))
	for _, seed := range seeds {
		id := newID("grp")
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("groups")+`
          (id,system_account_id,name,provider_code,description,enabled,is_default,group_type,created_at,updated_at)
          VALUES (?,?,?,?,?,1,1,'personal',?,?)`), id, accountID, seed.name, seed.provider, nullableString(normalizeDescriptionValue(seed.description)), now, now); err != nil {
			return nil, err
		}
		groupIDs[seed.provider] = id
	}
	routeIDs := make(map[string]string, len(seeds)-1)
	for _, seed := range seeds {
		if seed.provider == "hybrid" {
			continue
		}
		groupID := groupIDs[seed.provider]
		routeID := newID("route_strategy")
		name := strings.Replace(seed.name, "分组", "路由", 1)
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("route_strategies")+`
          (id,system_account_id,name,description,mode,status,is_default,created_at,updated_at)
          VALUES (?,?,?,?, 'normal','active',1,?,?)`), routeID, accountID, name, "系统默认普通路由，绑定"+seed.name+"。", now, now); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("route_strategy_groups")+`
          (id,route_strategy_id,system_account_id,group_id,priority,weight,status,created_at,updated_at)
          VALUES (?,?,?,?,1,1,'active',?,?)`), newID("rsg"), routeID, accountID, groupID, now, now); err != nil {
			return nil, err
		}
		routeIDs[seed.provider] = routeID
		if err := s.createDefaultAPIKey(ctx, tx, accountID, routeID, name, now); err != nil {
			return nil, err
		}
	}
	gptRoute := routeIDs["gpt"]
	if gptRoute == "" {
		return nil, errors.New("default GPT route is missing")
	}
	if err := s.createChatAPIKey(ctx, tx, accountID, gptRoute, now); err != nil {
		return nil, err
	}
	return routeIDsInSeedOrder(routeIDs), nil
}

func (s *Store) createDefaultAPIKey(ctx context.Context, tx *sql.Tx, accountID, routeID, routeName, now string) error {
	return s.createAPIKey(ctx, tx, accountID, routeID, strings.Replace(routeName, "路由", "API Key", 1), "系统默认 API Key，绑定"+routeName+"。", "general", 1, now)
}

func (s *Store) createChatAPIKey(ctx context.Context, tx *sql.Tx, accountID, routeID, now string) error {
	return s.createAPIKey(ctx, tx, accountID, routeID, "AI 对话 API Key", "AI 对话专用 API Key，默认绑定默认 GPT 路由，可在 API Key 页面修改策略路由。", "chat", 0, now)
}

func (s *Store) createAPIKey(ctx context.Context, tx *sql.Tx, accountID, routeID, name, description, purpose string, isDefault int, now string) error {
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return err
	}
	secret := "sk-" + hex.EncodeToString(keyBytes)
	encrypted, err := s.cipher.Encrypt(ctx, []byte(secret))
	if err != nil {
		return fmt.Errorf("encrypt default API key: %w", err)
	}
	if strings.TrimSpace(encrypted) == "" {
		return errors.New("encrypt default API key: cipher returned empty ciphertext")
	}
	hash := sha256.Sum256([]byte(secret))
	hexHash := hex.EncodeToString(hash[:])
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("api_keys")+`
      (id,system_account_id,route_strategy_id,name,description,key_hash,key_prefix,key_suffix,key_secret_encrypted,status,is_default,purpose,created_at,updated_at)
      VALUES (?,?,?,?,?,?,?,?,?,'active',?,?,?,?)`),
		newID("key"), accountID, routeID, name, description, hexHash, secret[:8], secret[len(secret)-8:], encrypted, isDefault, purpose, now, now)
	return err
}

func ensureUniqueCreate(ctx context.Context, tx *sql.Tx, s *Store, username, displayName string) error {
	var id string
	err := tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("system_accounts")+` WHERE lower(username)=lower(?) LIMIT 1`), username).Scan(&id)
	if err == nil {
		return errors.New("username already exists")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("system_accounts")+` WHERE lower(display_name)=lower(?) LIMIT 1`), displayName).Scan(&id)
	if err == nil {
		return errors.New("display name already exists")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func ensureUniqueDisplayName(ctx context.Context, tx *sql.Tx, s *Store, displayName, excludeID string) error {
	var id string
	err := tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("system_accounts")+` WHERE lower(display_name)=lower(?) AND id<>? LIMIT 1`), displayName, excludeID).Scan(&id)
	if err == nil {
		return errors.New("display name already exists")
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func (s *Store) keywordWhere(keyword string, fields ...string) (string, []any) {
	if keyword == "" {
		return "", nil
	}
	prefix := escapeLikePrefix(keyword) + "%"
	parts := make([]string, 0, len(fields)*2)
	args := make([]any, 0, len(fields)*2)
	for _, field := range fields {
		if s.mode == Postgres {
			parts = append(parts, "lower("+field+")=lower(?)", "lower("+field+") LIKE lower(?) ESCAPE '\\'")
		} else {
			parts = append(parts, field+" COLLATE NOCASE = ?", field+" LIKE ? ESCAPE '\\'")
		}
		args = append(args, keyword, prefix)
	}
	return "WHERE (" + strings.Join(parts, " OR ") + ")", args
}

func (s *Store) table(name string) string {
	if s.mode == Postgres {
		return s.schema + "." + name
	}
	return name
}

func (s *Store) bind(query string) string {
	if s.mode != Postgres {
		return query
	}
	var b strings.Builder
	position := 1
	for _, r := range query {
		if r == '?' {
			fmt.Fprintf(&b, "$%d", position)
			position++
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (s *Store) timestamp() string {
	return s.now().UTC().Format(time.RFC3339Nano)
}

func (s *Store) nextTimestamp(current string) string {
	currentTime, err := parseInstant(current)
	if err != nil {
		return s.timestamp()
	}
	now := s.now().UTC()
	if !now.After(currentTime) {
		now = currentTime.Add(time.Millisecond)
	}
	return now.Format(time.RFC3339Nano)
}

func scanSystemAccountRow(scanner interface{ Scan(...any) error }) (systemAccountRow, error) {
	var row systemAccountRow
	err := scanner.Scan(&row.ID, &row.Username, &row.DisplayName, &row.Description, &row.Role, &row.Status, &row.PasswordHash, &row.MustChangePassword, &row.ImageGenerationEnabled, &row.AIAccountLimit, &row.RequestLimitsJSON, &row.LastLoginAt, &row.CreatedAt, &row.UpdatedAt)
	return row, err
}

func scanSystemAccountPublicRow(scanner interface{ Scan(...any) error }) (systemAccountRow, error) {
	var row systemAccountRow
	err := scanner.Scan(&row.ID, &row.Username, &row.DisplayName, &row.Description, &row.Role, &row.Status, &row.MustChangePassword, &row.ImageGenerationEnabled, &row.AIAccountLimit, &row.RequestLimitsJSON, &row.LastLoginAt, &row.CreatedAt, &row.UpdatedAt)
	return row, err
}

func (r systemAccountRow) summary() SystemAccount {
	return SystemAccount{
		ID: r.ID, Username: r.Username, DisplayName: r.DisplayName, Description: nullString(r.Description), Role: r.Role, Status: r.Status,
		MustChangePassword:     boolValue(r.MustChangePassword) && r.Role != "admin" && r.Role != "super_admin",
		ImageGenerationEnabled: boolValue(r.ImageGenerationEnabled), AIAccountLimit: nullInt64(r.AIAccountLimit), RequestLimitsJSON: nullString(r.RequestLimitsJSON), LastLoginAt: nullString(r.LastLoginAt), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func normalizeCreate(input CreateInput) (CreateInput, error) {
	var err error
	input.ID = strings.TrimSpace(input.ID)
	if input.ID == "" {
		input.ID = newID("sysacc")
	}
	if input.PasswordHash = strings.TrimSpace(input.PasswordHash); input.PasswordHash == "" {
		return CreateInput{}, fmt.Errorf("%w: password hash is required", ErrInvalidInput)
	}
	if input.Username, err = normalizeRequiredText(input.Username, "username"); err != nil {
		return CreateInput{}, err
	}
	if utf16Length(input.Username) < 2 {
		return CreateInput{}, fmt.Errorf("%w: username is too short", ErrInvalidInput)
	}
	if input.DisplayName, err = normalizeRequiredText(input.DisplayName, "displayName"); err != nil {
		return CreateInput{}, err
	}
	if input.Description, err = normalizeNullableText(input.Description, "description"); err != nil {
		return CreateInput{}, err
	}
	input.Role, err = normalizeRole(input.Role, "user")
	if err != nil {
		return CreateInput{}, err
	}
	if input.Role == "super_admin" {
		return CreateInput{}, fmt.Errorf("%w: super_admin cannot be created by management API", ErrInvalidInput)
	}
	if input.Status == "" {
		input.Status = "active"
	}
	if input.Status != "active" && input.Status != "disabled" {
		return CreateInput{}, fmt.Errorf("%w: invalid status", ErrInvalidInput)
	}
	if input.MustChangePassword == nil {
		value := input.Role == "user"
		input.MustChangePassword = &value
	}
	if input.Role == "admin" || input.Role == "super_admin" {
		value := false
		input.MustChangePassword = &value
	}
	if input.ImageGenerationEnabled == nil {
		value := false
		input.ImageGenerationEnabled = &value
	}
	if input.AIAccountLimit, err = normalizeAILimit(input.AIAccountLimit); err != nil {
		return CreateInput{}, err
	}
	if input.RequestLimitsJSON, err = normalizeJSONPointer(input.RequestLimitsJSON); err != nil {
		return CreateInput{}, err
	}
	return input, nil
}

func normalizeRole(value, fallback string) (string, error) {
	if value == "" {
		value = fallback
	}
	if value != "admin" && value != "user" && value != "super_admin" {
		return "", fmt.Errorf("%w: invalid role", ErrInvalidInput)
	}
	return value, nil
}

func normalizeRequiredText(value, field string) (string, error) {
	trimmed := trimNodeSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("%w: %s is required", ErrInvalidInput, field)
	}
	for _, r := range trimmed {
		if unicode.IsSpace(r) || r == '\uFEFF' {
			return "", fmt.Errorf("%w: %s cannot contain whitespace", ErrInvalidInput, field)
		}
	}
	if value != trimmed {
		return "", fmt.Errorf("%w: %s cannot contain whitespace", ErrInvalidInput, field)
	}
	return trimmed, nil
}

func normalizeNullableText(value *string, field string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := trimNodeSpace(*value)
	if utf16Length(trimmed) > maxDescriptionLen {
		return nil, fmt.Errorf("%w: %s is too long", ErrInvalidInput, field)
	}
	if trimmed == "" {
		return nil, nil
	}
	return &trimmed, nil
}

func utf16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}

func trimNodeSpace(value string) string {
	return strings.TrimFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || r == '\uFEFF'
	})
}

func normalizeDescriptionValue(value string) *string {
	return &value
}

func normalizeJSONPointer(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	if len(trimmed) > maxRequestJSONLen || !json.Valid([]byte(trimmed)) {
		return nil, fmt.Errorf("%w: requestLimitsJSON must be valid bounded JSON", ErrInvalidInput)
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil || object == nil {
		return nil, fmt.Errorf("%w: requestLimitsJSON must be a JSON object", ErrInvalidInput)
	}
	allowed := map[string]struct{}{"perMinute": {}, "perDay": {}, "perWeek": {}, "perMonth": {}, "expiresOn": {}}
	var normalized struct {
		PerMinute *int64  `json:"perMinute,omitempty"`
		PerDay    *int64  `json:"perDay,omitempty"`
		PerWeek   *int64  `json:"perWeek,omitempty"`
		PerMonth  *int64  `json:"perMonth,omitempty"`
		ExpiresOn *string `json:"expiresOn,omitempty"`
	}
	for key, value := range object {
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("%w: requestLimitsJSON contains unknown field %q", ErrInvalidInput, key)
		}
		switch key {
		case "perMinute", "perDay", "perWeek", "perMonth":
			number, ok := value.(float64)
			if !ok || number != float64(int64(number)) || number < 0 || number > 1_000_000_000 {
				return nil, fmt.Errorf("%w: %s is out of range", ErrInvalidInput, key)
			}
			parsed := int64(number)
			switch key {
			case "perMinute":
				normalized.PerMinute = &parsed
			case "perDay":
				normalized.PerDay = &parsed
			case "perWeek":
				normalized.PerWeek = &parsed
			case "perMonth":
				normalized.PerMonth = &parsed
			}
		case "expiresOn":
			date, ok := value.(string)
			if !ok || !validDate(date) {
				return nil, fmt.Errorf("%w: expiresOn is invalid", ErrInvalidInput)
			}
			normalized.ExpiresOn = &date
		}
	}
	if len(object) == 0 || noLimitWindow(object) {
		return nil, nil
	}
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("%w: requestLimitsJSON cannot be encoded", ErrInvalidInput)
	}
	result := string(canonical)
	return &result, nil
}

func noLimitWindow(object map[string]any) bool {
	for _, key := range []string{"perMinute", "perDay", "perWeek", "perMonth"} {
		if _, ok := object[key]; ok {
			return false
		}
	}
	return true
}

func validDate(value string) bool {
	if len(value) != len("2006-01-02") {
		return false
	}
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

func normalizeAILimit(value *int64) (*int64, error) {
	if value == nil {
		return nil, nil
	}
	if *value < 0 || *value > 1_000_000 {
		return nil, fmt.Errorf("%w: aiAccountLimit is out of range", ErrInvalidInput)
	}
	return cloneInt64(value), nil
}

func parseInstant(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, trimNodeSpace(value))
}

func sameInstant(left, right string) bool {
	a, errA := parseInstant(left)
	b, errB := parseInstant(right)
	return errA == nil && errB == nil && a.Equal(b)
}

func boolValue(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case int64:
		return v != 0
	case int:
		return v != 0
	case []byte:
		return string(v) == "1" || strings.EqualFold(string(v), "true")
	case string:
		return v == "1" || strings.EqualFold(v, "true")
	default:
		return false
	}
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func optionalValue[T any](value *T) any {
	if value == nil {
		return nil
	}
	return *value
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func pointerBoolValue(value *bool) bool {
	return value != nil && *value
}

func escapeLikePrefix(value string) string {
	return strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(value)
}

func placeholders(count int) string {
	if count <= 0 {
		return "NULL"
	}
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	if len(out) > maxOptionLimit {
		out = out[:maxOptionLimit]
	}
	return out
}

var idSequence atomic.Uint64

func newID(prefix string) string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano(), idSequence.Add(1))
	}
	return prefix + "_" + hex.EncodeToString(random[:]) + fmt.Sprintf("_%d", idSequence.Add(1))
}

func routeIDsInSeedOrder(values map[string]string) []string {
	order := []string{"openai", "gpt", "xai", "deepseek", "anthropic", "gemini", "glm"}
	out := make([]string, 0, len(values))
	for _, provider := range order {
		if id := values[provider]; id != "" {
			out = append(out, id)
		}
	}
	return out
}
