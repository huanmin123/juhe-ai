import assert from 'node:assert/strict'
import { createHash, randomBytes, randomUUID } from 'node:crypto'
import http from 'node:http'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { DatabaseClient } from '../../storage/database-client.js'
import type { PageDataChangeStore, PageDataRevisionToken } from '../../modules/page-data/page-data-change.service.js'
import type { RedisCommandClient } from '../../shared/redis-client.js'

const allowEnvironmentName = 'JUHE_AI_PAGE_DATA_CONFIRM_SMOKE_ALLOW'
const postgresEnvironmentName = 'JUHE_AI_PAGE_DATA_CONFIRM_SMOKE_POSTGRES_URL'
const redisEnvironmentNames = {
  cache: 'JUHE_AI_PAGE_DATA_CONFIRM_SMOKE_REDIS_CACHE_URL',
  state: 'JUHE_AI_PAGE_DATA_CONFIRM_SMOKE_REDIS_STATE_URL',
  queue: 'JUHE_AI_PAGE_DATA_CONFIRM_SMOKE_REDIS_QUEUE_URL'
} as const
const systemApiPrefix = '/__aisys__/api'
const confirmPath = `${systemApiPrefix}/data-changes/confirm`
const smokeClientIp = '198.51.100.77'
const requestTimeoutMs = 10_000
const cleanupOperationTimeoutMs = 3_000
const cleanupStepTimeoutMs = 15_000
const serverForceCloseGraceMs = 1_000
const maxSafeRateLimitRequests = 256

interface ConfirmDomainResult {
  action: 'unchanged' | 'delta' | 'reload' | 'reset'
  token: PageDataRevisionToken
  changes?: Array<{
    entityId?: string
    operation: string
    fieldMask: string[]
  }>
}

interface ConfirmResult {
  serverTime: string
  domains: Record<string, ConfirmDomainResult>
}

interface HttpResult {
  status: number
  body: unknown
  retryAfter?: string
}

interface ReadRateLimitSettings {
  ipPerMinute: number
  ipBurstPer10Seconds: number
  userPerMinute: number
}

interface SmokeFixture {
  accountId: string
  sessionId: string
  token: string
}

class SmokeFailure extends Error {
  constructor(readonly step: string, message: string) {
    super(message)
    this.name = 'SmokeFailure'
  }
}

if (isEntrypoint()) {
  await main().catch((error) => {
    const failure = error instanceof SmokeFailure
      ? error
      : new SmokeFailure('unexpected', safeErrorMessage(error))
    process.stderr.write(`${JSON.stringify({
      passed: false,
      step: failure.step,
      message: safeErrorMessage(failure)
    })}\n`)
    process.exitCode = 1
  })
}

