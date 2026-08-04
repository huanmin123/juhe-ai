import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'

if (process.env.JUHE_AI_RUNTIME_CONFIG_ENV_OVERRIDE_CHILD === '1') {
  const { runtimeConfig } = await import('../../config/runtime.js')
  const { usageRecordShardRoot } = await import('../../storage/usage-record-shards.js')

  assert.equal(runtimeConfig.port, 39123, '进程环境变量 JUHE_AI_PORT 应覆盖 backend/.env')
  assert.equal(runtimeConfig.host, '127.0.0.2', '进程环境变量 JUHE_AI_HOST 应覆盖 backend/.env')
  assert.equal(runtimeConfig.databasePath.endsWith('env-override-business.sqlite3'), true, '进程环境变量 JUHE_AI_DATABASE_PATH 应覆盖 backend/.env')
  assert.equal(runtimeConfig.datasetDatabasePath.endsWith('env-override-dataset.sqlite3'), true, '进程环境变量 JUHE_AI_DATASET_DATABASE_PATH 应覆盖 backend/.env')
  assert.equal(runtimeConfig.usageCatalogDatabasePath.endsWith('env-override-usage-catalog.sqlite3'), true, '进程环境变量 JUHE_AI_USAGE_CATALOG_DATABASE_PATH 应覆盖 backend/.env')
  assert.equal(runtimeConfig.statsDatabasePath.endsWith('env-override-stats.sqlite3'), true, '进程环境变量 JUHE_AI_STATS_DATABASE_PATH 应覆盖 backend/.env')
  assert.equal(runtimeConfig.usageShardRoot.endsWith('env-override-usage-shards'), true, '进程环境变量 JUHE_AI_USAGE_SHARD_ROOT 应覆盖 backend/.env')
  assert.equal(normalizePath(usageRecordShardRoot()).endsWith('env-override-usage-shards'), true, '显式 usage shard 根目录不应被数据集目录库默认规则覆盖')
  assert.equal(runtimeConfig.usageShardCount, 32, '进程环境变量 JUHE_AI_USAGE_SHARD_COUNT 应覆盖 backend/.env')
  assert.equal(runtimeConfig.log.consoleEnabled, false, '进程环境变量 JUHE_AI_LOG_CONSOLE_ENABLED 应覆盖 backend/.env')
  assert.equal(runtimeConfig.background.accountHealthCheckBatchSize, 100, '健康检测批次应支持进程环境变量覆盖')
  assert.equal(runtimeConfig.background.cooldownAccountRetestBatchSize, 100, '冷却复测批次应支持进程环境变量覆盖')
  assert.equal(runtimeConfig.concurrency.globalMax, 4321, '全局共享并发上限应支持进程环境变量覆盖')
  assert.equal(runtimeConfig.concurrency.globalLeaseDurationMs, 240000, '全局共享并发租约时长应支持进程环境变量覆盖')
  assert.equal(runtimeConfig.concurrency.globalAcquirePollMs, 25, '全局共享并发槽轮询间隔应支持进程环境变量覆盖')
  assert.equal(runtimeConfig.gateway.upstreamAgentMaxSockets, 3456, 'HTTP Agent 单源连接容量应支持进程环境变量覆盖')
  assert.equal(runtimeConfig.gateway.upstreamAgentMaxTotalSockets, 4567, 'HTTP Agent 总连接容量应支持进程环境变量覆盖')
  assert.equal(runtimeConfig.gateway.automaticProbeSweepBatchSize, 80, '自动恢复探针扫描批次应支持进程环境变量覆盖')
  assert.equal(runtimeConfig.gateway.automaticProbeSweepIntervalMs, 750, '自动恢复探针扫描周期应支持进程环境变量覆盖')
  assert.equal(runtimeConfig.gateway.accountCircuitRecoveryLeaseDurationMs, 240000, '熔断恢复租约应支持进程环境变量覆盖')
  assert.deepEqual(runtimeConfig.gateway.accountCircuitBackoffMs, [1000, 2000, 3000], '熔断退避阶梯应支持进程环境变量覆盖')
  assert.equal(runtimeConfig.background.proxyLatencyRefreshRunBudgetMs, 55000, '代理刷新时间预算应支持进程环境变量覆盖')
  assert.equal(runtimeConfig.background.auditHotRetentionCleanupBatchSize, 120, '审计热保留清理批次应支持进程环境变量覆盖')
  assert.equal(runtimeConfig.background.auditHotRetentionCleanupMaxBatches, 4, '审计热保留清理轮次应支持进程环境变量覆盖')
  assert.equal(runtimeConfig.background.auditHotRetentionCleanupMaxRunMs, 9000, '审计热保留清理时间预算应支持进程环境变量覆盖')
  assert.equal(runtimeConfig.runtimeMode, 'standalone', '默认运行模式应为 standalone')
  assert.equal(runtimeConfig.databaseDriver, 'sqlite', 'standalone 默认数据库 driver 应为 sqlite')
  assert.equal(runtimeConfig.cacheDriver, 'memory', 'standalone 默认缓存 driver 应为 memory')
  assert.equal(runtimeConfig.runtimeStateDriver, 'memory', 'standalone 默认运行态 driver 应为 memory')
  assert.equal(runtimeConfig.queueDriver, 'memory', 'standalone 默认队列 driver 应为 memory')
  assert.equal(runtimeConfig.systemApi.dbServiceMaxInFlight, 64, 'standalone 默认 System API DB service 在途上限应为 64')
  assert.equal('readOnly' in runtimeConfig.systemApi, false, '运行时配置不得保留临时发布只读开关')
  assert.equal(runtimeConfig.chat.retentionDays, 3, '聊天数据默认应保留 3 天')
  assert.equal(runtimeConfig.chat.maxConversationsPerUser, 50, '每用户默认最多应创建 50 个会话')
  assert.equal(runtimeConfig.chat.maxTurnsPerConversation, 50, '每个会话默认最多应接受 50 个用户轮次')
  assert.equal(runtimeConfig.chat.upstreamSseMaxEvents, 65_536, '聊天上游 SSE 默认事件上限应为 65536')
  assert.equal(runtimeConfig.chat.diagnosticToolEnabled, false, '内部诊断工具默认必须关闭')

  process.exit(0)
}

