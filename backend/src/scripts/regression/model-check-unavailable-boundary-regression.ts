import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-check-unavailable-boundary-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.secret = 'model-check-unavailable-boundary-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })

const upstream = createFailingUpstream()
let stopGatewayJsonParseWorker: (() => Promise<void>) | undefined

try {
  await listen(upstream)
  const [
    repositories,
    { createMockGatewayFixture },
    { ModelCheckRequestError, runModelCheck },
    databaseModule,
    gatewayJsonParser
  ] = await Promise.all([
    import('../../storage/repositories.js'),
    import('../maintenance/mockdata-fixtures.js'),
    import('../../modules/model-checks/model-checks.service.js'),
    import('../../storage/database.js'),
    import('../../modules/gateway/openai-gateway-json-parser.js')
  ])
  stopGatewayJsonParseWorker = gatewayJsonParser.stopGatewayJsonParseWorker

  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const emptyGroup = repositories.createGroup({
    name: '模型检测无可用账号边界分组',
    providerCode: 'openai',
    description: '模型检测异常边界回归：分组内无可用账号',
    enabled: true
  }, access)
  const beforeEmptyGroupRuns = repositories.listModelCheckRuns(access, { page: 1, pageSize: 10 }).items.length

  await assert.rejects(
    () => runModelCheck({
      targetType: 'group',
      targetId: emptyGroup.id,
      model: 'gpt-5.5',
      profile: 'full',
      officialBaseline: false
    }, access),
    (error: unknown) => {
      assert(error instanceof ModelCheckRequestError, '无可用账号应返回模型检测请求错误')
      assert.equal(error.statusCode, 400)
      assert.match(error.message, /分组内没有可用的 OpenAI 账户，无法执行模型检测/)
      return true
    }
  )

  const afterEmptyGroupRuns = repositories.listModelCheckRuns(access, { page: 1, pageSize: 10 }).items.length
  assert.equal(afterEmptyGroupRuns, beforeEmptyGroupRuns, '目标解析阶段无可用账号时不应创建模型检测成功报告')

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
    officialBaseline: false
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

  const quotaApiKey = repositories.createApiKeyRecord({
    name: '模型检测额度不足边界 Key',
    groupId: fixture.group.id,
    status: 'active',
    quotaLimits: {
      total: { enabled: true, limit: 1 }
    }
  }, access)
  databaseModule.getRecordDatabase()
    .prepare(`
      INSERT INTO usage_stats_totals (
        system_account_id, scope_type, scope_id, total_cost_usd, updated_at
      ) VALUES (?, ?, ?, ?, ?)
    `)
    .run('sys_admin', 'api_key', quotaApiKey.id, 1, new Date().toISOString())

  const quotaRun = await runModelCheck({
    targetType: 'api_key',
    targetId: quotaApiKey.id,
    model: 'gpt-5.5',
    profile: 'full',
    officialBaseline: false
  }, access)

  assert.equal(quotaRun.status, 'completed', '额度不足应形成可排查报告')
  assert.equal(quotaRun.level, 'unavailable', '额度不足不能误判为模型不符或可信')
  assert(!['suspicious', 'likely', 'high_confidence'].includes(quotaRun.level), '额度不足不能落成可疑、较可信或高可信结论')
  assert.match(JSON.stringify(quotaRun), /额度已用完/, '额度不足报告应保留中文排障线索')
  assert(quotaRun.checks.length > 0, '额度不足报告应保留失败探针用于排查')
  assert(quotaRun.checks.every((item) => item.evidenceSummary.success !== true), '额度不足不应记录成功探针证据')
  assert(!JSON.stringify(quotaRun).includes('sk-mockdata'), '额度不足报告不应泄露上游 API Key')

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
  })
  assert(repositories.updateAccount(proxyAccount.id, { proxyProfileId: proxy.id }, access), '代理失败边界账户应能绑定启用代理')
  assert(repositories.updateProxy(proxy.id, { enabled: false }), '代理失败边界代理应能被停用')

  const proxyRun = await runModelCheck({
    targetType: 'account',
    targetId: proxyAccount.id,
    model: 'gpt-5.5',
    profile: 'full',
    officialBaseline: false
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

  console.log('模型检测异常边界回归通过：无可用账号返回中文错误且不建报告，上游失败、额度不足和代理失败只落 unavailable')
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
