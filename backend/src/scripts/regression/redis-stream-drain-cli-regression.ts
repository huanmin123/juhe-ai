import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const source = readFileSync(fileURLToPath(new URL('../operations/drain-redis-streams.ts', import.meta.url)), 'utf8')

for (const functionName of [
  'startUsageRecordRedisStreamConsumer',
  'startAuditLogRedisStreamConsumer',
  'startOperationLogRedisStreamConsumer',
  'startPublicApiLogRedisStreamConsumer',
  'startRecordMaintenanceRedisStreamConsumer',
  'stopUsageRecordRedisStreamConsumer',
  'stopAuditLogRedisStreamConsumer',
  'stopOperationLogRedisStreamConsumer',
  'stopPublicApiLogRedisStreamConsumer',
  'stopRecordMaintenanceRedisStreamConsumer'
]) {
  assert.match(source, new RegExp(`${functionName}\\(`), `排空 CLI 必须调用 ${functionName}`)
}

assert.match(source, /JUHE_AI_QUEUE_FENCE_TOKEN/, '排空 CLI 必须要求 fence token')
assert.match(source, /redisQueueFenceKey\(\)/, '排空 CLI 必须核对 queue fence key')
assert.doesNotMatch(source, /redisQueueFenceLeaseMs|renewRedisQueueFence/, '排空 CLI 不得依赖有限租约自动解封 queue fence')
assert.match(source, /enabledDrainContracts\(\)/, '排空 CLI 必须只核对当前启用的队列')
assert.match(source, /runtimeConfig\.auditLog\.enabled/, '关闭审计采集时，不应要求空审计队列的 consumer group')
assert.match(source, /RedisStreamDrainStabilityTracker/, '排空 CLI 必须使用连续稳定窗口判定')
assert.match(source, /assertRequiredConsumerGroupsPresent/, '排空 CLI 必须在启动消费者前确认全部既有 consumer group')
assert.ok(
  source.indexOf('assertRequiredConsumerGroupsPresent(preflightSnapshot') < source.indexOf('startConsumers()'),
  '排空 CLI 的 consumer group 门禁必须先于 startConsumers，避免 one-shot 自建空 group 后误判'
)
assert.match(
  source,
  /async function drainRedisStreams\(\): Promise<void> \{[\s\S]*?let consumersStarted = false\s+try \{\s+const client = await getRedisClient\(queueUrl\)/,
  '排空 CLI 必须在创建共享 Redis client 前进入该函数的 finally 保护范围'
)
assert.ok(
  source.indexOf('const currentFenceToken = await client.get') < source.indexOf('assertRequiredConsumerGroupsPresent(preflightSnapshot'),
  '排空 CLI 必须在 preflight 前核对 fence token'
)
assert.match(source, /runtimeConfig\.processRole !== 'worker'/, '排空 CLI 必须限制 worker 进程角色')
assert.match(source, /runtimeConfig\.workerRole !== 'ingest-worker'/, '排空 CLI 必须限制 ingest-worker 角色')
assert.match(source, /runtimeConfig\.queueDriver !== 'redis_stream'/, '排空 CLI 必须限制 Redis Stream 队列驱动')
assert.doesNotMatch(source, /enqueue[A-Z]|startBackgroundJobs|from ['"].*worker\.js['"]/, '排空 CLI 不得加载 producer 或后台任务')
assert.match(source, /finally\s*{[\s\S]*consumersStarted[\s\S]*stopConsumers[\s\S]*closeRedisClients/, '排空 CLI 必须在 token 或 preflight 失败时也关闭 Redis，并只停止已启动的消费者')

console.log('redis stream drain CLI regression passed')
