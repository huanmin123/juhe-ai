// Deterministic mock payloads for the built-in test token, ported from
// external-public-account-push.mock.ts and
// external-public-route-strategy.mock.ts. The mock path never touches the
// resource tables; the username fallback is "huanmin", the group fallback is
// "福利" (account) / "公开接口分组" (group).
package aipublic

import (
	"net/http"
	"strings"
)

const mockSystemAccountID = "mock_system_account_huanmin"

// textFrom returns the trimmed string value of a body field ("" otherwise),
// matching the mock files' normalizedText(input.x) ?? fallback reads.
func textFrom(body map[string]any, key string) string {
	raw, exists := body[key]
	if !exists {
		return ""
	}
	return normalizedText(raw)
}

// optionalTextPointer mirrors description: normalizedText(input.description)
// (absent or blank renders as undefined/omitted).
func optionalTextPointer(body map[string]any, key string) *string {
	if text := textFrom(body, key); text != "" {
		return &text
	}
	return nil
}

func optionalObject(body map[string]any, key string) any {
	raw, exists := body[key]
	if !exists || raw == nil {
		return nil
	}
	if object, isObject := raw.(map[string]any); isObject {
		return object
	}
	return nil
}

func strPtr(value string) *string { return &value }

// bindingsFromBody mirrors the mock bindings projection
// (input.groupBindings ?? default, each rendered as mock_route_strategy_group_N).
func bindingsFromBody(body map[string]any) []PublicBindingSummary {
	raw, exists := body["groupBindings"]
	if !exists || raw == nil {
		return nil
	}
	items, isList := raw.([]any)
	if !isList || len(items) == 0 {
		return nil
	}
	out := make([]PublicBindingSummary, 0, len(items))
	for index, item := range items {
		record, isObject := item.(map[string]any)
		if !isObject {
			continue
		}
		groupID := textFrom(record, "groupId")
		if groupID == "" {
			groupID = "mock_group_public"
		}
		priority := index + 1
		if value, ok := numberFrom(record["priority"]); ok {
			priority = value
		}
		weight := 100
		if value, ok := numberFrom(record["weight"]); ok {
			weight = value
		}
		status := textFrom(record, "status")
		if status == "" {
			status = "active"
		}
		out = append(out, PublicBindingSummary{
			ID: "mock_route_strategy_group_" + itoa(index+1), GroupID: groupID,
			GroupName: strPtr("公开接口分组" + itoa(index+1)), ProviderCode: strPtr("mock_provider"),
			Priority: priority, Weight: weight, Status: status, GroupEnabled: true,
		})
	}
	return out
}

