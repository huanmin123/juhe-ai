// external_write.go owns the external integration source write paths: source
// and token inserts, patch updates with optimistic locking, delete and the
// built-in test token reset.
package policyreads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
)

// ---------------------------------------------------------------------------
// Writes.
// ---------------------------------------------------------------------------

// externalSourceInput is the normalized POST/PATCH payload subset shared by
// the source create/update paths.
type externalSourceInput struct {
	Name       any
	Status     any
	Scopes     any
	RateLimits any
	ExpiresAt  any
	Notes      any
}

// buildSourceCreateRow mirrors buildExternalIntegrationSourceCreateRow.
func buildSourceCreateRow(input externalSourceInput, now string, newID func(string) string) (*externalSourceRow, []ExternalRateLimitRule, []string, error) {
	name, err := normalizeExternalName(input.Name, "来源系统名称不能为空")
	if err != nil {
		return nil, nil, nil, err
	}
	status, err := normalizeSourceStatusInput(input.Status)
	if err != nil {
		return nil, nil, nil, err
	}
	scopes, err := normalizeExternalScopes(input.Scopes)
	if err != nil {
		return nil, nil, nil, err
	}
	rateLimits, err := normalizeExternalRateLimits(input.RateLimits)
	if err != nil {
		return nil, nil, nil, err
	}
	expiresAt, err := normalizeExternalNullableISO(input.ExpiresAt)
	if err != nil {
		return nil, nil, nil, err
	}
	notes, err := normalizeExternalNullableText(input.Notes)
	if err != nil {
		return nil, nil, nil, err
	}
	rateLimitsJSON, err := json.Marshal(rateLimits)
	if err != nil {
		return nil, nil, nil, err
	}
	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		return nil, nil, nil, err
	}
	row := &externalSourceRow{
		id:         newID("extsrc"),
		name:       name,
		status:     status,
		scopesJSON: string(scopesJSON),
		rateLimits: sql.NullString{String: string(rateLimitsJSON), Valid: true},
		notes:      ptrToNullString(notes),
		expiresAt:  ptrToNullString(expiresAt),
		createdAt:  now,
		updatedAt:  now,
	}
	return row, rateLimits, scopes, nil
}

func ptrToNullString(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

// ensureSourceNameAvailable mirrors ensureSourceNameAvailable.
func (s *ExternalStore) ensureSourceNameAvailable(ctx context.Context, q queryer, name, currentID string) error {
	var existingID string
	err := q.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("external_integration_sources")+`
		WHERE lower(name) = lower(?) LIMIT 1`), name).Scan(&existingID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if existingID != currentID {
		return &ValidationError{Message: "来源系统名称已存在"}
	}
	return nil
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "UNIQUE constraint failed") || strings.Contains(message, "23505")
}

// insertSource mirrors the create INSERT with the duplicate-name mapping.
func (s *ExternalStore) insertSource(ctx context.Context, tx queryer, row *externalSourceRow) error {
	_, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("external_integration_sources")+`
		(id, name, status, scopes_json, rate_limits_json, expires_at, notes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		row.id, row.name, row.status, row.scopesJSON, row.rateLimits, row.expiresAt, row.notes,
		row.createdAt, row.updatedAt)
	if err != nil && isUniqueConstraintError(err) {
		return &ValidationError{Message: "来源系统名称已存在"}
	}
	return err
}

// insertToken mirrors the token INSERT with the duplicate mapping.
func (s *ExternalStore) insertToken(ctx context.Context, tx queryer, row *externalTokenWriteRow) error {
	_, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("external_integration_source_tokens")+`
		(id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status, scopes_json, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		row.id, row.sourceRefID, row.name, row.tokenHash, row.tokenSecretEncrypted,
		row.tokenPrefix, row.tokenSuffix, row.status, row.scopesJSON, row.expiresAt,
		row.createdAt, row.updatedAt)
	if err != nil && isUniqueConstraintError(err) {
		return &ValidationError{Message: "来源系统 token 已存在，请重新生成"}
	}
	return err
}

// externalTokenWriteRow carries a fresh token INSERT.
type externalTokenWriteRow struct {
	id                   string
	sourceRefID          string
	name                 string
	tokenHash            string
	tokenSecretEncrypted string
	tokenPrefix          string
	tokenSuffix          string
	status               string
	scopesJSON           string
	expiresAt            sql.NullString
	createdAt            string
	updatedAt            string
}

