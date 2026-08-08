import { createHash } from 'node:crypto'
import { existsSync, readFileSync } from 'node:fs'
import { isIP } from 'node:net'
import { dirname, isAbsolute, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { assertDevelopmentAutoLoginConfig } from './development.js'
import { loadRuntimeBaseEnv, loadRuntimeEnvFile } from './runtime-base-env.js'

export interface RuntimeConfig {
  runtimeMode: RuntimeMode
  performanceNodeRole: PerformanceNodeRole
  processRole: ProcessRole
  workerRole: WorkerRuntimeRole
  instanceId: string
  workerReplicaIndex: number
  topology: {
    backgroundWorkerSupervisorEnabled: boolean
    gatewayReplicas: number
    usageWorkerReplicas: number
    logWorkerReplicas: number
    statsWorkerReplicas: number
    opsWorkerReplicas: number
  }
  databaseDriver: DatabaseDriver
  cacheDriver: CacheDriver
  runtimeStateDriver: RuntimeStateDriver
  queueDriver: QueueDriver
  host: string
  port: number
  development: {
    autoLoginUsername?: string
  }
  httpSecurity: {
    cors: {
      allowedOrigins: string[]
      allowAnyOrigin: boolean
    }
    cookie: {
      secure: boolean
      sameSite: CookieSameSiteRuntimeConfig
    }
    trustProxy: boolean | number
  }
  auth: {
    captchaDisabled: boolean
  }
  upstreamUrlSecurity: {
    allowPrivateBaseUrls: boolean
    privateBaseUrlAllowlist: string[]
  }
  dbServiceHttpHost: string
  dbServiceHttpPort: number
  accountHealthCheckDispatchUrl?: string
  systemApi: {
    dbServiceMaxInFlight: number
  }
  dbService: {
    maxActiveRequests: number
    queueMaxRequests: number
    queueMaxBytes: number
    highDispatchesBeforeNormal: number
    highDispatchesBeforeLow: number
  }
  dbServiceHttpProxy: {
    maxInFlight: number
    timeoutMs: number
    chatMaxInFlight: number
    chatTimeoutMs: number
  }
  ownerLock: {
    enabled: boolean
    manifestPath?: string
    deploymentEpoch?: string
  }
  postgres: {
    url?: string
    poolMax: number
    writeMaxConcurrency: number
    writeQueueMaxItems: number
    connectionTimeoutMs: number
    statementTimeoutMs: number
    lockTimeoutMs: number
    idleInTransactionSessionTimeoutMs: number
  }
  redis: {
    cacheUrl?: string
    stateUrl?: string
    queueUrl?: string
    namespace: string
  }
  queue: {
    redisStreamReadCount: number
    redisStreamBlockMs: number
    redisStreamClaimIdleMs: number
  }
  usageSpool: {
    directory: string
    maxItems: number
    maxBytes: number
    replayBatchSize: number
    replayIntervalMs: number
  }
  databasePath: string
  chatDatabasePath: string
  datasetDatabasePath: string
  runtimeLogDatabasePath: string
  usageCatalogDatabasePath: string
  statsDatabasePath: string
  tableMonitorDatabasePath: string
  usageShardRoot: string
  codexContextRoot: string
  chatAssetsRoot: string
  chat: {
    retentionDays: number
    maxConversationsPerUser: number
    maxTurnsPerConversation: number
    upstreamSseMaxEvents: number
    diagnosticToolEnabled: boolean
  }
  openAICompatibleFilesRoot: string
  codexContextStateShardRoot: string
  codexContextStateShardCount: number
  codexContextStateWriterPoolEnabled: boolean
  codexContextStateWriterPoolSize: number
  codexContextStateWriterQueueMaxItems: number
  sqliteReadWorkerPoolSize: number
  sqliteReadWorkerQueueMaxItems: number
  usageRecordWriterPoolEnabled: boolean
  usageRecordWriterPoolSize: number
  usageRecordWriterQueueMaxItems: number
  usageShardCount: number
  secret: string
  oauthProxyUrl?: string
  concurrency: {
    globalMax: number
    globalLeaseDurationMs: number
    globalAcquirePollMs: number
    defaultAccountLimit: number
    accountSlotLeaseDurationMs: number
    accountSlotRefreshIntervalMs: number
  }
  gateway: {
    bodyInFlightMaxBytes: number
    upstreamAgentMaxSockets: number
    upstreamAgentMaxFreeSockets: number
    upstreamAgentMaxTotalSockets: number
    accountCircuitConfirmationFailuresRequired?: number
    accountCircuitEscalationDistinctScopeThreshold: number
    accountCircuitEscalationWindowMs: number
    accountCircuitCapacity: number
    accountCircuitRebuildPageTimeoutMs: number
    accountCircuitRebuildTotalTimeoutMs: number
    accountCircuitRebuildMaxPages: number
    usageFinalizationMaxItems: number
    accountCircuitRecoveryBatchSize: number
    accountCircuitRecoveryLeaseDurationMs: number
    accountCircuitBackoffMs: number[]
    accountCircuitRecoverySuccessThreshold: number
    accountCircuitRecoveryCanaryIntervalMs: number
    accountCircuitSuspectConfirmationIntervalMs: number
    accountConcurrencyRetryBudgetMs: number
    accountConcurrencyRetryInitialDelayMs: number
    accountConcurrencyRetryMaxDelayMs: number
    accountApiKeyRequestAttemptSafetyLimit: number
    dispatchAccountCandidateLimit: number
    proxyHealthFailureMaxEntries: number
    proxyHealthFailureWindowMs: number
    proxyHealthAvoidTtlMs: number
    proxyHealthHalfOpenLeaseMs: number
    proxyHealthDistinctAccountThreshold: number
    proxyHealthCasMaxAttempts: number
    proxyHealthMaxAccountSamples: number
    accountSideEffectAvoidanceCacheTtlMs: number
    accountSideEffectAvoidanceNegativeCacheTtlMs: number
    accountSideEffectAvoidanceCacheMaxEntries: number
    accountSideEffectRetryInitialDelayMs: number
    accountSideEffectRetryMaxDelayMs: number
    accountSideEffectQueueMaxLength: number
    automaticProbeSweepBatchSize: number
    automaticProbeSweepIntervalMs: number
    automaticProbeDueRetryDelayMs: number
    automaticProbeStateReadBatchSize: number
    automaticProbePrecheckMinIntervalMs: number
    automaticProbeConcurrencyDrainPollMs: number
    automaticProbeRecoveryRetryDelayMs: number
    automaticProbeRecoveryPrecheckFailureThreshold: number
    automaticProbeRecoveryAccountMinIntervalMs: number
    automaticProbeRecoveryScopeMinIntervalMs: number
    automaticProbeRecoveryJitterMs: number
    recoverableUnavailableMaxWaitMs: number
    recoverableUnavailableCheckIntervalMs: number
    recoverableUnavailableDueRetryDelayMs: number
    recoverableUnavailableMaxWaitersPerScope: number
    recoverableUnavailableMaxWaitersGlobal: number
  }
  background: {
    accountHealthCheckBatchSize: number
    cooldownAccountRetestBatchSize: number
    accountApiKeyCooldownRetestBatchSize: number
    accountQualityFailurePrecheckBatchSize: number
    normalRouteSpeedFirstRecoveryProbeBatchSize: number
    accountBalanceAutoDetectionRecoveryBatchSize: number
    accountBalanceAutoDetectionBackfillPageSize: number
    accountBalanceRefreshBatchSize: number
    accountBalanceRefreshRecoveryBatchSize: number
    auditLogPostgresFlushBatchSize: number
    auditLogPostgresRedisConsumerConcurrency: number
    auditPayloadBlobWriteConcurrency: number
    auditBlobCleanupDeleteConcurrency: number
    modelCheckTokenWorkerTargetSize: number
    modelCheckTokenWorkerQueueMaxItems: number
    diagnosticTaskMaxInFlight: number
    accountTestRefillMaxBatchSize: number
    accountTestQueuedSweepBatchSize: number
    accountTestQueuedMaxWaitMs: number
    accountApiKeyProbeCandidateScanLimit: number
    accountBalanceRecoveryMaxScanPages: number
    accountAvailabilityScheduleSyncBatchLimit: number
    apiKeyScheduleSyncBatchLimit: number
    accountRuntimeStatusHydrationBatchSize: number
    proxyLatencyRefreshConcurrency: number
    proxyLatencyRefreshBatchSize: number
    proxyProbeTimeoutMs: number
    proxyManualTestDeadlineMs: number
    proxyLatencyRefreshIntervalSeconds: number
    proxyLatencyRefreshRunBudgetMs: number
    proxyLatencyRefreshCandidateDeadlineMs: number
    proxyLatencyRefreshCandidatePoolFactor: number
    proxyLatencyRefreshLeaseGraceMs: number
    accountProbeDbServiceTimeoutMs: number
    accountHealthCheckProbeDeadlineMs: number
    cooldownAccountRetestStartupDelayMs: number
    accountApiKeyCooldownRetestStartupDelayMs: number
    normalRouteSpeedFirstProbeStartupDelayMs: number
    modelQualityScheduledCheckBatchSize: number
    modelQualityHealthSyncRetryBatchSize: number
    taskRunReconcileBatchSize: number
    modelTrustObservationAggregationBatchSize: number
    auditHotRetentionCleanupBatchSize: number
    auditHotRetentionCleanupMaxBatches: number
    auditHotRetentionCleanupMaxRunMs: number
    operationLogBatchSize: number
    operationLogShutdownFlushMaxBatches: number
    operationLogQueueMaxItems: number
    operationLogQueueMaxMb: number
    auditLogTransportMaxQueuedJobs: number
    auditLogTransportMaxTotalMb: number
    auditLogTransportMaxActiveMb: number
    auditLogTransportMaxJobMb: number
    auditLogFlushBatchMaxMb: number
    auditLogScheduledFlushMaxBatches: number
    auditLogShutdownFlushMaxBatches: number
    auditLogRedisStreamMaxItems: number
    auditLogRedisStreamMaxMb: number
    ipcUsageRecordQueueMaxMessages: number
    ipcUsageRecordQueueMaxMb: number
    ipcRegularWorkerQueueMaxMessages: number
    ipcRegularWorkerQueueMaxMb: number
    ipcPendingDbServiceRequestMaxCount: number
    recordMaintenanceBatchSize: number
    recordMaintenanceShutdownFlushMaxBatches: number
    recordMaintenanceQueueMaxItems: number
    recordMaintenanceQueueMaxMb: number
    recordMaintenanceAuditCleanupBatchSize: number
    recordMaintenanceAuditCleanupMaxBatches: number
    usageRecordBatchSize: number
    usageRecordFlushBatchMaxMb: number
    usageRecordShutdownFlushMaxBatches: number
    usageRecordQueueMaxItems: number
    usageRecordQueueMaxMb: number
  }
  modelCheck: {
    probeRetryDelayMs: number
  }
  auditLog: {
    enabled: boolean
    successHotRetentionHours: number
    successSampleRate: number
    successRetentionDays: number
    problemRetentionDays: number
    successFullBodyLimitBytes: number
    problemFullBodyLimitBytes: number
  }
  codexWebSearch: {
    endpoint?: string
    apiKey?: string
    timeoutMs: number
    maxResults: number
    maxBodyBytes: number
  }
  codeInterpreter: {
    pythonCommand: string
    timeoutMs: number
    maxCodeBytes: number
    maxOutputBytes: number
    maxArtifactCount: number
    maxArtifactBytes: number
    tempRoot: string
    cleanupTempDirectory: boolean
  }
  computerAdapter: {
    enabled: boolean
    endpoint?: string
    timeoutMs: number
    maxBodyBytes: number
  }
  hostedToolRuntimes: {
    codeInterpreter: HostedToolRuntimeMode
    computer: HostedToolRuntimeMode
    shell: HostedToolRuntimeMode
    skills: HostedToolRuntimeMode
    toolSearch: HostedToolRuntimeMode
  }
  log: {
    level: LogLevel
    directory: string
    fileEnabled: boolean
    consoleEnabled: boolean
    maxFileBytes: number
    retentionDays: number
    maxFiles: number
    cleanupIntervalMinutes: number
    gatewayTimingDetailSamplePermille: number
    gatewayStagePressureMaxPendingBytes: number
  }
  smokeTest: {
    backendUrl: string
    adminUsername: string
    adminPassword: string
    accountName: string
    model: string
    prompt: string
  }
}

export type LogLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error' | 'fatal' | 'silent'
export type RuntimeMode = 'standalone' | 'performance'
export type PerformanceNodeRole = 'combined' | 'gateway' | 'control'
export type ProcessRole = 'server' | 'worker' | 'db-service'
export type DatabaseDriver = 'sqlite' | 'postgres'
export type CacheDriver = 'memory' | 'redis'
export type RuntimeStateDriver = 'memory' | 'redis'
export type QueueDriver = 'memory' | 'redis_stream'
export type WorkerRuntimeRole =
  | 'worker'
  | 'ingest-worker'
  | 'usage-worker'
  | 'log-worker'
  | 'stats-worker'
  | 'ops-worker'
  | 'temporary-maintenance-worker'
export type CookieSameSiteRuntimeConfig = 'lax' | 'strict' | 'none'
export type HostedToolRuntimeMode = 'guidance' | 'reject' | 'mock' | 'local_runtime'
export const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
export const localEnvPath = resolve(backendRoot, '.env')
export const defaultDatabasePath = resolve(backendRoot, 'data', 'juhe-ai.sqlite3')
export const defaultChatDatabasePath = resolve(backendRoot, 'data', 'juhe-ai-chat.sqlite3')
export const defaultDatasetDatabasePath = resolve(backendRoot, 'data', 'juhe-ai-dataset.sqlite3')
export const defaultRuntimeLogDatabasePath = resolve(backendRoot, 'data', 'juhe-ai-runtime-log.sqlite3')
export const defaultUsageCatalogDatabasePath = resolve(backendRoot, 'data', 'juhe-ai-usage-catalog.sqlite3')
export const defaultStatsDatabasePath = resolve(backendRoot, 'data', 'juhe-ai-stats.sqlite3')
export const defaultTableMonitorDatabasePath = resolve(backendRoot, 'data', 'juhe-ai-table-monitor.sqlite3')
export const defaultUsageSpoolDirectory = resolve(backendRoot, 'data', 'usage-spool')
export const defaultUsageShardRoot = resolve(backendRoot, 'data', 'usage-shards')
export const defaultCodexContextRoot = resolve(backendRoot, 'data', 'codex-context')
export const defaultChatAssetsRoot = resolve(backendRoot, 'data', 'chat-assets')
export const defaultOpenAICompatibleFilesRoot = resolve(backendRoot, 'data', 'openai-compatible-files')
export const defaultCodeInterpreterTempRoot = resolve(backendRoot, 'data', 'code-interpreter-tmp')
export const defaultCodexContextStateShardRoot = resolve(defaultCodexContextRoot, 'state-shards')
export const defaultRuntimeSecret = 'juhe-ai-dev-secret-change-me'
const minimumProductionSecretLength = 32

const localEnv = loadRuntimeBaseEnv(localEnvPath, process.env)
const localEnvOverlayPath = envFilePathConfig(process.env.JUHE_AI_ENV_FILE ?? localEnv.JUHE_AI_ENV_FILE)
const localEnvOverlay = localEnvOverlayPath ? loadRuntimeEnvFile(localEnvOverlayPath) : {}
export const localCapacityEnvPath = resolve(backendRoot, '.env.capacity')
const localCapacityEnv = Object.fromEntries(
  Object.entries(loadRuntimeEnvFile(localCapacityEnvPath)).filter(([name]) => isCapacityEnvironmentVariable(name))
)
const defaultBackgroundConcurrency = integerConfig('JUHE_AI_CONCURRENCY_BACKGROUND_DEFAULT_MAX', 20, 1, 1_000)
const globalConcurrencyMax = integerConfig('JUHE_AI_CONCURRENCY_GLOBAL_MAX', 5_000, 1, 50_000)
const globalConcurrencyLeaseDurationMs = integerConfig(
  'JUHE_AI_CONCURRENCY_GLOBAL_LEASE_DURATION_MS',
  300_000,
  10_000,
  3_600_000
)
const globalConcurrencyAcquirePollMs = integerConfig(
  'JUHE_AI_CONCURRENCY_GLOBAL_ACQUIRE_POLL_MS',
  50,
  10,
  1_000
)
const upstreamAgentMaxSockets = numberConfig(
  'JUHE_AI_GATEWAY_UPSTREAM_AGENT_MAX_SOCKETS',
  globalConcurrencyMax,
  64,
  50_000
)
const upstreamAgentMaxFreeSockets = numberConfig('JUHE_AI_GATEWAY_UPSTREAM_AGENT_MAX_FREE_SOCKETS', 512, 16, 50_000)
const upstreamAgentMaxTotalSockets = numberConfig(
  'JUHE_AI_GATEWAY_UPSTREAM_AGENT_MAX_TOTAL_SOCKETS',
  globalConcurrencyMax,
  64,
  50_000
)
if (upstreamAgentMaxTotalSockets < upstreamAgentMaxSockets) {
  throw new Error('JUHE_AI_GATEWAY_UPSTREAM_AGENT_MAX_TOTAL_SOCKETS 不能小于 JUHE_AI_GATEWAY_UPSTREAM_AGENT_MAX_SOCKETS')
}
const hasPerformanceDriverHints = hasAnyRawConfig([
  'JUHE_AI_POSTGRES_URL',
  'JUHE_AI_REDIS_CACHE_URL',
  'JUHE_AI_REDIS_STATE_URL',
  'JUHE_AI_REDIS_QUEUE_URL'
])
const configuredRuntimeMode = runtimeModeConfig('JUHE_AI_RUNTIME_MODE', hasPerformanceDriverHints ? 'performance' : 'standalone')
const configuredPerformanceNodeRole = configuredRuntimeMode === 'performance'
  ? performanceNodeRoleConfig('JUHE_AI_PERFORMANCE_NODE_ROLE', 'combined')
  : 'combined'
const configuredProcessRole = processRoleConfig('JUHE_AI_PROCESS_ROLE', 'server')
const defaultModelCheckProbeRetryDelayMs = isScriptEntryRuntime() ? 0 : 65000
const configuredDatabaseDriver = databaseDriverConfig(
  'JUHE_AI_DATABASE_DRIVER',
  configuredRuntimeMode === 'performance' ? 'postgres' : 'sqlite'
)
const configuredCacheDriver = cacheDriverConfig(
  'JUHE_AI_CACHE_DRIVER',
  configuredRuntimeMode === 'performance' ? 'redis' : 'memory'
)
const configuredRuntimeStateDriver = runtimeStateDriverConfig(
  'JUHE_AI_RUNTIME_STATE_DRIVER',
  configuredRuntimeMode === 'performance' ? 'redis' : 'memory'
)
const configuredQueueDriver = queueDriverConfig(
  'JUHE_AI_QUEUE_DRIVER',
  configuredRuntimeMode === 'performance' ? 'redis_stream' : 'memory'
)
const configuredPostgresUrl = optionalStringConfig('JUHE_AI_POSTGRES_URL')
const configuredRedisCacheUrl = optionalStringConfig('JUHE_AI_REDIS_CACHE_URL')
const configuredRedisStateUrl = optionalStringConfig('JUHE_AI_REDIS_STATE_URL')
const configuredRedisQueueUrl = optionalStringConfig('JUHE_AI_REDIS_QUEUE_URL')
const configuredAccountHealthCheckDispatchUrl = accountHealthCheckDispatchUrlConfig(
  'JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_URL',
  optionalStringConfig('JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_URL'),
  {
    runtimeMode: configuredRuntimeMode,
    performanceNodeRole: configuredPerformanceNodeRole,
    processRole: configuredProcessRole
  }
)
const configuredSecret = secretConfig('JUHE_AI_SECRET', defaultRuntimeSecret)
const configuredRedisNamespace = redisNamespaceConfig('JUHE_AI_REDIS_NAMESPACE', configuredSecret)
const configuredHost = stringConfig('JUHE_AI_HOST', '127.0.0.1')
const configuredPort = numberConfig('JUHE_AI_PORT', 3000, 1, 65535)
const configuredDevelopmentAutoLoginUsername = optionalStringConfig('JUHE_AI_DEV_AUTO_LOGIN_USERNAME')
const configuredLogFileEnabled = booleanConfig('JUHE_AI_LOG_FILE_ENABLED', true)

assertDevelopmentAutoLoginConfig({
  username: configuredDevelopmentAutoLoginUsername,
  nodeEnv: rawStringConfig('NODE_ENV'),
  host: configuredHost
})

assertRuntimeModeDrivers({
  runtimeMode: configuredRuntimeMode,
  databaseDriver: configuredDatabaseDriver,
  cacheDriver: configuredCacheDriver,
  runtimeStateDriver: configuredRuntimeStateDriver,
  queueDriver: configuredQueueDriver,
  postgresUrl: configuredPostgresUrl,
  redisCacheUrl: configuredRedisCacheUrl,
  redisStateUrl: configuredRedisStateUrl,
  redisQueueUrl: configuredRedisQueueUrl
})

export const runtimeConfig: RuntimeConfig = {
  runtimeMode: configuredRuntimeMode,
  performanceNodeRole: configuredPerformanceNodeRole,
  processRole: configuredProcessRole,
  workerRole: workerRoleConfig('JUHE_AI_WORKER_ROLE', 'worker'),
  instanceId: runtimeInstanceIdConfig('JUHE_AI_INSTANCE_ID', configuredRuntimeMode),
  workerReplicaIndex: numberConfig('JUHE_AI_WORKER_REPLICA_INDEX', 0, 0, 63),
  topology: {
    backgroundWorkerSupervisorEnabled: configuredRuntimeMode === 'standalone'
      || configuredPerformanceNodeRole !== 'gateway',
    gatewayReplicas: configuredRuntimeMode === 'performance'
      ? numberConfig('JUHE_AI_GATEWAY_REPLICAS', 3, 1, 32)
      : 1,
    usageWorkerReplicas: configuredRuntimeMode === 'performance'
      ? numberConfig('JUHE_AI_USAGE_WORKER_REPLICAS', 2, 1, 32)
      : 1,
    logWorkerReplicas: configuredRuntimeMode === 'performance'
      ? numberConfig('JUHE_AI_LOG_WORKER_REPLICAS', 2, 1, 32)
      : 1,
    statsWorkerReplicas: configuredRuntimeMode === 'performance'
      ? numberConfig('JUHE_AI_STATS_WORKER_REPLICAS', 1, 1, 1)
      : 1,
    opsWorkerReplicas: configuredRuntimeMode === 'performance'
      ? numberConfig('JUHE_AI_OPS_WORKER_REPLICAS', 1, 1, 1)
      : 1
  },
  databaseDriver: configuredDatabaseDriver,
  cacheDriver: configuredCacheDriver,
  runtimeStateDriver: configuredRuntimeStateDriver,
  queueDriver: configuredQueueDriver,
  host: configuredHost,
  port: configuredPort,
  development: {
    autoLoginUsername: configuredDevelopmentAutoLoginUsername
  },
  dbServiceHttpHost: stringConfig('JUHE_AI_DB_SERVICE_HTTP_HOST', '127.0.0.1'),
  dbServiceHttpPort: numberConfig('JUHE_AI_DB_SERVICE_HTTP_PORT', 0, 0, 65535),
  accountHealthCheckDispatchUrl: configuredAccountHealthCheckDispatchUrl,
  systemApi: {
    dbServiceMaxInFlight: numberConfig(
      'JUHE_AI_SYSTEM_API_DB_SERVICE_MAX_IN_FLIGHT',
      globalConcurrencyMax,
      1,
      5000
    )
  },
  dbService: {
    maxActiveRequests: numberConfig('JUHE_AI_DB_SERVICE_MAX_ACTIVE_REQUESTS', globalConcurrencyMax, 1, 50000),
    queueMaxRequests: numberConfig('JUHE_AI_DB_SERVICE_QUEUE_MAX_REQUESTS', globalConcurrencyMax, 1, 1000000),
    queueMaxBytes: numberConfig('JUHE_AI_DB_SERVICE_QUEUE_MAX_MB', 512, 16, 4096) * 1024 * 1024,
    highDispatchesBeforeNormal: numberConfig('JUHE_AI_DB_SERVICE_HIGH_DISPATCHES_BEFORE_NORMAL', 8, 1, 1000),
    highDispatchesBeforeLow: numberConfig('JUHE_AI_DB_SERVICE_HIGH_DISPATCHES_BEFORE_LOW', 16, 1, 1000)
  },
  dbServiceHttpProxy: {
    maxInFlight: numberConfig('JUHE_AI_DB_SERVICE_HTTP_PROXY_MAX_IN_FLIGHT', globalConcurrencyMax, 1, 50000),
    timeoutMs: numberConfig('JUHE_AI_DB_SERVICE_HTTP_PROXY_TIMEOUT_MS', 30000, 1000, 3600000),
    chatMaxInFlight: numberConfig('JUHE_AI_CHAT_DB_SERVICE_HTTP_PROXY_MAX_IN_FLIGHT', globalConcurrencyMax, 1, 50000),
    chatTimeoutMs: numberConfig('JUHE_AI_CHAT_DB_SERVICE_HTTP_PROXY_TIMEOUT_MS', 15 * 60_000, 1000, 3600000)
  },
  ownerLock: {
    enabled: parseOwnerLockEnabled(rawStringConfig('JUHE_AI_OWNER_LOCK_ENABLED')),
    manifestPath: optionalStringConfig('JUHE_AI_OWNER_MANIFEST_PATH'),
    deploymentEpoch: optionalStringConfig('JUHE_AI_OWNER_LOCK_DEPLOYMENT_EPOCH')
  },
  postgres: {
    url: configuredPostgresUrl,
    poolMax: numberConfig('JUHE_AI_DB_POOL_MAX', 50, 1, 500),
    writeMaxConcurrency: numberConfig('JUHE_AI_DB_WRITE_MAX_CONCURRENCY', 100, 1, 1000),
    writeQueueMaxItems: numberConfig('JUHE_AI_DB_WRITE_QUEUE_MAX_ITEMS', 50000, 100, 1000000),
    connectionTimeoutMs: numberConfig('JUHE_AI_POSTGRES_CONNECTION_TIMEOUT_MS', 10000, 100, 3600000),
    statementTimeoutMs: numberConfig('JUHE_AI_POSTGRES_STATEMENT_TIMEOUT_MS', 30000, 0, 3600000),
    lockTimeoutMs: numberConfig('JUHE_AI_POSTGRES_LOCK_TIMEOUT_MS', 2000, 0, 60000),
    idleInTransactionSessionTimeoutMs: numberConfig('JUHE_AI_POSTGRES_IDLE_IN_TRANSACTION_TIMEOUT_MS', 30000, 0, 3600000)
  },
  redis: {
    cacheUrl: configuredRedisCacheUrl,
    stateUrl: configuredRedisStateUrl,
    queueUrl: configuredRedisQueueUrl,
    namespace: configuredRedisNamespace
  },
  queue: {
    redisStreamReadCount: numberConfig('JUHE_AI_REDIS_STREAM_READ_COUNT', 1000, 1, 5000),
    redisStreamBlockMs: numberConfig('JUHE_AI_REDIS_STREAM_BLOCK_MS', 1000, 100, 60000),
    redisStreamClaimIdleMs: numberConfig('JUHE_AI_REDIS_STREAM_CLAIM_IDLE_MS', 60000, 1000, 3600000)
  },
  usageSpool: {
    directory: pathConfig('JUHE_AI_USAGE_SPOOL_DIR', defaultUsageSpoolDirectory),
    maxItems: numberConfig('JUHE_AI_USAGE_SPOOL_MAX_ITEMS', 250_000, 1_000, 5_000_000),
    maxBytes: numberConfig('JUHE_AI_USAGE_SPOOL_MAX_MB', 4_096, 64, 102_400) * 1024 * 1024,
    replayBatchSize: numberConfig('JUHE_AI_USAGE_SPOOL_REPLAY_BATCH_SIZE', 500, 1, 5_000),
    replayIntervalMs: numberConfig('JUHE_AI_USAGE_SPOOL_REPLAY_INTERVAL_MS', 1_000, 100, 60_000)
  },
  databasePath: pathConfig('JUHE_AI_DATABASE_PATH', defaultDatabasePath),
  chatDatabasePath: pathConfig('JUHE_AI_CHAT_DATABASE_PATH', defaultChatDatabasePath),
  datasetDatabasePath: pathConfig('JUHE_AI_DATASET_DATABASE_PATH', defaultDatasetDatabasePath),
  runtimeLogDatabasePath: pathConfig('JUHE_AI_RUNTIME_LOG_DATABASE_PATH', defaultRuntimeLogDatabasePath),
  usageCatalogDatabasePath: pathConfig('JUHE_AI_USAGE_CATALOG_DATABASE_PATH', defaultUsageCatalogDatabasePath),
  statsDatabasePath: pathConfig('JUHE_AI_STATS_DATABASE_PATH', defaultStatsDatabasePath),
  tableMonitorDatabasePath: pathConfig('JUHE_AI_TABLE_MONITOR_DATABASE_PATH', defaultTableMonitorDatabasePath),
  usageShardRoot: pathConfig('JUHE_AI_USAGE_SHARD_ROOT', defaultUsageShardRoot),
  codexContextRoot: pathConfig('JUHE_AI_CODEX_CONTEXT_ROOT', defaultCodexContextRoot),
  chatAssetsRoot: pathConfig('JUHE_AI_CHAT_ASSETS_ROOT', defaultChatAssetsRoot),
  chat: {
    retentionDays: integerConfig('JUHE_AI_CHAT_RETENTION_DAYS', 3, 1, 365),
    maxConversationsPerUser: integerConfig('JUHE_AI_CHAT_MAX_CONVERSATIONS_PER_USER', 50, 1, 1000),
    maxTurnsPerConversation: integerConfig('JUHE_AI_CHAT_MAX_TURNS_PER_CONVERSATION', 50, 1, 1000),
    upstreamSseMaxEvents: integerConfig('JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS', 65_536, 2_048, 262_144),
    diagnosticToolEnabled: booleanConfig('JUHE_AI_CHAT_DIAGNOSTIC_TOOL_ENABLED', false)
  },
  openAICompatibleFilesRoot: pathConfig('JUHE_AI_OPENAI_COMPATIBLE_FILES_ROOT', defaultOpenAICompatibleFilesRoot),
  codexContextStateShardRoot: pathConfig('JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT', defaultCodexContextStateShardRoot),
  codexContextStateShardCount: numberConfig('JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT', 16, 1, 256),
  codexContextStateWriterPoolEnabled: booleanConfig('JUHE_AI_CODEX_CONTEXT_STATE_WRITER_POOL_ENABLED', !isScriptEntryRuntime()),
  codexContextStateWriterPoolSize: numberConfig('JUHE_AI_CODEX_CONTEXT_STATE_WRITER_POOL_SIZE', 0, 0, 64),
  codexContextStateWriterQueueMaxItems: numberConfig('JUHE_AI_CODEX_CONTEXT_STATE_WRITER_QUEUE_MAX_ITEMS', 5000, 1, 100000),
  sqliteReadWorkerPoolSize: numberConfig('JUHE_AI_SQLITE_READ_WORKER_POOL_SIZE', 0, 0, 64),
  sqliteReadWorkerQueueMaxItems: numberConfig('JUHE_AI_SQLITE_READ_WORKER_QUEUE_MAX_ITEMS', 1000, 1, 100000),
  usageRecordWriterPoolEnabled: booleanConfig('JUHE_AI_USAGE_RECORD_WRITER_POOL_ENABLED', false),
  usageRecordWriterPoolSize: numberConfig('JUHE_AI_USAGE_RECORD_WRITER_POOL_SIZE', 0, 0, 64),
  usageRecordWriterQueueMaxItems: numberConfig('JUHE_AI_USAGE_RECORD_WRITER_QUEUE_MAX_ITEMS', 5000, 1, 100000),
  usageShardCount: numberConfig('JUHE_AI_USAGE_SHARD_COUNT', 16, 1, 256),
  secret: configuredSecret,
  concurrency: {
    globalMax: globalConcurrencyMax,
    globalLeaseDurationMs: globalConcurrencyLeaseDurationMs,
    globalAcquirePollMs: globalConcurrencyAcquirePollMs,
    defaultAccountLimit: integerConfig('JUHE_AI_ACCOUNT_DEFAULT_CONCURRENCY_LIMIT', globalConcurrencyMax, 1, 50000),
    accountSlotLeaseDurationMs: integerConfig('JUHE_AI_ACCOUNT_SLOT_LEASE_DURATION_MS', 90000, 10000, 3600000),
    accountSlotRefreshIntervalMs: integerConfig('JUHE_AI_ACCOUNT_SLOT_REFRESH_INTERVAL_MS', 15000, 1000, 600000)
  },
  httpSecurity: httpSecurityConfig(),
  auth: authRuntimeConfig(),
  upstreamUrlSecurity: upstreamUrlSecurityConfig(),
  oauthProxyUrl: optionalStringConfig('JUHE_AI_OAUTH_PROXY_URL'),
  gateway: {
    bodyInFlightMaxBytes: numberConfig('JUHE_AI_GATEWAY_BODY_IN_FLIGHT_MAX_MB', 256, 16, 4096) * 1024 * 1024,
    upstreamAgentMaxSockets,
    upstreamAgentMaxFreeSockets,
    upstreamAgentMaxTotalSockets,
    accountCircuitConfirmationFailuresRequired: optionalIntegerConfig(
      'JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_CONFIRMATION_FAILURES_REQUIRED',
      1,
      5
    ),
    accountCircuitEscalationDistinctScopeThreshold: integerConfig(
      'JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_ESCALATION_DISTINCT_SCOPE_THRESHOLD',
      3,
      3,
      64
    ),
    accountCircuitEscalationWindowMs: integerConfig(
      'JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_ESCALATION_WINDOW_MS',
      10 * 60_000,
      60_000,
      24 * 60 * 60_000
    ),
    accountCircuitCapacity: integerConfig('JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_CAPACITY', 50_000, 1_000, 1_000_000),
    accountCircuitRebuildPageTimeoutMs: integerConfig(
      'JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_REBUILD_PAGE_TIMEOUT_MS',
      2_000,
      100,
      30_000
    ),
    accountCircuitRebuildTotalTimeoutMs: integerConfig(
      'JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_REBUILD_TOTAL_TIMEOUT_MS',
      15_000,
      500,
      300_000
    ),
    accountCircuitRebuildMaxPages: integerConfig('JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_REBUILD_MAX_PAGES', 200, 1, 2_000),
    usageFinalizationMaxItems: integerConfig('JUHE_AI_GATEWAY_USAGE_FINALIZATION_MAX_ITEMS', 2048, 1, 1_000_000),
    accountCircuitRecoveryBatchSize: integerConfig('JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_RECOVERY_BATCH_SIZE', 200, 1, 2_000),
    accountCircuitRecoveryLeaseDurationMs: integerConfig('JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_RECOVERY_LEASE_DURATION_MS', 180_000, 10_000, 30 * 60_000),
    accountCircuitBackoffMs: integerListConfig(
      'JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_BACKOFF_MS',
      [3_000, 5_000, 10_000, 30_000, 60_000, 120_000, 300_000, 600_000, 900_000],
      100,
      24 * 60 * 60_000
    ),
    accountCircuitRecoverySuccessThreshold: integerConfig('JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_RECOVERY_SUCCESS_THRESHOLD', 3, 1, 100),
    accountCircuitRecoveryCanaryIntervalMs: integerConfig('JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_RECOVERY_CANARY_INTERVAL_MS', 3_000, 100, 10 * 60_000),
    accountCircuitSuspectConfirmationIntervalMs: integerConfig('JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_SUSPECT_CONFIRMATION_INTERVAL_MS', 3_000, 100, 10 * 60_000),
    accountConcurrencyRetryBudgetMs: integerConfig('JUHE_AI_GATEWAY_ACCOUNT_CONCURRENCY_RETRY_BUDGET_MS', 1_200, 0, 60_000),
    accountConcurrencyRetryInitialDelayMs: integerConfig('JUHE_AI_GATEWAY_ACCOUNT_CONCURRENCY_RETRY_INITIAL_DELAY_MS', 120, 1, 60_000),
    accountConcurrencyRetryMaxDelayMs: integerConfig('JUHE_AI_GATEWAY_ACCOUNT_CONCURRENCY_RETRY_MAX_DELAY_MS', 480, 1, 60_000),
    accountApiKeyRequestAttemptSafetyLimit: integerConfig('JUHE_AI_GATEWAY_ACCOUNT_API_KEY_REQUEST_ATTEMPT_SAFETY_LIMIT', globalConcurrencyMax, 1, 50_000),
    dispatchAccountCandidateLimit: integerConfig('JUHE_AI_GATEWAY_DISPATCH_ACCOUNT_CANDIDATE_LIMIT', globalConcurrencyMax, 1, 50_000),
    proxyHealthFailureMaxEntries: integerConfig('JUHE_AI_GATEWAY_PROXY_HEALTH_FAILURE_MAX_ENTRIES', 2_000, 1, 1_000_000),
    proxyHealthFailureWindowMs: integerConfig('JUHE_AI_GATEWAY_PROXY_HEALTH_FAILURE_WINDOW_MS', 60_000, 1_000, 24 * 60 * 60_000),
    proxyHealthAvoidTtlMs: integerConfig('JUHE_AI_GATEWAY_PROXY_HEALTH_AVOID_TTL_MS', 60_000, 1_000, 24 * 60 * 60_000),
    proxyHealthHalfOpenLeaseMs: integerConfig('JUHE_AI_GATEWAY_PROXY_HEALTH_HALF_OPEN_LEASE_MS', 60_000, 1_000, 24 * 60 * 60_000),
    proxyHealthDistinctAccountThreshold: integerConfig('JUHE_AI_GATEWAY_PROXY_HEALTH_DISTINCT_ACCOUNT_THRESHOLD', 2, 1, 1_000),
    proxyHealthCasMaxAttempts: integerConfig('JUHE_AI_GATEWAY_PROXY_HEALTH_CAS_MAX_ATTEMPTS', 1_024, 1, 100_000),
    proxyHealthMaxAccountSamples: integerConfig('JUHE_AI_GATEWAY_PROXY_HEALTH_MAX_ACCOUNT_SAMPLES', 256, 1, 100_000),
    accountSideEffectAvoidanceCacheTtlMs: integerConfig('JUHE_AI_GATEWAY_ACCOUNT_SIDE_EFFECT_AVOIDANCE_CACHE_TTL_MS', 1_000, 0, 60_000),
    accountSideEffectAvoidanceNegativeCacheTtlMs: integerConfig('JUHE_AI_GATEWAY_ACCOUNT_SIDE_EFFECT_AVOIDANCE_NEGATIVE_CACHE_TTL_MS', 500, 0, 60_000),
    accountSideEffectAvoidanceCacheMaxEntries: integerConfig('JUHE_AI_GATEWAY_ACCOUNT_SIDE_EFFECT_AVOIDANCE_CACHE_MAX_ENTRIES', 5_000, 1, 1_000_000),
    accountSideEffectRetryInitialDelayMs: integerConfig('JUHE_AI_GATEWAY_ACCOUNT_SIDE_EFFECT_RETRY_INITIAL_DELAY_MS', 500, 1, 60_000),
    accountSideEffectRetryMaxDelayMs: integerConfig('JUHE_AI_GATEWAY_ACCOUNT_SIDE_EFFECT_RETRY_MAX_DELAY_MS', 30_000, 1, 10 * 60_000),
    accountSideEffectQueueMaxLength: integerConfig('JUHE_AI_GATEWAY_ACCOUNT_SIDE_EFFECT_QUEUE_MAX_LENGTH', 5_000, 1, 1_000_000),
    automaticProbeSweepBatchSize: integerConfig('JUHE_AI_GATEWAY_AUTOMATIC_PROBE_SWEEP_BATCH_SIZE', 25, 1, 1_000),
    automaticProbeSweepIntervalMs: integerConfig('JUHE_AI_GATEWAY_AUTOMATIC_PROBE_SWEEP_INTERVAL_MS', 1_000, 100, 60_000),
    automaticProbeDueRetryDelayMs: integerConfig('JUHE_AI_GATEWAY_AUTOMATIC_PROBE_DUE_RETRY_DELAY_MS', 250, 10, 60_000),
    automaticProbeStateReadBatchSize: integerConfig('JUHE_AI_GATEWAY_AUTOMATIC_PROBE_STATE_READ_BATCH_SIZE', 100, 1, 10_000),
    automaticProbePrecheckMinIntervalMs: integerConfig('JUHE_AI_GATEWAY_AUTOMATIC_PROBE_PRECHECK_MIN_INTERVAL_MS', 60_000, 0, 60 * 60_000),
    automaticProbeConcurrencyDrainPollMs: integerConfig('JUHE_AI_GATEWAY_AUTOMATIC_PROBE_CONCURRENCY_DRAIN_POLL_MS', 1_000, 10, 60_000),
    automaticProbeRecoveryRetryDelayMs: integerConfig('JUHE_AI_GATEWAY_AUTOMATIC_PROBE_RECOVERY_RETRY_DELAY_MS', 10_000, 0, 10 * 60_000),
    automaticProbeRecoveryPrecheckFailureThreshold: integerConfig('JUHE_AI_GATEWAY_AUTOMATIC_PROBE_RECOVERY_PRECHECK_FAILURE_THRESHOLD', 2, 1, 100),
    automaticProbeRecoveryAccountMinIntervalMs: integerConfig('JUHE_AI_GATEWAY_AUTOMATIC_PROBE_RECOVERY_ACCOUNT_MIN_INTERVAL_MS', 3_000, 0, 10 * 60_000),
    automaticProbeRecoveryScopeMinIntervalMs: integerConfig('JUHE_AI_GATEWAY_AUTOMATIC_PROBE_RECOVERY_SCOPE_MIN_INTERVAL_MS', 1_000, 0, 10 * 60_000),
    automaticProbeRecoveryJitterMs: integerConfig('JUHE_AI_GATEWAY_AUTOMATIC_PROBE_RECOVERY_JITTER_MS', 750, 0, 60_000),
    recoverableUnavailableMaxWaitMs: integerConfig('JUHE_AI_GATEWAY_RECOVERABLE_UNAVAILABLE_MAX_WAIT_MS', 30_000, 0, 10 * 60_000),
    recoverableUnavailableCheckIntervalMs: integerConfig('JUHE_AI_GATEWAY_RECOVERABLE_UNAVAILABLE_CHECK_INTERVAL_MS', 5_000, 10, 60_000),
    recoverableUnavailableDueRetryDelayMs: integerConfig('JUHE_AI_GATEWAY_RECOVERABLE_UNAVAILABLE_DUE_RETRY_DELAY_MS', 250, 10, 60_000),
    recoverableUnavailableMaxWaitersPerScope: integerConfig('JUHE_AI_GATEWAY_RECOVERABLE_UNAVAILABLE_MAX_WAITERS_PER_SCOPE', globalConcurrencyMax, 1, 50_000),
    recoverableUnavailableMaxWaitersGlobal: integerConfig('JUHE_AI_GATEWAY_RECOVERABLE_UNAVAILABLE_MAX_WAITERS_GLOBAL', globalConcurrencyMax, 1, 50_000)
  },
  background: {
    accountHealthCheckBatchSize: integerConfig('JUHE_AI_BACKGROUND_ACCOUNT_HEALTH_CHECK_BATCH_SIZE', 20, 1, 1_000),
    cooldownAccountRetestBatchSize: integerConfig('JUHE_AI_BACKGROUND_COOLDOWN_ACCOUNT_RETEST_BATCH_SIZE', 10, 1, 1_000),
    accountApiKeyCooldownRetestBatchSize: integerConfig('JUHE_AI_BACKGROUND_ACCOUNT_API_KEY_COOLDOWN_RETEST_BATCH_SIZE', 10, 1, 1_000),
    accountQualityFailurePrecheckBatchSize: integerConfig('JUHE_AI_BACKGROUND_ACCOUNT_QUALITY_FAILURE_PRECHECK_BATCH_SIZE', 10, 1, 1_000),
    normalRouteSpeedFirstRecoveryProbeBatchSize: integerConfig('JUHE_AI_BACKGROUND_NORMAL_ROUTE_SPEED_FIRST_RECOVERY_PROBE_BATCH_SIZE', 10, 1, 1_000),
    accountBalanceAutoDetectionRecoveryBatchSize: integerConfig('JUHE_AI_BACKGROUND_ACCOUNT_BALANCE_AUTO_DETECTION_RECOVERY_BATCH_SIZE', 2, 1, 1_000),
    accountBalanceAutoDetectionBackfillPageSize: integerConfig('JUHE_AI_BACKGROUND_ACCOUNT_BALANCE_AUTO_DETECTION_BACKFILL_PAGE_SIZE', 50, 1, 10_000),
    accountBalanceRefreshBatchSize: integerConfig('JUHE_AI_BACKGROUND_ACCOUNT_BALANCE_REFRESH_BATCH_SIZE', 36, 1, 1_000),
    accountBalanceRefreshRecoveryBatchSize: integerConfig('JUHE_AI_BACKGROUND_ACCOUNT_BALANCE_REFRESH_RECOVERY_BATCH_SIZE', 4, 1, 1_000),
    auditLogPostgresFlushBatchSize: integerConfig('JUHE_AI_BACKGROUND_AUDIT_LOG_POSTGRES_FLUSH_BATCH_SIZE', 25, 1, 1_000),
    auditLogPostgresRedisConsumerConcurrency: integerConfig('JUHE_AI_BACKGROUND_AUDIT_LOG_POSTGRES_REDIS_CONSUMER_CONCURRENCY', 20, 1, 1_000),
    auditPayloadBlobWriteConcurrency: integerConfig('JUHE_AI_BACKGROUND_AUDIT_PAYLOAD_BLOB_WRITE_CONCURRENCY', 10, 1, 1_000),
    auditBlobCleanupDeleteConcurrency: integerConfig('JUHE_AI_BACKGROUND_AUDIT_BLOB_CLEANUP_DELETE_CONCURRENCY', 5, 1, 1_000),
    modelCheckTokenWorkerTargetSize: integerConfig('JUHE_AI_BACKGROUND_MODEL_CHECK_TOKEN_WORKER_TARGET_SIZE', 5, 1, 64),
    modelCheckTokenWorkerQueueMaxItems: integerConfig('JUHE_AI_BACKGROUND_MODEL_CHECK_TOKEN_WORKER_QUEUE_MAX_ITEMS', 16, 1, 100_000),
    diagnosticTaskMaxInFlight: integerConfig('JUHE_AI_BACKGROUND_DIAGNOSTIC_TASK_MAX_IN_FLIGHT', 5, 1, 1_000),
    accountTestRefillMaxBatchSize: integerConfig('JUHE_AI_BACKGROUND_ACCOUNT_TEST_REFILL_MAX_BATCH_SIZE', 1_000, 1, 100_000),
    accountTestQueuedSweepBatchSize: integerConfig('JUHE_AI_BACKGROUND_ACCOUNT_TEST_QUEUED_SWEEP_BATCH_SIZE', 500, 1, 100_000),
    accountTestQueuedMaxWaitMs: integerConfig('JUHE_AI_BACKGROUND_ACCOUNT_TEST_QUEUED_MAX_WAIT_MS', 10 * 60_000, 1_000, 24 * 60 * 60_000),
    accountApiKeyProbeCandidateScanLimit: integerConfig('JUHE_AI_BACKGROUND_ACCOUNT_API_KEY_PROBE_CANDIDATE_SCAN_LIMIT', 10_000, 1, 1_000_000),
    accountBalanceRecoveryMaxScanPages: integerConfig('JUHE_AI_BACKGROUND_ACCOUNT_BALANCE_RECOVERY_MAX_SCAN_PAGES', 4, 1, 1_000),
    accountAvailabilityScheduleSyncBatchLimit: integerConfig('JUHE_AI_BACKGROUND_ACCOUNT_AVAILABILITY_SCHEDULE_SYNC_BATCH_LIMIT', 500, 1, 100_000),
    apiKeyScheduleSyncBatchLimit: integerConfig('JUHE_AI_BACKGROUND_API_KEY_SCHEDULE_SYNC_BATCH_LIMIT', 500, 1, 100_000),
    accountRuntimeStatusHydrationBatchSize: integerConfig('JUHE_AI_BACKGROUND_ACCOUNT_RUNTIME_STATUS_HYDRATION_BATCH_SIZE', 100, 1, 100_000),
    proxyLatencyRefreshConcurrency: integerConfig('JUHE_AI_BACKGROUND_PROXY_LATENCY_REFRESH_CONCURRENCY', defaultBackgroundConcurrency, 1, 1_000),
    proxyLatencyRefreshBatchSize: integerConfig('JUHE_AI_BACKGROUND_PROXY_LATENCY_REFRESH_BATCH_SIZE', 20, 1, 1_000),
    proxyProbeTimeoutMs: integerConfig('JUHE_AI_BACKGROUND_PROXY_PROBE_TIMEOUT_MS', 15_000, 1_000, 5 * 60_000),
    proxyManualTestDeadlineMs: integerConfig('JUHE_AI_BACKGROUND_PROXY_MANUAL_TEST_DEADLINE_MS', 25_000, 1_000, 10 * 60_000),
    proxyLatencyRefreshIntervalSeconds: integerConfig('JUHE_AI_BACKGROUND_PROXY_LATENCY_REFRESH_INTERVAL_SECONDS', 60, 5, 24 * 60 * 60),
    proxyLatencyRefreshRunBudgetMs: integerConfig('JUHE_AI_BACKGROUND_PROXY_LATENCY_REFRESH_RUN_BUDGET_MS', 45_000, 1_000, 10 * 60_000),
    proxyLatencyRefreshCandidateDeadlineMs: integerConfig('JUHE_AI_BACKGROUND_PROXY_LATENCY_REFRESH_CANDIDATE_DEADLINE_MS', 25_000, 1_000, 60_000),
    proxyLatencyRefreshCandidatePoolFactor: integerConfig('JUHE_AI_BACKGROUND_PROXY_LATENCY_REFRESH_CANDIDATE_POOL_FACTOR', 4, 1, 100),
    proxyLatencyRefreshLeaseGraceMs: integerConfig('JUHE_AI_BACKGROUND_PROXY_LATENCY_REFRESH_LEASE_GRACE_MS', 5_000, 0, 5 * 60_000),
    accountProbeDbServiceTimeoutMs: integerConfig('JUHE_AI_BACKGROUND_ACCOUNT_PROBE_DB_SERVICE_TIMEOUT_MS', 30_000, 1_000, 5 * 60_000),
    accountHealthCheckProbeDeadlineMs: integerConfig('JUHE_AI_BACKGROUND_ACCOUNT_HEALTH_CHECK_PROBE_DEADLINE_MS', 65_000, 1_000, 10 * 60_000),
    cooldownAccountRetestStartupDelayMs: integerConfig('JUHE_AI_BACKGROUND_COOLDOWN_ACCOUNT_RETEST_STARTUP_DELAY_MS', 60_000, 0, 10 * 60_000),
    accountApiKeyCooldownRetestStartupDelayMs: integerConfig('JUHE_AI_BACKGROUND_ACCOUNT_API_KEY_COOLDOWN_RETEST_STARTUP_DELAY_MS', 65_000, 0, 10 * 60_000),
    normalRouteSpeedFirstProbeStartupDelayMs: integerConfig('JUHE_AI_BACKGROUND_NORMAL_ROUTE_SPEED_FIRST_PROBE_STARTUP_DELAY_MS', 75_000, 0, 10 * 60_000),
    modelQualityScheduledCheckBatchSize: integerConfig('JUHE_AI_BACKGROUND_MODEL_QUALITY_SCHEDULED_CHECK_BATCH_SIZE', 3, 1, 1_000),
    modelQualityHealthSyncRetryBatchSize: integerConfig('JUHE_AI_BACKGROUND_MODEL_QUALITY_HEALTH_SYNC_RETRY_BATCH_SIZE', 20, 1, 1_000),
    taskRunReconcileBatchSize: integerConfig('JUHE_AI_BACKGROUND_TASK_RUN_RECONCILE_BATCH_SIZE', 500, 1, 10_000),
    modelTrustObservationAggregationBatchSize: integerConfig('JUHE_AI_BACKGROUND_MODEL_TRUST_OBSERVATION_AGGREGATION_BATCH_SIZE', 100, 1, 10_000),
    auditHotRetentionCleanupBatchSize: integerConfig('JUHE_AI_BACKGROUND_AUDIT_HOT_RETENTION_CLEANUP_BATCH_SIZE', 100, 1, 10_000),
    auditHotRetentionCleanupMaxBatches: integerConfig('JUHE_AI_BACKGROUND_AUDIT_HOT_RETENTION_CLEANUP_MAX_BATCHES', 1, 1, 1_000),
    auditHotRetentionCleanupMaxRunMs: integerConfig('JUHE_AI_BACKGROUND_AUDIT_HOT_RETENTION_CLEANUP_MAX_RUN_MS', 3_000, 100, 300_000),
    operationLogBatchSize: integerConfig('JUHE_AI_BACKGROUND_OPERATION_LOG_BATCH_SIZE', 200, 1, 10_000),
    operationLogShutdownFlushMaxBatches: integerConfig('JUHE_AI_BACKGROUND_OPERATION_LOG_SHUTDOWN_FLUSH_MAX_BATCHES', 100, 1, 10_000),
    operationLogQueueMaxItems: integerConfig('JUHE_AI_BACKGROUND_OPERATION_LOG_QUEUE_MAX_ITEMS', 5_000, 1, 1_000_000),
    operationLogQueueMaxMb: integerConfig('JUHE_AI_BACKGROUND_OPERATION_LOG_QUEUE_MAX_MB', 32, 1, 4_096),
    auditLogTransportMaxQueuedJobs: integerConfig('JUHE_AI_BACKGROUND_AUDIT_LOG_TRANSPORT_MAX_QUEUED_JOBS', 256, 1, 100_000),
    auditLogTransportMaxTotalMb: integerConfig('JUHE_AI_BACKGROUND_AUDIT_LOG_TRANSPORT_MAX_TOTAL_MB', 128, 1, 4_096),
    auditLogTransportMaxActiveMb: integerConfig('JUHE_AI_BACKGROUND_AUDIT_LOG_TRANSPORT_MAX_ACTIVE_MB', 72, 1, 4_096),
    auditLogTransportMaxJobMb: integerConfig('JUHE_AI_BACKGROUND_AUDIT_LOG_TRANSPORT_MAX_JOB_MB', 64, 1, 4_096),
    auditLogFlushBatchMaxMb: integerConfig('JUHE_AI_BACKGROUND_AUDIT_LOG_FLUSH_BATCH_MAX_MB', 8, 1, 4_096),
    auditLogScheduledFlushMaxBatches: integerConfig('JUHE_AI_BACKGROUND_AUDIT_LOG_SCHEDULED_FLUSH_MAX_BATCHES', 20, 1, 10_000),
    auditLogShutdownFlushMaxBatches: integerConfig('JUHE_AI_BACKGROUND_AUDIT_LOG_SHUTDOWN_FLUSH_MAX_BATCHES', 100, 1, 10_000),
    auditLogRedisStreamMaxItems: integerConfig('JUHE_AI_BACKGROUND_AUDIT_LOG_REDIS_STREAM_MAX_ITEMS', 50_000, 1, 5_000_000),
    auditLogRedisStreamMaxMb: integerConfig('JUHE_AI_BACKGROUND_AUDIT_LOG_REDIS_STREAM_MAX_MB', 256, 1, 16_384),
    ipcUsageRecordQueueMaxMessages: integerConfig('JUHE_AI_BACKGROUND_IPC_USAGE_RECORD_QUEUE_MAX_MESSAGES', 10_000, 1, 1_000_000),
    ipcUsageRecordQueueMaxMb: integerConfig('JUHE_AI_BACKGROUND_IPC_USAGE_RECORD_QUEUE_MAX_MB', 64, 1, 4_096),
    ipcRegularWorkerQueueMaxMessages: integerConfig('JUHE_AI_BACKGROUND_IPC_REGULAR_WORKER_QUEUE_MAX_MESSAGES', 5_000, 1, 1_000_000),
    ipcRegularWorkerQueueMaxMb: integerConfig('JUHE_AI_BACKGROUND_IPC_REGULAR_WORKER_QUEUE_MAX_MB', 64, 1, 4_096),
    ipcPendingDbServiceRequestMaxCount: integerConfig('JUHE_AI_BACKGROUND_IPC_PENDING_DB_SERVICE_REQUEST_MAX_COUNT', 1_000, 1, 1_000_000),
    recordMaintenanceBatchSize: integerConfig('JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_BATCH_SIZE', 10, 1, 10_000),
    recordMaintenanceShutdownFlushMaxBatches: integerConfig('JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_SHUTDOWN_FLUSH_MAX_BATCHES', 1, 1, 10_000),
    recordMaintenanceQueueMaxItems: integerConfig('JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_QUEUE_MAX_ITEMS', 5_000, 1, 1_000_000),
    recordMaintenanceQueueMaxMb: integerConfig('JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_QUEUE_MAX_MB', 32, 1, 4_096),
    recordMaintenanceAuditCleanupBatchSize: integerConfig('JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_AUDIT_CLEANUP_BATCH_SIZE', 100, 1, 10_000),
    recordMaintenanceAuditCleanupMaxBatches: integerConfig('JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_AUDIT_CLEANUP_MAX_BATCHES', 3, 1, 10_000),
    usageRecordBatchSize: integerConfig('JUHE_AI_BACKGROUND_USAGE_RECORD_BATCH_SIZE', 1_000, 1, 100_000),
    usageRecordFlushBatchMaxMb: integerConfig('JUHE_AI_BACKGROUND_USAGE_RECORD_FLUSH_BATCH_MAX_MB', 8, 1, 4_096),
    usageRecordShutdownFlushMaxBatches: integerConfig('JUHE_AI_BACKGROUND_USAGE_RECORD_SHUTDOWN_FLUSH_MAX_BATCHES', 100, 1, 10_000),
    usageRecordQueueMaxItems: integerConfig('JUHE_AI_BACKGROUND_USAGE_RECORD_QUEUE_MAX_ITEMS', 10_000, 1, 1_000_000),
    usageRecordQueueMaxMb: integerConfig('JUHE_AI_BACKGROUND_USAGE_RECORD_QUEUE_MAX_MB', 64, 1, 4_096)
  },
  modelCheck: {
    probeRetryDelayMs: numberConfig('JUHE_AI_MODEL_CHECK_PROBE_RETRY_DELAY_MS', defaultModelCheckProbeRetryDelayMs, 0, 300000)
  },
  auditLog: auditLogRuntimeConfig(),
  codexWebSearch: {
    endpoint: optionalStringConfig('JUHE_AI_CODEX_WEB_SEARCH_ENDPOINT'),
    apiKey: optionalStringConfig('JUHE_AI_CODEX_WEB_SEARCH_API_KEY'),
    timeoutMs: numberConfig('JUHE_AI_CODEX_WEB_SEARCH_TIMEOUT_MS', 10000, 1000, 120000),
    maxResults: numberConfig('JUHE_AI_CODEX_WEB_SEARCH_MAX_RESULTS', 5, 1, 10),
    maxBodyBytes: numberConfig('JUHE_AI_CODEX_WEB_SEARCH_MAX_BODY_KB', 512, 16, 4096) * 1024
  },
  codeInterpreter: {
    pythonCommand: stringConfig('JUHE_AI_CODE_INTERPRETER_PYTHON_COMMAND', 'python'),
    timeoutMs: numberConfig('JUHE_AI_CODE_INTERPRETER_TIMEOUT_MS', 5000, 100, 120000),
    maxCodeBytes: numberConfig('JUHE_AI_CODE_INTERPRETER_MAX_CODE_KB', 64, 1, 1024) * 1024,
    maxOutputBytes: numberConfig('JUHE_AI_CODE_INTERPRETER_MAX_OUTPUT_KB', 64, 4, 1024) * 1024,
    maxArtifactCount: numberConfig('JUHE_AI_CODE_INTERPRETER_MAX_ARTIFACTS', 8, 0, 128),
    maxArtifactBytes: numberConfig('JUHE_AI_CODE_INTERPRETER_MAX_ARTIFACT_KB', 256, 1, 10240) * 1024,
    tempRoot: pathConfig('JUHE_AI_CODE_INTERPRETER_TEMP_ROOT', defaultCodeInterpreterTempRoot),
    cleanupTempDirectory: booleanConfig('JUHE_AI_CODE_INTERPRETER_CLEANUP_TEMP_DIR', true)
  },
  computerAdapter: computerAdapterConfig(),
  hostedToolRuntimes: {
    codeInterpreter: hostedToolRuntimeModeConfig('JUHE_AI_HOSTED_TOOL_CODE_INTERPRETER_MODE', 'guidance'),
    computer: hostedToolRuntimeModeConfig('JUHE_AI_HOSTED_TOOL_COMPUTER_MODE', 'guidance'),
    shell: hostedToolRuntimeModeConfig('JUHE_AI_HOSTED_TOOL_SHELL_MODE', 'guidance'),
    skills: hostedToolRuntimeModeConfig('JUHE_AI_HOSTED_TOOL_SKILLS_MODE', 'guidance'),
    toolSearch: hostedToolRuntimeModeConfig('JUHE_AI_HOSTED_TOOL_TOOL_SEARCH_MODE', 'guidance')
  },
  log: {
    level: logLevelConfig('JUHE_AI_LOG_LEVEL', 'info'),
    directory: pathConfig('JUHE_AI_LOG_DIR', resolve(backendRoot, 'logs')),
    fileEnabled: configuredLogFileEnabled,
    consoleEnabled: booleanConfig('JUHE_AI_LOG_CONSOLE_ENABLED', true),
    maxFileBytes: numberConfig('JUHE_AI_LOG_MAX_FILE_MB', 100, 1, 1024) * 1024 * 1024,
    retentionDays: numberConfig('JUHE_AI_LOG_RETENTION_DAYS', 30, 1, 30),
    maxFiles: numberConfig('JUHE_AI_LOG_MAX_FILES', 500, 1, 500),
    cleanupIntervalMinutes: numberConfig('JUHE_AI_LOG_CLEANUP_INTERVAL_MINUTES', 60, 1, 1440),
    gatewayTimingDetailSamplePermille: numberConfig(
      'JUHE_AI_GATEWAY_TIMING_DETAIL_SAMPLE_PERMILLE',
      configuredRuntimeMode === 'performance' ? 50 : 1000,
      0,
      1000
    ),
    gatewayStagePressureMaxPendingBytes: numberConfig(
      'JUHE_AI_GATEWAY_STAGE_PRESSURE_MAX_PENDING_MB',
      8,
      1,
      1024
    ) * 1024 * 1024
  },
  smokeTest: {
    backendUrl: stringConfig('JUHE_AI_BACKEND_URL', 'http://127.0.0.1:3000'),
    adminUsername: stringConfig('JUHE_AI_SMOKE_ADMIN_USERNAME', 'admin'),
    adminPassword: stringConfig('JUHE_AI_SMOKE_ADMIN_PASSWORD', 'admin'),
    accountName: stringConfig('JUHE_AI_SMOKE_ACCOUNT_NAME', ''),
    model: stringConfig('JUHE_AI_SMOKE_MODEL', 'gpt-5.4-mini'),
    prompt: stringConfig('JUHE_AI_SMOKE_PROMPT', '只输出 OK')
  }
}

function envFilePathConfig(value: string | undefined): string | undefined {
  const trimmedValue = value?.trim()
  if (!trimmedValue) return undefined
  return isAbsolute(trimmedValue) ? trimmedValue : resolve(backendRoot, trimmedValue)
}

function stringConfig(name: string, fallback: string): string {
  const value = rawStringConfig(name)
  return value ? value : fallback
}

function rawStringConfig(name: string): string | undefined {
  return process.env[name]?.trim() ?? localEnvOverlay[name]?.trim() ?? localCapacityEnv[name]?.trim() ?? localEnv[name]?.trim()
}

function isCapacityEnvironmentVariable(name: string): boolean {
  return name.startsWith('JUHE_AI_CONCURRENCY_')
    || name.startsWith('JUHE_AI_ACCOUNT_')
    || name.startsWith('JUHE_AI_BACKGROUND_')
    || name.startsWith('JUHE_AI_GATEWAY_')
    || name.startsWith('JUHE_AI_DB_')
    || name.startsWith('JUHE_AI_CHAT_DB_SERVICE_')
    || name.startsWith('JUHE_AI_REDIS_STREAM_')
    || name.startsWith('JUHE_AI_USAGE_SPOOL_')
    || name === 'JUHE_AI_SYSTEM_API_DB_SERVICE_MAX_IN_FLIGHT'
    || /^JUHE_AI_(GATEWAY|USAGE|LOG|STATS|OPS)_WORKER_REPLICAS$/.test(name)
}

function hasAnyRawConfig(names: string[]): boolean {
  return names.some((name) => Boolean(rawStringConfig(name)))
}

function secretConfig(name: string, fallback: string): string {
  const value = stringConfig(name, fallback)
  assertProductionSecret(name, value)
  return value
}

function assertProductionSecret(name: string, value: string): void {
  if (!isProductionRuntime()) return
  if (value === defaultRuntimeSecret || value.length < minimumProductionSecretLength) {
    throw new Error(`${name} 在生产环境必须配置为至少 ${minimumProductionSecretLength} 位的稳定随机密钥，不能使用默认开发密钥或过短密钥`)
  }
}

function authRuntimeConfig(): RuntimeConfig['auth'] {
  return {
    captchaDisabled: booleanConfig('JUHE_AI_AUTH_CAPTCHA_DISABLED', false)
  }
}

export function isProductionRuntime(): boolean {
  return (rawStringConfig('NODE_ENV') ?? '').toLowerCase() === 'production'
}

function optionalStringConfig(name: string): string | undefined {
  const value = rawStringConfig(name)
  return value ? value : undefined
}

function booleanConfig(name: string, fallback: boolean): boolean {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return fallback
  if (['1', 'true', 'yes', 'on'].includes(value)) return true
  if (['0', 'false', 'no', 'off'].includes(value)) return false
  throw new Error(`${name} 只能配置为 true/false/1/0/yes/no/on/off`)
}

function strictDeploymentBooleanConfig(name: string, fallback: boolean): boolean {
  const configuredValue = rawStringConfig(name)
  if (configuredValue === undefined) return fallback
  const value = configuredValue.toLowerCase()
  if (['1', 'true', 'yes', 'on'].includes(value)) return true
  if (['0', 'false', 'no', 'off'].includes(value)) return false
  throw new Error(`${name} 只能配置为 true/false/1/0/yes/no/on/off`)
}

export function parseOwnerLockEnabled(value: string | undefined): boolean {
  return value?.trim().toLowerCase() === 'true'
}

function runtimeModeConfig(name: string, fallback: RuntimeMode): RuntimeMode {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return fallback
  if (value === 'standalone' || value === 'performance') return value
  throw new Error(`${name} 只能配置为 standalone 或 performance`)
}

function performanceNodeRoleConfig(name: string, fallback: PerformanceNodeRole): PerformanceNodeRole {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return fallback
  if (value === 'combined' || value === 'gateway' || value === 'control') return value
  throw new Error(`${name} 只能配置为 combined、gateway 或 control`)
}

function runtimeInstanceIdConfig(name: string, runtimeMode: RuntimeMode): string {
  const configured = rawStringConfig(name)?.trim()
  if (!configured) {
    if (runtimeMode === 'performance' && rawStringConfig('NODE_ENV')?.toLowerCase() === 'production') {
      throw new Error(`${name} 在 production performance 模式下必须显式配置为跨重启稳定且进程唯一的值`)
    }
    return `process-${process.pid}`
  }
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(configured)) {
    throw new Error(`${name} 必须以字母或数字开头，且只能包含字母、数字、点、下划线或连字符，最长 64 字符`)
  }
  return configured
}

