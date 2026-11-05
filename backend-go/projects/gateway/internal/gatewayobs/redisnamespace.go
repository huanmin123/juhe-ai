package gatewayobs

import (
	"errors"
	"strings"
	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

// Redis namespace helpers mirroring backend/src/shared/redis-namespace.ts
// （与 gatewayhotquality/namespace.go 同源的本地镜像，保持切片自包含）。

const redisRootPrefix = "juhe-ai:"

var (
	errRedisNamespaceEmpty = errors.New("Redis namespace 不能为空")
	errRedisKeyEmpty       = errors.New("Redis key 不能为空")
)

// RedisNamespacePrefix mirrors redisNamespacePrefix.
func RedisNamespacePrefix(namespace string) (string, error) {
	if _, err := SanitizeRedisNamespacePart(namespace); err != nil {
		return "", err
	}
	return rediscfg.TaggedPrefix(namespace), nil
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
	if strings.TrimSpace(key) == "" {
		return "", errRedisKeyEmpty
	}
	if _, err := RedisNamespacePrefix(namespace); err != nil {
		return "", err
	}
	return rediscfg.NamespacedKey(key, namespace), nil
}
