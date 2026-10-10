package main

// /v1/models 账户并集装载（权威契约：docs/functions/网关模型列表账户并集设计.md，
// 下称"并集设计"）。
//
//   - chainGroupModelUnionLoader 实现 gatewayruntimecache.GroupModelUnionLoader
//     （并集设计 §4.1/§4.1.1/§4.2/§4.3/§6.2）：聚合 SQL + 授权门 + 映射有效性
//     过滤 + validUntil 计算。
//   - chainGatewayKeyModelCatalog 实现 gatewayresponse.ModelCatalogLoader 的
//     ListGatewayKeyModels（并集设计 §4.9/§6.1/§6.4）：并集成员 ∪ 目录字典。
//
// 静态门 SQL 逐条复用 chain_accounts.go 的 chainCandidateWhere /
// chainCandidateColumns / chainCandidateFrom 原文常量；授权门逐臂复用
// resolveGroupAccess / resolveChainAccountAccess / canScheduleChainAuthorizedAccount。
// 两侧语义以注释互引锁定，矩阵判定走 gatewayopenai 唯一函数源，禁止复刻。

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// A. 分组模型并集装载器（gatewayruntimecache.GroupModelUnionLoader）
// ---------------------------------------------------------------------------

// chainGroupModelUnionLoader 是并集数据面装载器：复用 chainAccountsSelector 的
// db/table/bind/now 注入（同一实例，db 句柄与其他 selector 来源同源），以及其
// 分组访问与授权臂判定方法。
type chainGroupModelUnionLoader struct {
	selector *chainAccountsSelector
}

// newChainGroupModelUnionLoader 装配并集装载器；selector 必须提供。
func newChainGroupModelUnionLoader(selector *chainAccountsSelector) (*chainGroupModelUnionLoader, error) {
	if selector == nil {
		return nil, fmt.Errorf("网关分组模型并集装载器需要账户选择器")
	}
	return &chainGroupModelUnionLoader{selector: selector}, nil
}

var _ gatewayruntimecache.GroupModelUnionLoader = chainGroupModelUnionLoader{}

// chainUnionMappingRow 是 account_model_mappings 的启用行投影（含 provider_code，
// 供消费侧供应商门匹配）。
type chainUnionMappingRow struct {
	providerCode string
	mapping      gatewayopenai.AccountModelMapping
}