function databaseDriverConfig(name: string, fallback: DatabaseDriver): DatabaseDriver {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return fallback
  if (value === 'sqlite' || value === 'postgres') return value
  throw new Error(`${name} 只能配置为 sqlite 或 postgres`)
}

function cacheDriverConfig(name: string, fallback: CacheDriver): CacheDriver {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return fallback
  if (value === 'memory' || value === 'redis') return value
  throw new Error(`${name} 只能配置为 memory 或 redis`)
}

function runtimeStateDriverConfig(name: string, fallback: RuntimeStateDriver): RuntimeStateDriver {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return fallback
  if (value === 'memory' || value === 'redis') return value
  throw new Error(`${name} 只能配置为 memory 或 redis`)
}

function queueDriverConfig(name: string, fallback: QueueDriver): QueueDriver {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return fallback
  if (value === 'memory' || value === 'redis_stream') return value
  throw new Error(`${name} 只能配置为 memory 或 redis_stream`)
}

function redisNamespaceConfig(name: string, secret: string): string {
  const rawValue = rawStringConfig(name)
  const value = rawValue?.trim() || `env-${createHash('sha256').update(secret).digest('hex').slice(0, 12)}`
  const normalized = value.replace(/[^a-zA-Z0-9_.:-]+/g, '_').replace(/^_+|_+$/g, '')
  if (!normalized) {
    throw new Error(`${name} 不能为空`)
  }
  if (normalized.length > 64) {
    throw new Error(`${name} 最多 64 个字符`)
  }
  return normalized
}

