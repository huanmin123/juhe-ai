// external_apidocs.go mirrors the static external public API catalog served by
// GET /external-integration-sources/api-docs (backend/src/modules/
// external-integrations/external-public-api-catalog*.ts). The composition
// helpers mirror the Node builder functions so the produced document keeps
// the same shape, ordering and example values.
package policyreads

import "encoding/json"

// catalogValue wraps an optional example so `false`, `0` and `null` examples
// survive marshaling while absent examples stay omitted.
type catalogValue struct{ value any }

func (c catalogValue) MarshalJSON() ([]byte, error) { return json.Marshal(c.value) }

func catalogExample(value any) *catalogValue { return &catalogValue{value} }

type catalogField struct {
	Name        string        `json:"name"`
	Type        string        `json:"type"`
	Required    bool          `json:"required"`
	Description string        `json:"description"`
	Example     *catalogValue `json:"example,omitempty"`
}

type catalogHeader struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
	Example     string `json:"example"`
}

type catalogBody struct {
	ContentType string         `json:"contentType"`
	Fields      []catalogField `json:"fields"`
	Example     any            `json:"example"`
}

type catalogItem struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Summary         string          `json:"summary"`
	Status          string          `json:"status"`
	Method          string          `json:"method"`
	Path            string          `json:"path"`
	Headers         []catalogHeader `json:"headers"`
	Query           []catalogField  `json:"query"`
	RequestBody     *catalogBody    `json:"requestBody,omitempty"`
	ResponseExample any             `json:"responseExample"`
	ResponseFields  []catalogField  `json:"responseFields"`
	Scope           string          `json:"scope,omitempty"`
}

type externalAPICatalog struct {
	BasePath string        `json:"basePath"`
	AuthType string        `json:"authType"`
	Items    []catalogItem `json:"items"`
}

func catalogFieldOf(name, typ string, required bool, description string) catalogField {
	return catalogField{Name: name, Type: typ, Required: required, Description: description}
}

func catalogFieldEx(name, typ string, required bool, description string, example ...any) catalogField {
	if len(example) == 0 {
		return catalogField{Name: name, Type: typ, Required: required, Description: description}
	}
	return catalogField{Name: name, Type: typ, Required: required, Description: description, Example: catalogExample(example[0])}
}

func catalogGeneratedFields() []catalogField {
	return []catalogField{
		catalogFieldEx("data.source", "string", true, "数据来源：stats 表示正式统计或控制面数据，mock 表示内置测试 token 模拟数据。", "stats"),
		catalogFieldEx("data.generatedAt", "string", true, "响应生成时间，ISO 8601 字符串。", "2026-05-30T00:00:00.000Z"),
	}
}

func catalogPageFields() []catalogField {
	return []catalogField{
		catalogFieldEx("data.page", "number", true, "当前页码。", 1),
		catalogFieldEx("data.pageSize", "number", true, "当前每页数量。", 20),
		catalogFieldEx("data.pageUpperBound", "number", true, "分页上界，用于前端翻页；不是精确总数。", 1),
		catalogFieldEx("data.hasMore", "boolean", true, "是否还有下一页。", false),
	}
}

func catalogTargetFields(prefix string) []catalogField {
	return []catalogField{
		catalogFieldEx(prefix+".username", "string", true, "目标系统用户账号。", "huanmin"),
		catalogFieldEx(prefix+".displayName", "string", true, "目标系统用户显示名称。", "huanmin"),
		catalogFieldEx(prefix+".systemAccountId", "string", true, "目标系统用户在 sub2api-lite 内的 ID。", "sysacc_xxx"),
		catalogFieldEx(prefix+".created", "boolean", true, "本次调用是否自动创建了目标系统用户。", false),
	}
}

func catalogTargetWithGroupFields(prefix string) []catalogField {
	return append(catalogTargetFields(prefix),
		catalogFieldEx(prefix+".groupId", "string", true, "目标分组 ID。", "grp_xxx"),
		catalogFieldEx(prefix+".groupName", "string", true, "目标分组名称。", "福利"),
		catalogFieldEx(prefix+".groupCreated", "boolean", true, "本次调用是否自动创建了目标分组。", false),
	)
}

func catalogGroupFields(prefix string) []catalogField {
	return []catalogField{
		catalogFieldEx(prefix+".id", "string", false, "分组 ID；对象为 null 时没有该字段。", "grp_xxx"),
		catalogFieldEx(prefix+".name", "string", false, "分组名称。", "福利"),
		catalogFieldEx(prefix+".providerCode", "string", false, "供应商编码。", vendorCodeGPT),
		catalogFieldEx(prefix+".description", "string", false, "分组说明；未填写时缺省。", "公益站账号分组"),
		catalogFieldEx(prefix+".enabled", "boolean", false, "分组是否启用。", true),
		catalogFieldEx(prefix+".groupType", "string", false, "分组类型：personal 或 high_concurrency。", "personal"),
		catalogFieldEx(prefix+".isDefault", "boolean", false, "是否默认分组。", false),
	}
}

