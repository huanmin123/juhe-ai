import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-upstream-request-failure-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'upstream-request-failure.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'upstream-request-failure-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  readWorkerPool,
  repositories,
  gatewayCache,
  accountSideEffects,
  gatewayFailureDispatch,
  gatewayHotQuality,
  accountCircuit,
  accountRuntimeKeys,
  gatewayBody,
  storageCrypto,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/response/failure-dispatch.js'),
  import('../../modules/gateway/runtime/hot-quality-runtime.service.js'),
  import('../../modules/gateway/runtime/account-circuit.service.js'),
  import('../../modules/gateway/runtime/account-runtime-keys.js'),
  import('../../modules/gateway/upstream/body.js'),
  import('../../storage/crypto.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const timeoutError = Object.assign(new Error(''), { code: 'ETIMEDOUT' })
assert.equal(
  gatewayFailureDispatch.formatUpstreamRequestErrorMessage(timeoutError),
  '请求失败：ETIMEDOUT',
  '空错误消息应回退到错误码，避免最后一次尝试文案只剩空白'
)
assert.equal(typeof gatewayFailureDispatch.handleUpstreamRequestError, 'function', '传输失败必须由统一请求错误处理器接管')
assert.equal(
  gatewayBody.isProvenUpstreamBodyTransportError(new Error('本地响应转换失败')),
  false,
  '没有正文读取来源证据的本地 post-header/transform 异常不得伪装成上游 transport'
)
try {
  await gatewayBody.readUpstreamBodyLimited((async function* () {
    throw new Error('mock upstream body reset')
  })())
  assert.fail('正文读取中断夹具必须抛错')
} catch (error) {
  assert.equal(
    gatewayBody.isProvenUpstreamBodyTransportError(error),
    true,
    '正文读取边界包装后的 incomplete error 必须保留已开始上游 body transport 证据'
  )
}

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const opaqueFailureBody = JSON.stringify({
  error: {
    message: 'Invalid value for model level: expected one of low, medium, high.',
    type: 'invalid_request_error',
    code: null
  }
})

async function main(): Promise<void> {
  let appServer: http.Server | undefined
  let upstreamServer: http.Server | undefined
  let closedTransportServer: http.Server | undefined
  try {
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
    auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)

    const upstreamAuthorizations: string[] = []
    upstreamServer = http.createServer((req, res) => {
      upstreamAuthorizations.push(String(req.headers.authorization ?? ''))
      if (req.url?.includes('mock_unsupported_content_encoding=1')) {
        res.writeHead(200, {
          'content-type': 'application/json; charset=utf-8',
          'content-encoding': 'x-juhe-local-unsupported'
        })
        res.end('{"choices":[{"message":{"role":"assistant","content":"must not be decoded"}}]}')
        return
      }
      if (req.url?.includes('mock_side_effect_transport_drop=1')) {
        req.socket.destroy()
        return
      }
      res.writeHead(422, {
        'content-type': 'application/json; charset=utf-8',
        'x-upstream-contract': 'opaque'
      })
      res.end(opaqueFailureBody)
    })
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`

    const opaqueGroup = repositories.createGroup({ name: '不透明非幂等失败回归分组', providerCode: 'gpt', enabled: true }, access)
    const opaqueAccounts = [
      createAccount(opaqueGroup.id, '01-不透明失败账户', 'sk-opaque-failure-first', upstreamBaseUrl, true),
      createAccount(opaqueGroup.id, '02-不透明后备账户', 'sk-opaque-failure-fallback', upstreamBaseUrl)
    ]
    const opaqueApiKey = createRegressionApiKey(opaqueGroup.id, 'sk-opaque-request-failure-regression')

    const sideEffectTransportGroup = repositories.createGroup({ name: '副作用传输失败不可重放分组', providerCode: 'gpt', enabled: true }, access)
    const sideEffectTransportAccounts = [
      createAccount(sideEffectTransportGroup.id, '01-副作用传输失败账户', 'sk-side-effect-transport-first', upstreamBaseUrl),
      createAccount(sideEffectTransportGroup.id, '02-副作用传输失败后备账户', 'sk-side-effect-transport-fallback', upstreamBaseUrl)
    ]
    const sideEffectTransportApiKey = createRegressionApiKey(sideEffectTransportGroup.id, 'sk-side-effect-transport-regression')

    closedTransportServer = http.createServer()
    await listen(closedTransportServer)
    const closedTransportBaseUrl = `http://127.0.0.1:${serverPort(closedTransportServer)}/v1`
    await closeServer(closedTransportServer)
    closedTransportServer = undefined
    const transportGroup = repositories.createGroup({ name: '不透明传输失败回归分组', providerCode: 'gpt', enabled: true }, access)
    const transportAccounts = [
      createAccount(transportGroup.id, '01-传输失败账户', 'sk-transport-failure-first', closedTransportBaseUrl),
      createAccount(transportGroup.id, '02-传输失败后备账户', 'sk-transport-failure-fallback', closedTransportBaseUrl)
    ]
    const transportApiKey = createRegressionApiKey(transportGroup.id, 'sk-opaque-transport-failure-regression')

    const localDispatchFailureGroup = repositories.createGroup({ name: '本地派发异常不得切号分组', providerCode: 'gpt', enabled: true }, access)
    const localDispatchFailureAccounts = [
      createAccount(localDispatchFailureGroup.id, '01-本地派发异常账户', 'sk-local-dispatch-first', upstreamBaseUrl, false, 0),
      createAccount(localDispatchFailureGroup.id, '02-本地派发异常后备账户', 'sk-local-dispatch-fallback', upstreamBaseUrl, false, 100)
    ]
    const localDispatchFailureApiKey = createRegressionApiKey(localDispatchFailureGroup.id, 'sk-local-dispatch-failure-regression')
    const localDispatchFailurePrimary = localDispatchFailureAccounts[0]!
    databaseModule.getBusinessDatabase().prepare(`
      UPDATE accounts
      SET credentials_encrypted = ?,
          config_revision = config_revision + 1
      WHERE id = ?
    `).run(storageCrypto.encryptJson({
      api_keys: ['sk-local-dispatch-first-a', 'sk-local-dispatch-first-b'],
      base_url: 'file:///gateway-local-dispatch-must-not-be-transport'
    }), localDispatchFailurePrimary.id)

    const localPostHeaderFailureGroup = repositories.createGroup({ name: '响应头后本地异常不得切号分组', providerCode: 'gpt', enabled: true }, access)
    const localPostHeaderFailureAccounts = [
      createAccount(localPostHeaderFailureGroup.id, '01-响应头后本地异常账户', 'sk-local-post-header-first', upstreamBaseUrl, false, 0),
      createAccount(localPostHeaderFailureGroup.id, '02-响应头后本地异常后备账户', 'sk-local-post-header-fallback', upstreamBaseUrl, false, 100)
    ]
    const localPostHeaderFailureApiKey = createRegressionApiKey(localPostHeaderFailureGroup.id, 'sk-local-post-header-failure-regression')

    gatewayCache.clearGatewayRuntimeCache()
    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverPort(appServer)}`

    const invalidJsonHitsBefore = upstreamAuthorizations.length
    const invalidJsonResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: { authorization: `Bearer ${opaqueApiKey.key}`, 'content-type': 'application/json' },
      body: '{"model":"gpt-4o-mini",'
    })
    const invalidJsonText = await invalidJsonResponse.text()
    assert.equal(invalidJsonResponse.status, 400, `无效 JSON 应由网关直接拒绝：${invalidJsonText}`)
    assert.match(invalidJsonText, /请求体不是合法 JSON/)
    assert.equal(upstreamAuthorizations.length, invalidJsonHitsBefore, '无效 JSON 不应命中上游')

    const opaqueResponse = await fetch(`${baseUrl}/v1/audio/speech`, {
      method: 'POST',
      headers: { authorization: `Bearer ${opaqueApiKey.key}`, 'content-type': 'application/json' },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        purpose: 'assistants',
        content: 'opaque resource creation failure must not replay'
      })
    })
    const opaqueResponseText = await opaqueResponse.text()
    assert.equal(opaqueResponse.status, 503, `副作用 POST 的未知完整上游结果应返回稳定网关错误：${opaqueResponseText}`)
    assert.match(opaqueResponseText, /upstream_outcome_unknown/, '副作用 POST 不得把供应商错误当作客户端重试语义')
    assert.doesNotMatch(opaqueResponseText, /Invalid value for model level|invalid_request_error|422/, '副作用 POST 不得泄漏供应商状态或错误正文')
    assert.equal(opaqueResponse.headers.get('x-upstream-contract'), null, '副作用 POST 不得泄漏不透明上游响应头')
    assert.deepEqual(upstreamAuthorizations, ['Bearer sk-opaque-failure-first'], '完整 HTTP 非 2xx 后不得换 Key 或跨账号重放 POST')
    assertAccountsRemainAvailable(opaqueAccounts, '不透明 HTTP 失败')
    const opaqueAccount = opaqueAccounts[0]!
    const opaqueQuality = await gatewayHotQuality.getGatewayHotQualityRuntime().hotQualityStore.get({
      accountRuntimeKey: accountRuntimeKeys.gatewayAccountRuntimeKey(opaqueAccount),
      protocolProfile: opaqueAccount.providerProtocolProfileId ?? GPT_OPENAI_V1_PROFILE_ID,
      requestLane: 'text',
      modelFamily: gatewayHotQuality.gatewayHotQualityModelFamily('gpt-4o-mini')
    })
    assert.ok(opaqueQuality, '透明返回的非 2xx 也必须完成热质量 attempt 诊断闭环')
    assert.equal(opaqueQuality.window5m.completedResponses, 0, '透明返回的非 2xx 不得记为 completed_response')
    assert.equal(opaqueQuality.window5m.upstreamResponseFailures, 1, '透明返回的非 2xx 只能增加 opaque 诊断计数')
    assert.equal(opaqueQuality.window5m.qualityAttempts, 0, 'opaque HTTP 不得进入跨请求 reliability 评分')
    assert.equal(opaqueQuality.window5m.firstByteSampleCount, 0, 'opaque HTTP 不得进入跨请求速度评分')
    assert.equal(opaqueQuality.window5m.lastFailureAtMs, undefined, 'opaque HTTP 不得改变候选探索的新鲜度排序')
    assert.equal(opaqueQuality.sampleState, 'cold', '只有 opaque HTTP 诊断的账户必须保持 cold 中性状态')
    assert.equal(opaqueQuality.effectiveReliability, 0.5, 'opaque HTTP 诊断不得偏移候选排序的中性可靠性')

    const sideEffectTransportOffset = upstreamAuthorizations.length
    const sideEffectTransportResponse = await fetch(`${baseUrl}/v1/audio/speech?mock_side_effect_transport_drop=1`, {
      method: 'POST',
      headers: { authorization: `Bearer ${sideEffectTransportApiKey.key}`, 'content-type': 'application/json' },
      body: JSON.stringify({ model: 'gpt-4o-mini', input: 'already dispatched side effect must not replay' })
    })
    const sideEffectTransportText = await sideEffectTransportResponse.text()
    assert.equal(sideEffectTransportResponse.status, 503, sideEffectTransportText)
    assert.match(sideEffectTransportText, /upstream_outcome_unknown/)
    assert.deepEqual(
      upstreamAuthorizations.slice(sideEffectTransportOffset),
      ['Bearer sk-side-effect-transport-first'],
      'audio/files 等副作用 POST 一旦 dispatch，transport 失败不得自动换 Key 或账户重放'
    )
    assertAccountsRemainAvailable(sideEffectTransportAccounts, '副作用 transport 失败')

    const transportCircuitSizeBefore = await accountCircuit.getGatewayAccountCircuitStore().size()
    const transportResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: { authorization: `Bearer ${transportApiKey.key}`, 'content-type': 'application/json' },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'opaque transport failure must remain request scoped' }],
        stream: false
      })
    })
    const transportResponseText = await transportResponse.text()
    assert.equal(transportResponse.status, 503, `未收到上游响应头的传输失败应返回统一网关错误：${transportResponseText}`)
    assert.match(transportResponseText, /upstream_retryable_error/)
    assert.ok(
      await accountCircuit.getGatewayAccountCircuitStore().size() > transportCircuitSizeBefore,
      '真实已经派发的连接失败仍必须进入账户传输电路'
    )
    assertAccountsRemainAvailable(transportAccounts, '不透明传输失败')

    const localDispatchFailureCircuitSizeBefore = await accountCircuit.getGatewayAccountCircuitStore().size()
    const localDispatchFailureOffset = upstreamAuthorizations.length
    const localDispatchFailureResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: { authorization: `Bearer ${localDispatchFailureApiKey.key}`, 'content-type': 'application/json' },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'local URL validation must not rotate any key or account' }],
        stream: false
      })
    })
    const localDispatchFailureText = await localDispatchFailureResponse.text()
    assert.equal(localDispatchFailureResponse.status, 503, `本地派发异常应返回稳定网关错误：${localDispatchFailureText}`)
    assert.deepEqual(
      upstreamAuthorizations.slice(localDispatchFailureOffset),
      [],
      '上游 URL 本地校验失败时不得尝试同账户兄弟 Key 或后备账户'
    )
    assert.equal(
      await accountCircuit.getGatewayAccountCircuitStore().size(),
      localDispatchFailureCircuitSizeBefore,
      '未真正发往上游的本地异常不得创建账户传输电路 incident'
    )
    assertAccountsRemainAvailable(localDispatchFailureAccounts, '本地派发异常')
    const localDispatchPrimaryQuality = await gatewayHotQuality.getGatewayHotQualityRuntime().hotQualityStore.get({
      accountRuntimeKey: accountRuntimeKeys.gatewayAccountRuntimeKey(localDispatchFailurePrimary),
      protocolProfile: localDispatchFailurePrimary.providerProtocolProfileId ?? GPT_OPENAI_V1_PROFILE_ID,
      requestLane: 'text',
      modelFamily: gatewayHotQuality.gatewayHotQualityModelFamily('gpt-4o-mini')
    })
    assert.ok(localDispatchPrimaryQuality, '本地派发异常也必须中性结束本次热质量 attempt')
    assert.equal(localDispatchPrimaryQuality.window5m.attempts, 1)
    assert.equal(localDispatchPrimaryQuality.window5m.unknownOutcomes, 1)
    assert.equal(localDispatchPrimaryQuality.window5m.localTransportFailures, 0, '本地派发异常不得伪装成 transport failure')
    assert.equal(localDispatchPrimaryQuality.window5m.timeouts, 0, '本地派发异常不得伪装成 timeout')
    const localDispatchBackup = localDispatchFailureAccounts[1]!
    assert.equal(await gatewayHotQuality.getGatewayHotQualityRuntime().hotQualityStore.get({
      accountRuntimeKey: accountRuntimeKeys.gatewayAccountRuntimeKey(localDispatchBackup),
      protocolProfile: localDispatchBackup.providerProtocolProfileId ?? GPT_OPENAI_V1_PROFILE_ID,
      requestLane: 'text',
      modelFamily: gatewayHotQuality.gatewayHotQualityModelFamily('gpt-4o-mini')
    }), undefined, '本地异常不得启动后备账户热质量 attempt')

    const localPostHeaderCircuitSizeBefore = await accountCircuit.getGatewayAccountCircuitStore().size()
    const localPostHeaderOffset = upstreamAuthorizations.length
    const localPostHeaderResponse = await fetch(`${baseUrl}/v1/chat/completions?mock_unsupported_content_encoding=1`, {
      method: 'POST',
      headers: { authorization: `Bearer ${localPostHeaderFailureApiKey.key}`, 'content-type': 'application/json' },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'local post-header decoder failure must remain local' }],
        stream: false
      })
    })
    const localPostHeaderText = await localPostHeaderResponse.text()
    assert.equal(localPostHeaderResponse.status, 503, `响应头后的本地解码异常应返回稳定网关错误：${localPostHeaderText}`)
    assert.deepEqual(
      upstreamAuthorizations.slice(localPostHeaderOffset),
      ['Bearer sk-local-post-header-first'],
      '响应头后的本地解码/转换异常不得换 Key 或切到后备账户'
    )
    assert.equal(
      await accountCircuit.getGatewayAccountCircuitStore().size(),
      localPostHeaderCircuitSizeBefore,
      '响应头后的本地解码/转换异常不得创建账户 transport circuit incident'
    )
    assertAccountsRemainAvailable(localPostHeaderFailureAccounts, '响应头后本地异常')
    const localPostHeaderPrimary = localPostHeaderFailureAccounts[0]!
    const localPostHeaderQuality = await gatewayHotQuality.getGatewayHotQualityRuntime().hotQualityStore.get({
      accountRuntimeKey: accountRuntimeKeys.gatewayAccountRuntimeKey(localPostHeaderPrimary),
      protocolProfile: localPostHeaderPrimary.providerProtocolProfileId ?? GPT_OPENAI_V1_PROFILE_ID,
      requestLane: 'text',
      modelFamily: gatewayHotQuality.gatewayHotQualityModelFamily('gpt-4o-mini')
    })
    assert.ok(localPostHeaderQuality, '响应头后本地异常必须中性结束当前热质量 attempt')
    assert.equal(localPostHeaderQuality.window5m.unknownOutcomes, 1)
    assert.equal(localPostHeaderQuality.window5m.localTransportFailures, 0)
    assert.equal(localPostHeaderQuality.window5m.timeouts, 0)

    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    assert.equal(Object.keys(accountSideEffects.snapshotGatewayAccountRuntimeAvailability()).length, 0, '不透明用户请求失败不得写账户运行态屏障')
    console.log('上游请求失败回归通过：副作用 POST 不重放，真实传输失败保持请求级切号，本地 URL/派发/响应头后转换异常不轮换 Key/账户且不污染共享状态')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    auditLogQueue.flushAllAuditLogQueue()
    await closeServer(appServer)
    await closeServer(upstreamServer)
    await closeServer(closedTransportServer)
    try {
      await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
      databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    await removeTempRoot()
  }
}

function createAccount(
  groupId: string,
  name: string,
  apiKey: string,
  baseUrl: string,
  withBodyConstrainedRule = false,
  priority = 0
) {
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name,
    type: 'api_key',
    credentials: {
      api_key: apiKey,
      base_url: baseUrl,
      error_handling_rules: withBodyConstrainedRule
        ? [{
            enabled: true,
            name: '仅匹配用户指定正文',
            priority: 1,
            status_codes: [422],
            keywords: ['configured-body-marker'],
            action: 'retry_next'
          }]
        : undefined
    },
    groupId,
    supportedModels: ['gpt-4o-mini'],
    status: 'active',
    schedulable: true,
    priority
  }, access)
  repositories.recordAccountHealthCheckSuccess(account.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  return account
}

function createRegressionApiKey(groupId: string, key: string) {
  return createApiKeyRecordWithRouteStrategy(repositories, {
    name: `${key}-name`,
    groupBindings: [{ groupId, priority: 1, status: 'active' }],
    status: 'active',
    description: 'upstream request failure regression'
  }, access)
}

function assertAccountsRemainAvailable(accounts: Array<{ id: string; name: string }>, reason: string): void {
  for (const account of accounts) {
    const current = repositories.findAccountForTest(account.id, access)
    assert.equal(current?.status, 'active', `${reason}：${account.name} 应保持 active`)
    assert.equal(current?.schedulable, true, `${reason}：${account.name} 应保持可调度`)
    assert.equal(current?.apiKeyRuntime?.temporaryUnavailable ?? 0, 0, `${reason}：${account.name} 的 Key 不应被写为临时不可用`)
  }
}

async function listen(server: http.Server): Promise<void> {
  await new Promise<void>((resolveListen, rejectListen) => {
    server.once('error', rejectListen)
    server.listen(0, '127.0.0.1', () => {
      server.off('error', rejectListen)
      resolveListen()
    })
  })
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolveClose) => server.close(() => resolveClose()))
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(address && typeof address === 'object', 'server address unavailable')
  return address.port
}

async function removeTempRoot(): Promise<void> {
  for (let attempt = 0; attempt < 5; attempt += 1) {
    try {
      rmSync(tempRoot, { recursive: true, force: true })
      return
    } catch (error) {
      if (!(error instanceof Error) || !/EBUSY|EPERM/.test(error.message)) throw error
      await delay(200)
    }
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

main().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
