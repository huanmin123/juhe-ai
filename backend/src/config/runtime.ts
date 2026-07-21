import { createHash } from 'node:crypto'
import { existsSync, readFileSync } from 'node:fs'
import { isIP } from 'node:net'
import { dirname, isAbsolute, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { assertDevelopmentAutoLoginConfig } from './development.js'
import { loadRuntimeBaseEnv, loadRuntimeEnvFile } from './runtime-base-env.js'

export interface RuntimeConfig {
  runtimeMode: RuntimeMode
  processRole: ProcessRole
  workerRole: WorkerRuntimeRole
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
  systemApi: {
    dbServiceMaxInFlight: number
    readOnly: boolean
  }
  ownerLock: {
    enabled: boolean
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
  databasePath: string
  chatDatabasePath: string
  datasetDatabasePath: string
  usageCatalogDatabasePath: string
  statsDatabasePath: string
  usageShardRoot: string
  codexContextRoot: string
  chatAssetsRoot: string
  chat: {
    retentionDays: number
    maxConversationsPerUser: number
    maxTurnsPerConversation: number
    upstreamSseMaxEvents: number
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
  gateway: {
    bodyInFlightMaxBytes: number
    upstreamAgentMaxSockets: number
    upstreamAgentMaxFreeSockets: number
    upstreamAgentMaxTotalSockets: number
  }
  modelCheck: {
    probeRetryDelayMs: number
  }
  auditLog: {
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
  imageGenerationProvider: {
    endpoint?: string
    apiKey?: string
    api: ImageGenerationProviderApi
    model: string
    timeoutMs: number
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
export type ProcessRole = 'server' | 'worker' | 'db-service'
export type DatabaseDriver = 'sqlite' | 'postgres'
export type CacheDriver = 'memory' | 'redis'
export type RuntimeStateDriver = 'memory' | 'redis'
export type QueueDriver = 'memory' | 'redis_stream'
export type WorkerRuntimeRole =
  | 'worker'
  | 'ingest-worker'
  | 'stats-worker'
  | 'ops-worker'
  | 'temporary-maintenance-worker'
export type CookieSameSiteRuntimeConfig = 'lax' | 'strict' | 'none'
export type HostedToolRuntimeMode = 'guidance' | 'reject' | 'mock' | 'local_runtime'
export type ImageGenerationProviderApi = 'images' | 'responses'
export const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
export const localEnvPath = resolve(backendRoot, '.env')
export const defaultDatabasePath = resolve(backendRoot, 'data', 'juhe-ai.sqlite3')
export const defaultChatDatabasePath = resolve(backendRoot, 'data', 'juhe-ai-chat.sqlite3')
export const defaultDatasetDatabasePath = resolve(backendRoot, 'data', 'juhe-ai-dataset.sqlite3')
export const defaultUsageCatalogDatabasePath = resolve(backendRoot, 'data', 'juhe-ai-usage-catalog.sqlite3')
export const defaultStatsDatabasePath = resolve(backendRoot, 'data', 'juhe-ai-stats.sqlite3')
export const defaultUsageShardRoot = resolve(backendRoot, 'data', 'usage-shards')
export const defaultCodexContextRoot = resolve(backendRoot, 'data', 'codex-context')
export const defaultChatAssetsRoot = resolve(backendRoot, 'data', 'chat-assets')
export const defaultOpenAICompatibleFilesRoot = resolve(backendRoot, 'data', 'openai-compatible-files')
export const defaultCodeInterpreterTempRoot = resolve(backendRoot, 'data', 'code-interpreter-tmp')
export const defaultCodexContextStateShardRoot = resolve(defaultCodexContextRoot, 'state-shards')
export const defaultRuntimeSecret = 'juhe-ai-dev-secret-change-me'
export const defaultStandaloneSystemApiDbServiceMaxInFlight = 64
export const defaultPerformanceSystemApiDbServiceMaxInFlight = 256
const minimumProductionSecretLength = 32

const localEnv = loadRuntimeBaseEnv(localEnvPath, process.env)
const localEnvOverlayPath = envFilePathConfig(process.env.JUHE_AI_ENV_FILE ?? localEnv.JUHE_AI_ENV_FILE)
const localEnvOverlay = localEnvOverlayPath ? loadRuntimeEnvFile(localEnvOverlayPath) : {}
const hasPerformanceDriverHints = hasAnyRawConfig([
  'JUHE_AI_POSTGRES_URL',
  'JUHE_AI_REDIS_CACHE_URL',
  'JUHE_AI_REDIS_STATE_URL',
  'JUHE_AI_REDIS_QUEUE_URL'
])
const configuredRuntimeMode = runtimeModeConfig('JUHE_AI_RUNTIME_MODE', hasPerformanceDriverHints ? 'performance' : 'standalone')
const defaultSystemApiDbServiceMaxInFlight =
  configuredRuntimeMode === 'performance'
    ? defaultPerformanceSystemApiDbServiceMaxInFlight
    : defaultStandaloneSystemApiDbServiceMaxInFlight
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
const configuredSecret = secretConfig('JUHE_AI_SECRET', defaultRuntimeSecret)
const configuredRedisNamespace = redisNamespaceConfig('JUHE_AI_REDIS_NAMESPACE', configuredSecret)
const configuredHost = stringConfig('JUHE_AI_HOST', '127.0.0.1')
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
assertRuntimeLogFileIndexingConfig({
  runtimeMode: configuredRuntimeMode,
  fileEnabled: configuredLogFileEnabled
})

export const runtimeConfig: RuntimeConfig = {
  runtimeMode: configuredRuntimeMode,
  processRole: processRoleConfig('JUHE_AI_PROCESS_ROLE', 'server'),
  workerRole: workerRoleConfig('JUHE_AI_WORKER_ROLE', 'worker'),
  databaseDriver: configuredDatabaseDriver,
  cacheDriver: configuredCacheDriver,
  runtimeStateDriver: configuredRuntimeStateDriver,
  queueDriver: configuredQueueDriver,
  host: configuredHost,
  port: numberConfig('JUHE_AI_PORT', 3000, 1, 65535),
  development: {
    autoLoginUsername: configuredDevelopmentAutoLoginUsername
  },
  dbServiceHttpHost: stringConfig('JUHE_AI_DB_SERVICE_HTTP_HOST', '127.0.0.1'),
  dbServiceHttpPort: numberConfig('JUHE_AI_DB_SERVICE_HTTP_PORT', 0, 0, 65535),
  systemApi: {
    dbServiceMaxInFlight: numberConfig(
      'JUHE_AI_SYSTEM_API_DB_SERVICE_MAX_IN_FLIGHT',
      defaultSystemApiDbServiceMaxInFlight,
      1,
      5000
    ),
    readOnly: booleanConfig('JUHE_AI_SYSTEM_API_READ_ONLY', false)
  },
  ownerLock: {
    enabled: parseOwnerLockEnabled(rawStringConfig('JUHE_AI_OWNER_LOCK_ENABLED'))
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
  databasePath: pathConfig('JUHE_AI_DATABASE_PATH', defaultDatabasePath),
  chatDatabasePath: pathConfig('JUHE_AI_CHAT_DATABASE_PATH', defaultChatDatabasePath),
  datasetDatabasePath: pathConfig('JUHE_AI_DATASET_DATABASE_PATH', defaultDatasetDatabasePath),
  usageCatalogDatabasePath: pathConfig('JUHE_AI_USAGE_CATALOG_DATABASE_PATH', defaultUsageCatalogDatabasePath),
  statsDatabasePath: pathConfig('JUHE_AI_STATS_DATABASE_PATH', defaultStatsDatabasePath),
  usageShardRoot: pathConfig('JUHE_AI_USAGE_SHARD_ROOT', defaultUsageShardRoot),
  codexContextRoot: pathConfig('JUHE_AI_CODEX_CONTEXT_ROOT', defaultCodexContextRoot),
  chatAssetsRoot: pathConfig('JUHE_AI_CHAT_ASSETS_ROOT', defaultChatAssetsRoot),
  chat: {
    retentionDays: integerConfig('JUHE_AI_CHAT_RETENTION_DAYS', 3, 1, 365),
    maxConversationsPerUser: integerConfig('JUHE_AI_CHAT_MAX_CONVERSATIONS_PER_USER', 50, 1, 1000),
    maxTurnsPerConversation: integerConfig('JUHE_AI_CHAT_MAX_TURNS_PER_CONVERSATION', 50, 1, 1000),
    upstreamSseMaxEvents: integerConfig('JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS', 65_536, 2_048, 262_144)
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
  httpSecurity: httpSecurityConfig(),
  auth: authRuntimeConfig(),
  upstreamUrlSecurity: upstreamUrlSecurityConfig(),
  oauthProxyUrl: optionalStringConfig('JUHE_AI_OAUTH_PROXY_URL'),
  gateway: {
    bodyInFlightMaxBytes: numberConfig('JUHE_AI_GATEWAY_BODY_IN_FLIGHT_MAX_MB', 256, 16, 4096) * 1024 * 1024,
    upstreamAgentMaxSockets: numberConfig('JUHE_AI_GATEWAY_UPSTREAM_AGENT_MAX_SOCKETS', 2048, 64, 20000),
    upstreamAgentMaxFreeSockets: numberConfig('JUHE_AI_GATEWAY_UPSTREAM_AGENT_MAX_FREE_SOCKETS', 512, 16, 5000),
    upstreamAgentMaxTotalSockets: numberConfig('JUHE_AI_GATEWAY_UPSTREAM_AGENT_MAX_TOTAL_SOCKETS', 8192, 64, 50000)
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
  imageGenerationProvider: {
    endpoint: optionalStringConfig('JUHE_AI_IMAGE_GENERATION_PROVIDER_ENDPOINT'),
    apiKey: optionalStringConfig('JUHE_AI_IMAGE_GENERATION_PROVIDER_API_KEY'),
    api: imageGenerationProviderApiConfig('JUHE_AI_IMAGE_GENERATION_PROVIDER_API', 'images'),
    model: stringConfig('JUHE_AI_IMAGE_GENERATION_PROVIDER_MODEL', 'gpt-image-2'),
    timeoutMs: numberConfig('JUHE_AI_IMAGE_GENERATION_PROVIDER_TIMEOUT_MS', 600000, 1000, 900000),
    maxBodyBytes: numberConfig('JUHE_AI_IMAGE_GENERATION_PROVIDER_MAX_BODY_MB', 64, 1, 256) * 1024 * 1024
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
    cleanupIntervalMinutes: numberConfig('JUHE_AI_LOG_CLEANUP_INTERVAL_MINUTES', 60, 1, 1440)
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
  return process.env[name]?.trim() ?? localEnvOverlay[name]?.trim() ?? localEnv[name]?.trim()
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

export function parseOwnerLockEnabled(value: string | undefined): boolean {
  return value?.trim().toLowerCase() === 'true'
}

function runtimeModeConfig(name: string, fallback: RuntimeMode): RuntimeMode {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return fallback
  if (value === 'standalone' || value === 'performance') return value
  throw new Error(`${name} 只能配置为 standalone 或 performance`)
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

export function assertRuntimeLogFileIndexingConfig(config: {
  runtimeMode: RuntimeMode
  fileEnabled: boolean
}): void {
  if (config.runtimeMode === 'performance' && !config.fileEnabled) {
    throw new Error('JUHE_AI_RUNTIME_MODE=performance 时必须启用 JUHE_AI_LOG_FILE_ENABLED，否则 runtime_logs 没有耐久索引来源')
  }
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
  if (value === 'stats-worker') return 'stats-worker'
  if (value === 'ops-worker') return 'ops-worker'
  if (value === 'temporary-maintenance-worker') return 'temporary-maintenance-worker'
  throw new Error(`${name} 只能配置为 worker、ingest-worker、stats-worker、ops-worker 或 temporary-maintenance-worker`)
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

export function parseAuditLogRuntimeConfig(values: Record<string, string | undefined>): RuntimeConfig['auditLog'] {
  const read = (name: string): string | undefined => values[name]?.trim()
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

function imageGenerationProviderApiConfig(name: string, fallback: ImageGenerationProviderApi): ImageGenerationProviderApi {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return fallback
  if (value === 'images' || value === 'responses') return value
  throw new Error(`${name} 只能配置为 images 或 responses`)
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
