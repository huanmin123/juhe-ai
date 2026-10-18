package providerconnectionmigration

import (
	"testing"
	"time"
)

// 规则 0-13 的表驱动 dry-run 判定测试：每条规则至少 1 正 1 阻塞。

var testNow = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

func newTestSource() *sourceData {
	data := &sourceData{tables: map[string]*sourceTable{}, historyCounts: map[string]int{}}
	for _, def := range configSourceTables {
		data.tables[def.name] = &sourceTable{def: def}
	}
	return data
}

func addRows(t *testing.T, data *sourceData, tableName string, rows ...map[string]any) {
	t.Helper()
	table := data.tables[tableName]
	if table == nil {
		t.Fatalf("测试 source 缺少表 %s", tableName)
	}
	for _, values := range rows {
		cells := make([]Cell, 0, len(table.def.required))
		cellValues := map[string]Cell{}
		for _, column := range table.def.required {
			raw, present := values[column]
			cell, err := classifyDriverValue(raw)
			if err != nil {
				t.Fatalf("构造 %s.%s cell 失败: %v", tableName, column, err)
			}
			if !present {
				cell = NullCell()
			}
			cells = append(cells, cell)
			cellValues[column] = cell
		}
		pk := make([]Cell, 0, len(table.def.pk))
		for _, name := range table.def.pk {
			pk = append(pk, cellValues[name])
		}
		table.rows = append(table.rows, sourceRow{cells: cells, values: cellValues, pk: pk})
	}
}

func setHistoryCounts(data *sourceData, counts map[string]int) {
	for name, count := range counts {
		data.historyCounts[name] = count
	}
}

func findRowOut(t *testing.T, out *ruleOutput, table, pk string) *RowDisposition {
	t.Helper()
	for index := range out.rows {
		row := &out.rows[index]
		if row.SourceTable == table && len(row.SourcePK) >= 1 && row.SourcePK[0] == pk {
			return row
		}
	}
	t.Fatalf("manifest 缺少 %s/%s 判定行", table, pk)
	return nil
}

func mustString(t *testing.T, fields map[string]string, key string) string {
	t.Helper()
	value, present := fields[key]
	if !present {
		t.Fatalf("target fields 缺少 %q: %+v", key, fields)
	}
	return value
}

// pricedCatalogRow 返回一条已定价、可推导 chat/responses mode 的目录行。
func pricedCatalogRow(id, provider, model string) map[string]any {
	return map[string]any{
		"id": id, "provider_code": provider, "model": model, "status": "active",
		"mode": "chat", "supported_api_protocols_json": `["chat_completions","responses"]`,
		"service_tier_prices_json": "{}", "input_usd_per_1m": float64(1.5), "output_usd_per_1m": float64(2),
	}
}

func gptAccountFixture(id, profileID string) map[string]any {
	return map[string]any{
		"id": id, "config_revision": int64(3), "system_account_id": "sys_admin",
		"provider_code": "gpt", "provider_protocol_profile_id": profileID,
		"name": id, "type": "api_key", "status": "active",
		"credentials_encrypted": "v1:iv:tag:cipher", "capability_domain": "chat",
		"health_check_model": "m1", "health_check_endpoint_mode": "chat_json",
	}
}

func TestRule0ProviderCodeValidation(t *testing.T) {
	data := newTestSource()
	addRows(t, data, "accounts",
		gptAccountFixture("acc-ok", "profile_gpt_openai_v1"),
		map[string]any{"id": "acc-null", "provider_code": nil, "system_account_id": "sys_admin", "type": "api_key"},
		map[string]any{"id": "acc-blank", "provider_code": " gpt ", "system_account_id": "sys_admin", "type": "api_key"},
		map[string]any{"id": "acc-upper", "provider_code": "GPT", "system_account_id": "sys_admin", "type": "api_key"},
		map[string]any{"id": "acc-unknown", "provider_code": "unknown_ai", "system_account_id": "sys_admin", "type": "api_key"},
		map[string]any{"id": "acc-empty", "provider_code": "", "system_account_id": "sys_admin", "type": "api_key"},
	)
	// 无效账户的子表行不得重复计数（规则 0）。
	addRows(t, data, "account_supported_models",
		map[string]any{"account_id": "acc-unknown", "provider_code": "unknown_ai", "model": "m"},
	)
	addRows(t, data, "provider_model_catalog", pricedCatalogRow("cat-1", "gpt", "m1"))
	out := applyRules(data, testNow)
	if out.blocked.AccountsBlocked != 5 {
		t.Fatalf("accounts_blocked = %d, want 5（NULL/空白/大小写/未知/空串各 1 次）", out.blocked.AccountsBlocked)
	}
	if out.blocked.SupportedModelsBlocked != 0 {
		t.Fatalf("无效账户子行不得重复计数: %+v", out.blocked)
	}
	blocked := findRowOut(t, out, "accounts", "acc-blank")
	if blocked.Disposition != DispositionBlocked || blocked.Rule != 0 {
		t.Fatalf("规则 0 行判定不符: %+v", blocked)
	}
	if got := mustString(t, blocked.TargetFields, "provider_code_source"); got != "text: gpt " {
		t.Fatalf("provider_code_source 记录不符: %q", got)
	}
	ok := findRowOut(t, out, "accounts", "acc-ok")
	if ok.Disposition != DispositionMapped || ok.Rule != 1 {
		t.Fatalf("有效账户判定不符: %+v", ok)
	}
}

func TestRule1RealVendorDriverMapping(t *testing.T) {
	data := newTestSource()
	addRows(t, data, "accounts", gptAccountFixture("acc-gpt", "profile_gpt_openai_v1"))
	addRows(t, data, "provider_protocol_profiles", map[string]any{
		"id": "profile_gpt_openai_v1", "provider_code": "gpt", "protocol_code": "openai",
		"protocol_version": "v1", "base_url": "https://api.openai.com/v1",
		"default_health_check_model": "m1", "enabled": int64(1),
	})
	addRows(t, data, "provider_model_catalog", pricedCatalogRow("cat-1", "gpt", "m1"))
	out := applyRules(data, testNow)
	if out.blocked.ConnectionsBlocked != 0 {
		t.Fatalf("正例不应阻塞连接: %+v", out.blocked)
	}
	row := findRowOut(t, out, "accounts", "acc-gpt")
	if row.Disposition != DispositionMapped {
		t.Fatalf("真实厂商直迁应 mapped: %+v", row)
	}
	if got := mustString(t, row.TargetFields, "vendor_code"); got != "gpt" {
		t.Fatalf("vendor_code = %q, want gpt", got)
	}
	if got := mustString(t, row.TargetFields, "connection_driver_code"); got != "gpt" {
		t.Fatalf("connection_driver_code = %q, want gpt", got)
	}
	if got := mustString(t, row.TargetFields, "target_config_revision"); got != "3" {
		t.Fatalf("规则 1b config_revision = %q, want 3", got)
	}
	if len(row.TargetIDs) != 1 {
		t.Fatalf("应派生 1 条连接: %+v", row.TargetIDs)
	}
}

