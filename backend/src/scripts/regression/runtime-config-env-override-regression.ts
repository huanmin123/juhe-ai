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
  assert.equal(runtimeConfig.runtimeMode, 'standalone', '默认运行模式应为 standalone')
  assert.equal(runtimeConfig.databaseDriver, 'sqlite', 'standalone 默认数据库 driver 应为 sqlite')
  assert.equal(runtimeConfig.cacheDriver, 'memory', 'standalone 默认缓存 driver 应为 memory')
  assert.equal(runtimeConfig.runtimeStateDriver, 'memory', 'standalone 默认运行态 driver 应为 memory')
  assert.equal(runtimeConfig.queueDriver, 'memory', 'standalone 默认队列 driver 应为 memory')
  assert.equal(runtimeConfig.systemApi.dbServiceMaxInFlight, 64, 'standalone 默认 System API DB service 在途上限应为 64')
  assert.equal(runtimeConfig.systemApi.readOnly, true, '临时接管只读开关应支持进程环境变量显式开启')
  assert.equal(runtimeConfig.chat.retentionDays, 3, '聊天数据默认应保留 3 天')
  assert.equal(runtimeConfig.chat.maxConversationsPerUser, 50, '每用户默认最多应创建 50 个会话')
  assert.equal(runtimeConfig.chat.maxTurnsPerConversation, 50, '每个会话默认最多应接受 50 个用户轮次')
  assert.equal(runtimeConfig.chat.upstreamSseMaxEvents, 65_536, '聊天上游 SSE 默认事件上限应为 65536')

  process.exit(0)
}

if (process.env.JUHE_AI_RUNTIME_CONFIG_USAGE_SHARD_DEFAULT_CHILD === '1') {
  const { usageRecordShardRoot } = await import('../../storage/usage-record-shards.js')

  assert.equal(normalizePath(usageRecordShardRoot()).endsWith('runtime-config-usage-catalog-dir/usage-shards'), true, '未配置 usage shard 根目录时应跟随使用记录目录库生成同级 usage-shards')

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
  assert.equal(runtimeConfig.systemApi.readOnly, false, 'System API 临时只读开关应支持显式关闭')
  assert.equal(runtimeConfig.queue.redisStreamReadCount, 500, 'Redis Stream 批量读取数量应正确读取')
  assert.deepEqual(runtimeConfig.chat, {
    retentionDays: 9,
    maxConversationsPerUser: 60,
    maxTurnsPerConversation: 70,
    upstreamSseMaxEvents: 131_072
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
  assert.equal(runtimeConfig.systemApi.readOnly, false, '正式环境未配置时 System API 临时只读开关必须默认关闭')
  assert.equal(runtimeConfig.postgres.statementTimeoutMs, 30000, 'performance 默认 PostgreSQL statement timeout 应为 30 秒')
  assert.equal(runtimeConfig.postgres.lockTimeoutMs, 2000, 'performance 默认 PostgreSQL lock timeout 应为 2 秒')
  assert.equal(runtimeConfig.postgres.idleInTransactionSessionTimeoutMs, 30000, 'performance 默认 PostgreSQL idle transaction timeout 应为 30 秒')
  assert.match(runtimeConfig.redis.namespace, /^env-[a-f0-9]{12}$/, '未显式配置 Redis namespace 时应由运行密钥派生稳定环境前缀')
  assert.equal('redisStreamMaxLen' in runtimeConfig.queue, false, 'Redis Stream 可靠队列不应暴露近似裁剪配置')

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
  JUHE_AI_SYSTEM_API_READ_ONLY: 'true',
  JUHE_AI_LOG_CONSOLE_ENABLED: 'false'
})

assertRegressionSuccess(result)

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
  JUHE_AI_SYSTEM_API_READ_ONLY: 'false',
  JUHE_AI_REDIS_STREAM_READ_COUNT: '500',
  JUHE_AI_CHAT_RETENTION_DAYS: '9',
  JUHE_AI_CHAT_MAX_CONVERSATIONS_PER_USER: '60',
  JUHE_AI_CHAT_MAX_TURNS_PER_CONVERSATION: '70',
  JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS: '131072'
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
  JUHE_AI_RUNTIME_MODE: 'standalone',
  JUHE_AI_DATABASE_DRIVER: 'sqlite',
  JUHE_AI_CACHE_DRIVER: 'memory',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'memory',
  JUHE_AI_QUEUE_DRIVER: 'memory',
  JUHE_AI_SYSTEM_API_READ_ONLY: 'invalid'
}), /JUHE_AI_SYSTEM_API_READ_ONLY 只能配置为/, '临时只读开关非法值必须在启动时失败')

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