function assertRuntimeModeDrivers(config: {
  runtimeMode: RuntimeMode
  databaseDriver: DatabaseDriver
  cacheDriver: CacheDriver
  runtimeStateDriver: RuntimeStateDriver
  queueDriver: QueueDriver
  postgresUrl?: string
  redisCacheUrl?: string
  redisStateUrl?: string
  redisQueueUrl?: string
}): void {
  if (config.runtimeMode === 'standalone') {
    if (config.databaseDriver !== 'sqlite') {
      throw new Error('JUHE_AI_RUNTIME_MODE=standalone 时 JUHE_AI_DATABASE_DRIVER 必须为 sqlite')
    }
    if (config.cacheDriver !== 'memory') {
      throw new Error('JUHE_AI_RUNTIME_MODE=standalone 时 JUHE_AI_CACHE_DRIVER 必须为 memory')
    }
    if (config.runtimeStateDriver !== 'memory') {
      throw new Error('JUHE_AI_RUNTIME_MODE=standalone 时 JUHE_AI_RUNTIME_STATE_DRIVER 必须为 memory')
    }
    if (config.queueDriver !== 'memory') {
      throw new Error('JUHE_AI_RUNTIME_MODE=standalone 时 JUHE_AI_QUEUE_DRIVER 必须为 memory')
    }
    return
  }

  if (config.databaseDriver !== 'postgres') {
    throw new Error('JUHE_AI_RUNTIME_MODE=performance 时 JUHE_AI_DATABASE_DRIVER 必须为 postgres')
  }
  if (config.cacheDriver !== 'redis') {
    throw new Error('JUHE_AI_RUNTIME_MODE=performance 时 JUHE_AI_CACHE_DRIVER 必须为 redis')
  }
  if (config.runtimeStateDriver !== 'redis') {
    throw new Error('JUHE_AI_RUNTIME_MODE=performance 时 JUHE_AI_RUNTIME_STATE_DRIVER 必须为 redis')
  }
  if (config.queueDriver !== 'redis_stream') {
    throw new Error('JUHE_AI_RUNTIME_MODE=performance 时 JUHE_AI_QUEUE_DRIVER 必须为 redis_stream')
  }
  assertUrlConfig('JUHE_AI_POSTGRES_URL', config.postgresUrl, ['postgres:', 'postgresql:'])
  assertUrlConfig('JUHE_AI_REDIS_CACHE_URL', config.redisCacheUrl, ['redis:', 'rediss:'])
  assertUrlConfig('JUHE_AI_REDIS_STATE_URL', config.redisStateUrl, ['redis:', 'rediss:'])
  assertUrlConfig('JUHE_AI_REDIS_QUEUE_URL', config.redisQueueUrl, ['redis:', 'rediss:'])
  assertCanonicalProductionRedisUrls([
    ['JUHE_AI_REDIS_CACHE_URL', config.redisCacheUrl],
    ['JUHE_AI_REDIS_STATE_URL', config.redisStateUrl],
    ['JUHE_AI_REDIS_QUEUE_URL', config.redisQueueUrl]
  ])
}

