import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { type AuditCaptureContext } from '../../modules/gateway/audit/capture.service.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import {
  RecoverableUnavailableWaitCoordinator,
  waitForRecoverableUnavailableState
} from '../../modules/gateway/runtime/recoverable-unavailable-wait.js'
import { logger } from '../../shared/logger.js'

interface MockUpstreamHit {
  authorization: string
  path: string
  bodyText: string
  receivedAtMs: number
}

interface GatewayScenario {
  accountId: string
  groupId: string
  apiKey: string
}

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-recoverable-unavailable-mock-ai-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'gateway-recoverable-unavailable-mock-ai.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'gateway-recoverable-unavailable-mock-ai-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.runtimeMode = 'standalone'
runtimeConfig.cacheDriver = 'memory'
runtimeConfig.runtimeStateDriver = 'memory'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  settingsRepository,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  readWorkerPool
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const upstreamHits: MockUpstreamHit[] = []
const transportFailureCounts = new Map<string, number>()
let rateLimitedCooldownClearTimer: ReturnType<typeof setTimeout> | undefined
let rateLimitedCooldownClearResult: ReturnType<typeof repositories.clearAccountFailureState> | undefined

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  settingsRepository.updateSettings({
    noAvailableAccountWaitTimeoutSeconds: 10,
    textStreamIdleTimeoutSeconds: 30,
    textFirstResponseTimeoutSeconds: 30,
    temporaryUnschedulableRetryAttempts: 0
  })
  gatewayCache.clearGatewayRuntimeCache()
  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    upstreamServer = createMockOpenAIUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`

    const localSuppression = createSingleAccountScenario('本地屏蔽恢复等待', 'sk-recoverable-local-suppression', upstreamBaseUrl)
    const allRecoverableTimeout = createSingleAccountScenario('全池可恢复最大等待', 'sk-recoverable-max-wait', upstreamBaseUrl)
    const transportFailure = createSingleAccountScenario('传输失败直接交还客户端', 'sk-recoverable-transport-failure', upstreamBaseUrl)
    const persistentTransportFailure = createSingleAccountScenario('持续传输失败直接交还客户端', 'sk-recoverable-transport-always-fails', upstreamBaseUrl)
    const rateLimitedCooldown = createSingleAccountScenario('限流冷却恢复等待', 'sk-recoverable-rate-limited', upstreamBaseUrl)
    const activeCooldown = createSingleAccountScenario('正常状态冷却时间恢复等待', 'sk-recoverable-active-cooldown', upstreamBaseUrl)
    const fallback = createFallbackScenario(upstreamBaseUrl)
    const disabled = createDisabledScenario(upstreamBaseUrl)

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    await assertLocalSuppressionWaitsAndRecovers(baseUrl, localSuppression)
    await assertAllRecoverableAccountsHonorMaxWait(baseUrl, allRecoverableTimeout)
    await assertSingleCandidateTransportFailureDoesNotEnterRecoveryWait(baseUrl, transportFailure)
    await assertPersistentSingleCandidateTransportFailureDoesNotEnterRecoveryWait(baseUrl, persistentTransportFailure)
    await assertRateLimitedCooldownWaitsAndRecovers(baseUrl, rateLimitedCooldown)
    await assertActiveCooldownWaitsAndRecovers(baseUrl, activeCooldown)
    await assertFallbackGroupBypassesRecoverableWait(baseUrl, fallback)
    await assertHardUnavailableDoesNotEnterRecoverableWait(baseUrl, disabled)
    await assertRecoverableWaitTimeoutBranch()

    console.log('gateway recoverable unavailable mock ai regression passed')
  } finally {
    await closeServer(appServer)
    await closeServer(upstreamServer)
  }
} finally {
  if (rateLimitedCooldownClearTimer) {
    clearTimeout(rateLimitedCooldownClearTimer)
    rateLimitedCooldownClearTimer = undefined
  }
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  await readWorkerPool.closeSqliteReadWorkerPool()
  databaseModule.getBusinessDatabase().close()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertLocalSuppressionWaitsAndRecovers(baseUrl: string, scenario: GatewayScenario): Promise<void> {
  const startHitCount = upstreamHits.length
  accountSideEffects.suppressGatewayAccountLocallyForTest(scenario.accountId, 1_000, 'mock ai 本地屏蔽恢复等待')
  const suppressionUntil = accountSideEffects.snapshotGatewayAccountRuntimeAvailability()[scenario.accountId]?.until
  assert(suppressionUntil, '本地屏蔽测试必须取得确切 until')
  const suppressionUntilMs = Date.parse(suppressionUntil)
  const startedAt = Date.now()
  const response = await postChat(baseUrl, scenario.apiKey, 'local suppression should wait and recover')
  const elapsedMs = Date.now() - startedAt
  assert.equal(response.status, 200, `本地屏蔽释放后应恢复调度，实际 HTTP ${response.status}: ${response.text}`)
  assert.match(response.text, /mock ai ok from sk-recoverable-local-suppression/)
  assert(elapsedMs < 3_000, `本地屏蔽恢复等待不应等满巡检窗口，实际 ${elapsedMs}ms`)
  assert.deepEqual(authorizationsForKeySince(startHitCount, 'sk-recoverable-local-suppression'), ['Bearer sk-recoverable-local-suppression'])
  assert(
    singleHitForKeySince(startHitCount, 'sk-recoverable-local-suppression').receivedAtMs >= suppressionUntilMs,
    '本地屏蔽账户不得在绝对 until 前被派发'
  )
}

async function assertAllRecoverableAccountsHonorMaxWait(baseUrl: string, scenario: GatewayScenario): Promise<void> {
  const startHitCount = upstreamHits.length
  accountSideEffects.suppressGatewayAccountLocallyForTest(scenario.accountId, 30_000, 'mock ai 全池可恢复最大等待')
  const startedAt = Date.now()
  const response = await postChat(baseUrl, scenario.apiKey, 'all recoverable accounts should honor max wait')
  const elapsedMs = Date.now() - startedAt
  assert.equal(response.status, 503, `全池仅有尚未到期的可恢复账户时应交还客户端重试，实际 HTTP ${response.status}: ${response.text}`)
  assert(elapsedMs >= 2_500, `全池可恢复等待不得立即失败，应消费约 3 秒短协调预算，实际 ${elapsedMs}ms`)
  assert(elapsedMs < 6_000, `全池可恢复等待不得无界超过共享短协调上界，实际 ${elapsedMs}ms`)
  assert.deepEqual(authorizationsForKeySince(startHitCount, 'sk-recoverable-max-wait'), [], '等待预算耗尽前账户未恢复时不得请求上游')
  assert.match(response.text, /upstream_retryable_error/, `全池可恢复等待耗尽后应返回稳定可重试错误码，实际耗时 ${elapsedMs}ms`)
}

async function assertSingleCandidateTransportFailureDoesNotEnterRecoveryWait(baseUrl: string, scenario: GatewayScenario): Promise<void> {
  const startHitCount = upstreamHits.length
  const startedAt = Date.now()
  const response = await postChat(baseUrl, scenario.apiKey, 'single candidate transport failure must exhaust once')
  const elapsedMs = Date.now() - startedAt
  const matchingAuthorizations = authorizationsSince(startHitCount)
    .filter((authorization) => authorization === 'Bearer sk-recoverable-transport-failure')
  assert.equal(response.status, 503, `通用 POST 传输失败应交给客户端决定是否重试，实际 HTTP ${response.status}: ${response.text}`)
  assert.match(response.text, /上游暂时不可用|上游请求失败/)
  assert(elapsedMs < 3_000, `通用 POST 传输失败不得进入服务端恢复等待，实际 ${elapsedMs}ms`)
  assert.deepEqual(matchingAuthorizations, ['Bearer sk-recoverable-transport-failure'], '仅有一个候选时统一切号也只能派发该候选一次')
}

async function assertPersistentSingleCandidateTransportFailureDoesNotEnterRecoveryWait(baseUrl: string, scenario: GatewayScenario): Promise<void> {
  const startHitCount = upstreamHits.length
  const startedAt = Date.now()
  const response = await postChat(baseUrl, scenario.apiKey, 'persistent single candidate transport failure must exhaust once')
  const elapsedMs = Date.now() - startedAt
  const matchingAuthorizations = authorizationsForKeySince(startHitCount, 'sk-recoverable-transport-always-fails')
  assert.equal(response.status, 503, `持续通用 POST transport 失败应直接交给客户端重试，实际 HTTP ${response.status}: ${response.text}`)
  assert.match(response.text, /上游暂时不可用|上游请求失败/)
  assert(elapsedMs < 3_000, `持续通用 POST transport 失败不得消耗服务端恢复预算，实际 ${elapsedMs}ms`)
  assert.deepEqual(matchingAuthorizations, ['Bearer sk-recoverable-transport-always-fails'], '持续失败且仅有一个候选时也不得重复派发同一候选')
}

async function assertRateLimitedCooldownWaitsAndRecovers(baseUrl: string, scenario: GatewayScenario): Promise<void> {
  const startHitCount = upstreamHits.length
  const cooldownUntilMs = Date.now() + 1_000
  repositories.markAccountCooldown(
    scenario.accountId,
    new Date(cooldownUntilMs).toISOString(),
    'mock ai 限流冷却恢复等待',
    'rate_limited'
  )
  gatewayCache.clearGatewayRuntimeCache()
  const recoverableBeforeWait = repositories.listRecoverableUnavailableOpenAIAccountsForGroup(
    scenario.groupId,
    access.systemAccountId,
    { requestedModel: 'gpt-5.5', windowMs: 10_000 }
  )
  const recoverableDiagnostic = repositories.findAccountForTest(scenario.accountId, access)
  assert.equal(
    recoverableBeforeWait.some((account) => account.id === scenario.accountId),
    true,
    `限流冷却账户应进入恢复等待候选：status=${recoverableDiagnostic?.status} schedulable=${recoverableDiagnostic?.schedulable} cooldown=${recoverableDiagnostic?.cooldownUntil} group=${scenario.groupId} candidates=${recoverableBeforeWait.length}`
  )
  rateLimitedCooldownClearTimer = setTimeout(() => {
    rateLimitedCooldownClearTimer = undefined
    rateLimitedCooldownClearResult = repositories.clearAccountFailureState(scenario.accountId, access)
    gatewayCache.clearGatewayRuntimeCache()
  }, 500)
  const startedAt = Date.now()
  const response = await postChat(baseUrl, scenario.apiKey, 'rate limited cooldown should wait and recover')
  const elapsedMs = Date.now() - startedAt
  const finalAccount = repositories.findAccountForTest(scenario.accountId, access)
  const clearDiagnostic = rateLimitedCooldownClearResult
    ? { status: rateLimitedCooldownClearResult.status, schedulable: rateLimitedCooldownClearResult.schedulable, cooldownUntil: rateLimitedCooldownClearResult.cooldownUntil }
    : undefined
  const finalDiagnostic = finalAccount
    ? { status: finalAccount.status, schedulable: finalAccount.schedulable, cooldownUntil: finalAccount.cooldownUntil }
    : undefined
  assert.equal(response.status, 200, `限流冷却恢复后应恢复调度，实际 HTTP ${response.status}，耗时 ${elapsedMs}ms，clear=${JSON.stringify(clearDiagnostic)}，final=${JSON.stringify(finalDiagnostic)}: ${response.text}`)
  assert.match(response.text, /mock ai ok from sk-recoverable-rate-limited/)
  assert(elapsedMs < 3_000, `限流冷却恢复等待不应等满巡检窗口，实际 ${elapsedMs}ms`)
  assert.deepEqual(authorizationsForKeySince(startHitCount, 'sk-recoverable-rate-limited'), ['Bearer sk-recoverable-rate-limited'])
  assert(
    singleHitForKeySince(startHitCount, 'sk-recoverable-rate-limited').receivedAtMs >= cooldownUntilMs,
    '限流冷却账户不得在绝对 cooldown_until 前被派发'
  )
}

async function assertActiveCooldownWaitsAndRecovers(baseUrl: string, scenario: GatewayScenario): Promise<void> {
  const startHitCount = upstreamHits.length
  const cooldownUntilMs = Date.now() + 1_000
  const cooldownUntil = new Date(cooldownUntilMs).toISOString()
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET status = ?, schedulable = 1, cooldown_until = ?, updated_at = ? WHERE id = ?')
    .run('active', cooldownUntil, new Date().toISOString(), scenario.accountId)
  gatewayCache.clearGatewayRuntimeCache()
  const startedAt = Date.now()
  const response = await postChat(baseUrl, scenario.apiKey, 'active cooldown timestamp should wait and recover')
  const elapsedMs = Date.now() - startedAt
  assert.equal(response.status, 200, `active + cooldown_until 到期后应恢复调度，实际 HTTP ${response.status}: ${response.text}`)
  assert.match(response.text, /mock ai ok from sk-recoverable-active-cooldown/)
  assert(elapsedMs < 3_000, `active 冷却时间恢复等待不应等满巡检窗口，实际 ${elapsedMs}ms`)
  assert.deepEqual(authorizationsForKeySince(startHitCount, 'sk-recoverable-active-cooldown'), ['Bearer sk-recoverable-active-cooldown'])
  assert(
    singleHitForKeySince(startHitCount, 'sk-recoverable-active-cooldown').receivedAtMs >= cooldownUntilMs,
    'active 冷却账户不得在绝对 cooldown_until 前被派发'
  )
}

async function assertFallbackGroupBypassesRecoverableWait(baseUrl: string, scenario: { primaryAccountId: string; apiKey: string }): Promise<void> {
  const startHitCount = upstreamHits.length
  accountSideEffects.suppressGatewayAccountLocallyForTest(scenario.primaryAccountId, 30_000, 'mock ai 后备分组优先')
  const startedAt = Date.now()
  const response = await postChat(baseUrl, scenario.apiKey, 'fallback group should bypass wait')
  const elapsedMs = Date.now() - startedAt
  assert.equal(response.status, 200, `主分组全屏蔽时应先切后备分组，实际 HTTP ${response.status}: ${response.text}`)
  assert(elapsedMs < 2_500, `存在可承接后备分组时不应进入恢复巡检等待，实际 ${elapsedMs}ms`)
  assert.deepEqual(authorizationsForKeySince(startHitCount, 'sk-recoverable-fallback-backup'), ['Bearer sk-recoverable-fallback-backup'])
}

async function assertHardUnavailableDoesNotEnterRecoverableWait(baseUrl: string, scenario: { apiKey: string }): Promise<void> {
  const startHitCount = upstreamHits.length
  const startedAt = Date.now()
  const response = await postChat(baseUrl, scenario.apiKey, 'disabled account should fail without recoverable wait')
  const elapsedMs = Date.now() - startedAt
  assert.equal(response.status, 503, `硬不可用账号不应恢复等待，实际 HTTP ${response.status}: ${response.text}`)
  assert.match(response.text, /upstream_retryable_error/, '硬不可用账号耗尽后应返回稳定可重试错误码')
  assert(elapsedMs < 800, `硬不可用账号不应进入本地恢复等待，实际 ${elapsedMs}ms`)
  assert.deepEqual(authorizationsForKeySince(startHitCount, 'sk-recoverable-disabled'), [])
}

async function assertRecoverableWaitTimeoutBranch(): Promise<void> {
  let nowMs = 1_000
  interface FakeTimer { cancelled: boolean }
  const coordinator = new RecoverableUnavailableWaitCoordinator({
    now: () => nowMs,
    setTimer(callback, delayMs) {
      const timer: FakeTimer = { cancelled: false }
      queueMicrotask(() => {
        if (timer.cancelled) return
        nowMs += delayMs
        callback()
      })
      return timer
    },
    clearTimer(timer) {
      const fakeTimer = timer as FakeTimer
      fakeTimer.cancelled = true
    }
  })
  const metadata: Array<{ label?: string; metadata: Record<string, unknown> }> = []
  const auditCapture = {
    addGatewayMetadata(input: { label?: string; metadata: Record<string, unknown> }) {
      metadata.push(input)
    }
  } as unknown as AuditCaptureContext
  const result = await waitForRecoverableUnavailableState({
    scopeKey: 'mock-ai-timeout',
    reason: 'mock_ai_recoverable_timeout',
    initialState: { ready: false, retryAfterMs: 25 },
    refresh: () => ({ ready: false, retryAfterMs: 25 }),
    isReady: (state) => state.ready,
    nextRetryAfterMs: (state) => state.retryAfterMs,
    auditCapture,
    maxWaitMs: 140,
    checkIntervalMs: 40,
    requestStartedAtMs: nowMs,
    coordinator,
    now: () => nowMs
  })
  assert.equal(result.ready, false, '恢复等待 helper 超时分支不应返回 ready')
  assert.equal(result.timedOut, true, '恢复等待 helper 应标记 timedOut')
  assert(result.checkCount >= 2, `恢复等待 helper 超时前应至少巡检两次，实际 ${result.checkCount}`)
  assert(metadata.some((item) => item.label === 'recoverable_unavailable_wait_result'), '恢复等待 helper 应写入结果元数据')
}

function createSingleAccountScenario(label: string, upstreamApiKey: string, upstreamBaseUrl: string): GatewayScenario {
  const group = repositories.createGroup({
    name: `${label}分组`,
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `${label}账户`,
    type: 'api_key',
    credentials: {
      api_key: upstreamApiKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5', 'gpt-5.6-sol']
  }, access)
  activateAccountAfterBackgroundCheck(account.id)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: `${label}网关 Key`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, `${label}网关 Key 未返回明文密钥`)
  return {
    accountId: account.id,
    groupId: group.id,
    apiKey: apiKey.key
  }
}

function createFallbackScenario(upstreamBaseUrl: string): { primaryAccountId: string; apiKey: string } {
  const primaryGroup = repositories.createGroup({
    name: '恢复等待后备分组优先主分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const backupGroup = repositories.createGroup({
    name: '恢复等待后备分组优先备用分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const primary = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '恢复等待后备分组优先主账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-recoverable-fallback-primary',
      base_url: upstreamBaseUrl
    },
    groupId: primaryGroup.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5', 'gpt-5.6-sol']
  }, access)
  const backup = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '恢复等待后备分组优先备用账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-recoverable-fallback-backup',
      base_url: upstreamBaseUrl
    },
    groupId: backupGroup.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5', 'gpt-5.6-sol']
  }, access)
  activateAccountAfterBackgroundCheck(primary.id)
  activateAccountAfterBackgroundCheck(backup.id)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '恢复等待后备分组优先网关 Key',
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, status: 'active' },
      { groupId: backupGroup.id, priority: 2, status: 'active' }
    ],
    status: 'active'
  }, access)
  assert(apiKey.key, '后备分组优先网关 Key 未返回明文密钥')
  return {
    primaryAccountId: primary.id,
    apiKey: apiKey.key
  }
}

function createDisabledScenario(upstreamBaseUrl: string): { apiKey: string } {
  const group = repositories.createGroup({
    name: '恢复等待硬不可用分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '恢复等待硬不可用账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-recoverable-disabled',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'disabled',
    schedulable: false,
    supportedModels: ['gpt-5.5', 'gpt-5.6-sol']
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '恢复等待硬不可用网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '硬不可用网关 Key 未返回明文密钥')
  return { apiKey: apiKey.key }
}

function activateAccountAfterBackgroundCheck(accountId: string): void {
  const changed = repositories.projectAccountHealthFixtureSuccess(accountId, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  assert.equal(changed, true, `后台健康检查激活账户失败：${accountId}`)
}

async function postChat(baseUrl: string, apiKey: string, content: string): Promise<{ status: number; text: string }> {
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content }],
      stream: false
    })
  })
  return {
    status: response.status,
    text: await response.text()
  }
}

function authorizationsSince(startHitCount: number): string[] {
  return upstreamHits.slice(startHitCount).map((hit) => hit.authorization)
}

function authorizationsForKeySince(startHitCount: number, apiKey: string): string[] {
  return authorizationsSince(startHitCount).filter((authorization) => authorization === `Bearer ${apiKey}`)
}

function singleHitForKeySince(startHitCount: number, apiKey: string): MockUpstreamHit {
  const hits = upstreamHits.slice(startHitCount)
    .filter((hit) => hit.authorization === `Bearer ${apiKey}`)
  assert.equal(hits.length, 1, `${apiKey} 应且只应命中一次上游`)
  return hits[0]!
}

function createMockOpenAIUpstream(): http.Server {
  return http.createServer((req, res) => {
    const receivedAtMs = Date.now()
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const path = req.url?.split('?', 1)[0] ?? ''
      upstreamHits.push({
        authorization: String(req.headers.authorization ?? ''),
        path,
        bodyText,
        receivedAtMs
      })
      if (req.method !== 'POST' || path !== '/v1/chat/completions') {
        res.writeHead(404, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { message: 'mock ai path not found' } }))
        return
      }
      const upstreamApiKey = String(req.headers.authorization ?? '').replace(/^Bearer\s+/i, '')
      if (upstreamApiKey === 'sk-recoverable-transport-always-fails') {
        res.destroy()
        return
      }
      if (upstreamApiKey === 'sk-recoverable-transport-failure') {
        const failureCount = transportFailureCounts.get(upstreamApiKey) ?? 0
        transportFailureCounts.set(upstreamApiKey, failureCount + 1)
        if (failureCount === 0) {
          res.destroy()
          return
        }
      }
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({
        id: `chatcmpl-${upstreamApiKey}`,
        object: 'chat.completion',
        choices: [
          {
            index: 0,
            message: { role: 'assistant', content: `mock ai ok from ${upstreamApiKey}` },
            finish_reason: 'stop'
          }
        ],
        usage: {
          prompt_tokens: 5,
          completion_tokens: 3,
          total_tokens: 8
        }
      }))
    })
  })
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  server.listen(0, '127.0.0.1')
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  assert(typeof address === 'object' && address !== null, 'server 未监听端口')
  return { port: address.port }
}

function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server || !server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.close((error) => {
      if (error) rejectPromise(error)
      else resolvePromise()
    })
  })
}