// ListGroupModelUnion 装载一个调用方在一组分组上的模型并集（并集设计 §4.1/
// §4.1.1/§6.2/§6.3）：
//  1. 每组先做分组访问判定（resolveGroupAccess），无访问整组不贡献；
//  2. 复用调度候选 SQL 原文常量取全量候选行（差异仅两处，见
//     listUnionCandidateRows）；
//  3. 授权门按调度实态顺序逐臂执行（含后置绑定门）；
//  4. 模型事实主体批量装载 supportedModels 与启用映射行；
//  5. 映射有效性过滤走 gatewayopenai.IsAdmissibleModelMapping 唯一函数源；
//  6. validUntil = min(装载时刻+基础TTL, 全部未来到期点)。
func (l chainGroupModelUnionLoader) ListGroupModelUnion(ctx context.Context, input gatewayruntimecache.GroupModelUnionListOptions) (gatewayruntimecache.GroupModelUnionEntry, error) {
	s := l.selector
	now := s.now()
	caller := strings.TrimSpace(input.CallerSystemAccountID)
	models := map[string]bool{}
	// 默认上界 = 基础 TTL；有任何更近的未来到期点则收窄（§6.3）。
	validUntil := now.Add(gatewayruntimecache.GroupModelUnionBaseTTL)

	for _, groupID := range uniqueNonEmpty(input.GroupIDs) {
		// 分组访问（并集设计 §4.1.1 首段）：分组缺失/禁用、无组级授权或
		// settings enabled=0 → nil，整组不贡献，不能因账户归属绕过组级授权。
		groupAccess, err := s.resolveGroupAccess(ctx, groupID, caller)
		if err != nil {
			return gatewayruntimecache.GroupModelUnionEntry{}, err
		}
		if groupAccess == nil {
			continue
		}
		// validUntil 来源④：本组组级授权行到期（owner 臂为 nil）。
		absorbUnionExpiry(&validUntil, now, groupAccess.GroupAuthorizationExpiresAt)

		rows, err := l.listUnionCandidateRows(ctx, groupID, groupAccess, chainNowISO(s.now))
		if err != nil {
			return gatewayruntimecache.GroupModelUnionEntry{}, err
		}
		if len(rows) == 0 {
			continue
		}

		// 授权行批量装载（loadAccountAuthorizationsForSelection 同款：owner
		// 域组批量预取、authorized 组返回 nil map 走逐行读取——与调度同款）。
		authorizations, err := s.loadAccountAuthorizationsForSelection(ctx, rows, groupAccess, caller)
		if err != nil {
			return gatewayruntimecache.GroupModelUnionEntry{}, err
		}

		admitted := make([]*chainCandidateRow, 0, len(rows))
		for index := range rows {
			row := &rows[index]
			// 授权臂（并集设计 §4.1.1，与 resolveChainAccountAccess 顺序即语义
			// 一致：实例臂互斥最优先 → 属主臂 → 授权分组臂 → owner 域授权臂）。
			access, err := s.resolveChainAccountAccess(ctx, row, caller, groupAccess, row.AccountAuthorizationID.String, authorizations)
			if err != nil {
				return gatewayruntimecache.GroupModelUnionEntry{}, err
			}
			if access == nil {
				continue
			}
			// canScheduleChainAuthorizedAccount（chain_accounts.go:1094）。
			if !canScheduleChainAuthorizedAccount(row, authorizations, access) {
				continue
			}
			// 后置绑定门：与 chain_accounts.go ListOpenAIAccountsForGroupResult
			// 的 authorized 绑定门逐字对齐——authorized 臂要求绑定行 GroupID 与
			// AccountAuthorizationID 均非空且与运行时授权 ID 精确一致（供应不变
			// 式：正常数据恒非空；历史/不完整绑定必须拒绝）。调度侧同位置的
			// ResourceAccountID 守卫属调度臂特有，不进成员资格（并集设计 §4.1.1
			// owner 域账户授权臂允许直连账户）。
			if access.accountAccessType == chainAccountAccessAuthorized {
				if !row.GroupID.Valid || row.GroupID.String == "" ||
					!row.AccountAuthorizationID.Valid || row.AccountAuthorizationID.String == "" ||
					row.AccountAuthorizationID.String != *access.accountAuthorizationID {
					continue
				}
			}
			// validUntil 来源①②③：账户到期、来源账户到期、该行使用的账户级/
			// 实例授权行到期（owner/group_authorized 臂为 nil）。
			absorbUnionExpiry(&validUntil, now, nullStringPtr(row.AccountExpiresAt))
			absorbUnionExpiry(&validUntil, now, nullStringPtr(row.ResourceAccountExpiresAt))
			absorbUnionExpiry(&validUntil, now, access.expiresAt)
			admitted = append(admitted, row)
		}
		if len(admitted) == 0 {
			continue
		}

		// 模型事实主体批量装载（并集设计 §4.1/§6.2）。协议档案与 provider 不另
		// 查库：chainCandidateColumns 已按来源账户取 resource_* 列（实例为来源
		// 账户档案、直连为 NULL 落回自身列），resourceAccountID()/
		// resourceProviderCode() 即 COALESCE 语义。
		factIDs := make([]string, 0, len(admitted))
		seenFact := map[string]bool{}
		for _, row := range admitted {
			factID := row.resourceAccountID()
			if seenFact[factID] {
				continue
			}
			seenFact[factID] = true
			factIDs = append(factIDs, factID)
		}
		supportedModels, err := l.loadUnionSupportedModels(ctx, factIDs)
		if err != nil {
			return gatewayruntimecache.GroupModelUnionEntry{}, err
		}
		mappings, err := l.loadUnionModelMappings(ctx, factIDs)
		if err != nil {
			return gatewayruntimecache.GroupModelUnionEntry{}, err
		}

		for _, row := range admitted {
			factID := row.resourceAccountID()
			factProvider := row.resourceProviderCode()
			factModels := supportedModels[factID][factProvider]
			// 直接支持模型并入（空 SM 账户此处天然为空集，不产出任何模型——
			// 并集设计 §4.3 裁决：空集合不展开"全部模型"，无需特判）。
			for model := range factModels {
				models[model] = true
			}
			// 有效映射源并入（§4.2）：DB 层已取启用行，此处按 provider 门 +
			// IsAdmissibleModelMapping 四道代码侧判定 + upstream ∈ 事实主体 SM
			// 收敛。RuntimeAccount 视图用事实主体档案列——授权实例不得用包装
			// 实例账户档案。
			account := &gatewayopenai.RuntimeAccount{
				ProviderCode:              factProvider,
				ProviderProtocolProfileID: row.resourceProviderProtocolProfileID(),
				ProtocolCode:              row.resourceProtocolCode(),
				ProtocolVersion:           row.resourceProtocolVersion(),
			}
			for _, item := range mappings[factID] {
				// 供应商门（调度 SQL account_model_mappings.provider_code = 事实
				// 主体 provider 同条件）：账内跨供应商历史映射行不并入。
				if item.providerCode != factProvider {
					continue
				}
				if !gatewayopenai.IsAdmissibleModelMapping(item.mapping, account) {
					continue
				}
				if !factModels[item.mapping.UpstreamModel] {
					continue
				}
				models[item.mapping.SourceModel] = true
			}
		}
	}

	out := make([]string, 0, len(models))
	for model := range models {
		out = append(out, model)
	}
	sort.Strings(out)
	return gatewayruntimecache.GroupModelUnionEntry{Models: out, ValidUntil: validUntil}, nil
}

