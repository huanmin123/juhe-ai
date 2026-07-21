import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-test-options-http-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-test-options-http-contract-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { accountsRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { saveCustomProviderModel },
  { requestContextMiddleware },
  { closeSqliteReadWorkerPool },
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../modules/model-pricing/model-catalog.service.js'),
  import('../../shared/request-context.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-accounts', forceSelfAccessScope, accountsRouter)
app.use('/__aisys__/api/accounts', requireAdmin, accountsRouter)

interface ApiEnvelope<T> {
  data?: T
  message?: string
}

type TestOptions = Array<{ id: string; name: string }>

interface ModelCapabilities {
  id: string
  name: string
  testEndpointModes: string[]
}

let server: ReturnType<typeof app.listen> | undefined
const database = databaseModule.getBusinessDatabase()
const originalPrepare = database.prepare.bind(database) as typeof database.prepare
const capturedSelectSql: string[] = []
database.prepare = ((sql: string) => {
  if (/^\s*(?:SELECT|WITH)\b/i.test(sql)) capturedSelectSql.push(sql)
  return originalPrepare(sql)
}) as typeof database.prepare

try {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  repositories.updateSystemAccount(admin.id, { mustChangePassword: false })
  const user = repositories.createSystemAccount({
    username: 'account_test_options_http_user',
    displayName: '账户测试选项HTTP用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const userAccess = { systemAccountId: user.id, role: 'user' as const }
  const encodedModelId = 'vendor/model-test'
  const responsesOnlyModelId = 'vendor/responses-only'
  const imageModelId = 'vendor/image-only'
  const messagesOnlyModelId = 'vendor/messages-only'
  for (const model of [encodedModelId, responsesOnlyModelId]) {
    saveCustomProviderModel({
      providerCode: 'openai',
      model,
      scope: 'personal',
      systemAccountId: user.id,
      status: 'active',
      supportedApiProtocols: ['responses'],
      actorSystemAccountId: user.id
    })
  }
  saveCustomProviderModel({
    providerCode: 'openai',
    model: imageModelId,
    scope: 'personal',
    systemAccountId: user.id,
    status: 'active',
    mode: 'image',
    supportedApiProtocols: ['responses'],
    actorSystemAccountId: user.id
  })
  saveCustomProviderModel({
    providerCode: 'openai',
    model: messagesOnlyModelId,
    scope: 'personal',
    systemAccountId: user.id,
    status: 'active',
    supportedApiProtocols: ['messages'],
    actorSystemAccountId: user.id
  })
  const group = repositories.createGroup({ name: '账户测试选项 HTTP 分组', providerCode: 'openai' }, userAccess)
  const account = repositories.createAccount({
    providerCode: 'openai',
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    name: '账户测试选项 HTTP 账户',
    type: 'api_key',
    groupId: group.id,
    supportedModels: [encodedModelId],
    healthCheckModel: encodedModelId,
    credentials: {
      api_key: 'sk-account-test-options-http',
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['responses_json', 'responses_sse']
    }
  }, userAccess)
  const openaiGroup = repositories.createGroup({ name: '账户测试选项 HTTP OpenAI分组', providerCode: 'openai' }, userAccess)
  const chatOnlyAccount = repositories.createAccount({
    providerCode: 'openai',
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    name: '账户测试选项 HTTP 无能力账户',
    type: 'api_key',
    groupId: openaiGroup.id,
    supportedModels: [responsesOnlyModelId],
    healthCheckModel: responsesOnlyModelId,
    credentials: {
      api_key: 'sk-account-test-options-http-chat-only',
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['chat_json']
    }
  }, userAccess)
  const adminCookie = sessionCookie(admin.id)
  const userCookie = sessionCookie(user.id)

  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('账户测试选项 HTTP 回归服务地址不可用')
  const baseUrl = `http://127.0.0.1:${address.port}`
  const managementQuery = `?systemAccountId=${encodeURIComponent(user.id)}`

  for (const target of [
    { prefix: '/__aisys__/api/accounts', query: managementQuery, cookie: adminCookie, label: '管理端' },
    { prefix: '/__aisys__/api/my-accounts', query: '', cookie: userCookie, label: '个人端' }
  ]) {
    capturedSelectSql.length = 0
    const options = await requestEnvelope<TestOptions>(
      baseUrl,
      `${target.prefix}/${account.id}/test-options${querySuffix(target.query, {
        keyword: 'model-test',
        limit: '1',
        selectedIds: responsesOnlyModelId
      })}`,
      target.cookie,
      200
    )
    assert(Array.isArray(options), `${target.label}测试选项响应数据必须直接是模型数组`)
    const selected = options.find((item) => item.id === encodedModelId)
    assert(selected, `${target.label}模型摘要必须保留请求模型 ID`)
    assert.deepEqual(Object.keys(selected).sort(), ['id', 'name'], `${target.label}模型摘要只能返回 id/name`)
    assert(selected.name.trim(), `${target.label}模型摘要展示名称不得为空`)
    assert(options.some((item) => item.id === responsesOnlyModelId), `${target.label}已选模型不得被 keyword/limit 窗口截断`)
    assert(options.length <= 2, `${target.label}limit=1 时除已选补齐外最多返回一条搜索结果`)
    assertListQueryBoundary(capturedSelectSql, target.label)

    const defaultModelOptions = await requestEnvelope<TestOptions>(
      baseUrl,
      `${target.prefix}/${account.id}/test-options${querySuffix(target.query, {
        keyword: 'responses-only',
        limit: '1'
      })}`,
      target.cookie,
      200
    )
    assert(
      defaultModelOptions.some((item) => item.id === encodedModelId),
      `${target.label}未传 selectedIds 时仍必须从最小账户上下文补齐检查模型`
    )
    const protocolEligibleOptions = await requestEnvelope<TestOptions>(
      baseUrl,
      `${target.prefix}/${account.id}/test-options${querySuffix(target.query, {
        keyword: 'vendor',
        limit: '50'
      })}`,
      target.cookie,
      200
    )
    assert.equal(protocolEligibleOptions.some((item) => item.id === imageModelId), false, `${target.label}模型候选不得包含图片模型`)
    assert.equal(protocolEligibleOptions.some((item) => item.id === messagesOnlyModelId), false, `${target.label}OpenAI 档案模型候选不得包含仅支持 Messages 的模型`)

    await requestEnvelope(
      baseUrl,
      `${target.prefix}/${account.id}/test-options${querySuffix(target.query, { limit: '51' })}`,
      target.cookie,
      400,
      /limit/
    )

    capturedSelectSql.length = 0
    const capabilities = await requestEnvelope<ModelCapabilities>(
      baseUrl,
      `${target.prefix}/${account.id}/test-options/models/${encodeURIComponent(encodedModelId)}${target.query}`,
      target.cookie,
      200
    )
    assert.deepEqual(
      Object.keys(capabilities).sort(),
      ['id', 'name', 'testEndpointModes'],
      `${target.label}模型能力字段集合必须稳定`
    )
    assert.equal(capabilities.id, encodedModelId, `${target.label}路由必须解码包含斜杠的模型 ID`)
    assert.deepEqual([...capabilities.testEndpointModes].sort(), ['responses_json', 'responses_sse'])
    assertCapabilitiesQueryBoundary(capturedSelectSql, target.label)

    await requestEnvelope(
      baseUrl,
      `${target.prefix}/missing-account/test-options${target.query}`,
      target.cookie,
      404
    )
    await requestEnvelope(
      baseUrl,
      `${target.prefix}/${account.id}/test-options/models/${encodeURIComponent('missing/model')}${target.query}`,
      target.cookie,
      400,
      /模型不在当前账户供应商可用目录中/
    )
    await requestEnvelope(
      baseUrl,
      `${target.prefix}/${chatOnlyAccount.id}/test-options/models/${encodeURIComponent(responsesOnlyModelId)}${target.query}`,
      target.cookie,
      400,
      /账户上游接口能力中没有可用于连接测试的请求形态/
    )
  }

  console.log('账户测试选项 HTTP 契约回归通过：管理/个人镜像、轻量字段、模型 ID 解码和错误边界均符合预期')
} finally {
  await closeServer(server)
  await closeSqliteReadWorkerPool()
  database.prepare = originalPrepare
  try {
    database.close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertListQueryBoundary(sqlList: string[], label: string): void {
  const contextQueries = sqlList.filter((sql) => sql.includes('AS view_account_id'))
  assert.equal(contextQueries.length, 1, `${label}模型列表只能读取一次最小账户上下文`)
  assert.doesNotMatch(contextQueries[0] ?? '', /credentials_encrypted|SELECT\s+\*/i, `${label}模型列表不得读取账户密文或完整行`)
  assert.equal(sqlList.some((sql) => /account_model_mappings/i.test(sql)), false, `${label}模型列表不得读取模型映射`)
  assertLightweightCatalogQueries(sqlList, label, false)
}

function assertCapabilitiesQueryBoundary(sqlList: string[], label: string): void {
  const contextQueries = sqlList.filter((sql) => sql.includes('AS view_account_id'))
  assert.equal(contextQueries.length, 1, `${label}模型能力只能读取一次单行账户上下文`)
  assert.match(contextQueries[0] ?? '', /credentials_encrypted/i, `${label}模型能力必须只为 supported_endpoint_modes 读取单行密文`)
  assert.doesNotMatch(contextQueries[0] ?? '', /SELECT\s+\*/i, `${label}模型能力不得构造完整账户摘要`)
  const mappingQueries = sqlList.filter((sql) => /FROM\s+account_model_mappings/i.test(sql))
  assert.equal(mappingQueries.length, 1, `${label}模型能力只能定点读取一次当前账户映射`)
  assertLightweightCatalogQueries(sqlList, label, true)
}

function assertLightweightCatalogQueries(sqlList: string[], label: string, targeted: boolean): void {
  const catalogQueries = sqlList.filter((sql) => /FROM\s+(?:provider_model_catalog|custom_provider_models)/i.test(sql))
  assert(catalogQueries.length <= 2, `${label}模型目录最多允许内置与自定义各一次查询，实际 ${catalogQueries.length}`)
  assert(catalogQueries.length > 0, `${label}必须读取轻量模型目录`)
  for (const sql of catalogQueries) {
    assert.doesNotMatch(
      sql,
      /input_usd_per_1m|output_usd_per_1m|service_tier_prices_json|supported_service_tiers_json|supported_reasoning_efforts_json|default_reasoning_effort|capability_notes|pricing_notes|notes/i,
      `${label}轻量模型目录不得读取定价或说明字段`
    )
    if (targeted) {
      assert.match(sql, /model\s*=\s*\?/i, `${label}模型能力目录查询必须按 modelId 定点过滤`)
    } else {
      assert.match(sql, /LIMIT\s+\?/i, `${label}模型列表目录查询必须在 SQL 层限制窗口`)
    }
  }
}

function querySuffix(baseQuery: string, params: Record<string, string>): string {
  const search = new URLSearchParams(baseQuery.startsWith('?') ? baseQuery.slice(1) : baseQuery)
  for (const [key, value] of Object.entries(params)) search.set(key, value)
  const text = search.toString()
  return text ? `?${text}` : ''
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function requestEnvelope<T = unknown>(
  baseUrl: string,
  path: string,
  cookie: string,
  expectedStatus: number,
  expectedMessage?: RegExp
): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
  const text = await response.text()
  assert.equal(
    response.status,
    expectedStatus,
    `${path} HTTP ${response.status}: ${text}\nSELECT SQL:\n${capturedSelectSql.join('\n---\n')}`
  )
  const payload = JSON.parse(text) as ApiEnvelope<T>
  if (expectedMessage) assert.match(payload.message ?? '', expectedMessage)
  return payload.data as T
}

async function onceListening(listeningServer: ReturnType<typeof app.listen>): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', rejectPromise)
  })
}

async function closeServer(listeningServer?: ReturnType<typeof app.listen>): Promise<void> {
  if (!listeningServer?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}
