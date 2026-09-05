import { notifyGatewayRuntimeCacheInvalidationAsync } from '../shared/gateway-cache-invalidation.js'
import { getTraceId } from '../shared/request-context.js'
import { errorLogFields, logger } from '../shared/logger.js'

export async function notifyCommittedModelCacheInvalidationAsync(reason: string): Promise<void> {
  try {
    await notifyGatewayRuntimeCacheInvalidationAsync(reason)
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'model_cache_sync_failed_after_commit',
      reason,
      traceId: getTraceId()
    }), '模型已保存，但缓存同步失败')
  }
}
