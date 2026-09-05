package authz

// Store reads for the authorization-options family (Node
// storage/authorization-options.repository.ts). The queries are scope-free
// mirrors: grantee accounts and teams list active-first principals, grantee
// groups list enabled groups owned by the caller's namespace that the grantee
// account can see.

import (
	"context"
	"strings"
)

// systemAccountPrincipalSummary mirrors SystemAccountPrincipalSummary.
type systemAccountPrincipalSummary struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Status      string `json:"status"`
}

// systemTeamPrincipalSummary mirrors SystemTeamPrincipalSummary.
type systemTeamPrincipalSummary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// granteeGroupOptionSummary mirrors AuthorizationGranteeGroupOptionSummary.
type granteeGroupOptionSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListAuthorizationGranteeAccounts mirrors listAuthorizationGranteeAccounts.
func (s *Store) ListAuthorizationGranteeAccounts(ctx context.Context, options authorizationPrincipalOptionListOptions) ([]systemAccountPrincipalSummary, error) {
	clauses, params := principalFilter(options, func(keyword string) (string, []any) {
		text := strings.TrimSpace(keyword)
		if text == "" {
			return "", nil
		}
		clause := `(
			(username >= ? AND username < ?)
			OR (display_name >= ? AND display_name < ?)
		)`
		return clause, []any{text, textPrefixUpperBound(text), text, textPrefixUpperBound(text)}
	})
	query := `
		SELECT id, username, display_name, status
		FROM ` + s.table("system_accounts") + `
		` + clauses + `
		ORDER BY status ASC, display_name ASC, username ASC, id ASC
		LIMIT ?`
	params = append(params, options.Limit)
	rows, err := s.db.QueryContext(ctx, s.bind(query), params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := []systemAccountPrincipalSummary{}
	for rows.Next() {
		var summary systemAccountPrincipalSummary
		var displayName string
		if err := rows.Scan(&summary.ID, &summary.Username, &displayName, &summary.Status); err != nil {
			return nil, err
		}
		summary.DisplayName = displayName
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// ListAuthorizationGranteeTeams mirrors listAuthorizationGranteeTeams.
func (s *Store) ListAuthorizationGranteeTeams(ctx context.Context, options authorizationPrincipalOptionListOptions) ([]systemTeamPrincipalSummary, error) {
	clauses, params := principalFilter(options, func(keyword string) (string, []any) {
		text := strings.TrimSpace(keyword)
		if text == "" {
			return "", nil
		}
		return "(name >= ? AND name < ?)", []any{text, textPrefixUpperBound(text)}
	})
	query := `
		SELECT id, name, status
		FROM ` + s.table("system_teams") + `
		` + clauses + `
		ORDER BY status ASC, name ASC, id ASC
		LIMIT ?`
	params = append(params, options.Limit)
	rows, err := s.db.QueryContext(ctx, s.bind(query), params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := []systemTeamPrincipalSummary{}
	for rows.Next() {
		var summary systemTeamPrincipalSummary
		if err := rows.Scan(&summary.ID, &summary.Name, &summary.Status); err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// ListAuthorizationGranteeGroups mirrors listAuthorizationGranteeGroups:
// enabled groups of the grantee's owner namespace plus the active grantee
// gate; is_default ordering unless preferDefault === false.
func (s *Store) ListAuthorizationGranteeGroups(ctx context.Context, options authorizationGranteeGroupOptionListOptions) ([]granteeGroupOptionSummary, error) {
	granteeID := strings.TrimSpace(options.GranteeSystemAccountID)
	if granteeID == "" {
		return []granteeGroupOptionSummary{}, nil
	}
	clauses := []string{
		"groups.system_account_id = ?",
		"groups.enabled = 1",
		"EXISTS (SELECT 1 FROM " + s.table("system_accounts") + " grantee WHERE grantee.id = ? AND grantee.status = 'active')",
	}
	params := []any{granteeID, granteeID}
	if ids := normalizeTextList(options.IDs); len(ids) > 0 {
		clauses = append(clauses, "groups.id IN ("+placeholders(len(ids))+")")
		params = append(params, idsToAny(ids)...)
	}
	if providerCode := strings.TrimSpace(options.ProviderCode); providerCode != "" {
		clauses = append(clauses, "groups.provider_code = ?")
		params = append(params, providerCode)
	}
	if keyword := strings.TrimSpace(options.Keyword); keyword != "" {
		clauses = append(clauses, "(groups.name >= ? AND groups.name < ?)")
		params = append(params, keyword, textPrefixUpperBound(keyword))
	}
	orderClause := "ORDER BY groups.is_default DESC, groups.updated_at DESC, groups.id DESC"
	if options.HasPreferDefault && options.PreferDefault != nil && !*options.PreferDefault {
		orderClause = "ORDER BY groups.updated_at DESC, groups.id DESC"
	}
	query := `
		SELECT groups.id, groups.name
		FROM ` + s.table("groups") + ` groups
		WHERE ` + strings.Join(clauses, " AND ") + `
		` + orderClause + `
		LIMIT ?`
	params = append(params, options.Limit)
	rows, err := s.db.QueryContext(ctx, s.bind(query), params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := []granteeGroupOptionSummary{}
	for rows.Next() {
		var summary granteeGroupOptionSummary
		if err := rows.Scan(&summary.ID, &summary.Name); err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// principalFilter mirrors buildPrincipalFilter.
func principalFilter(options authorizationPrincipalOptionListOptions, keywordFilter func(keyword string) (string, []any)) (string, []any) {
	clauses := []string{}
	params := []any{}
	if ids := normalizeTextList(options.IDs); len(ids) > 0 {
		clauses = append(clauses, "id IN ("+placeholders(len(ids))+")")
		params = append(params, idsToAny(ids)...)
	}
	if clause, clauseParams := keywordFilter(options.Keyword); clause != "" {
		clauses = append(clauses, strings.TrimPrefix(strings.TrimPrefix(clause, "WHERE "), "where "))
		params = append(params, clauseParams...)
	}
	if len(clauses) == 0 {
		return "", params
	}
	return "WHERE " + strings.Join(clauses, " AND "), params
}

func placeholders(count int) string {
	if count < 1 {
		count = 1
	}
	pieces := make([]string, count)
	for index := range pieces {
		pieces[index] = "?"
	}
	return strings.Join(pieces, ", ")
}

func idsToAny(ids []string) []any {
	values := make([]any, 0, len(ids))
	for _, id := range ids {
		values = append(values, id)
	}
	return values
}
