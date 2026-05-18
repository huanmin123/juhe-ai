import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { clearAccountConcurrency, tryAcquireAccountConcurrency } from '../../shared/account-concurrency.js'
import { fetchFirstAvailableUpstream, UpstreamAttemptError } from '../../modules/gateway/openai-gateway-upstream-dispatch.js'
import type { GatewaySettings } from '../../modules/gateway/account-error-policy.service.js'
import type { AuditCaptureContext } from '../../modules/gateway/audit-capture.service.js'
import type { GatewayUsageContext } from '../../modules/gateway/openai-gateway-usage-records.js'
import type { UpstreamAccount } from '../../modules/gateway/openai-gateway-route-helpers.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-concurrency-preparation-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.secret = 'gateway-concurrency-preparation-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, usageRecordQueue] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/gateway/usage-record-queue.service.js')
])

const settings: GatewaySettings = {
  defaultTemporaryUnschedulableMinutes: 5,
  temporaryUnschedulableRetryIntervalSeconds: 0,
  temporaryUnschedulableRetryAttempts: 0,
  streamCircuitBreakerEnabled: false,
  streamRequestTimeoutSeconds: 180,
  streamIdleTimeoutSeconds: 60,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 10
}

const usageContext: GatewayUsageContext = {
  traceId: 'trace_gateway_concurrency_preparation',
  clientIp: '127.0.0.1',
  systemAccountId: 'sys_admin',
  groupId: 'grp_gateway_concurrency_preparation',
  endpoint: 'POST /v1/chat/completions',
  requestSnapshot: {
    method: 'POST',
    path: '/v1/chat/completions',
    originalUrl: '/v1/chat/completions',
    traceId: 'trace_gateway_concurrency_preparation',
    headers: {},
    body: { model: 'gpt-5.5', messages: [{ role: 'user', content: 'hi' }] }
  }
}

const auditCapture = {
  startAttempt: () => '',
  completeAttempt: () => undefined,
  addGatewayMetadata: () => undefined
} as unknown as AuditCaptureContext

try {
  clearAccountConcurrency()
  const saturatedAccount = buildAccount({
    id: 'acct_saturated',
    name: '饱和账号',
    concurrencyLimit: 1,
    type: 'api_key',
    baseUrl: 'https://should-not-build.example'
  })
  const heldSlot = tryAcquireAccountConcurrency(saturatedAccount.id, saturatedAccount.concurrencyLimit)
  assert.equal(heldSlot.acquired, true, '测试前应成功占用账号并发槽')

  await assert.rejects(
    fetchFirstAvailableUpstream(
      buildRequest(),
      [saturatedAccount],
      settings,
      usageContext,
      auditCapture,
      undefined,
      new AbortController().signal
    ),
    (error: unknown) => error instanceof UpstreamAttemptError
      && error.lastAttempt?.upstreamUrl === 'concurrency:limit'
      && error.message.includes('账户并发已达到上限')
  )

  usageRecordQueue.flushAllUsageRecordQueue()
  const usage = databaseModule.getRecordDatabase()
    .prepare('SELECT account_id, success, error_message FROM usage_records WHERE account_id = ? ORDER BY created_at DESC, id DESC LIMIT 1')
    .get(saturatedAccount.id) as { account_id?: string; success?: number; error_message?: string } | undefined
  assert.equal(usage?.account_id, saturatedAccount.id, '账号并发满也应写入失败使用记录')
  assert.equal(usage?.success, 0, '账号并发满使用记录应标记失败')
  assert.match(usage?.error_message ?? '', /并发已达到上限/, '账号并发满使用记录应保留错误原因')

  heldSlot.release()
  clearAccountConcurrency()
  console.log('网关并发准备回归通过：饱和账号不会进入上游准备阶段，并会记录失败用量')
} finally {
  clearAccountConcurrency()
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function buildRequest(): Parameters<typeof fetchFirstAvailableUpstream>[0] {
  return {
    method: 'POST',
    originalUrl: '/v1/chat/completions',
    path: '/chat/completions',
    headers: { 'content-type': 'application/json' },
    body: {
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'hi' }]
    },
    header(name: string): string | undefined {
      const value = this.headers[name.toLowerCase()]
      return Array.isArray(value) ? value.join(', ') : value
    }
  } as Parameters<typeof fetchFirstAvailableUpstream>[0]
}

function buildAccount(input: {
  id: string
  name: string
  concurrencyLimit: number
  type: 'api_key' | 'oauth'
  baseUrl: string
}): UpstreamAccount {
  return {
    id: input.id,
    systemAccountId: 'sys_admin',
    name: input.name,
    type: input.type,
    status: 'active',
    apiKey: 'sk-concurrency-preparation',
    baseUrl: input.baseUrl,
    credentials: {
      api_key: 'sk-concurrency-preparation',
      base_url: input.baseUrl
    },
    concurrencyLimit: input.concurrencyLimit,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    passthroughEnabled: false,
    streamFailureCount: 0,
    accountOwnerSystemAccountId: 'sys_admin',
    groupOwnerSystemAccountId: 'sys_admin',
    accountAccessType: 'owner',
    groupAccessType: 'owner'
  }
}
