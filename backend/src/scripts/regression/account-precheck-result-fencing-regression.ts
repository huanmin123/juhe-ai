import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import type { AccountStatus } from '../../domain/types.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-precheck-result-fencing-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'precheck-result-fencing-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [repositories, databaseModule, { handleDbServiceOperation }] = await Promise.all([
  import('../../storage/repositories.js'),
  import('../../storage/database.js'),
  import('../../modules/db-service/db-service-handlers.js')
])

const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  verifyDistributedWriteFenceBoundary()
  await verifyNewerRealSuccessWins()
  await verifyEqualMillisecondSuccessWins()
  await verifyDispatchRevisionFence()
  await verifyStatusFence()
  await verifyCurrentProbeCanStillMutate()
  console.log('account-precheck-result-fencing-regression passed')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function verifyDistributedWriteFenceBoundary(): void {
  const sideEffectsSource = readFileSync(resolve('src/modules/gateway/runtime/account-side-effects.service.ts'), 'utf8')
  const storeSource = readFileSync(resolve('src/shared/runtime-probe-state-store.ts'), 'utf8')
  const distributedPrecheck = sideEffectsSource.slice(
    sideEffectsSource.indexOf('async function runDistributedGatewayAccountPrecheck'),
    sideEffectsSource.indexOf('function promoteRecoveryProbeToPrecheck')
  )
  assert.match(
    distributedPrecheck,
    /renewGenerationRun\([\s\S]*mark_account_precheck_temporary_unavailable/,
    '分布式 precheck 必须在最终 DB 副作用前再做 generation + runId CAS'
  )
  assert.match(
    storeSource,
    /redisRenewProbeGenerationRunScript[\s\S]*decoded\['generation'\][\s\S]*decoded\['probeRunId'\][\s\S]*redis\.call\('SET'/,
    'Redis 续租必须在单条 Lua 内校验 generation + runId 并续租'
  )
}

async function verifyNewerRealSuccessWins(): Promise<void> {
  const fixture = createFixture('新真实成功优先')
  await delay(5)
  const precheckStartedAt = new Date().toISOString()
  await delay(5)
  repositories.createUsageRecordsBatch([{
    traceId: 'precheck-result-fencing-real-success',
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    groupId: fixture.group.id,
    accountId: fixture.account.id,
    endpoint: '/v1/responses',
    providerCode: 'gpt',
    model: 'gpt-5.5',
    stream: false,
    statusCode: 200,
    success: true,
    durationMs: 10,
    createdAt: new Date().toISOString()
  }])
  const afterSuccess = repositories.findAccountSummary(fixture.account.id, adminAccess)
  assert(afterSuccess?.lastHealthSuccessAt && afterSuccess.lastHealthSuccessAt > precheckStartedAt)

  const result = await markPrecheckFailure(fixture, {
    precheckStartedAt,
    expectedDispatchRevision: fixture.gatewayAccount.dispatchRevision ?? 0,
    expectedStatus: fixture.gatewayAccount.status
  })
  assert.deepEqual(result, { updated: false, skippedReason: 'newer_health_success' })
  assert.equal(repositories.findAccountSummary(fixture.account.id, adminAccess)?.status, 'active')
}

async function verifyEqualMillisecondSuccessWins(): Promise<void> {
  const fixture = createFixture('同毫秒成功优先')
  const observedAt = new Date(Date.now() + 1000).toISOString()
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET last_health_success_at = ?, updated_at = ? WHERE id = ?')
    .run(observedAt, observedAt, fixture.account.id)
  const result = await markPrecheckFailure(fixture, {
    precheckStartedAt: observedAt,
    expectedDispatchRevision: fixture.gatewayAccount.dispatchRevision ?? 0,
    expectedStatus: fixture.gatewayAccount.status
  })
  assert.deepEqual(result, { updated: false, skippedReason: 'newer_health_success' })
  assert.equal(repositories.findAccountSummary(fixture.account.id, adminAccess)?.status, 'active')
}

async function verifyDispatchRevisionFence(): Promise<void> {
  const fixture = createFixture('派发版本隔离')
  const result = await markPrecheckFailure(fixture, {
    precheckStartedAt: new Date().toISOString(),
    expectedDispatchRevision: (fixture.gatewayAccount.dispatchRevision ?? 0) + 1,
    expectedStatus: fixture.gatewayAccount.status
  })
  assert.deepEqual(result, { updated: false, skippedReason: 'stale_dispatch_revision' })
  assert.equal(repositories.findAccountSummary(fixture.account.id, adminAccess)?.status, 'active')
}

async function verifyStatusFence(): Promise<void> {
  const fixture = createFixture('状态隔离')
  const result = await markPrecheckFailure(fixture, {
    precheckStartedAt: new Date().toISOString(),
    expectedDispatchRevision: fixture.gatewayAccount.dispatchRevision ?? 0,
    expectedStatus: 'rate_limited'
  })
  assert.deepEqual(result, { updated: false, skippedReason: 'stale_account_status' })
  assert.equal(repositories.findAccountSummary(fixture.account.id, adminAccess)?.status, 'active')
}

async function verifyCurrentProbeCanStillMutate(): Promise<void> {
  const fixture = createFixture('当前探针正常写入')
  const result = await markPrecheckFailure(fixture, {
    precheckStartedAt: new Date().toISOString(),
    expectedDispatchRevision: fixture.gatewayAccount.dispatchRevision ?? 0,
    expectedStatus: fixture.gatewayAccount.status
  })
  assert.deepEqual(result, { updated: true })
  assert.equal(repositories.findAccountSummary(fixture.account.id, adminAccess)?.status, 'temporary_unavailable')
}

function createFixture(name: string) {
  const group = repositories.createGroup({
    name: `${name}分组-${Math.random().toString(16).slice(2, 8)}`,
    providerCode: 'gpt'
  }, adminAccess)
  const created = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `${name}-${Math.random().toString(16).slice(2, 8)}`,
    type: 'api_key',
    groupId: group.id,
    credentials: {
      api_key: `sk-${Math.random().toString(16).slice(2)}`,
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    schedulable: true
  }, adminAccess)
  assert(repositories.recordAccountHealthCheckSuccess(created.id, {
    intervalHours: 24,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }))
  const account = repositories.findAccountSummary(created.id, adminAccess)
  assert(account)
  const gatewayAccount = repositories.findOpenAIAccountForGroup(group.id, created.id, 'sys_admin', { ignoreAvailability: true })
  assert(gatewayAccount)
  assert.equal(gatewayAccount.status, 'active')
  assert(Number.isSafeInteger(gatewayAccount.dispatchRevision) && (gatewayAccount.dispatchRevision ?? 0) > 0)
  return { account, group, gatewayAccount }
}

function markPrecheckFailure(
  fixture: ReturnType<typeof createFixture>,
  fence: {
    precheckStartedAt: string
    expectedDispatchRevision: number
    expectedStatus: AccountStatus
  }
) {
  return handleDbServiceOperation({
    type: 'mark_account_precheck_temporary_unavailable',
    account: fixture.gatewayAccount,
    reason: '模拟迟到的探针传输失败',
    ...fence
  })
}

function delay(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}
