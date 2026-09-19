// external_query.go owns the external integration source read paths: the
// static scope options, the paged source list and the token-aware lookups.
package policyreads

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
)

// ---------------------------------------------------------------------------
// Reads.
// ---------------------------------------------------------------------------

// Scopes mirrors GET /scopes.
func (s *ExternalStore) Scopes() []ExternalScopeOption {
	out := make([]ExternalScopeOption, len(externalIntegrationScopeOptions))
	copy(out, externalIntegrationScopeOptions)
	return out
}

// ListPage mirrors listExternalIntegrationSourcesAsync.
func (s *ExternalStore) ListPage(ctx context.Context, page *int, pageSize *int, keyword, status string) (*ExternalSourceListResult, error) {
	ctx = ensureCtx(ctx)
	size := externalDefaultPageSize
	if pageSize != nil {
		size = *pageSize
		if size < 1 {
			size = 1
		}
		if size > externalMaxPageSize {
			size = externalMaxPageSize
		}
	}
	currentPage := 1
	if page != nil {
		currentPage = normalizeExternalListPage(*page, size)
	}
	offset := (currentPage - 1) * size
	clauses := []string{}
	args := []any{}
	if status != "" && status != "all" {
		clauses = append(clauses, "sources.status = ?")
		args = append(args, status)
	}
	trimmedKeyword := strings.TrimSpace(keyword)
	if trimmedKeyword != "" {
		if s.pg {
			clauses = append(clauses, "(LOWER(sources.name) = LOWER(?) OR LOWER(sources.name) LIKE LOWER(?) ESCAPE '\\')")
		} else {
			clauses = append(clauses, "(sources.name = ? OR sources.name LIKE ? ESCAPE '\\')")
		}
		pattern := escapeLikePrefix(trimmedKeyword) + "%"
		args = append(args, trimmedKeyword, pattern)
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	queryArgs := append(args, size+1, offset)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+externalSourceColumns+`
		FROM `+s.table("external_integration_sources")+` AS sources
		`+where+`
		ORDER BY sources.updated_at DESC, sources.id DESC
		LIMIT ? OFFSET ?`), queryArgs...)
	if err != nil {
		return nil, err
	}
	pageRows := []externalSourceRow{}
	for rows.Next() {
		row, scanErr := scanExternalSourceRow(rows.Scan)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		pageRows = append(pageRows, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	hasMore := len(pageRows) > size
	if hasMore {
		pageRows = pageRows[:size]
	}
	sourceIDs := make([]string, 0, len(pageRows))
	for _, row := range pageRows {
		sourceIDs = append(sourceIDs, row.id)
	}
	primaryTokens, err := s.loadPrimaryTokensBySourceIDs(ctx, sourceIDs)
	if err != nil {
		return nil, err
	}
	items := make([]ExternalSourceListItem, 0, len(pageRows))
	for _, row := range pageRows {
		scopes, scopesErr := decodeExternalScopes(row.scopesJSON)
		if scopesErr != nil {
			return nil, scopesErr
		}
		rateLimits, rateErr := decodeExternalRateLimits(row.rateLimits.String)
		if rateErr != nil {
			return nil, rateErr
		}
		item := ExternalSourceListItem{
			ID: row.id, Name: row.name, Status: row.status, Scopes: scopes, RateLimits: rateLimits,
			ExpiresAt: nullPtrString(row.expiresAt), Notes: nullPtrString(row.notes),
			LastUsedAt: nullPtrString(row.lastUsedAt), UpdatedAt: row.updatedAt,
			PrimaryToken: primaryTokens[row.id],
			IsBuiltIn:    row.id == builtInExternalTestSourceID,
		}
		items = append(items, item)
	}
	return &ExternalSourceListResult{
		Items:          items,
		Page:           currentPage,
		PageSize:       size,
		PageUpperBound: offset + len(items) + boolToInt(hasMore),
		HasMore:        hasMore,
	}, nil
}

// normalizeExternalListPage mirrors normalizeListPage (default window 1001).
func normalizeExternalListPage(page, pageSize int) int {
	upperBound := (1000 - 1) / pageSize
	if upperBound < 1 {
		upperBound = 1
	}
	if page < 1 {
		return 1
	}
	if page > upperBound {
		return upperBound
	}
	return page
}

// loadPrimaryTokensBySourceIDs mirrors
// loadExternalIntegrationSourcePrimaryTokensBySourceIds.
func (s *ExternalStore) loadPrimaryTokensBySourceIDs(ctx context.Context, sourceIDs []string) (map[string]*ExternalPrimaryToken, error) {
	result := map[string]*ExternalPrimaryToken{}
	unique := uniqueSortedStrings(sourceIDs)
	if len(unique) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(unique))
	args := make([]any, 0, len(unique))
	for i, id := range unique {
		placeholders[i] = "?"
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT
		id, source_ref_id, token_prefix, token_suffix
		FROM (
			SELECT
				tokens.id,
				tokens.source_ref_id,
				tokens.token_prefix,
				tokens.token_suffix,
				tokens.status,
				tokens.created_at,
				ROW_NUMBER() OVER (
					PARTITION BY tokens.source_ref_id
					ORDER BY CASE WHEN tokens.status = 'active' THEN 0 ELSE 1 END ASC, tokens.created_at DESC, tokens.id DESC
				) AS token_rank
			FROM `+s.table("external_integration_source_tokens")+` AS tokens
			WHERE tokens.source_ref_id IN (`+strings.Join(placeholders, ",")+`)
		) ranked_tokens
		WHERE token_rank = 1`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var token ExternalPrimaryToken
		var sourceRefID string
		if err := rows.Scan(&token.ID, &sourceRefID, &token.TokenPrefix, &token.TokenSuffix); err != nil {
			return nil, err
		}
		result[sourceRefID] = &token
	}
	return result, rows.Err()
}

// FindSource mirrors findExternalIntegrationSourceAsync.
func (s *ExternalStore) FindSource(ctx context.Context, id string) (*ExternalSourceSummary, error) {
	ctx = ensureCtx(ctx)
	row, err := scanExternalSourceRow(func(targets ...any) error {
		// Node selects `*, 0 AS token_count, 0 AS active_token_count`; the two
		// literal counts are recomputed from the token rows below, so they are
		// scanned into throwaway targets here.
		return s.db.QueryRowContext(ctx, s.bind(`SELECT `+externalSourceColumns+`,
			0 AS token_count, 0 AS active_token_count
			FROM `+s.table("external_integration_sources")+` AS sources WHERE sources.id = ?`), id).
			Scan(append(targets, new(any), new(any))...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record, err := s.mapSourceRecord(ctx, row)
	if err != nil {
		return nil, err
	}
	tokensBySource, err := s.loadTokensBySourceIDs(ctx, s.db, []string{row.id})
	if err != nil {
		return nil, err
	}
	tokens := tokensBySource[row.id]
	if tokens == nil {
		tokens = []ExternalTokenSummary{}
	}
	activeCount := 0
	for _, token := range tokens {
		if token.Status == "active" {
			activeCount++
		}
	}
	return &ExternalSourceSummary{
		ExternalSourceRecord: *record,
		TokenCount:           len(tokens),
		ActiveTokenCount:     activeCount,
		Tokens:               tokens,
	}, nil
}

// FindTokenSecret mirrors findExternalIntegrationSourceTokenSecretAsync.
func (s *ExternalStore) FindTokenSecret(ctx context.Context, sourceRefID, tokenID string) (*string, error) {
	ctx = ensureCtx(ctx)
	var ciphertext sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT tokens.token_secret_encrypted
		FROM `+s.table("external_integration_source_tokens")+` AS tokens
		JOIN `+s.table("external_integration_sources")+` AS sources ON sources.id = tokens.source_ref_id
		WHERE sources.id = ? AND tokens.id = ?`), sourceRefID, tokenID).Scan(&ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !ciphertext.Valid || ciphertext.String == "" {
		return nil, &ValidationError{Message: "来源系统 Token 密文缺少完整 Token"}
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := apikeys.DecryptJSON(s.CryptoSecret, ciphertext.String, &payload); err != nil {
		return nil, &ValidationError{Message: "来源系统 Token 密文缺少完整 Token"}
	}
	if payload.Token == "" {
		return nil, &ValidationError{Message: "来源系统 Token 密文缺少完整 Token"}
	}
	return &payload.Token, nil
}
