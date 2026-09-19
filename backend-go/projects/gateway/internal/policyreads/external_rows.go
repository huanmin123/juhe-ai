// external_rows.go owns the external_integration_sources row structs, column
// lists, scanners and row mappers shared by the read and write paths.
package policyreads

import (
	"context"
	"database/sql"
	"strings"
)

// ---------------------------------------------------------------------------
// Row mapping.
// ---------------------------------------------------------------------------

type externalSourceRow struct {
	id         string
	name       string
	status     string
	scopesJSON string
	rateLimits sql.NullString
	expiresAt  sql.NullString
	notes      sql.NullString
	lastUsedAt sql.NullString
	createdAt  string
	updatedAt  string
}

const externalSourceColumns = `sources.id, sources.name, sources.status, sources.scopes_json,
	sources.rate_limits_json, sources.expires_at, sources.notes, sources.last_used_at,
	sources.created_at, sources.updated_at`

func scanExternalSourceRow(scan func(...any) error) (externalSourceRow, error) {
	var row externalSourceRow
	err := scan(&row.id, &row.name, &row.status, &row.scopesJSON, &row.rateLimits,
		&row.expiresAt, &row.notes, &row.lastUsedAt, &row.createdAt, &row.updatedAt)
	return row, err
}

func (s *ExternalStore) mapSourceRecord(ctx context.Context, row externalSourceRow) (*ExternalSourceRecord, error) {
	scopes, err := decodeExternalScopes(row.scopesJSON)
	if err != nil {
		return nil, err
	}
	rateLimits, err := decodeExternalRateLimits(row.rateLimits.String)
	if err != nil {
		return nil, err
	}
	return &ExternalSourceRecord{
		ID: row.id, Name: row.name, Status: row.status, Scopes: scopes, RateLimits: rateLimits,
		ExpiresAt: nullPtrString(row.expiresAt), Notes: nullPtrString(row.notes),
		LastUsedAt: nullPtrString(row.lastUsedAt), CreatedAt: row.createdAt, UpdatedAt: row.updatedAt,
		IsBuiltIn: row.id == builtInExternalTestSourceID,
	}, nil
}

func (s *ExternalStore) loadTokensBySourceIDs(ctx context.Context, q queryer, sourceIDs []string) (map[string][]ExternalTokenSummary, error) {
	result := map[string][]ExternalTokenSummary{}
	if len(sourceIDs) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(sourceIDs))
	args := make([]any, 0, len(sourceIDs))
	for i, id := range sourceIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, s.bind(`SELECT `+externalTokenColumns+` FROM `+s.table("external_integration_source_tokens")+`
		WHERE source_ref_id IN (`+strings.Join(placeholders, ",")+`)
		ORDER BY created_at DESC, id DESC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		token, err := scanExternalTokenRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		result[token.sourceRefID] = append(result[token.sourceRefID], s.mapTokenSummary(token))
	}
	return result, rows.Err()
}

func (s *ExternalStore) mapTokenSummary(row externalTokenRow) ExternalTokenSummary {
	scopes, err := decodeExternalScopes(row.scopesJSON)
	if err != nil {
		scopes = []string{}
	}
	return ExternalTokenSummary{
		ID: row.id, Name: row.name, TokenPrefix: row.tokenPrefix, TokenSuffix: row.tokenSuffix,
		Status: row.status, Scopes: scopes, ExpiresAt: nullPtrString(row.expiresAt),
		LastUsedAt: nullPtrString(row.lastUsedAt), CreatedAt: row.createdAt, UpdatedAt: row.updatedAt,
		RevokedAt: nullPtrString(row.revokedAt), IsBuiltIn: row.id == builtInExternalTestTokenID,
	}
}

type externalTokenRow struct {
	id          string
	sourceRefID string
	name        string
	tokenPrefix string
	tokenSuffix string
	status      string
	scopesJSON  string
	expiresAt   sql.NullString
	lastUsedAt  sql.NullString
	createdAt   string
	updatedAt   string
	revokedAt   sql.NullString
}

func scanExternalTokenRow(scan func(...any) error) (externalTokenRow, error) {
	var row externalTokenRow
	err := scan(&row.id, &row.sourceRefID, &row.name, &row.tokenPrefix, &row.tokenSuffix, &row.status,
		&row.scopesJSON, &row.expiresAt, &row.lastUsedAt, &row.createdAt, &row.updatedAt, &row.revokedAt)
	return row, err
}

const externalTokenColumns = `id, source_ref_id, name, token_prefix, token_suffix, status,
	scopes_json, expires_at, last_used_at, created_at, updated_at, revoked_at`