// listUnionCandidateRows 复用 chainCandidateWhere / chainCandidateColumns /
// chainCandidateFrom 原文常量（与 listCandidateRows 同一 Replacer 方式），与调度
// 基础候选窗口的差异仅两处：
//
//  1. 去掉 LIMIT 与调度排序：调度窗口的 candidateScanLimit 是"调度尝试容量"
//     语义，按排序截断候选；并集是成员资格语义，必须全量过门成员——截断会把
//     排序靠后账户的模型整批漏出列表。
//  2. 实参取放宽档：includeFlag=1（不施加 cooldown 门，§4.4 明确排除运行态）
//     且 statusSet='active','rate_limited','temporary_unavailable'（§4.4 三档
//     白名单，与调度 includeUnavailable 档一致）。
//
// 第二个绑定参数传该组 GroupOwnerSystemAccountID（分组属主域，与调度
// listCandidateRows 实参同源——ga.system_account_id 表达绑定所属属主域，调用方
// 只进授权门）；provider 参数传该组 ProviderCode。逐条谓词与 chainCandidateWhere
// 互引：group 域绑定 + ga.enabled + provider 门 + 未删 + 状态白名单 + schedulable
// + cooldown 门（此处经 includeFlag=1 旁路）+ 直连类型门/实例来源账户门 + 未过期。
func (l chainGroupModelUnionLoader) listUnionCandidateRows(ctx context.Context, groupID string, groupAccess *gatewayruntimecache.GroupUsageAccessMetadata, now string) ([]chainCandidateRow, error) {
	s := l.selector
	const unionStatusSet = "'active', 'rate_limited', 'temporary_unavailable'"
	where := strings.NewReplacer("{statusSet}", unionStatusSet).Replace(chainCandidateWhere)
	query := strings.NewReplacer(
		"{columns}", chainCandidateColumns,
		"{from}", fmt.Sprintf(chainCandidateFrom, s.table("group_accounts"), s.table("accounts"), s.table("accounts")),
		"{where}", where,
	).Replace(`SELECT {columns} {from} {where}`)
	rows, err := s.db.QueryContext(ctx, s.bind(query),
		groupID, groupAccess.GroupOwnerSystemAccountID, groupAccess.ProviderCode,
		1, now, groupAccess.ProviderCode, 1, now, now, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []chainCandidateRow{}
	for rows.Next() {
		var row chainCandidateRow
		if err := rows.Scan(candidateScanDest(&row)...); err != nil {
			return nil, err
		}
		row.ModelRank = -1
		out = append(out, row)
	}
	return out, rows.Err()
}

// loadUnionSupportedModels 按模型事实主体 ID 集批量装载 account_supported_models，
// 结果按 (accountID, providerCode) 分桶。provider 过滤在消费侧按事实主体
// provider 精确匹配（§4.1 的 asm.provider_code = COALESCE(...) 条件）；单条
// IN 查询 + 内存分桶等价且避免逐账户查询。
func (l chainGroupModelUnionLoader) loadUnionSupportedModels(ctx context.Context, factIDs []string) (map[string]map[string]map[string]bool, error) {
	s := l.selector
	out := map[string]map[string]map[string]bool{}
	ids := uniqueNonEmpty(factIDs)
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(ids)), ", ")
	query := fmt.Sprintf(`SELECT account_id, provider_code, model FROM %s WHERE account_id IN (%s)`,
		s.table("account_supported_models"), placeholders)
	rows, err := s.db.QueryContext(ctx, s.bind(query), rowsArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, providerCode, model string
		if err := rows.Scan(&accountID, &providerCode, &model); err != nil {
			return nil, err
		}
		byProvider := out[accountID]
		if byProvider == nil {
			byProvider = map[string]map[string]bool{}
			out[accountID] = byProvider
		}
		byModel := byProvider[providerCode]
		if byModel == nil {
			byModel = map[string]bool{}
			byProvider[providerCode] = byModel
		}
		byModel[model] = true
	}
	return out, rows.Err()
}