func TestRule1UnknownProfileBlocked(t *testing.T) {
	data := newTestSource()
	addRows(t, data, "accounts", gptAccountFixture("acc-gpt", "profile_bogus_v9"))
	addRows(t, data, "provider_model_catalog", pricedCatalogRow("cat-1", "gpt", "m1"))
	out := applyRules(data, testNow)
	if out.blocked.ConnectionsBlocked != 1 {
		t.Fatalf("未知 profile 应计入 connections_blocked: %+v", out.blocked)
	}
	row := findRowOut(t, out, "accounts", "acc-gpt")
	if row.Disposition != DispositionMapped || len(row.TargetIDs) != 0 {
		t.Fatalf("账户身份仍可映射但无连接: %+v", row)
	}
	if !containsString(row.Errors, ReasonLegacyProfileUnsupported) {
		t.Fatalf("错误清单缺少 legacy_profile_unsupported: %v", row.Errors)
	}
}

func TestRule2OpenaiToGenericHTTP(t *testing.T) {
	data := newTestSource()
	addRows(t, data, "accounts", map[string]any{
		"id": "acc-openai", "config_revision": int64(1), "system_account_id": "sys_admin",
		"provider_code": "openai", "provider_protocol_profile_id": "profile_openai_openai_v1",
		"type": "api_key", "credentials_encrypted": "v1:i:t:c", "capability_domain": "chat",
	})
	addRows(t, data, "provider_protocol_profiles", map[string]any{
		"id": "profile_openai_openai_v1", "provider_code": "openai", "protocol_code": "openai",
		"protocol_version": "v1", "base_url": "https://api.openai.com/v1",
		"default_health_check_model": "m1", "enabled": int64(1),
	})
	out := applyRules(data, testNow)
	row := findRowOut(t, out, "accounts", "acc-openai")
	if row.Rule != 2 || row.Disposition != DispositionMapped {
		t.Fatalf("openai 账户应按规则 2 映射: %+v", row)
	}
	if got := mustString(t, row.TargetFields, "vendor_code"); got != "NULL" {
		t.Fatalf("openai → vendor_code 必须 NULL: %q", got)
	}
	if got := mustString(t, row.TargetFields, "connection_driver_code"); got != "generic_http" {
		t.Fatalf("openai → generic_http: %q", got)
	}
	if len(out.generated) == 0 || out.generated[0].TargetTable != "account_connections" {
		t.Fatalf("generated_target_ids 缺少连接: %+v", out.generated)
	}
}

func TestRule3HybridFamilyToPreset(t *testing.T) {
	data := newTestSource()
	addRows(t, data, "accounts", map[string]any{
		"id": "acc-h", "system_account_id": "sys_admin", "provider_code": "hybrid",
		"provider_protocol_profile_id": "profile_hybrid_anthropic_messages_v1",
		"type": "api_key", "credentials_encrypted": "v1:i:t:c", "capability_domain": "chat",
	})
	addRows(t, data, "account_model_mappings", map[string]any{
		"account_id": "acc-h", "provider_code": "deepseek", "source_model": "client-a",
		"source_endpoint_family": "messages", "upstream_model": "deepseek-chat",
		"upstream_endpoint_family": "messages", "enabled": int64(1),
	})
	addRows(t, data, "provider_model_catalog", pricedCatalogRow("cat-1", "deepseek", "deepseek-chat"))
	out := applyRules(data, testNow)
	row := findRowOut(t, out, "accounts", "acc-h")
	if row.Rule != 3 {
		t.Fatalf("hybrid 账户规则号应为 3: %+v", row)
	}
	state := findAccountState(t, data, "acc-h")
	if len(state.connections) != 1 || state.connections[0].preset != "generic_anthropic_v1" {
		t.Fatalf("messages 族应映射 generic_anthropic_v1: %+v", state.connections)
	}
	if state.connections[0].surface != "anthropic_v1" {
		t.Fatalf("surface 不符: %+v", state.connections[0])
	}
}

func TestRule3HybridUnknownFamilyBlocked(t *testing.T) {
	data := newTestSource()
	addRows(t, data, "accounts", map[string]any{
		"id": "acc-h", "system_account_id": "sys_admin", "provider_code": "hybrid",
		"provider_protocol_profile_id": "profile_hybrid_openai_chat_v1",
		"type": "api_key", "credentials_encrypted": "v1:i:t:c", "capability_domain": "chat",
	})
	addRows(t, data, "account_model_mappings", map[string]any{
		"account_id": "acc-h", "provider_code": "hybrid", "source_model": "client-a",
		"source_endpoint_family": "chat_completions", "upstream_model": "u",
		"upstream_endpoint_family": "models", "enabled": int64(1),
	})
	addRows(t, data, "provider_model_catalog", pricedCatalogRow("cat-1", "gpt", "u"))
	out := applyRules(data, testNow)
	if out.blocked.MappingsBlocked != 1 {
		t.Fatalf("表外 family 应计入 mappings_blocked: %+v", out.blocked)
	}
	mapping := findRowOut(t, out, "account_model_mappings", "acc-h")
	if mapping.Disposition != DispositionBlocked {
		t.Fatalf("表外 family mapping 应 blocked: %+v", mapping)
	}
}

