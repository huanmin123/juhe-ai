import assert from 'node:assert/strict'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import type { GatewaySettings } from '../../modules/gateway/policy/account-error-policy.service.js'
import {
  fetchFirstAvailableUpstream,
  type GatewayUpstreamRequestCoordinationContext
} from '../../modules/gateway/dispatch/upstream-dispatch.js'
import type { AuditCaptureContext } from '../../modules/gateway/audit/capture.service.js'
import type { GatewayUsageContext } from '../../modules/gateway/usage/records.js'
import {
  GatewayRequestAttemptTracker,
  GatewayRequestWallBudget,
  RouteCoordinationBudget
} from '../../modules/gateway/routing/route-coordination.js'
import { ServerRetryBudget } from '../../modules/gateway/runtime/server-retry-budget.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-upstream-policy-framing-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'gateway-upstream-policy-framing-secret'
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
  databaseModule,
  repositories,
  usageRecordQueue,
  accountSideEffects,
  accountCircuit,
  readWorkerPool
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/runtime/account-circuit.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const settings: GatewaySettings = {
  gatewayTextRawBodyLimitMegabytes: 8,
  accountCircuitConfirmationFailuresRequired: 2,
  defaultTemporaryUnschedulableMinutes: 5,
  temporaryUnschedulableRetryIntervalSeconds: 0,
  temporaryUnschedulableRetryAttempts: 0,
  streamCircuitBreakerEnabled: true,
  textFirstResponseTimeoutSeconds: 30,
  textStreamIdleTimeoutSeconds: 30,
  textUncommittedAttemptMaxLifetimeSeconds: 60,
  imageFirstResponseTimeoutSeconds: 600,
  imageStreamIdleTimeoutSeconds: 120,
  imageUncommittedAttemptMaxLifetimeSeconds: 900,
  imageRequestWallTimeoutSeconds: 900,
  noAvailableAccountWaitTimeoutSeconds: 1,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 5
}
const model = 'gpt-5.5'
const request = buildRequest()
const usageContext: GatewayUsageContext = {
  traceId: 'trace-policy-framing',
  trafficSource: 'gateway',
  clientIp: '198.51.100.44',
  systemAccountId: 'sys_admin',
  apiKeyId: 'gateway-key-policy-framing',
  groupId: 'group-policy-framing',
  endpoint: 'POST /v1/chat/completions',
  requestSnapshot: {
    method: 'POST',
    path: '/v1/chat/completions',
    originalUrl: '/v1/chat/completions',
    traceId: 'trace-policy-framing',
    headers: { 'x-session-id': 'policy-framing-session' },
    body: request.body
  }
}
const auditCapture = {
  startAttempt: () => 'policy-framing-attempt',
  completeAttempt: () => undefined,
  addGatewayMetadata: () => undefined,
  recordFailedDispatchAttempt: () => 'policy-framing-failed-dispatch'
} as unknown as AuditCaptureContext
const policyScenarios = [
  { action: 'retry_next', expectedStatus: 'active' },
  { action: 'rate_limited', expectedStatus: 'rate_limited' },
  { action: 'temp_unschedulable', expectedStatus: 'temporary_unavailable' },
  { action: 'error_disabled', expectedStatus: 'error' }
] as const