async function main(): Promise<void> {
  const startedAt = Date.now()
  const input = smokeEnvironment()
  const runId = randomUUID().replaceAll('-', '')
  const redisNamespace = `page-data-confirm-smoke-${runId.slice(0, 20)}`
  const redisPrefix = `juhe-ai:${redisNamespace}:`
  configureRuntime(input, redisNamespace)

  const fixtureIds = [0, 1].map((index) => ({
    accountId: `pdsmoke_sys_${runId}_${index}`,
    sessionId: `pdsmoke_sess_${runId}_${index}`,
    token: randomBytes(32).toString('base64url')
  }))
  const fixtureAccountIds = fixtureIds.map((item) => item.accountId)
  const fixtureSessionIds = fixtureIds.map((item) => item.sessionId)

  let databaseClient: DatabaseClient | undefined
  let pageDataStore: PageDataChangeStore | undefined
  let server: http.Server | undefined
  let closePostgresPool: (() => Promise<void>) | undefined
  let closeRedisClients: (() => Promise<void>) | undefined
  const dedicatedRedisClients = new Map<keyof typeof redisEnvironmentNames, RedisCommandClient>()
  let fixturesInserted = false
  let confirmRequestCount = 0
  let mainError: unknown
  let successReport: Record<string, unknown> | undefined

  try {
    const [
      { createSystemApiApp },
      pageDataRuntime,
      databaseClientModule,
      postgresClientModule,
      redisClientModule,
      settingsRepository,
      { hashSecret },
      { sessionCookieName },
      { logger }
    ] = await Promise.all([
      import('../../modules/system-api/system-api-app.js'),
      import('../../modules/page-data/page-data-change.runtime.js'),
      import('../../storage/database-client.js'),
      import('../../storage/postgres-client.js'),
      import('../../shared/redis-client.js'),
      import('../../storage/settings.repository.js'),
      import('../../storage/crypto.js'),
      import('../../modules/auth/auth.routes.js'),
      import('../../shared/logger.js')
    ])
    logger.level = 'silent'
    closePostgresPool = postgresClientModule.closePostgresPool
    closeRedisClients = redisClientModule.closeRedisClients
    databaseClient = databaseClientModule.createPostgresDatabaseClient(await postgresClientModule.getPostgresPool())
    pageDataStore = pageDataRuntime.getPageDataChangeStore()

    await assertRequiredSchema(databaseClient)
    for (const [role, redisUrl] of Object.entries(input.redisUrls) as Array<[keyof typeof redisEnvironmentNames, string]>) {
      const client = await redisClientModule.createDedicatedRedisClient(redisUrl, {
        disableOfflineQueue: true,
        commandsQueueMaxLength: 32,
        connectTimeoutMs: 5_000
      })
      dedicatedRedisClients.set(role, client)
      await deleteRedisNamespace(client, redisPrefix)
    }

    const lastSeenAt = new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString()
    const fixtures = await insertFixtures(databaseClient, fixtureIds, lastSeenAt, hashSecret)
    fixturesInserted = true
    const sessionLastSeenBefore = await readSessionLastSeen(databaseClient, fixtures[0]!.sessionId)
    const rateLimitSettings = readRateLimitSettings(await settingsRepository.getSettingsAsync())

    const app = createSystemApiApp({
      systemApiPrefix,
      trustProxy: true
    })
    server = http.createServer((request, response) => {
      if (request.method === 'POST' && request.url?.split('?', 1)[0] === confirmPath) {
        confirmRequestCount += 1
      }
      app(request, response)
    })
    server.listen(0, '127.0.0.1')
    await listen(server)
    const baseUrl = `http://127.0.0.1:${serverPort(server)}`
    const cookieA = `${sessionCookieName}=${fixtures[0]!.token}`
    const cookieB = `${sessionCookieName}=${fixtures[1]!.token}`

    const unauthenticated = await postConfirm(baseUrl, undefined, {
      viewScope: 'self',
      domains: { 'accounts.static': null }
    })
    assert.equal(unauthenticated.status, 401, '未携带 session 的 confirm 必须返回 HTTP 401')

    const forgedAdminScope = await postConfirm(baseUrl, cookieA, {
      viewScope: 'admin',
      targetSystemAccountId: fixtures[1]!.accountId,
      domains: { 'accounts.static': null }
    })
    assert.equal(forgedAdminScope.status, 403, '普通用户不能伪造 admin scope')

    const baselineRequestCount = confirmRequestCount
    const baselineA = confirmData(await postConfirm(baseUrl, cookieA, {
      viewScope: 'self',
      domains: {
        'accounts.static': null,
        'accounts.options': null,
        'providers.catalog': null
      }
    }), '三域基线 confirm')
    assert.equal(confirmRequestCount - baselineRequestCount, 1, '三个页面数据域必须由一个 HTTP confirm 请求完成')
    assert.deepEqual(Object.keys(baselineA.domains).sort(), [
      'accounts.options',
      'accounts.static',
      'providers.catalog'
    ])
    for (const domain of Object.keys(baselineA.domains)) {
      const result = requiredDomain(baselineA, domain)
      assert.equal(result.action, 'reload', `${domain} 首次 confirm 必须要求 reload`)
      assert.equal(result.token.protocolVersion, 2, `${domain} 必须返回 v2 token`)
      assert.equal(result.token.domain, domain, `${domain} token 必须绑定原始数据域`)
    }

    const baselineB = confirmData(await postConfirm(baseUrl, cookieB, {
      viewScope: 'self',
      domains: { 'accounts.static': null }
    }), '第二账户基线 confirm')
    const staticA = requiredDomain(baselineA, 'accounts.static')
    const staticB = requiredDomain(baselineB, 'accounts.static')
    assert.notEqual(staticA.token.scope, staticB.token.scope, '不同系统账户必须获得隔离的 scope fingerprint')

    await pageDataStore.publish({
      eventId: `pdsmoke-event-${runId}`,
      domain: 'accounts.static',
      entityId: `pdsmoke-account-${runId}`,
      operation: 'upsert',
      fieldMask: ['status'],
      ownerSystemAccountIds: [fixtures[0]!.accountId],
      membershipChanged: false,
      orderChanged: false,
      filterChanged: false,
      pageChanged: false,
      occurredAt: new Date().toISOString()
    })
    const changedA = confirmData(await postConfirm(baseUrl, cookieA, {
      viewScope: 'self',
      domains: { 'accounts.static': staticA.token }
    }), '账户 A 增量 confirm')
    const unchangedB = confirmData(await postConfirm(baseUrl, cookieB, {
      viewScope: 'self',
      domains: { 'accounts.static': staticB.token }
    }), '账户 B 隔离 confirm')
    assert.equal(requiredDomain(changedA, 'accounts.static').action, 'delta', '目标账户必须看到自身增量')
    assert.equal(requiredDomain(unchangedB, 'accounts.static').action, 'unchanged', '其他账户不能看到目标账户增量')

    const providerToken = requiredDomain(baselineA, 'providers.catalog').token
    const staleEpoch = confirmData(await postConfirm(baseUrl, cookieA, {
      viewScope: 'self',
      domains: {
        'providers.catalog': {
          ...providerToken,
          epoch: `stale-${runId}`
        }
      }
    }), 'epoch 不匹配 confirm')
    const staleEpochResult = requiredDomain(staleEpoch, 'providers.catalog')
    assert.equal(staleEpochResult.action, 'reload', 'epoch 不匹配必须要求 reload')
    assert.equal(staleEpochResult.token.epoch, providerToken.epoch, '服务端必须返回当前 epoch')

    await pageDataStore.publish({
      eventId: `pdsmoke-reset-${runId}`,
      domain: 'accounts.options',
      operation: 'range_reset',
      fieldMask: [],
      ownerSystemAccountIds: [],
      membershipChanged: true,
      orderChanged: true,
      filterChanged: true,
      pageChanged: true,
      occurredAt: new Date().toISOString(),
      allScopes: true
    })
    const resetResult = confirmData(await postConfirm(baseUrl, cookieA, {
      viewScope: 'self',
      domains: { 'accounts.options': requiredDomain(baselineA, 'accounts.options').token }
    }), 'reset confirm')
    assert.equal(requiredDomain(resetResult, 'accounts.options').action, 'reset', '全 scope reset 必须返回 reset')

    const stateRedisClient = dedicatedRedisClients.get('state')
    assert.ok(stateRedisClient, '必须创建 runtime state Redis 检查客户端')
    const rateLimitEvidence = await exerciseReadRateLimit({
      baseUrl,
      cookie: cookieA,
      client: stateRedisClient,
      namespace: redisNamespace,
      accountId: fixtures[0]!.accountId,
      accountIds: fixtureAccountIds,
      settings: rateLimitSettings,
      token: requiredDomain(baselineA, 'providers.catalog').token
    })

    const sessionLastSeenAfter = await readSessionLastSeen(databaseClient, fixtures[0]!.sessionId)
    assert.equal(sessionLastSeenAfter, sessionLastSeenBefore, 'confirm read 请求不能更新 session last_seen_at')

    successReport = {
      passed: true,
      protocolVersion: 2,
      requestedDomains: 3,
      confirmRequests: confirmRequestCount,
      authenticationChecked: true,
      scopeIsolationChecked: true,
      epochChecked: true,
      resetChecked: true,
      readBucketChecked: true,
      rateLimitRequests: rateLimitEvidence.requestCount,
      rateLimitRejected: rateLimitEvidence.rejectedCount,
      sessionNoTouchChecked: true,
      elapsedMs: Date.now() - startedAt
    }
  } catch (error) {
    mainError = error
    throw error instanceof SmokeFailure ? error : new SmokeFailure('verification', safeErrorMessage(error))
  } finally {
    const cleanupErrors: string[] = []
    await shutdownServerAndRuntimeRedis({
      server,
      closeRuntimeRedis: closeRedisClients,
      errors: cleanupErrors,
      operationTimeoutMs: cleanupOperationTimeoutMs,
      forceCloseGraceMs: serverForceCloseGraceMs
    })
    await cleanupStep('postgres-fixture', cleanupErrors, async () => {
      if (!databaseClient || !fixturesInserted) return
      await deleteFixtures(databaseClient, fixtureSessionIds, fixtureAccountIds)
    })
    await cleanupDedicatedRedisResources({
      clients: dedicatedRedisClients,
      prefix: redisPrefix,
      errors: cleanupErrors,
      operationTimeoutMs: cleanupOperationTimeoutMs
    })
    await cleanupStep('postgres-pool', cleanupErrors, async () => closePostgresPool?.())
    if (cleanupErrors.length > 0) {
      const suffix = mainError ? `；原验证失败：${safeErrorMessage(mainError)}` : ''
      throw new SmokeFailure('cleanup', `清理失败：${cleanupErrors.join(', ')}${suffix}`)
    }
  }
  if (successReport) process.stdout.write(`${JSON.stringify(successReport)}\n`)
}