func TestRule3HybridMultiDomainBlocked(t *testing.T) {
	data := newTestSource()
	addRows(t, data, "accounts", map[string]any{
		"id": "acc-h", "system_account_id": "sys_admin", "provider_code": "hybrid",
		"provider_protocol_profile_id": "profile_hybrid_openai_chat_v1",
		"type": "api_key", "credentials_encrypted": "v1:i:t:c", "capability_domain": "chat",
	})
	addRows(t, data, "account_model_mappings",
		map[string]any{
			"account_id": "acc-h", "provider_code": "gpt", "source_model": "c1",
			"source_endpoint_family": "chat_completions", "upstream_model": "m1",
			"upstream_endpoint_family": "chat_completions", "enabled": int64(1),
		},
		map[string]any{
			"account_id": "acc-h", "provider_code": "minimax", "source_model": "c2",
			"source_endpoint_family": "video_generation", "upstream_model": "m2",
			"upstream_endpoint_family": "video_generation", "enabled": int64(1),
		},
	)
	addRows(t, data, "provider_model_catalog",
		pricedCatalogRow("cat-1", "gpt", "m1"),
		pricedCatalogRow("cat-2", "minimax", "m2"),
	)
	out := applyRules(data, testNow)
	if out.blocked.AccountsBlocked != 1 {
		t.Fatalf("跨域 hybrid 应计 1 次 accounts_blocked: %+v", out.blocked)
	}
	if out.blocked.MappingsBlocked != 0 {
		t.Fatalf("子 mapping 不重复计数: %+v", out.blocked)
	}
	row := findRowOut(t, out, "accounts", "acc-h")
	if !containsString(row.Errors, ReasonHybridMultiDomain) {
		t.Fatalf("错误清单缺少 hybrid_multi_capability_domain: %v", row.Errors)
	}
}

func TestRule4HybridTargetModelExactMatch(t *testing.T) {
	data := newTestSource()
	addRows(t, data, "accounts", map[string]any{
		"id": "acc-h", "system_account_id": "sys_admin", "provider_code": "hybrid",
		"provider_protocol_profile_id": "profile_hybrid_openai_chat_v1",
		"type": "api_key", "credentials_encrypted": "v1:i:t:c", "capability_domain": "chat",
	})
	addRows(t, data, "account_model_mappings",
		map[string]any{
			"account_id": "acc-h", "provider_code": "hybrid", "source_model": "client-a",
			"source_endpoint_family": "chat_completions", "upstream_model": "hybrid-model",
			"upstream_endpoint_family": "chat_completions", "enabled": int64(1),
		},
		map[string]any{
			"account_id": "acc-h", "provider_code": "hybrid", "source_model": "client-b",
			"source_endpoint_family": "chat_completions", "upstream_model": "missing-model",
			"upstream_endpoint_family": "chat_completions", "enabled": int64(1),
		},
	)
	addRows(t, data, "provider_model_catalog", pricedCatalogRow("cat-1", "hybrid", "hybrid-model"))
	out := applyRules(data, testNow)
	rows := mappingsOfTable(t, out)
	if len(rows) != 2 {
		t.Fatalf("应输出 2 条 mapping 判定行: %d", len(rows))
	}
	var hit, miss *RowDisposition
	for _, row := range rows {
		if mustString(t, row.TargetFields, "source_model") == "client-a" {
			hit = row
		} else {
			miss = row
		}
	}
	if hit.Disposition != DispositionMapped || mustString(t, hit.TargetFields, "target_model_id") != "cat-1" {
		t.Fatalf("规则 4 精确匹配应解析目标模型: %+v", hit)
	}
	if got := mustString(t, hit.TargetFields, "source_model"); got != "client-a" {
		t.Fatalf("规则 6 source_model 必须原样: %q", got)
	}
	if miss == nil || miss.Disposition != DispositionBlocked || !containsString(miss.Errors, "mapping_target_model_missing") {
		t.Fatalf("0 候选应计 mapping_target_model_missing: %+v", miss)
	}
	if out.blocked.MappingsBlocked != 1 {
		t.Fatalf("规则 4 阻塞计数不符: %+v", out.blocked)
	}
}

// mappingsOfTable 收集规则输出中的 account_model_mappings 判定行。
func mappingsOfTable(t *testing.T, out *ruleOutput) []*RowDisposition {
	t.Helper()
	var result []*RowDisposition
	for index := range out.rows {
		if out.rows[index].SourceTable == "account_model_mappings" {
			result = append(result, &out.rows[index])
		}
	}
	return result
}

func TestRule5SupportedModels(t *testing.T) {
	data := newTestSource()
	addRows(t, data, "accounts", gptAccountFixture("acc-1", "profile_gpt_openai_v1"))
	addRows(t, data, "provider_protocol_profiles", map[string]any{
		"id": "profile_gpt_openai_v1", "provider_code": "gpt", "protocol_code": "openai",
		"protocol_version": "v1", "base_url": "https://api.openai.com/v1",
		"default_health_check_model": "m1", "enabled": int64(1),
	})
	addRows(t, data, "provider_model_catalog",
		pricedCatalogRow("cat-1", "gpt", "m1"),
		pricedCatalogRow("cat-t1", "xai", "twin"),
		pricedCatalogRow("cat-t2", "xai", "twin"),
	)
	addRows(t, data, "account_supported_models",
		map[string]any{"account_id": "acc-1", "provider_code": "gpt", "model": "nope"},
		map[string]any{"account_id": "acc-1", "provider_code": "xai", "model": "twin"},
		map[string]any{"account_id": "acc-1", "provider_code": "gpt", "model": "m1"},
	)
	out := applyRules(data, testNow)
	if out.blocked.SupportedModelsBlocked != 2 {
		t.Fatalf("0 候选与多候选各计 1 次: %+v", out.blocked)
	}
	missing := findRowOut(t, out, "account_supported_models", "acc-1")
	if missing.Disposition != DispositionBlocked {
		t.Fatalf("缺失目标应 blocked: %+v", missing)
	}
}

func TestRule6MappingSourcePassthroughAndDisabled(t *testing.T) {
	data := newTestSource()
	addRows(t, data, "accounts", map[string]any{
		"id": "acc-openai", "system_account_id": "sys_admin", "provider_code": "openai",
		"provider_protocol_profile_id": "profile_openai_openai_v1",
		"type": "api_key", "credentials_encrypted": "v1:i:t:c", "capability_domain": "chat",
	})
	addRows(t, data, "account_model_mappings",
		map[string]any{
			"account_id": "acc-openai", "provider_code": "openai", "source_model": "Client-Model",
			"source_endpoint_family": "chat_completions", "upstream_model": "u1",
			"upstream_endpoint_family": "chat_completions", "enabled": int64(1),
		},
		map[string]any{
			"account_id": "acc-openai", "provider_code": "openai", "source_model": "Disabled-Map",
			"source_endpoint_family": "chat_completions", "upstream_model": "u1",
			"upstream_endpoint_family": "chat_completions", "enabled": int64(0),
		},
		map[string]any{
			"account_id": "acc-openai", "provider_code": "openai", "source_model": "Bad-Family",
			"source_endpoint_family": "chat_completions", "upstream_model": "u1",
			"upstream_endpoint_family": "embeddings", "enabled": int64(1),
		},
	)
	addRows(t, data, "provider_model_catalog", pricedCatalogRow("cat-1", "openai", "u1"))
	out := applyRules(data, testNow)
	if out.blocked.MappingsBlocked != 1 {
		t.Fatalf("表外 upstream family 应计 1 次 mappings_blocked: %+v", out.blocked)
	}
	// disabled mapping 有合法 target 时保留为 disabled（规则 6）。
	for _, row := range out.rows {
		if row.SourceTable != "account_model_mappings" {
			continue
		}
		if mustString(t, row.TargetFields, "source_model") == "Disabled-Map" && row.Disposition != DispositionMapped {
			t.Fatalf("合法 disabled mapping 不应阻塞: %+v", row)
		}
	}
}