if (process.env.JUHE_AI_RUNTIME_CONFIG_USAGE_SHARD_DEFAULT_CHILD === '1') {
  const { usageRecordShardRoot } = await import('../../storage/usage-record-shards.js')

  assert.equal(normalizePath(usageRecordShardRoot()).endsWith('runtime-config-usage-catalog-dir/usage-shards'), true, '未配置 usage shard 根目录时应跟随使用记录目录库生成同级 usage-shards')

  process.exit(0)
}

if (process.env.JUHE_AI_RUNTIME_CONFIG_GATEWAY_DEFAULT_CHILD === '1') {
  const { runtimeConfig } = await import('../../config/runtime.js')

  assert.equal(runtimeConfig.concurrency.globalMax, 5_000, '禁用基础 env 后全局共享并发默认必须为 5000')
  assert.equal(runtimeConfig.concurrency.globalLeaseDurationMs, 300_000, '禁用基础 env 后全局共享并发租约默认必须为 5 分钟')
  assert.equal(runtimeConfig.concurrency.globalAcquirePollMs, 50, '禁用基础 env 后全局共享并发槽轮询默认必须为 50ms')
  assert.equal(runtimeConfig.gateway.upstreamAgentMaxSockets, 5_000, '禁用基础 env 后 HTTP Agent 单源连接容量必须跟随全局共享并发')
  assert.equal(runtimeConfig.gateway.upstreamAgentMaxTotalSockets, 5_000, '禁用基础 env 后 HTTP Agent 总连接容量必须跟随全局共享并发')
  assert.equal(runtimeConfig.gateway.usageFinalizationMaxItems, 2048, '禁用基础 env 后网关失败用量收尾默认队列容量必须为 2048')
  assert.equal(runtimeConfig.concurrency.globalMax, 5_000, '禁用基础 env 后网关失败用量收尾必须使用全局共享并发')

  process.exit(0)
}

if (process.env.JUHE_AI_RUNTIME_CONFIG_PERFORMANCE_CHILD === '1') {
  const { runtimeConfig } = await import('../../config/runtime.js')

  assert.equal(runtimeConfig.runtimeMode, 'performance', '高性能模式应读取为 performance')
  assert.equal(runtimeConfig.databaseDriver, 'postgres', '高性能模式数据库 driver 应为 postgres')
  assert.equal(runtimeConfig.cacheDriver, 'redis', '高性能模式缓存 driver 应为 redis')
  assert.equal(runtimeConfig.runtimeStateDriver, 'redis', '高性能模式运行态 driver 应为 redis')
  assert.equal(runtimeConfig.queueDriver, 'redis_stream', '高性能模式队列 driver 应为 redis_stream')
  assert.equal(runtimeConfig.postgres.url, 'postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai', 'PostgreSQL URL 应正确读取')
  assert.equal(runtimeConfig.redis.cacheUrl, 'redis://:cache-secret@127.0.0.1:6379/0', 'Redis cache URL 应正确读取')
  assert.equal(runtimeConfig.redis.stateUrl, 'redis://:state-secret@127.0.0.1:6380/0', 'Redis state URL 应正确读取')
  assert.equal(runtimeConfig.redis.queueUrl, 'redis://:queue-secret@127.0.0.1:6381/0', 'Redis queue URL 应正确读取')
  assert.equal(runtimeConfig.redis.namespace, 'runtime-test', 'Redis namespace 应正确读取')
  assert.equal(runtimeConfig.postgres.poolMax, 25, 'PostgreSQL pool max 应正确读取')
  assert.equal(runtimeConfig.postgres.writeMaxConcurrency, 100, 'PostgreSQL 写队列并发应正确读取')
  assert.equal(runtimeConfig.postgres.writeQueueMaxItems, 60000, 'PostgreSQL 写队列容量应正确读取')
  assert.equal(runtimeConfig.postgres.statementTimeoutMs, 45000, 'PostgreSQL statement timeout 应正确读取')
  assert.equal(runtimeConfig.postgres.lockTimeoutMs, 3000, 'PostgreSQL lock timeout 应正确读取')
  assert.equal(runtimeConfig.postgres.idleInTransactionSessionTimeoutMs, 55000, 'PostgreSQL idle transaction timeout 应正确读取')
  assert.equal(runtimeConfig.systemApi.dbServiceMaxInFlight, 321, 'System API DB service 在途上限应正确读取')
  assert.equal(runtimeConfig.gateway.usageFinalizationMaxItems, 10000, '网关失败用量收尾队列容量应正确读取')
  assert.equal(runtimeConfig.concurrency.globalMax, 4321, '网关失败用量收尾必须使用全局共享并发')
  assert.equal('readOnly' in runtimeConfig.systemApi, false, '显式历史开关不得恢复临时发布拦截模式')
  assert.equal(runtimeConfig.queue.redisStreamReadCount, 500, 'Redis Stream 批量读取数量应正确读取')
  assert.deepEqual(runtimeConfig.chat, {
    retentionDays: 9,
    maxConversationsPerUser: 60,
    maxTurnsPerConversation: 70,
    upstreamSseMaxEvents: 131_072,
    diagnosticToolEnabled: true
  }, '聊天保留、会话上限和轮次上限应支持进程环境变量覆盖')

  process.exit(0)
}

