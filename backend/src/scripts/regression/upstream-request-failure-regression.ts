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
    false,
    '仅由本地假 iterable 包装出的 incomplete error 不得自行制造已开始上游 body transport 证据'
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
const explicitRetryNextFailureBody = JSON.stringify({
  error: {
    message: 'configured-body-marker',
    type: 'invalid_request_error',
    code: 'configured_retry_next'
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
      const authorization = String(req.headers.authorization ?? '')
      upstreamAuthorizations.push(authorization)
      if (req.url?.includes('mock_truncated_error_body=1')) {
        res.writeHead(400, {
          'content-type': 'application/json; charset=utf-8',
          'content-length': '4096',
          connection: 'close'
        })
        res.flushHeaders()
        res.write('{"error":{"message":"partial body must never reach client"')
        setTimeout(() => res.destroy(), 5)
        return
      }
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
      if (req.url?.includes('mock_explicit_retry_next=1')) {
        if (authorization.includes('sk-explicit-retry-next-fallback')) {
          res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
          res.end('{"choices":[{"message":{"role":"assistant","content":"explicit retry next backup success"},"finish_reason":"stop"}]}')
          return
        }
        res.writeHead(422, { 'content-type': 'application/json; charset=utf-8' })
        res.end(explicitRetryNextFailureBody)
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

    const opaqueGroup = repositories.createGroup({ name: '未配置策略的 HTTP 失败回归分组', providerCode: 'gpt', enabled: true }, access)
    const opaqueAccounts = [
      createAccount(opaqueGroup.id, '01-不透明失败账户', 'sk-opaque-failure-first', upstreamBaseUrl),
      createAccount(opaqueGroup.id, '02-不透明后备账户', 'sk-opaque-failure-fallback', upstreamBaseUrl)
    ]
    const opaqueApiKey = createRegressionApiKey(opaqueGroup.id, 'sk-opaque-request-failure-regression')

    const explicitRetryNextGroup = repositories.createGroup({ name: '显式 retry_next 回归分组', providerCode: 'gpt', enabled: true }, access)
    const explicitRetryNextAccounts = [
      createAccount(explicitRetryNextGroup.id, '01-显式 retry_next 账户', 'sk-explicit-retry-next-first', upstreamBaseUrl, true),
      createAccount(explicitRetryNextGroup.id, '02-显式 retry_next 后备账户', 'sk-explicit-retry-next-fallback', upstreamBaseUrl)
    ]
    const explicitRetryNextApiKey = createRegressionApiKey(explicitRetryNextGroup.id, 'sk-explicit-retry-next-regression')

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

    const truncatedBodyGroup = repositories.createGroup({ name: '响应正文客观截断终态分组', providerCode: 'gpt', enabled: true }, access)
    const truncatedBodyAccounts = [
      createAccount(truncatedBodyGroup.id, '01-正文截断账户', 'sk-truncated-body-primary', upstreamBaseUrl, false, 0),
      createAccount(truncatedBodyGroup.id, '02-正文截断健康后备账户', 'sk-truncated-body-backup', upstreamBaseUrl, false, 100)
    ]
    const truncatedBodyApiKey = createRegressionApiKey(truncatedBodyGroup.id, 'sk-truncated-body-regression')

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
    assert.equal(opaqueResponse.status, 422, `未配置策略的上游 HTTP 失败必须保留原状态：${opaqueResponseText}`)
    assert.equal(opaqueResponseText, opaqueFailureBody, '未配置策略的上游 HTTP 失败必须保留原正文')
    assert.deepEqual(
      upstreamAuthorizations,
      ['Bearer sk-opaque-failure-first'],
      '未配置策略的完整 HTTP 非 2xx 不得切换 Key、账户或分组'
    )
    assertAccountsRemainAvailable(opaqueAccounts, '不透明 HTTP 失败')

    const explicitRetryNextOffset = upstreamAuthorizations.length
    const explicitRetryNextResponse = await fetch(`${baseUrl}/v1/chat/completions?mock_explicit_retry_next=1`, {
      method: 'POST',
      headers: { authorization: `Bearer ${explicitRetryNextApiKey.key}`, 'content-type': 'application/json' },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'only an explicitly matched retry_next rule may switch account' }]
      })
    })
    const explicitRetryNextText = await explicitRetryNextResponse.text()
    assert.equal(explicitRetryNextResponse.status, 200, explicitRetryNextText)
    assert.match(explicitRetryNextText, /explicit retry next backup success/)
    assert.deepEqual(
      upstreamAuthorizations.slice(explicitRetryNextOffset),
      ['Bearer sk-explicit-retry-next-first', 'Bearer sk-explicit-retry-next-fallback'],
      '只有精确命中 retry_next 的策略才可切到后备账户'
    )
    assertAccountsRemainAvailable(explicitRetryNextAccounts, '显式 retry_next')

    const sideEffectTransportOffset = upstreamAuthorizations.length
    const sideEffectTransportResponse = await fetch(`${baseUrl}/v1/audio/speech?mock_side_effect_transport_drop=1`, {
      method: 'POST',
      headers: { authorization: `Bearer ${sideEffectTransportApiKey.key}`, 'content-type': 'application/json' },
      body: JSON.stringify({ model: 'gpt-4o-mini', input: 'dispatched side effect must use unified failover' })
    })
    const sideEffectTransportText = await sideEffectTransportResponse.text()
    assert.equal(sideEffectTransportResponse.status, 502, sideEffectTransportText)
    assert.match(sideEffectTransportText, /upstream_transport_error/, '未知 transport 失败必须以明确 transport 错误返回')
    assert.doesNotMatch(sideEffectTransportText, /upstream_retryable_error/, '未知 transport 失败不得伪装成候选耗尽后可重试错误')
    assert.deepEqual(
      upstreamAuthorizations.slice(sideEffectTransportOffset),
      ['Bearer sk-side-effect-transport-first'],
      '未知 transport 失败不得切换 Key、账户或分组'
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
    assert.equal(transportResponse.status, 502, `未收到上游响应头的传输失败应返回明确 transport 错误：${transportResponseText}`)
    assert.match(transportResponseText, /upstream_transport_error/)
    assert.ok(
      await accountCircuit.getGatewayAccountCircuitStore().size() > transportCircuitSizeBefore,
      '真实已经派发的连接失败仍必须进入账户传输电路'
    )
    assertAccountsRemainAvailable(transportAccounts, '不透明传输失败')

    const truncatedBodyCircuitSizeBefore = await accountCircuit.getGatewayAccountCircuitStore().size()
    const truncatedBodyOffset = upstreamAuthorizations.length
    const truncatedBodyResponse = await fetch(`${baseUrl}/v1/chat/completions?mock_truncated_error_body=1`, {
      method: 'POST',
      headers: { authorization: `Bearer ${truncatedBodyApiKey.key}`, 'content-type': 'application/json' },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'partial 400 body must fail over before downstream commit' }],
        stream: false
      })
    })
    const truncatedBodyText = await truncatedBodyResponse.text()
    assert.equal(truncatedBodyResponse.status, 400, `上游正文截断必须保留已收到的 HTTP 状态：${truncatedBodyText}`)
    assert.match(truncatedBodyText, /upstream_transport_error/, '上游正文截断必须以明确 transport 错误返回')
    assert.deepEqual(
      upstreamAuthorizations.slice(truncatedBodyOffset),
      ['Bearer sk-truncated-body-primary'],
      '上游正文截断不得切到后备账户并隐藏当前失败'
    )
    assert.ok(
      await accountCircuit.getGatewayAccountCircuitStore().size() > truncatedBodyCircuitSizeBefore,
      'content-length 未满足的真实正文 framing failure 必须保留 transport circuit 证据'
    )
    assertAccountsRemainAvailable(truncatedBodyAccounts, '正文客观截断')

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
    console.log('上游请求失败回归通过：未知 HTTP、transport 和正文截断均在当前请求终止，只有显式 retry_next 可切换候选')
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
