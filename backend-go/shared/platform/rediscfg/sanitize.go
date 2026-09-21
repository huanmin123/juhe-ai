// Package rediscfg 提供 Redis 键名清洗与命名空间键构造的统一实现。
// 收敛 6 份 sanitizeRedisName/SanitizeRedisNamespacePart 副本（gatewaycircuit
// 与 circuitstore 逐字节相同的成对副本、gatewayproxyhealth、gatewayclientip
// 变体；评估文档 R1 取证），语义对齐既有实现：合法输入逐字节不变。
package rediscfg

import "strings"

// SanitizeRedisName 把名称清洗为 [a-zA-Z0-9:_-]，其余字符替换为 `_`。
func SanitizeRedisName(name string) string {
	trimmed := strings.TrimSpace(name)
	var out strings.Builder
	for _, c := range trimmed {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == ':', c == '_', c == '-':
			out.WriteRune(c)
		default:
			out.WriteRune('_')
		}
	}
	return out.String()
}

// SanitizeRedisNamespacePart 清洗命名空间段：非法字符折叠为单个 `_`，
// 允许 `.`，首尾 `_` 去除，空值返回空串。
func SanitizeRedisNamespacePart(value string) string {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return ""
	}
	var out strings.Builder
	var lastUnderscore bool
	for _, c := range normalized {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.', c == ':', c == '-':
			out.WriteRune(c)
			lastUnderscore = false
		default:
			if !lastUnderscore {
				out.WriteRune('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(out.String(), "_")
}

// CanonicalRedisNamespace 把 JUHE_AI_REDIS_NAMESPACE 配置值规范为剥除
// `juhe-ai:` 根前缀的短形式（加载层单点收敛，2026-09-22）：全前缀配置
// （`juhe-ai:dev`）与短名（`dev`）落同一短形式，避免下游直接拼接型实现
// 产生 `juhe-ai:juhe-ai:...` 键空间分裂；短名输入逐字节不变。尾随冒号与
// 空白一并清除，空值返回空串（沿用调用点的缺省/校验语义）。
func CanonicalRedisNamespace(value string) string {
	normalized := strings.TrimSpace(value)
	normalized = strings.TrimPrefix(normalized, "juhe-ai:")
	normalized = strings.TrimRight(normalized, ":")
	return SanitizeRedisNamespacePart(normalized)
}

// NamespacedKey 把 namespace 插在 `juhe-ai:` 根之后（与部署键位一致；
// 对齐 shared/redis-namespace.ts 语义）。key 为空 panic（调用方契约）。
func NamespacedKey(key, namespace string) string {
	normalized := strings.TrimSpace(key)
	if normalized == "" {
		panic("Redis key 不能为空")
	}
	rootPrefix := "juhe-ai:"
	ns := SanitizeRedisNamespacePart(namespace)
	if ns == "" {
		return normalized
	}
	namespacePrefix := rootPrefix + ns + ":"
	if strings.HasPrefix(normalized, namespacePrefix) {
		return normalized
	}
	if strings.HasPrefix(normalized, rootPrefix) {
		return namespacePrefix + normalized[len(rootPrefix):]
	}
	return namespacePrefix + normalized
}