if (process.env.JUHE_AI_RUNTIME_CONFIG_CHAT_SSE_CHILD === '1') {
  const { runtimeConfig } = await import('../../config/runtime.js')
  assert.equal(runtimeConfig.chat.upstreamSseMaxEvents, Number(process.env.JUHE_AI_RUNTIME_CONFIG_CHAT_SSE_EXPECTED))
  process.exit(0)
}

if (process.env.JUHE_AI_RUNTIME_CONFIG_PERFORMANCE_DEFAULT_CHILD === '1') {
  const { runtimeConfig } = await import('../../config/runtime.js')

  assert.equal(runtimeConfig.runtimeMode, 'performance', '高性能模式应读取为 performance')
  assert.equal(runtimeConfig.systemApi.dbServiceMaxInFlight, 256, 'performance 默认 System API DB service 在途上限应为 256')
  assert.equal('readOnly' in runtimeConfig.systemApi, false, '正式环境不得保留临时发布拦截模式')
  assert.equal(runtimeConfig.postgres.statementTimeoutMs, 30000, 'performance 默认 PostgreSQL statement timeout 应为 30 秒')
  assert.equal(runtimeConfig.postgres.lockTimeoutMs, 2000, 'performance 默认 PostgreSQL lock timeout 应为 2 秒')
  assert.equal(runtimeConfig.postgres.idleInTransactionSessionTimeoutMs, 30000, 'performance 默认 PostgreSQL idle transaction timeout 应为 30 秒')
  assert.match(runtimeConfig.redis.namespace, /^env-[a-f0-9]{12}$/, '未显式配置 Redis namespace 时应由运行密钥派生稳定环境前缀')
  assert.equal('redisStreamMaxLen' in runtimeConfig.queue, false, 'Redis Stream 可靠队列不应暴露近似裁剪配置')

  process.exit(0)
}

if (process.env.JUHE_AI_RUNTIME_CONFIG_PERFORMANCE_CONTROL_CHILD === '1') {
  const { runtimeConfig } = await import('../../config/runtime.js')

  assert.equal(runtimeConfig.runtimeMode, 'performance', 'control 节点必须运行在高性能模式')
  assert.equal(runtimeConfig.performanceNodeRole, 'control', 'control 节点角色必须正确读取')
  assert.equal(runtimeConfig.processRole, 'db-service', '聊天控制面必须运行 DB service 进程')

  process.exit(0)
}

if (process.env.JUHE_AI_RUNTIME_CONFIG_ENV_FILE_CHILD === '1') {
  const { runtimeConfig } = await import('../../config/runtime.js')

  assert.equal(runtimeConfig.runtimeMode, 'performance', '专用 env 文件应能覆盖运行模式')
  assert.equal(runtimeConfig.databaseDriver, 'postgres', '专用 env 文件应能覆盖数据库 driver')
  assert.equal(runtimeConfig.cacheDriver, 'redis', '专用 env 文件应能覆盖缓存 driver')
  assert.equal(runtimeConfig.runtimeStateDriver, 'redis', '专用 env 文件应能覆盖运行态 driver')
  assert.equal(runtimeConfig.queueDriver, 'redis_stream', '专用 env 文件应能覆盖队列 driver')
  assert.equal(runtimeConfig.postgres.poolMax, 44, '专用 env 文件应能覆盖 PostgreSQL pool max')
  assert.equal(runtimeConfig.systemApi.dbServiceMaxInFlight, 333, '专用 env 文件应能覆盖 System API DB service 在途上限')

  process.exit(0)
}

