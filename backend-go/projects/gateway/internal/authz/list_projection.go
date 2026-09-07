// List item projection (BUG-0165 item 4): the ResourceAuthorizationListItem
// shape ported from resource-authorization-read.repository.ts
// (resourceAuthorizationListItemFromRow :743-818) — name/owner/grantee/team
// lookups, the effective source summary, the manage-gated limits/expiry
// fields and the permission bits.
package authz

import (
	"context"
	"database/sql"
	"strings"
)

// ListItemPermissions mirrors Pick<ResourcePermissions, 'canEdit'|'canAuthorize'>.
type ListItemPermissions struct {
	CanEdit      bool `json:"canEdit"`
	CanAuthorize bool `json:"canAuthorize"`
}

// ListItemTeamSource mirrors the sourceSummary.teamSources entries.
type ListItemTeamSource struct {
	SourceTeamID   string  `json:"sourceTeamId"`
	SourceTeamName *string `json:"sourceTeamName,omitempty"`
}

// ListItemSourceSummary mirrors the nested sourceSummary object.
type ListItemSourceSummary struct {
	ActiveSourceCount int                  `json:"activeSourceCount"`
	HasManual         bool                 `json:"hasManual"`
	HasTeam           bool                 `json:"hasTeam"`
	TeamSources       []ListItemTeamSource `json:"teamSources"`
}

// ListItem mirrors ResourceAuthorizationListItem (domain/types.ts:1843-1876):
// optional fields stay absent like the Node undefined projections.
type ListItem struct {
	ID                      string                `json:"id"`
	ResourceType            string                `json:"resourceType"`
	ResourceID              string                `json:"resourceId"`
	ResourceName            *string               `json:"resourceName,omitempty"`
	OwnerID                 string                `json:"resourceOwnerSystemAccountId"`
	OwnerName               *string               `json:"resourceOwnerSystemAccountName,omitempty"`
	GranteeType             string                `json:"granteeType"`
	GranteeUserID           *string               `json:"granteeSystemAccountId,omitempty"`
	GranteeName             *string               `json:"granteeSystemAccountName,omitempty"`
	GranteeUsername         *string               `json:"granteeUsername,omitempty"`
	GranteeTeamID           *string               `json:"granteeTeamId,omitempty"`
	GranteeTeamName         *string               `json:"granteeTeamName,omitempty"`
	Status                  string                `json:"status"`
	Remark                  *string               `json:"remark,omitempty"`
	ExpiresAt               *string               `json:"expiresAt,omitempty"`
	Limits                  any                   `json:"limits,omitempty"`
	ResourceAccountExpiresAt *string              `json:"resourceAccountExpiresAt,omitempty"`
	EffectiveSourceType     string                `json:"effectiveSourceType"`
	EffectiveSourceTeamID   *string               `json:"effectiveSourceTeamId,omitempty"`
	EffectiveSourceTeamName *string               `json:"effectiveSourceTeamName,omitempty"`
	CreatedAt               string                `json:"createdAt"`
	UpdatedAt               string                `json:"updatedAt"`
	SourceSummary           ListItemSourceSummary `json:"sourceSummary"`
	Permissions             ListItemPermissions   `json:"permissions"`
}

// canManageOwner mirrors canManageResourceOwner over the request scope: a
// scoped identity must match the owner, everything else depends on the admin
// role.
func canManageOwner(ownerID string, access accessInfo) bool {
	scoped := ""
	if access.IsAdmin {
		scoped = access.FilterID
	} else {
		scoped = access.ViewerID
	}
	if scoped != "" {
		return scoped == ownerID
	}
	return access.IsAdmin
}

