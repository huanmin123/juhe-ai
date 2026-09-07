import { runtimeConfig } from '../../config/runtime.js'
import {
  acquireRedisQueueFence,
  releaseRedisQueueFence,
  releaseRedisQueueFenceIdempotently
} from '../../shared/redis-queue-fence.js'

const action = process.argv[2]

try {
  if (action !== 'acquire' && action !== 'release' && action !== 'release-idempotent') {
    throw new Error('用法: manage-redis-queue-fence.ts <acquire|release|release-idempotent>')
  }
  if (runtimeConfig.runtimeMode !== 'performance'
    || runtimeConfig.queueDriver !== 'redis_stream'
    || !runtimeConfig.redis.queueUrl) {
    throw new Error('queue fence 仅允许使用已配置 queue Redis 的 performance 模式')
  }
  const token = requiredEnv('JUHE_AI_QUEUE_FENCE_TOKEN')
  const changed = action === 'acquire'
    ? await acquireRedisQueueFence(runtimeConfig.redis.queueUrl, token)
    : action === 'release'
      ? await releaseRedisQueueFence(runtimeConfig.redis.queueUrl, token)
      : await releaseRedisQueueFenceIdempotently(runtimeConfig.redis.queueUrl, token)
  if (!changed) {
    throw new Error(action === 'acquire'
      ? 'queue fence 已由其他 token 持有'
      : 'queue fence token 不匹配，拒绝释放')
  }
  console.log(JSON.stringify({ event: 'redis_queue_fence_changed', action }))
} catch (error) {
  process.exitCode = 1
  console.error(JSON.stringify({
    event: 'redis_queue_fence_failed',
    action,
    error: error instanceof Error ? error.message : String(error)
  }))
}

function requiredEnv(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`${name} 不能为空`)
  return value
}
