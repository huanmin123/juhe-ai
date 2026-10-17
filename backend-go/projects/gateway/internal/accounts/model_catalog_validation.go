package accounts

// Provider 模型目录校验域：modelMappings 来源/目标模型目录校验，对照归档实
// 现 storage/account-model-normalization.ts
// (normalizeAccountModelMappingsForProvider(Async) 非 hybrid 分支的
// upstreamModelPoolForAccount / accountEndpointModelPoolForAccount 段，含
// isProtocolProviderCode 守卫)。hybrid 分支的矩阵与协议池校验见
// model_mapping_protocol_matrix.go（assertAccountModelMappingsInProvider
// Catalog 入口分发）。
//
// gpt 请求覆盖（service_tier_override / reasoning_effort_override）的目录
// 断言层（归档 account-gpt-request-overrides.validation.ts）已于 2026-10-18
// 简化定案删除：值域改为协议族全集（shared/platform requestoverride），在
// 写入面凭据归一化（credentials_normalize.go
// normalizeGPTAccountRequestOverrides）执行「token 形状 + 协议族值域」，
// 最终上游模型是否支持该档位由运行时降级/跳过，模型目录不再参与写入面
// 判定；Gemini Interactions-only 守卫同步收拢到同一处。
//
// 目录读取通过窄接口 AccountModelCatalogReader 注入（组合根把
// internal/providers Store.ListProviderModelsForRequest 适配上来），nil 端口
// 保持目录段断言 no-op（与 authorized/testEffects 等既有端口的 self-contained
// 约定一致，既有测试与隔离部署不受影响）。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// AccountModelCatalogFact mirrors the catalog row fields the accounts
// validation family consumes: the protocol columns of the mapping pool checks
// (supportedApiProtocols) plus the capability lists carried by the same row.
type AccountModelCatalogFact struct {
	Model                     string
	SupportedAPIProtocols     []string
	SupportedServiceTiers     []string
	SupportedReasoningEfforts []string
}

// AccountModelCatalogReader ports listProviderModelCatalog(Async)
// (model-catalog.service.ts) for the accounts validation family.
// includeUnpriced=false keeps the priced-only default (the mapping pool call
// sites omit the flag). Inactive rows stay filtered (Node never lifts
// the availability filter on these paths).
type AccountModelCatalogReader interface {
	ListAccountModelCatalog(ctx context.Context, providerCode, systemAccountID string, includeUnpriced bool) ([]AccountModelCatalogFact, error)
}

// SetModelCatalogReader wires the catalog port (composition-root handover).
// A nil reader keeps the catalog segments of the mapping/supported-model
// assertions no-ops so the slice stays self-contained (store-level tests,
// isolated deployments).
func (s *Store) SetModelCatalogReader(reader AccountModelCatalogReader) {
	s.modelCatalog = reader
}