// buildTokenWriteRow mirrors the token value/normalize part of
// createExternalIntegrationSourceToken.
func (s *ExternalStore) buildTokenWriteRow(sourceRefID, name string, status string, scopes []string, expiresAt *string, now string, token string) (*externalTokenWriteRow, error) {
	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		return nil, err
	}
	ciphertext, err := apikeys.EncryptJSON(s.CryptoSecret, map[string]string{"token": token})
	if err != nil {
		return nil, err
	}
	return &externalTokenWriteRow{
		id:                   s.generateID("exttok"),
		sourceRefID:          sourceRefID,
		name:                 name,
		tokenHash:            hashExternalSourceToken(token),
		tokenSecretEncrypted: ciphertext,
		tokenPrefix:          externalTokenSlice(token, 0, 8),
		tokenSuffix:          externalTokenSlice(token, len(token)-8, len(token)),
		status:               status,
		scopesJSON:           string(scopesJSON),
		expiresAt:            ptrToNullString(expiresAt),
		createdAt:            now,
		updatedAt:            now,
	}, nil
}

// externalTokenSlice mirrors JS token.slice(start, end) semantics for the
// prefix/suffix previews.
func externalTokenSlice(token string, start, end int) string {
	runes := []rune(token)
	if start < 0 {
		start = 0
	}
	if end > len(runes) {
		end = len(runes)
	}
	if start >= end {
		return ""
	}
	return string(runes[start:end])
}

// CreateAuthorization mirrors createExternalIntegrationSourceAuthorizationAsync:
// the source row plus its "生产 Token" primary token inside one transaction.
func (s *ExternalStore) CreateAuthorization(ctx context.Context, input externalSourceInput) (*ExternalSourceRecord, *CreatedExternalToken, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	now := s.nowISO()
	row, rateLimits, scopes, err := buildSourceCreateRow(input, now, s.generateID)
	if err != nil {
		return nil, nil, err
	}
	if err := s.ensureSourceNameAvailable(ctx, tx, row.name, ""); err != nil {
		return nil, nil, err
	}
	if err := s.insertSource(ctx, tx, row); err != nil {
		return nil, nil, err
	}
	tokenName := row.name + " 生产 Token"
	tokenValue := createExternalSourceTokenValue()
	tokenRow, err := s.buildTokenWriteRow(row.id, tokenName, row.status, scopes, nullToPtr(row.expiresAt), now, tokenValue)
	if err != nil {
		return nil, nil, err
	}
	if err := s.insertToken(ctx, tx, tokenRow); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	record := &ExternalSourceRecord{
		ID: row.id, Name: row.name, Status: row.status, Scopes: scopes, RateLimits: rateLimits,
		ExpiresAt: nullToPtr(row.expiresAt), Notes: nullToPtr(row.notes),
		CreatedAt: row.createdAt, UpdatedAt: row.updatedAt, IsBuiltIn: false,
	}
	created := &CreatedExternalToken{
		ID: tokenRow.id, Name: tokenRow.name, Token: tokenValue,
		TokenPrefix: tokenRow.tokenPrefix, TokenSuffix: tokenRow.tokenSuffix, Scopes: scopes,
		ExpiresAt: nullToPtr(row.expiresAt),
	}
	return record, created, nil
}

func nullToPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

// resolveSourceForToken mirrors resolveSourceForToken.
func (s *ExternalStore) resolveSourceForToken(ctx context.Context, q queryer, sourceRefID string) (string, error) {
	if strings.TrimSpace(sourceRefID) == "" {
		return "", &ValidationError{Message: "来源系统不存在"}
	}
	var id string
	err := q.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("external_integration_sources")+`
		WHERE id = ?`), sourceRefID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", &ValidationError{Message: "来源系统不存在"}
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// CreateToken mirrors createExternalIntegrationSourceTokenAsync.
func (s *ExternalStore) CreateToken(ctx context.Context, sourceRefID string, input externalTokenInput) (*CreatedExternalToken, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	name, err := normalizeExternalName(input.Name, "来源系统 token 名称不能为空")
	if err == nil && runeLen(name) > 80 {
		err = &ValidationError{Message: "来源系统 token 名称不能超过 80 个字符"}
	}
	if err != nil {
		return nil, err
	}
	sourceID, err := s.resolveSourceForToken(ctx, tx, sourceRefID)
	if err != nil {
		return nil, err
	}
	if sourceID == builtInExternalTestSourceID {
		return nil, &ValidationError{Message: "内置测试 Token 不支持新增 Token"}
	}
	status, err := normalizeTokenStatusInput(input.Status)
	if err != nil {
		return nil, err
	}
	scopes, err := normalizeExternalScopes(input.Scopes)
	if err != nil {
		return nil, err
	}
	expiresAt, err := normalizeExternalNullableISO(input.ExpiresAt)
	if err != nil {
		return nil, err
	}
	now := s.nowISO()
	tokenValue := createExternalSourceTokenValue()
	tokenRow, err := s.buildTokenWriteRow(sourceID, name, status, scopes, expiresAt, now, tokenValue)
	if err != nil {
		return nil, err
	}
	if err := s.insertToken(ctx, tx, tokenRow); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &CreatedExternalToken{
		ID: tokenRow.id, Name: name, Token: tokenValue,
		TokenPrefix: tokenRow.tokenPrefix, TokenSuffix: tokenRow.tokenSuffix,
		Scopes: scopes, ExpiresAt: expiresAt,
	}, nil
}