func TestRule7CapabilityDomainAndModeIntersection(t *testing.T) {
	// 正例：video 域 hybrid + video_generation 映射 → generic_openai_v1 词表
	// 与 video lane 相交，可映射。
	positive := newTestSource()
	addRows(t, positive, "accounts", map[string]any{
		"id": "acc-v", "system_account_id": "sys_admin", "provider_code": "hybrid",
		"provider_protocol_profile_id": "profile_hybrid_openai_chat_v1",
		"type": "api_key", "credentials_encrypted": "v1:i:t:c", "capability_domain": "video",
	})
	addRows(t, positive, "account_model_mappings", map[string]any{
		"account_id": "acc-v", "provider_code": "minimax", "source_model": "v1",
		"source_endpoint_family": "video_generation", "upstream_model": "vm",
		"upstream_endpoint_family": "video_generation", "enabled": int64(1),
	})
	addRows(t, positive, "provider_model_catalog", map[string]any{
		"id": "cat-v", "provider_code": "minimax", "model": "vm", "status": "active",
		"supported_api_protocols_json": `["video"]`, "service_tier_prices_json": "{}",
	})
	out := applyRules(positive, testNow)
	if out.blocked.ConnectionsBlocked != 0 || out.blocked.MappingsBlocked != 0 {
		t.Fatalf("video 域正例不应阻塞: %+v", out.blocked)
	}
	// 阻塞例：域字段缺失 → accounts_blocked。
	badDomain := newTestSource()
	addRows(t, badDomain, "accounts", map[string]any{
		"id": "acc-bad", "system_account_id": "sys_admin", "provider_code": "gpt",
		"provider_protocol_profile_id": "profile_gpt_openai_v1",
		"type": "api_key", "credentials_encrypted": "v1:i:t:c", "capability_domain": "television",
	})
	out = applyRules(badDomain, testNow)
	if out.blocked.AccountsBlocked != 1 {
		t.Fatalf("域字段缺失应阻塞: %+v", out.blocked)
	}
	// 阻塞例：preset 词表 ∩ 域 lane 为空 → connections_blocked（video 域 +
	// messages 映射 → generic_anthropic_v1 无 video mode）。
	emptyIntersection := newTestSource()
	addRows(t, emptyIntersection, "accounts", map[string]any{
		"id": "acc-x", "system_account_id": "sys_admin", "provider_code": "hybrid",
		"provider_protocol_profile_id": "profile_hybrid_anthropic_messages_v1",
		"type": "api_key", "credentials_encrypted": "v1:i:t:c", "capability_domain": "video",
	})
	addRows(t, emptyIntersection, "account_model_mappings", map[string]any{
		"account_id": "acc-x", "provider_code": "deepseek", "source_model": "c",
		"source_endpoint_family": "messages", "upstream_model": "m",
		"upstream_endpoint_family": "messages", "enabled": int64(1),
	})
	addRows(t, emptyIntersection, "provider_model_catalog", pricedCatalogRow("cat-1", "deepseek", "m"))
	out = applyRules(emptyIntersection, testNow)
	if out.blocked.ConnectionsBlocked != 1 {
		t.Fatalf("mode 交集为空应计 connections_blocked: %+v", out.blocked)
	}
}

func TestRule8GroupConflicts(t *testing.T) {
	data := newTestSource()
	addRows(t, data, "groups",
		map[string]any{"id": "g-dup-a", "system_account_id": "sys_admin", "name": "默认", "enabled": int64(1), "is_default": int64(0), "group_type": "personal"},
		map[string]any{"id": "g-dup-b", "system_account_id": "sys_admin", "name": "默认", "enabled": int64(1), "is_default": int64(0), "group_type": "personal"},
		map[string]any{"id": "g-def-a", "system_account_id": "o2", "name": "A", "enabled": int64(1), "is_default": int64(1), "group_type": "personal"},
		map[string]any{"id": "g-def-b", "system_account_id": "o2", "name": "B", "enabled": int64(1), "is_default": int64(1), "group_type": "personal"},
		map[string]any{"id": "g-clean", "system_account_id": "sys_admin", "name": "  清洁组  ", "enabled": int64(1), "is_default": int64(0), "group_type": "personal"},
	)
	out := applyRules(data, testNow)
	if out.blocked.GroupsBlocked != 4 {
		t.Fatalf("同名冲突 2 + 多默认冲突 2 = 4: %+v", out.blocked)
	}
	clean := findRowOut(t, out, "groups", "g-clean")
	if clean.Disposition != DispositionMapped {
		t.Fatalf("无冲突组应 mapped: %+v", clean)
	}
	if got := mustString(t, clean.TargetFields, "name"); got != "清洁组" {
		t.Fatalf("目标 name 必须 TrimSpace: %q", got)
	}
	dup := findRowOut(t, out, "groups", "g-dup-a")
	if !containsString(dup.Errors, "group_name_conflict") {
		t.Fatalf("同名组应记 group_name_conflict: %+v", dup)
	}
}