if (process.env.JUHE_AI_RUNTIME_CONFIG_USAGE_CATALOG_DERIVED_CHILD === '1') {
  const { usageCatalogDatabasePath } = await import('../../storage/database.js')
  const { usageRecordShardRoot } = await import('../../storage/usage-record-shards.js')

  assert.equal(normalizePath(usageCatalogDatabasePath()).endsWith('runtime-config-derived-dataset/usage-catalog.sqlite3'), true, '未显式配置使用记录目录库时应跟随数据集目录库所在目录，避免回归脚本误写默认 data 目录')
  assert.equal(normalizePath(usageRecordShardRoot()).endsWith('runtime-config-derived-dataset/usage-shards'), true, '未显式配置 usage shard 根目录时应跟随推导后的使用记录目录库')

  process.exit(0)
}

const result = spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_ENV_OVERRIDE_CHILD: '1',
  JUHE_AI_RUNTIME_MODE: 'standalone',
  JUHE_AI_DATABASE_DRIVER: 'sqlite',
  JUHE_AI_CACHE_DRIVER: 'memory',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'memory',
  JUHE_AI_QUEUE_DRIVER: 'memory',
  JUHE_AI_PORT: '39123',
  JUHE_AI_HOST: '127.0.0.2',
  JUHE_AI_DATABASE_PATH: 'env-override-business.sqlite3',
  JUHE_AI_DATASET_DATABASE_PATH: 'env-override-dataset.sqlite3',
  JUHE_AI_USAGE_CATALOG_DATABASE_PATH: 'env-override-usage-catalog.sqlite3',
  JUHE_AI_STATS_DATABASE_PATH: 'env-override-stats.sqlite3',
  JUHE_AI_USAGE_SHARD_ROOT: 'env-override-usage-shards',
  JUHE_AI_USAGE_SHARD_COUNT: '32',
  JUHE_AI_SYSTEM_API_READ_ONLY: 'invalid',
  JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
  JUHE_AI_BACKGROUND_ACCOUNT_HEALTH_CHECK_BATCH_SIZE: '100',
  JUHE_AI_BACKGROUND_COOLDOWN_ACCOUNT_RETEST_BATCH_SIZE: '100',
  JUHE_AI_BACKGROUND_ACCOUNT_API_KEY_COOLDOWN_RETEST_BATCH_SIZE: '10',
  JUHE_AI_CONCURRENCY_GLOBAL_MAX: '4321',
  JUHE_AI_CONCURRENCY_GLOBAL_LEASE_DURATION_MS: '240000',
  JUHE_AI_CONCURRENCY_GLOBAL_ACQUIRE_POLL_MS: '25',
  JUHE_AI_GATEWAY_UPSTREAM_AGENT_MAX_SOCKETS: '3456',
  JUHE_AI_GATEWAY_UPSTREAM_AGENT_MAX_TOTAL_SOCKETS: '4567',
  JUHE_AI_GATEWAY_AUTOMATIC_PROBE_SWEEP_BATCH_SIZE: '80',
  JUHE_AI_GATEWAY_AUTOMATIC_PROBE_SWEEP_INTERVAL_MS: '750',
  JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_RECOVERY_LEASE_DURATION_MS: '240000',
  JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_BACKOFF_MS: '1000,2000,3000',
  JUHE_AI_BACKGROUND_PROXY_LATENCY_REFRESH_RUN_BUDGET_MS: '55000',
  JUHE_AI_BACKGROUND_AUDIT_HOT_RETENTION_CLEANUP_BATCH_SIZE: '120',
  JUHE_AI_BACKGROUND_AUDIT_HOT_RETENTION_CLEANUP_MAX_BATCHES: '4',
  JUHE_AI_BACKGROUND_AUDIT_HOT_RETENTION_CLEANUP_MAX_RUN_MS: '9000'
})

assertRegressionSuccess(result)

const gatewayDefaultResult = spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_GATEWAY_DEFAULT_CHILD: '1',
  JUHE_AI_DISABLE_BASE_ENV: 'true'
})

assertRegressionSuccess(gatewayDefaultResult)

const defaultUsageRootResult = spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_USAGE_SHARD_DEFAULT_CHILD: '1',
  JUHE_AI_RUNTIME_MODE: 'standalone',
  JUHE_AI_DATABASE_DRIVER: 'sqlite',
  JUHE_AI_CACHE_DRIVER: 'memory',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'memory',
  JUHE_AI_QUEUE_DRIVER: 'memory',
  JUHE_AI_DATASET_DATABASE_PATH: 'runtime-config-dataset-dir/dataset.sqlite3',
  JUHE_AI_USAGE_CATALOG_DATABASE_PATH: 'runtime-config-usage-catalog-dir/usage-catalog.sqlite3',
  JUHE_AI_USAGE_SHARD_ROOT: ''
})

assertRegressionSuccess(defaultUsageRootResult)

