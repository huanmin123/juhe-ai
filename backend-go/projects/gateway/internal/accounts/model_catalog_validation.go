package accounts

// Provider 模型目录校验域：补齐此前登记「依赖 provider model catalog」的
// 两处 M09 deferral（batch.go prepareBatchUpdates 注释、import.go
// validateImportModelCatalogFields 注释所指的 model-validation companion
// slice 的目录段）。对照归档实现：
//
//   - gpt 请求覆盖目录断言：
//     modules/accounts/account-gpt-request-overrides.validation.ts
//     (assertAccountGptRequestOverridesSupportedAsync +
//     assertAccountGptRequestOverridesSupportedByCatalog + the provider
//     wire-mapping whitelist)。
//   - modelMappings 来源/目标模型目录校验：
//     storage/account-model-normalization.ts
//     (normalizeAccountModelMappingsForProvider(Async) 非 hybrid 分支的
//     upstreamModelPoolForAccount / accountEndpointModelPoolForAccount 段，
//     含 isProtocolProviderCode 守卫)。
//
// 目录读取通过窄接口 AccountModelCatalogReader 注入（组合根把
// internal/providers Store.ListProviderModelsForRequest 适配上来），nil 端口
// 保持断言 no-op（与 authorized/testEffects 等既有端口的 self-contained
// 约定一致，既有测试与隔离部署不受影响）。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// AccountModelCatalogFact mirrors the catalog row fields the accounts
// validation family consumes: GptAccountOverrideModelFact plus the protocol
// columns of the mapping pool checks (supportedApiProtocols).
type AccountModelCatalogFact struct {
	Model                     string
	SupportedAPIProtocols     []string
	SupportedServiceTiers     []string
	SupportedReasoningEfforts []string
}

// AccountModelCatalogReader ports listProviderModelCatalog(Async)
// (model-catalog.service.ts) for the accounts validation family.
// includeUnpriced=false keeps the priced-only default (the mapping pool call
// sites omit the flag); the gpt request-override call sites pass true
// (归档 includeUnpriced: true). Inactive rows stay filtered (Node never lifts
// the availability filter on these paths).
type AccountModelCatalogReader interface {
	ListAccountModelCatalog(ctx context.Context, providerCode, systemAccountID string, includeUnpriced bool) ([]AccountModelCatalogFact, error)
}

// SetModelCatalogReader wires the catalog port (composition-root handover).
// A nil reader keeps both catalog assertions no-ops so the slice stays
// self-contained (store-level tests, isolated deployments).
func (s *Store) SetModelCatalogReader(reader AccountModelCatalogReader) {
	s.modelCatalog = reader
}

// ---- gpt request overrides (account-gpt-request-overrides.validation.ts) ----

// accountGptRequestOverrides mirrors GptAccountRequestOverrides.
type accountGptRequestOverrides struct {
	serviceTier     string
	reasoningEffort string
}

// readAccountGptRequestOverrides mirrors readGptAccountRequestOverrides
// (providers/drivers/gpt/request-overrides.ts): the trimmed credential tokens
// behind service_tier_override / reasoning_effort_override.
func readAccountGptRequestOverrides(credentials Credentials) accountGptRequestOverrides {
	return accountGptRequestOverrides{
		serviceTier:     credentialOverrideTokenText(credentials["service_tier_override"]),
		reasoningEffort: credentialOverrideTokenText(credentials["reasoning_effort_override"]),
	}
}