// mockSupportedModels mirrors normalizedStringList(input.supportedModels).
func mockSupportedModels(body map[string]any) []string {
	raw, exists := body["supportedModels"]
	if !exists || raw == nil {
		return nil
	}
	items, isList := raw.([]any)
	if !isList {
		return nil
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		if text, isString := item.(string); isString {
			values = append(values, text)
		}
	}
	normalized := normalizedStringList(values)
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func mockTarget(username string) PublicTarget {
	return PublicTarget{Username: username, DisplayName: username, SystemAccountID: mockSystemAccountID}
}

func mockUsername(input string) string {
	if trimmed := normalizedText(input); trimmed != "" {
		return trimmed
	}
	return "huanmin"
}

func (d *Deps) mockGroupAdd(w http.ResponseWriter, body map[string]any) {
	name := textFrom(body, "name")
	if name == "" {
		name = "公开接口分组"
	}
	providerCode := textFrom(body, "providerCode")
	if providerCode == "" {
		// Node mockPublicGroupAdd calls requiredProviderCode -> the route
		// renders 400 供应商编码不能为空.
		kernelWriteBadRequest(w, "供应商编码不能为空")
		return
	}
	groupType := textFrom(body, "groupType")
	if groupType == "" {
		groupType = "personal"
	}
	enabled := true
	if raw, exists := body["enabled"]; exists {
		if flag, isBool := raw.(bool); isBool {
			enabled = flag
		}
	}
	group := PublicGroupSummary{
		ID: "mock_group_public", Name: name, ProviderCode: providerCode,
		Description: optionalTextPointer(body, "description"),
		Enabled:     enabled, GroupType: groupType, IsDefault: false,
	}
	d.writeMockEnvelope(w, http.StatusOK, map[string]any{
		"action": "mock", "target": mockTarget(mockUsername(textFrom(body, "targetUsername"))), "group": group,
	})
}

func (d *Deps) mockGroupUpdate(w http.ResponseWriter, body map[string]any) {
	name := textFrom(body, "name")
	if name == "" {
		name = "公开接口分组"
	}
	groupID := textFrom(body, "groupId")
	if groupID == "" {
		groupID = "mock_group_public"
	}
	providerCode := textFrom(body, "providerCode")
	groupType := textFrom(body, "groupType")
	if groupType == "" {
		groupType = "personal"
	}
	enabled := true
	if raw, exists := body["enabled"]; exists {
		if flag, isBool := raw.(bool); isBool {
			enabled = flag
		}
	}
	group := PublicGroupSummary{
		ID: groupID, Name: name, ProviderCode: normalizedText(providerCode),
		Description: optionalTextPointer(body, "description"),
		Enabled:     enabled, GroupType: groupType, IsDefault: false,
	}
	d.writeMockEnvelope(w, http.StatusOK, map[string]any{
		"action": "mock", "target": mockTarget(mockUsername(textFrom(body, "targetUsername"))), "group": group,
	})
}

func (d *Deps) mockGroupDelete(w http.ResponseWriter, body map[string]any) {
	groupID := textFrom(body, "groupId")
	if groupID == "" {
		groupID = "mock_group_public"
	}
	group := PublicGroupSummary{
		ID: groupID, Name: "公开接口分组", ProviderCode: "mock_provider",
		Enabled: true, GroupType: "personal", IsDefault: false,
	}
	d.writeMockEnvelope(w, http.StatusOK, map[string]any{
		"action": "mock", "target": mockTarget(mockUsername(textFrom(body, "targetUsername"))), "group": group,
	})
}

func (d *Deps) mockGroupList(w http.ResponseWriter, query map[string]string, page, pageSize int) {
	username := mockUsername(query["targetUsername"])
	keyword := query["keyword"]
	if keyword == "" {
		keyword = "公开接口分组"
	}
	providerCode := query["providerCode"]
	if providerCode == "" {
		providerCode = "mock_provider"
	}
	group := PublicGroupSummary{
		ID: "mock_group_public", Name: keyword, ProviderCode: providerCode,
		Enabled: true, GroupType: "personal", IsDefault: false,
	}
	d.writeMockEnvelope(w, http.StatusOK, map[string]any{
		"target":         mockTarget(username),
		"page":           page,
		"pageSize":       pageSize,
		"pageUpperBound": 1,
		"hasMore":        false,
		"items":          []PublicGroupSummary{group},
	})
}

func (d *Deps) mockStrategyAdd(w http.ResponseWriter, body map[string]any) {
	name := textFrom(body, "name")
	if name == "" {
		name = "公开接口策略路由"
	}
	mode := textFrom(body, "mode")
	if mode == "" {
		mode = "normal"
	}
	status := textFrom(body, "status")
	if status == "" {
		status = "active"
	}
	summary := mockStrategySummary("mock_route_strategy_public", name, mode, status,
		bindingsFromBody(body), optionalObject(body, "normalRoutingConfig"))
	d.writeMockEnvelope(w, http.StatusOK, map[string]any{
		"action": "mock", "target": mockTarget(mockUsername(textFrom(body, "targetUsername"))),
		"routeStrategy": summary,
	})
}

func (d *Deps) mockStrategyUpdate(w http.ResponseWriter, body map[string]any) {
	strategyID := textFrom(body, "routeStrategyId")
	if strategyID == "" {
		strategyID = "mock_route_strategy_public"
	}
	name := textFrom(body, "name")
	if name == "" {
		name = "公开接口策略路由"
	}
	mode := textFrom(body, "mode")
	if mode == "" {
		mode = "normal"
	}
	status := textFrom(body, "status")
	if status == "" {
		status = "active"
	}
	summary := mockStrategySummary(strategyID, name, mode, status,
		bindingsFromBody(body), optionalObject(body, "normalRoutingConfig"))
	d.writeMockEnvelope(w, http.StatusOK, map[string]any{
		"action": "mock", "target": mockTarget(mockUsername(textFrom(body, "targetUsername"))),
		"routeStrategy": summary,
	})
}

func (d *Deps) mockStrategyDelete(w http.ResponseWriter, body map[string]any) {
	strategyID := textFrom(body, "routeStrategyId")
	if strategyID == "" {
		strategyID = "mock_route_strategy_public"
	}
	summary := mockStrategySummary(strategyID, "公开接口策略路由", "normal", "disabled", nil, nil)
	d.writeMockEnvelope(w, http.StatusOK, map[string]any{
		"action": "mock", "target": mockTarget(mockUsername(textFrom(body, "targetUsername"))),
		"routeStrategy": summary,
	})
}

func (d *Deps) mockStrategyList(w http.ResponseWriter, query map[string]string, page, pageSize int) {
	username := mockUsername(query["targetUsername"])
	keyword := query["keyword"]
	if keyword == "" {
		keyword = "公开接口策略路由"
	}
	mode := query["mode"]
	if mode == "" || mode == "all" {
		mode = "normal"
	}
	status := query["status"]
	if status == "" || status == "all" {
		status = "active"
	}
	summary := mockStrategySummary("mock_route_strategy_public", keyword, mode, status, nil, nil)
	d.writeMockEnvelope(w, http.StatusOK, map[string]any{
		"target":         mockTarget(username),
		"page":           page,
		"pageSize":       pageSize,
		"pageUpperBound": 1,
		"hasMore":        false,
		"items":          []PublicStrategySummary{summary},
	})
}

// mockStrategySummary mirrors mockRouteStrategySummary: default bindings and
// the normal-mode default normalRoutingConfig.
func mockStrategySummary(id, name, mode, status string, bindings []PublicBindingSummary, normalConfig any) PublicStrategySummary {
	if len(bindings) == 0 {
		bindings = []PublicBindingSummary{{
			ID: "mock_route_strategy_group_1", GroupID: "mock_group_public",
			GroupName: strPtr("公开接口分组1"), ProviderCode: strPtr("mock_provider"),
			Priority: 1, Weight: 100, Status: "active", GroupEnabled: true,
		}}
	}
	normal := normalConfig
	if mode == "normal" && normal == nil {
		normal = map[string]any{"schedulingPreference": "cost_first"}
	}
	if mode != "normal" {
		normal = nil
	}
	return PublicStrategySummary{
		ID: id, Name: name, Mode: mode, Status: status, IsDefault: false,
		NormalRoutingConfig: normal,
		GroupBindings:       bindings,
		APIKeyCount:         0,
	}
}

func (d *Deps) mockApiKeyAdd(w http.ResponseWriter, body map[string]any) {
	name := textFrom(body, "name")
	if name == "" {
		name = "公开接口 API Key"
	}
	status := textFrom(body, "status")
	if status == "" {
		status = "active"
	}
	if status != "disabled" {
		status = "active"
	}
	routeStrategyID := textFrom(body, "routeStrategyId")
	if routeStrategyID == "" {
		routeStrategyID = "mock_route_strategy_public"
	}
	expiresAt := optionalTextPointer(body, "expiresAt")
	summary := PublicApiKeySummary{
		ID: "mock_api_key_public", Name: name, KeyPrefix: "juis_mock",
		Key: strPtr("juis_mock_public_api_key"), Status: status,
		RouteStrategyID: routeStrategyID, RouteStrategyName: strPtr("公开接口策略路由"),
		RouteStrategyMode: strPtr("normal"), RouteStrategyStatus: strPtr("active"),
		ExpiresAt: expiresAt,
	}
	d.writeMockEnvelope(w, http.StatusOK, map[string]any{
		"action": "mock", "target": mockTarget(mockUsername(textFrom(body, "targetUsername"))), "apiKey": summary,
	})
}

func (d *Deps) mockApiKeyUpdate(w http.ResponseWriter, body map[string]any) {
	apiKeyID := textFrom(body, "apiKeyId")
	if apiKeyID == "" {
		apiKeyID = "mock_api_key_public"
	}
	name := textFrom(body, "name")
	if name == "" {
		name = "公开接口 API Key"
	}
	status := textFrom(body, "status")
	if status != "disabled" {
		status = "active"
	}
	routeStrategyID := textFrom(body, "routeStrategyId")
	if routeStrategyID == "" {
		routeStrategyID = "mock_route_strategy_public"
	}
	summary := PublicApiKeySummary{
		ID: apiKeyID, Name: name, KeyPrefix: "juis_mock", Status: status,
		RouteStrategyID: routeStrategyID, RouteStrategyName: strPtr("公开接口策略路由"),
		RouteStrategyMode: strPtr("normal"), RouteStrategyStatus: strPtr("active"),
		ExpiresAt: optionalTextPointer(body, "expiresAt"),
	}
	d.writeMockEnvelope(w, http.StatusOK, map[string]any{
		"action": "mock", "target": mockTarget(mockUsername(textFrom(body, "targetUsername"))), "apiKey": summary,
	})
}

func (d *Deps) mockApiKeyDelete(w http.ResponseWriter, body map[string]any) {
	apiKeyID := textFrom(body, "apiKeyId")
	if apiKeyID == "" {
		apiKeyID = "mock_api_key_public"
	}
	summary := PublicApiKeySummary{
		ID: apiKeyID, Name: "公开接口 API Key", KeyPrefix: "juis_mock", Status: "disabled",
		RouteStrategyID: "mock_route_strategy_public", RouteStrategyName: strPtr("公开接口策略路由"),
		RouteStrategyMode: strPtr("normal"), RouteStrategyStatus: strPtr("active"),
	}
	d.writeMockEnvelope(w, http.StatusOK, map[string]any{
		"action": "mock", "target": mockTarget(mockUsername(textFrom(body, "targetUsername"))), "apiKey": summary,
	})
}

func (d *Deps) mockApiKeyList(w http.ResponseWriter, query map[string]string, page, pageSize int) {
	username := mockUsername(query["targetUsername"])
	keyword := query["keyword"]
	if keyword == "" {
		keyword = "公开接口 API Key"
	}
	status := query["status"]
	if status != "disabled" {
		status = "active"
	}
	routeStrategyID := query["routeStrategyId"]
	name := "公开接口策略路由"
	itemID := "mock_api_key_public"
	if routeStrategyID != "" {
		name = "公开接口指定策略路由"
		itemID = "mock_api_key_public_bound"
	} else {
		routeStrategyID = "mock_route_strategy_public"
	}
	summary := PublicApiKeySummary{
		ID: itemID, Name: keyword, KeyPrefix: "juis_mock", Status: status,
		RouteStrategyID: routeStrategyID, RouteStrategyName: strPtr(name),
		RouteStrategyMode: strPtr("normal"), RouteStrategyStatus: strPtr("active"),
	}
	d.writeMockEnvelope(w, http.StatusOK, map[string]any{
		"target":         mockTarget(username),
		"page":           page,
		"pageSize":       pageSize,
		"pageUpperBound": 1,
		"hasMore":        false,
		"items":          []PublicApiKeySummary{summary},
	})
}

func (d *Deps) mockAccountPush(w http.ResponseWriter, body map[string]any) {
	username := mockUsername(textFrom(body, "targetUsername"))
	displayName := username
	if raw, exists := body["targetDisplayName"]; exists {
		if text, isString := raw.(string); isString && strings.TrimSpace(text) != "" {
			displayName = strings.TrimSpace(text)
		}
	}
	groupName := textFrom(body, "targetGroupName")
	if groupName == "" {
		groupName = "福利"
	}
	providerCode := textFrom(body, "providerCode")
	if providerCode == "" {
		providerCode = "gpt"
	}
	profileID := textFrom(body, "providerProtocolProfileId")
	if profileID == "" {
		profileID = "profile_gpt_openai_v1"
	}
	accountName := textFrom(body, "name")
	if accountName == "" {
		accountName = "公益站测试账号"
	}
	status := textFrom(body, "status")
	schedulable := status != "disabled"
	if status == "" {
		status = "active"
	}
	account := PublicAccountSummary{
		ID: "mock_account_public_welfare", Name: accountName,
		ProviderCode: providerCode, ProviderProtocolProfileID: strPtr(profileID),
		ProtocolCode: strPtr("openai"), ProtocolVersion: strPtr("v1"),
		Type: "api_key", ClientCompatibility: "openai_standard", Status: status,
		SupportedModels: mockSupportedModels(body),
		BoundGroupID:    strPtr("mock_group_welfare"), BoundGroupName: strPtr(groupName),
		Schedulable: schedulable,
	}
	target := PublicGroupTarget{
		PublicTarget: PublicTarget{Username: username, DisplayName: displayName, SystemAccountID: mockSystemAccountID},
		GroupID:      "mock_group_welfare", GroupName: groupName,
	}
	d.writeMockEnvelope(w, http.StatusOK, map[string]any{"action": "mock", "target": target, "account": account})
}

func (d *Deps) mockAccountDelete(w http.ResponseWriter, body map[string]any) {
	username := mockUsername(textFrom(body, "targetUsername"))
	groupName := textFrom(body, "targetGroupName")
	if groupName == "" {
		groupName = "福利"
	}
	providerCode := textFrom(body, "providerCode")
	if providerCode == "" {
		providerCode = "gpt"
	}
	profileID := textFrom(body, "providerProtocolProfileId")
	if profileID == "" {
		profileID = "profile_gpt_openai_v1"
	}
	accountID := textFrom(body, "accountId")
	if accountID == "" {
		accountID = "mock_account_public_welfare"
	}
	account := PublicAccountSummary{
		ID: accountID, Name: accountID,
		ProviderCode: providerCode, ProviderProtocolProfileID: strPtr(profileID),
		ProtocolCode: strPtr("openai"), ProtocolVersion: strPtr("v1"),
		Type: "api_key", ClientCompatibility: "openai_standard", Status: "disabled",
		BoundGroupID: strPtr("mock_group_welfare"), BoundGroupName: strPtr(groupName),
		Schedulable: false,
	}
	target := PublicGroupTarget{
		PublicTarget: mockTarget(username),
		GroupID:      "mock_group_welfare", GroupName: groupName,
	}
	d.writeMockEnvelope(w, http.StatusOK, map[string]any{"action": "mock", "target": target, "account": account})
}

func (d *Deps) mockAccountList(w http.ResponseWriter, query map[string]string, page, pageSize int) {
	username := mockUsername(query["targetUsername"])
	groupName := query["targetGroupName"]
	if groupName == "" {
		groupName = "福利"
	}
	providerCode := query["providerCode"]
	if providerCode == "" {
		providerCode = "mock_provider"
	}
	profileID := query["providerProtocolProfileId"]
	if profileID == "" {
		profileID = "profile_gpt_openai_v1"
	}
	keyword := query["keyword"]
	if keyword == "" {
		keyword = "公益站测试账号"
	}
	boundGroupID := query["groupId"]
	if boundGroupID == "" {
		boundGroupID = "mock_group_welfare"
	}
	item := PublicAccountListItem{
		PublicAccountSummary: PublicAccountSummary{
			ID: "mock_account_public_welfare", Name: keyword,
			ProviderCode: providerCode, ProviderProtocolProfileID: strPtr(profileID),
			ProtocolCode: strPtr("openai"), ProtocolVersion: strPtr("v1"),
			Type: "api_key", ClientCompatibility: "openai_standard", Status: "active",
			SupportedModels: []string{"gpt-5.5"},
			BoundGroupID:    strPtr(boundGroupID), BoundGroupName: strPtr(groupName),
			Schedulable: true,
		},
		ConcurrencyLimit: 20,
		Priority:         0,
	}
	d.writeMockEnvelope(w, http.StatusOK, map[string]any{
		"target":         mockTarget(username),
		"page":           page,
		"pageSize":       pageSize,
		"pageUpperBound": 1,
		"hasMore":        false,
		"items":          []PublicAccountListItem{item},
	})
}
