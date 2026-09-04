package gatewayhotquality

import (
	"errors"
	"fmt"
	"strings"
)

// Redis namespace helpers mirroring backend/src/shared/redis-namespace.ts.

const redisRootPrefix = "juhe-ai:"

var (
	errRedisNamespaceEmpty = errors.New("Redis namespace 不能为空")
	errRedisKeyEmpty       = errors.New("Redis key 不能为空")
)

// RedisNamespacePrefix mirrors redisNamespacePrefix.
func RedisNamespacePrefix(namespace string) (string, error) {
	sanitized, err := SanitizeRedisNamespacePart(namespace)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%s:", redisRootPrefix, sanitized), nil
}

// SanitizeRedisNamespacePart mirrors sanitizeRedisNamespacePart.
func SanitizeRedisNamespacePart(value string) (string, error) {
	normalized := strings.TrimSpace(value)
	var builder strings.Builder
	for _, r := range normalized {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '.', r == ':', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	normalized = strings.Trim(builder.String(), "_")
	if normalized == "" {
		return "", errRedisNamespaceEmpty
	}
	return normalized, nil
}

// RedisNamespacedKey mirrors redisNamespacedKey: keys already carrying the
// namespace prefix (or the juhe-ai root) are not double-prefixed.
func RedisNamespacedKey(namespace string, key string) (string, error) {
	normalized := strings.TrimSpace(key)
	if normalized == "" {
		return "", errRedisKeyEmpty
	}
	namespacePrefix, err := RedisNamespacePrefix(namespace)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(normalized, namespacePrefix) {
		return normalized, nil
	}
	if strings.HasPrefix(normalized, redisRootPrefix) {
		return namespacePrefix + normalized[len(redisRootPrefix):], nil
	}
	return namespacePrefix + normalized, nil
}

// normalizedNamespace accepts the short namespace or a full juhe-ai: prefixed
// namespace (mirrors the gatewayhybrid namespacedKey convention).
func normalizedNamespace(namespace string) string {
	normalized := strings.TrimRight(strings.TrimSpace(namespace), ":")
	if strings.HasPrefix(normalized, redisRootPrefix) {
		normalized = normalized[len(redisRootPrefix):]
	}
	return normalized
}

// namespacedKey is the Go-side helper used by business/key_model_runtime and
// internal/gatewayhybrid: accept the short namespace or the full juhe-ai
// prefix, never double-prefix. The namespace part is used verbatim (it is
// validated by the callers).
func namespacedKey(namespace string, key string) string {
	normalized := strings.TrimRight(strings.TrimSpace(namespace), ":")
	if !strings.HasPrefix(normalized, "juhe-ai:") {
		normalized = "juhe-ai:" + normalized
	}
	return normalized + ":" + key
}