function smokeEnvironment(): {
  postgresUrl: string
  redisUrls: Record<keyof typeof redisEnvironmentNames, string>
} {
  if (process.env[allowEnvironmentName]?.trim() !== '1') {
    throw new SmokeFailure('configuration', `必须显式设置 ${allowEnvironmentName}=1 才允许运行真实依赖 smoke`)
  }
  const postgresUrl = requiredEnvironment(postgresEnvironmentName)
  assertUrlProtocol(postgresEnvironmentName, postgresUrl, ['postgres:', 'postgresql:'])
  const redisUrls = Object.fromEntries(Object.entries(redisEnvironmentNames).map(([role, name]) => {
    const value = requiredEnvironment(name)
    assertUrlProtocol(name, value, ['redis:', 'rediss:'])
    return [role, value]
  })) as Record<keyof typeof redisEnvironmentNames, string>
  const redisResources = Object.entries(redisUrls).map(([role, value]) => [role, redisResource(value)] as const)
  if (new Set(redisResources.map(([, resource]) => resource)).size !== redisResources.length) {
    throw new SmokeFailure('configuration', 'cache/state/queue smoke Redis 必须指向三个不同的 host:port')
  }
  return { postgresUrl, redisUrls }
}

function configureRuntime(
  input: { postgresUrl: string; redisUrls: Record<keyof typeof redisEnvironmentNames, string> },
  redisNamespace: string
): void {
  process.env.NODE_ENV = 'test'
  process.env.JUHE_AI_RUNTIME_MODE = 'performance'
  process.env.JUHE_AI_DATABASE_DRIVER = 'postgres'
  process.env.JUHE_AI_CACHE_DRIVER = 'redis'
  process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'redis'
  process.env.JUHE_AI_QUEUE_DRIVER = 'redis_stream'
  process.env.JUHE_AI_POSTGRES_URL = input.postgresUrl
  process.env.JUHE_AI_REDIS_CACHE_URL = input.redisUrls.cache
  process.env.JUHE_AI_REDIS_STATE_URL = input.redisUrls.state
  process.env.JUHE_AI_REDIS_QUEUE_URL = input.redisUrls.queue
  process.env.JUHE_AI_REDIS_NAMESPACE = redisNamespace
  process.env.JUHE_AI_SECRET = randomBytes(32).toString('hex')
  process.env.JUHE_AI_LOG_CONSOLE_ENABLED = 'false'
  process.env.JUHE_AI_LOG_FILE_ENABLED = 'false'
  process.env.JUHE_AI_RUNTIME_LOG_INDEX_ENABLED = 'false'
  process.env.JUHE_AI_DISABLE_BASE_ENV = 'true'
  delete process.env.JUHE_AI_DEV_AUTO_LOGIN_USERNAME
}