// credentialEndpointModes mirrors endpointModesFromCredentials: the
// supported_endpoint_modes array when the credential value is one (either the
// stored []string shape or the decoded-JSON []any shape); nil otherwise.
func credentialEndpointModes(credentials Credentials) []string {
	value, ok := credentials["supported_endpoint_modes"]
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		out := []string{}
		for _, item := range typed {
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := []string{}
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

// ---- model mapping catalog checks (account-model-normalization.ts) ----

// assertAccountModelMappingsInProviderCatalog ports normalizeAccountModel
// MappingsForProvider(Async) 的写入断言主体：
//
//   - 非 hybrid：先跑协议档案映射断言（assertAccountModelMappingProtocolAllowed，
//     account-model-mapping-protocol-matrix.ts:69 的 per-profile 门禁），再走
//     目录段（来源/目标模型必须落在当前供应商模型目录中，且目标模型必须声明
//     对应的上游协议 supportedApiProtocols）。
//   - hybrid：跑跨协议矩阵断言（assertHybridAccountModelMappingProtocolAllowed）
//     与协议模型池校验（assertMappingModelsInProtocolPools，此前登记在
//     model-validation companion slice 的遗留项）。
//
// supportedEndpointModes 取自账户最终凭据的 supported_endpoint_modes（归档
// normalizedModelMappingsForPatch 的 options.supportedEndpointModes 语义）。
// q 必须传入调用方事务：协议守卫查询与写入同事务执行（SQLite 单连接池下
// 事务内另开连接会死锁）。
func (s *Store) assertAccountModelMappingsInProviderCatalog(ctx context.Context, q queryer, providerCode, systemAccountID string, profile protocolPredicateInput, mappings []ModelMapping, supportedEndpointModes []string) error {
	if len(mappings) == 0 {
		return nil
	}
	if isHybridProviderCodeToken(providerCode) {
		for _, mapping := range mappings {
			if err := assertHybridAccountModelMappingProtocolAllowed(mapping, supportedEndpointModes); err != nil {
				return err
			}
		}
		return s.assertMappingModelsInProtocolPools(ctx, q, systemAccountID, mappings)
	}
	for _, mapping := range mappings {
		if err := assertAccountModelMappingProtocolAllowed(mapping, profile, supportedEndpointModes); err != nil {
			return err
		}
	}
	if s.modelCatalog == nil {
		return nil
	}
	// M4b：媒体映射（video_generation/tts 族）的目录行无价格列（媒体价格只
	// 落在 gateway 静态 pricing），媒体映射在场时目录读取放开
	// includeUnpriced——否则 priced-only 过滤把媒体行滤掉、来源/目标模型恒
	// 报"不在当前供应商模型目录中"。纯 chat 映射维持 priced-only 既有语义。
	mediaMappingPresent := false
	for _, mapping := range mappings {
		if isMediaMappingEndpointFamily(mapping.SourceEndpointFamily) || isMediaMappingEndpointFamily(mapping.UpstreamEndpointFamily) {
			mediaMappingPresent = true
			break
		}
	}
	catalog, _, err := s.guardedAccountModelCatalog(ctx, q, providerCode, systemAccountID, profile, mediaMappingPresent)
	if err != nil {
		return err
	}
	// upstreamModelPoolForAccount 与 accountEndpointModelPoolForAccount 共享
	// 同一次 listProviderModelCatalog({providerCode, systemAccountId}) 读取
	// （Node 两个 helper 参数一致），这里合并为一次目录查询建池，行为等价。
	// guarded=false 时 catalog 为空，两个池保持为空，与 Node 的空 pool 一致。
	pool := map[string]bool{}
	familyPools := map[string]map[string]bool{}
	for _, item := range catalog {
		pool[item.Model] = true
		for _, family := range item.SupportedAPIProtocols {
			if familyPools[family] == nil {
				familyPools[family] = map[string]bool{}
			}
			familyPools[family][item.Model] = true
		}
	}
	invalidSourceModels := []string{}
	invalidUpstreamModels := []string{}
	for _, mapping := range mappings {
		if !pool[mapping.SourceModel] {
			invalidSourceModels = append(invalidSourceModels, mapping.SourceModel)
		}
		if !pool[mapping.UpstreamModel] {
			invalidUpstreamModels = append(invalidUpstreamModels, mapping.UpstreamModel)
		}
	}
	if len(invalidSourceModels) > 0 {
		return &ValidationError{Message: "账号模型别名来源模型不在当前供应商模型目录中：" + joinMappingModelSample(invalidSourceModels)}
	}
	if len(invalidUpstreamModels) > 0 {
		return &ValidationError{Message: "账号模型别名目标模型不在当前供应商模型目录中：" + joinMappingModelSample(invalidUpstreamModels)}
	}
	invalidUpstreamProtocolModels := []string{}
	for _, mapping := range mappings {
		// M4b：familyPools 按目录协议 token（supportedApiProtocols 值）建
		// 池，端点族经 mappingFamilyProtocolToken 翻译（媒体族码与协议 token
		// 不同名；chat 族两者同名，行为不变）。
		if !familyPools[mappingFamilyProtocolToken(mapping.UpstreamEndpointFamily)][mapping.UpstreamModel] {
			invalidUpstreamProtocolModels = append(invalidUpstreamProtocolModels, mapping.UpstreamModel)
		}
	}
	if len(invalidUpstreamProtocolModels) > 0 {
		return &ValidationError{Message: "账号模型别名目标模型不支持对应上游协议：" + joinMappingModelSample(invalidUpstreamProtocolModels)}
	}
	return nil
}

// guardedAccountModelCatalog mirrors the shared guard of
// upstreamModelPoolForAccount + accountEndpointModelPoolForAccount: an empty
// provider code, or a provider outside the openai protocol family without an
// anthropic/gemini protocol profile, yields no catalog at all (Node returns
// an empty pool and every model fails the membership check; guarded=false
// reproduces that empty-pool behavior). The catalog read keeps the Node
// default includeUnpriced=false; M4b 媒体映射在场时由调用方放开为 true
//（媒体目录行无价格列）。
func (s *Store) guardedAccountModelCatalog(ctx context.Context, q queryer, providerCode, systemAccountID string, profile protocolPredicateInput, includeUnpriced bool) (catalog []AccountModelCatalogFact, guarded bool, err error) {
	normalized := normalizeProviderToken(providerCode)
	if normalized == "" {
		return nil, false, nil
	}
	openAIProtocol, err := s.isProtocolProviderCode(ctx, q, normalized, openAIProtocolCode, openAIProtocolVersion)
	if err != nil {
		return nil, false, err
	}
	if !openAIProtocol && !isAnthropicProtocolProfileOf(profile) && !isGeminiProtocolProfileOf(profile) {
		return nil, false, nil
	}
	catalog, err = s.modelCatalog.ListAccountModelCatalog(ctx, normalized, systemAccountID, includeUnpriced)
	if err != nil {
		return nil, false, err
	}
	return catalog, true, nil
}

// isProtocolProviderCode mirrors storage/provider.repository.ts
// isProtocolProviderCode(Async): any enabled profile on the protocol pair
// behind an enabled provider marks the code protocol-enabled. q rides the
// caller's transaction (see assertAccountModelMappingsInProviderCatalog).
func (s *Store) isProtocolProviderCode(ctx context.Context, q queryer, providerCode, protocolCode, protocolVersion string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, s.bind(`SELECT 1
		FROM `+s.table("provider_protocol_profiles")+` ppp
		INNER JOIN `+s.table("providers")+` p ON p.code = ppp.provider_code
		WHERE ppp.provider_code = ? AND p.enabled = 1 AND ppp.enabled = 1
			AND ppp.protocol_code = ? AND ppp.protocol_version = ?
		LIMIT 1`), providerCode, protocolCode, protocolVersion).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// joinMappingModelSample mirrors the `slice(0, 5).join('、')` error samples.
func joinMappingModelSample(models []string) string {
	if len(models) > 5 {
		models = models[:5]
	}
	return strings.Join(models, "、")
}