// externalTokenInput is the zod-validated token payload.
type externalTokenInput struct {
	Name      any
	Status    any
	Scopes    any
	ExpiresAt any
}

// externalSourceUpdateInput is the zod-validated source PATCH payload.
type externalSourceUpdateInput struct {
	ExpectedUpdatedAt string
	Name              any // string when present
	Status            any
	Scopes            any
	RateLimits        any
	ExpiresAt         any
	Notes             any
	SetFields         map[string]bool
}

// UpdateSource mirrors updateExternalIntegrationSourceAsync.
func (s *ExternalStore) UpdateSource(ctx context.Context, id string, input externalSourceUpdateInput) (*ExternalSourcePatchOutcome, error) {
	ctx = ensureCtx(ctx)
	if strings.TrimSpace(input.ExpectedUpdatedAt) == "" {
		return nil, &ValidationError{Message: "外部来源配置版本不能为空"}
	}
	if id == builtInExternalTestSourceID {
		for _, field := range []string{"name", "scopes", "rateLimits", "expiresAt", "notes"} {
			if input.SetFields[field] {
				return nil, &ValidationError{Message: "内置测试 Token 只支持启用或停用，不支持编辑名称、授权范围、限频、到期时间或备注"}
			}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Patch projection: always id/name/updated_at, plus the patched columns.
	projection := "id, name, updated_at"
	if input.SetFields["status"] {
		projection += ", status"
	}
	if input.SetFields["scopes"] {
		projection += ", scopes_json"
	}
	if input.SetFields["rateLimits"] {
		projection += ", rate_limits_json"
	}
	if input.SetFields["expiresAt"] {
		projection += ", expires_at"
	}
	if input.SetFields["notes"] {
		projection += ", notes"
	}
	var existingID, existingName, existingUpdatedAt string
	var statusVal, scopesVal, rateLimitsVal, expiresVal, notesVal sql.NullString
	targets := []any{&existingID, &existingName, &existingUpdatedAt}
	if input.SetFields["status"] {
		targets = append(targets, &statusVal)
	}
	if input.SetFields["scopes"] {
		targets = append(targets, &scopesVal)
	}
	if input.SetFields["rateLimits"] {
		targets = append(targets, &rateLimitsVal)
	}
	if input.SetFields["expiresAt"] {
		targets = append(targets, &expiresVal)
	}
	if input.SetFields["notes"] {
		targets = append(targets, &notesVal)
	}
	err = tx.QueryRowContext(ctx, s.bind(`SELECT `+projection+` FROM `+s.table("external_integration_sources")+`
		WHERE id = ?`), id).Scan(targets...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if existingUpdatedAt != input.ExpectedUpdatedAt {
		return nil, &ConflictError{Message: externalConflictMessage}
	}

	type updateColumn struct {
		column string
		value  any
	}
	columns := []updateColumn{}
	changes := []ExternalPatchChange{}
	sourceName := existingName
	nextStatus := ""
	if input.SetFields["name"] {
		value, err := normalizeExternalName(input.Name, "来源系统名称不能为空")
		if err != nil {
			return nil, err
		}
		sourceName = value
		if value != existingName {
			columns = append(columns, updateColumn{"name", value})
			changes = append(changes, ExternalPatchChange{Field: "name", Before: existingName, After: value})
		}
	}
	if input.SetFields["status"] {
		before, err := normalizeSourceStatus(statusVal.String)
		if err != nil {
			return nil, err
		}
		value, err := normalizeSourceStatusInput(input.Status)
		if err != nil {
			return nil, err
		}
		if value != before {
			columns = append(columns, updateColumn{"status", value})
			changes = append(changes, ExternalPatchChange{Field: "status", Before: before, After: value})
			nextStatus = value
		}
	}
	if input.SetFields["scopes"] {
		value, err := normalizeExternalScopes(input.Scopes)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if string(encoded) != scopesVal.String {
			columns = append(columns, updateColumn{"scopes_json", string(encoded)})
			before, beforeErr := decodeExternalScopes(scopesVal.String)
			if beforeErr != nil {
				return nil, beforeErr
			}
			changes = append(changes, ExternalPatchChange{Field: "scopes", Before: before, After: value})
		}
	}
	if input.SetFields["rateLimits"] {
		value, err := normalizeExternalRateLimits(input.RateLimits)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if string(encoded) != rateLimitsVal.String {
			columns = append(columns, updateColumn{"rate_limits_json", string(encoded)})
			before, beforeErr := decodeExternalRateLimits(rateLimitsVal.String)
			if beforeErr != nil {
				return nil, beforeErr
			}
			changes = append(changes, ExternalPatchChange{Field: "rateLimits", Before: before, After: value})
		}
	}
	if input.SetFields["expiresAt"] {
		value, err := normalizeExternalNullableISO(input.ExpiresAt)
		if err != nil {
			return nil, err
		}
		if nullStringText(value) != expiresVal.String {
			columns = append(columns, updateColumn{"expires_at", value})
			changes = append(changes, ExternalPatchChange{
				Field: "expiresAt", Before: nullToPtr(expiresVal), After: value,
			})
		}
	}
	if input.SetFields["notes"] {
		value, err := normalizeExternalNullableText(input.Notes)
		if err != nil {
			return nil, err
		}
		if nullStringText(value) != notesVal.String {
			columns = append(columns, updateColumn{"notes", value})
			changes = append(changes, ExternalPatchChange{
				Field: "notes", Before: nullToPtr(notesVal), After: value,
			})
		}
	}

	nextUpdatedAt := existingUpdatedAt
	if len(columns) > 0 {
		nameChanged := sourceName != existingName
		if nameChanged {
			if err := s.ensureSourceNameAvailable(ctx, tx, sourceName, id); err != nil {
				return nil, err
			}
		}
		tokenUpdatedAt := ""
		if id != builtInExternalTestSourceID && nextStatus != "" {
			var latest sql.NullString
			err := tx.QueryRowContext(ctx, s.bind(`SELECT updated_at FROM `+s.table("external_integration_source_tokens")+`
				WHERE source_ref_id = ?
				ORDER BY updated_at DESC
				LIMIT 1`), id).Scan(&latest)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if latest.Valid {
				tokenUpdatedAt = latest.String
			}
		}
		base := existingUpdatedAt
		if tokenUpdatedAt > existingUpdatedAt {
			base = tokenUpdatedAt
		}
		updated, err := nextRFC3339Millis(base, s.now(), "外部集成来源 updatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
		if err != nil {
			return nil, err
		}
		nextUpdatedAt = updated
		assignments := make([]string, 0, len(columns))
		values := make([]any, 0, len(columns))
		for _, column := range columns {
			assignments = append(assignments, column.column+" = ?")
			values = append(values, column.value)
		}
		values = append(values, nextUpdatedAt, id, input.ExpectedUpdatedAt)
		result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("external_integration_sources")+`
			SET `+strings.Join(assignments, ", ")+", updated_at = ? WHERE id = ? AND updated_at = ?"), values...)
		if err != nil {
			return nil, err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return nil, &ConflictError{Message: externalConflictMessage}
		}
		if id != builtInExternalTestSourceID && nextStatus != "" {
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("external_integration_source_tokens")+`
				SET status = ?, updated_at = ?
				WHERE source_ref_id = ? AND status <> 'revoked' AND status <> ?`),
				nextStatus, nextUpdatedAt, id, nextStatus); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &ExternalSourcePatchOutcome{
		Mutation:   ExternalMutationResult{ID: existingID, UpdatedAt: nextUpdatedAt},
		SourceName: sourceName,
		Changes:    changes,
	}, nil
}

func nullStringText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// DeleteSource mirrors deleteExternalIntegrationSourceAsync.
func (s *ExternalStore) DeleteSource(ctx context.Context, id, expectedUpdatedAt string) (*ExternalSourceDeleteReceipt, error) {
	ctx = ensureCtx(ctx)
	if id == builtInExternalTestSourceID {
		return nil, &ValidationError{Message: "内置测试 Token 不支持删除"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var sourceID, name, updatedAt string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id, name, updated_at FROM `+s.table("external_integration_sources")+`
		WHERE id = ?`), id).Scan(&sourceID, &name, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if updatedAt != expectedUpdatedAt {
		return nil, &ConflictError{Message: externalConflictMessage}
	}
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("external_integration_source_tokens")+`
		WHERE source_ref_id = ?`), id); err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("external_integration_sources")+`
		WHERE id = ? AND updated_at = ?`), id, expectedUpdatedAt)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, &ConflictError{Message: externalConflictMessage}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &ExternalSourceDeleteReceipt{ID: sourceID, Name: name}, nil
}

// externalTokenUpdateInput is the zod-validated token PATCH payload.
type externalTokenUpdateInput struct {
	ExpectedUpdatedAt string
	Name              any
	Status            any
	Scopes            any
	ExpiresAt         any
	SetFields         map[string]bool
}

// UpdateToken mirrors updateExternalIntegrationSourceTokenAsync.
func (s *ExternalStore) UpdateToken(ctx context.Context, sourceRefID, tokenID string, input externalTokenUpdateInput) (*ExternalTokenPatchOutcome, error) {
	ctx = ensureCtx(ctx)
	if sourceRefID == builtInExternalTestSourceID || tokenID == builtInExternalTestTokenID {
		return nil, &ValidationError{Message: "内置测试 Token 不支持编辑"}
	}
	if strings.TrimSpace(input.ExpectedUpdatedAt) == "" {
		return nil, &ValidationError{Message: "来源系统 token 版本不能为空"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	projection := "tokens.id, tokens.source_ref_id, tokens.name, tokens.updated_at, sources.name AS source_name"
	if input.SetFields["status"] {
		projection += ", tokens.status"
	}
	if input.SetFields["scopes"] {
		projection += ", tokens.scopes_json"
	}
	if input.SetFields["expiresAt"] {
		projection += ", tokens.expires_at"
	}
	var id, refID, existingName, existingUpdatedAt, sourceName string
	var statusVal, scopesVal, expiresVal sql.NullString
	targets := []any{&id, &refID, &existingName, &existingUpdatedAt, &sourceName}
	if input.SetFields["status"] {
		targets = append(targets, &statusVal)
	}
	if input.SetFields["scopes"] {
		targets = append(targets, &scopesVal)
	}
	if input.SetFields["expiresAt"] {
		targets = append(targets, &expiresVal)
	}
	err = tx.QueryRowContext(ctx, s.bind(`SELECT `+projection+`
		FROM `+s.table("external_integration_source_tokens")+` AS tokens
		INNER JOIN `+s.table("external_integration_sources")+` AS sources ON sources.id = tokens.source_ref_id
		WHERE tokens.id = ? AND tokens.source_ref_id = ?`), tokenID, sourceRefID).Scan(targets...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if existingUpdatedAt != input.ExpectedUpdatedAt {
		return nil, &ConflictError{Message: externalConflictMessage}
	}
	nextUpdatedAt, err := nextRFC3339Millis(existingUpdatedAt, s.now(), "外部集成来源 updatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	if err != nil {
		return nil, err
	}

	type tokenColumn struct {
		column string
		value  any
	}
	columns := []tokenColumn{}
	changes := []ExternalPatchChange{}
	if input.SetFields["name"] {
		value, err := normalizeExternalName(input.Name, "来源系统 token 名称不能为空")
		if err == nil && runeLen(value) > 80 {
			err = &ValidationError{Message: "来源系统 token 名称不能超过 80 个字符"}
		}
		if err != nil {
			return nil, err
		}
		if value != existingName {
			columns = append(columns, tokenColumn{"name", value})
			changes = append(changes, ExternalPatchChange{Field: "name", Before: existingName, After: value})
		}
	}
	if input.SetFields["status"] {
		before, err := normalizeTokenStatus(statusVal.String)
		if err != nil {
			return nil, err
		}
		value, err := normalizeTokenStatusInput(input.Status)
		if err != nil {
			return nil, err
		}
		if value != before {
			revokedAt := sql.NullString{}
			if value == "revoked" {
				revokedAt = sql.NullString{String: nextUpdatedAt, Valid: true}
			}
			columns = append(columns, tokenColumn{"status", value}, tokenColumn{"revoked_at", revokedAt})
			changes = append(changes, ExternalPatchChange{Field: "status", Before: before, After: value})
		}
	}
	if input.SetFields["scopes"] {
		value, err := normalizeExternalScopes(input.Scopes)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if string(encoded) != scopesVal.String {
			columns = append(columns, tokenColumn{"scopes_json", string(encoded)})
			before, beforeErr := decodeExternalScopes(scopesVal.String)
			if beforeErr != nil {
				return nil, beforeErr
			}
			changes = append(changes, ExternalPatchChange{Field: "scopes", Before: before, After: value})
		}
	}
	if input.SetFields["expiresAt"] {
		value, err := normalizeExternalNullableISO(input.ExpiresAt)
		if err != nil {
			return nil, err
		}
		if nullStringText(value) != expiresVal.String {
			columns = append(columns, tokenColumn{"expires_at", value})
			changes = append(changes, ExternalPatchChange{
				Field: "expiresAt", Before: nullToPtr(expiresVal), After: value,
			})
		}
	}

	tokenName := existingName
	currentUpdatedAt := existingUpdatedAt
	if len(columns) > 0 {
		assignments := make([]string, 0, len(columns))
		values := make([]any, 0, len(columns))
		for _, column := range columns {
			assignments = append(assignments, column.column+" = ?")
			values = append(values, column.value)
		}
		values = append(values, nextUpdatedAt, tokenID, sourceRefID, input.ExpectedUpdatedAt)
		result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("external_integration_source_tokens")+`
			SET `+strings.Join(assignments, ", ")+", updated_at = ? WHERE id = ? AND source_ref_id = ? AND updated_at = ?"), values...)
		if err != nil {
			return nil, err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return nil, &ConflictError{Message: externalConflictMessage}
		}
		currentUpdatedAt = nextUpdatedAt
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if renamed, ok := input.Name.(string); ok && strings.TrimSpace(renamed) != "" {
		if value, err := normalizeExternalName(renamed, "来源系统 token 名称不能为空"); err == nil {
			tokenName = value
		}
	}
	return &ExternalTokenPatchOutcome{
		Mutation:   ExternalMutationResult{ID: id, UpdatedAt: currentUpdatedAt},
		SourceName: sourceName,
		TokenName:  tokenName,
		Changes:    changes,
	}, nil
}

// ResetBuiltInTestToken mirrors resetBuiltInExternalIntegrationTestTokenAsync.
func (s *ExternalStore) ResetBuiltInTestToken(ctx context.Context) (*CreatedExternalToken, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var sourceScopesJSON string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT scopes_json FROM `+s.table("external_integration_sources")+`
		WHERE id = ?`), builtInExternalTestSourceID).Scan(&sourceScopesJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &ValidationError{Message: "内置测试 Token 不存在"}
	}
	if err != nil {
		return nil, err
	}
	var tokenName string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT name FROM `+s.table("external_integration_source_tokens")+`
		WHERE id = ? AND source_ref_id = ?`), builtInExternalTestTokenID, builtInExternalTestSourceID).Scan(&tokenName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &ValidationError{Message: "内置测试 Token 不存在"}
	}
	if err != nil {
		return nil, err
	}
	token := createExternalSourceTokenValue()
	now := s.nowISO()
	ciphertext, err := apikeys.EncryptJSON(s.CryptoSecret, map[string]string{"token": token})
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("external_integration_source_tokens")+`
		SET token_hash = ?, token_secret_encrypted = ?, token_prefix = ?, token_suffix = ?,
			status = 'active', revoked_at = NULL, updated_at = ?
		WHERE id = ? AND source_ref_id = ?`),
		hashExternalSourceToken(token), ciphertext,
		externalTokenSlice(token, 0, 8), externalTokenSlice(token, len(token)-8, len(token)),
		now, builtInExternalTestTokenID, builtInExternalTestSourceID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("external_integration_sources")+`
		SET updated_at = ? WHERE id = ?`), now, builtInExternalTestSourceID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	scopes, err := decodeExternalScopes(sourceScopesJSON)
	if err != nil {
		return nil, err
	}
	return &CreatedExternalToken{
		ID: builtInExternalTestTokenID, Name: tokenName, Token: token,
		TokenPrefix: externalTokenSlice(token, 0, 8),
		TokenSuffix: externalTokenSlice(token, len(token)-8, len(token)),
		Scopes:      scopes,
	}, nil
}
