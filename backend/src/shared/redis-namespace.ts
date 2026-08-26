import { runtimeConfig } from '../config/runtime.js'

const redisRootPrefix = 'juhe-ai:'

export function redisNamespacedKey(key: string): string {
  const normalized = key.trim()
  if (!normalized) {
    throw new Error('Redis key 不能为空')
  }
  const namespacePrefix = redisNamespacePrefix()
  if (normalized.startsWith(namespacePrefix)) return normalized
  if (normalized.startsWith(redisRootPrefix)) {
    return `${namespacePrefix}${normalized.slice(redisRootPrefix.length)}`
  }
  return `${namespacePrefix}${normalized}`
}

export function redisNamespacedGroup(groupName: string): string {
  return redisNamespacedKey(groupName)
}

export function redisNamespacePrefix(): string {
  return `${redisRootPrefix}${sanitizeRedisNamespacePart(runtimeConfig.redis.namespace)}:`
}

export function sanitizeRedisNamespacePart(value: string): string {
  const normalized = value.trim().replace(/[^a-zA-Z0-9_.:-]+/g, '_').replace(/^_+|_+$/g, '')
  if (!normalized) {
    throw new Error('Redis namespace 不能为空')
  }
  return normalized
}
