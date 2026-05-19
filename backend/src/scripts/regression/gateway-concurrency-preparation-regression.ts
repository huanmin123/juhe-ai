import { strict as assert } from 'node:assert'
import http from 'node:http'
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

let holdAndReleaseServer: http.Server | undefined

try {
  clearAccountConcurrency()
  const holdAndReleaseUpstream = await createHoldAndReleaseServer()
  holdAndReleaseServer = holdAndReleaseUpstream.server
  const retryAccount = buildAccount({
    id: 'acct_retry',
    name: '短等后继续账号',
    concurrencyLimit: 1,
    type: 'api_key',
    baseUrl: holdAndReleaseUpstream.baseUrl
  })
  const retrySlot = tryAcquireAccountConcurrency(retryAccount.id, retryAccount.concurrencyLimit)
  assert.equal(retrySlot.acquired, true, '短等回归前应成功占用账号并发槽')
  setTimeout(() => retrySlot.release(), 950)

  const retryResult = await fetchFirstAvailableUpstream(
    buildRequest(),
    [retryAccount],
    settings,
    usageContext,
    auditCapture,
    undefined,
    new AbortController().signal
  )
  assert.equal(retryResult.account.id, retryAccount.id, '账号并发短等后应继续使用原账号')
  assert.equal(retryResult.upstreamUrl, `${retryAccount.baseUrl}/chat/completions`, '账号并发短等后应命中同一上游地址')
  assert.equal(retryResult.response.status, 200, '账号并发短等后应成功拿到上游响应')
  for await (const _chunk of retryResult.response.body ?? []) {
  }
  retryResult.releaseConcurrency()
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
  console.log('网关并发准备回归通过：短等释放的账号会继续复用，持续饱和的账号会记录失败用量并退出')
} finally {
  clearAccountConcurrency()
  holdAndReleaseServer?.close()
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

async function createHoldAndReleaseServer(): Promise<{ server: http.Server; baseUrl: string }> {
  const server = http.createServer((_req, res) => {
    res.writeHead(200, {
      'content-type': 'application/json; charset=utf-8'
    })
    res.end(JSON.stringify({
      id: 'resp_concurrency_retry',
      object: 'response'
    }))
  })
  await new Promise<void>((resolvePromise) => {
    server.listen(0, '127.0.0.1', () => resolvePromise())
  })
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('无法启动并发回归测试上游服务器')
  }
  return {
    server,
    baseUrl: `http://127.0.0.1:${address.port}/v1`
  }
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