async function assertRequiredSchema(client: DatabaseClient): Promise<void> {
  const row = await client.one<{
    accounts_table: string | null
    sessions_table: string | null
    settings_table: string | null
  }>(`
    SELECT
      to_regclass('juhe_business.system_accounts')::text AS accounts_table,
      to_regclass('juhe_business.system_sessions')::text AS sessions_table,
      to_regclass('juhe_business.system_settings')::text AS settings_table
  `)
  if (!row?.accounts_table || !row.sessions_table || !row.settings_table) {
    throw new SmokeFailure('schema', '测试 PostgreSQL 缺少当前 juhe_business system_accounts/system_sessions/system_settings schema，请先离线同步测试库')
  }
}

async function insertFixtures(
  client: DatabaseClient,
  fixtures: SmokeFixture[],
  lastSeenAt: string,
  hashSecret: (value: string) => string
): Promise<SmokeFixture[]> {
  const now = new Date().toISOString()
  const expiresAt = new Date(Date.now() + 60 * 60 * 1000).toISOString()
  await client.transaction(async (tx) => {
    for (const [index, fixture] of fixtures.entries()) {
      await tx.execute(`
        INSERT INTO "juhe_business"."system_accounts" (
          id, username, display_name, description, role, status, password_hash,
          created_at, updated_at
        ) VALUES (?, ?, ?, NULL, 'user', 'active', ?, ?, ?)
      `, [
        fixture.accountId,
        `pdsmoke_${fixture.accountId.slice(-18)}`,
        `Page data smoke ${index + 1} ${fixture.accountId.slice(-8)}`,
        'smoke-login-disabled',
        now,
        now
      ])
      await tx.execute(`
        INSERT INTO "juhe_business"."system_sessions" (
          id, system_account_id, token_hash, expires_at, created_at, last_seen_at
        ) VALUES (?, ?, ?, ?, ?, ?)
      `, [fixture.sessionId, fixture.accountId, hashSecret(fixture.token), expiresAt, now, lastSeenAt])
    }
  })
  return fixtures
}

