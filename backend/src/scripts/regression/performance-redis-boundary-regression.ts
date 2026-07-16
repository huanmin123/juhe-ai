import assert from 'node:assert/strict'
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

interface CacheHit {
  file: string
  line: number
  variableName: string
}

const scriptDir = dirname(fileURLToPath(import.meta.url))
const backendRoot = resolve(scriptDir, '../../..')
const srcRoot = resolve(backendRoot, 'src')

const localOnlyAppCaches = new Map<string, Set<string>>([
  ['modules/gateway/runtime/runtime-cache.service.ts', new Set(['gatewayRuntimeCache', 'openAIAccountsCache'])],
  ['modules/gateway/runtime/session-affinity.service.ts', new Set(['sessionAffinityCache', 'trafficMigrationPreferenceCache'])],
  ['modules/gateway/upstream/request.ts', new Set(['proxyAgents'])],
  ['modules/openai-oauth/openai-oauth-access-token-refresh.service.ts', new Set(['recentRefreshByAccountId'])]
])

const queueContracts = [
  {
    file: 'modules/gateway/usage/record-queue.service.ts',
    functionName: 'shouldEnqueueUsageRecordToRedisStream',
    startConsumer: 'startUsageRecordRedisStreamConsumer'
  },
  {
    file: 'modules/audit-logs/audit-log-queue.service.ts',
    functionName: 'shouldEnqueueAuditLogToRedisStream',
    startConsumer: 'startAuditLogRedisStreamConsumer'
  },
  {
    file: 'modules/operation-logs/operation-log-queue.service.ts',
    functionName: 'shouldEnqueueOperationLogToRedisStream',
    startConsumer: 'startOperationLogRedisStreamConsumer'
  },
  {
    file: 'modules/public-api-logs/public-api-log-queue.service.ts',
    functionName: 'shouldEnqueuePublicApiLogToRedisStream',
    startConsumer: 'startPublicApiLogRedisStreamConsumer'
  },
  {
    file: 'modules/record-maintenance/record-maintenance-queue.service.ts',
    functionName: 'shouldEnqueueRecordMaintenanceJobToRedisStream',
    startConsumer: 'startRecordMaintenanceRedisStreamConsumer'
  },
  {
    file: 'modules/runtime-logs/runtime-log-index-queue.service.ts',
    functionName: 'shouldEnqueueRuntimeLogToRedisStream',
    startConsumer: 'startRuntimeLogRedisStreamConsumer'
  }
]

