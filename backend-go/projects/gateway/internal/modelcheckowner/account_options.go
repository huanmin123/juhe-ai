package modelcheckowner

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// ModelCheckOptions returns the immutable catalog advertised by the Node
// model-check UI. It is derived from the Gateway profile catalog, not from a
// mutable database row.
func (s *BusinessTargetSource) ModelCheckOptions() ModelCheckOptions {
	models := make([]ModelCheckSupportedOption, 0, len(modelcheckprofile.SupportedModels()))
	for _, model := range modelcheckprofile.SupportedModels() {
		models = append(models, ModelCheckSupportedOption{Value: model, Label: model})
	}
	return ModelCheckOptions{
		SupportedModels: models,
		SupportedProfiles: []ModelCheckSupportedOption{
			{Value: "quick", Label: "快速检测", Description: "最多执行 2 个轻量串行探针，快速给出初步判断"},
			{Value: "full", Label: "深度检测", Description: "准确优先，执行多轮协议、行为指纹、长上下文、稳定性和可信对比探针"},
		},
		DefaultModel:   modelcheckprofile.DefaultModel,
		DefaultProfile: modelcheckprofile.DefaultProfile,
		TrustedComparison: map[string]any{
			"enabledByDefault": false,
			"available":        true,
			"message":          "可信对比默认关闭；选择一个你信任的可用同协议账户后，会额外消耗该账户额度",
		},
	}
}