func credentialOverrideTokenText(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

// accountGptRequestOverridesNeedModelCatalog mirrors the same-named helper:
// whether the credential record carries any request override at all.
func accountGptRequestOverridesNeedModelCatalog(credentials Credentials) bool {
	overrides := readAccountGptRequestOverrides(credentials)
	return overrides.serviceTier != "" || overrides.reasoningEffort != ""
}

// accountGptRequestOverridesInput mirrors the assert input object.
type accountGptRequestOverridesInput struct {
	ProviderCode    string
	AccountType     string
	Credentials     Credentials
	SupportedModels []string
	SystemAccountID string
}

// assertAccountGptRequestOverridesSupported mirrors
// assertAccountGptRequestOverridesSupportedAsync: credentials without any
// override skip the catalog round-trip (归档 readGptAccountRequestOverrides
// 早退), a nil port keeps the assertion a no-op.
func (s *Store) assertAccountGptRequestOverridesSupported(ctx context.Context, input accountGptRequestOverridesInput) error {
	overrides := readAccountGptRequestOverrides(input.Credentials)
	if overrides.serviceTier == "" && overrides.reasoningEffort == "" {
		return nil
	}
	if s.modelCatalog == nil {
		return nil
	}
	catalog, err := s.modelCatalog.ListAccountModelCatalog(ctx, input.ProviderCode, input.SystemAccountID, true)
	if err != nil {
		return err
	}
	return assertAccountGptRequestOverridesSupportedByCatalog(accountGptRequestOverridesCatalogInput{
		ProviderCode:           input.ProviderCode,
		AccountType:            input.AccountType,
		Overrides:              overrides,
		SupportedModels:        input.SupportedModels,
		Catalog:                catalog,
		SupportedEndpointModes: credentialEndpointModes(input.Credentials),
	})
}

// accountGptRequestOverridesCatalogInput mirrors the
// assertAccountGptRequestOverridesSupportedByCatalog input object.
type accountGptRequestOverridesCatalogInput struct {
	ProviderCode           string
	AccountType            string
	Overrides              accountGptRequestOverrides
	SupportedModels        []string
	Catalog                []AccountModelCatalogFact
	SupportedEndpointModes []string
}

// assertAccountGptRequestOverridesSupportedByCatalog mirrors the same-named
// function: the provider wire-mapping whitelist, the Interactions-only Gemini
// guard, the required supported-model set and the per-model service-tier /
// reasoning-effort capability checks (identical error copy).
func assertAccountGptRequestOverridesSupportedByCatalog(input accountGptRequestOverridesCatalogInput) error {
	if err := assertProviderSupportsAccountRequestOverrides(input.ProviderCode); err != nil {
		return err
	}
	// 归档 gemini 分支只在实际读到 endpoint modes 数组时启用；空/缺失数组
	// (undefined) 不触发该守卫。
	if input.ProviderCode == geminiProviderCode && input.SupportedEndpointModes != nil &&
		!containsString(input.SupportedEndpointModes, "generate_content_json") &&
		!containsString(input.SupportedEndpointModes, "generate_content_sse") {
		return &ValidationError{Message: "Interactions-only Gemini 账户不能配置 GenerateContent 请求覆盖"}
	}
	supportedModels := uniqueTextListInOrder(input.SupportedModels)
	if len(supportedModels) == 0 {
		return &ValidationError{Message: "请求覆盖要求账户至少配置一个支持模型"}
	}
	catalogByModel := map[string]AccountModelCatalogFact{}
	for _, item := range input.Catalog {
		catalogByModel[strings.TrimSpace(item.Model)] = item
	}
	modelItems := []AccountModelCatalogFact{}
	for _, model := range supportedModels {
		if item, ok := catalogByModel[model]; ok {
			modelItems = append(modelItems, item)
		}
	}
	if input.Overrides.serviceTier != "" {
		requiredTier := input.Overrides.serviceTier
		if requiredTier == "default" {
			requiredTier = ""
		}
		supported := false
		for _, item := range modelItems {
			if requiredTier != "" {
				if containsString(item.SupportedServiceTiers, requiredTier) {
					supported = true
					break
				}
			} else if len(item.SupportedServiceTiers) > 0 {
				supported = true
				break
			}
		}
		if !supported {
			if requiredTier != "" {
				return &ValidationError{Message: "所选支持模型中没有模型支持服务等级 " + requiredTier}
			}
			return &ValidationError{Message: "所选支持模型中没有模型声明服务等级覆盖"}
		}
	}
	if input.Overrides.reasoningEffort != "" {
		supported := false
		for _, item := range modelItems {
			if containsString(item.SupportedReasoningEfforts, input.Overrides.reasoningEffort) {
				supported = true
				break
			}
		}
		if !supported {
			return &ValidationError{Message: "所选支持模型中没有模型支持思考级别 " + input.Overrides.reasoningEffort}
		}
	}
	return nil
}

// assertProviderSupportsAccountRequestOverrides mirrors the same-named
// helper: the raw provider code must be one of the four wire-mapped codes
// (Node checks the un-normalized value against the literal set).
func assertProviderSupportsAccountRequestOverrides(providerCode string) error {
	switch providerCode {
	case gptVendorCode, openAICompatibleProviderCodeConstant, anthropicProviderCode, geminiProviderCode:
		return nil
	default:
		return &ValidationError{Message: "供应商 " + providerCode + " 没有可确认的账户请求覆盖 wire 映射"}
	}
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

// uniqueTextListInOrder mirrors uniqueTextList: trimmed, blanks dropped,
// duplicates dropped, first-occurrence order preserved.
func uniqueTextListInOrder(values []string) []string {
	output := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		normalized := strings.TrimSpace(value)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		output = append(output, normalized)
	}
	return output
}

// ---- model mapping catalog checks (account-model-normalization.ts) ----

// assertAccountModelMappingsInProviderCatalog ports the catalog segment of
// normalizeAccountModelMappingsForProvider(Async), the non-hybrid branch:
// 来源/目标模型都必须落在当前供应商模型目录中，且目标模型必须声明对应的
// 上游协议（supportedApiProtocols）。Hybrid providers 走跨协议模型池家族
// （assertMappingModelsInProtocolPools + hybrid 端点形态矩阵），仍留在
// model-validation companion slice（见报告遗留项）。q 必须传入调用方事务：
// 协议守卫查询与写入同事务执行（SQLite 单连接池下事务内另开连接会死锁）。
func (s *Store) assertAccountModelMappingsInProviderCatalog(ctx context.Context, q queryer, providerCode, systemAccountID string, profile protocolPredicateInput, mappings []ModelMapping) error {
	if len(mappings) == 0 || isHybridProviderCodeToken(providerCode) {
		return nil
	}
	if s.modelCatalog == nil {
		return nil
	}
	catalog, _, err := s.guardedAccountModelCatalog(ctx, q, providerCode, systemAccountID, profile)
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
		if !familyPools[mapping.UpstreamEndpointFamily][mapping.UpstreamModel] {
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
// default includeUnpriced=false.
func (s *Store) guardedAccountModelCatalog(ctx context.Context, q queryer, providerCode, systemAccountID string, profile protocolPredicateInput) (catalog []AccountModelCatalogFact, guarded bool, err error) {
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
	catalog, err = s.modelCatalog.ListAccountModelCatalog(ctx, normalized, systemAccountID, false)
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