const derivedUsageCatalogResult = spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_USAGE_CATALOG_DERIVED_CHILD: '1',
  JUHE_AI_RUNTIME_MODE: 'standalone',
  JUHE_AI_DATABASE_DRIVER: 'sqlite',
  JUHE_AI_CACHE_DRIVER: 'memory',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'memory',
  JUHE_AI_QUEUE_DRIVER: 'memory',
  JUHE_AI_DATASET_DATABASE_PATH: 'runtime-config-derived-dataset/dataset.sqlite3',
  JUHE_AI_USAGE_CATALOG_DATABASE_PATH: '',
  JUHE_AI_USAGE_SHARD_ROOT: ''
})

assertRegressionSuccess(derivedUsageCatalogResult)

const performanceResult = spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_PERFORMANCE_CHILD: '1',
  JUHE_AI_RUNTIME_MODE: 'performance',
  JUHE_AI_DATABASE_DRIVER: 'postgres',
  JUHE_AI_CACHE_DRIVER: 'redis',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
  JUHE_AI_QUEUE_DRIVER: 'redis_stream',
  JUHE_AI_POSTGRES_URL: 'postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai',
  JUHE_AI_REDIS_CACHE_URL: 'redis://:cache-secret@127.0.0.1:6379/0',
  JUHE_AI_REDIS_STATE_URL: 'redis://:state-secret@127.0.0.1:6380/0',
  JUHE_AI_REDIS_QUEUE_URL: 'redis://:queue-secret@127.0.0.1:6381/0',
  JUHE_AI_REDIS_NAMESPACE: 'runtime-test',
  JUHE_AI_DB_POOL_MAX: '25',
  JUHE_AI_DB_WRITE_MAX_CONCURRENCY: '100',
  JUHE_AI_DB_WRITE_QUEUE_MAX_ITEMS: '60000',
  JUHE_AI_POSTGRES_STATEMENT_TIMEOUT_MS: '45000',
  JUHE_AI_POSTGRES_LOCK_TIMEOUT_MS: '3000',
  JUHE_AI_POSTGRES_IDLE_IN_TRANSACTION_TIMEOUT_MS: '55000',
  JUHE_AI_SYSTEM_API_DB_SERVICE_MAX_IN_FLIGHT: '321',
  JUHE_AI_GATEWAY_USAGE_FINALIZATION_MAX_ITEMS: '10000',
  JUHE_AI_CONCURRENCY_GLOBAL_MAX: '4321',
  JUHE_AI_REDIS_STREAM_READ_COUNT: '500',
  JUHE_AI_CHAT_RETENTION_DAYS: '9',
  JUHE_AI_CHAT_MAX_CONVERSATIONS_PER_USER: '60',
  JUHE_AI_CHAT_MAX_TURNS_PER_CONVERSATION: '70',
  JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS: '131072',
  JUHE_AI_CHAT_DIAGNOSTIC_TOOL_ENABLED: 'true'
})

assertRegressionSuccess(performanceResult)

const performanceDefaultResult = spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_PERFORMANCE_DEFAULT_CHILD: '1',
  JUHE_AI_RUNTIME_MODE: 'performance',
  JUHE_AI_DATABASE_DRIVER: 'postgres',
  JUHE_AI_CACHE_DRIVER: 'redis',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
  JUHE_AI_QUEUE_DRIVER: 'redis_stream',
  JUHE_AI_POSTGRES_URL: 'postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai',
  JUHE_AI_REDIS_CACHE_URL: 'redis://:cache-secret@127.0.0.1:6379/0',
  JUHE_AI_REDIS_STATE_URL: 'redis://:state-secret@127.0.0.1:6380/0',
  JUHE_AI_REDIS_QUEUE_URL: 'redis://:queue-secret@127.0.0.1:6381/0'
})

assertRegressionSuccess(performanceDefaultResult)

const performanceControlResult = spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_PERFORMANCE_CONTROL_CHILD: '1',
  JUHE_AI_RUNTIME_MODE: 'performance',
  JUHE_AI_PERFORMANCE_NODE_ROLE: 'control',
  JUHE_AI_PROCESS_ROLE: 'db-service',
  JUHE_AI_DATABASE_DRIVER: 'postgres',
  JUHE_AI_CACHE_DRIVER: 'redis',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
  JUHE_AI_QUEUE_DRIVER: 'redis_stream',
  JUHE_AI_POSTGRES_URL: 'postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai',
  JUHE_AI_REDIS_CACHE_URL: 'redis://:cache-secret@127.0.0.1:6379/0',
  JUHE_AI_REDIS_STATE_URL: 'redis://:state-secret@127.0.0.1:6380/0',
  JUHE_AI_REDIS_QUEUE_URL: 'redis://:queue-secret@127.0.0.1:6381/0'
})

assertRegressionSuccess(performanceControlResult)

const performanceHintDefaultResult = spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_PERFORMANCE_DEFAULT_CHILD: '1',
  JUHE_AI_POSTGRES_URL: 'postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai',
  JUHE_AI_REDIS_CACHE_URL: 'redis://:cache-secret@127.0.0.1:6379/0',
  JUHE_AI_REDIS_STATE_URL: 'redis://:state-secret@127.0.0.1:6380/0',
  JUHE_AI_REDIS_QUEUE_URL: 'redis://:queue-secret@127.0.0.1:6381/0'
})