func catalogRouteStrategyFields(prefix string) []catalogField {
	return []catalogField{
		catalogFieldEx(prefix+".id", "string", false, "路由策略 ID；对象为 null 时没有该字段。", "rts_xxx"),
		catalogFieldEx(prefix+".name", "string", false, "路由策略名称。", "公益站默认路由"),
		catalogFieldEx(prefix+".description", "string", false, "路由策略说明；未填写时缺省。"),
		catalogFieldEx(prefix+".mode", "string", false, "路由模式：normal、hybrid_smart、weighted、failover 或 round_robin。", "normal"),
		catalogFieldEx(prefix+".status", "string", false, "路由策略状态：active 或 disabled。", "active"),
		catalogFieldEx(prefix+".isDefault", "boolean", false, "是否默认路由策略。", false),
		catalogFieldEx(prefix+".apiKeyCount", "number", false, "绑定该路由策略的 API Key 数量。", 1),
		catalogFieldEx(prefix+".hybridRoutingConfig", "object", false, "混合智能路由配置；非 hybrid_smart 模式通常缺省。"),
		catalogFieldEx(prefix+".groupBindings", "array", false, "路由策略绑定的分组列表。"),
		catalogFieldEx(prefix+".groupBindings[].id", "string", false, "路由策略分组绑定 ID。", "rsg_xxx"),
		catalogFieldEx(prefix+".groupBindings[].groupId", "string", false, "绑定分组 ID。", "grp_xxx"),
		catalogFieldEx(prefix+".groupBindings[].groupName", "string", false, "绑定分组名称。", "福利"),
		catalogFieldEx(prefix+".groupBindings[].providerCode", "string", false, "绑定分组供应商编码。", vendorCodeGPT),
		catalogFieldEx(prefix+".groupBindings[].priority", "number", false, "故障回退或轮询等模式使用的优先级。", 1),
		catalogFieldEx(prefix+".groupBindings[].weight", "number", false, "权重调度模式使用的权重，范围 1 到 100。", 100),
		catalogFieldEx(prefix+".groupBindings[].status", "string", false, "绑定状态：active 或 disabled。", "active"),
		catalogFieldEx(prefix+".groupBindings[].groupEnabled", "boolean", false, "绑定分组当前是否启用。", true),
		catalogFieldEx(prefix+".createdAt", "string", false, "创建时间，ISO 8601 字符串。", "2026-05-30T00:00:00.000Z"),
		catalogFieldEx(prefix+".updatedAt", "string", false, "更新时间，ISO 8601 字符串。", "2026-05-30T00:00:00.000Z"),
	}
}

func catalogAPIKeyFields(prefix string) []catalogField {
	return []catalogField{
		catalogFieldEx(prefix+".id", "string", false, "API Key ID；对象为 null 时没有该字段。", "key_xxx"),
		catalogFieldEx(prefix+".name", "string", false, "API Key 名称。", "公益站访问密钥"),
		catalogFieldEx(prefix+".keyPrefix", "string", false, "API Key 前缀，用于展示和对账，不是完整密钥。", "juis_xxx"),
		catalogFieldEx(prefix+".key", "string", false, "新增 API Key 时一次性返回的完整明文密钥；列表、修改、删除响应不会返回。", "juis_xxx_plain_once"),
		catalogFieldEx(prefix+".status", "string", false, "API Key 状态：active 或 disabled。", "active"),
		catalogFieldEx(prefix+".routeStrategyId", "string", false, "API Key 绑定的策略路由 ID。", "rts_xxx"),
		catalogFieldEx(prefix+".routeStrategyName", "string", false, "策略路由名称；无法补齐时可能缺省。", "公益站默认路由"),
		catalogFieldEx(prefix+".routeStrategyMode", "string", false, "策略路由模式。", "normal"),
		catalogFieldEx(prefix+".routeStrategyStatus", "string", false, "策略路由状态：active 或 disabled。", "active"),
		catalogFieldEx(prefix+".expiresAt", "string", false, "API Key 到期时间，ISO 8601 字符串；未设置时缺省。", "2026-12-31T23:59:59.000Z"),
		catalogFieldEx(prefix+".availabilitySchedule", "object", false, "API Key 时间计划；未设置时缺省。计划命中开始 / 结束边界时会直接更新 API Key status。"),
	}
}

func catalogAccountFields(prefix string) []catalogField {
	return []catalogField{
		catalogFieldEx(prefix+".id", "string", false, "AI 账户 ID；对象为 null 时没有该字段。", "acc_xxx"),
		catalogFieldEx(prefix+".name", "string", false, "AI 账户名称。", "公益站-青芽主通道"),
		catalogFieldEx(prefix+".providerCode", "string", false, "供应商编码。", vendorCodeGPT),
		catalogFieldEx(prefix+".providerProtocolProfileId", "string", false, "供应商协议档案；账号元数据缺失或旧记录未补齐时可能缺省。", "profile_gpt_openai_v1"),
		catalogFieldEx(prefix+".protocolCode", "string", false, "协议编码；由供应商协议档案派生。", "openai"),
		catalogFieldEx(prefix+".protocolVersion", "string", false, "协议版本；由供应商协议档案派生。", "v1"),
		catalogFieldEx(prefix+".type", "string", false, "账号类型，公开写接口当前只支持 api_key。", "api_key"),
		catalogFieldEx(prefix+".clientCompatibility", "string", false, "账号内部派生客户端能力摘要，只读返回；客户端画像由网关内部识别，跨协议桥接请使用混合供应商账户。", "openai_standard"),
		catalogFieldEx(prefix+".status", "string", false, "账号状态。", "active"),
		catalogFieldEx(prefix+".supportedModels", "string[]", false, "账号支持的模型列表；未限制或未配置时可能缺省。", []string{"gpt-5.5", "gpt-5.4"}),
		catalogFieldEx(prefix+".boundGroupId", "string", false, "账号绑定分组 ID。", "grp_xxx"),
		catalogFieldEx(prefix+".boundGroupName", "string", false, "账号绑定分组名称。", "福利"),
		catalogFieldEx(prefix+".schedulable", "boolean", false, "账号当前是否可调度。", true),
		catalogFieldEx(prefix+".availabilitySchedule", "object", false, "账号时间计划；未设置时缺省。"),
	}
}