async function readSessionLastSeen(client: DatabaseClient, sessionId: string): Promise<string> {
  const row = await client.one<{ last_seen_at: string }>(`
    SELECT last_seen_at
    FROM "juhe_business"."system_sessions"
    WHERE id = ?
  `, [sessionId])
  if (!row?.last_seen_at) throw new SmokeFailure('fixture', '测试 session 不存在')
  return row.last_seen_at
}

async function deleteFixtures(client: DatabaseClient, sessionIds: string[], accountIds: string[]): Promise<void> {
  await client.transaction(async (tx) => {
    for (const sessionId of sessionIds) {
      await tx.execute('DELETE FROM "juhe_business"."system_sessions" WHERE id = ?', [sessionId])
    }
    for (const accountId of accountIds) {
      await tx.execute('DELETE FROM "juhe_business"."system_accounts" WHERE id = ?', [accountId])
    }
  })
  const remaining = await client.one<{ account_count: number; session_count: number }>(`
    SELECT
      (SELECT COUNT(*)::int FROM "juhe_business"."system_accounts" WHERE id IN (?, ?)) AS account_count,
      (SELECT COUNT(*)::int FROM "juhe_business"."system_sessions" WHERE id IN (?, ?)) AS session_count
  `, [...accountIds, ...sessionIds])
  assert.equal(Number(remaining?.account_count ?? -1), 0, 'PG smoke system account fixture 必须清理')
  assert.equal(Number(remaining?.session_count ?? -1), 0, 'PG smoke session fixture 必须清理')
}

async function postConfirm(baseUrl: string, cookie: string | undefined, body: object): Promise<HttpResult> {
  const response = await fetch(`${baseUrl}${confirmPath}`, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      'x-forwarded-for': smokeClientIp,
      ...(cookie ? { cookie } : {})
    },
    body: JSON.stringify(body),
    signal: AbortSignal.timeout(requestTimeoutMs)
  })
  const text = await response.text()
  let parsed: unknown
  try {
    parsed = text ? JSON.parse(text) : undefined
  } catch {
    throw new SmokeFailure('http', `confirm 返回了非 JSON 响应，HTTP ${response.status}`)
  }
  return {
    status: response.status,
    body: parsed,
    ...(response.headers.get('retry-after') ? { retryAfter: response.headers.get('retry-after') ?? undefined } : {})
  }
}

function confirmData(result: HttpResult, label: string): ConfirmResult {
  assert.equal(result.status, 200, `${label} 必须返回 HTTP 200`)
  const envelope = recordValue(result.body, `${label} 响应`)
  const data = recordValue(envelope.data, `${label} data`)
  const domains = recordValue(data.domains, `${label} domains`) as Record<string, ConfirmDomainResult>
  return {
    serverTime: String(data.serverTime ?? ''),
    domains
  }
}

function requiredDomain(result: ConfirmResult, domain: string): ConfirmDomainResult {
  const value = result.domains[domain]
  assert.ok(value, `confirm 响应缺少 ${domain}`)
  return value
}

function readRateLimitSettings(settings: Record<string, unknown>): ReadRateLimitSettings {
  return {
    ipPerMinute: nonNegativeIntegerSetting(settings.systemApiRateLimitIpReadPerMinute, 'systemApiRateLimitIpReadPerMinute'),
    ipBurstPer10Seconds: nonNegativeIntegerSetting(settings.systemApiRateLimitIpReadBurstPer10Seconds, 'systemApiRateLimitIpReadBurstPer10Seconds'),
    userPerMinute: nonNegativeIntegerSetting(settings.systemApiRateLimitUserReadPerMinute, 'systemApiRateLimitUserReadPerMinute')
  }
}