let upstream: http.Server | undefined
try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  accountCircuit.resetGatewayAccountCircuitStoreForTest()

  const group = repositories.createGroup({
    name: 'policy framing group',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const upstreamBaseUrl = await startMockUpstream((server) => { upstream = server })
  const circuitService = accountCircuit.getGatewayAccountCircuitService()
  for (const [index, scenario] of policyScenarios.entries()) {
    const accountSummary = repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `policy framing account ${scenario.action}`,
      type: 'api_key',
      credentials: {
        api_key: `sk-policy-framing-${scenario.action}`,
        base_url: upstreamBaseUrl,
        error_handling_rules: [{
          enabled: true,
          name: `用户显式 429 ${scenario.action}`,
          priority: 1,
          status_codes: [429],
          keywords: ['explicit-policy-marker'],
          action: scenario.action,
          reset_strategy: 'duration',
          duration_hours: 1
        }]
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      concurrencyLimit: 4,
      priority: 0,
      supportedModels: [model]
    }, access)
    const backupSummary = repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `policy framing backup ${scenario.action}`,
      type: 'api_key',
      credentials: {
        api_key: `sk-policy-framing-backup-${scenario.action}`,
        base_url: upstreamBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      concurrencyLimit: 4,
      priority: 100,
      supportedModels: [model]
    }, access)
    assert.equal(repositories.projectAccountHealthFixtureSuccess(accountSummary.id, {
      intervalHours: 12,
      jitterMinutes: 0,
      failureThreshold: 3,
      statusCode: 200
    }), true)
    assert.equal(repositories.projectAccountHealthFixtureSuccess(backupSummary.id, {
      intervalHours: 12,
      jitterMinutes: 0,
      failureThreshold: 3,
      statusCode: 200
    }), true)
    const account = repositories.listOpenAIAccountsForGroup(group.id, access.systemAccountId, {
      requestedModel: model
    }).find((candidate) => candidate.id === accountSummary.id)
    const backup = repositories.listOpenAIAccountsForGroup(group.id, access.systemAccountId, {
      requestedModel: model
    }).find((candidate) => candidate.id === backupSummary.id)
    assert(account, 'Mock 账户必须可被派发查询')
    assert(backup, 'Mock 后备账户必须可被派发查询')

    const circuitScope = accountCircuit.gatewayAccountProtocolModelScope(account, 'text', model)
    const seedEvidenceKey = index.toString(16).padStart(64, 'a')
    const seed = await accountCircuit.getGatewayAccountCircuitStore().suspect({
      scope: circuitScope,
      dispatchRevision: accountCircuit.accountCircuitDispatchRevision(account),
      transitionId: `policy-framing-seed-${scenario.action}`,
      confirmationFailuresRequired: 2,
      reason: 'transport:seed',
      failureEvidenceKey: seedEvidenceKey,
      nowMs: Date.now() - 3_001
    })
    assert.equal(seed.status, 'applied', '测试前必须建立已到期 SUSPECT')
    assert.equal((await accountCircuit.getGatewayAccountCircuitStore().get(circuitScope)).phase, 'SUSPECT')

    const dispatchResult = await fetchFirstAvailableUpstream(
        request,
        [account, backup],
        settings,
        usageContext,
        auditCapture,
        undefined,
        new AbortController().signal,
        undefined,
        'text',
        undefined,
        true,
        undefined,
        undefined,
        undefined,
        false,
        createRequestCoordination(scenario.action),
        false,
        false
      )
    assert.equal(dispatchResult.account.id, backup.id, `${scenario.action} 命中后必须切换到后备账户`)
    assert.equal(dispatchResult.response.status, 200, `${scenario.action} 切号后必须返回后备账户成功响应`)
    for await (const _chunk of dispatchResult.response.body ?? []) {
      // Drain the successful response before releasing its concurrency slot.
    }
    dispatchResult.releaseConcurrency()

    const circuitState = await accountCircuit.getGatewayAccountCircuitStore().get(circuitScope)
    assert.equal(circuitState.phase, 'RECOVERING', `${scenario.action} 完整 HTTP framing 必须结算 SUSPECT confirmation`)
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const persisted = repositories.findAccountForTest(account.id, access)
    assert.equal(persisted?.status, scenario.expectedStatus, `${scenario.action} 业务状态必须独立于 transport framing 持久化`)
    if (scenario.action === 'rate_limited') {
      assert.ok(persisted?.cooldownUntil, '用户显式冷却策略必须保留 cooldownUntil')
    }
  }
  console.log('gateway upstream policy framing mock ai regression passed (retry_next, rate_limited, temp_unschedulable, error_disabled)')
} finally {
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  accountCircuit.resetGatewayAccountCircuitStoreForTest()
  await readWorkerPool.closeSqliteReadWorkerPool()
  databaseModule.closeStorageDatabases()
  await closeServer(upstream)
  rmSync(tempRoot, { recursive: true, force: true })
}

function createRequestCoordination(label: string): GatewayUpstreamRequestCoordinationContext {
  return {
    scope: 'gateway_request',
    serverRetryBudget: new ServerRetryBudget(1_000),
    gatewayRequestWallBudget: new GatewayRequestWallBudget({ requestAcceptedAtMs: Date.now() }),
    routeCoordinationBudget: new RouteCoordinationBudget({ requestId: `policy-framing-request-${label}` }),
    requestAttemptTracker: new GatewayRequestAttemptTracker()
  }
}

function buildRequest(): Request {
  const headers = new Map<string, string>([
    ['content-type', 'application/json'],
    ['x-session-id', 'policy-framing-session']
  ])
  return {
    method: 'POST',
    originalUrl: '/v1/chat/completions',
    path: '/chat/completions',
    headers: Object.fromEntries(headers),
    body: { model, messages: [{ role: 'user', content: 'framing' }] },
    header(name: string) {
      return headers.get(name.toLowerCase())
    }
  } as unknown as Request
}

async function startMockUpstream(onServer: (server: http.Server) => void): Promise<string> {
  const server = http.createServer((req, res) => {
    req.resume()
    if (String(req.headers.authorization ?? '').includes('sk-policy-framing-backup-')) {
      res.writeHead(200, {
        'content-type': 'application/json; charset=utf-8'
      })
      res.end(JSON.stringify({
        id: 'policy-framing-backup-success',
        choices: [{ message: { role: 'assistant', content: 'backup success' } }]
      }))
      return
    }
    res.writeHead(429, {
      'content-type': 'application/json; charset=utf-8'
    })
    res.end(JSON.stringify({ error: { message: 'explicit-policy-marker' } }))
  })
  onServer(server)
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.once('error', rejectPromise)
    server.listen(0, '127.0.0.1', () => resolvePromise())
  })
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('Mock 上游未监听')
  return `http://127.0.0.1:${address.port}/v1`
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server) return
  await new Promise<void>((resolvePromise) => server.close(() => resolvePromise()))
}