assertRegressionSuccess(performanceHintDefaultResult)

assertRegressionFailure(spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_ENV_OVERRIDE_CHILD: '1',
  JUHE_AI_POSTGRES_URL: 'postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai',
  JUHE_AI_REDIS_CACHE_URL: '',
  JUHE_AI_REDIS_STATE_URL: '',
  JUHE_AI_REDIS_QUEUE_URL: ''
}), /JUHE_AI_REDIS_CACHE_URL/, '配置 PostgreSQL URL 时不能静默默认 standalone / SQLite')

assertRegressionFailure(spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_ENV_OVERRIDE_CHILD: '1',
  JUHE_AI_POSTGRES_URL: '',
  JUHE_AI_REDIS_CACHE_URL: 'redis://:cache-secret@127.0.0.1:6379/0',
  JUHE_AI_REDIS_STATE_URL: '',
  JUHE_AI_REDIS_QUEUE_URL: ''
}), /JUHE_AI_POSTGRES_URL/, '配置 Redis URL 时必须推断高性能模式并 fail-fast，不能静默回退 memory')

assertRegressionFailure(spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_PERFORMANCE_DEFAULT_CHILD: '1',
  JUHE_AI_RUNTIME_MODE: 'performance',
  JUHE_AI_DATABASE_DRIVER: 'postgres',
  JUHE_AI_CACHE_DRIVER: 'redis',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
  JUHE_AI_QUEUE_DRIVER: 'redis_stream',
  JUHE_AI_POSTGRES_URL: 'postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai',
  JUHE_AI_REDIS_CACHE_URL: 'redis://:cache-secret@127.0.0.1:6379/0',
  JUHE_AI_REDIS_STATE_URL: 'redis://:state-secret@127.0.0.1:6380/0'
}), /JUHE_AI_REDIS_QUEUE_URL/, '高性能模式必须显式配置 Redis queue URL，不能自动复用 Redis state URL')

assertRegressionFailure(spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_PERFORMANCE_DEFAULT_CHILD: '1',
  JUHE_AI_RUNTIME_MODE: 'performance',
  JUHE_AI_DATABASE_DRIVER: 'postgres',
  JUHE_AI_CACHE_DRIVER: 'redis',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
  JUHE_AI_QUEUE_DRIVER: 'redis_stream',
  JUHE_AI_POSTGRES_URL: 'postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai',
  JUHE_AI_REDIS_CACHE_URL: 'redis://:shared-secret@127.0.0.1:6379/0',
  JUHE_AI_REDIS_STATE_URL: 'redis://:shared-secret@127.0.0.1:6379/0',
  JUHE_AI_REDIS_QUEUE_URL: 'redis://:shared-secret@127.0.0.1:6379/0'
}), /不能与 .* 指向同一个 Redis 进程/, '高性能模式默认必须拒绝 cache/state/queue 共享同一个 Redis 进程')

assertRegressionFailure(spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_PERFORMANCE_DEFAULT_CHILD: '1',
  JUHE_AI_RUNTIME_MODE: 'performance',
  JUHE_AI_DATABASE_DRIVER: 'postgres',
  JUHE_AI_CACHE_DRIVER: 'redis',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
  JUHE_AI_QUEUE_DRIVER: 'redis_stream',
  JUHE_AI_POSTGRES_URL: 'postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai',
  JUHE_AI_REDIS_CACHE_URL: 'redis://:cache-secret@127.0.0.1:6379/0',
  JUHE_AI_REDIS_STATE_URL: 'redis://:state-secret@127.0.0.1:6380/0',
  JUHE_AI_REDIS_QUEUE_URL: 'redis://:queue-secret@127.0.0.1:6380/1'
}), /不能与 .* 指向同一个 Redis/, '同一 Redis 进程不同 DB 也必须拒绝，不能把 DB 隔离当作物理隔离')

assertRegressionFailure(spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_PERFORMANCE_DEFAULT_CHILD: '1',
  JUHE_AI_RUNTIME_MODE: 'performance',
  JUHE_AI_DATABASE_DRIVER: 'postgres',
  JUHE_AI_CACHE_DRIVER: 'redis',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
  JUHE_AI_QUEUE_DRIVER: 'redis_stream',
  JUHE_AI_POSTGRES_URL: 'postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai',
  JUHE_AI_REDIS_CACHE_URL: 'redis://:cache-secret@127.0.0.1:6379/0',
  JUHE_AI_REDIS_STATE_URL: 'redis://:state-secret@127.0.0.1:6380/0',
  JUHE_AI_REDIS_QUEUE_URL: 'rediss://:queue-secret@127.0.0.1:6380/1'
}), /不能与 .* 指向同一个 Redis/, '同一 host:port 即使协议不同也必须拒绝，不能用 redis/rediss 绕过物理隔离')