async function exerciseReadRateLimit(input: {
  baseUrl: string
  cookie: string
  client: RedisCommandClient
  namespace: string
  accountId: string
  accountIds: string[]
  settings: ReadRateLimitSettings
  token: PageDataRevisionToken
}): Promise<{ requestCount: number; rejectedCount: number }> {
  const activeLimits = [input.settings.ipPerMinute, input.settings.ipBurstPer10Seconds, input.settings.userPerMinute]
    .filter((value) => value > 0)
  if (activeLimits.length === 0) {
    throw new SmokeFailure('read-bucket', '测试库已禁用全部 read 限流，无法通过实际 HTTP 429 验证 read bucket')
  }
  const requestCount = Math.min(...activeLimits) + 1
  if (requestCount > maxSafeRateLimitRequests) {
    throw new SmokeFailure(
      'read-bucket',
      `触发最小 read bucket 需要 ${requestCount} 次请求，超过 smoke 安全上限 ${maxSafeRateLimitRequests}；请使用专用低阈值测试库`
    )
  }

  const readBuckets = [
    {
      key: fixedWindowKey(input.namespace, 'system_api_ip_minute', `${smokeClientIp}:read`),
      limit: input.settings.ipPerMinute,
      required: input.settings.ipPerMinute > 0
    },
    {
      key: fixedWindowKey(input.namespace, 'system_api_ip_burst', `${smokeClientIp}:read`),
      limit: input.settings.ipBurstPer10Seconds,
      required: input.settings.ipBurstPer10Seconds > 0
    },
    {
      key: fixedWindowKey(input.namespace, 'system_api_user_minute', `${input.accountId}:read`),
      limit: input.settings.userPerMinute,
      required: input.settings.userPerMinute > 0
    }
  ]
  const writeKeys = [
    fixedWindowKey(input.namespace, 'system_api_ip_minute', `${smokeClientIp}:write`),
    fixedWindowKey(input.namespace, 'system_api_ip_burst', `${smokeClientIp}:write`),
    ...input.accountIds.map((accountId) => fixedWindowKey(input.namespace, 'system_api_user_minute', `${accountId}:write`))
  ]
  const resetKeys = [
    ...readBuckets.map((bucket) => bucket.key),
    ...input.accountIds.flatMap((accountId) => [
      fixedWindowKey(input.namespace, 'system_api_user_minute', `${accountId}:read`),
      fixedWindowKey(input.namespace, 'system_api_user_minute', `${accountId}:write`)
    ]),
    ...writeKeys
  ]
  await clearRateLimitKeys(input.client, [
    ...readBuckets.map((bucket) => bucket.key),
    ...writeKeys,
    ...resetKeys
  ])
  const resetValues = await Promise.all([...new Set(resetKeys)].map((key) => input.client.get(key)))
  assert.ok(resetValues.every((value) => value === null), '限流实测必须从零计数开始')

  const responses: HttpResult[] = []
  const batchSize = 32
  for (let offset = 0; offset < requestCount; offset += batchSize) {
    const batchCount = Math.min(batchSize, requestCount - offset)
    responses.push(...await Promise.all(Array.from({ length: batchCount }, () => postConfirm(input.baseUrl, input.cookie, {
      viewScope: 'self',
      domains: { 'providers.catalog': input.token }
    }))))
    if (responses.some((response) => response.status === 429)) break
  }
  const unexpected = responses.find((response) => response.status !== 200 && response.status !== 429)
  assert.equal(unexpected, undefined, 'read bucket 压测只允许 HTTP 200 或 429')
  const rejected = responses.filter((response) => response.status === 429)
  if (rejected.length === 0) {
    throw new SmokeFailure('read-bucket', '在安全请求上限内未观察到实际 HTTP 429；请确认测试 IP 未加入白名单且 read 阈值可用于 smoke')
  }
  assert.ok(rejected.every((response) => /^\d+$/.test(response.retryAfter ?? '')), 'HTTP 429 必须携带数字 Retry-After')

  const readValues = await Promise.all(readBuckets.map(async (bucket) => ({
    ...bucket,
    value: await input.client.get(bucket.key)
  })))
  if (readValues.some((bucket) => bucket.required && !bucket.value)) {
    throw new SmokeFailure('read-bucket', '未观察到 confirm read bucket；请确认测试库 read 限流启用且测试 IP 未加入白名单')
  }
  const activeReadValues = readValues.filter((bucket) => bucket.required)
  for (const bucket of activeReadValues) assert.match(bucket.value ?? '', /^\d+:\d+$/, 'read bucket 值格式必须有效')
  assert.ok(activeReadValues.some((bucket) => rateLimitCount(bucket.value) === bucket.limit), '实际 HTTP 429 必须对应至少一个已达到阈值的 read Redis bucket')
  const writeValues = await Promise.all(writeKeys.map((key) => input.client.get(key)))
  assert.ok(writeValues.every((value) => value === null), 'confirm 不得写入 write rate-limit bucket')
  return { requestCount: responses.length, rejectedCount: rejected.length }
}