function accountHealthCheckDispatchUrlConfig(
  name: string,
  configuredValue: string | undefined,
  runtime: {
    runtimeMode: RuntimeMode
    performanceNodeRole: PerformanceNodeRole
    processRole: ProcessRole
  }
): string | undefined {
  const required = runtime.runtimeMode === 'performance'
    && runtime.performanceNodeRole === 'gateway'
    && runtime.processRole === 'server'
  if (!configuredValue) {
    if (required) {
      throw new Error(`${name} 在 performance gateway server 模式下必须配置为 control 的 loopback Origin`)
    }
    return undefined
  }
  let url: URL
  try {
    url = new URL(configuredValue)
  } catch {
    throw new Error(`${name} 必须是有效 URL`)
  }
  if (
    url.protocol !== 'http:'
    || (url.hostname !== '127.0.0.1' && url.hostname !== '[::1]' && url.hostname !== '::1')
    || !url.port
    || url.username
    || url.password
    || (url.pathname !== '/' && url.pathname !== '')
    || url.search
    || url.hash
  ) {
    throw new Error(`${name} 只能配置为带显式端口的 loopback HTTP Origin，不能包含路径、用户名密码、查询参数或片段`)
  }
  return url.origin
}

function assertUrlConfig(name: string, value: string | undefined, protocols: string[]): void {
  if (!value) {
    throw new Error(`${name} 在高性能模式下必须配置`)
  }
  let url: URL
  try {
    url = new URL(value)
  } catch {
    throw new Error(`${name} 必须是有效 URL`)
  }
  if (!protocols.includes(url.protocol)) {
    throw new Error(`${name} 只允许协议：${protocols.join(', ')}`)
  }
}

