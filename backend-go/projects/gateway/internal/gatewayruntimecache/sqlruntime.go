package gatewayruntimecache

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// read_gateway_runtime composition (Node db-service readGatewayRuntime with
// skipDynamicRouteSelection: true — the cache path) plus the gateway API key
// row read (Node validateGatewayApiKey /
// loadActiveGatewayApiKeyGroupBindings).
// ---------------------------------------------------------------------------

// ReadGatewayRuntime mirrors the cache-path read_gateway_runtime: settings,
// validated API key row with active bindings, then the first candidate group
// (binding order) with usage access and a dispatchable account set. Dynamic
// route modes return the static shape here; the Service re-routes them through
// the GroupBindingOrderer seam (Node routeCachedDynamicGatewayRuntime*).
func (m *SQLReadModels) ReadGatewayRuntime(ctx context.Context, key string) (GatewayRuntime, error) {
	ctx = ensureModelCtx(ctx)
	projectedSettings, err := m.ReadGatewaySettings(ctx)
	if err != nil {
		return GatewayRuntime{}, err
	}
	apiKey, err := m.loadGatewayAPIKeyByKeyHash(ctx, key)
	if err != nil {
		return GatewayRuntime{}, err
	}
	if apiKey == nil {
		return GatewayRuntime{Settings: projectedSettings, Accounts: []OpenAIAccountSecret{}}, nil
	}
	systemAccountID := apiKey.SystemAccountID
	if IsDynamicRouteStrategyMode(apiKey.RouteStrategyMode) {
		return GatewayRuntime{
			APIKey:                     apiKey,
			Settings:                   projectedSettings,
			Accounts:                   []OpenAIAccountSecret{},
			ResponseInspectionPolicies: []ResponseInspectionPolicySummary{},
		}, nil
	}
	orderedBindings := normalizeGatewayAPIKeyGroupBindings(apiKey.GroupBindings)
	if len(orderedBindings) > 0 {
		apiKey.SelectedGroupID = orderedBindings[0].GroupID
	}
	candidateGroupIDs := uniqueGroupIDs(orderedBindings)

	for _, groupID := range candidateGroupIDs {
		groupAccess, err := m.ResolveGroupUsageAccessMetadata(ctx, groupID, systemAccountID)
		if err != nil {
			return GatewayRuntime{}, err
		}
		if groupAccess == nil {
			continue
		}
		if m.accounts == nil {
			return GatewayRuntime{}, errors.New("gatewayruntimecache 账户选择器未接线（AccountsSelector）")
		}
		result, err := m.accounts.ListOpenAIAccountsForGroupResult(ctx, groupID, systemAccountID, OpenAIAccountsForGroupOptions{
			PreResolvedGroupAccess: groupAccess,
		})
		if err != nil {
			return GatewayRuntime{}, err
		}
		if !hasDispatchableCachedGatewayAccount(result.Accounts) && len(candidateGroupIDs) > 1 {
			continue
		}
		policies, err := m.inspectionPoliciesForAccounts(ctx, result.Accounts)
		if err != nil {
			return GatewayRuntime{}, err
		}
		row := CloneGatewayAPIKeyRow(*apiKey)
		if len(orderedBindings) > 0 {
			row.GroupBindings = append([]GatewayAPIKeyGroupBindingRow(nil), orderedBindings...)
		}
		row.SelectedGroupID = groupID
		return GatewayRuntime{
			APIKey:                     &row,
			Settings:                   projectedSettings,
			GroupAccess:                groupAccess,
			Accounts:                   result.Accounts,
			AccountDispatchDiagnostics: result.Diagnostics,
			ResponseInspectionPolicies: policies,
		}, nil
	}

	return GatewayRuntime{
		APIKey:                     apiKey,
		Settings:                   projectedSettings,
		Accounts:                   []OpenAIAccountSecret{},
		ResponseInspectionPolicies: []ResponseInspectionPolicySummary{},
	}, nil
}

