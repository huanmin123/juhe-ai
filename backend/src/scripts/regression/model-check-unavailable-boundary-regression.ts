import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-check-unavailable-boundary-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'model-check-unavailable-boundary-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })

const upstream = createFailingUpstream()
let stopGatewayJsonParseWorker: (() => Promise<void>) | undefined

try {
  await listen(upstream)
  const [
    repositories,
    { getBusinessDatabase },
    { createMockGatewayFixture },
    { ModelCheckRequestError, runModelCheck },
    gatewayJsonParser
  ] = await Promise.all([
    import('../../storage/repositories.js'),
    import('../../storage/database.js'),
    import('../maintenance/mockdata-fixtures.js'),
    import('../../modules/model-checks/model-checks.service.js'),
    import('../../modules/gateway/openai-gateway-json-parser.js')
  ])
  stopGatewayJsonParseWorker = gatewayJsonParser.stopGatewayJsonParseWorker

  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const beforeInvalidTargetRuns = repositories.listModelCheckRuns(access, { page: 1, pageSize: 10 }).items.length
  await assert.rejects(
    () => runModelCheck({
      targetType: 'group',
      targetId: 'grp_invalid_target',
      model: 'gpt-5.5',
      profile: 'full',
      trustedComparison: false
    } as never, access),
    (error: unknown) => {
      assert(error instanceof ModelCheckRequestError, '无效目标类型应返回模型检测请求错误')
      assert.equal(error.statusCode, 400)
      assert.match(error.message, /AI 账户/)
      return true
    }
  )
  const afterInvalidTargetRuns = repositories.listModelCheckRuns(access, { page: 1, pageSize: 10 }).items.length
  assert.equal(afterInvalidTargetRuns, beforeInvalidTargetRuns, '无效目标类型被拒绝时不应创建模型检测报告')

  const temporaryGroup = repositories.createGroup({
    name: '模型检测未绑定分组边界临时分组',
    providerCode: 'openai'
  }, access)
  const unboundAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '模型检测未绑定分组边界账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-model-check-unbound-account',
      base_url: `http://127.0.0.1:${serverPort(upstream)}/v1`
    },
    status: 'active',
    schedulable: true,
    groupId: temporaryGroup.id
  }, access)
  getBusinessDatabase().prepare('DELETE FROM group_accounts WHERE account_id = ?').run(unboundAccount.id)
  const beforeUnboundRuns = repositories.listModelCheckRuns(access, { page: 1, pageSize: 10 }).items.length

  await assert.rejects(
    () => runModelCheck({
      targetType: 'account',
      targetId: unboundAccount.id,
      model: 'gpt-5.5',
      profile: 'full',
      trustedComparison: false
    }, access),
    (error: unknown) => {
      assert(error instanceof ModelCheckRequestError, '不可检测账户应返回模型检测请求错误')
      assert.equal(error.statusCode, 400)
      assert.match(error.message, /账户未绑定可用分组/)
      return true
    }
  )

  const afterUnboundRuns = repositories.listModelCheckRuns(access, { page: 1, pageSize: 10 }).items.length
  assert.equal(afterUnboundRuns, beforeUnboundRuns, '目标解析阶段账户不可检测时不应创建模型检测报告')

  const fixture = createMockGatewayFixture({
    label: '模型检测上游失败边界',
    upstreamBaseUrl: `http://127.0.0.1:${serverPort(upstream)}/v1`,
    systemAccountId: 'sys_admin',
    accountCount: 1,
    createApiKey: false
  })
  const account = fixture.accounts[0]
  assert(account, 'mock fixture should create a target account')

  const failedRun = await runModelCheck({
    targetType: 'account',
    targetId: account.id,
    model: 'gpt-5.5',
    profile: 'full',
    trustedComparison: false
  }, access)

  assert.equal(failedRun.status, 'completed', '上游失败应形成可排查报告')
  assert.equal(failedRun.level, 'unavailable', '纯上游失败不能误判为 suspicious 或 likely')
  assert(!['suspicious', 'likely'].includes(failedRun.level), '上游失败不能落成可疑或较可信结论')
  assert.match(failedRun.message, /不可检测|上游不可用/, '总览消息应说明目标链路不可检测或上游不可用')
  assert(failedRun.checks.length > 0, '上游失败报告应保留失败探针用于排查')
  const upstreamDependentChecks = failedRun.checks.filter((item) => item.itemKey !== 'target.model_catalog' && item.itemType !== 'usage_shape')
  assert(upstreamDependentChecks.length > 0, '上游失败报告应包含依赖上游响应的探针')
  assert(upstreamDependentChecks.every((item) => item.status !== 'passed'), '上游失败不应创建上游响应探针通过项')
  assert(upstreamDependentChecks.every((item) => item.evidenceSummary.success !== true), '上游失败不应记录上游响应成功证据')
  assert(!JSON.stringify(failedRun).includes('sk-mockdata'), '异常报告不应泄露上游 API Key')

  const proxyFixture = createMockGatewayFixture({
    label: '模型检测代理失败边界',
    upstreamBaseUrl: `http://127.0.0.1:${serverPort(upstream)}/v1`,
    systemAccountId: 'sys_admin',
    accountCount: 1,
    createApiKey: false
  })
  const proxyAccount = proxyFixture.accounts[0]
  assert(proxyAccount, 'mock fixture should create a proxy-bound account')
  const proxy = repositories.createProxy({
    name: '模型检测停用代理边界',
    type: 'http',
    host: '127.0.0.1',
    port: 9,
    enabled: true
  }, access)
  assert(repositories.updateAccount(proxyAccount.id, { proxyProfileId: proxy.id }, access), '代理失败边界账户应能绑定启用代理')
  assert(repositories.updateProxy(proxy.id, { enabled: false }), '代理失败边界代理应能被停用')

  const proxyRun = await runModelCheck({
    targetType: 'account',
    targetId: proxyAccount.id,
    model: 'gpt-5.5',
    profile: 'full',
    trustedComparison: false
  }, access)

  assert.equal(proxyRun.status, 'completed', '代理失败应形成可排查报告')
  assert.equal(proxyRun.level, 'unavailable', '代理失败不能误判为模型不符或可信')
  assert(!['suspicious', 'likely', 'high_confidence'].includes(proxyRun.level), '代理失败不能落成可疑、较可信或高可信结论')
  assert.match(JSON.stringify(proxyRun), /代理|没有可用的上游账户/, '代理失败报告应保留中文排障线索')
  assert(proxyRun.checks.length > 0, '代理失败报告应保留失败探针用于排查')
  const proxyDependentChecks = proxyRun.checks.filter((item) => item.itemKey !== 'target.model_catalog' && item.itemType !== 'usage_shape')
  assert(proxyDependentChecks.length > 0, '代理失败报告应包含依赖上游响应的探针')
  assert(proxyDependentChecks.every((item) => item.status !== 'passed'), '代理失败不应创建上游响应探针通过项')
  assert(proxyDependentChecks.every((item) => item.evidenceSummary.success !== true), '代理失败不应记录上游响应成功证据')
  assert(!JSON.stringify(proxyRun).includes('sk-mockdata'), '代理失败报告不应泄露上游 API Key')

  console.log('模型检测异常边界回归通过：无效目标类型被拒绝，账户不可检测不建报告，上游失败和代理失败只落 unavailable')
} finally {
  await stopGatewayJsonParseWorker?.()
  await closeServer(upstream)
}

function createFailingUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => {
      chunks.push(Buffer.from(chunk))
    })
    req.on('end', () => {
      res.writeHead(503, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: '模拟上游不可用' } }))
    })
  })
}

function listen(server: http.Server): Promise<void> {
  server.listen(0, '127.0.0.1')
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(typeof address === 'object' && address !== null, 'mock upstream should be listening')
  return address.port
}

function closeServer(server: http.Server): Promise<void> {
  if (!server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.close((error) => {
      if (error) rejectPromise(error)
      else resolvePromise()
    })
  })
}