// buildListItems mirrors resourceAuthorizationListItems + ...FromRow: batched
// lookups over the page rows, then a pure projection per row.
func (s *Store) buildListItems(ctx context.Context, rows []grantRow, access accessInfo) ([]ListItem, error) {
	var accountIDs, groupIDs, principalIDs, teamIDs []string
	for _, row := range rows {
		if row.ResourceType == "account" && row.ResourceID != "" {
			accountIDs = append(accountIDs, row.ResourceID)
		}
		if row.ResourceType == "group" && row.ResourceID != "" {
			groupIDs = append(groupIDs, row.ResourceID)
		}
		if row.GranteeTeamID.Valid && row.GranteeTeamID.String != "" {
			teamIDs = append(teamIDs, row.GranteeTeamID.String)
		}
		principalIDs = append(principalIDs, row.OwnerID)
		if row.GranteeUserID.Valid {
			principalIDs = append(principalIDs, row.GranteeUserID.String)
		}
	}
	accounts, err := s.loadAccountLookupRows(ctx, uniqueStrings(accountIDs))
	if err != nil {
		return nil, err
	}
	instanceNames, err := s.loadInstanceAccountNameMap(ctx, uniqueStrings(accountIDs))
	if err != nil {
		return nil, err
	}
	groupNames, err := s.loadGroupNameMap(ctx, uniqueStrings(groupIDs))
	if err != nil {
		return nil, err
	}
	principals, err := s.loadPrincipalMap(ctx, uniqueStrings(principalIDs))
	if err != nil {
		return nil, err
	}
	teamNames, err := s.loadTeamNameMap(ctx, uniqueStrings(teamIDs))
	if err != nil {
		return nil, err
	}

	items := make([]ListItem, 0, len(rows))
	for _, row := range rows {
		owner := principals[row.OwnerID]
		var grantee *accountPrincipal
		if row.GranteeUserID.Valid && row.GranteeUserID.String != "" {
			if principal, ok := principals[row.GranteeUserID.String]; ok {
				grantee = &principal
			}
		}
		var teamName *string
		if row.GranteeTeamID.Valid && row.GranteeTeamID.String != "" {
			if name, ok := teamNames[row.GranteeTeamID.String]; ok {
				value := name
				teamName = &value
			}
		}
		isTeamSource := row.GranteeType == "team"
		sourceActive := row.Status == StatusActive || row.Status == StatusPaused
		canManage := canManageOwner(row.OwnerID, access)
		var account *accountLookupRow
		if row.ResourceType == "account" {
			if lookup, ok := accounts[row.ResourceID]; ok {
				account = &lookup
			}
		}

		item := ListItem{
			ID:                  row.ID,
			ResourceType:        row.ResourceType,
			ResourceID:          row.ResourceID,
			OwnerID:             row.OwnerID,
			GranteeType:         row.GranteeType,
			Status:              row.Status,
			EffectiveSourceType: "manual",
			CreatedAt:           row.CreatedAt,
			UpdatedAt:           row.UpdatedAt,
			Permissions:         ListItemPermissions{CanEdit: canManage, CanAuthorize: canManage},
			SourceSummary:       ListItemSourceSummary{TeamSources: []ListItemTeamSource{}},
		}
		if ownerName := owner.displayName; ownerName != "" {
			value := ownerName
			item.OwnerName = &value
		}
		if row.GranteeUserID.Valid && row.GranteeUserID.String != "" {
			value := row.GranteeUserID.String
			item.GranteeUserID = &value
		}
		if grantee != nil {
			if grantee.displayName != "" {
				value := grantee.displayName
				item.GranteeName = &value
			}
			if grantee.username != "" {
				value := grantee.username
				item.GranteeUsername = &value
			}
		}
		if row.GranteeTeamID.Valid && row.GranteeTeamID.String != "" {
			value := row.GranteeTeamID.String
			item.GranteeTeamID = &value
			item.GranteeTeamName = teamName
		}
		if row.Remark.Valid && row.Remark.String != "" {
			value := row.Remark.String
			item.Remark = &value
		}
		if row.ExpiresAt.Valid && row.ExpiresAt.String != "" {
			value := row.ExpiresAt.String
			item.ExpiresAt = &value
		}
		if canManage {
			item.Limits = decodeAuthorizationLimits(grantLimitsText(row.LimitsJSON))
		}
		if canManage && account != nil && account.accountExpiresAt != "" {
			value := account.accountExpiresAt
			item.ResourceAccountExpiresAt = &value
		}
		if isTeamSource {
			item.EffectiveSourceType = "team"
			if canManage && row.GranteeTeamID.Valid && row.GranteeTeamID.String != "" {
				value := row.GranteeTeamID.String
				item.EffectiveSourceTeamID = &value
				item.EffectiveSourceTeamName = teamName
			}
		}
		if account != nil && account.name != "" {
			value := account.name
			item.ResourceName = &value
		} else if name, ok := instanceNames[row.ResourceID]; ok && name != "" {
			value := name
			item.ResourceName = &value
		} else if row.ResourceType == "group" {
			if name, ok := groupNames[row.ResourceID]; ok && name != "" {
				value := name
				item.ResourceName = &value
			}
		}
		if sourceActive {
			item.SourceSummary.ActiveSourceCount = 1
			item.SourceSummary.HasManual = !isTeamSource
			item.SourceSummary.HasTeam = isTeamSource
			if isTeamSource && canManage && row.GranteeTeamID.Valid && row.GranteeTeamID.String != "" {
				item.SourceSummary.TeamSources = []ListItemTeamSource{{
					SourceTeamID:   row.GranteeTeamID.String,
					SourceTeamName: teamName,
				}}
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// accountLookupRow mirrors the BusinessResourceLookup projection of
// loadAccountLookupMap (name + owner + expiry, no status filtering).
type accountLookupRow struct {
	name             string
	ownerID          string
	accountExpiresAt string
}

// loadAccountLookupRows mirrors loadAccountLookupMap.
func (s *Store) loadAccountLookupRows(ctx context.Context, accountIDs []string) (map[string]accountLookupRow, error) {
	result := map[string]accountLookupRow{}
	if len(accountIDs) == 0 {
		return result, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(accountIDs)), ", ")
	query := `SELECT id, name, system_account_id, account_expires_at FROM ` + s.table("accounts") + ` WHERE id IN (` + placeholders + `)`
	rows, err := s.db.QueryContext(ctx, s.bind(query), toStrings(accountIDs)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var lookup accountLookupRow
		var expiresAt sql.NullString
		if err := rows.Scan(&id, &lookup.name, &lookup.ownerID, &expiresAt); err != nil {
			return nil, err
		}
		if expiresAt.Valid {
			lookup.accountExpiresAt = expiresAt.String
		}
		result[id] = lookup
	}
	return result, rows.Err()
}

// loadInstanceAccountNameMap mirrors loadAuthorizationInstanceAccountNameMap:
// the first instance account name per authorization id.
func (s *Store) loadInstanceAccountNameMap(ctx context.Context, resourceIDs []string) (map[string]string, error) {
	result := map[string]string{}
	if len(resourceIDs) == 0 {
		return result, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(resourceIDs)), ", ")
	query := `SELECT ra.resource_id, accounts.name
		FROM ` + s.table("resource_authorizations") + ` ra
		INNER JOIN ` + s.table("accounts") + ` ON accounts.authorization_instance_authorization_id = ra.id
		WHERE ra.resource_type = 'account'
			AND ra.resource_id IN (` + placeholders + `)
		ORDER BY ra.created_at ASC, ra.id ASC`
	rows, err := s.db.QueryContext(ctx, s.bind(query), toStrings(resourceIDs)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var resourceID, name string
		if err := rows.Scan(&resourceID, &name); err != nil {
			return nil, err
		}
		if resourceID != "" && name != "" {
			if _, exists := result[resourceID]; !exists {
				result[resourceID] = name
			}
		}
	}
	return result, rows.Err()
}

// loadGroupNameMap mirrors loadGroupNameMap.
func (s *Store) loadGroupNameMap(ctx context.Context, groupIDs []string) (map[string]string, error) {
	result := map[string]string{}
	if len(groupIDs) == 0 {
		return result, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(groupIDs)), ", ")
	query := `SELECT id, name FROM ` + s.table("groups") + ` WHERE id IN (` + placeholders + `)`
	rows, err := s.db.QueryContext(ctx, s.bind(query), toStrings(groupIDs)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		result[id] = name
	}
	return result, rows.Err()
}