// responseFieldsForCatalogItem mirrors responseFieldsForPublicApiDocItem.
func responseFieldsForCatalogItem(id string) []catalogField {
	fields := []catalogField{}
	appendFields := func(groups ...[]catalogField) {
		for _, group := range groups {
			fields = append(fields, group...)
		}
	}
	switch id {
	case "group-list":
		appendFields(catalogGeneratedFields(), catalogTargetFields("data.target"), catalogPageFields(),
			[]catalogField{catalogFieldEx("data.items", "array", true, "当前页分组列表。")},
			catalogGroupFields("data.items[]"))
	case "group-add", "group-update", "group-delete":
		appendFields(catalogGeneratedFields(),
			[]catalogField{catalogFieldEx("data.action", "string", true, "执行结果：created、existing、updated、deleted 或 mock；分组不存在时正式调用返回 404 错误响应。", "created")},
			catalogTargetFields("data.target"),
			[]catalogField{catalogFieldEx("data.group", "object|null", true, "分组摘要；正式成功响应为对象，错误响应不包在 data 内。")},
			catalogGroupFields("data.group"))
	case "route-strategy-list":
		appendFields(catalogGeneratedFields(), catalogTargetFields("data.target"), catalogPageFields(),
			[]catalogField{catalogFieldEx("data.items", "array", true, "当前页路由策略列表。")},
			catalogRouteStrategyFields("data.items[]"))
	case "route-strategy-add", "route-strategy-update", "route-strategy-delete":
		appendFields(catalogGeneratedFields(),
			[]catalogField{catalogFieldEx("data.action", "string", true, "执行结果：created、updated、deleted 或 mock；路由策略不存在时正式调用返回 404 错误响应。", "created")},
			catalogTargetFields("data.target"),
			[]catalogField{catalogFieldEx("data.routeStrategy", "object|null", true, "路由策略摘要；正式成功响应为对象。")},
			catalogRouteStrategyFields("data.routeStrategy"))
	case "api-key-list":
		appendFields(catalogGeneratedFields(), catalogTargetFields("data.target"), catalogPageFields(),
			[]catalogField{catalogFieldEx("data.items", "array", true, "当前页 API Key 摘要列表。")},
			catalogAPIKeyFields("data.items[]"))
	case "api-key-add", "api-key-update", "api-key-delete":
		appendFields(catalogGeneratedFields(),
			[]catalogField{catalogFieldEx("data.action", "string", true, "执行结果：created、updated、deleted 或 mock；API Key 不存在时正式调用返回 404 错误响应。", "created")},
			catalogTargetFields("data.target"),
			[]catalogField{catalogFieldEx("data.apiKey", "object|null", true, "API Key 摘要；正式成功响应为对象，新增接口仅在该对象内一次性返回 key 明文。")},
			catalogAPIKeyFields("data.apiKey"))
	case "account-list":
		appendFields(catalogGeneratedFields(), catalogTargetFields("data.target"), catalogPageFields(),
			[]catalogField{catalogFieldEx("data.items", "array", true, "当前页 AI 账户脱敏摘要列表。")},
			append(catalogAccountFields("data.items[]"),
				catalogFieldEx("data.items[].concurrencyLimit", "number", true, "单账号并发限制。", 20),
				catalogFieldEx("data.items[].priority", "number", true, "账号调度优先级。", 0)))
	case "account-add", "account-update":
		appendFields(catalogGeneratedFields(),
			[]catalogField{catalogFieldEx("data.action", "string", true, "执行结果：created、updated 或 mock。", "created")},
			catalogTargetWithGroupFields("data.target"),
			[]catalogField{catalogFieldEx("data.account", "object", true, "AI 账户脱敏摘要。")},
			catalogAccountFields("data.account"))
	case "account-delete":
		appendFields(catalogGeneratedFields(),
			[]catalogField{catalogFieldEx("data.action", "string", true, "执行结果：deleted、not_found 或 mock。", "deleted")},
			catalogTargetWithGroupFields("data.target"),
			[]catalogField{catalogFieldEx("data.account", "object|null", true, "已删除账户的脱敏摘要；not_found 时为 null。")},
			catalogAccountFields("data.account"))
	default:
		return []catalogField{}
	}
	return fields
}

// scopeForCatalogItem mirrors scopeForPublicApiDocItem.
func scopeForCatalogItem(id string) string {
	scopesByID := map[string]string{
		"api-key-list":          "juhe_ai_public:api_key_list:read",
		"route-strategy-list":   "juhe_ai_public:route_strategy_list:read",
		"group-list":            "juhe_ai_public:group_list:read",
		"account-list":          "juhe_ai_public:account_list:read",
		"api-key-add":           "juhe_ai_public:api_key_add:write",
		"api-key-update":        "juhe_ai_public:api_key_update:write",
		"api-key-delete":        "juhe_ai_public:api_key_delete:write",
		"route-strategy-add":    "juhe_ai_public:route_strategy_add:write",
		"route-strategy-update": "juhe_ai_public:route_strategy_update:write",
		"route-strategy-delete": "juhe_ai_public:route_strategy_delete:write",
		"group-add":             "juhe_ai_public:group_add:write",
		"group-update":          "juhe_ai_public:group_update:write",
		"group-delete":          "juhe_ai_public:group_delete:write",
		"account-add":           "juhe_ai_public:account_add:write",
		"account-update":        "juhe_ai_public:account_update:write",
		"account-delete":        "juhe_ai_public:account_delete:write",
	}
	return scopesByID[id]
}