// inspectionPoliciesForAccounts mirrors listActiveResponseInspectionPoliciesForAccounts:
// unique protocol:provider scopes in first-seen order, merged by policy id.
func (m *SQLReadModels) inspectionPoliciesForAccounts(ctx context.Context, accounts []OpenAIAccountSecret) ([]ResponseInspectionPolicySummary, error) {
	scopes := uniqueInspectionScopes(accounts)
	ids := []string{}
	byID := map[string]ResponseInspectionPolicySummary{}
	for _, scope := range scopes {
		policies, err := m.ListActiveResponseInspectionPolicies(ctx, scope[0], scope[1])
		if err != nil {
			return nil, err
		}
		for _, policy := range policies {
			if _, exists := byID[policy.ID]; !exists {
				ids = append(ids, policy.ID)
			}
			byID[policy.ID] = policy
		}
	}
	out := make([]ResponseInspectionPolicySummary, 0, len(ids))
	for _, id := range ids {
		out = append(out, byID[id])
	}
	return out, nil
}

// uniqueInspectionScopes mirrors the Node scope fan-out (first-seen order).
func uniqueInspectionScopes(accounts []OpenAIAccountSecret) [][2]string {
	seen := map[string]bool{}
	scopes := [][2]string{}
	for i := range accounts {
		protocolCode := trimSpace(accounts[i].ProtocolCode)
		if protocolCode == "" {
			continue
		}
		providerCode := trimSpace(accounts[i].ProviderCode)
		key := protocolCode + ":" + providerCode
		if seen[key] {
			continue
		}
		seen[key] = true
		scopes = append(scopes, [2]string{protocolCode, providerCode})
	}
	return scopes
}