function assertCanonicalProductionRedisUrls(values: Array<[string, string | undefined]>): void {
  const seen = new Map<string, string>()
  for (const [name, value] of values) {
    if (!value) continue
    const url = parseRedisUrl(value)
    if (url.hostname === 'localhost' || url.hostname === '::1' || url.hostname === '[::1]') {
      throw new Error(`${name} 在高性能模式下不能使用 localhost 或 ::1 loopback 别名，请使用 canonical 127.0.0.1`)
    }
    const key = redisUrlResourceKey(value)
    const existing = seen.get(key)
    if (existing) {
      throw new Error(`${name} 不能与 ${existing} 指向同一个 Redis 进程；cache/state/queue 必须使用不同 host:port`)
    }
    seen.set(key, name)
  }
}

function redisUrlResourceKey(value: string): string {
  const url = parseRedisUrl(value)
  const port = url.port || '6379'
  return `${url.hostname}:${port}`
}

function parseRedisUrl(value: string): URL {
  try {
    return new URL(value)
  } catch {
    throw new Error('Redis URL 必须是有效 URL')
  }
}

function logLevelConfig(name: string, fallback: LogLevel): LogLevel {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return fallback
  if (isLogLevel(value)) return value
  throw new Error(`${name} 只能配置为 trace/debug/info/warn/error/fatal/silent`)
}