func TestRule8GroupAccountAndBindingRows(t *testing.T) {
	data := newTestSource()
	addRows(t, data, "accounts", gptAccountFixture("acc-1", "profile_gpt_openai_v1"))
	addRows(t, data, "groups",
		map[string]any{"id": "g-ok", "system_account_id": "sys_admin", "name": "ok", "enabled": int64(1), "is_default": int64(0), "group_type": "personal"},
		map[string]any{"id": "g-bad", "system_account_id": "sys_admin", "name": "dup", "enabled": int64(1), "is_default": int64(0), "group_type": "personal"},
		map[string]any{"id": "g-bad2", "system_account_id": "sys_admin", "name": "dup", "enabled": int64(1), "is_default": int64(0), "group_type": "personal"},
	)
	addRows(t, data, "group_accounts",
		map[string]any{"group_id": "g-ok", "account_id": "acc-1", "system_account_id": "sys_admin", "enabled": int64(1)},
		map[string]any{"group_id": "g-bad", "account_id": "acc-1", "system_account_id": "sys_admin", "enabled": int64(1)},
	)
	addRows(t, data, "route_strategy_groups",
		map[string]any{"id": "rsg-1", "route_strategy_id": "rs-1", "system_account_id": "sys_admin", "group_id": "g-ok", "status": "active"},
		map[string]any{"id": "rsg-2", "route_strategy_id": "rs-1", "system_account_id": "sys_admin", "group_id": "g-bad", "status": "active"},
	)
	out := applyRules(data, testNow)
	// 引用行阻塞但不重复计入计数。
	if out.blocked.GroupsBlocked != 2 {
		t.Fatalf("groups_blocked 应为 2: %+v", out.blocked)
	}
	for _, row := range out.rows {
		switch row.SourceTable {
		case "group_accounts":
			if row.SourcePK[0] == "g-bad" && row.Disposition != DispositionBlocked {
				t.Fatalf("指向阻塞组的成员行应 blocked: %+v", row)
			}
			if row.SourcePK[0] == "g-ok" && row.Disposition != DispositionMapped {
				t.Fatalf("正常成员行应 mapped: %+v", row)
			}
		case "route_strategy_groups":
			if row.SourcePK[0] == "rsg-2" && row.Disposition != DispositionBlocked {
				t.Fatalf("指向阻塞组的绑定行应 blocked: %+v", row)
			}
		}
	}
}

func TestRule9AuthorizationInstance(t *testing.T) {
	// 正例：三字段完整、来源存在且非实例、owner 一致 → mapped 且无连接。
	positive := newTestSource()
	addRows(t, positive, "accounts",
		gptAccountFixture("acc-src", "profile_gpt_openai_v1"),
		map[string]any{
			"id": "acc-inst", "system_account_id": "grantee", "provider_code": "gpt",
			"provider_protocol_profile_id": "profile_gpt_openai_v1", "type": "api_key",
			"credentials_encrypted": "v1:i:t:c", "capability_domain": "chat",
			"authorization_instance_authorization_id":          "auth-1",
			"authorization_instance_source_account_id":        "acc-src",
			"authorization_instance_owner_system_account_id": "sys_admin",
		},
	)
	out := applyRules(positive, testNow)
	if out.blocked.AccountsBlocked != 0 {
		t.Fatalf("有效授权实例不应阻塞: %+v", out.blocked)
	}
	inst := findRowOut(t, out, "accounts", "acc-inst")
	if inst.Rule != 9 || inst.Disposition != DispositionMapped || len(inst.TargetIDs) != 0 {
		t.Fatalf("授权实例应 mapped 且不复制连接面: %+v", inst)
	}
	if got := mustString(t, inst.TargetFields, "config_revision"); got != "0" {
		t.Fatalf("实例 config_revision 固定 0: %q", got)
	}
	// 阻塞例：三字段缺一。
	incomplete := newTestSource()
	addRows(t, incomplete, "accounts", map[string]any{
		"id": "acc-inst", "system_account_id": "grantee", "provider_code": "gpt", "type": "api_key",
		"credentials_encrypted":                     "v1:i:t:c",
		"authorization_instance_authorization_id":   "auth-1",
		"authorization_instance_source_account_id": "acc-src",
	})
	out = applyRules(incomplete, testNow)
	if out.blocked.AccountsBlocked != 1 {
		t.Fatalf("授权字段不完整应 accounts_blocked: %+v", out.blocked)
	}
	// 阻塞例：来源缺失 / 来源是实例 / owner 不一致。
	chain := newTestSource()
	addRows(t, chain, "accounts",
		map[string]any{
			"id": "acc-mid", "system_account_id": "sys_admin", "provider_code": "gpt", "type": "api_key",
			"credentials_encrypted":                    "v1:i:t:c",
			"authorization_instance_authorization_id":   "auth-0",
			"authorization_instance_source_account_id": "acc-root",
			"authorization_instance_owner_system_account_id": "sys_admin",
		},
		map[string]any{
			"id": "acc-leaf", "system_account_id": "grantee", "provider_code": "gpt", "type": "api_key",
			"credentials_encrypted":                          "v1:i:t:c",
			"authorization_instance_authorization_id":        "auth-1",
			"authorization_instance_source_account_id":      "acc-missing",
			"authorization_instance_owner_system_account_id": "sys_admin",
		},
		map[string]any{
			"id": "acc-leaf2", "system_account_id": "grantee", "provider_code": "gpt", "type": "api_key",
			"credentials_encrypted":                          "v1:i:t:c",
			"authorization_instance_authorization_id":        "auth-2",
			"authorization_instance_source_account_id":      "acc-mid",
			"authorization_instance_owner_system_account_id": "sys_admin",
		},
		map[string]any{
			"id": "acc-leaf3", "system_account_id": "grantee", "provider_code": "gpt", "type": "api_key",
			"credentials_encrypted":                          "v1:i:t:c",
			"authorization_instance_authorization_id":        "auth-3",
			"authorization_instance_source_account_id":      "acc-root",
			"authorization_instance_owner_system_account_id": "other-owner",
		},
	)
	out = applyRules(chain, testNow)
	if out.blocked.AccountsBlocked != 4 {
		t.Fatalf("链式/缺失/owner 不一致（含被链的 mid 实例自身）应各计 1 次: %+v", out.blocked)
	}
}