// loadUnionModelMappings 按模型事实主体 ID 集批量装载 account_model_mappings 全部
// 启用行（§4.2 的 DB 层条件：enabled = 1），行内含 source/upstream 端点族供
// IsAdmissibleModelMapping 判定。
func (l chainGroupModelUnionLoader) loadUnionModelMappings(ctx context.Context, factIDs []string) (map[string][]chainUnionMappingRow, error) {
	s := l.selector
	out := map[string][]chainUnionMappingRow{}
	ids := uniqueNonEmpty(factIDs)
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(ids)), ", ")
	// account_model_mappings 无 id 列（PK = account_id + source_model +
	// source_endpoint_family），排序按 PK 列——loadModelMappingsByAccountIds
	// 同款注释。
	query := fmt.Sprintf(`SELECT account_id, provider_code, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family
		FROM %s WHERE account_id IN (%s) AND enabled = 1
		ORDER BY account_id ASC, source_model ASC, source_endpoint_family ASC`,
		s.table("account_model_mappings"), placeholders)
	rows, err := s.db.QueryContext(ctx, s.bind(query), rowsArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID string
		var item chainUnionMappingRow
		enabled := true
		if err := rows.Scan(&accountID, &item.providerCode, &item.mapping.SourceModel, &item.mapping.SourceEndpointFamily,
			&item.mapping.UpstreamModel, &item.mapping.UpstreamEndpointFamily); err != nil {
			return nil, err
		}
		item.mapping.Enabled = &enabled
		out[accountID] = append(out[accountID], item)
	}
	return out, rows.Err()
}

// resourceProviderProtocolProfileID 返回模型事实主体的协议档案 ID：实例账户取
// 来源账户档案（resource_provider_protocol_profile_id），直连账户取自身列——
// 与 resourceProtocolCode / resourceProtocolVersion 同一 COALESCE 语义。授权
// 实例不得用包装实例账户档案（§4.2）。
func (row *chainCandidateRow) resourceProviderProtocolProfileID() string {
	if row.ResourceProviderProtocolProfileID.Valid && row.ResourceProviderProtocolProfileID.String != "" {
		return row.ResourceProviderProtocolProfileID.String
	}
	return row.ProviderProtocolProfileID.String
}

// absorbUnionExpiry 把一个未来到期点折进 validUntil（§6.3：validUntil =
// min(装载时刻+基础TTL, 最近未来到期点)；全部无未来值时保持基础 TTL）。解析
// 失败或已过期的时点按"无未来到期点"跳过——该时点只影响缓存视界上界，正确性
// 由 SQL 静态门与运行时失效链保证（基础 TTL 是最后防线），不构成数据面错误。
func absorbUnionExpiry(validUntil *time.Time, now time.Time, expiresAt *string) {
	if expiresAt == nil {
		return
	}
	trimmed := strings.TrimSpace(*expiresAt)
	if trimmed == "" {
		return
	}
	expires, err := chainRFC3339Millis(trimmed)
	if err != nil || expires <= now.UnixMilli() {
		return
	}
	if candidate := time.UnixMilli(expires); candidate.Before(*validUntil) {
		*validUntil = candidate
	}
}

// ---------------------------------------------------------------------------
// B. /v1/models 端口实现（gatewayresponse.ModelCatalogLoader）
// ---------------------------------------------------------------------------

// chainGatewayKeyModelCatalog 实现 gatewayresponse.ModelCatalogLoader 的
// ListGatewayKeyModels（§4.9/§6.1/§6.4）：并集成员 ∪ 目录字典元数据。装载失败
// 原样上抛（§4.6.2 数据面失败禁止渲染成 200 空列表，废除旧实现的吞错
// return nil）；缓存面失败由 runtime cache 按既有容错降级。
type chainGatewayKeyModelCatalog struct {
	cache *gatewayruntimecache.Service
	// now 注入测试；nil 用系统时钟（空绑定的 ValidUntil 基准）。
	now func() time.Time
}