var catalogAuthHeader = catalogHeader{
	Name:        "Authorization",
	Required:    true,
	Description: "来源授权 Bearer token。每个公开接口都有独立资源 scope；使用内置测试 token 时接口只返回 mock 数据。",
	Example:     "Bearer <source_token>",
}

var catalogTargetQuery = catalogFieldEx("targetUsername", "string", true, "目标系统用户账号。", "huanmin")

func catalogPageQuery() []catalogField {
	return []catalogField{
		catalogFieldEx("page", "number", false, "分页页码，默认 1。", 1),
		catalogFieldEx("pageSize", "number", false, "每页数量，范围 1 到 100。", 20),
	}
}

// externalPublicAPICatalog mirrors getExternalPublicApiCatalog.
func externalPublicAPICatalog() externalAPICatalog {
	target := map[string]any{"username": "huanmin", "displayName": "huanmin", "systemAccountId": "sysacc_xxx", "created": false}
	targetWithGroup := map[string]any{
		"username": "huanmin", "displayName": "huanmin", "systemAccountId": "sysacc_xxx", "created": false,
		"groupId": "grp_xxx", "groupName": "福利", "groupCreated": false,
	}
	group := map[string]any{
		"id": "grp_xxx", "name": "福利", "providerCode": vendorCodeGPT, "enabled": true,
		"groupType": "personal", "isDefault": false,
	}
	routeStrategy := map[string]any{
		"id": "rts_xxx", "name": "公益站默认路由", "mode": "normal", "status": "active", "isDefault": false,
		"groupBindings": []any{map[string]any{
			"id": "rsg_xxx", "groupId": "grp_xxx", "groupName": "福利", "providerCode": vendorCodeGPT,
			"priority": 1, "weight": 100, "status": "active", "groupEnabled": true,
		}},
		"apiKeyCount": 1,
		"createdAt":   "2026-05-30T00:00:00.000Z", "updatedAt": "2026-05-30T00:00:00.000Z",
	}
	apiKey := map[string]any{
		"id": "key_xxx", "name": "公益站访问密钥", "keyPrefix": "juis_xxx", "status": "active",
		"routeStrategyId": "rts_xxx", "routeStrategyName": "公益站默认路由",
		"routeStrategyMode": "normal", "routeStrategyStatus": "active",
	}
	account := map[string]any{
		"id": "acc_xxx", "name": "公益站-青芽主通道", "providerCode": vendorCodeGPT,
		"providerProtocolProfileId": "profile_gpt_openai_v1", "protocolCode": "openai", "protocolVersion": "v1",
		"type": "api_key", "clientCompatibility": "openai_standard", "status": "active",
		"supportedModels": []any{"gpt-5.5"}, "boundGroupId": "grp_xxx", "boundGroupName": "福利",
		"schedulable": true, "concurrencyLimit": 20, "priority": 0,
	}
	responseEnvelope := func(inner map[string]any) map[string]any {
		payload := map[string]any{
			"source": "stats", "generatedAt": "2026-05-30T00:00:00.000Z",
		}
		for key, value := range inner {
			payload[key] = value
		}
		return map[string]any{"data": payload}
	}
	pageEnvelope := func(items any) map[string]any {
		return responseEnvelope(map[string]any{
			"target": target, "page": 1, "pageSize": 20, "pageUpperBound": 1, "hasMore": false, "items": items,
		})
	}
	accountCreated := map[string]any{}
	for key, value := range account {
		accountCreated[key] = value
	}
	accountCreated["status"] = "pending_test"
	accountCreated["schedulable"] = false
	accountDisabled := map[string]any{}
	for key, value := range account {
		accountDisabled[key] = value
	}
	accountDisabled["status"] = "disabled"
	accountDisabled["schedulable"] = false
	apiKeyDisabled := map[string]any{}
	for key, value := range apiKey {
		apiKeyDisabled[key] = value
	}
	apiKeyDisabled["status"] = "disabled"
	routeStrategyRoundRobin := map[string]any{}
	for key, value := range routeStrategy {
		routeStrategyRoundRobin[key] = value
	}
	routeStrategyRoundRobin["mode"] = "round_robin"
	groupRenamed := map[string]any{}
	for key, value := range group {
		groupRenamed[key] = value
	}
	groupRenamed["name"] = "福利-主池"
	apiKeyWithKey := map[string]any{}
	for key, value := range apiKey {
		apiKeyWithKey[key] = value
	}
	apiKeyWithKey["key"] = "juis_xxx_plain_once"

	items := []catalogItem{
		{
			ID: "api-key-list", Name: "API Key 列表",
			Summary: "分页读取指定系统用户名下的 API Key 摘要和策略路由信息；不会返回 API Key 明文。",
			Status:  "available", Method: "GET", Path: "/__aipublic__/api-key/list",
			Headers: []catalogHeader{catalogAuthHeader},
			Query: append([]catalogField{catalogTargetQuery,
				catalogFieldEx("routeStrategyId", "string", false, "按策略路由 ID 筛选。", "rts_xxx"),
				catalogFieldEx("keyword", "string", false, "按 API Key 名称精确 / 前缀筛选。", "公益站访问密钥"),
				catalogFieldEx("status", "string", false, "状态筛选：active、disabled 或 all。", "active")},
				catalogPageQuery()...),
			ResponseExample: pageEnvelope([]any{apiKey}),
		},
		{
			ID: "api-key-add", Name: "API Key 新增",
			Summary: "为指定系统用户新增 API Key，并绑定一条策略路由；分组绑定和路由模式由策略路由维护。",
			Status:  "available", Method: "POST", Path: "/__aipublic__/api-key/add",
			Headers: []catalogHeader{catalogAuthHeader},
			Query:   []catalogField{},
			RequestBody: &catalogBody{
				ContentType: "application/json",
				Fields: []catalogField{catalogTargetQuery,
					catalogFieldEx("name", "string", true, "API Key 名称。", "公益站访问密钥"),
					catalogFieldEx("description", "string|null", false, "API Key 说明；传 null 表示清空说明。", "公益站后端访问"),
					catalogFieldEx("routeStrategyId", "string", true, "API Key 绑定的策略路由 ID。", "rts_xxx"),
					catalogFieldEx("status", "string", false, "状态：active 或 disabled，默认 active。", "active"),
					catalogFieldEx("expiresAt", "string", false, "API Key 到期时间，ISO 8601 字符串；未填写表示不过期。"),
					catalogFieldEx("quotaLimits", "object|null", false, "请求成本额度限制；传 null 表示清空。"),
					catalogFieldEx("availabilitySchedule", "object|null", false, "时间计划；null 表示清空计划，未填写表示不设置计划。")},
				Example: map[string]any{"targetUsername": "huanmin", "name": "公益站访问密钥", "routeStrategyId": "rts_xxx", "status": "active"},
			},
			ResponseExample: responseEnvelope(map[string]any{"action": "created", "target": target, "apiKey": apiKeyWithKey}),
		},
		{
			ID: "api-key-update", Name: "API Key 修改",
			Summary: "修改指定 API Key 的名称、状态、策略路由绑定、额度或时间计划。",
			Status:  "available", Method: "POST", Path: "/__aipublic__/api-key/update",
			Headers: []catalogHeader{catalogAuthHeader},
			Query:   []catalogField{},
			RequestBody: &catalogBody{
				ContentType: "application/json",
				Fields: []catalogField{
					catalogFieldEx("targetUsername", "string", false, "可选校验条件。提供时必须与 API Key 归属目标用户一致。", "huanmin"),
					catalogFieldEx("apiKeyId", "string", true, "API Key ID。", "key_xxx"),
					catalogFieldEx("name", "string", false, "新的 API Key 名称。"),
					catalogFieldEx("description", "string|null", false, "新的 API Key 说明；传 null 表示清空。"),
					catalogFieldEx("status", "string", false, "状态：active 或 disabled。", "disabled"),
					catalogFieldEx("routeStrategyId", "string", false, "新的策略路由 ID。", "rts_xxx"),
					catalogFieldEx("expiresAt", "string|null", false, "新的到期时间；传 null 表示清空。"),
					catalogFieldEx("quotaLimits", "object|null", false, "新的请求成本额度限制；传 null 表示清空。"),
					catalogFieldEx("availabilitySchedule", "object|null", false, "时间计划；null 表示清空计划，未填写表示保留。"),
				},
				Example: map[string]any{"apiKeyId": "key_xxx", "status": "disabled"},
			},
			ResponseExample: responseEnvelope(map[string]any{"action": "updated", "target": target, "apiKey": apiKeyDisabled}),
		},
		{
			ID: "api-key-delete", Name: "API Key 删除",
			Summary: "按 API Key 新增或列表响应返回的 ID 删除 API Key。",
			Status:  "available", Method: "POST", Path: "/__aipublic__/api-key/del",
			Headers: []catalogHeader{catalogAuthHeader},
			Query:   []catalogField{},
			RequestBody: &catalogBody{
				ContentType: "application/json",
				Fields: []catalogField{
					catalogFieldEx("targetUsername", "string", false, "可选校验条件。提供时必须与 API Key 归属目标用户一致。", "huanmin"),
					catalogFieldEx("apiKeyId", "string", true, "API Key 新增或列表响应返回的 API Key ID。", "key_xxx"),
				},
				Example: map[string]any{"apiKeyId": "key_xxx"},
			},
			ResponseExample: responseEnvelope(map[string]any{"action": "deleted", "target": target, "apiKey": apiKey}),
		},
		{
			ID: "route-strategy-list", Name: "路由策略列表",
			Summary: "分页读取指定系统用户名下的路由策略摘要和分组绑定，用于 API Key 绑定和资源对账。",
			Status:  "available", Method: "GET", Path: "/__aipublic__/route-strategy/list",
			Headers: []catalogHeader{catalogAuthHeader},
			Query: append([]catalogField{catalogTargetQuery,
				catalogFieldEx("keyword", "string", false, "按路由策略名称精确 / 前缀筛选。", "公益站默认路由"),
				catalogFieldEx("mode", "string", false, "路由模式筛选：normal、hybrid_smart、weighted、failover、round_robin 或 all。", "normal"),
				catalogFieldEx("status", "string", false, "状态筛选：active、disabled 或 all。", "active")},
				catalogPageQuery()...),
			ResponseExample: pageEnvelope([]any{routeStrategy}),
		},
		{
			ID: "route-strategy-add", Name: "路由策略新增",
			Summary: "在指定系统用户下新增路由策略，绑定一个或多个分组。",
			Status:  "available", Method: "POST", Path: "/__aipublic__/route-strategy/add",
			Headers: []catalogHeader{catalogAuthHeader},
			Query:   []catalogField{},
			RequestBody: &catalogBody{
				ContentType: "application/json",
				Fields: []catalogField{catalogTargetQuery,
					catalogFieldEx("name", "string", true, "路由策略名称。", "公益站默认路由"),
					catalogFieldEx("description", "string|null", false, "路由策略说明；传 null 表示清空。"),
					catalogFieldEx("mode", "string", false, "路由模式，默认 normal。", "normal"),
					catalogFieldEx("status", "string", false, "状态：active 或 disabled，默认 active。", "active"),
					catalogFieldEx("groupBindings", "array", true, "分组绑定列表，至少 1 个，最多 20 个。", []any{map[string]any{"groupId": "grp_xxx", "priority": 1, "weight": 100, "status": "active"}}),
					catalogFieldEx("hybridRoutingConfig", "object|null", false, "混合智能路由配置；仅 hybrid_smart 模式需要。")},
				Example: map[string]any{"targetUsername": "huanmin", "name": "公益站默认路由", "mode": "normal", "groupBindings": []any{map[string]any{"groupId": "grp_xxx"}}},
			},
			ResponseExample: responseEnvelope(map[string]any{"action": "created", "target": target, "routeStrategy": routeStrategy}),
		},
		{
			ID: "route-strategy-update", Name: "路由策略修改",
			Summary: "按路由策略 ID 修改名称、状态、模式、分组绑定或混合智能路由配置。",
			Status:  "available", Method: "POST", Path: "/__aipublic__/route-strategy/update",
			Headers: []catalogHeader{catalogAuthHeader},
			Query:   []catalogField{},
			RequestBody: &catalogBody{
				ContentType: "application/json",
				Fields: []catalogField{
					catalogFieldEx("targetUsername", "string", false, "可选校验条件。提供时必须与路由策略归属目标用户一致。", "huanmin"),
					catalogFieldEx("routeStrategyId", "string", true, "路由策略 ID。", "rts_xxx"),
					catalogFieldEx("name", "string", false, "新的路由策略名称。"),
					catalogFieldEx("description", "string|null", false, "新的路由策略说明；传 null 表示清空。"),
					catalogFieldEx("mode", "string", false, "新的路由模式。", "round_robin"),
					catalogFieldEx("status", "string", false, "状态：active 或 disabled。", "active"),
					catalogFieldEx("groupBindings", "array", false, "新的分组绑定列表；提供时整体覆盖。"),
					catalogFieldEx("hybridRoutingConfig", "object|null", false, "新的混合智能路由配置；传 null 表示清空。"),
				},
				Example: map[string]any{"routeStrategyId": "rts_xxx", "mode": "round_robin", "groupBindings": []any{map[string]any{"groupId": "grp_xxx", "priority": 1}}},
			},
			ResponseExample: responseEnvelope(map[string]any{"action": "updated", "target": target, "routeStrategy": routeStrategyRoundRobin}),
		},
		{
			ID: "route-strategy-delete", Name: "路由策略删除",
			Summary: "按路由策略 ID 删除策略。默认策略或仍被 API Key 使用的策略会被拒绝删除。",
			Status:  "available", Method: "POST", Path: "/__aipublic__/route-strategy/del",
			Headers: []catalogHeader{catalogAuthHeader},
			Query:   []catalogField{},
			RequestBody: &catalogBody{
				ContentType: "application/json",
				Fields: []catalogField{
					catalogFieldEx("targetUsername", "string", false, "可选校验条件。提供时必须与路由策略归属目标用户一致。", "huanmin"),
					catalogFieldEx("routeStrategyId", "string", true, "路由策略 ID。", "rts_xxx"),
				},
				Example: map[string]any{"routeStrategyId": "rts_xxx"},
			},
			ResponseExample: responseEnvelope(map[string]any{"action": "deleted", "target": target, "routeStrategy": routeStrategy}),
		},
		{
			ID: "group-list", Name: "分组列表",
			Summary: "分页读取指定系统用户名下的分组，用于外部来源系统对账和找回分组 ID。",
			Status:  "available", Method: "GET", Path: "/__aipublic__/group/list",
			Headers: []catalogHeader{catalogAuthHeader},
			Query: append([]catalogField{catalogTargetQuery,
				catalogFieldEx("providerCode", "string", false, "供应商编码筛选。", vendorCodeGPT),
				catalogFieldEx("keyword", "string", false, "按分组名称或供应商编码精确 / 前缀筛选。", "福利")},
				catalogPageQuery()...),
			ResponseExample: pageEnvelope([]any{group}),
		},
		{
			ID: "group-add", Name: "分组新增",
			Summary: "在指定系统用户下新增账号分组；目标用户不存在时自动创建，同名分组已存在时按幂等成功返回既有分组。",
			Status:  "available", Method: "POST", Path: "/__aipublic__/group/add",
			Headers: []catalogHeader{catalogAuthHeader},
			Query:   []catalogField{},
			RequestBody: &catalogBody{
				ContentType: "application/json",
				Fields: []catalogField{catalogTargetQuery,
					catalogFieldEx("targetDisplayName", "string", false, "自动创建目标系统用户时使用的显示名称；未填写时使用 targetUsername。", "欢民"),
					catalogFieldEx("name", "string", true, "分组名称。", "福利"),
					catalogFieldEx("providerCode", "string", true, "供应商编码。", vendorCodeGPT),
					catalogFieldEx("description", "string", false, "分组说明。"),
					catalogFieldEx("enabled", "boolean", false, "是否启用，默认 true。", true),
					catalogFieldEx("groupType", "string", false, "分组类型：personal 或 high_concurrency，默认 personal。", "personal")},
				Example: map[string]any{"targetUsername": "huanmin", "name": "福利", "providerCode": vendorCodeGPT},
			},
			ResponseExample: responseEnvelope(map[string]any{"action": "created", "target": target, "group": group}),
		},
		{
			ID: "group-update", Name: "分组修改",
			Summary: "按分组 ID 修改名称、供应商编码、说明、启用状态或分组类型。",
			Status:  "available", Method: "POST", Path: "/__aipublic__/group/update",
			Headers: []catalogHeader{catalogAuthHeader},
			Query:   []catalogField{},
			RequestBody: &catalogBody{
				ContentType: "application/json",
				Fields: []catalogField{
					catalogFieldEx("targetUsername", "string", false, "可选校验条件。提供时必须与分组归属目标用户一致。", "huanmin"),
					catalogFieldEx("groupId", "string", true, "分组 ID。", "grp_xxx"),
					catalogFieldEx("name", "string", false, "新的分组名称。"),
					catalogFieldEx("providerCode", "string", false, "新的供应商编码。"),
					catalogFieldEx("description", "string|null", false, "新的分组说明；传 null 表示清空。"),
					catalogFieldEx("enabled", "boolean", false, "是否启用。"),
					catalogFieldEx("groupType", "string", false, "分组类型：personal 或 high_concurrency。"),
				},
				Example: map[string]any{"groupId": "grp_xxx", "name": "福利-主池"},
			},
			ResponseExample: responseEnvelope(map[string]any{"action": "updated", "target": target, "group": groupRenamed}),
		},
		{
			ID: "group-delete", Name: "分组删除",
			Summary: "按分组新增或列表响应返回的 ID 删除分组。默认分组或仍被约束保护的分组会被拒绝删除。",
			Status:  "available", Method: "POST", Path: "/__aipublic__/group/del",
			Headers: []catalogHeader{catalogAuthHeader},
			Query:   []catalogField{},
			RequestBody: &catalogBody{
				ContentType: "application/json",
				Fields: []catalogField{
					catalogFieldEx("targetUsername", "string", false, "可选校验条件。提供时必须与分组归属目标用户一致。", "huanmin"),
					catalogFieldEx("groupId", "string", true, "分组 ID。", "grp_xxx"),
				},
				Example: map[string]any{"groupId": "grp_xxx"},
			},
			ResponseExample: responseEnvelope(map[string]any{"action": "deleted", "target": target, "group": group}),
		},
		{
			ID: "account-list", Name: "账号列表",
			Summary: "分页读取指定系统用户名下的 AI 账户脱敏摘要，支持按分组、供应商、状态和名称筛选。",
			Status:  "available", Method: "GET", Path: "/__aipublic__/account/list",
			Headers: []catalogHeader{catalogAuthHeader},
			Query: append([]catalogField{catalogTargetQuery,
				catalogFieldEx("targetGroupName", "string", false, "目标分组名称；提供该字段时必须同时提供 providerCode。", "福利"),
				catalogFieldEx("providerCode", "string", false, "供应商编码筛选。", vendorCodeGPT),
				catalogFieldEx("providerProtocolProfileId", "string", false, "供应商协议档案筛选。", "profile_gpt_openai_v1"),
				catalogFieldEx("groupId", "string", false, "目标分组 ID；优先于 targetGroupName。", "grp_xxx"),
				catalogFieldEx("keyword", "string", false, "按账号名称精确 / 前缀筛选。", "公益站"),
				catalogFieldEx("type", "string", false, "账号类型筛选；公开写入当前只支持 api_key。", "api_key"),
				catalogFieldEx("status", "string", false, "账号状态，支持逗号分隔多个状态。", "active,disabled"),
				catalogFieldEx("schedulable", "string", false, "可调度状态筛选：all、enabled、disabled 或 cooling。", "enabled")},
				catalogPageQuery()...),
			ResponseExample: pageEnvelope([]any{account}),
		},
		{
			ID: "account-add", Name: "账号新增",
			Summary: "新增 API Key 类型账号到指定系统用户和分组；目标用户或分组不存在时自动创建，响应不会回显上游凭据。",
			Status:  "available", Method: "POST", Path: "/__aipublic__/account/add",
			Headers: []catalogHeader{catalogAuthHeader},
			Query:   []catalogField{},
			RequestBody: &catalogBody{
				ContentType: "application/json",
				Fields: []catalogField{catalogTargetQuery,
					catalogFieldEx("targetDisplayName", "string", false, "自动创建目标系统用户时使用的显示名称。", "欢民"),
					catalogFieldEx("targetGroupName", "string", true, "目标账号分组名称。", "福利"),
					catalogFieldEx("providerCode", "string", true, "供应商编码。", vendorCodeGPT),
					catalogFieldEx("providerProtocolProfileId", "string", true, "供应商协议档案。", "profile_gpt_openai_v1"),
					catalogFieldEx("name", "string", true, "账号名称。", "公益站-青芽主通道"),
					catalogFieldEx("type", "string", true, "账号类型；当前公开新增只支持 api_key。", "api_key"),
					catalogFieldEx("baseUrl", "string", true, "OpenAI 兼容 Base URL。", "https://api.openai.com/v1"),
					catalogFieldEx("apiKey", "string", true, "上游 API Key；响应不会回显。", "sk-..."),
					catalogFieldEx("supportedModels", "string[]", false, "该账号支持的模型列表。"),
					catalogFieldEx("concurrencyLimit", "number", false, "单账号并发限制，范围 1 到 100000。", 20),
					catalogFieldEx("priority", "number", false, "账号调度优先级，范围 0 到 100000。", 0),
					catalogFieldEx("status", "string", false, "账号状态：active 或 disabled。", "active"),
					catalogFieldEx("availabilitySchedule", "object|null", false, "时间计划；null 表示清空计划，未填写表示不限制。"),
					catalogFieldEx("notes", "string", false, "账号备注，最多 1000 个字符。")},
				Example: map[string]any{"targetUsername": "huanmin", "targetGroupName": "福利", "providerCode": vendorCodeGPT, "providerProtocolProfileId": "profile_gpt_openai_v1", "name": "公益站-青芽主通道", "type": "api_key", "baseUrl": "https://api.openai.com/v1", "apiKey": "sk-..."},
			},
			ResponseExample: responseEnvelope(map[string]any{"action": "created", "target": targetWithGroup, "account": accountCreated}),
		},
		{
			ID: "account-update", Name: "账号修改",
			Summary: "按账号 ID 修改既有 API Key 类型账号；找不到时返回 404，响应不回显上游凭据。",
			Status:  "available", Method: "POST", Path: "/__aipublic__/account/update",
			Headers: []catalogHeader{catalogAuthHeader},
			Query:   []catalogField{},
			RequestBody: &catalogBody{
				ContentType: "application/json",
				Fields: []catalogField{
					catalogFieldEx("accountId", "string", true, "账号 ID。", "acc_xxx"),
					catalogFieldEx("targetUsername", "string", false, "可选校验条件。提供时必须与账号归属目标用户一致。", "huanmin"),
					catalogFieldEx("targetGroupName", "string", false, "可选校验条件。提供时账号必须在该目标分组内。", "福利"),
					catalogFieldEx("providerCode", "string", false, "可选校验条件。提供时必须与账号供应商一致。", vendorCodeGPT),
					catalogFieldEx("providerProtocolProfileId", "string", false, "可选校验条件。提供时必须与账号协议档案一致。", "profile_gpt_openai_v1"),
					catalogFieldEx("name", "string", false, "账号名称。"),
					catalogFieldEx("type", "string", false, "可选校验字段；当前公开修改只支持 api_key。", "api_key"),
					catalogFieldEx("baseUrl", "string", false, "OpenAI 兼容 Base URL。"),
					catalogFieldEx("apiKey", "string", false, "上游 API Key；响应不会回显。"),
					catalogFieldEx("supportedModels", "string[]", false, "该账号支持的模型列表。"),
					catalogFieldEx("concurrencyLimit", "number", false, "单账号并发限制。"),
					catalogFieldEx("priority", "number", false, "账号调度优先级。"),
					catalogFieldEx("status", "string", false, "账号状态：active 或 disabled。"),
					catalogFieldEx("availabilitySchedule", "object|null", false, "时间计划；null 表示清空计划。"),
					catalogFieldEx("notes", "string", false, "账号备注。"),
				},
				Example: map[string]any{"accountId": "acc_xxx", "apiKey": "sk-...", "status": "disabled"},
			},
			ResponseExample: responseEnvelope(map[string]any{"action": "updated", "target": targetWithGroup, "account": accountDisabled}),
		},
		{
			ID: "account-delete", Name: "账号删除",
			Summary: "按账号 ID 删除账号；目标用户已停用时拒绝删除，找不到时幂等返回 not_found。",
			Status:  "available", Method: "POST", Path: "/__aipublic__/account/del",
			Headers: []catalogHeader{catalogAuthHeader},
			Query:   []catalogField{},
			RequestBody: &catalogBody{
				ContentType: "application/json",
				Fields: []catalogField{
					catalogFieldEx("accountId", "string", true, "账号 ID。", "acc_xxx"),
					catalogFieldEx("targetUsername", "string", false, "可选校验条件。提供时必须与账号归属目标用户一致。", "huanmin"),
					catalogFieldEx("targetGroupName", "string", false, "可选校验条件。提供时账号必须在该目标分组内。", "福利"),
					catalogFieldEx("providerCode", "string", false, "可选校验条件。提供时必须与账号供应商一致。", vendorCodeGPT),
					catalogFieldEx("providerProtocolProfileId", "string", false, "可选校验条件。提供时必须与账号协议档案一致。", "profile_gpt_openai_v1"),
				},
				Example: map[string]any{"accountId": "acc_xxx"},
			},
			ResponseExample: responseEnvelope(map[string]any{"action": "deleted", "target": targetWithGroup, "account": account}),
		},
	}
	for index := range items {
		items[index].ResponseFields = responseFieldsForCatalogItem(items[index].ID)
		items[index].Scope = scopeForCatalogItem(items[index].ID)
	}
	return externalAPICatalog{BasePath: "/__aipublic__", AuthType: "Bearer", Items: items}
}