func TestRule10ModelMerge(t *testing.T) {
	data := newTestSource()
	addRows(t, data, "provider_model_catalog",
		pricedCatalogRow("cat-1", "gpt", "m1"),
		map[string]any{
			"id": "cat-unpriced", "provider_code": "gpt", "model": "free-chat", "status": "active",
			"supported_api_protocols_json": `["chat_completions"]`, "service_tier_prices_json": "{}",
		},
		map[string]any{
			"id": "cat-unknown-protocol", "provider_code": "gpt", "model": "weird", "status": "active",
			"supported_api_protocols_json": `["warp_v9"]`, "service_tier_prices_json": "{}",
		},
		map[string]any{
			"id": "cat-legacy-completions", "provider_code": "gpt", "model": "legacy", "status": "active",
			"supported_api_protocols_json": `["completions"]`, "service_tier_prices_json": "{}",
		},
		map[string]any{
			"id": "cat-dup", "provider_code": "qwen", "model": "dup-model", "status": "active",
			"supported_api_protocols_json": `["video"]`, "service_tier_prices_json": "{}",
		},
	)
	addRows(t, data, "custom_provider_models",
		map[string]any{
			"id": "cus-1", "provider_code": "glm", "model": "mine", "scope": "personal",
			"system_account_id": "sys_admin", "status": "active", "currency": "CNY",
			"supported_api_protocols_json": `["chat_completions"]`, "service_tier_prices_json": "{}",
			"input_usd_per_1m": float64(0.5),
		},
		// catalog 恒 builtin、custom 至多 global/personal，跨表身份不会重复；
		// 同身份冲突只能出现在 custom 表内两行。
		map[string]any{
			"id": "cus-dup", "provider_code": "qwen", "model": "dup-model", "scope": "personal",
			"system_account_id": "sys_admin", "status": "active", "currency": "USD",
			"supported_api_protocols_json": `["video"]`, "service_tier_prices_json": "{}",
		},
		map[string]any{
			"id": "cus-dup2", "provider_code": "qwen", "model": "dup-model", "scope": "personal",
			"system_account_id": "sys_admin", "status": "active", "currency": "USD",
			"supported_api_protocols_json": `["video"]`, "service_tier_prices_json": "{}",
		},
		map[string]any{
			"id": "cus-catalog-twin", "provider_code": "gpt", "model": "m1", "scope": "global",
			"status": "active", "currency": "USD",
			"supported_api_protocols_json": `["chat_completions"]`, "service_tier_prices_json": "{}",
			"input_usd_per_1m": float64(1),
		},
	)
	out := applyRules(data, testNow)
	// cat-unpriced（文本无价）+ cat-unknown-protocol + cat-legacy-completions
	//（空推导）+ cus-dup/cus-dup2（同身份冲突 2 行）= 5。
	if out.blocked.ModelsBlocked != 5 {
		t.Fatalf("models_blocked = %d, want 5: %+v", out.blocked.ModelsBlocked, out.blocked)
	}
	merged := findRowOut(t, out, "provider_model_catalog", "cat-1")
	if merged.Disposition != DispositionMerged {
		t.Fatalf("已定价目录行应 merged: %+v", merged)
	}
	if got := mustString(t, merged.TargetFields, "scope"); got != "builtin" {
		t.Fatalf("catalog scope 固定 builtin: %q", got)
	}
	if got := mustString(t, merged.TargetFields, "currency"); got != "USD" {
		t.Fatalf("catalog 缺 currency 补 USD: %q", got)
	}
	if got := mustString(t, merged.TargetFields, "supported_endpoint_modes_json"); got != `["chat_json","chat_sse","responses_json","responses_sse"]` {
		t.Fatalf("mode 集合推导不符: %q", got)
	}
	custom := findRowOut(t, out, "custom_provider_models", "cus-1")
	if got := mustString(t, custom.TargetFields, "currency"); got != "CNY" {
		t.Fatalf("custom currency 原样保留: %q", got)
	}
	if got := mustString(t, custom.TargetFields, "vendor_code"); got != "glm" {
		t.Fatalf("custom vendor 推导不符: %q", got)
	}
}

func TestRule11HistoryRowsBlocked(t *testing.T) {
	data := newTestSource()
	setHistoryCounts(data, map[string]int{"usage_records": 3, "audit_logs": 2, "model_check_runs": 1})
	out := applyRules(data, testNow)
	if out.blocked.HistoryRowsUnmatched != 6 {
		t.Fatalf("history_rows_unmatched = %d, want 6", out.blocked.HistoryRowsUnmatched)
	}
	historyRows := 0
	for _, row := range out.rows {
		if row.Rule == 11 {
			historyRows++
			if row.Disposition != DispositionBlocked || row.SourceRowCount == 0 {
				t.Fatalf("规则 11 聚合行应 blocked 且带行数: %+v", row)
			}
		}
	}
	if historyRows != 3 {
		t.Fatalf("应按表产生 3 条规则 11 聚合行: %d", historyRows)
	}
}