for (const alias of ['localhost', '[::1]']) {
  assertRegressionFailure(spawnRegression({
    JUHE_AI_RUNTIME_CONFIG_PERFORMANCE_DEFAULT_CHILD: '1',
    JUHE_AI_RUNTIME_MODE: 'performance',
    JUHE_AI_DATABASE_DRIVER: 'postgres',
    JUHE_AI_CACHE_DRIVER: 'redis',
    JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
    JUHE_AI_QUEUE_DRIVER: 'redis_stream',
    JUHE_AI_POSTGRES_URL: 'postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai',
    JUHE_AI_REDIS_CACHE_URL: 'redis://:cache-secret@127.0.0.1:6379/0',
    JUHE_AI_REDIS_STATE_URL: `redis://:state-secret@${alias}:6380/0`,
    JUHE_AI_REDIS_QUEUE_URL: 'redis://:queue-secret@127.0.0.1:6381/0'
  }), /不能使用 localhost 或 ::1/, `生产 loopback Redis URL ${alias} 必须拒绝`)
}

assertRegressionFailure(spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_PERFORMANCE_DEFAULT_CHILD: '1',
  JUHE_AI_RUNTIME_MODE: 'performance',
  JUHE_AI_DATABASE_DRIVER: 'postgres',
  JUHE_AI_CACHE_DRIVER: 'redis',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
  JUHE_AI_QUEUE_DRIVER: 'redis_stream',
  JUHE_AI_POSTGRES_URL: 'postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai',
  JUHE_AI_REDIS_CACHE_URL: 'redis://:shared-secret@127.0.0.1:6379/0',
  JUHE_AI_REDIS_STATE_URL: 'redis://:shared-secret@127.0.0.1:6379/0',
  JUHE_AI_REDIS_QUEUE_URL: 'redis://:shared-secret@127.0.0.1:6379/0',
  JUHE_AI_ALLOW_SHARED_REDIS_URLS: 'true'
}), /不能与 .* 指向同一个 Redis/, '高性能模式不能通过 JUHE_AI_ALLOW_SHARED_REDIS_URLS 绕过三实例隔离')

assertRegressionFailure(spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_PERFORMANCE_DEFAULT_CHILD: '1',
  JUHE_AI_RUNTIME_MODE: 'performance',
  JUHE_AI_DATABASE_DRIVER: 'postgres',
  JUHE_AI_CACHE_DRIVER: 'memory',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
  JUHE_AI_QUEUE_DRIVER: 'redis_stream',
  JUHE_AI_POSTGRES_URL: 'postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai',
  JUHE_AI_REDIS_CACHE_URL: 'redis://:cache-secret@127.0.0.1:6379/0',
  JUHE_AI_REDIS_STATE_URL: 'redis://:state-secret@127.0.0.1:6380/0',
  JUHE_AI_REDIS_QUEUE_URL: 'redis://:queue-secret@127.0.0.1:6381/0'
}), /JUHE_AI_CACHE_DRIVER/, '高性能模式缓存 driver 必须强制为 redis，不能使用 memory')

assertRegressionFailure(spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_PERFORMANCE_DEFAULT_CHILD: '1',
  JUHE_AI_RUNTIME_MODE: 'performance',
  JUHE_AI_DATABASE_DRIVER: 'postgres',
  JUHE_AI_CACHE_DRIVER: 'redis',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'memory',
  JUHE_AI_QUEUE_DRIVER: 'redis_stream',
  JUHE_AI_POSTGRES_URL: 'postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai',
  JUHE_AI_REDIS_CACHE_URL: 'redis://:cache-secret@127.0.0.1:6379/0',
  JUHE_AI_REDIS_STATE_URL: 'redis://:state-secret@127.0.0.1:6380/0',
  JUHE_AI_REDIS_QUEUE_URL: 'redis://:queue-secret@127.0.0.1:6381/0'
}), /JUHE_AI_RUNTIME_STATE_DRIVER/, '高性能模式运行态 driver 必须强制为 redis，不能使用 memory')

assertRegressionFailure(spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_PERFORMANCE_DEFAULT_CHILD: '1',
  JUHE_AI_RUNTIME_MODE: 'performance',
  JUHE_AI_DATABASE_DRIVER: 'postgres',
  JUHE_AI_CACHE_DRIVER: 'redis',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
  JUHE_AI_QUEUE_DRIVER: 'memory',
  JUHE_AI_POSTGRES_URL: 'postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai',
  JUHE_AI_REDIS_CACHE_URL: 'redis://:cache-secret@127.0.0.1:6379/0',
  JUHE_AI_REDIS_STATE_URL: 'redis://:state-secret@127.0.0.1:6380/0',
  JUHE_AI_REDIS_QUEUE_URL: 'redis://:queue-secret@127.0.0.1:6381/0'
}), /JUHE_AI_QUEUE_DRIVER/, '高性能模式队列 driver 必须强制为 redis_stream，不能使用 memory')