function processRoleConfig(name: string, fallback: ProcessRole): ProcessRole {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return fallback
  if (value === 'server') return 'server'
  if (value === 'worker') return 'worker'
  if (value === 'db-service') return 'db-service'
  throw new Error(`${name} 只能配置为 server、worker 或 db-service`)
}

function workerRoleConfig(name: string, fallback: WorkerRuntimeRole): WorkerRuntimeRole {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return fallback
  if (value === 'worker') return 'worker'
  if (value === 'ingest-worker') return 'ingest-worker'
  if (value === 'usage-worker') return 'usage-worker'
  if (value === 'log-worker') return 'log-worker'
  if (value === 'stats-worker') return 'stats-worker'
  if (value === 'ops-worker') return 'ops-worker'
  if (value === 'temporary-maintenance-worker') return 'temporary-maintenance-worker'
  throw new Error(`${name} 只能配置为 worker、ingest-worker、usage-worker、log-worker、stats-worker、ops-worker 或 temporary-maintenance-worker`)
}

function numberConfig(name: string, fallback: number, min: number, max: number): number {
  const rawValue = rawStringConfig(name)
  if (!rawValue) return fallback
  const value = Number(rawValue)
  if (!Number.isFinite(value)) {
    throw new Error(`${name} 必须配置为数字`)
  }
  const integerValue = Math.trunc(value)
  if (integerValue < min || integerValue > max) {
    throw new Error(`${name} 必须在 ${min}-${max} 范围内`)
  }
  return integerValue
}