const runtimeSource = source('config/runtime.ts')
assert.match(runtimeSource, /configuredRuntimeMode === 'performance'\s*\?\s*'redis'\s*:\s*'memory'/, 'performance 模式默认 cache driver 必须是 redis')
assert.match(runtimeSource, /configuredRuntimeMode === 'performance'\s*\?\s*'redis'\s*:\s*'memory'/, 'performance 模式默认 runtime state driver 必须是 redis')
assert.match(runtimeSource, /configuredRuntimeMode === 'performance'\s*\?\s*'redis_stream'\s*:\s*'memory'/, 'performance 模式默认 queue driver 必须是 redis_stream')
assert.match(runtimeSource, /JUHE_AI_RUNTIME_MODE=performance 时 JUHE_AI_CACHE_DRIVER 必须为 redis/, 'performance 模式必须拒绝 memory cache driver')
assert.match(runtimeSource, /JUHE_AI_RUNTIME_MODE=performance 时 JUHE_AI_RUNTIME_STATE_DRIVER 必须为 redis/, 'performance 模式必须拒绝 memory runtime state driver')
assert.match(runtimeSource, /JUHE_AI_RUNTIME_MODE=performance 时 JUHE_AI_QUEUE_DRIVER 必须为 redis_stream/, 'performance 模式必须拒绝 memory queue driver')
const cacheSource = source('shared/cache.ts')
assert.match(cacheSource, /export function canUseProcessLocalAppCacheAsFactSource\(\): boolean \{[\s\S]*runtimeConfig\.cacheDriver !== 'redis'/, 'performance 模式不得把进程内 AppCache 当跨进程事实源')
assert.match(functionBody(cacheSource, 'createAppCache'), /set:\s*\([^)]*\)\s*=>\s*\{[\s\S]*if \(!canUseProcessLocalAppCacheAsFactSource\(\)\) return[\s\S]*store\.set/, 'Redis cache driver 下 createAppCache.set 必须是空操作')
assert.match(functionBody(cacheSource, 'createAppCache'), /get:\s*\([^)]*\)\s*=>\s*\{[\s\S]*canUseProcessLocalAppCacheAsFactSource\(\) \? store\.get\(key\) : undefined/, 'Redis cache driver 下 createAppCache.get 必须返回 undefined')
assert.doesNotMatch(functionBody(cacheSource, 'createProcessLocalResourceCache'), /canUseProcessLocalAppCacheAsFactSource/, '进程本地资源缓存不得被 Redis cache driver 事实源开关禁用')
assert.match(source('modules/gateway/upstream/request.ts'), /createProcessLocalResourceCache<string,\s*http\.Agent>/, '代理 Agent 连接复用必须使用进程本地资源缓存，而不是业务事实源 AppCache')
assert.match(cacheSource, /export function createSharedJsonCache[\s\S]*new DriverSharedJsonCache/, 'SharedJsonCache 必须通过 driver wrapper 按运行模式选择底座')
assert.match(cacheSource, /private cache\(\): SharedJsonCache<V> \{[\s\S]*runtimeConfig\.cacheDriver !== 'redis'[\s\S]*return this\.memoryCache[\s\S]*new RedisSharedJsonCache/, 'SharedJsonCache 在 Redis cache driver 下必须直接使用 Redis')
assert.match(cacheSource, /class RedisSharedJsonCache[\s\S]*runtimeConfig\.redis\.cacheUrl/, 'Redis shared cache 必须使用 JUHE_AI_REDIS_CACHE_URL')
assert.match(cacheSource, /throw new Error\('JUHE_AI_REDIS_CACHE_URL 在 Redis cache driver 下必须配置'\)/, 'Redis shared cache 缺少 URL 时必须 fail-fast')
assert.match(cacheSource, /indexKeyPrefix[\s\S]*ZADD[\s\S]*ZCARD[\s\S]*ZRANGE[\s\S]*ZREM/, 'Redis shared cache 必须维护索引并按 options.max 淘汰，避免高基数 key 无上限增长')
assertStrictRedisCacheBoundaries()
assertPostgresAsyncRuntimeFactReads()

const runtimeStateSource = source('shared/runtime-state-store.ts')
assert.match(runtimeStateSource, /export function createRuntimeStateStore[\s\S]*runtimeConfig\.runtimeStateDriver === 'redis'[\s\S]*new RedisRuntimeStateStore/, 'RuntimeStateStore 在 Redis state driver 下必须直接使用 Redis')
assert.match(runtimeStateSource, /class RedisRuntimeStateStore[\s\S]*runtimeConfig\.redis\.stateUrl/, 'Redis runtime state 必须使用 JUHE_AI_REDIS_STATE_URL')
assert.match(runtimeStateSource, /throw new Error\('JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置'\)/, 'Redis runtime state 缺少 URL 时必须 fail-fast')
assert.match(runtimeStateSource, /eval\(incrWithMaxScript/, '运行态计数必须使用 Redis Lua 保证跨进程原子性')
assert.match(runtimeStateSource, /eval\(releaseLockScript/, '运行态锁释放必须使用 Redis Lua 校验 token')

const runtimeProbeStateSource = source('shared/runtime-probe-state-store.ts')
const redisMergeProbeStateScriptSource = templateLiteralBody(runtimeProbeStateSource, 'redisMergeProbeStateScript')
assert.match(runtimeProbeStateSource, /export function createRuntimeProbeStateStore[\s\S]*runtimeConfig\.runtimeStateDriver === 'redis'[\s\S]*new RedisRuntimeProbeStateStore/, '探针运行态在 Redis state driver 下必须直接使用 Redis')
assert.match(runtimeProbeStateSource, /class RedisRuntimeProbeStateStore[\s\S]*runtimeConfig\.redis\.stateUrl/, 'Redis 探针运行态必须使用 JUHE_AI_REDIS_STATE_URL')
assert.match(runtimeProbeStateSource, /throw new Error\('JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置'\)/, 'Redis 探针运行态缺少 URL 时必须 fail-fast')
assert.doesNotMatch(runtimeProbeStateSource, /acquireLock|releaseLock|lockPrefix|redisReleaseProbeLockScript|\bNX\b/, 'Redis 探针运行态不得引入分布式锁，重复执行必须靠 generation 幂等收敛')
assert.match(runtimeProbeStateSource, /ZADD[\s\S]*ZRANGEBYSCORE/, 'Redis 探针 due index 必须使用跨节点可见的有序索引')
assert.match(runtimeProbeStateSource, /redisNextProbeGenerationScript[\s\S]*INCR[\s\S]*PEXPIRE/, 'Redis 探针 generation 必须原子递增并带 TTL')
assert.match(runtimeProbeStateSource, /redisSetProbeStateScript[\s\S]*cjson\.decode[\s\S]*current_generation > incoming_generation[\s\S]*return 0/, 'Redis 探针状态写入必须拒绝旧 generation 覆盖新状态')
assert.match(runtimeProbeStateSource, /merge\(state: TState, ttlMs: number, options: RuntimeProbeStateMergeOptions\)/, 'Redis 探针状态必须支持原子 merge，避免高并发失败观测读改写丢失')
assert.match(runtimeProbeStateSource, /redisMergeProbeStateScript[\s\S]*incrementFields[\s\S]*unionArrayFields[\s\S]*cjson\.encode\(merged\)/, 'Redis 探针状态 merge 必须在 Lua 内累加计数并合并 distinct 观测集合')
assert.doesNotMatch(redisMergeProbeStateScriptSource, /current_generation > incoming_generation[\s\S]*return ''/, 'Redis 探针状态 merge 不能因旧 generation 直接丢弃失败观测')
assert.match(redisMergeProbeStateScriptSource, /merged\['generation'\] = current_generation[\s\S]*merged\['generation'\] = incoming_generation/, 'Redis 探针状态 merge 必须保留新旧状态中的最大 generation')
assert.match(redisMergeProbeStateScriptSource, /merged_next_probe_at[\s\S]*ZADD[\s\S]*merged_next_probe_at/, 'Redis 探针状态 merge 后 due index 分数必须使用合并后的 nextProbeAtMs')
assert.match(runtimeProbeStateSource, /deleteGeneration\(runtimeKey: string, generation: number\)[\s\S]*redisDeleteProbeStateGenerationScript[\s\S]*current_generation == target_generation/, 'Redis 探针状态清理必须支持按 generation 条件删除，避免旧探针误删新状态')

const redisStreamQueueSource = source('shared/redis-stream-queue.ts')
assert.match(redisStreamQueueSource, /redisUrl/, 'Redis Stream 队列必须显式持有 Redis queue URL')
assert.match(redisStreamQueueSource, /this\.redisUrl = options\.redisUrl \?\? requiredRedisQueueUrl\(\)/, 'Redis Stream 队列必须从 JUHE_AI_REDIS_QUEUE_URL 解析默认连接')
assert.match(redisStreamQueueSource, /createDedicatedRedisClient\(this\.redisUrl\)/, 'Redis Stream consumer 必须通过专用 Redis queue URL 建立连接')
assert.match(redisStreamQueueSource, /'XGROUP',\s*'CREATE',\s*this\.streamKey,\s*this\.groupName,\s*'0',\s*'MKSTREAM'/, 'Redis Stream consumer group 首次创建必须从 0 消费已有消息，不能用 $ 跳过 backlog')
assert.match(redisStreamQueueSource, /async inspectRuntime\(\): Promise<RedisStreamQueueRuntime>[\s\S]*XINFO[\s\S]*XPENDING[\s\S]*XLEN/, 'Redis Stream 队列必须暴露只读运行态供统计水位检查使用')
assert.match(redisStreamQueueSource, /async ack\(ids: string\[\]\): Promise<number>[\s\S]*redisAckAndDeleteMessagesScript/, 'Redis Stream 消息确认和删除必须走同一个 Lua 脚本，避免部分 ack 时误删')
assert.match(redisStreamQueueSource, /const redisAckAndDeleteMessagesScript = `[\s\S]*XACK[\s\S]*if result > 0 then[\s\S]*XDEL/, 'Redis Stream 消息成功确认后必须只删除已 ack 条目，避免 stream 长期增长')
assert.match(redisStreamQueueSource, /const redisInspectPendingMessagesScript = `[\s\S]*XPENDING[\s\S]*XRANGE/, 'Redis Stream backlog pending 消息检查必须用 Lua 一次取回 pending id 和 payload，避免按消息多次往返')
assert.match(redisStreamQueueSource, /async inspectBacklog\(limit = 256\)[\s\S]*inspectPendingMessages[\s\S]*remainingLimit[\s\S]*runtime\.lag !== undefined[\s\S]*remainingLimit === 0/, 'Redis Stream backlog 检查在 lag 缺失时必须按扫描窗口保守标记截断')

const backgroundIpcSource = source('modules/background/background-ipc.ts')
assert.match(backgroundIpcSource, /function isRedisStreamManagedIngestQueueMessage[\s\S]*background_worker_usage_records[\s\S]*background_worker_runtime_log_line/, 'Redis Stream 管理的记录类消息必须集中识别')
assert.match(backgroundIpcSource, /function queueWorkerMessage[\s\S]*runtimeConfig\.queueDriver === 'redis_stream'[\s\S]*isRedisStreamManagedIngestQueueMessage/, 'redis_stream driver 下禁止记录类消息进入后台 IPC 本地队列')
assert.match(backgroundIpcSource, /function requeueIngestWorkerMessageFirst[\s\S]*runtimeConfig\.queueDriver === 'redis_stream'[\s\S]*isRedisStreamManagedIngestQueueMessage/, 'redis_stream driver 下发送失败不能把记录类消息 requeue 回本地 IPC 队列')
assert.match(backgroundIpcSource, /function sendBackgroundWorkerMessageToParent[\s\S]*runtimeConfig\.queueDriver === 'redis_stream'[\s\S]*isRedisStreamManagedIngestQueueMessage/, 'redis_stream driver 下非 ingest worker 不能把记录类消息发回父进程 IPC')

const dbServiceIpcSource = source('modules/db-service/db-service-ipc.ts')
assert.match(dbServiceIpcSource, /function rejectRedisStreamLocalQueueForward[\s\S]*runtimeConfig\.queueDriver !== 'redis_stream'[^]*return false[\s\S]*db_service_redis_stream_local_queue_forward_rejected/, 'redis_stream driver 下 DB service 父消息不能转发记录类 IPC 本地队列')
for (const functionName of [
  'forwardUsageRecordsToWorker',
  'forwardAuditLogsToWorker',
  'forwardOperationLogsToWorker',
  'forwardPublicApiLogsToWorker',
  'forwardRuntimeLogLineToWorker',
  'forwardRecordMaintenanceJobsToWorker'
]) {
  assert.match(
    functionBody(dbServiceIpcSource, functionName),
    /rejectRedisStreamLocalQueueForward/,
    `${functionName} 必须在 redis_stream driver 下拒绝本地 IPC 转发`
  )
}

const accountConcurrencySource = source('shared/account-concurrency.ts')
assert.match(accountConcurrencySource, /runtimeConfig\.runtimeStateDriver === 'memory'[\s\S]*tryAcquireAccountConcurrency\(accountId, concurrencyLimit, options\)/, '账号并发 memory 分支只能作为 standalone 本地实现')
assert.match(accountConcurrencySource, /acquireRedisAccountConcurrency\(accountId, limit, lane, laneLimit, redisToken\)/, '账号并发在 Redis runtime state driver 下必须使用 Redis 原子占用')
assert.match(accountConcurrencySource, /redisStateClient\(\)/, '账号并发读取在 Redis runtime state driver 下必须读取 Redis state')
assert.match(accountConcurrencySource, /account-concurrency-v2/, 'Redis 账号并发必须使用带槽位租约的 v2 key，避免旧数字计数脏占用')
assert.match(accountConcurrencySource, /redisNamespacedKey\(`juhe-ai:account-concurrency-v2:\$\{accountId\}:total`\)/, 'Redis 账号并发 key 必须使用命名空间，避免多环境共享 Redis DB 时互相污染')
assert.match(accountConcurrencySource, /redisAccountConcurrencySlotLeaseTtlMs\s*=\s*90_000/, 'Redis 账号并发槽必须使用短租约，释放失败或进程退出后应及时过期')
assert.match(accountConcurrencySource, /function ensureRedisAccountConcurrencySlotRefresh\(\)/, 'Redis 账号并发活跃槽必须由当前进程定期续租')
assert.match(accountConcurrencySource, /ZREMRANGEBYSCORE/, 'Redis 账号并发读取和占用前必须清理已过租约的槽位')
assert.match(accountConcurrencySource, /function hdel_expired[\s\S]*unpack\(expired, index, last\)/, 'Redis 账号并发清理过期槽位时 HDEL 必须分批，避免大量过期 token 触发 Lua 参数上限')
assert.match(accountConcurrencySource, /redisRefreshAccountConcurrencySlotsScript[\s\S]*ZSCORE/, 'Redis 账号并发续租只能刷新仍存在的 token，禁止复活已过期租约')
assert.match(functionBody(accountConcurrencySource, 'refreshRedisAccountConcurrencySlots'), /detachExpiredRedisAccountConcurrencySlot/, 'Redis 账号并发续租发现过期 token 后必须从本机刷新集合摘除')
assert.doesNotMatch(accountConcurrencySource, /redis\.call\('INCR'/, 'Redis 账号并发不能回退简单 INCR 计数，否则进程崩溃会残留脏并发')
assert.doesNotMatch(accountConcurrencySource, /redis\.call\('DECR'/, 'Redis 账号并发不能回退简单 DECR 释放，否则无法按请求槽位精确归还')
assert.match(functionBody(accountConcurrencySource, 'tryAcquireAccountConcurrency'), /assertProcessLocalAccountConcurrencyAllowed/, 'Redis runtime state 下账号并发同步占槽必须 fail-fast')
assert.match(functionBody(accountConcurrencySource, 'loadAccountCurrentConcurrencyByIds'), /assertProcessLocalAccountConcurrencyAllowed/, 'Redis runtime state 下账号并发同步读取必须 fail-fast')
assert.match(functionBody(accountConcurrencySource, 'releaseRedisAccountConcurrencyWithRetry'), /redis_account_concurrency_release_failed/, 'Redis 账号并发释放失败不能吞错，必须重试并记录错误')

const proxyHealthSource = source('modules/gateway/runtime/proxy-health.service.ts')
assert.match(proxyHealthSource, /createRuntimeStateStore\('gateway-upstream-bucket-health'\)/, 'Redis runtime state 下上游桶健康必须写共享运行态')
assert.doesNotMatch(proxyHealthSource, /createAppCache/, '上游桶健康不能依赖 createAppCache 作为 performance 事实源')
assert.doesNotMatch(proxyHealthSource, /withRedisBucketEntryLock|upstreamBucketFailureLock|acquireLock|releaseLock|bucket-lock|运行态锁等待超时/, 'Redis 上游桶健康不能在请求路径引入分布式锁等待')
assert.match(functionBody(proxyHealthSource, 'shouldUseRedisUpstreamBucketHealthState'), /runtimeConfig\.runtimeStateDriver === 'redis'/, 'Redis 上游桶健康必须只在 Redis runtime state driver 下启用共享状态')
assert.match(functionBody(proxyHealthSource, 'orderGatewayAccountsByUpstreamBucketHealthAsync'), /shouldUseRedisUpstreamBucketHealthState\(\)[\s\S]*loadRedisBucketEntriesForAccounts[\s\S]*Promise\.all\(pendingWrites\)/, 'Redis 上游桶排序必须读取共享状态并等待半开探测写入')
assert.match(proxyHealthSource, /async function recordGatewayUpstreamBucketFailureKeyAsync[\s\S]*getRedisBucketFailureEntry[\s\S]*setRedisBucketFailureEntry/, 'Redis 上游桶失败记录必须使用无锁共享状态写入')
assert.match(source('modules/gateway/dispatch/preparation.ts'), /await orderGatewayAccountsByUpstreamBucketHealthAsync/, '调度准备必须等待 Redis 上游桶健康排序')
assert.match(source('modules/gateway/response/failure-dispatch.ts'), /await recordGatewayUpstreamBucketFailureAsync/, '上游失败处理必须等待 Redis 上游桶失败记录')
assert.match(source('modules/gateway/response/finalization.ts'), /await recordGatewayUpstreamBucketSuccessAsync/, '上游成功最终化必须等待 Redis 上游桶恢复清理')

const clientIpAccountAvoidanceSource = source('modules/gateway/runtime/client-ip-account-avoidance.service.ts')
assert.match(clientIpAccountAvoidanceSource, /createRuntimeStateStore\('gateway-client-ip-account-avoidance'\)/, 'Redis runtime state 下 Client-IP 账号回避必须写共享运行态')
assert.doesNotMatch(clientIpAccountAvoidanceSource, /createAppCache/, 'Client-IP 账号回避不能依赖 createAppCache 作为 performance 事实源')
assert.doesNotMatch(clientIpAccountAvoidanceSource, /withRedisClientIpAccountAvoidanceLock|clientIpAccountAvoidanceLock|acquireLock|releaseLock|运行态锁等待超时/, 'Redis Client-IP 账号回避不能在请求路径引入分布式锁等待')
assert.match(functionBody(clientIpAccountAvoidanceSource, 'orderOpenAIAccountsByClientIpAccountAvoidanceAsync'), /shouldUseRedisClientIpAccountAvoidanceState\(\)[\s\S]*getRedisClientIpAccountAvoidanceEntry/, 'Redis Client-IP 账号回避排序必须读取共享状态')
assert.match(functionBody(clientIpAccountAvoidanceSource, 'confirmTrackerPendingFailuresAsync'), /getRedisClientIpAccountAvoidanceEntry[\s\S]*setRedisClientIpAccountAvoidanceEntry/, 'Redis Client-IP 账号回避确认必须无锁写入共享状态')
assert.match(source('modules/gateway/dispatch/preparation.ts'), /await orderOpenAIAccountsByClientIpAccountAvoidanceAsync/, '调度准备必须等待 Redis Client-IP 账号回避排序')
assert.match(source('modules/gateway/response/finalization.ts'), /await confirmClientIpAccountAvoidanceAfterSuccessAsync/, '成功最终化必须等待 Redis Client-IP 账号回避更新')
assert.match(source('modules/gateway/routes.ts'), /await confirmCurrentClientIpAccountAvoidanceAfterFinalFailure/, '最终失败响应前必须等待 Redis Client-IP 账号回避确认')

const clientIpErrorCircuitSource = source('modules/gateway/runtime/client-ip-error-circuit.service.ts')
assert.match(clientIpErrorCircuitSource, /createRuntimeStateStore\('gateway-client-ip-error-circuit'\)/, 'Redis runtime state 下 Client-IP 错误熔断必须写共享运行态')
assert.doesNotMatch(clientIpErrorCircuitSource, /withRuntimeEntryLock|runtimeEntryLock|acquireLock|releaseLock|运行态锁等待超时/, 'Redis Client-IP 错误熔断不能在请求路径引入分布式锁等待')
assert.match(functionBody(clientIpErrorCircuitSource, 'recordPreAuthEntryAsync'), /getRuntimeEntry[\s\S]*setRuntimeEntry/, 'Redis Client-IP 认证前错误熔断必须无锁写入共享状态')
assert.match(functionBody(clientIpErrorCircuitSource, 'recordClientIpErrorCircuitSampleAsync'), /getRuntimeEntry[\s\S]*setRuntimeEntry/, 'Redis Client-IP 请求错误熔断必须无锁写入共享状态')

const codexTurnRetrySource = source('modules/gateway/client-profiles/codex-turn-retry.service.ts')
assert.match(codexTurnRetrySource, /createRuntimeStateStore\('gateway-codex-turn-retry'\)/, 'Redis runtime state 下 Codex turn retry 必须写共享运行态')
assert.doesNotMatch(codexTurnRetrySource, /createAppCache/, 'Codex turn retry 不能依赖 createAppCache 作为 performance 事实源')
assert.doesNotMatch(codexTurnRetrySource, /withRedisCodexTurnRetryLock|codexTurnRetryLock|acquireLock|releaseLock|运行态锁等待超时/, 'Redis Codex turn retry 不能在请求路径引入分布式锁等待')
assert.match(functionBody(codexTurnRetrySource, 'orderOpenAIAccountsByCodexTurnAvoidanceAsync'), /getRedisCodexTurnRetryState/, 'Redis Codex turn retry 排序必须读取共享状态')
assert.match(functionBody(codexTurnRetrySource, 'rememberCodexTurnStreamFailureAsync'), /getRedisCodexTurnRetryState[\s\S]*setRedisCodexTurnRetryState/, 'Redis Codex turn retry 失败记录必须无锁写共享状态')
assert.match(source('modules/gateway/dispatch/preparation.ts'), /await orderOpenAIAccountsByCodexTurnAvoidanceAsync/, '调度准备必须等待 Redis Codex turn retry 排序')
assert.match(source('modules/gateway/response/finalization.ts'), /await rememberCodexTurnStreamFailureAsync/, '响应最终化必须等待 Redis Codex turn retry 失败记录')
assert.match(source('modules/gateway/routes.ts'), /await rememberCodexTurnFailureWhenClientRetryIsVisible/, '可见客户端重试响应前必须等待 Redis Codex turn retry 失败记录')

const hybridAffinitySource = source('modules/gateway/hybrid/affinity.service.ts')
assert.match(hybridAffinitySource, /createRuntimeStateStore\('gateway-hybrid-route-affinity'\)/, 'Redis runtime state 下 hybrid route affinity 必须写共享运行态')
assert.doesNotMatch(hybridAffinitySource, /createAppCache/, 'hybrid route affinity 不能依赖 createAppCache 作为 performance 事实源')
assert.match(functionBody(hybridAffinitySource, 'applyHybridRouteAffinityAsync'), /hybridRouteAffinityStateStore\.getJson[\s\S]*rememberHybridRouteAffinityAsync/, 'Redis hybrid route affinity 必须读取并写入共享状态')
assert.match(source('modules/gateway/hybrid/routing.service.ts'), /await applyHybridRouteAffinityAsync/, 'hybrid routing 必须等待 Redis affinity 状态')

const normalRouteLatencySource = source('modules/gateway/runtime/normal-route-latency-degradation.service.ts')
assert.match(normalRouteLatencySource, /createRuntimeStateStore\('gateway-normal-route-latency-degradation'\)/, 'Redis runtime state 下普通路由速度优先降级必须写共享运行态')
assert.doesNotMatch(normalRouteLatencySource, /createAppCache/, '普通路由速度优先降级不能依赖 createAppCache 作为 performance 事实源')
assert.match(functionBody(normalRouteLatencySource, 'orderGatewayAccountsByNormalRouteLatencyDegradationAsync'), /await loadLatencyState/, '普通路由速度优先排序必须读取共享降级状态')
assert.match(source('modules/gateway/dispatch/preparation.ts'), /await orderGatewayAccountsByNormalRouteLatencyDegradationAsync/, '调度准备必须等待普通路由速度优先降级状态')

assertNoPerformanceLocalFactQueues()
assertRedisRuntimeQueuesAndLimits()
assertOAuthAndRateLimitRedisBoundaries()

assertRuntimeStateStoreCallsites()
assertQueueContracts()
assertAppCacheClassification()

console.log('performance-redis-boundary-regression passed')

function assertRuntimeStateStoreCallsites(): void {
  const files = listSourceFiles(srcRoot)
  const hits: Array<{ file: string; line: number; name: string }> = []
  for (const filePath of files) {
    const relativePath = slash(relative(srcRoot, filePath))
    if (relativePath.startsWith('scripts/') || relativePath === 'shared/runtime-state-store.ts') continue
    const lines = readFileSync(filePath, 'utf8').split(/\r?\n/)
    for (let index = 0; index < lines.length; index += 1) {
      const match = lines[index].match(/createRuntimeStateStore\(['"]([^'"]+)['"]\)/)
      if (!match) continue
      hits.push({ file: relativePath, line: index + 1, name: match[1] })
    }
  }
  assert.deepEqual(
    hits.map((hit) => `${hit.file}:${hit.name}`).sort(),
    [
      'modules/auth/captcha.service.ts:auth_captcha',
      'modules/auth/login-guard.service.ts:auth_login_guard',
      'modules/gateway/client-profiles/codex-turn-retry.service.ts:gateway-codex-turn-retry',
      'modules/gateway/hybrid/affinity.service.ts:gateway-hybrid-route-affinity',
      'modules/gateway/quota/quota-snapshot-cache.service.ts:gateway_quota_snapshot',
      'modules/gateway/runtime/client-ip-account-avoidance.service.ts:gateway-client-ip-account-avoidance',
      'modules/gateway/runtime/client-ip-error-circuit.service.ts:gateway-client-ip-error-circuit',
      'modules/gateway/runtime/normal-route-latency-degradation.service.ts:gateway-normal-route-latency-degradation',
      'modules/gateway/runtime/proxy-health.service.ts:gateway-upstream-bucket-health',
      'modules/openai-oauth/openai-oauth.service.ts:openai-oauth:sessions',
      'modules/openai-oauth/openai-oauth-access-token-refresh.service.ts:openai-oauth:refresh-locks',
      'shared/gateway-cache-invalidation.ts:gateway_cache_invalidation'
    ].sort(),
    `新增 RuntimeStateStore 调用点必须确认 performance 模式走 Redis，不得新增进程内跨进程事实源：${JSON.stringify(hits, null, 2)}`
  )
}

function assertQueueContracts(): void {
  const workerSource = source('worker.ts')
  for (const contract of queueContracts) {
    const content = source(contract.file)
    assert.match(content, new RegExp(`function ${contract.functionName}`), `${contract.file} 必须声明 Redis Stream 入队判定函数`)
    assert.match(content, /runtimeConfig\.queueDriver === 'redis_stream'/, `${contract.file} 必须在 redis_stream driver 下写 Redis Stream`)
    assert.match(content, new RegExp(contract.startConsumer), `${contract.file} 必须暴露 Redis Stream consumer`)
    assert.match(content, /queue\.ack\(/, `${contract.file} 必须只在成功消费后 ack Redis Stream 消息`)
    if (contract.file === 'modules/record-maintenance/record-maintenance-queue.service.ts') {
      assert.match(content, /async function enqueueRecordMaintenanceJobWithResultAsync[\s\S]*await enqueueRecordMaintenanceJobToRedisStream/, '数据维护 Redis Stream async-with-result 必须等待 XADD 成功')
      assert.match(content, /export function enqueueRecordMaintenanceJobWithResult\(input: RecordMaintenanceJob\): RecordMaintenanceEnqueueResult \{[\s\S]*droppedReason: 'redis_stream_async_required'/, '数据维护同步 withResult 在 redis_stream 下不能假报 queued=true')
      assert.doesNotMatch(content, /export function enqueueRecordMaintenanceJobWithResult\(input: RecordMaintenanceJob\): RecordMaintenanceEnqueueResult \{[\s\S]*void enqueueRecordMaintenanceJobToRedisStream/, '数据维护同步 withResult 禁止 fire-and-forget 写 Redis Stream')
    } else if (contract.file === 'modules/runtime-logs/runtime-log-index-queue.service.ts') {
      assert.match(content, /runtimeLogRedisProducer\.enqueue\(input, estimateRuntimeLogBytes\(input\)\)/, '运行日志索引 Redis Stream 入队必须先经过有界 producer')
      assert.match(content, /onDrop: recordRuntimeLogRedisStreamDrop/, '运行日志索引 Redis Stream 入队失败必须记录分类丢弃事实和可观测错误')
      assert.match(content, /disableOfflineQueue: true/, '运行日志专用 Redis producer 必须禁用 node-redis offline queue')
      assert.match(content, /commandsQueueMaxLength: runtimeLogRedisProducerCommandQueueMaxLength/, '运行日志专用 Redis producer 必须限制底层命令队列')
      assert.doesNotMatch(content, /scheduleProcessFatalError/, '派生运行日志索引单次入队失败不得终止业务主进程')
    } else {
      assert.match(content, /catch\(scheduleProcessFatalError\)/, `${contract.file} 同步入口 Redis Stream 入队失败必须进入受控 fail-fast，不能退化为未处理 Promise`)
    }
    assert.doesNotMatch(content, /AfterRedisStreamFailure/, `${contract.file} Redis Stream 入队失败后不能回退到进程内队列作为事实源`)
    assert.doesNotMatch(content, /shouldEnqueue\w+ToRedisStream[\s\S]*&&\s*!is\w+IngestWorker/, `${contract.file} redis_stream driver 下不能因为当前是 ingest-worker 而绕过 Redis Stream`)
    assert.match(content, /Redis Stream queue driver 下禁止写入/, `${contract.file} redis_stream driver 下必须禁止写入本地队列`)
    assert.match(workerSource, new RegExp(`${contract.startConsumer}\\(\\)`), `ingest-worker 必须启动 ${contract.startConsumer}`)
  }
  assert.match(workerSource, /assertLocalQueueIpcAllowed\(message\)/, 'worker 收到本地队列 IPC 消息前必须检查 redis_stream driver 边界')
  assert.match(workerSource, /Redis Stream queue driver 下禁止消费后台 IPC 本地队列消息/, 'redis_stream driver 下 worker 不能消费 IPC 本地队列消息')
}

function assertAppCacheClassification(): void {
  const files = listSourceFiles(srcRoot)
  const appCacheHits = collectCacheHits(files, /const\s+(\w+)\s*=\s*createAppCache/g)
    .filter((hit) => !hit.file.startsWith('scripts/') && hit.file !== 'shared/cache.ts')
  const sharedCacheFiles = new Set(
    collectCacheHits(files, /const\s+(\w+)\s*=\s*createSharedJsonCache/g)
      .filter((hit) => !hit.file.startsWith('scripts/') && hit.file !== 'shared/cache.ts')
      .map((hit) => hit.file)
  )
  const unclassified = appCacheHits.filter((hit) => {
    if (sharedCacheFiles.has(hit.file)) return false
    return !(localOnlyAppCaches.get(hit.file)?.has(hit.variableName))
  })
  assert.deepEqual(
    unclassified,
    [],
    '新增 createAppCache 调用必须说明边界：要么同文件有 Redis SharedJsonCache 作为跨进程事实源，要么加入本脚本 localOnlyAppCaches 并证明只是进程内易失优化'
  )
}

function assertStrictRedisCacheBoundaries(): void {
  const productionSource = listSourceFiles(srcRoot)
    .filter((filePath) => !slash(relative(srcRoot, filePath)).startsWith('scripts/'))
    .map((filePath) => readFileSync(filePath, 'utf8'))
    .join('\n')
  assert.doesNotMatch(productionSource, /throwIfRedisCacheIsRequired/, '生产代码不能保留 Redis shared cache fail-open helper')
  assert.doesNotMatch(productionSource, /shared_cache_(read|write)_failed/, 'Redis shared cache 读写失败不能被日志吞掉后当 cache miss')
  assert.doesNotMatch(productionSource, /(读取|写入)[^\n]*Redis[^\n]*(共享缓存|shared cache)[^\n]*失败/, 'Redis shared cache 读写失败必须直接抛错，不能 warn 后回退其他事实源')
  assert.doesNotMatch(productionSource, /void \w+SharedCache\.clear\(\)/, 'Redis shared cache 清理不能裸 fire-and-forget，必须使用受控后台清理 helper')
  assert.match(source('shared/cache.ts'), /export function clearSharedJsonCacheInBackground[\s\S]*cache\.clear\(\)\.catch[\s\S]*logger\.warn/, '同步失效入口的 Redis shared cache 清理必须捕获并记录失败，避免未处理 Promise')

  const invalidationSource = source('shared/gateway-cache-invalidation.ts')
  assert.match(
    functionBody(invalidationSource, 'syncGatewayCacheInvalidationsFromRuntimeState'),
    /gateway_cache_invalidation_runtime_state_sync_failed[\s\S]*throw error/,
    'Redis runtime state 失效同步失败必须抛错，不能继续使用本地缓存'
  )
  assert.match(
    invalidationSource,
    /gateway_cache_invalidation_runtime_state_publish_failed[\s\S]*runtimeConfig\.runtimeMode === 'performance'[\s\S]*(throw error|scheduleProcessFatalError\(error\))/,
    'performance 模式发布 Redis runtime state 失效版本失败必须 fail-fast'
  )

  const gatewayApiKeySource = source('storage/gateway-api-key.repository.ts')
  assert.match(
    functionBody(gatewayApiKeySource, 'validateGatewayApiKeyAsync'),
    /runtimeConfig\.cacheDriver !== 'redis'[\s\S]*gatewayApiKeyCache\.get/,
    'Redis cache driver 下 API Key 校验不能先读本地 LRU'
  )
  assert.match(
    functionBody(gatewayApiKeySource, 'setGatewayApiKeyCacheEntryAsync'),
    /await setGatewayApiKeySharedCacheEntry[\s\S]*setGatewayApiKeyCacheEntry/,
    'API Key 校验 PG 回源后必须先写 Redis shared cache，再进入本地缓存函数'
  )
  assert.match(
    functionBody(gatewayApiKeySource, 'setGatewayApiKeyCacheEntry'),
    /runtimeConfig\.cacheDriver === 'redis'[\s\S]*gatewayApiKeyCacheKeysById\.clear\(\)[\s\S]*return[\s\S]*addGatewayApiKeyCacheIndex/,
    'Redis cache driver 下 API Key 校验不得维护本地 key 索引'
  )
  assert.match(
    functionBody(gatewayApiKeySource, 'invalidateGatewayApiKeyCacheByIdAsync'),
    /await clearGatewayApiKeySharedCacheAsync/,
    'Redis cache driver 下 API Key async 失效必须等待 Redis shared cache 清理完成'
  )

  const settingsSource = source('storage/settings.repository.ts')
  assert.match(functionBody(settingsSource, 'listGlobalSettingsAsync'), /runtimeConfig\.cacheDriver !== 'redis'[\s\S]*globalSettingsCache\.get/, 'Redis cache driver 下全局设置不能先读本地 LRU')
  assert.match(functionBody(settingsSource, 'getSettingsAsync'), /runtimeConfig\.cacheDriver !== 'redis'[\s\S]*systemSettingsCache\.get/, 'Redis cache driver 下系统设置不能先读本地 LRU')
  assert.match(settingsSource, /function assertSyncSettingsReadAllowed[\s\S]*高性能模式禁止同步读取本地 settings 缓存或 SQLite/, 'PG/performance 模式必须禁止同步 settings 读取')
  assert.match(functionBody(settingsSource, 'setSystemSettingsCacheAsync'), /await setSettingsSharedCache[\s\S]*systemSettingsCache\.set/, '系统设置必须先写 Redis shared cache 再写本地近端缓存')
  assert.match(functionBody(settingsSource, 'setGlobalSettingsCacheAsync'), /await setSettingsSharedCache[\s\S]*globalSettingsCache\.set/, '全局设置必须先写 Redis shared cache 再写本地近端缓存')

  const authorizationLoaderSource = source('storage/authorization-read-loaders.ts')
  assert.match(functionBody(authorizationLoaderSource, 'loadResourceAuthorizationStatsByResourceIdsAsync'), /runtimeConfig\.cacheDriver !== 'redis'[\s\S]*authorizationStatsCache\.get/, 'Redis cache driver 下授权统计不能先读本地 LRU')
  assert.match(functionBody(authorizationLoaderSource, 'loadResourceAuthorizationStatsByResourceIdsAsync'), /await setAuthorizationStatsSharedCacheEntryAsync[\s\S]*authorizationStatsCache\.set/, '授权统计 PG 回源后必须先写 Redis shared cache')
  assert.match(functionBody(authorizationLoaderSource, 'loadResourceAuthorizationSourcesByAuthorizationIdsAsync'), /runtimeConfig\.cacheDriver !== 'redis'[\s\S]*authorizationSourcesCache\.get/, 'Redis cache driver 下授权来源不能先读本地 LRU')
  assert.match(functionBody(authorizationLoaderSource, 'loadResourceAuthorizationSourcesByAuthorizationIdsAsync'), /await setAuthorizationSourcesSharedCacheEntryAsync[\s\S]*authorizationSourcesCache\.set/, '授权来源 PG 回源后必须先写 Redis shared cache')

  const groupLoaderSource = source('storage/group-read-loaders.ts')
  assert.match(functionBody(groupLoaderSource, 'loadGroupAccountIdsByGroupIdsAsync'), /runtimeConfig\.cacheDriver !== 'redis'[\s\S]*groupAccountIdsCache\.get/, 'Redis cache driver 下分组账号 ID 不能先读本地 LRU')
  assert.match(functionBody(groupLoaderSource, 'loadGroupAccountIdsByGroupIdsAsync'), /await setGroupAccountIdsSharedCacheEntryAsync[\s\S]*groupAccountIdsCache\.set/, '分组账号 ID PG 回源后必须先写 Redis shared cache')

  const repositoryLookupSource = source('storage/repository-lookups.ts')
  assert.match(functionBody(repositoryLookupSource, 'loadCachedRowsByIdsAsync'), /await setLookupSharedCacheEntryAsync[\s\S]*result\.set/, 'repository lookup Redis miss 后必须等待 Redis shared cache 写入成功')

  const modelCatalogSource = source('modules/model-pricing/model-catalog.service.ts')
  assert.match(functionBody(modelCatalogSource, 'listProviderModelCatalogAsync'), /runtimeConfig\.cacheDriver !== 'redis'[\s\S]*providerModelCatalogCache\.get/, 'Redis cache driver 下模型目录不能先读本地 LRU')
  assert.match(functionBody(modelCatalogSource, 'setProviderModelCatalogCacheEntryAsync'), /await setProviderModelCatalogSharedCacheEntry[\s\S]*providerModelCatalogCache\.set/, '模型目录 PG 回源后必须先写 Redis shared cache')

  const clientIpPolicySource = source('modules/gateway/runtime/client-ip-policy-cache.service.ts')
  const backgroundStatsWriterSource = source('modules/background/background-stats-writer.ts')
  assert.doesNotMatch(functionBody(backgroundStatsWriterSource, 'aggregateClientIpStats'), /listActiveClientIpPoliciesAsync|sendClientIpPolicySnapshotToServer/, 'Client-IP 统计聚合不能全量读取并推送 active 策略快照')
  assert.match(functionBody(clientIpPolicySource, 'inspectClientIpPolicy'), /runtimeConfig\.cacheDriver === 'redis'[\s\S]*getClientIpPolicyByIpSharedCacheEntry[\s\S]*loadClientIpPolicyByHashFromSharedCacheOrDatabase[\s\S]*return policyDecisionFromCacheEntry[\s\S]*policyCache\.get/, 'Redis cache driver 下 IP 封禁判定必须按 IP 读取 Redis shared cache 或索引查询，不得落本地 policyCache')
  assert.match(functionBody(clientIpPolicySource, 'replaceClientIpPolicyCacheLocal'), /runtimeConfig\.cacheDriver === 'redis'[\s\S]*activePolicySnapshotLoadedAt = undefined[\s\S]*高性能模式禁止同步写入 Client-IP 策略 Redis shared cache[\s\S]*return[\s\S]*activePolicySnapshot\.set/, 'Redis cache driver 下 IP 封禁策略同步替换必须拒绝 fire-and-forget Redis 写入，且不能写本地 activePolicySnapshot')
  assert.match(functionBody(clientIpPolicySource, 'reloadClientIpPolicyCacheLocal'), /bypassSharedCache[\s\S]*clearActivePolicySnapshotSharedCache[\s\S]*clearPolicyByIpSharedCache/, 'Redis cache driver 下 IP 封禁策略失效重载必须清理旧 full snapshot 和单 IP shared cache')
  assert.match(clientIpPolicySource, /export async function replaceClientIpPolicySharedSnapshotAsync[\s\S]*setClientIpPolicyByIpSharedCacheEntry/, '显式策略快照更新必须异步写入 Redis 单 IP shared cache')
  assert.match(functionBody(clientIpPolicySource, 'loadClientIpPolicyByHashFromDatabase'), /find_active_client_ip_policy_by_hash/, 'Redis cache miss 下 IP 封禁只能按 ip_hash 走索引查询，不能全量扫描 active 策略')
  assert.match(functionBody(clientIpPolicySource, 'clearPolicyByIpSharedCache'), /await policyByIpSharedCache\.clear/, 'Client-IP 单 IP shared cache 清理必须等待 Redis 删除完成')

  const routeSelectorSource = source('modules/gateway/routing/api-key-group-route-selector.service.ts')
  assert.match(functionBody(routeSelectorSource, 'orderGatewayApiKeyGroupBindingsForDispatchAsync'), /runtimeConfig\.runtimeStateDriver !== 'redis'[\s\S]*orderGatewayApiKeyGroupBindingsForDispatch[\s\S]*nextRedisRouteCounterIndex/, '高性能动态路由必须通过 Redis 计数器共享状态')
  assert.match(functionBody(routeSelectorSource, 'assertSyncRouteStateAllowed'), /runtimeConfig\.runtimeStateDriver !== 'redis'[\s\S]*throw new Error\('高性能模式动态路由禁止使用本机同步状态/, '高性能动态路由同步本机状态必须 fail-fast')

  const accountApiKeyRotationSource = source('storage/account-api-key-rotation.ts')
  assert.match(functionBody(accountApiKeyRotationSource, 'selectAccountRuntimeApiKeyEntryAsync'), /runtimeConfig\.runtimeStateDriver !== 'redis'[\s\S]*selectAccountRuntimeApiKeyEntry[\s\S]*selectWeightedApiKeyWithRedisCounter[\s\S]*selectRoundRobinApiKeyWithRedisCounter/, '高性能账户 API Key 轮换必须通过 Redis 计数器共享状态')
  assert.match(functionBody(accountApiKeyRotationSource, 'assertSyncAccountApiKeyRotationAllowed'), /runtimeConfig\.runtimeStateDriver !== 'redis'[\s\S]*throw new Error\('高性能模式账户 API Key 轮换禁止使用本机同步状态/, '高性能账户 API Key 同步轮换必须 fail-fast')
  const accountTestQueueSource = source('modules/accounts/account-test-task-queue.service.ts')
  assert.doesNotMatch(functionBody(accountTestQueueSource, 'openAIDraftAccountSecret'), /\bselectAccountRuntimeApiKeyEntry(?:Async)?\b/, '手动账号测试草稿 API Key 选择不能推进本机或 Redis 轮换状态')
  assert.match(functionBody(accountTestQueueSource, 'openAIDraftAccountSecret'), /accountApiKeyEntries\(credentials\)\[0\][\s\S]*selectedApiKeyFingerprint[\s\S]*selectedApiKeyEntry\?\.fingerprint/, '手动账号测试草稿应无状态固定本次测试 Key')

  const quotaSnapshotSource = source('modules/gateway/quota/quota-snapshot-cache.service.ts')
  assert.match(functionBody(quotaSnapshotSource, 'replaceGatewayQuotaSnapshot'), /runtimeConfig\.cacheDriver === 'redis'[\s\S]*clearGatewayQuotaSnapshot\(\)[\s\S]*return[\s\S]*costSnapshot\.set/, 'Redis cache driver 下 quota snapshot 不得装入本机 Map')
  assert.match(functionBody(quotaSnapshotSource, 'readGatewayQuotaCostsSnapshot'), /runtimeConfig\.cacheDriver === 'redis'\) return undefined/, 'Redis cache driver 下 API Key 配额不得读取本机 cost snapshot')
  assert.match(functionBody(quotaSnapshotSource, 'readGatewayAuthorizationQuotaSnapshot'), /runtimeConfig\.cacheDriver === 'redis'\) return undefined/, 'Redis cache driver 下授权配额不得读取本机 authorization snapshot')
  assert.match(functionBody(quotaSnapshotSource, 'runtimeState'), /createRuntimeStateStore\('gateway_quota_snapshot'\)/, 'Redis cache driver 下 quota snapshot 只能通过 runtime state 共享快照，不得使用本机 Map 作为事实源')
  assert.match(functionBody(quotaSnapshotSource, 'readGatewayQuotaCostsSnapshotAsync'), /readSharedGatewayQuotaSnapshot[\s\S]*sharedSnapshotCostEntries\.get/, 'Redis cache driver 下 API Key 配额 async 快照读取必须来自共享 runtime state memo')
  assert.doesNotMatch(functionBody(quotaSnapshotSource, 'readGatewayQuotaCostsSnapshotAsync'), /costSnapshot\.get/, 'Redis cache driver 下 API Key 配额 async 快照读取不得回落本机 costSnapshot')
  assert.match(functionBody(quotaSnapshotSource, 'readGatewayAuthorizationQuotaSnapshotAsync'), /readSharedGatewayQuotaSnapshot[\s\S]*sharedSnapshotAuthorizationEntries\.get/, 'Redis cache driver 下授权配额 async 快照读取必须来自共享 runtime state memo')
  assert.doesNotMatch(functionBody(quotaSnapshotSource, 'readGatewayAuthorizationQuotaSnapshotAsync'), /authorizationSnapshot\.get/, 'Redis cache driver 下授权配额 async 快照读取不得回落本机 authorizationSnapshot')

  const apiKeyQuotaSource = source('modules/gateway/quota/api-key-quota.service.ts')
  assert.match(functionBody(apiKeyQuotaSource, 'checkGatewayApiKeyQuotaAsync'), /runtimeConfig\.cacheDriver === 'redis' && runtimeConfig\.processRole === 'server'[\s\S]*readGatewayQuotaCostsSnapshotAsync[\s\S]*requestDbService\(\{ type: 'check_api_key_quota'[\s\S]*gateway_api_key_quota_redis_exact_check_failed/, 'Redis cache driver 下 API Key 配额 shared cache miss 必须先读 runtime state snapshot，未命中再走 DB service 精确判定')
  assert.match(functionBody(apiKeyQuotaSource, 'setApiKeyQuotaCacheEntry'), /runtimeConfig\.cacheDriver === 'redis'[\s\S]*apiKeyQuotaCacheKeysById\.clear\(\)[\s\S]*return[\s\S]*addApiKeyQuotaCacheIndex/, 'Redis cache driver 下 API Key quota 不得维护本地 key 索引')

  const authorizationQuotaSource = source('modules/gateway/quota/authorization-quota.service.ts')
  assert.match(functionBody(authorizationQuotaSource, 'checkGatewayAuthorizationQuotaAsync'), /runtimeConfig\.cacheDriver === 'redis' && runtimeConfig\.processRole === 'server'[\s\S]*hasGatewayQuotaSnapshotAsync[\s\S]*authorizationQuotaDecisionFromSnapshotAsync[\s\S]*requestDbService\(\{[\s\S]*type: 'check_authorization_quota'[\s\S]*gateway_authorization_quota_redis_exact_check_failed/, 'Redis cache driver 下授权配额 shared cache miss 必须先读 runtime state snapshot，未命中再走 DB service 精确判定')
  assert.match(functionBody(authorizationQuotaSource, 'checkGatewayAuthorizationQuotaBatchAsync'), /runtimeConfig\.cacheDriver === 'redis'[\s\S]*hasGatewayQuotaSnapshotAsync[\s\S]*authorizationQuotaDecisionFromSnapshotAsync[\s\S]*type: 'check_authorization_quota_batch'[\s\S]*gateway_authorization_quota_batch_redis_exact_check_failed/, 'Redis cache driver 下授权配额批量 shared cache miss 必须先读 runtime state snapshot，未命中再走 DB service 精确判定')

  const localSuppressionSource = source('modules/gateway/runtime/account-local-suppression-store.ts')
  assert.match(functionBody(localSuppressionSource, 'canUseProcessLocalAccountRuntimeState'), /runtimeConfig\.runtimeStateDriver !== 'redis'[\s\S]*localAccountSuppressions\.clear\(\)[\s\S]*localAccountDegradations\.clear\(\)[\s\S]*return false/, 'Redis runtime state 下账号本机 suppression/degradation 必须禁用并清空')

  const accountApiKeyGuardSource = source('modules/gateway/runtime/account-api-key-failure-guard.service.ts')
  assert.match(functionBody(accountApiKeyGuardSource, 'canUseProcessLocalApiKeyRuntimeState'), /runtimeConfig\.runtimeStateDriver !== 'redis'[\s\S]*localApiKeySuppressions\.clear\(\)[\s\S]*apiKeySuccessObservations\.clear\(\)[\s\S]*return false/, 'Redis runtime state 下账户内 API Key 本机失败状态必须禁用并清空')

  const accountApiKeyEffectsSource = source('modules/gateway/runtime/account-api-key-effects.service.ts')
  assert.match(functionBody(accountApiKeyEffectsSource, 'recordGatewayAccountApiKeySuccess'), /runtimeConfig\.runtimeStateDriver !== 'redis'[\s\S]*shouldSkipRecentAccountApiKeySuccessWrite[\s\S]*runtimeConfig\.runtimeStateDriver !== 'redis'[\s\S]*rememberAccountApiKeySuccessWrite[\s\S]*recentAccountApiKeySuccessWrites\.clear\(\)/, 'Redis runtime state 下账户内 API Key 成功写入不能使用本机成功节流 Map，并必须清空旧节流状态')
  assert.doesNotMatch(functionBody(accountApiKeyEffectsSource, 'recordGatewayAccountApiKeyLocalFailure'), /recordGatewayAccountApiKeyFailure/, 'Redis runtime state 下账户内 API Key local failure 禁止直接升级为全局持久失败')

  const accountSideEffectsSource = source('modules/gateway/runtime/account-side-effects.service.ts')
  assert.doesNotMatch(accountSideEffectsSource, /gateway-account-recovery-probe-budget|distributedRecoveryProbeBudgetRuntimeStore|acquireDistributedRecoveryProbeBudget|createRuntimeStateStore\('gateway-account-recovery-probe-budget'\)|distributedRecoveryProbeStore\.acquireLock|distributedRecoveryProbeStore\.releaseLock|distributedRecoveryProbeLockTtlMs/, 'Redis 探针不能引入分布式锁或分布式全局/provider/proxy/baseUrl 预算锁，避免高并发恢复被锁残留或跨节点争用限制')
  assert.match(functionBody(accountSideEffectsSource, 'canUseProcessLocalGatewayAccountRuntimeState'), /runtimeConfig\.runtimeStateDriver !== 'redis'[\s\S]*failureStorms\.clear\(\)[\s\S]*successObservations\.clear\(\)[\s\S]*precheckStates\.clear\(\)[\s\S]*recoveryProbeStates\.clear\(\)[\s\S]*return false/, 'Redis runtime state 下账号 failure/precheck/recovery/success 本机运行态必须禁用并清空')
  assert.doesNotMatch(functionBody(accountSideEffectsSource, 'canUseProcessLocalGatewayAccountRuntimeState'), /recoveryProbeLastStartedAtByScope\.clear\(\)|runningRecoveryProbeCount = 0/, 'Redis runtime state 下读取管理快照不能清空本进程正在执行的恢复探针预算')
  assert.match(functionBody(accountSideEffectsSource, 'recordGatewayAccountFailureForPrecheckInternal'), /runtimeConfig\.runtimeStateDriver === 'redis'[\s\S]*recordDistributedGatewayAccountFailureForPrecheck[\s\S]*return/, 'Redis runtime state 下账号失败必须写入共享探针状态，不能直接使用本机 failureStorm/precheck Map')
  assert.match(accountSideEffectsSource, /distributedRecoveryProbeFailureMergeOptions[\s\S]*incrementFields: \['failureCount'\][\s\S]*clientIpMarkers[\s\S]*apiKeyMarkers/, 'Redis runtime state 下账号失败观测必须原子合并 failureCount 和 distinct Client-IP/API-Key 观测')
  assert.match(functionBody(accountSideEffectsSource, 'recordDistributedGatewayAccountFailureForPrecheck'), /runtimeProbeObservationMarker[\s\S]*mergeDistributedRecoveryProbeFailureState/, 'Redis runtime state 下账号失败记录必须使用哈希观测标记并通过 merge 写入共享探针状态')
  assert.match(functionBody(accountSideEffectsSource, 'runDistributedGatewayAccountRecoveryProbe'), /loadDistributedRecoveryProbeStateWithAccount[\s\S]*runSingleGatewayAccountPrecheck[\s\S]*currentDistributedRecoveryProbeState/, 'Redis runtime state 下恢复探针必须重载账号凭据、使用账号测试健康探针并校验 generation')
  assert.match(functionBody(accountSideEffectsSource, 'runDistributedGatewayAccountRecoveryProbe'), /if \(!persisted\) \{[\s\S]*distributedRecoveryProbeStore\.delete\(runtimeKey\)[\s\S]*return[\s\S]*\}/, 'Redis 探针 due 索引命中已过期状态时必须清理 due 成员，避免旧 runtimeKey 反复占用 sweep batch')
  assert.match(functionBody(accountSideEffectsSource, 'loadDistributedRecoveryProbeStateWithAccount'), /type: 'find_openai_account_for_group'[\s\S]*ignoreAvailability: true/, 'Redis 探针执行前必须通过 DB service 重载账号凭据，Redis 状态不得保存凭据')
  assert.doesNotMatch(functionBody(accountSideEffectsSource, 'recordDistributedGatewayAccountFailureForPrecheck'), /\bapiKey:\s*|apiKeys|refreshToken|credentials|accessToken|secretKey/, 'Redis 探针状态不得写入账号凭据字段')
  assert.match(functionBody(accountSideEffectsSource, 'filterGatewayAccountRuntimeSuppressionsAsync'), /runtimeConfig\.runtimeStateDriver !== 'redis'[\s\S]*filterLocallySuppressedGatewayAccounts[\s\S]*filterDistributedRecoveryProbeSuppressions/, 'Redis runtime state 下调度过滤必须读取共享探针运行态')
  assert.match(functionBody(accountSideEffectsSource, 'filterDistributedRecoveryProbeSuppressions'), /cachedDistributedRecoveryProbeSuppressionState[\s\S]*loadDistributedRecoveryProbeSuppressionState/, 'Redis runtime state 下调度过滤必须使用近端短 TTL 缓存，避免每次请求按候选账号数量打 Redis')
  assert.match(accountSideEffectsSource, /distributedRecoveryProbeSuppressionCacheTtlMs\s*=\s*1000[\s\S]*distributedRecoveryProbeSuppressionNegativeCacheTtlMs\s*=\s*500/, 'Redis 探针调度过滤近端缓存必须保持短 TTL，只允许短暂不一致')

  const sessionAffinitySource = source('modules/gateway/runtime/session-affinity.service.ts')
  assert.match(functionBody(sessionAffinitySource, 'canUseProcessLocalSessionAffinity'), /runtimeConfig\.cacheDriver !== 'redis'[\s\S]*clearSessionAffinityIndexes\(\)[\s\S]*return false/, 'Redis cache driver 下 session affinity 本机索引必须禁用并清空')
  assert.match(functionBody(sessionAffinitySource, 'rememberOpenAIAccountForSessionAsync'), /getRedisSessionAffinityRecord[\s\S]*setRedisSessionAffinityBinding[\s\S]*if \(written\)/, 'Redis cache driver 下会话亲和成功绑定必须写入共享 Redis 绑定并处理 CAS 冲突')
  assert.match(sessionAffinitySource, /redisSetSessionAffinityBindingScript[\s\S]*redis\.call\('GET', KEYS\[1\]\)[\s\S]*redis\.call\('SET', KEYS\[1\], new_value, 'PX', binding_ttl_ms\)[\s\S]*redis\.call\('ZADD', KEYS\[i\], expires_at, session_key\)/, 'Redis cache driver 下会话亲和 binding 与反查索引必须通过 Lua 原子写入')
  assert.match(sessionAffinitySource, /redisDeleteSessionAffinityBindingScript[\s\S]*if current ~= ARGV\[1\][\s\S]*redis\.call\('DEL', KEYS\[1\]\)[\s\S]*redis\.call\('ZREM', KEYS\[i\], ARGV\[2\]\)/, 'Redis cache driver 下会话亲和删除必须通过 expected raw value 原子清理 binding 与索引')
  assert.doesNotMatch(functionBody(sessionAffinitySource, 'forgetOpenAIAccountForSession'), /void forgetOpenAIAccountForSessionAsync/, 'Redis cache driver 下生产路径不能通过同步会话亲和清理入口 fire-and-forget')
  assert.match(sessionAffinitySource, /async function migrateOpenAIAccountSessionAffinityAsync[\s\S]*migrateRedisOpenAIAccountSessionAffinity/, 'Redis cache driver 下手动迁移会话亲和必须走 Redis 反查索引')
  assert.match(functionBody(sessionAffinitySource, 'orderOpenAIAccountsBySessionAffinityAsync'), /getRedisSessionAffinityBindingForOrdering[\s\S]*orderOpenAIPersonalAccountsBySessionBinding/, 'Redis cache driver 下调度排序必须读取共享 Redis 会话绑定')
  assert.match(functionBody(source('modules/db-service/db-service-ipc.ts'), 'migrateOpenAIAccountTrafficRuntimeLocal'), /await sessionAffinity\.migrateOpenAIAccountSessionAffinityAsync[\s\S]*await sessionAffinity\.rememberOpenAIAccountTrafficMigrationPreferenceAsync\([\s\S]*throwOnRedisError:\s*true/, 'DB service 运行态流量迁移必须等待 Redis 会话迁移和偏向写入完成')
  assert.match(functionBody(sessionAffinitySource, 'accountCurrentConcurrency'), /runtimeCurrentConcurrency \?\? account\.currentConcurrency/, '高并发异步排序必须优先使用 Redis runtime 并发值，不得被账号对象旧 currentConcurrency 覆盖')
  assert.match(functionBody(sessionAffinitySource, 'orderOpenAIHighConcurrencyAccountsAsync'), /trafficMigrationTargetAccountId[\s\S]*await orderOpenAIHighConcurrencyHardBusyLastAsync\(accounts\)/, '高并发异步排序的流量迁移 hard-busy 分支必须读取 Redis 并发状态')

  assert.match(functionBody(groupLoaderSource, 'loadGroupAccountStatsByGroupIds'), /assertSyncGroupReadLoaderAllowed/, 'Redis cache driver 下分组统计同步 loader 必须 fail-fast')

  assert.match(functionBody(repositoryLookupSource, 'loadSystemAccountsByIds'), /高性能模式禁止同步直读系统账户 lookup/, 'Redis cache driver 下系统账户同步 lookup 必须 fail-fast')

  const usageRecordsSource = source('modules/gateway/usage/records.ts')
  assert.match(functionBody(usageRecordsSource, 'canUseSynchronousCatalogPricingInGatewayRequest'), /runtimeConfig\.cacheDriver !== 'redis'/, '高性能网关请求链路不能同步读取模型目录计算 usage 成本')
  assert.match(functionBody(usageRecordsSource, 'recordFailedUpstreamAttempt'), /await enqueueUsageRecord/, '高性能失败使用记录必须等待 Redis Stream 接收')
  assert.match(functionBody(usageRecordsSource, 'recordCompletedUpstreamAttempt'), /await enqueueUsageRecord/, '高性能成功使用记录必须等待 Redis Stream 接收')
  assert.match(functionBody(usageRecordsSource, 'recordHybridScoringAttempt'), /await enqueueUsageRecord/, '高性能混合评分使用记录必须等待 Redis Stream 接收')
  assert.match(functionBody(usageRecordsSource, 'recordGatewayFailure'), /await enqueueUsageRecord/, '高性能网关失败使用记录必须等待 Redis Stream 接收')

  const usageRecordQueueSource = source('modules/gateway/usage/record-queue.service.ts')
  assert.match(functionBody(usageRecordQueueSource, 'enqueueUsageRecord'), /shouldEnqueueUsageRecordToRedisStream\(\)[\s\S]*await freezeUsageRecordPricingFactsAsync\(queuedInput\)[\s\S]*enqueueUsageRecordToRedisStream\(frozenInput\)/, '高性能使用记录必须在 Redis Stream 入队前异步固化请求时计价事实')
  assert.doesNotMatch(functionBody(usageRecordQueueSource, 'enqueueUsageRecord'), /buildCatalogCostBreakdown\(/, '高性能使用记录请求路径不得同步扫描目录生成计价快照')

  const usageRecordsRepositorySource = source('storage/usage-records.repository.ts')
  assert.match(usageRecordsRepositorySource, /listProviderModelCatalogAsync/, 'PostgreSQL 使用记录写入必须使用异步模型目录快照补算成本')
  assert.match(functionBody(usageRecordsRepositorySource, 'createUsageRecordsBatchPostgres'), /await enrichUsageRecordPricingAsync\(inputs\)[\s\S]*buildUsageRecordBatchWritePlan\(enrichedInputs/, 'PostgreSQL 使用记录写入必须先异步补齐 pricingModel/costUsd，再生成写入计划')
  const pricingEnrichmentBody = functionBody(usageRecordsRepositorySource, 'enrichUsageRecordPricingAsync')
  assert.match(pricingEnrichmentBody, /catalogCache = new Map<[\s\S]*listProviderModelCatalogAsync\(input\)/, '批量计价应按供应商和账户作用域共享一次模型目录快照')
  assert.doesNotMatch(usageRecordsRepositorySource, /estimateCatalog(?:CacheRead|CacheWrite|Cost)UsdAsync/, '使用记录补算不得为每个成本字段重复读取模型目录')
  assert.match(functionBody(usageRecordsRepositorySource, 'enrichSingleUsageRecordPricingAsync'), /input\.pricingSnapshot !== undefined\) return input/, 'Redis consumer 不得用未来目录覆盖已固化计价快照')

  const auditCaptureSource = source('modules/gateway/audit/capture.service.ts')
  assert.match(functionBody(auditCaptureSource, 'auditModelAccounting'), /runtimeConfig\.cacheDriver !== 'redis'[\s\S]*resolveCatalogPricingModel/, '高性能审计尝试记录不能同步读取模型目录解析 pricingModel')
  const auditLogsRepositorySource = source('storage/audit-logs.repository.ts')
  assert.match(auditLogsRepositorySource, /resolveCatalogPricingModelAsync/, 'PostgreSQL 审计日志写入前必须在异步落库链路补齐 pricingModel')
  assert.match(functionBody(auditLogsRepositorySource, 'createAuditLogsBatchPostgres'), /await enrichPostgresAuditLogPricing\(inputs\)[\s\S]*for \(const input of enrichedInputs\)/, 'PostgreSQL 审计日志必须先异步补齐 pricingModel，再生成写入计划')
}

function assertPostgresAsyncRuntimeFactReads(): void {
  const asyncConcurrencyReaders = [
    {
      file: 'storage/account-health-check.repository.ts',
      functions: ['healthCheckAccountSummariesAsync']
    },
    {
      file: 'storage/account-cooldown-retest.repository.ts',
      functions: ['cooldownRetestAccountSummariesAsync']
    },
    {
      file: 'storage/account-summary.repository.ts',
      functions: ['authorizedAccountSummaryFromRowAsync', 'ownerAccountSummariesFromRowsAsync']
    },
    {
      file: 'storage/group-summary.repository.ts',
      functions: ['buildGroupSummariesInClientAsync']
    }
  ]
  for (const item of asyncConcurrencyReaders) {
    const fileSource = source(item.file)
    for (const functionName of item.functions) {
      const body = functionBody(fileSource, functionName)
      assert.match(
        body,
        /loadAccountCurrentConcurrencyByIdsAsync/,
        `${item.file}:${functionName} 在 PG+Redis 下必须读取 Redis runtime state 并发事实源`
      )
      assert.doesNotMatch(
        body,
        /(?<!Async)\bloadAccountCurrentConcurrencyByIds\(/,
        `${item.file}:${functionName} 在 PG+Redis 下禁止读取同步本机并发 Map`
      )
    }
  }
}

function assertNoPerformanceLocalFactQueues(): void {
  const usageRecordQueueSource = source('modules/gateway/usage/record-queue.service.ts')
  assert.match(
    functionBody(usageRecordQueueSource, 'enqueueUsageRecord'),
    /shouldEnqueueUsageRecordToRedisStream\(\)[\s\S]*await enqueueUsageRecordToRedisStream/,
    'redis_stream driver 下使用记录必须等待 Redis Stream XADD 成功'
  )
  assert.doesNotMatch(
    functionBody(usageRecordQueueSource, 'enqueueUsageRecord'),
    /void enqueueUsageRecordToRedisStream/,
    'redis_stream driver 下使用记录禁止 fire-and-forget 写 Redis Stream'
  )
  assert.match(
    functionBody(usageRecordQueueSource, 'enqueueUsageRecordLocal'),
    /assertLocalUsageRecordWriteAllowed/,
    'redis_stream driver 下使用记录禁止回退本地队列'
  )
  assert.match(
    functionBody(usageRecordQueueSource, 'getUsageRecordRedisStreamRuntime'),
    /usageRecordRedisStreamQueue\(\)\.inspectRuntime\(\)/,
    '使用记录 Redis Stream 必须暴露 lag/pending 运行态'
  )
  assert.match(
    functionBody(usageRecordQueueSource, 'getUsageRecordRedisStreamOldestCreatedAt'),
    /usageRecordRedisStreamQueue\(\)\.inspectBacklog\(512\)[\s\S]*pendingTruncated[\s\S]*normalizeUsageRecordCreatedAtForBacklog\(message\.payload\.createdAt\)/,
    '使用记录 Redis Stream 必须扫描 backlog 业务 createdAt，并在扫描截断时使用保守安全边界'
  )
  const fixedResponsesSource = source('modules/gateway/response/fixed-responses.ts')
  assert.match(
    functionBody(fixedResponsesSource, 'sendModelsGatewayResponse'),
    /await enqueueUsageRecord/,
    '模型列表固定响应的使用记录必须等待 Redis Stream XADD 成功'
  )

  const backgroundJobsSource = source('modules/background/background-jobs.ts')
  assert.match(
    functionBody(backgroundJobsSource, 'usageStatsAggregationSafety'),
    /oldestPendingUsageRecordCreatedAt\(status\)[\s\S]*await oldestRedisStreamUsageRecordCreatedAtForStatsAggregation\(\)[\s\S]*usageStatsSafeCreatedBeforeForPendingBacklog/,
    '统计聚合推进 safeCreatedBefore 前必须用 Redis Stream backlog 最早 createdAt 收窄安全上界'
  )
  assert.match(
    functionBody(backgroundJobsSource, 'usageStatsSafeCreatedBeforeForPendingBacklog'),
    /normalizeIsoTime\(oldestPendingCreatedAt\)[\s\S]*normalizedOldestPendingCreatedAt > defaultSafeCreatedBefore[\s\S]*oldestPendingTime - 1/,
    'redis_stream driver 下统计聚合不能因 pending/lag 非零整轮停止，必须把安全上界收窄到最早 backlog 前'
  )

  const accountSideEffectsSource = source('modules/gateway/runtime/account-side-effects.service.ts')
  assert.doesNotMatch(
    functionBody(accountSideEffectsSource, 'enqueueGatewayAccountErrorHandlingSideEffect'),
    /await executeAccountSideEffect\(operation\)/,
    'Redis runtime state 下账号状态副作用不能在请求链路等待 DB service 写入'
  )
  assert.match(
    accountSideEffectsSource,
    /export async function flushGatewayAccountSideEffects\(\): Promise<void> \{[\s\S]*await drainSideEffectQueue\(\)/,
    'Redis runtime state 下账号状态副作用仍必须支持异步队列 drain，避免请求直接等待 PG 写'
  )

  const clientIpPolicySource = source('modules/gateway/runtime/client-ip-policy-cache.service.ts')
  assert.match(
    functionBody(clientIpPolicySource, 'recordClientIpPolicyHitAsync'),
    /runtimeConfig\.cacheDriver === 'redis' \|\| runtimeConfig\.runtimeMode === 'performance'[\s\S]*await writeClientIpPolicyHits\(\[hit\]\)[\s\S]*pendingPolicyHits\.get/,
    '高性能 IP 封禁命中必须直接投递 stats writer/PG，不能进入本机 pendingPolicyHits 缓冲'
  )

  const accountApiKeyEffectsSource = source('modules/gateway/runtime/account-api-key-effects.service.ts')
  assert.match(
    functionBody(accountApiKeyEffectsSource, 'recordGatewayAccountApiKeyFailure'),
    /runtimeConfig\.runtimeStateDriver === 'redis'[\s\S]*void requestGatewayDbService[\s\S]*return[\s\S]*await requestGatewayDbService/,
    'Redis runtime state 下账户内 API Key 失败状态必须异步投递，不能在请求链路等待 DB service 写入'
  )
  assert.doesNotMatch(
    source('modules/gateway/runtime/account-effects.ts'),
    /void applyAccountErrorHandlingWithCacheInvalidation|void recordGatewayAccountApiKeyFailure/,
    '流式失败账号状态副作用禁止 fire-and-forget'
  )
}

function assertRedisRuntimeQueuesAndLimits(): void {
  const highConcurrencyQueueSource = source('modules/gateway/runtime/high-concurrency-queue.service.ts')
  assert.match(functionBody(highConcurrencyQueueSource, 'waitForHighConcurrencyGroupCapacity'), /runtimeConfig\.runtimeStateDriver === 'redis'[\s\S]*waitForRedisHighConcurrencyGroupCapacity/, '高性能分组排队必须进入 Redis 队列分支')
  assert.match(functionBody(highConcurrencyQueueSource, 'waitForRedisHighConcurrencyGroupCapacity'), /enqueueRedisHighConcurrencyQueueItem[\s\S]*redisHighConcurrencyQueuePosition[\s\S]*removeRedisHighConcurrencyQueueItem/, '高性能分组排队必须把队列项写入 Redis 并按 Redis 队首放行')
  assert.match(highConcurrencyQueueSource, /const redisHighConcurrencyQueueEnqueueScript = `[\s\S]*ZCARD[\s\S]*per_api_key_queue_limit[\s\S]*ZADD/, '高性能分组队列必须用 Redis ZSET 维护全局队列和 per-api-key 队列上限')
  assert.match(highConcurrencyQueueSource, /export function highConcurrencyGroupQueueSnapshot[\s\S]*runtimeConfig\.runtimeStateDriver === 'redis'\) return \[\][\s\S]*queues\.values/, 'Redis runtime state 下分组队列快照不得返回本机队列')

  const clientIpConcurrencySource = source('modules/gateway/runtime/client-ip-concurrency.service.ts')
  assert.match(functionBody(clientIpConcurrencySource, 'acquireHighConcurrencyClientIpSlot'), /runtimeConfig\.runtimeStateDriver === 'redis'[\s\S]*acquireRedisClientIpSlot/, '高性能 Client-IP 并发必须进入 Redis 分支')
  assert.match(functionBody(clientIpConcurrencySource, 'acquireRedisClientIpSlot'), /enqueueRedisClientIpQueueItem[\s\S]*redisClientIpQueuePosition[\s\S]*removeRedisClientIpQueueItem/, '高性能 Client-IP 溢出队列必须写入 Redis 队列')
  assert.match(clientIpConcurrencySource, /const redisClientIpQueueEnqueueScript = `[\s\S]*queue_limit[\s\S]*ZADD/, '高性能 Client-IP 队列必须用 Redis ZSET 维护队列上限')
  assert.match(clientIpConcurrencySource, /const redisAcquireClientIpConcurrencyScript = `[\s\S]*ZREMRANGEBYSCORE[\s\S]*ZADD[\s\S]*slot_token/, '高性能 Client-IP 并发必须使用 Redis token lease，禁止简单计数器误释放新请求')
  assert.match(clientIpConcurrencySource, /const redisClientIpConcurrencyRenewIntervalMs[\s\S]*redisClientIpConcurrencyTtlMs/, 'Redis Client-IP 并发活跃槽必须按 TTL 定期续租，避免长流式请求租约过期')
  assert.match(functionBody(clientIpConcurrencySource, 'redisAcquiredDecision'), /startRedisClientIpSlotRenewal[\s\S]*stopRenewal\(\)[\s\S]*releaseRedisClientIpSlotWithRetry/, 'Redis Client-IP 并发槽释放前必须停止续租并归还 token')
  const clientIpSlotRenewalBody = functionBody(clientIpConcurrencySource, 'startRedisClientIpSlotRenewal')
  assert.match(clientIpSlotRenewalBody, /setInterval[\s\S]*renewRedisClientIpSlot/, 'Redis Client-IP 并发活跃槽必须由当前进程定期续租')
  assert.match(clientIpSlotRenewalBody, /clearInterval/, 'Redis Client-IP 并发活跃槽续租必须可停止')
  assert.match(clientIpConcurrencySource, /const redisRenewClientIpConcurrencyScript = `[\s\S]*ZSCORE[\s\S]*ZADD[\s\S]*PEXPIRE/, 'Redis Client-IP 并发续租只能刷新仍存在的 token，禁止复活已释放槽位')
  assert.match(clientIpConcurrencySource, /const redisRenewClientIpConcurrencyScript = `[\s\S]*current_score <= now_ms[\s\S]*ZREM[\s\S]*return \{0\}/, 'Redis Client-IP 并发续租发现已过期 token 必须删除并返回失败，禁止复活过期租约')
  assert.doesNotMatch(clientIpConcurrencySource, /redis\.call\('INCR'|redis\.call\('DECR'/, 'Redis Client-IP 并发不能使用简单 INCR/DECR 计数器')
  assert.match(clientIpConcurrencySource, /async function tryAcquireRedisClientIpSlot[\s\S]*requireEmptyQueue[\s\S]*redisAcquireClientIpConcurrencyScript/, '高性能 Client-IP 新请求不能绕过 Redis 队列插队抢槽')
  assert.match(functionBody(clientIpConcurrencySource, 'releaseRedisClientIpSlotWithRetry'), /redis_client_ip_concurrency_release_failed/, 'Redis Client-IP 并发释放失败不能吞错，必须重试并记录错误')
  assert.match(clientIpConcurrencySource, /export function clientIpConcurrencySnapshot[\s\S]*runtimeConfig\.runtimeStateDriver === 'redis'\) return \[\][\s\S]*states\.values/, 'Redis runtime state 下 Client-IP 并发快照不得返回本机状态')
}

function assertOAuthAndRateLimitRedisBoundaries(): void {
  const oauthSource = source('modules/openai-oauth/openai-oauth.service.ts')
  assert.match(functionBody(oauthSource, 'generateOpenAIAuthURL'), /oauthSessionStore\(\)\.setJson[\s\S]*sessionTtlMs/, 'OAuth 授权会话必须写 RuntimeStateStore，Redis 模式共享且带 TTL')
  assert.match(functionBody(oauthSource, 'exchangeOpenAIAuthCode'), /oauthSessionStore\(\)\.getDeleteJson/, 'OAuth 授权会话兑换必须用 GETDEL 防重放，不能读本机 Map')
  assert.doesNotMatch(oauthSource, /const sessions = new Map/, 'OAuth 授权会话不能使用进程内 Map')

  const oauthRefreshSource = source('modules/openai-oauth/openai-oauth-access-token-refresh.service.ts')
  assert.match(functionBody(oauthRefreshSource, 'recordRefreshFailure'), /runtimeConfig\.runtimeStateDriver === 'redis'[\s\S]*redisRecordRefreshFailureScript/, 'OAuth 刷新失败计数和 backoff 在 Redis runtime state 下必须使用 Redis 原子状态')
  assert.match(functionBody(oauthRefreshSource, 'runWithAccountRefreshLock'), /runtimeConfig\.runtimeStateDriver === 'redis'[\s\S]*runWithRedisAccountRefreshLock/, 'OAuth 刷新锁在 Redis runtime state 下必须使用分布式锁')
  assert.match(functionBody(oauthRefreshSource, 'runWithRedisAccountRefreshLock'), /acquireLock[\s\S]*releaseLock/, 'OAuth Redis 刷新锁必须 acquire/release runtime state lock')

  const penaltyRateLimitSource = source('modules/rate-limit/penalty-window-rate-limit.ts')
  assert.match(functionBody(penaltyRateLimitSource, 'consumePenaltyWindowRateLimit'), /assertPenaltyWindowMemoryStoreAllowed/, 'Redis runtime state 下 penalty window 同步限流入口必须 fail-fast')
  assert.match(functionBody(penaltyRateLimitSource, 'consumePenaltyWindowRateLimitAsync'), /runtimeConfig\.runtimeStateDriver !== 'redis'[\s\S]*consumePenaltyWindowRateLimit[\s\S]*consumeRedisPenaltyWindowRateLimit/, 'Penalty window async 限流在 Redis runtime state 下必须使用 Redis')
  assert.match(functionBody(penaltyRateLimitSource, 'consumeRedisPenaltyWindowRateLimit'), /keys: rules\.map[\s\S]*rules\.flatMap/, 'Penalty window Redis 限流必须一次 Lua 覆盖全部规则，不能逐条规则提前消费')
  assert.match(penaltyRateLimitSource, /local blocked_index = 0[\s\S]*for index = 1, rule_count do[\s\S]*blocked_index == 0[\s\S]*if blocked_index > 0 then[\s\S]*return \{0, blocked_retry_ms, blocked_index\}[\s\S]*for index = 1, rule_count do[\s\S]*'count', tostring\(counts\[index\] \+ 1\)/, 'Penalty window Redis Lua 必须检查并更新全部 blocked 规则，且只有全部通过后才递增计数')
  assert.match(penaltyRateLimitSource, /const redisPenaltyWindowRateLimitScript = `[\s\S]*HSET[\s\S]*PEXPIRE/, 'Penalty window Redis 限流必须使用 Redis Lua 维护全局窗口和惩罚')

  const publicModelsRateLimitSource = source('modules/gateway/runtime/public-models-rate-limit.service.ts')
  assert.match(functionBody(publicModelsRateLimitSource, 'consumePublicModelsRateLimit'), /await consumePenaltyWindowRateLimitAsync/, '公开模型列表限流必须走 Redis-aware async penalty limiter')
  const preflightSource = source('modules/gateway/request/preflight.ts')
  assert.match(functionBody(preflightSource, 'handleGatewayModelsRequestBeforeRequiredAuth'), /await consumePublicModelsRateLimit/, '无鉴权模型列表请求必须 await Redis-aware 限流')

  const externalSourceAuthSource = source('modules/external-integrations/external-source-auth.middleware.ts')
  assert.match(externalSourceAuthSource, /async function consumeExternalSourceRateLimit[\s\S]*await consumePenaltyWindowRateLimitAsync/, '外部来源限流必须走 Redis-aware async penalty limiter')

  const systemApiRateLimitSource = source('modules/system-api/system-api-rate-limit.middleware.ts')
  assert.match(functionBody(systemApiRateLimitSource, 'checkRateLimit'), /runtimeConfig\.runtimeStateDriver === 'redis'[\s\S]*checkRedisRateLimit/, '后台系统 API 限流在 Redis runtime state 下必须使用 Redis')
  assert.match(functionBody(systemApiRateLimitSource, 'checkRedisRateLimit'), /redisFixedWindowRateLimitScript/, '后台系统 API Redis 限流必须使用 Redis Lua 原子检查多个 bucket')
}

function collectCacheHits(files: string[], pattern: RegExp): CacheHit[] {
  const hits: CacheHit[] = []
  for (const filePath of files) {
    const file = slash(relative(srcRoot, filePath))
    const lines = readFileSync(filePath, 'utf8').split(/\r?\n/)
    for (let index = 0; index < lines.length; index += 1) {
      pattern.lastIndex = 0
      const match = pattern.exec(lines[index])
      if (!match) continue
      hits.push({ file, line: index + 1, variableName: match[1] })
    }
  }
  return hits
}

function listSourceFiles(root: string): string[] {
  const output: string[] = []
  const walk = (directory: string): void => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const fullPath = resolve(directory, entry.name)
      if (entry.isDirectory()) {
        if (entry.name === 'dist' || entry.name === 'node_modules') continue
        walk(fullPath)
        continue
      }
      if (entry.isFile() && entry.name.endsWith('.ts')) output.push(fullPath)
    }
  }
  walk(root)
  return output.sort()
}

function source(path: string): string {
  return readFileSync(resolve(srcRoot, path), 'utf8')
}

function functionBody(sourceText: string, functionName: string): string {
  const start = sourceText.indexOf(`function ${functionName}`)
  assert(start >= 0, `缺少函数 ${functionName}`)
  const parametersStart = sourceText.indexOf('(', start)
  assert(parametersStart >= 0, `函数 ${functionName} 缺少参数列表`)
  let parameterDepth = 0
  let parametersEnd = -1
  for (let index = parametersStart; index < sourceText.length; index += 1) {
    const char = sourceText[index]
    if (char === '(') parameterDepth += 1
    if (char === ')') {
      parameterDepth -= 1
      if (parameterDepth === 0) {
        parametersEnd = index
        break
      }
    }
  }
  assert(parametersEnd >= 0, `函数 ${functionName} 参数列表未闭合`)
  const openBrace = sourceText.indexOf('{', parametersEnd)
  assert(openBrace >= 0, `函数 ${functionName} 缺少函数体`)
  let depth = 0
  for (let index = openBrace; index < sourceText.length; index += 1) {
    const char = sourceText[index]
    if (char === '{') depth += 1
    if (char === '}') {
      depth -= 1
      if (depth === 0) {
        return sourceText.slice(openBrace, index + 1)
      }
    }
  }
  throw new Error(`函数 ${functionName} 函数体解析失败`)
}

function templateLiteralBody(sourceText: string, constName: string): string {
  const pattern = new RegExp(`const ${constName} = \`([\\s\\S]*?)\\r?\\n\``)
  const match = pattern.exec(sourceText)
  assert(match, `缺少模板字符串常量 ${constName}`)
  return match[1]
}

function slash(path: string): string {
  return path.replace(/\\/g, '/')
}