assertRegressionFailure(spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_ENV_OVERRIDE_CHILD: '1',
  JUHE_AI_RUNTIME_MODE: 'fast',
  JUHE_AI_DATABASE_DRIVER: 'sqlite',
  JUHE_AI_CACHE_DRIVER: 'memory',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'memory',
  JUHE_AI_QUEUE_DRIVER: 'memory'
}), /JUHE_AI_RUNTIME_MODE/, '非法运行模式必须 fail-fast，不能回落 standalone')

assertRegressionFailure(spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_ENV_OVERRIDE_CHILD: '1',
  JUHE_AI_RUNTIME_MODE: 'standalone',
  JUHE_AI_DATABASE_DRIVER: 'sqlite',
  JUHE_AI_CACHE_DRIVER: 'memory',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'memory',
  JUHE_AI_QUEUE_DRIVER: 'memory',
  JUHE_AI_PORT: '70000'
}), /JUHE_AI_PORT/, '非法数字配置必须 fail-fast，不能截断或回默认值')

for (const [name, value] of [
  ['JUHE_AI_CHAT_RETENTION_DAYS', '3.5'],
  ['JUHE_AI_CHAT_MAX_CONVERSATIONS_PER_USER', '0'],
  ['JUHE_AI_CHAT_MAX_TURNS_PER_CONVERSATION', '1001'],
  ['JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS', '2047'],
  ['JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS', '262145'],
  ['JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS', '2048.5'],
  ['JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS', 'many']
] as const) {
  assertRegressionFailure(spawnRegression({
    JUHE_AI_RUNTIME_CONFIG_ENV_OVERRIDE_CHILD: '1',
    [name]: value
  }), new RegExp(name), `${name} 非法值必须在启动时 fail-fast`)
}

for (const value of ['2048', '262144']) {
  assertRegressionSuccess(spawnRegression({
    JUHE_AI_RUNTIME_CONFIG_CHAT_SSE_CHILD: '1',
    JUHE_AI_RUNTIME_CONFIG_CHAT_SSE_EXPECTED: value,
    JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS: value
  }))
}

const overlayDir = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-config-'))
const overlayPath = join(overlayDir, 'performance.env')
writeFileSync(overlayPath, [
  'JUHE_AI_RUNTIME_MODE=performance',
  'JUHE_AI_DATABASE_DRIVER=postgres',
  'JUHE_AI_CACHE_DRIVER=redis',
  'JUHE_AI_RUNTIME_STATE_DRIVER=redis',
  'JUHE_AI_QUEUE_DRIVER=redis_stream',
  'JUHE_AI_POSTGRES_URL=postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai',
  'JUHE_AI_REDIS_CACHE_URL=redis://:cache-secret@127.0.0.1:6379/0',
  'JUHE_AI_REDIS_STATE_URL=redis://:state-secret@127.0.0.1:6380/0',
  'JUHE_AI_REDIS_QUEUE_URL=redis://:queue-secret@127.0.0.1:6381/0',
  'JUHE_AI_REDIS_NAMESPACE=runtime-env-file',
  'JUHE_AI_DB_POOL_MAX=44',
  'JUHE_AI_SYSTEM_API_DB_SERVICE_MAX_IN_FLIGHT=333',
  ''
].join('\n'), 'utf8')

try {
  const envFileResult = spawnRegression({
    JUHE_AI_RUNTIME_CONFIG_ENV_FILE_CHILD: '1',
    JUHE_AI_ENV_FILE: overlayPath
  })

  assertRegressionSuccess(envFileResult)
} finally {
  rmSync(overlayDir, { recursive: true, force: true })
}

console.log('运行配置环境变量覆盖回归通过：进程环境变量优先于 backend/.env，专用 env 文件可隔离高性能配置，standalone/performance 默认阈值正确')

function spawnRegression(env: Record<string, string>): ReturnType<typeof spawnSync> {
  const childEnv = { ...process.env }
  for (const key of Object.keys(childEnv)) {
    if (key.startsWith('JUHE_AI_') || key === 'NODE_ENV') {
      delete childEnv[key]
    }
  }
  if (!Object.prototype.hasOwnProperty.call(env, 'JUHE_AI_ENV_FILE')) {
    env.JUHE_AI_ENV_FILE = ''
  }

  return spawnSync(process.execPath, [
    '--import',
    'tsx',
    fileURLToPath(import.meta.url)
  ], {
    cwd: process.cwd(),
    env: {
      ...childEnv,
      ...env
    },
    encoding: 'utf8'
  })
}

function assertRegressionSuccess(result: ReturnType<typeof spawnSync>): void {
  if (result.status === 0) {
    return
  }
  process.stdout.write(result.stdout)
  process.stderr.write(result.stderr)
  process.exit(result.status ?? 1)
}

function assertRegressionFailure(result: ReturnType<typeof spawnSync>, pattern: RegExp, message: string): void {
  if (result.status !== 0 && pattern.test(`${result.stdout}\n${result.stderr}`)) {
    return
  }
  process.stdout.write(result.stdout)
  process.stderr.write(result.stderr)
  throw new Error(message)
}

function normalizePath(path: string): string {
  return path.replace(/\\/g, '/')
}