var _ gatewayresponse.ModelCatalogLoader = chainGatewayKeyModelCatalog{}

func (c chainGatewayKeyModelCatalog) ListGatewayKeyModels(ctx context.Context, systemAccountID string, bindings []gatewayresponse.GatewayModelBinding) (gatewayresponse.GatewayKeyModelList, error) {
	if c.cache == nil {
		return gatewayresponse.GatewayKeyModelList{}, fmt.Errorf("网关模型列表装载缺少 runtime cache")
	}
	// 1. 绑定去重：排序 providerCodes（字典维度）+ 排序 groupIDs（并集维度）。
	providerCodes := make([]string, 0, len(bindings))
	groupIDs := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		providerCodes = append(providerCodes, binding.ProviderCode)
		groupIDs = append(groupIDs, binding.GroupID)
	}
	codes := sortedUniqueProviderCodes(providerCodes)
	groups := uniqueNonEmpty(groupIDs)
	sort.Strings(groups)
	if len(codes) == 0 {
		// §4.7 空态：无激活绑定 → 空列表（HTTP 200），不触发并集装载。
		return gatewayresponse.GatewayKeyModelList{
			Entries:    []gatewayresponse.ModelCatalogEntry{},
			ValidUntil: c.nowFn().Add(gatewayruntimecache.GroupModelUnionBaseTTL),
		}, nil
	}

	validUntil := c.nowFn().Add(gatewayruntimecache.GroupModelUnionBaseTTL)
	var unionModels []string
	if len(groups) > 0 {
		// 2. 并集成员（错误原样上抛；singleflight/共享缓存细节由 runtime cache
		// 承担，§6.3）。
		union, err := c.cache.ListCachedGroupModelUnionAsync(ctx, gatewayruntimecache.GroupModelUnionListOptions{
			CallerSystemAccountID: systemAccountID,
			GroupIDs:              groups,
		})
		if err != nil {
			return gatewayresponse.GatewayKeyModelList{}, err
		}
		unionModels = union.Models
		validUntil = union.ValidUntil
	}

	// 3. 元数据字典（§4.9.1）：按去重 providerCode 复用现有目录缓存路径；
	// active 行内按 scope 优先级（personal > global > built_in）取每模型最优行。
	// 成员过滤职责（可见性/价格/模式）已退役——目录只回答"元数据长什么样"。
	dictionary := map[string]gatewayruntimecache.ProviderModelCatalogItem{}
	for _, code := range codes {
		catalog, err := c.cache.ListCachedProviderModelCatalogAsync(ctx, gatewayruntimecache.ModelCatalogListOptions{
			ProviderCode:    code,
			SystemAccountID: systemAccountID,
		})
		if err != nil {
			return gatewayresponse.GatewayKeyModelList{}, fmt.Errorf("装载供应商模型目录失败（provider=%s）: %w", code, err)
		}
		for _, item := range catalog {
			if item.Status != "active" {
				continue
			}
			model := strings.TrimSpace(item.Model)
			if model == "" {
				continue
			}
			if existing, ok := dictionary[model]; ok && clientCatalogScopeRank(item) <= clientCatalogScopeRank(existing) {
				continue
			}
			dictionary[model] = item
		}
	}

	// 4. 合并（§4.9.2/§4.9.3）：命中沿用目录条目全字段；未命中合成仅含 Model
	// 的零值条目，默认值由现有响应 builder 出。
	entries := make([]gatewayresponse.ModelCatalogEntry, 0, len(unionModels))
	for _, model := range unionModels {
		if item, ok := dictionary[model]; ok {
			entries = append(entries, clientCatalogEntryOf(item))
			continue
		}
		entries = append(entries, gatewayresponse.ModelCatalogEntry{Model: model})
	}
	// 5. 排序在合并后统一执行（§4.9.4 字典序升序，替代旧目录运营排序
	// clientCatalogCompareItems；并集装载器已升序，端口侧复排保证契约）。
	sort.Slice(entries, func(i, j int) bool { return entries[i].Model < entries[j].Model })
	return gatewayresponse.GatewayKeyModelList{Entries: entries, ValidUntil: validUntil}, nil
}

// nowFn 返回注入时钟或系统时钟。
func (c chainGatewayKeyModelCatalog) nowFn() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}