function rateLimitCount(value: string | null): number {
  const count = Number(value?.split(':', 1)[0])
  return Number.isSafeInteger(count) && count >= 0 ? count : -1
}

function nonNegativeIntegerSetting(value: unknown, name: string): number {
  if (!Number.isSafeInteger(value) || Number(value) < 0) {
    throw new SmokeFailure('read-bucket', `测试库 ${name} 必须是非负整数`)
  }
  return Number(value)
}

function fixedWindowKey(namespace: string, storeName: string, key: string): string {
  return `juhe-ai:${namespace}:rate-limit:fixed:${sha256(storeName)}:${sha256(key)}`
}

function sha256(value: string): string {
  return createHash('sha256').update(value).digest('base64url')
}

async function clearRateLimitKeys(client: RedisCommandClient, keys: string[]): Promise<void> {
  for (const key of new Set(keys)) {
    await withDeadline(client.del(key), cleanupOperationTimeoutMs, 'Redis rate-limit DEL timeout')
  }
}

async function deleteRedisNamespace(
  client: RedisCommandClient,
  prefix: string,
  operationTimeoutMs = cleanupOperationTimeoutMs
): Promise<number> {
  const keys = await scanRedisNamespace(client, prefix, operationTimeoutMs)
  for (const key of keys) {
    await withDeadline(client.del(key), operationTimeoutMs, 'Redis cleanup DEL timeout')
  }
  return keys.length
}

async function scanRedisNamespace(
  client: RedisCommandClient,
  prefix: string,
  operationTimeoutMs = cleanupOperationTimeoutMs
): Promise<string[]> {
  const keys: string[] = []
  let cursor = '0'
  do {
    const value = await withDeadline(
      client.sendCommand(['SCAN', cursor, 'MATCH', `${prefix}*`, 'COUNT', '200']),
      operationTimeoutMs,
      'Redis cleanup SCAN timeout'
    )
    if (!Array.isArray(value) || value.length !== 2 || !Array.isArray(value[1])) {
      throw new SmokeFailure('redis-cleanup', 'Redis SCAN 返回格式无效')
    }
    cursor = String(value[0])
    keys.push(...value[1].map((item) => String(item)))
  } while (cursor !== '0')
  return [...new Set(keys)]
}

async function closeRedisClient(
  client: RedisCommandClient,
  operationTimeoutMs = cleanupOperationTimeoutMs
): Promise<void> {
  if (!client.quit) {
    client.destroy?.()
    return
  }
  try {
    await withDeadline(client.quit(), operationTimeoutMs, 'Redis cleanup QUIT timeout')
  } catch (error) {
    client.destroy?.()
    throw error
  }
}

export async function cleanupDedicatedRedisResources(input: {
  clients: ReadonlyMap<string, RedisCommandClient>
  prefix: string
  errors: string[]
  operationTimeoutMs?: number
}): Promise<void> {
  const operationTimeoutMs = input.operationTimeoutMs ?? cleanupOperationTimeoutMs
  const stepTimeoutMs = Math.max(cleanupStepTimeoutMs, operationTimeoutMs * 4)
  for (const [role, client] of input.clients) {
    await cleanupStep(`redis-${role}-namespace`, input.errors, async () => {
      await deleteRedisNamespace(client, input.prefix, operationTimeoutMs)
      assert.equal(
        (await scanRedisNamespace(client, input.prefix, operationTimeoutMs)).length,
        0,
        `Redis ${role} smoke namespace 清理后必须为空`
      )
    }, stepTimeoutMs)
  }
  for (const [role, client] of input.clients) {
    await cleanupStep(
      `redis-${role}-client`,
      input.errors,
      async () => closeRedisClient(client, operationTimeoutMs),
      stepTimeoutMs
    )
  }
}