function integerConfig(name: string, fallback: number, min: number, max: number): number {
  const rawValue = rawStringConfig(name)
  if (!rawValue) return fallback
  const value = Number(rawValue)
  if (!Number.isInteger(value)) throw new Error(`${name} 必须配置为整数`)
  if (value < min || value > max) throw new Error(`${name} 必须在 ${min}-${max} 范围内`)
  return value
}

function integerListConfig(name: string, fallback: number[], min: number, max: number): number[] {
  const rawValue = rawStringConfig(name)
  if (!rawValue) return [...fallback]
  const parts = rawValue.split(',').map((part) => part.trim()).filter(Boolean)
  if (parts.length === 0) throw new Error(`${name} 必须配置为逗号分隔的整数列表`)
  return parts.map((part) => {
    const value = Number(part)
    if (!Number.isInteger(value) || value < min || value > max) {
      throw new Error(`${name} 的每一项必须在 ${min}-${max} 范围内`)
    }
    return value
  })
}

function optionalIntegerConfig(name: string, min: number, max: number): number | undefined {
  const rawValue = rawStringConfig(name)
  if (!rawValue) return undefined
  const value = Number(rawValue)
  if (!Number.isInteger(value)) throw new Error(`${name} 必须配置为整数`)
  if (value < min || value > max) throw new Error(`${name} 必须在 ${min}-${max} 范围内`)
  return value
}

