import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import type { SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { OPENAI_PROTOCOL_CODE } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = '0'

const tempRoot = resolve(tmpdir(), `juhe-ai-response-policy-progressive-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'response-policy-progressive-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  expressModule,
  databaseModule,
  policyRepository,
  systemAccountsRepository,
  readWorkerPool,
  policyRoutes,
  authMiddleware,
  requestContext
] = await Promise.all([
  import('express'),
  import('../../storage/database.js'),
  import('../../storage/response-inspection-policy.repository.js'),
  import('../../storage/system-accounts.repository.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../modules/response-inspection-policies/response-inspection-policies.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js')
])

const app = expressModule.default()
app.use(requestContext.requestContextMiddleware)
app.use(expressModule.default.json())
app.use('/response-inspection-policies', authMiddleware.requireAuth, authMiddleware.requireAdmin, policyRoutes.responseInspectionPoliciesRouter)
const server = createServer(app)
let providerFixture: ProviderFixture | undefined
let httpCreatedPolicyId: string | undefined

try {
  systemAccountsRepository.updateSystemAccount('sys_admin', { mustChangePassword: false })
  const user = systemAccountsRepository.createSystemAccount({
    username: `response_policy_user_${Date.now()}`,
    displayName: `响应策略普通用户${Date.now()}`,
    password: 'Response-policy-regression-123!',
    role: 'user',
    mustChangePassword: false
  })
  const adminCookie = sessionCookie('sys_admin')
  const userCookie = sessionCookie(user.id)
  const database = databaseModule.getBusinessDatabase()
  const insertedProviderFixture = insertProviderFixture(database, 205)
  providerFixture = insertedProviderFixture
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const custom = policyRepository.createResponseInspectionPolicy({
    name: '渐进加载详情回归策略',
    enabled: true,
    priority: 731,
    scopeType: 'protocol',
    protocolCode: OPENAI_PROTOCOL_CODE,
    match: { outputTextIncludes: ['large matcher payload'] },
    action: 'observe',
    notes: '详情备注只能由 detail 返回'
  })

  await assertSqliteMainAndReadWorkerParity(custom.id)

  await new Promise<void>((resolveListen) => server.listen(0, '127.0.0.1', resolveListen))
  const address = server.address()
  assert(address && typeof address === 'object', 'HTTP 回归服务器必须成功监听')
  const baseUrl = `http://127.0.0.1:${address.port}/response-inspection-policies`

  for (const path of ['', '/provider-options', `/${custom.id}`]) {
    assert.equal((await fetch(`${baseUrl}${path}`)).status, 401, `${path || '/'} 未登录必须返回 401`)
    assert.equal((await fetch(`${baseUrl}${path}`, { headers: { cookie: userCookie } })).status, 403, `${path || '/'} 普通用户必须返回 403`)
  }

  const createdByHttp = asRecord(await requestJsonData(baseUrl, adminCookie, 'POST', {
    name: 'HTTP 创建渐进加载策略',
    enabled: true,
    priority: 732,
    scopeType: 'protocol',
    protocolCode: OPENAI_PROTOCOL_CODE,
    match: { errorCodes: ['http_created_error'] },
    action: 'retry_no_avoidance',
    notes: 'HTTP 创建详情备注'
  }, 201), 'HTTP create overview')
  httpCreatedPolicyId = String(createdByHttp.id ?? '')
  assert(httpCreatedPolicyId, 'HTTP POST 必须返回策略 ID')
  assertOverviewShape(createdByHttp)
  assert.equal(typeof createdByHttp.updatedAt, 'string', 'HTTP POST 窄响应必须返回列表合并所需版本')

  const patchSql: string[] = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/^\s*UPDATE\s+response_inspection_policies\b/i.test(sql)) {
      const originalRun = statement.run.bind(statement) as typeof statement.run
      statement.run = ((...params: SQLInputValue[]) => {
        patchSql.push(sql)
        return originalRun(...params)
      }) as typeof statement.run
    }
    return statement
  }) as typeof database.prepare
  let updatedByHttp: Record<string, unknown>
  try {
    updatedByHttp = asRecord(await requestJsonData(`${baseUrl}/${encodeURIComponent(httpCreatedPolicyId)}`, adminCookie, 'PATCH', {
      expectedUpdatedAt: createdByHttp.updatedAt,
      notes: 'HTTP 更新详情备注'
    }), 'HTTP update overview')
  } finally {
    database.prepare = originalPrepare
  }
  assertOverviewShape(updatedByHttp)
  assert.equal(patchSql.length, 1, '单字段 PATCH 必须只执行一条策略 UPDATE')
  assert.match(patchSql[0] ?? '', /SET\s+notes\s*=\s*\?,\s*updated_at\s*=\s*\?/i, '备注 PATCH 只能更新 notes 与 updated_at')
  assert.doesNotMatch(patchSql[0] ?? '', /\b(?:name|enabled|priority|scope_type|protocol_code|provider_code|match_json|action)\s*=/i, '备注 PATCH 不得覆盖其他业务列')

  const noOpSql: string[] = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/^\s*UPDATE\s+response_inspection_policies\b/i.test(sql)) noOpSql.push(sql)
    return statement
  }) as typeof database.prepare
  let noOpByHttp: Record<string, unknown>
  try {
    noOpByHttp = asRecord(await requestJsonData(`${baseUrl}/${encodeURIComponent(httpCreatedPolicyId)}`, adminCookie, 'PATCH', {
      expectedUpdatedAt: updatedByHttp.updatedAt,
      notes: 'HTTP 更新详情备注'
    }), 'HTTP no-op overview')
  } finally {
    database.prepare = originalPrepare
  }
  assert.equal(noOpSql.length, 0, '同值 PATCH 必须零 DML')
  assert.equal(noOpByHttp.updatedAt, updatedByHttp.updatedAt, '同值 PATCH 不得推进版本')

  const staleResponse = await fetch(`${baseUrl}/${encodeURIComponent(httpCreatedPolicyId)}`, {
    method: 'PATCH',
    headers: { cookie: adminCookie, 'content-type': 'application/json' },
    body: JSON.stringify({ expectedUpdatedAt: createdByHttp.updatedAt, enabled: false })
  })
  assert.equal(staleResponse.status, 409, '旧版本 PATCH 必须返回 409')
  const emptyPatchResponse = await fetch(`${baseUrl}/${encodeURIComponent(httpCreatedPolicyId)}`, {
    method: 'PATCH',
    headers: { cookie: adminCookie, 'content-type': 'application/json' },
    body: JSON.stringify({ expectedUpdatedAt: updatedByHttp.updatedAt })
  })
  assert.equal(emptyPatchResponse.status, 400, '空 PATCH 必须返回 400')

  const overviewSql: string[] = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bFROM\s+(?:response_inspection_policies|juhe_business\.response_inspection_policies)\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        overviewSql.push(sql)
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof database.prepare

  let listBody: unknown
  try {
    listBody = await requestData(baseUrl, adminCookie)
  } finally {
    database.prepare = originalPrepare
  }
  const list = asRecord(listBody, 'overview data')
  const defaultRules = asRecordArray(list.defaultRules, 'defaultRules')
  const policies = asRecordArray(list.policies, 'policies')
  assert(defaultRules.length > 0, 'overview 必须返回系统默认规则')
  const customOverview = policies.find((item) => item.id === custom.id)
  const httpOverview = policies.find((item) => item.id === httpCreatedPolicyId)
  assert(customOverview, 'overview 必须返回刚创建的管理策略')
  assert(httpOverview, 'overview 必须返回 HTTP 创建并更新的管理策略')
  for (const item of [...defaultRules, ...policies]) assertOverviewShape(item)
  assert.equal(customOverview.updatedAt !== undefined, true, '管理策略 overview 必须包含 updatedAt')
  assertOverviewShape(httpOverview)

  assert(overviewSql.length > 0, 'overview 回归必须捕获管理策略 SQL')
  for (const sql of overviewSql) {
    assert.doesNotMatch(sql, /SELECT\s+(?:\w+\.)?\*/i, 'overview SQL 禁止 SELECT *')
    assert.doesNotMatch(sql, /\bmatch_json\b/i, 'overview SQL 禁止读取 match_json')
    assert.doesNotMatch(sql, /\bnotes\b/i, 'overview SQL 禁止读取 notes')
    assert.doesNotMatch(sql, /\bcreated_at\b/i, 'overview SQL 禁止读取 created_at')
  }

  const customDetail = asRecord(await requestData(`${baseUrl}/${custom.id}`, adminCookie), 'custom detail')
  assert.equal(customDetail.id, custom.id)
  assert.deepEqual(customDetail.match, { outputTextIncludes: ['large matcher payload'] })
  assert.equal(customDetail.notes, '详情备注只能由 detail 返回')
  assert.equal(typeof customDetail.updatedAt, 'string', 'custom detail 必须包含 CAS 版本')
  assert.equal(Object.hasOwn(customDetail, 'createdAt'), false, 'custom detail 不得返回编辑未使用的 createdAt')
  assert.equal(Object.hasOwn(customDetail, 'defaultRule'), false, 'custom detail 不得重复返回列表已有 defaultRule')
  assert.equal(Object.hasOwn(customDetail, 'editable'), false, 'custom detail 不得重复返回列表已有 editable')

  const httpDetail = asRecord(await requestData(`${baseUrl}/${httpCreatedPolicyId}`, adminCookie), 'HTTP patched detail')
  assert.deepEqual(httpDetail.match, { errorCodes: ['http_created_error'] }, '备注 PATCH 不得覆盖未提交 matcher')
  assert.equal(httpDetail.notes, 'HTTP 更新详情备注', '备注 PATCH 应保存目标字段')

  const defaultId = String(defaultRules[0]?.id ?? '')
  const defaultDetail = asRecord(await requestData(`${baseUrl}/${encodeURIComponent(defaultId)}`, adminCookie), 'default detail')
  assert.equal(defaultDetail.id, defaultId, '默认规则 detail 必须可按 ID 查询')
  assert(defaultDetail.match && typeof defaultDetail.match === 'object', '默认规则 detail 必须包含 matcher')
  assert.equal(Object.hasOwn(defaultDetail, 'createdAt'), false, '默认规则 detail 不得返回未展示的 createdAt')
  assert.equal((await fetch(`${baseUrl}/missing_policy_id`, { headers: { cookie: adminCookie } })).status, 404, '不存在策略 detail 必须返回 404')

  assert.equal((await fetch(`${baseUrl}/provider-options`, { headers: { cookie: adminCookie } })).status, 400, 'provider-options 缺少协议上下文必须返回 400')
  assert.equal((await fetch(`${baseUrl}/provider-options?protocolCode=openai&scopeType=invalid`, { headers: { cookie: adminCookie } })).status, 400, 'provider-options 非法层级必须返回 400')

  const providerOptionsSql: Array<{ sql: string; params: SQLInputValue[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bINNER\s+JOIN\s+provider_protocol_profiles\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        providerOptionsSql.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof database.prepare
  let providerOptionsBody: unknown
  try {
    const protocolScopeOptions = await requestData(`${baseUrl}/provider-options?protocolCode=openai&scopeType=protocol`, adminCookie)
    assert.deepEqual(protocolScopeOptions, [], '协议层不需要供应商候选且不得查询 provider 表')
    providerOptionsBody = await requestData(`${baseUrl}/provider-options?protocolCode=openai&scopeType=provider`, adminCookie)
  } finally {
    database.prepare = originalPrepare
  }
  const providerOptions = asRecordArray(providerOptionsBody, 'provider options')
  assert(providerOptions.length > 0, 'provider options 必须返回启用的受支持供应商')
  for (const option of providerOptions) {
    assert.deepEqual(Object.keys(option).sort(), ['code', 'name'], 'provider option 只能返回 code/name')
  }
  assert.deepEqual(providerOptions, [...providerOptions].sort(compareProviderOption), 'provider options 必须按 name/code 稳定排序')
  assert.equal(new Set(providerOptions.map((option) => option.code)).size, providerOptions.length, '单协议 provider options 必须按 code 去重')
  assert(providerOptions.length > 200, 'provider options 超过 200 条时不得静默截断')
  for (const code of insertedProviderFixture.expectedCodes) {
    assert(providerOptions.some((option) => option.code === code), `OpenAI provider options 不得遗漏夹具 ${code}`)
  }
  assert(!providerOptions.some((option) => option.code === insertedProviderFixture.disabledProviderCode), '停用 provider 必须排除')
  assert(!providerOptions.some((option) => option.code === insertedProviderFixture.disabledProfileProviderCode), '只有停用 profile 的 provider 必须排除')

  assert.equal(providerOptionsSql.length, 1, '一次 provider-options HTTP 请求只应执行一次专用 options SQL')
  const optionQuery = providerOptionsSql[0]
  assert(optionQuery, '必须捕获 provider-options SQL')
  const optionSql = optionQuery.sql
  assert.deepEqual(optionQuery.params, ['openai'], 'provider-options SQL 必须只绑定当前协议')
  assert.match(optionSql, /SELECT\s+DISTINCT\s+p\.code\s*,\s*p\.name\s+FROM/is, 'provider-options SQL 只能投影 code/name')
  assert.match(optionSql, /ppp\.protocol_code\s*=\s*\?/i, 'provider-options SQL 必须按单协议等值过滤')
  assert.doesNotMatch(optionSql, /ppp\.protocol_code\s+IN\s*\(/i, 'provider-options SQL 不得再读取全部协议')
  assert.doesNotMatch(optionSql, /SELECT\s+(?:\w+\.)?\*/i, 'provider-options SQL 禁止 SELECT *')
  assert.doesNotMatch(optionSql, /\b(?:description|base_url|capabilities_json|account_types_json|default_supported_models_json)\b/i, 'provider-options SQL 禁止读取完整 provider/profile 字段')
  assert.doesNotMatch(optionSql, /\bLIMIT\b/i, 'provider-options SQL 不得用固定窗口静默截断')

  const keywordOptions = asRecordArray(await requestData(`${baseUrl}/provider-options?protocolCode=openai&scopeType=provider&keyword=Fixture%20Provider%2000`, adminCookie), 'keyword provider options')
  assert(keywordOptions.length > 0 && keywordOptions.length < providerOptions.length, 'provider-options keyword 必须在数据库侧缩小候选集')
  assert(keywordOptions.every((option) => String(option.name).startsWith('Fixture Provider 00')), 'provider-options keyword 不得返回不匹配供应商')
  assertProviderOptionsDoesNotUseFullProviderLoader()

  console.log('响应检查策略渐进加载回归通过：overview/detail、按协议/关键词 options、权限、SQLite 双读路径与窄 SQL 投影均符合契约')
} finally {
  if (httpCreatedPolicyId) policyRepository.deleteResponseInspectionPolicy(httpCreatedPolicyId)
  if (providerFixture) cleanupProviderFixture(databaseModule.getBusinessDatabase(), providerFixture)
  await new Promise<void>((resolveClose) => server.close(() => resolveClose()))
  await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  for (let attempt = 0; attempt < 5; attempt += 1) {
    try {
      rmSync(tempRoot, { recursive: true, force: true })
      break
    } catch (error) {
      if (attempt === 4) throw error
      await delay(100 * (attempt + 1))
    }
  }
}

async function assertSqliteMainAndReadWorkerParity(customId: string): Promise<void> {
  const mainList = policyRepository.listResponseInspectionPolicies()
  const mainDetail = policyRepository.getResponseInspectionPolicyDetail(customId)
  const providerOptionsQuery = { protocolCode: OPENAI_PROTOCOL_CODE, scopeType: 'provider' as const, keyword: 'Fixture' }
  const mainOptions = policyRepository.listResponseInspectionPolicyProviderOptions(providerOptionsQuery)
  runtimeConfig.processRole = 'db-service'
  try {
    assert.deepEqual(jsonValue(await policyRepository.listResponseInspectionPoliciesAsync()), jsonValue(mainList), 'SQLite read worker overview 必须与主线程 JSON 逐字段一致')
    assert.deepEqual(jsonValue(await policyRepository.getResponseInspectionPolicyDetailAsync(customId)), jsonValue(mainDetail), 'SQLite read worker detail 必须与主线程 JSON 逐字段一致')
    assert.deepEqual(jsonValue(await policyRepository.listResponseInspectionPolicyProviderOptionsAsync(providerOptionsQuery)), jsonValue(mainOptions), 'SQLite read worker provider options 必须与主线程 JSON 逐字段一致')
  } finally {
    runtimeConfig.processRole = 'worker'
  }
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${systemAccountsRepository.createSession(systemAccountId, 1).token}`
}

async function requestData(url: string, cookie: string): Promise<unknown> {
  const response = await fetch(url, { headers: { cookie } })
  const body = await response.json() as unknown
  assert.equal(response.status, 200, `${url} 应返回 200：${JSON.stringify(body)}`)
  return asRecord(body, 'API envelope').data
}

async function requestJsonData(
  url: string,
  cookie: string,
  method: 'POST' | 'PATCH',
  body: unknown,
  expectedStatus = 200
): Promise<unknown> {
  const response = await fetch(url, {
    method,
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  const responseBody = await response.json() as unknown
  assert.equal(response.status, expectedStatus, `${method} ${url} 应返回 ${expectedStatus}：${JSON.stringify(responseBody)}`)
  return asRecord(responseBody, 'API envelope').data
}

function assertOverviewShape(item: Record<string, unknown>): void {
  const allowed = new Set([
    'id', 'defaultRule', 'editable', 'name', 'enabled', 'priority', 'scopeType', 'protocolCode',
    'providerCode', 'providerName', 'action', 'updatedAt'
  ])
  for (const key of Object.keys(item)) assert(allowed.has(key), `overview 出现非白名单字段：${key}`)
  for (const key of ['id', 'defaultRule', 'editable', 'name', 'enabled', 'priority', 'scopeType', 'protocolCode', 'action']) {
    assert(Object.hasOwn(item, key), `overview 缺少必需字段：${key}`)
  }
  for (const key of ['match', 'notes', 'createdAt']) assert(!Object.hasOwn(item, key), `overview 禁止返回详情字段：${key}`)
}

function compareProviderOption(left: Record<string, unknown>, right: Record<string, unknown>): number {
  return compareText(String(left.name), String(right.name))
    || compareText(String(left.code), String(right.code))
}

function compareText(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0
}

function asRecord(value: unknown, label: string): Record<string, unknown> {
  assert(value && typeof value === 'object' && !Array.isArray(value), `${label} 必须是对象`)
  return value as Record<string, unknown>
}

function asRecordArray(value: unknown, label: string): Array<Record<string, unknown>> {
  assert(Array.isArray(value), `${label} 必须是数组`)
  return value.map((item, index) => asRecord(item, `${label}[${index}]`))
}

function jsonValue<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}

interface ProviderFixture {
  providerCodes: string[]
  profileIds: string[]
  expectedCodes: Set<string>
  disabledProviderCode: string
  disabledProfileProviderCode: string
}

function insertProviderFixture(
  database: ReturnType<typeof databaseModule.getBusinessDatabase>,
  enabledCount: number
): ProviderFixture {
  const now = new Date().toISOString()
  const providerCodes: string[] = []
  const profileIds: string[] = []
  const expectedCodes = new Set<string>()
  const insertProvider = database.prepare(`
    INSERT INTO providers (
      id, code, name, description, parent_code, enabled, default_supported_models_json, created_at, updated_at
    ) VALUES (?, ?, ?, NULL, NULL, ?, '[]', ?, ?)
  `)
  const insertProfile = database.prepare(`
    INSERT INTO provider_protocol_profiles (
      id, provider_code, name, description, enabled, protocol_code, protocol_version,
      base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at
    ) VALUES (?, ?, ?, NULL, ?, 'openai', 'v1', 'https://fixture.invalid/v1', 'fixture-model', '["api_key"]', '[]', ?, ?)
  `)

  for (let index = 0; index < enabledCount; index += 1) {
    const suffix = String(index).padStart(3, '0')
    const code = `rip_option_fixture_${suffix}`
    const profileId = `rip_option_profile_${suffix}`
    insertProvider.run(`rip_option_provider_${suffix}`, code, `Fixture Provider ${suffix}`, 1, now, now)
    insertProfile.run(profileId, code, `Fixture Profile ${suffix}`, 1, now, now)
    providerCodes.push(code)
    profileIds.push(profileId)
    expectedCodes.add(code)
  }

  const duplicateProfileId = 'rip_option_profile_duplicate'
  insertProfile.run(duplicateProfileId, providerCodes[0], 'Fixture Duplicate Profile', 1, now, now)
  profileIds.push(duplicateProfileId)

  const disabledProviderCode = 'rip_option_disabled_provider'
  insertProvider.run('rip_option_disabled_provider_id', disabledProviderCode, 'Fixture Disabled Provider', 0, now, now)
  const disabledProviderProfileId = 'rip_option_disabled_provider_profile'
  insertProfile.run(disabledProviderProfileId, disabledProviderCode, 'Fixture Disabled Provider Profile', 1, now, now)
  providerCodes.push(disabledProviderCode)
  profileIds.push(disabledProviderProfileId)

  const disabledProfileProviderCode = 'rip_option_disabled_profile_provider'
  insertProvider.run('rip_option_disabled_profile_provider_id', disabledProfileProviderCode, 'Fixture Disabled Profile Provider', 1, now, now)
  const disabledProfileId = 'rip_option_disabled_profile'
  insertProfile.run(disabledProfileId, disabledProfileProviderCode, 'Fixture Disabled Profile', 0, now, now)
  providerCodes.push(disabledProfileProviderCode)
  profileIds.push(disabledProfileId)

  return { providerCodes, profileIds, expectedCodes, disabledProviderCode, disabledProfileProviderCode }
}

function cleanupProviderFixture(
  database: ReturnType<typeof databaseModule.getBusinessDatabase>,
  fixture: ProviderFixture
): void {
  const profilePlaceholders = fixture.profileIds.map(() => '?').join(', ')
  const providerPlaceholders = fixture.providerCodes.map(() => '?').join(', ')
  database.prepare(`DELETE FROM provider_protocol_profiles WHERE id IN (${profilePlaceholders})`).run(...fixture.profileIds)
  database.prepare(`DELETE FROM providers WHERE code IN (${providerPlaceholders})`).run(...fixture.providerCodes)
}

function assertProviderOptionsDoesNotUseFullProviderLoader(): void {
  const source = readFileSync(resolve('src/storage/response-inspection-policy.repository.ts'), 'utf8')
  const start = source.indexOf('export function listResponseInspectionPolicyProviderOptions(')
  const end = source.indexOf('export async function listResponseInspectionPolicyProviderOptionsAsync(', start)
  assert(start >= 0 && end > start, '必须能定位同步 provider-options repository 函数')
  const snippet = source.slice(start, end)
  assert.doesNotMatch(snippet, /\b(?:listProviders|listProviderDefinitions|loadProviders|loadProviderDefinitions)\b/, 'provider-options 不得调用完整 provider loader')
}