func TestRule12HealthCheckReasons(t *testing.T) {
	buildContext := func(models []*modelInfo, providerModelKey func(info *modelInfo) string) *rulesContext {
		ctx := &rulesContext{
			source:                newTestSource(),
			now:                   testNow,
			modelsByProviderModel: map[string][]*modelInfo{},
			accountStates:         map[string]*accountState{},
			blockedGroups:         map[string]bool{},
			mappingM:              map[string]map[string]bool{},
		}
		for _, info := range models {
			ctx.models = append(ctx.models, info)
			ctx.modelsByProviderModel[providerModelKey(info)] = append(ctx.modelsByProviderModel[providerModelKey(info)], info)
		}
		return ctx
	}
	model := func(id string) *modelInfo {
		return &modelInfo{id: id, providerCode: "gpt", model: "m1", scope: "builtin", status: "active", modes: []string{"chat_json", "chat_sse"}}
	}
	conn := func(id string, modes ...string) derivedConnection {
		set := map[string]bool{}
		for _, mode := range modes {
			set[mode] = true
		}
		return derivedConnection{id: id, preset: "gpt_openai_v1", baseURLRaw: "https://api.openai.com/v1", modes: set}
	}
	healthRow := func(mode, modelStr string) *sourceRow {
		return &sourceRow{values: map[string]Cell{
			"health_check_endpoint_mode": TextCell(mode),
			"health_check_model":         TextCell(modelStr),
			"provider_code":              TextCell("gpt"),
		}, pk: []Cell{TextCell("acc-1")}}
	}
	state := func(connections ...derivedConnection) *accountState {
		return &accountState{
			id: "acc-1", providerCode: "gpt", owner: "sys_admin",
			connectionsValid: true, connections: connections,
			supportedModels: map[string]bool{"cat-1": true},
			targetFields:    map[string]string{},
		}
	}
	stateWithoutSupport := func(connections ...derivedConnection) *accountState {
		result := state(connections...)
		result.supportedModels = map[string]bool{}
		return result
	}
	assertReason := func(t *testing.T, got, want string) {
		t.Helper()
		if got != want {
			t.Fatalf("health reason = %q, want %q", got, want)
		}
	}
	t.Run("positive", func(t *testing.T) {
		ctx := buildContext([]*modelInfo{model("cat-1")}, func(info *modelInfo) string { return "gpt\x00m1" })
		reason, _, _ := ctx.resolveHealthCheck(state(conn("c1", "chat_json")), healthRow("chat_json", "m1"))
		assertReason(t, reason, "")
	})
	t.Run("mode_unsupported", func(t *testing.T) {
		ctx := buildContext(nil, func(info *modelInfo) string { return "" })
		reason, _, _ := ctx.resolveHealthCheck(state(conn("c1", "chat_json")), healthRow("chat_xml", "m1"))
		assertReason(t, reason, HealthReasonModeUnsupported)
		reason, _, _ = ctx.resolveHealthCheck(state(conn("c1", "chat_json")), healthRow("", "m1"))
		assertReason(t, reason, HealthReasonModeUnsupported)
	})
	t.Run("connection_zero", func(t *testing.T) {
		ctx := buildContext(nil, func(info *modelInfo) string { return "" })
		reason, _, _ := ctx.resolveHealthCheck(state(conn("c1", "responses_json")), healthRow("chat_json", "m1"))
		assertReason(t, reason, HealthReasonConnectionZero)
	})
	t.Run("connection_many", func(t *testing.T) {
		ctx := buildContext(nil, func(info *modelInfo) string { return "" })
		reason, candidates, _ := ctx.resolveHealthCheck(state(
			conn("c1", "chat_json"), conn("c2", "chat_json"),
		), healthRow("chat_json", "m1"))
		assertReason(t, reason, HealthReasonConnectionMany)
		if len(candidates) != 2 {
			t.Fatalf("connection_many 应带 2 个候选: %v", candidates)
		}
	})
	t.Run("base_url_missing", func(t *testing.T) {
		ctx := buildContext(nil, func(info *modelInfo) string { return "" })
		bare := conn("c1", "chat_json")
		bare.baseURLRaw = ""
		reason, _, _ := ctx.resolveHealthCheck(state(bare), healthRow("chat_json", "m1"))
		assertReason(t, reason, HealthReasonBaseURLMissing)
	})
	t.Run("base_url_invalid", func(t *testing.T) {
		ctx := buildContext(nil, func(info *modelInfo) string { return "" })
		bad := conn("c1", "chat_json")
		bad.baseURLRaw = "not a url"
		reason, _, _ := ctx.resolveHealthCheck(state(bad), healthRow("chat_json", "m1"))
		assertReason(t, reason, HealthReasonBaseURLInvalid)
	})
	t.Run("model_zero_and_many", func(t *testing.T) {
		ctx := buildContext(nil, func(info *modelInfo) string { return "" })
		reason, _, _ := ctx.resolveHealthCheck(state(conn("c1", "chat_json")), healthRow("chat_json", "ghost"))
		assertReason(t, reason, HealthReasonModelZero)
		ctx = buildContext([]*modelInfo{model("cat-1"), model("cat-2")}, func(info *modelInfo) string { return "gpt\x00m1" })
		reason, _, modelCandidates := ctx.resolveHealthCheck(state(conn("c1", "chat_json")), healthRow("chat_json", "m1"))
		assertReason(t, reason, HealthReasonModelMany)
		if len(modelCandidates) != 2 {
			t.Fatalf("model_many 应带 2 个候选: %v", modelCandidates)
		}
	})
	t.Run("model_mode_mismatch", func(t *testing.T) {
		// 目标模型存在但不属于已解析支持模型 → model_mode_mismatch。
		ctx := buildContext([]*modelInfo{model("cat-1")}, func(info *modelInfo) string { return "gpt\x00m1" })
		reason, _, _ := ctx.resolveHealthCheck(stateWithoutSupport(conn("c1", "chat_json")), healthRow("chat_json", "m1"))
		assertReason(t, reason, HealthReasonModelModeMismatch)
	})
}

func TestRule12HealthCheckFullPositive(t *testing.T) {
	data := newTestSource()
	addRows(t, data, "accounts", gptAccountFixture("acc-1", "profile_gpt_openai_v1"))
	addRows(t, data, "provider_protocol_profiles", map[string]any{
		"id": "profile_gpt_openai_v1", "provider_code": "gpt", "protocol_code": "openai",
		"protocol_version": "v1", "base_url": "https://api.openai.com/v1",
		"default_health_check_model": "m1", "enabled": int64(1),
	})
	addRows(t, data, "provider_model_catalog", pricedCatalogRow("cat-1", "gpt", "m1"))
	addRows(t, data, "account_supported_models", map[string]any{
		"account_id": "acc-1", "provider_code": "gpt", "model": "m1",
	})
	out := applyRules(data, testNow)
	if out.blocked.HealthChecksBlocked != 0 || len(out.healthBlocks) != 0 {
		t.Fatalf("健康检查正例不应阻塞: %+v", out.blocked)
	}
	row := findRowOut(t, out, "accounts", "acc-1")
	if mustString(t, row.TargetFields, "health_check_endpoint_mode") != "chat_json" {
		t.Fatalf("health-check mode 未写入: %+v", row.TargetFields)
	}
	if mustString(t, row.TargetFields, "health_check_model_id") != "cat-1" {
		t.Fatalf("health-check model 未写入: %+v", row.TargetFields)
	}
	if got := mustString(t, row.TargetFields, "health_check_base_url_source"); got != "legacy_profile" {
		t.Fatalf("Base URL 来源证据不符: %q", got)
	}
}