export function parseAuditLogRuntimeConfig(values: Record<string, string | undefined>): RuntimeConfig['auditLog'] {
  const read = (name: string): string | undefined => values[name]?.trim()
  const strictBooleanValue = (name: string, fallback: boolean): boolean => {
    const configuredValue = read(name)
    if (configuredValue === undefined) return fallback
    const raw = configuredValue.toLowerCase()
    if (['true', '1', 'yes', 'on'].includes(raw)) return true
    if (['false', '0', 'no', 'off'].includes(raw)) return false
    throw new Error(`${name} 必须配置为 true/false、1/0、yes/no 或 on/off`)
  }
  const successHotRetentionHours = strictIntegerValue(read, 'JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS', 1, 0, 168)
  const successSampleRate = strictDecimalValue(read, 'JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE', 0.1, 0, 1, 4)
  const successRetentionDays = strictIntegerValue(read, 'JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS', 3, 0, 3650)
  if ((successSampleRate === 0) !== (successRetentionDays === 0)) {
    throw new Error('JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE 和 JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS 必须同时为 0 或同时大于 0')
  }
  if (successRetentionDays > 0 && successRetentionDays * 24 < successHotRetentionHours) {
    throw new Error('JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS 必须覆盖 JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS')
  }
  return {
    enabled: strictBooleanValue('JUHE_AI_AUDIT_LOG_ENABLED', true),
    successHotRetentionHours,
    successSampleRate,
    successRetentionDays,
    problemRetentionDays: strictIntegerValue(read, 'JUHE_AI_AUDIT_LOG_PROBLEM_RETENTION_DAYS', 7, 1, 3650),
    successFullBodyLimitBytes: strictIntegerValue(read, 'JUHE_AI_AUDIT_LOG_SUCCESS_FULL_BODY_LIMIT_KB', 512, 0, 512) * 1024,
    problemFullBodyLimitBytes: strictIntegerValue(read, 'JUHE_AI_AUDIT_LOG_PROBLEM_FULL_BODY_LIMIT_KB', 2048, 0, 2048) * 1024
  }
}

function auditLogRuntimeConfig(): RuntimeConfig['auditLog'] {
  const names = [
    'JUHE_AI_AUDIT_LOG_ENABLED',
    'JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS',
    'JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE',
    'JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS',
    'JUHE_AI_AUDIT_LOG_PROBLEM_RETENTION_DAYS',
    'JUHE_AI_AUDIT_LOG_SUCCESS_FULL_BODY_LIMIT_KB',
    'JUHE_AI_AUDIT_LOG_PROBLEM_FULL_BODY_LIMIT_KB'
  ]
  return parseAuditLogRuntimeConfig(Object.fromEntries(names.map((name) => [name, rawStringConfig(name)])))
}

function strictIntegerValue(
  read: (name: string) => string | undefined,
  name: string,
  fallback: number,
  min: number,
  max: number
): number {
  const raw = read(name)
  if (!raw) return fallback
  if (!/^\d+$/.test(raw)) throw new Error(`${name} 必须是 ${min} 到 ${max} 之间的整数`)
  const value = Number(raw)
  if (!Number.isSafeInteger(value) || value < min || value > max) {
    throw new Error(`${name} 必须是 ${min} 到 ${max} 之间的整数`)
  }
  return value
}

function strictDecimalValue(
  read: (name: string) => string | undefined,
  name: string,
  fallback: number,
  min: number,
  max: number,
  maxDecimals: number
): number {
  const raw = read(name)
  if (!raw) return fallback
  const message = `${name} 必须是 ${min} 到 ${max} 之间且最多 ${maxDecimals} 位小数的数字`
  if (!/^(?:\d+|\d*\.\d+)$/.test(raw) || (raw.split('.')[1]?.length ?? 0) > maxDecimals) throw new Error(message)
  const value = Number(raw)
  if (!Number.isFinite(value) || value < min || value > max) throw new Error(message)
  return value
}

function pathConfig(name: string, fallback: string): string {
  const value = stringConfig(name, fallback)
  return isAbsolute(value) ? value : resolve(backendRoot, value)
}

function isScriptEntryRuntime(): boolean {
  const entry = process.argv[1]
  if (!entry) return false
  const normalized = entry.replace(/\\/g, '/').toLowerCase()
  return normalized.includes('/src/scripts/') || normalized.includes('/dist/scripts/')
}

function httpSecurityConfig(): RuntimeConfig['httpSecurity'] {
  const production = isProductionRuntime()
  const allowedOrigins = allowedOriginsConfig('JUHE_AI_ALLOWED_ORIGINS', production)
  const cookieSecure = strictBooleanConfig('JUHE_AI_COOKIE_SECURE', production)
  const cookieSameSite = cookieSameSiteConfig('JUHE_AI_COOKIE_SAME_SITE', 'lax')
  if (production && cookieSameSite === 'none' && !cookieSecure) {
    throw new Error('JUHE_AI_COOKIE_SAME_SITE=none 时必须启用 JUHE_AI_COOKIE_SECURE=true')
  }
  return {
    cors: {
      allowedOrigins,
      allowAnyOrigin: !production && allowedOrigins.length === 0
    },
    cookie: {
      secure: cookieSecure,
      sameSite: cookieSameSite
    },
    trustProxy: trustProxyConfig('JUHE_AI_TRUST_PROXY')
  }
}

function allowedOriginsConfig(name: string, requireExplicit: boolean): string[] {
  const rawValue = rawStringConfig(name) ?? ''
  const parts = rawValue.split(',').map((part) => part.trim()).filter(Boolean)
  const allowAnyOrigin = parts.includes('*')
  const origins = Array.from(new Set(
    parts.filter((part) => part !== '*').map((part) => normalizeAllowedOrigin(name, part))
  ))
  if (allowAnyOrigin) {
    if (requireExplicit) {
      throw new Error(`${name} 不允许配置 *；需要逐项填写完整后台前端 Origin`)
    }
    return []
  }
  if (requireExplicit && origins.length === 0) {
    throw new Error(`${name} 在生产环境必须显式配置后台前端 Origin，不能继续反射任意跨域来源`)
  }
  return origins
}

function normalizeAllowedOrigin(name: string, value: string): string {
  let url: URL
  try {
    url = new URL(value)
  } catch {
    throw new Error(`${name} 包含无效 Origin：${value}`)
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw new Error(`${name} 只允许 http 或 https Origin：${value}`)
  }
  if (url.username || url.password || url.search || url.hash || (url.pathname && url.pathname !== '/')) {
    throw new Error(`${name} 只能填写 Origin，不要包含路径、查询、片段或用户名密码：${value}`)
  }
  return url.origin
}

function strictBooleanConfig(name: string, fallback: boolean): boolean {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return fallback
  if (['1', 'true', 'yes', 'on'].includes(value)) return true
  if (['0', 'false', 'no', 'off'].includes(value)) return false
  throw new Error(`${name} 只能配置为 true/false/1/0/yes/no/on/off`)
}

function cookieSameSiteConfig(name: string, fallback: CookieSameSiteRuntimeConfig): CookieSameSiteRuntimeConfig {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return fallback
  if (value === 'lax' || value === 'strict' || value === 'none') return value
  throw new Error(`${name} 只能配置为 lax、strict 或 none`)
}

function trustProxyConfig(name: string): boolean | number {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return false
  if (['true', 'yes', 'on'].includes(value)) return true
  if (['false', 'no', 'off'].includes(value)) return false
  const numericValue = Number(value)
  if (Number.isInteger(numericValue) && numericValue >= 0 && numericValue <= 16) {
    return numericValue
  }
  throw new Error(`${name} 只能配置为 true/false 或 0-16 的反向代理跳数`)
}

function hostedToolRuntimeModeConfig(name: string, fallback: HostedToolRuntimeMode): HostedToolRuntimeMode {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return fallback
  if (value === 'guidance' || value === 'reject' || value === 'mock' || value === 'local_runtime') return value
  throw new Error(`${name} 只能配置为 guidance、reject、mock 或 local_runtime`)
}

function computerAdapterConfig(): RuntimeConfig['computerAdapter'] {
  const enabled = strictBooleanConfig('JUHE_AI_COMPUTER_BROWSER_ADAPTER_ENABLED', false)
  const endpoint = computerAdapterEndpointConfig('JUHE_AI_COMPUTER_BROWSER_ADAPTER_ENDPOINT', enabled)
  return {
    enabled,
    endpoint,
    timeoutMs: numberConfig('JUHE_AI_COMPUTER_BROWSER_ADAPTER_TIMEOUT_MS', 30000, 1000, 300000),
    maxBodyBytes: numberConfig('JUHE_AI_COMPUTER_BROWSER_ADAPTER_MAX_BODY_KB', 512, 16, 4096) * 1024
  }
}

function computerAdapterEndpointConfig(name: string, required: boolean): string | undefined {
  const value = optionalStringConfig(name)
  if (!value) {
    if (required) {
      throw new Error(`${name} 在启用 Computer browser adapter 时必须配置`)
    }
    return undefined
  }
  let url: URL
  try {
    url = new URL(value)
  } catch {
    throw new Error(`${name} 必须是有效 URL`)
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw new Error(`${name} 只允许 http 或 https URL`)
  }
  if (url.username || url.password || (isProductionRuntime() && (url.search || url.hash))) {
    throw new Error(`${name} 不能包含用户名密码、查询参数或片段标识`)
  }
  if (isProductionRuntime() && url.protocol === 'http:' && !isLoopbackHost(url.hostname)) {
    throw new Error(`${name} 使用 http 时只能指向 loopback 本机地址；远程 sandbox adapter 必须使用 https`)
  }
  return url.toString()
}

function isLoopbackHost(hostname: string): boolean {
  const normalized = hostname.trim().toLowerCase().replace(/^\[/, '').replace(/\]$/, '')
  return normalized === 'localhost'
    || normalized === '::1'
    || normalized === '0:0:0:0:0:0:0:1'
    || normalized.startsWith('127.')
}

function upstreamUrlSecurityConfig(): RuntimeConfig['upstreamUrlSecurity'] {
  const allowPrivateBaseUrls = strictBooleanConfig('JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS', false)
  const privateBaseUrlAllowlist = privateUpstreamOriginAllowlistConfig('JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST')
  if (isProductionRuntime() && allowPrivateBaseUrls) {
    throw new Error('JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS 只能用于本地开发或回归测试，生产环境不能启用')
  }
  return {
    allowPrivateBaseUrls,
    privateBaseUrlAllowlist
  }
}

function privateUpstreamOriginAllowlistConfig(name: string): string[] {
  return Array.from(new Set(listConfig(name).map((value) => normalizePrivateUpstreamOrigin(name, value))))
}

function normalizePrivateUpstreamOrigin(name: string, value: string): string {
  let url: URL
  try {
    url = new URL(value)
  } catch {
    throw new Error(`${name} 只能逐项填写完整的 http/https 私网 IP Origin：${value}`)
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw new Error(`${name} 只允许 http 或 https Origin：${value}`)
  }
  if (url.username || url.password || url.search || url.hash || url.pathname !== '/') {
    throw new Error(`${name} 只能填写 Origin，不要包含路径、查询、片段或用户名密码：${value}`)
  }
  const hostname = url.hostname.replace(/^\[/, '').replace(/\]$/, '')
  if (!isIP(hostname)) {
    throw new Error(`${name} 只允许 IP Origin，不接受域名：${value}`)
  }
  return `${url.protocol}//${url.hostname.toLowerCase()}:${url.port || (url.protocol === 'https:' ? '443' : '80')}`
}

function listConfig(name: string): string[] {
  const value = rawStringConfig(name) ?? ''
  return Array.from(new Set(value.split(',').map((part) => part.trim()).filter(Boolean)))
}

function isLogLevel(value: string | undefined): value is LogLevel {
  return value === 'trace'
    || value === 'debug'
    || value === 'info'
    || value === 'warn'
    || value === 'error'
    || value === 'fatal'
    || value === 'silent'
}