async function cleanupStep(
  label: string,
  errors: string[],
  action: () => Promise<unknown>,
  timeoutMs = cleanupStepTimeoutMs
): Promise<void> {
  try {
    await withDeadline(Promise.resolve().then(action), timeoutMs, `${label} cleanup timeout`)
  } catch {
    errors.push(label)
  }
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  return new Promise((resolve, reject) => {
    server.once('listening', resolve)
    server.once('error', reject)
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  if (!address || typeof address === 'string') throw new SmokeFailure('server', '本地 system API 监听地址不可用')
  return address.port
}

interface CloseableHttpServer {
  readonly listening?: boolean
  close(callback: (error?: Error) => void): unknown
  closeAllConnections?(): void
}

export async function shutdownServerAndRuntimeRedis(input: {
  server: CloseableHttpServer | undefined
  closeRuntimeRedis: (() => Promise<void>) | undefined
  errors: string[]
  operationTimeoutMs?: number
  forceCloseGraceMs?: number
}): Promise<void> {
  const operationTimeoutMs = input.operationTimeoutMs ?? cleanupOperationTimeoutMs
  const forceCloseGraceMs = input.forceCloseGraceMs ?? serverForceCloseGraceMs
  const closePromise = beginServerClose(input.server)
  await cleanupStep('runtime-redis-clients', input.errors, async () => {
    await input.closeRuntimeRedis?.()
  }, operationTimeoutMs)
  await cleanupStep('server', input.errors, async () => {
    try {
      await withDeadline(closePromise, operationTimeoutMs, 'HTTP server graceful close timeout')
      return
    } catch (gracefulError) {
      try {
        input.server?.closeAllConnections?.()
      } catch {
        throw gracefulError
      }
    }
    await withDeadline(closePromise, forceCloseGraceMs, 'HTTP server forced close timeout')
  }, operationTimeoutMs + forceCloseGraceMs + 100)
}

function beginServerClose(server: CloseableHttpServer | undefined): Promise<void> {
  if (!server?.listening) return Promise.resolve()
  return new Promise<void>((resolvePromise, reject) => {
    server.close((error) => error ? reject(error) : resolvePromise())
  })
}

async function withDeadline<T>(promise: Promise<T>, timeoutMs: number, message: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    return await Promise.race([
      promise,
      new Promise<T>((_resolve, reject) => {
        timer = setTimeout(() => reject(new Error(message)), timeoutMs)
      })
    ])
  } finally {
    if (timer) clearTimeout(timer)
  }
}

function isEntrypoint(): boolean {
  const entrypoint = process.argv[1]
  return Boolean(entrypoint) && resolve(entrypoint!) === fileURLToPath(import.meta.url)
}

function requiredEnvironment(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) throw new SmokeFailure('configuration', `缺少专用环境变量 ${name}`)
  return value
}

function assertUrlProtocol(name: string, value: string, protocols: string[]): void {
  let url: URL
  try {
    url = new URL(value)
  } catch {
    throw new SmokeFailure('configuration', `${name} 必须是有效 URL`)
  }
  if (!protocols.includes(url.protocol)) {
    throw new SmokeFailure('configuration', `${name} 协议无效`)
  }
}

function redisResource(value: string): string {
  const url = new URL(value)
  return `${url.hostname}:${url.port || '6379'}`
}

function recordValue(value: unknown, label: string): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new SmokeFailure('http', `${label} 必须是对象`)
  }
  return value as Record<string, unknown>
}

function safeErrorMessage(error: unknown): string {
  const raw = error instanceof Error ? error.message : String(error)
  const configuredUrls = [
    process.env[postgresEnvironmentName],
    ...Object.values(redisEnvironmentNames).map((name) => process.env[name])
  ].filter((value): value is string => Boolean(value))
  let sanitized = raw
  for (const value of configuredUrls) sanitized = sanitized.replaceAll(value, '<redacted-url>')
  sanitized = sanitized
    .replace(/(?:postgres(?:ql)?|redis(?:s)?):\/\/[^\s]+/gi, '<redacted-url>')
    .replace(/(?:cookie|token|password|authorization)\s*[:=]\s*[^\s,;]+/gi, '$1=<redacted>')
  return sanitized.slice(0, 400)
}