// ListAccountOptions reads only accounts eligible for the requested purpose.
// The authenticated system-account scope is required even when the caller is
// an administrator: cross-tenant option reads stay denied until their own
// scope is explicitly selected. run/history additionally include only
// authorized instances whose grant and viewer binding are provable. The query
// is bounded and excludes deleted/un-grouped accounts.
func (s *BusinessTargetSource) ListAccountOptions(ctx context.Context, options AccountOptionsQuery) ([]AccountOption, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("J3b Business account options source is not initialized")
	}
	if options.Purpose != "run" && options.Purpose != "history" && options.Purpose != "schedule" {
		return nil, fmt.Errorf("J3b account options purpose is invalid")
	}
	if strings.TrimSpace(options.SystemAccountID) == "" {
		return nil, fmt.Errorf("J3b account options systemAccountId scope is required")
	}
	limit := options.Limit
	if limit < 1 || limit > 50 {
		return nil, fmt.Errorf("J3b account options limit is invalid")
	}
	queryLimit := limit
	if options.AccountID == "" && (options.Purpose == "run" || options.Purpose == "history") {
		// Node applies the limit after the owner+authorized UNION. Read the
		// bounded catalog first, then merge, sort and truncate below.
		queryLimit = 50
	}
	args := make([]any, 0, 9)
	where := []string{
		"a.deleted_at IS NULL",
		"a.authorization_instance_authorization_id IS NULL",
		"a.authorization_instance_source_account_id IS NULL",
		"a.type IN ('api_key','oauth','google_oauth')",
		"p.enabled = " + s.boolLiteral(true),
		modelCheckProfilePredicate("a", "p.id"),
		"a.system_account_id=" + s.placeholder(len(args)+1),
	}
	args = append(args, strings.TrimSpace(options.SystemAccountID))
	if options.Purpose == "run" || options.Purpose == "schedule" {
		where = append(where, "a.status = 'active'", "a.schedulable = "+s.boolLiteral(true), s.accountAvailable("a"))
	}
	where = append(where, "EXISTS (SELECT 1 FROM "+s.table("group_accounts")+" ga JOIN "+s.table("groups")+" g ON g.id=ga.group_id WHERE ga.account_id=a.id AND ga.system_account_id=a.system_account_id AND ga.enabled="+s.boolLiteral(true)+" AND g.enabled="+s.boolLiteral(true)+" AND (g.system_account_id=a.system_account_id OR EXISTS (SELECT 1 FROM "+s.table("resource_authorizations")+" group_auth WHERE group_auth.resource_type='group' AND group_auth.resource_id=g.id AND group_auth.resource_owner_system_account_id=g.system_account_id AND group_auth.grantee_system_account_id=a.system_account_id AND group_auth.scope='use' AND group_auth.status='active' AND "+s.expiryAfterNow("group_auth.expires_at")+")))")
	if options.AccountID != "" {
		where = append(where, "a.id="+s.placeholder(len(args)+1))
		args = append(args, options.AccountID)
	} else {
		searchParts := make([]string, 0, 2)
		if options.Keyword != "" {
			searchParts = append(searchParts, "(LOWER(a.name) LIKE "+s.placeholder(len(args)+1)+" OR LOWER(a.id) LIKE "+s.placeholder(len(args)+2)+")")
			needle := "%" + strings.ToLower(options.Keyword) + "%"
			args = append(args, needle, needle)
		}
		if len(options.SelectedID) > 0 {
			placeholders := make([]string, len(options.SelectedID))
			for i, id := range options.SelectedID {
				placeholders[i] = s.placeholder(len(args) + i + 1)
				args = append(args, id)
			}
			searchParts = append(searchParts, "a.id IN ("+strings.Join(placeholders, ",")+")")
		}
		if len(searchParts) > 0 {
			where = append(where, "("+strings.Join(searchParts, " OR ")+")")
		}
	}
	query := "SELECT a.id,a.name,a.provider_code,a.provider_protocol_profile_id,a.protocol_code,a.protocol_version,COALESCE(a.availability_schedule_json,'') FROM " + s.table("accounts") + " a JOIN " + s.table("provider_protocol_profiles") + " p ON p.id=a.provider_protocol_profile_id WHERE " + strings.Join(where, " AND ") + " ORDER BY LOWER(a.name),a.id LIMIT " + s.placeholder(len(args)+1)
	args = append(args, queryLimit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read J3b account options: %w", err)
	}
	defer rows.Close()
	result := make([]AccountOption, 0, limit)
	for rows.Next() {
		var option AccountOption
		var schedule string
		if err := rows.Scan(&option.ID, &option.Name, &option.ProviderCode, &option.ProviderProtocolProfile, &option.ProtocolCode, &option.ProtocolVersion, &schedule); err != nil {
			return nil, fmt.Errorf("scan J3b account option: %w", err)
		}
		if options.Purpose == "run" || options.Purpose == "schedule" {
			allowed, err := availabilityAllowedGateway(schedule, s.nowUTC())
			if err != nil {
				return nil, fmt.Errorf("evaluate J3b account option availability schedule: %w", err)
			}
			if !allowed {
				continue
			}
		}
		if options.AccountID != "" {
			if models, err := s.configuredModelCheckModels(ctx, option.ID, option.ProviderCode, option.ProviderProtocolProfile); err != nil {
				return nil, err
			} else {
				option.ModelCheckModels = models
			}
		}
		result = append(result, option)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate J3b account options: %w", err)
	}
	if options.Purpose == "run" || options.Purpose == "history" {
		authorizedOptions := options
		if options.AccountID == "" {
			authorizedOptions.Limit = 50
		}
		authorized, err := s.listAuthorizedAccountOptions(ctx, authorizedOptions)
		if err != nil {
			return nil, err
		}
		seen := make(map[string]struct{}, len(result)+len(authorized))
		for _, item := range result {
			seen[item.ID] = struct{}{}
		}
		for _, item := range authorized {
			if _, exists := seen[item.ID]; exists {
				continue
			}
			result = append(result, item)
			seen[item.ID] = struct{}{}
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if strings.ToLower(result[i].Name) == strings.ToLower(result[j].Name) {
			return result[i].ID < result[j].ID
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// listAuthorizedAccountOptions mirrors the Node access query for the narrow
// model-check surface. The viewer is also the account-instance tenant in the
// current Business schema; the authorization row and viewer group binding
// provide the second, explicit grant check. Source accounts supply the
// provider/profile facts for an authorized instance.
func (s *BusinessTargetSource) listAuthorizedAccountOptions(ctx context.Context, options AccountOptionsQuery) ([]AccountOption, error) {
	scope := strings.TrimSpace(options.SystemAccountID)
	where := []string{
		"a.deleted_at IS NULL",
		"a.authorization_instance_authorization_id IS NOT NULL",
		"a.system_account_id=" + s.placeholder(1),
		"ra.id=a.authorization_instance_authorization_id",
		"ra.resource_type='account'",
		"ra.resource_id=source_accounts.id",
		"ra.grantee_system_account_id=" + s.placeholder(2),
		"ra.scope='use'",
		"ra.status IN ('active','paused','expired')",
		"source_accounts.id IS NOT NULL",
		"source_accounts.deleted_at IS NULL",
		"source_accounts.system_account_id=ra.resource_owner_system_account_id",
		"source_accounts.type IN ('api_key','oauth','google_oauth')",
		modelCheckProfilePredicate("source_accounts", "source_accounts.provider_protocol_profile_id"),
		"bindings.group_id IS NOT NULL",
		"bindings.enabled=" + s.boolLiteral(true),
		"bindings.account_authorization_id=ra.id",
		"groups.enabled=" + s.boolLiteral(true),
		"(groups.system_account_id=bindings.system_account_id OR EXISTS (SELECT 1 FROM " + s.table("resource_authorizations") + " group_auth WHERE group_auth.resource_type='group' AND group_auth.resource_id=groups.id AND group_auth.resource_owner_system_account_id=groups.system_account_id AND group_auth.grantee_system_account_id=bindings.system_account_id AND group_auth.scope='use' AND group_auth.status='active' AND " + s.expiryAfterNow("group_auth.expires_at") + "))",
	}
	args := []any{scope, scope}
	if options.Purpose == "run" {
		where = append(where,
			"a.status='active'", "a.schedulable="+s.boolLiteral(true),
			"source_accounts.status='active'", "source_accounts.schedulable="+s.boolLiteral(true),
			s.accountAvailable("a"), s.accountAvailable("source_accounts"),
			s.expiryAfterNow("ra.expires_at"),
		)
	}
	if options.AccountID != "" {
		where = append(where, "a.id="+s.placeholder(len(args)+1))
		args = append(args, options.AccountID)
	} else {
		search := make([]string, 0, 2)
		if value := strings.TrimSpace(options.Keyword); value != "" {
			search = append(search, "(LOWER(COALESCE(source_accounts.name,a.name)) LIKE "+s.placeholder(len(args)+1)+" OR LOWER(a.id) LIKE "+s.placeholder(len(args)+2)+")")
			needle := "%" + strings.ToLower(value) + "%"
			args = append(args, needle, needle)
		}
		if len(options.SelectedID) > 0 {
			parts := make([]string, len(options.SelectedID))
			for i, id := range options.SelectedID {
				parts[i] = s.placeholder(len(args) + i + 1)
				args = append(args, id)
			}
			search = append(search, "a.id IN ("+strings.Join(parts, ",")+")")
		}
		if len(search) > 0 {
			where = append(where, "("+strings.Join(search, " OR ")+")")
		}
	}
	query := "SELECT options.id,options.name,options.provider_code,options.profile_id,options.protocol_code,options.protocol_version,options.instance_schedule,options.source_schedule,options.source_id FROM (SELECT DISTINCT a.id AS id,COALESCE(source_accounts.name,a.name) AS name,COALESCE(source_accounts.provider_code,a.provider_code) AS provider_code,COALESCE(source_accounts.provider_protocol_profile_id,a.provider_protocol_profile_id) AS profile_id,COALESCE(source_accounts.protocol_code,a.protocol_code) AS protocol_code,COALESCE(source_accounts.protocol_version,a.protocol_version) AS protocol_version,COALESCE(a.availability_schedule_json,'') AS instance_schedule,COALESCE(source_accounts.availability_schedule_json,'') AS source_schedule,source_accounts.id AS source_id FROM " + s.table("accounts") + " a JOIN " + s.table("resource_authorizations") + " ra ON ra.id=a.authorization_instance_authorization_id JOIN " + s.table("accounts") + " source_accounts ON source_accounts.id=a.authorization_instance_source_account_id LEFT JOIN " + s.table("group_accounts") + " bindings ON bindings.account_id=a.id AND bindings.system_account_id=" + s.placeholder(len(args)+1) + " LEFT JOIN " + s.table("groups") + " groups ON groups.id=bindings.group_id WHERE " + strings.Join(where, " AND ") + ") AS options ORDER BY LOWER(options.name),options.id LIMIT " + s.placeholder(len(args)+2)
	args = append(args, scope, options.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read J3b authorized account options: %w", err)
	}
	defer rows.Close()
	result := make([]AccountOption, 0, options.Limit)
	for rows.Next() {
		var option AccountOption
		var sourceID, instanceSchedule, sourceSchedule string
		if err := rows.Scan(&option.ID, &option.Name, &option.ProviderCode, &option.ProviderProtocolProfile, &option.ProtocolCode, &option.ProtocolVersion, &instanceSchedule, &sourceSchedule, &sourceID); err != nil {
			return nil, fmt.Errorf("scan J3b authorized account option: %w", err)
		}
		if options.Purpose == "run" {
			for label, raw := range map[string]string{"instance": instanceSchedule, "source": sourceSchedule} {
				allowed, err := availabilityAllowedGateway(raw, s.nowUTC())
				if err != nil {
					return nil, fmt.Errorf("evaluate J3b authorized %s account option availability schedule: %w", label, err)
				}
				if !allowed {
					option = AccountOption{}
					break
				}
			}
			if option.ID == "" {
				continue
			}
		}
		if options.AccountID != "" {
			if models, err := s.configuredModelCheckModels(ctx, sourceID, option.ProviderCode, option.ProviderProtocolProfile); err != nil {
				return nil, err
			} else {
				option.ModelCheckModels = models
			}
		}
		result = append(result, option)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate J3b authorized account options: %w", err)
	}
	return result, nil
}

func (s *BusinessTargetSource) configuredModelCheckModels(ctx context.Context, accountID, providerCode, profileID string) ([]string, error) {
	profile, ok := modelcheckprofile.Find(providerCode, profileID)
	if !ok {
		return []string{}, nil
	}
	models := make([]string, 0, len(profile.Models))
	for _, model := range profile.Models {
		resolved, err := resolveConfiguredUpstreamModelMapping(ctx, s.db, s.postgres, accountID, profile, model)
		if err != nil {
			return nil, err
		}
		if resolved.UpstreamModel != "" {
			models = append(models, model)
		}
	}
	return models, nil
}

func (s *BusinessTargetSource) boolLiteral(value bool) string {
	if s.postgres {
		if value {
			return "TRUE"
		}
		return "FALSE"
	}
	if value {
		return "1"
	}
	return "0"
}

func (s *BusinessTargetSource) expiryAfterNow(column string) string {
	if s.postgres {
		return "(" + column + " IS NULL OR " + column + "::timestamptz > CURRENT_TIMESTAMP)"
	}
	return "(" + column + " IS NULL OR datetime(" + column + ") > CURRENT_TIMESTAMP)"
}

func (s *BusinessTargetSource) accountAvailable(alias string) string {
	return s.expiryAfterNow(alias+".account_expires_at") + " AND " + s.cooldownClear(alias+".cooldown_until") + " AND COALESCE(" + alias + ".last_error_code,'') <> 'account_expired'"
}

func (s *BusinessTargetSource) cooldownClear(column string) string {
	if s.postgres {
		return "(" + column + " IS NULL OR " + column + "::timestamptz <= CURRENT_TIMESTAMP)"
	}
	return "(" + column + " IS NULL OR datetime(" + column + ") <= CURRENT_TIMESTAMP)"
}

// modelCheckProfilePredicate keeps the options query aligned with the
// immutable Gateway model-check catalog. The values come only from compiled
// profile definitions, so the generated SQL remains parameter-free and does
// not widen the query with caller-controlled identifiers.
func modelCheckProfilePredicate(accountAlias, profileIDColumn string) string {
	seen := make(map[string]struct{})
	clauses := make([]string, 0, len(modelcheckprofile.Profiles()))
	for _, profile := range modelcheckprofile.Profiles() {
		provider := escapeSQLLiteral(profile.ProviderCode)
		for _, profileID := range profile.ProviderProtocolProfileIDs {
			key := provider + "\x00" + profileID
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			clauses = append(clauses, "("+accountAlias+".provider_code='"+provider+"' AND "+profileIDColumn+"='"+escapeSQLLiteral(profileID)+"')")
		}
	}
	if len(clauses) == 0 {
		return "FALSE"
	}
	return "(" + strings.Join(clauses, " OR ") + ")"
}

func escapeSQLLiteral(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}
