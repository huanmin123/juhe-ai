package usagewriter

import "strings"

// usage-semantics 契约移植，对应 backend/src/modules/usage-semantics/types.ts
// （15 行契约文件）：
//
//	export interface ParsedUsageTokens { inputTokens?... }
//	export interface UsageSemantic {
//	  id: string
//	  normalizeForStorage(usage: ParsedUsageTokens): ParsedUsageTokens
//	  cacheReadRateDenominator(usage: Pick<...>): number
//	}
//
// Node 侧该契约目前没有实现类；provider driver registry 的
// usageSemanticForProfile 把语义 id（默认 'openai'）写入记录的
// usageSemantic 字段随记录透传。本包落地契约类型，并提供与 registry 默认
// 一致的 'openai' 语义实现，供写入路径做存储规范化与缓存读价分母计算；
// 其余语义由 UsageSemanticResolver port 在装配时注册。

// ParsedUsageTokens mirrors ParsedUsageTokens（types.ts 字段一一对应；
// nil 指针表示 Node undefined）。
type ParsedUsageTokens struct {
	InputTokens      *int `json:"inputTokens,omitempty"`
	OutputTokens     *int `json:"outputTokens,omitempty"`
	CacheReadTokens  *int `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens *int `json:"cacheWriteTokens,omitempty"`
	ThinkingTokens   *int `json:"thinkingTokens,omitempty"`
	ImageTokens      *int `json:"imageTokens,omitempty"`
	AudioTokens      *int `json:"audioTokens,omitempty"`
}

// UsageSemantic mirrors the UsageSemantic interface: semantic identity plus
// the two storage-side operations.
type UsageSemantic interface {
	// ID mirrors UsageSemantic.id（例如 'openai'，写入 usage_semantic 列）。
	ID() string
	// NormalizeForStorage mirrors normalizeForStorage.
	NormalizeForStorage(usage ParsedUsageTokens) ParsedUsageTokens
	// CacheReadRateDenominator mirrors cacheReadRateDenominator.
	CacheReadRateDenominator(usage ParsedUsageCacheReadRateInput) int
}

// ParsedUsageCacheReadRateInput mirrors the
// Pick<ParsedUsageTokens, 'inputTokens' | 'cacheReadTokens' |
// 'cacheWriteTokens'> parameter of cacheReadRateDenominator.
type ParsedUsageCacheReadRateInput struct {
	InputTokens      *int
	CacheReadTokens  *int
	CacheWriteTokens *int
}

// OpenAIUsageSemantic is the default 'openai' semantic (registry.ts:
// usageSemanticForProviderCode 默认 'openai'). The Node source defines no
// concrete normalizeForStorage behavior, so the default keeps token facts
// as-is and uses the OpenAI convention for the cache-read rate denominator:
// uncached input tokens only (cache reads are billed at their own rate).
type OpenAIUsageSemantic struct{}

// ID implements UsageSemantic.
func (OpenAIUsageSemantic) ID() string { return "openai" }

// NormalizeForStorage implements UsageSemantic：'openai' 语义无字段改写，
// 原样保留（语义基线；Node 无实现可对照，见包文档差异说明）。
func (OpenAIUsageSemantic) NormalizeForStorage(usage ParsedUsageTokens) ParsedUsageTokens {
	return usage
}

// CacheReadRateDenominator implements UsageSemantic.
func (OpenAIUsageSemantic) CacheReadRateDenominator(usage ParsedUsageCacheReadRateInput) int {
	return intFromPointer(usage.InputTokens)
}

// UsageSemanticResolver ports usageSemanticForProfile
// (providers/drivers/registry.ts，同 G17 ports.go 同名 port)：按语义 id 解析
// 契约实现；未知 id 回落默认语义。
type UsageSemanticResolver interface {
	UsageSemanticForID(id string) UsageSemantic
}

// DefaultUsageSemanticResolver resolves the built-in 'openai' semantic and
// falls back to it for unknown ids, mirroring usageSemanticForProfile.
type DefaultUsageSemanticResolver struct{}

// UsageSemanticForID implements UsageSemanticResolver.
func (DefaultUsageSemanticResolver) UsageSemanticForID(id string) UsageSemantic {
	var semantic OpenAIUsageSemantic
	if strings.TrimSpace(id) == semantic.ID() {
		return semantic
	}
	return OpenAIUsageSemantic{}
}

// SemanticForID resolves the semantic for a record's usageSemantic id
// through an optional resolver (nil → default).
func SemanticForID(resolver UsageSemanticResolver, id string) UsageSemantic {
	if resolver == nil {
		return OpenAIUsageSemantic{}
	}
	return resolver.UsageSemanticForID(id)
}

func intFromPointer(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