// loadGatewayAPIKeyByKeyHash mirrors validateGatewayApiKey without the process
// cache (the Service owns caching): sk- prefix guard, active owner join,
// expiry/status gates, normalized route fields and active bindings.
func (m *SQLReadModels) loadGatewayAPIKeyByKeyHash(ctx context.Context, key string) (*GatewayAPIKeyRow, error) {
	if !strings.HasPrefix(key, "sk-") {
		return nil, nil
	}
	var id, systemAccountID, routeStrategyID, routeStrategyMode, status string
	var routeConfigJSON, expiresAt, quotaLimitsJSON, requestLimitsJSON sql.NullString
	var imageGenerationEnabled int
	err := m.db.QueryRowContext(ctx, m.bind(`SELECT
			api_keys.id,
			api_keys.system_account_id,
			route_strategies.id AS route_strategy_id,
			route_strategies.mode AS route_strategy_mode,
			route_strategies.config_json AS route_strategy_config_json,
			api_keys.status,
			api_keys.expires_at,
			api_keys.quota_limits_json,
			system_accounts.image_generation_enabled,
			system_accounts.request_limits_json
		FROM `+m.table("api_keys")+` api_keys
		INNER JOIN `+m.table("system_accounts")+` system_accounts
			ON system_accounts.id = api_keys.system_account_id
			AND system_accounts.status = 'active'
		INNER JOIN `+m.table("route_strategies")+` route_strategies
			ON route_strategies.id = api_keys.route_strategy_id
			AND route_strategies.system_account_id = api_keys.system_account_id
			AND route_strategies.status = 'active'
		WHERE api_keys.key_hash = ?
		LIMIT 1`), HashSecret(key)).
		Scan(&id, &systemAccountID, &routeStrategyID, &routeStrategyMode, &routeConfigJSON,
			&status, &expiresAt, &quotaLimitsJSON, &imageGenerationEnabled, &requestLimitsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	expiresAtText := ""
	if expiresAt.Valid {
		expiresAtText = expiresAt.String
	}
	expired, err := isoTimeExpired(expiresAtText, m.now().UnixMilli())
	if err != nil {
		return nil, err
	}
	if expired || status != "active" {
		return nil, nil
	}

	bindings, err := m.loadActiveGatewayAPIKeyGroupBindings(ctx, id, routeStrategyID, systemAccountID)
	if err != nil {
		return nil, err
	}
	if len(bindings) == 0 {
		return nil, nil
	}

	row := &GatewayAPIKeyRow{
		ID:                                  id,
		SystemAccountID:                     systemAccountID,
		RouteStrategyID:                     routeStrategyID,
		RouteStrategyMode:                   NormalizeRouteStrategyMode(routeStrategyMode),
		RouteStrategyConfigJSON:             nullToStrPtr(routeConfigJSON),
		Status:                              status,
		ExpiresAt:                           nullToStrPtr(expiresAt),
		QuotaLimitsJSON:                     nullToStrPtr(quotaLimitsJSON),
		SystemAccountImageGenerationEnabled: imageGenerationEnabled,
		SystemAccountRequestLimitsJSON:      nullToStrPtr(requestLimitsJSON),
		SystemAccountRequestLimits:          parseUserRequestLimitsJSON(requestLimitsJSON),
		GroupBindings:                       bindings,
	}
	row.SelectedGroupID = bindings[0].GroupID
	if routeConfigJSON.Valid && strings.TrimSpace(routeConfigJSON.String) != "" {
		if row.RouteStrategyMode == RouteStrategyModeNormal {
			row.NormalRoutingConfig = decodeNormalRoutingConfig(routeConfigJSON.String)
		}
		if row.RouteStrategyMode == RouteStrategyModeHybridSmart {
			row.HybridRoutingConfig = decodeHybridRoutingConfig(routeConfigJSON.String)
		}
	}
	return row, nil
}

// loadActiveGatewayAPIKeyGroupBindings mirrors
// loadActiveGatewayApiKeyGroupBindings: enabled groups visible to the owner,
// active bindings, authorization-aware, priority/created/id order.
func (m *SQLReadModels) loadActiveGatewayAPIKeyGroupBindings(ctx context.Context, apiKeyID, routeStrategyID, systemAccountID string) ([]GatewayAPIKeyGroupBindingRow, error) {
	nowISO := m.now().UTC().Format("2006-01-02T15:04:05.000") + "Z"
	rows, err := m.db.QueryContext(ctx, m.bind(`SELECT
			route_strategy_groups.id,
			route_strategy_groups.group_id,
			route_strategy_groups.priority,
			route_strategy_groups.weight,
			route_strategy_groups.status,
			groups.provider_code,
			groups.enabled
		FROM `+m.table("route_strategies")+` route_strategies
		INNER JOIN `+m.table("route_strategy_groups")+` route_strategy_groups
			ON route_strategy_groups.route_strategy_id = route_strategies.id
			AND route_strategy_groups.system_account_id = route_strategies.system_account_id
		INNER JOIN `+m.table("groups")+` groups
			ON groups.id = route_strategy_groups.group_id
		LEFT JOIN `+m.table("resource_authorizations")+` group_authorization
			ON group_authorization.resource_type = 'group'
			AND group_authorization.resource_id = groups.id
			AND group_authorization.grantee_system_account_id = route_strategy_groups.system_account_id
			AND group_authorization.status = 'active'
			AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
		LEFT JOIN `+m.table("group_authorization_settings")+` group_authorization_settings
			ON group_authorization_settings.authorization_id = group_authorization.id
			AND group_authorization_settings.system_account_id = route_strategy_groups.system_account_id
			AND group_authorization_settings.group_id = groups.id
		WHERE route_strategies.id = ?
			AND route_strategies.system_account_id = ?
			AND route_strategies.status = 'active'
			AND route_strategy_groups.status = 'active'
			AND groups.enabled = 1
			AND (
				groups.system_account_id = route_strategy_groups.system_account_id
				OR (group_authorization.id IS NOT NULL AND COALESCE(group_authorization_settings.enabled, 1) = 1)
			)
		ORDER BY route_strategy_groups.priority ASC, route_strategy_groups.created_at ASC, route_strategy_groups.id ASC
		LIMIT ?`), nowISO, routeStrategyID, systemAccountID, maxRouteStrategyGroupBindings)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bindings := []GatewayAPIKeyGroupBindingRow{}
	for rows.Next() {
		var binding GatewayAPIKeyGroupBindingRow
		var weight sql.NullInt64
		var groupEnabled int
		if err := rows.Scan(&binding.ID, &binding.GroupID, &binding.Priority, &weight,
			&binding.Status, &binding.ProviderCode, &groupEnabled); err != nil {
			return nil, err
		}
		// normalizeApiKeyGroupBindingWeight: NULL 归一为 1，越界值抛错。
		normalizedWeight := 1
		if weight.Valid {
			normalizedWeight, err = normalizeAPIKeyGroupBindingWeight(int(weight.Int64))
			if err != nil {
				return nil, err
			}
		}
		binding.Weight = normalizedWeight
		binding.APIKeyID = apiKeyID
		binding.SystemAccountID = systemAccountID
		binding.GroupEnabled = groupEnabled
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return bindings, nil
}

// maxRouteStrategyGroupBindings mirrors route-strategy-group-binding-limits.
const maxRouteStrategyGroupBindings = 20

// normalizeAPIKeyGroupBindingWeight mirrors normalizeApiKeyGroupBindingWeight.
func normalizeAPIKeyGroupBindingWeight(value int) (int, error) {
	if value < 1 || value > 100 {
		return 0, errors.New("策略路由分组权重必须是 1-100 之间的整数")
	}
	return value, nil
}

// normalizeGatewayAPIKeyGroupBindings mirrors
// normalizeGatewayApiKeyGroupBindings: active + enabled only, normalized
// weights, priority then group id order.
func normalizeGatewayAPIKeyGroupBindings(bindings []GatewayAPIKeyGroupBindingRow) []GatewayAPIKeyGroupBindingRow {
	out := make([]GatewayAPIKeyGroupBindingRow, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Status != "active" || binding.GroupEnabled == 0 {
			continue
		}
		next := binding
		if next.Weight < 1 || next.Weight > 100 {
			next.Weight = 1
		}
		out = append(out, next)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].GroupID < out[j].GroupID
	})
	return out
}

