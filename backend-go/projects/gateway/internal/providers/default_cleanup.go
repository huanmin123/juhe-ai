// default_cleanup.go carried the default health-check model reference cleanup
// ported from backend/src/storage/provider-model-default-reference-cleanup.repository.ts.
// The two storage tables it cleaned (provider_default_health_check_models /
// provider_system_default_health_check_models) are retired by 供应商与上游协议
// 面重构设计 §8.11（表与 seed 一并删除），清理随之 vacuous：执行器恒返回无
// 变更 provider codes，不再触碰任何旧表。入参/出参形状保留，调用方（自定义
// 模型补丁/删除、内置行配置补丁）的响应契约（defaultHealthCheckModelCleared
// 仅在有清理时出现）由空结果自然保持——键恒缺席。
package providers

import (
	"context"
	"database/sql"
	"strings"
)

// defaultReferenceCleanupTarget mirrors ProviderModelDefaultReferenceCleanupTarget.
type defaultReferenceCleanupTarget struct {
	ProviderCode string
	// BuiltInSourceProviderCodes / CustomSourceProviderCodes 承载旧双表的
	// 可用性 EXISTS 源集；models 单表后源恒为归一化单值，字段保留供
	// normalizeCleanupTargets 形状与调用方组装兼容。
	BuiltInSourceProviderCodes []string
	CustomSourceProviderCodes  []string
}

// defaultReferenceCleanupInput mirrors ProviderModelDefaultReferenceCleanupInput.
type defaultReferenceCleanupInput struct {
	Model              string
	Targets            []defaultReferenceCleanupTarget
	SystemAccountID    string
	ClearSystemDefault bool
}

// clearUnavailableProviderModelDefaultReferences is the retired-storage no-op:
// the preference tables are gone (设计 §8.11)，无引用可清，恒返回空变更集。
func clearUnavailableProviderModelDefaultReferences(ctx context.Context, tx *sql.Tx, s *Store, input *defaultReferenceCleanupInput) ([]string, error) {
	_ = ctx
	_ = tx
	_ = s
	_ = input
	return []string{}, nil
}

// normalizeCleanupTargets keeps the archived dedupe-per-provider-code shape
// (trim, drop empties, merge source-code sets, first-seen order) for the
// cleanup input assembly.
func normalizeCleanupTargets(targets []defaultReferenceCleanupTarget) []defaultReferenceCleanupTarget {
	type merged struct {
		builtIn map[string]bool
		custom  map[string]bool
	}
	ordered := []string{}
	byCode := map[string]*merged{}
	for _, target := range targets {
		providerCode := strings.TrimSpace(target.ProviderCode)
		if providerCode == "" {
			continue
		}
		entry, ok := byCode[providerCode]
		if !ok {
			entry = &merged{builtIn: map[string]bool{}, custom: map[string]bool{}}
			byCode[providerCode] = entry
			ordered = append(ordered, providerCode)
		}
		for _, code := range target.BuiltInSourceProviderCodes {
			if trimmed := strings.TrimSpace(code); trimmed != "" {
				entry.builtIn[trimmed] = true
			}
		}
		for _, code := range target.CustomSourceProviderCodes {
			if trimmed := strings.TrimSpace(code); trimmed != "" {
				entry.custom[trimmed] = true
			}
		}
	}
	output := make([]defaultReferenceCleanupTarget, 0, len(ordered))
	for _, providerCode := range ordered {
		entry := byCode[providerCode]
		output = append(output, defaultReferenceCleanupTarget{
			ProviderCode:               providerCode,
			BuiltInSourceProviderCodes: mapKeysSorted(entry.builtIn),
			CustomSourceProviderCodes:  mapKeysSorted(entry.custom),
		})
	}
	return output
}

func mapKeysSorted(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	for index := 1; index < len(keys); index++ {
		for position := index; position > 0 && keys[position] < keys[position-1]; position-- {
			keys[position], keys[position-1] = keys[position-1], keys[position]
		}
	}
	return keys
}