func TestRule13PublicModelConflicts(t *testing.T) {
	// direct 与 enabled mapping 同名 → 1 个冲突对象。
	conflict := newTestSource()
	addRows(t, conflict, "accounts", map[string]any{
		"id": "acc-1", "system_account_id": "sys_admin", "provider_code": "openai",
		"provider_protocol_profile_id": "profile_openai_openai_v1",
		"type": "api_key", "credentials_encrypted": "v1:i:t:c", "capability_domain": "chat",
	})
	addRows(t, conflict, "provider_model_catalog", pricedCatalogRow("cat-1", "openai", "shared"))
	addRows(t, conflict, "account_supported_models", map[string]any{
		"account_id": "acc-1", "provider_code": "openai", "model": "shared",
	})
	addRows(t, conflict, "account_model_mappings", map[string]any{
		"account_id": "acc-1", "provider_code": "openai", "source_model": "shared",
		"source_endpoint_family": "chat_completions", "upstream_model": "shared",
		"upstream_endpoint_family": "chat_completions", "enabled": int64(1),
	})
	out := applyRules(conflict, testNow)
	if out.blocked.PublicModelConflicts != 1 || len(out.publicConflicts) != 1 {
		t.Fatalf("direct/mapping 同名应计 1 个冲突: %+v", out.blocked)
	}
	object := out.publicConflicts[0]
	if object.ResourceAccountID != "acc-1" || object.WireModel != "shared" {
		t.Fatalf("冲突对象字段不符: %+v", object)
	}
	if len(object.DirectModelIDs) != 1 || len(object.MappingIDs) != 1 || object.Resolution != "" {
		t.Fatalf("冲突对象 direct/mapping/resolution 不符: %+v", object)
	}
	// direct 多模型同名（不同 provider 的同名字符串）→ 1 个冲突。
	dupDirect := newTestSource()
	addRows(t, dupDirect, "accounts", gptAccountFixture("acc-2", "profile_gpt_openai_v1"))
	addRows(t, dupDirect, "provider_model_catalog",
		pricedCatalogRow("cat-a", "gpt", "twin"),
		pricedCatalogRow("cat-b", "openai", "twin"),
	)
	addRows(t, dupDirect, "account_supported_models",
		map[string]any{"account_id": "acc-2", "provider_code": "gpt", "model": "twin"},
		map[string]any{"account_id": "acc-2", "provider_code": "openai", "model": "twin"},
	)
	out = applyRules(dupDirect, testNow)
	if out.blocked.PublicModelConflicts != 1 {
		t.Fatalf("direct 多模型同名应计 1 个冲突: %+v", out.blocked)
	}
	if len(out.publicConflicts[0].DirectModelIDs) != 2 {
		t.Fatalf("冲突对象应含 2 个 direct 模型: %+v", out.publicConflicts[0])
	}
	// 不同名 direct 与 mapping → 0 冲突。
	clean := newTestSource()
	addRows(t, clean, "accounts", map[string]any{
		"id": "acc-3", "system_account_id": "sys_admin", "provider_code": "openai",
		"provider_protocol_profile_id": "profile_openai_openai_v1",
		"type": "api_key", "credentials_encrypted": "v1:i:t:c", "capability_domain": "chat",
	})
	addRows(t, clean, "provider_model_catalog", pricedCatalogRow("cat-1", "openai", "direct-name"))
	addRows(t, clean, "account_supported_models", map[string]any{
		"account_id": "acc-3", "provider_code": "openai", "model": "direct-name",
	})
	addRows(t, clean, "account_model_mappings", map[string]any{
		"account_id": "acc-3", "provider_code": "openai", "source_model": "alias-name",
		"source_endpoint_family": "chat_completions", "upstream_model": "direct-name",
		"upstream_endpoint_family": "chat_completions", "enabled": int64(1),
	})
	out = applyRules(clean, testNow)
	if out.blocked.PublicModelConflicts != 0 {
		t.Fatalf("不同名 direct/mapping 不冲突: %+v", out.blocked)
	}
}

func TestProviderRowsVendorsAndRetired(t *testing.T) {
	data := newTestSource()
	addRows(t, data, "providers",
		map[string]any{"id": "p1", "code": "gpt", "name": "GPT", "enabled": int64(1)},
		map[string]any{"id": "p2", "code": "openai", "name": "OpenAI 兼容", "enabled": int64(1)},
		map[string]any{"id": "p3", "code": "hybrid", "name": "混合", "enabled": int64(1)},
		map[string]any{"id": "p4", "code": "mystery", "name": "?", "enabled": int64(1)},
	)
	out := applyRules(data, testNow)
	gpt := findRowOut(t, out, "providers", "p1")
	if gpt.TargetTable != "vendors" || gpt.Disposition != DispositionMapped {
		t.Fatalf("真实厂商行应迁 vendors: %+v", gpt)
	}
	retired := findRowOut(t, out, "providers", "p2")
	if retired.TargetTable != "" || retired.Disposition != DispositionMapped {
		t.Fatalf("openai/hybrid 行按设计退役（无目标行、非阻塞）: %+v", retired)
	}
	unknown := findRowOut(t, out, "providers", "p4")
	if unknown.Disposition != DispositionBlocked {
		t.Fatalf("未知 provider 行应 blocked: %+v", unknown)
	}
}

func TestRule1aAccountTypeValidation(t *testing.T) {
	data := newTestSource()
	addRows(t, data, "accounts",
		map[string]any{"id": "acc-bad-type", "system_account_id": "o", "provider_code": "gpt", "type": "session", "credentials_encrypted": "v1:i:t:c"},
		map[string]any{"id": "acc-mismatch", "system_account_id": "o", "provider_code": "glm", "type": "oauth", "credentials_encrypted": "v1:i:t:c"},
		map[string]any{"id": "acc-google-oauth", "system_account_id": "o", "provider_code": "gemini", "provider_protocol_profile_id": "profile_gemini_native_v1beta", "type": "google_oauth", "credentials_encrypted": "v1:i:t:c"},
	)
	out := applyRules(data, testNow)
	if out.blocked.AccountsBlocked != 3 {
		t.Fatalf("非法 type / driver 不相容 / google_oauth 不可解析应各计 1 次: %+v", out.blocked)
	}
	if !containsString(findRowOut(t, out, "accounts", "acc-google-oauth").Errors, ReasonOAuthTypeUnresolvable) {
		t.Fatalf("google_oauth 应记 oauth_type_unresolvable_dry_run")
	}
}

func TestCredentialFingerprintNeverPlaintext(t *testing.T) {
	data := newTestSource()
	addRows(t, data, "accounts", gptAccountFixture("acc-1", "profile_gpt_openai_v1"))
	out := applyRules(data, testNow)
	row := findRowOut(t, out, "accounts", "acc-1")
	fingerprint := mustString(t, row.TargetFields, "credential_fingerprint")
	if fingerprint == "v1:iv:tag:cipher" || len(fingerprint) != 64 {
		t.Fatalf("credential fingerprint 必须是 SHA-256 hex 而非原文: %q", fingerprint)
	}
}

// findAccountState 重放账户行判定以反查连接派生结果（测试辅助，纯内存只读）。
func findAccountState(t *testing.T, data *sourceData, accountID string) *accountState {
	t.Helper()
	ctx := &rulesContext{
		source:                data,
		now:                   testNow,
		modelsByProviderModel: map[string][]*modelInfo{},
		accountStates:         map[string]*accountState{},
		blockedGroups:         map[string]bool{},
		mappingM:              map[string]map[string]bool{},
	}
	row := ctx.findAccountRow(accountID)
	if row == nil {
		t.Fatalf("账户 %s 不存在", accountID)
	}
	return ctx.buildAccountState(data.table("accounts"), row, &ruleOutput{})
}
