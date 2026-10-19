package accounts

// Provider 模型目录校验域：支持模型与映射的写入断言（目录断言切 models 表，
// 供应商与上游协议面重构设计 §4/§5.1；#B1 剩余①的目录段）。models 单表身份
// 键 = vendor_code + upstream_model + owner 可见性 + status=active，与写入面
// replaceAccountSupportedModels / replaceAccountModelMappings 的
// resolveEntryModelID 同键同序（同 internal/business/accounts.
// resolveSupportedModelID 口径）。旧协议档案目录池（provider_protocol_profiles
// 守卫 + AccountModelCatalogReader 端口读取 + supportedApiProtocols 成员判定）
// 随 legacy 面拆除：模式声明联动由连接行词表（§2.2）、映射 §5.3 与健康检查
// 三字段写侧校验承载。
//
// AccountModelCatalogReader 端口类型保留为编译兼容遗留（cmd 组合根
// compose.go 仍装配该端口，cmd 生产代码本批禁改）；断言链已不消费端口，端口
// 拆除随组合根收口切片处理。
//
// gpt 请求覆盖（service_tier_override / reasoning_effort_override）的目录
// 断言层（归档 account-gpt-request-overrides.validation.ts）已于 2026-10-18
// 简化定案删除：值域改为协议族全集（shared/platform requestoverride），在
// 写入面凭据归一化（credentials_normalize.go
// normalizeGPTAccountRequestOverrides）执行「token 形状 + 协议族值域」，
// 最终上游模型是否支持该档位由运行时降级/跳过，模型目录不再参与写入面
// 判定；Gemini Interactions-only 守卫同步收拢到同一处。

import (
	"context"
	"strings"
)

// AccountModelCatalogFact mirrors the catalog row fields the accounts
// validation family consumed: the protocol columns of the mapping pool checks
// plus the capability lists carried by the same row. 编译兼容遗留（见文件头）：
// 断言链已切 models 表身份解析，不再消费该形状。
type AccountModelCatalogFact struct {
	Model                     string
	SupportedAPIProtocols     []string
	SupportedServiceTiers     []string
	SupportedReasoningEfforts []string
}

// AccountModelCatalogReader ports listProviderModelCatalog(Async)
// (model-catalog.service.ts). 编译兼容遗留（见文件头）：cmd 组合根仍装配，
// 断言链不再调用。
type AccountModelCatalogReader interface {
	ListAccountModelCatalog(ctx context.Context, providerCode, systemAccountID string, includeUnpriced bool) ([]AccountModelCatalogFact, error)
}

// SetModelCatalogReader wires the catalog port (composition-root handover).
// 编译兼容遗留（见文件头）：端口仅被保存，断言链不消费。
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

// ---- model mapping catalog checks ----

// assertAccountModelMappingsInProviderCatalog 是映射写入断言（设计 §5.1/§5.2
// + 目录断言切 models 表）：
//
//   - 协议矩阵门（纯函数，无表读取）：非 hybrid 走
//     assertAccountModelMappingProtocolAllowed（per-profile 白名单 + 上游
//     endpoint-mode 能力门），hybrid 走 assertHybridAccountModelMappingProtocol
//     Allowed（跨协议矩阵）。batch/import 以行内协议列喂参。
//   - 目录段（models 表身份解析）：每条映射的 target 模型名必须解析到 owner
//     可见且 active 的 models 行（vendor_code + upstream_model，与写入面
//     resolveEntryModelID 同键）。来源模型是客户端 wire model 字符串、不作
//     models 外键（§5.1），旧来源目录段与协议池段随 legacy 面拆除。
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
		return nil
	}
	for _, mapping := range mappings {
		if err := assertAccountModelMappingProtocolAllowed(mapping, profile, supportedEndpointModes); err != nil {
			return err
		}
	}
	vendorNull, _ := accountVendorIdentity(providerCode)
	invalidUpstreamModels := []string{}
	for _, mapping := range mappings {
		if _, err := s.resolveEntryModelID(ctx, q, vendorNull.String, systemAccountID, mapping.UpstreamModel); err != nil {
			invalidUpstreamModels = append(invalidUpstreamModels, mapping.UpstreamModel)
		}
	}
	if len(invalidUpstreamModels) > 0 {
		return &ValidationError{Message: "账号模型别名目标模型不在当前供应商模型目录中：" + joinMappingModelSample(invalidUpstreamModels)}
	}
	return nil
}

// assertAccountSupportedModelsInProviderCatalog 是支持模型目录断言（设计 §4，
// 目录断言切 models 表）：每个支持模型必须解析到一行 owner 可见且 active 的
// models 行（vendor_code + upstream_model 身份键，与
// replaceAccountSupportedModels 的 resolveEntryModelID 同键同序、同
// internal/business/accounts.resolveSupportedModelID 口径）；解析失败按
// ValidationError（400 族）列出越界样本。空集直通（非空由
// assertSupportedModelsRequired 在各消费链前置拒绝）。
func (s *Store) assertAccountSupportedModelsInProviderCatalog(ctx context.Context, q queryer, models []string, providerCode, systemAccountID string) error {
	if len(models) == 0 {
		return nil
	}
	vendorNull, _ := accountVendorIdentity(providerCode)
	invalidModels := []string{}
	for _, model := range models {
		if _, err := s.resolveEntryModelID(ctx, q, vendorNull.String, systemAccountID, model); err != nil {
			invalidModels = append(invalidModels, model)
		}
	}
	if len(invalidModels) > 0 {
		return &ValidationError{Message: "账户支持模型不在供应商模型目录中：" + joinMappingModelSample(invalidModels)}
	}
	return nil
}

// joinMappingModelSample mirrors the `slice(0, 5).join('、')` error samples.
func joinMappingModelSample(models []string) string {
	if len(models) > 5 {
		models = models[:5]
	}
	return strings.Join(models, "、")
}