// uniqueGroupIDs mirrors the candidate group id dedupe.
func uniqueGroupIDs(bindings []GatewayAPIKeyGroupBindingRow) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, binding := range bindings {
		if binding.GroupID == "" || seen[binding.GroupID] {
			continue
		}
		seen[binding.GroupID] = true
		out = append(out, binding.GroupID)
	}
	return out
}

// parseUserRequestLimitsJSON mirrors parseUserRequestLimitsJson: any invalid
// shape reads as undefined (the override simply does not apply).
func parseUserRequestLimitsJSON(raw sql.NullString) *UserRequestLimits {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw.String), &decoded); err != nil {
		return nil
	}
	limits := UserRequestLimits{}
	hasWindow := false
	window := func(key string) *int64 {
		value, ok := decoded[key].(float64)
		if !ok || value != float64(int64(value)) || value < 0 || value > 1_000_000_000 {
			return nil
		}
		parsed := int64(value)
		return &parsed
	}
	limits.PerMinute = window("perMinute")
	limits.PerDay = window("perDay")
	limits.PerWeek = window("perWeek")
	limits.PerMonth = window("perMonth")
	if limits.PerMinute != nil || limits.PerDay != nil || limits.PerWeek != nil || limits.PerMonth != nil {
		hasWindow = true
	}
	if !hasWindow {
		return nil
	}
	if expiresOn, ok := decoded["expiresOn"].(string); ok && expiresOn != "" {
		value := expiresOn
		limits.ExpiresOn = &value
	}
	return &limits
}

// decodeNormalRoutingConfig mirrors parseRouteStrategyRuntimeConfigJson for
// the normal branch: the stored object rides along verbatim with the
// scheduling preference extracted for the clone rule.
func decodeNormalRoutingConfig(raw string) *RouteStrategyNormalRoutingConfig {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil
	}
	normalRaw, ok := decoded["normalRoutingConfig"]
	if !ok || normalRaw == nil {
		return &RouteStrategyNormalRoutingConfig{SchedulingPreference: "cost_first"}
	}
	normal, ok := normalRaw.(map[string]any)
	if !ok {
		return &RouteStrategyNormalRoutingConfig{SchedulingPreference: "cost_first"}
	}
	preference := ""
	if text, ok := normal["schedulingPreference"].(string); ok {
		preference = text
	}
	if preference == "" {
		preference = "cost_first"
	}
	encoded, err := json.Marshal(normal)
	if err != nil {
		return &RouteStrategyNormalRoutingConfig{SchedulingPreference: preference}
	}
	return &RouteStrategyNormalRoutingConfig{SchedulingPreference: preference, Raw: encoded}
}

// decodeHybridRoutingConfig mirrors parseRouteStrategyRuntimeConfigJson for
// the hybrid branch (opaque snapshot carrier).
func decodeHybridRoutingConfig(raw string) *ApiKeyHybridRoutingConfig {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil
	}
	hybridRaw, ok := decoded["hybridRoutingConfig"]
	if !ok || hybridRaw == nil {
		return nil
	}
	encoded, err := json.Marshal(hybridRaw)
	if err != nil {
		return nil
	}
	return &ApiKeyHybridRoutingConfig{Raw: encoded}
}
