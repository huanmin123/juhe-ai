import { createSharedJsonCache } from '../../../shared/cache.js'
import { registerGatewayRuntimeCacheInvalidator } from '../../../shared/gateway-cache-invalidation.js'
import { errorLogFields, logger } from '../../../shared/logger.js'

type ModelsResponseProtocol = 'openai' | 'anthropic' | 'gemini'
type ModelsResponseVariant = 'default' | 'openai' | 'codex'

interface ModelsResponseCacheKeyInput {
  systemAccountId: string
  providerCodes: readonly string[]
  protocol: ModelsResponseProtocol
  variant: ModelsResponseVariant
}

const authenticatedModelsResponseCache = createSharedJsonCache<object>({
  name: 'gateway_authenticated_models_response',
  max: 20_000,
  ttlMs: 30_000
})

export async function getAuthenticatedModelsResponseCache(input: ModelsResponseCacheKeyInput): Promise<object | undefined> {
  try {
    return await authenticatedModelsResponseCache.get(modelsResponseCacheKey(input))
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'authenticated_models_response_cache_read_failed'
    }), '读取认证模型列表最终响应缓存失败，本次回源构建')
    return undefined
  }
}

export async function setAuthenticatedModelsResponseCache(
  input: ModelsResponseCacheKeyInput,
  responsePayload: object
): Promise<void> {
  try {
    await authenticatedModelsResponseCache.set(modelsResponseCacheKey(input), responsePayload, { ttlMs: 30_000 })
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'authenticated_models_response_cache_write_failed'
    }), '写入认证模型列表最终响应缓存失败，本次响应继续返回')
  }
}

export async function clearAuthenticatedModelsResponseCache(): Promise<void> {
  try {
    await authenticatedModelsResponseCache.clear()
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'authenticated_models_response_cache_clear_failed'
    }), '清理认证模型列表最终响应缓存失败，等待 30 秒 TTL 自然失效')
  }
}

function modelsResponseCacheKey(input: ModelsResponseCacheKeyInput): string {
  const providerCodes = [...new Set(input.providerCodes.map((item) => item.trim()).filter(Boolean))].sort()
  return JSON.stringify([
    input.systemAccountId.trim(),
    providerCodes,
    input.protocol,
    input.variant
  ])
}

registerGatewayRuntimeCacheInvalidator(clearAuthenticatedModelsResponseCache)
