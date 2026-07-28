import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-provider-model-patch-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'provider-model-demand-write-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  { providersRouter },
  { requireAuth },
  { requestContextMiddleware },
  modelCatalogService,
  sqliteReadWorkerPool
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/providers/providers.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../modules/model-pricing/model-catalog.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

let server: ReturnType<typeof express.application.listen> | undefined
try {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  repositories.updateSystemAccount(admin.id, { mustChangePassword: false })
  const cookie = `juhe_ai_session=${repositories.createSession(admin.id, 1).token}`

  const app = express()
  app.use(requestContextMiddleware)
  app.use(express.json({ limit: '1mb' }))
  app.use('/__aisys__/api', requireAuth)
  app.use('/__aisys__/api/providers', providersRouter)
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  assert(address && typeof address !== 'string', '模型 PATCH 回归服务地址不可用')
  const baseUrl = `http://127.0.0.1:${address.port}`

  const providerList = await getEnvelope<Array<Record<string, unknown>>>(
    baseUrl,
    '/__aisys__/api/providers/list?viewScope=admin',
    cookie
  )
  assert(providerList.length > 0, '供应商轻量列表不能为空')
  assert(providerList.every((provider) => typeof provider.protocolCode === 'string' && provider.protocolCode), '供应商轻量列表必须携带真实 protocolCode 常量')
  assert(providerList.every((provider) => !('protocolProfiles' in provider)), '供应商轻量列表不得为协议默认值预加载完整 protocolProfiles')
  assert.equal(providerList.find((provider) => provider.code === 'anthropic')?.protocolCode, 'anthropic', 'Anthropic 轻量协议常量错误')
  assert.equal(providerList.find((provider) => provider.code === 'gemini')?.protocolCode, 'gemini', 'Gemini 轻量协议常量错误')

  const created = await requestEnvelope<{
    id: string
    providerCode: string
    model: string
    status: string
    updatedAt: string
  }>(baseUrl, '/__aisys__/api/providers/openai/models', cookie, 'POST', {
    scope: 'global',
    model: 'provider-model-demand-write-regression',
    status: 'active',
    mode: 'text',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2
  })
  setDefaultModelReferences(admin.id, created.providerCode, created.model)
  setDefaultModelReferences(admin.id, 'hybrid', created.model)
  const rowBefore = customModelRow(created.id)

  const updated = await requestEnvelope<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/providers/openai/models/${created.id}`,
    cookie,
    'PATCH',
    { expectedUpdatedAt: created.updatedAt, notes: 'updated-note' }
  )
  assert.deepEqual(
    Object.keys(updated).sort(),
    ['id', 'model', 'providerCode', 'status', 'updatedAt'],
    '模型 PATCH 响应只能包含当前行局部更新所需字段'
  )
  assert.notEqual(updated.updatedAt, created.updatedAt, '实际变化的 PATCH 必须推进版本')
  const rowAfter = customModelRow(created.id)
  assert.deepEqual(
    changedColumns(rowBefore, rowAfter),
    ['notes', 'updated_at'],
    '备注 PATCH 只能修改 notes 与 updated_at'
  )
  assert.equal(totalDefaultModelReferenceCount(created.model), 4, '备注 PATCH 不得清理源供应商或聚合供应商的个人/系统默认检查模型')

  const noOp = await requestEnvelope<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/providers/openai/models/${created.id}`,
    cookie,
    'PATCH',
    { expectedUpdatedAt: updated.updatedAt, notes: 'updated-note' }
  )
  assert.equal(noOp.updatedAt, updated.updatedAt, '同值 PATCH 不得推进版本')
  assert.deepEqual(customModelRow(created.id), rowAfter, '同值 PATCH 不得写入任何列')

  const staleResponse = await fetch(`${baseUrl}/__aisys__/api/providers/openai/models/${created.id}`, {
    method: 'PATCH',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify({ expectedUpdatedAt: created.updatedAt, notes: 'stale-overwrite' })
  })
  assert.equal(staleResponse.status, 409, `旧版本 PATCH 必须返回 409：${await staleResponse.text()}`)
  assert.deepEqual(customModelRow(created.id), rowAfter, '旧版本 PATCH 不得覆盖新值')

  const customHidden = await requestEnvelope<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/providers/openai/models/${created.id}`,
    cookie,
    'PATCH',
    { expectedUpdatedAt: updated.updatedAt, catalogVisible: false }
  )
  assert.equal(customHidden.defaultHealthCheckModelCleared, true, '自定义模型从可用变为隐藏时必须通知前端移除默认标记')
  assert.equal(totalDefaultModelReferenceCount(created.model), 0, '隐藏全局自定义模型必须定点清理源供应商与聚合供应商的个人/系统默认检查模型')
  const customManagementCatalog = await getEnvelope<Array<{ id?: string }>>(
    baseUrl,
    '/__aisys__/api/providers/openai/models?viewScope=admin&includeInactive=true&includeUnpriced=true',
    cookie
  )
  assert(customManagementCatalog.some((model) => model.id === created.id), '管理目录必须保留隐藏自定义模型供恢复')
  const customOrdinaryCatalog = await getEnvelope<Array<{ id?: string }>>(
    baseUrl,
    '/__aisys__/api/providers/openai/models?viewScope=self&includeUnpriced=true',
    cookie
  )
  assert(!customOrdinaryCatalog.some((model) => model.id === created.id), '普通目录必须排除 catalogVisible=false 的自定义模型')
  const customTestCatalog = await modelCatalogService.listProviderModelTestCatalogAsync({
    providerCode: created.providerCode,
    systemAccountId: admin.id
  })
  assert(!customTestCatalog.some((model) => model.id === created.id), '测试目录必须排除 catalogVisible=false 的自定义模型')

  const builtIn = databaseModule.getBusinessDatabase().prepare(`
    SELECT id, provider_code, model, catalog_visible, updated_at
    FROM provider_model_catalog
    WHERE status = 'active' AND catalog_visible = 1
    ORDER BY provider_code, model
    LIMIT 1
  `).get() as unknown as {
    id: string
    provider_code: string
    model: string
    catalog_visible: number
    updated_at: string
  }
  assert(builtIn, '没有可用于字段级 PATCH 回归的内置模型')
  setDefaultModelReferences(admin.id, builtIn.provider_code, builtIn.model)
  const builtInBefore = builtInModelRow(builtIn.id)
  const builtInUpdated = await requestEnvelope<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/providers/${builtIn.provider_code}/models/${builtIn.id}`,
    cookie,
    'PATCH',
    { expectedUpdatedAt: builtIn.updated_at, catalogVisible: false }
  )
  assert.deepEqual(
    Object.keys(builtInUpdated).sort(),
    ['defaultHealthCheckModelCleared', 'id', 'model', 'providerCode', 'status', 'updatedAt'],
    '内置模型 PATCH 也只能返回当前行局部更新所需字段'
  )
  assert.equal(defaultModelReferenceCount(builtIn.provider_code, builtIn.model), 0, '隐藏内置模型必须定点清理个人与系统默认检查模型')
  const builtInAfter = builtInModelRow(builtIn.id)
  assert.deepEqual(
    changedColumns(builtInBefore, builtInAfter),
    ['catalog_visible', 'source', 'updated_at'],
    '内置模型可见性 PATCH 只能修改目标字段与必要写入元数据'
  )
  const builtInNoOp = await requestEnvelope<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/providers/${builtIn.provider_code}/models/${builtIn.id}`,
    cookie,
    'PATCH',
    { expectedUpdatedAt: builtInUpdated.updatedAt, catalogVisible: false }
  )
  assert.equal(builtInNoOp.updatedAt, builtInUpdated.updatedAt, '内置模型同值 PATCH 不得推进版本')
  assert.deepEqual(builtInModelRow(builtIn.id), builtInAfter, '内置模型同值 PATCH 不得写入任何列')

  const today = new Date().toISOString().slice(0, 10)
  const builtInDisabled = await requestEnvelope<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/providers/${builtIn.provider_code}/models/${builtIn.id}`,
    cookie,
    'PATCH',
    { expectedUpdatedAt: builtInUpdated.updatedAt, status: 'disabled', shutdownDate: today }
  )
  assert.equal(builtInDisabled.status, 'disabled', '管理 PATCH 必须允许停用已隐藏内置模型')
  assert.equal(builtInDisabled.defaultHealthCheckModelCleared, undefined, '已经不可用的模型继续停用不得重复触发默认模型清理')

  const builtInManagementCatalog = await getEnvelope<Array<{ id?: string }>>(
    baseUrl,
    `/__aisys__/api/providers/${encodeURIComponent(builtIn.provider_code)}/models?viewScope=admin&includeInactive=true&includeUnpriced=true`,
    cookie
  )
  assert(builtInManagementCatalog.some((model) => model.id === builtIn.id), '管理目录必须返回隐藏/停用/到期内置模型供恢复')
  const builtInOrdinaryCatalog = await getEnvelope<Array<{ id?: string }>>(
    baseUrl,
    `/__aisys__/api/providers/${encodeURIComponent(builtIn.provider_code)}/models?viewScope=self&includeUnpriced=true`,
    cookie
  )
  assert(!builtInOrdinaryCatalog.some((model) => model.id === builtIn.id), '普通目录必须排除隐藏内置模型')
  const builtInTestCatalog = await modelCatalogService.listProviderModelTestCatalogAsync({
    providerCode: builtIn.provider_code,
    systemAccountId: admin.id
  })
  assert(!builtInTestCatalog.some((model) => model.id === builtIn.id), '测试目录必须排除隐藏内置模型')

  const expired = await requestEnvelope<{ id: string; providerCode: string; model: string }>(
    baseUrl,
    '/__aisys__/api/providers/openai/models',
    cookie,
    'POST',
    {
      scope: 'global',
      model: 'provider-model-expired-regression',
      status: 'active',
      mode: 'text',
      supportedApiProtocols: ['responses'],
      shutdownDate: today,
      inputUsdPer1M: 1,
      outputUsdPer1M: 2
    }
  )
  const expiredManagementCatalog = await getEnvelope<Array<{ id?: string }>>(
    baseUrl,
    '/__aisys__/api/providers/openai/models?viewScope=admin&includeInactive=true&includeUnpriced=true',
    cookie
  )
  assert(expiredManagementCatalog.some((model) => model.id === expired.id), '管理目录必须保留已到期自定义模型供恢复')
  const expiredOrdinaryCatalog = await getEnvelope<Array<{ id?: string }>>(
    baseUrl,
    '/__aisys__/api/providers/openai/models?viewScope=self&includeUnpriced=true',
    cookie
  )
  assert(!expiredOrdinaryCatalog.some((model) => model.id === expired.id), '普通目录必须排除 shutdownDate 已生效的自定义模型')
  const expiredTestCatalog = await modelCatalogService.listProviderModelTestCatalogAsync({
    providerCode: expired.providerCode,
    systemAccountId: admin.id
  })
  assert(!expiredTestCatalog.some((model) => model.id === expired.id), '测试目录必须排除 shutdownDate 已生效的自定义模型')

  assertNarrowPatchSource()

  console.log('provider-model-demand-write-regression: ok')
} finally {
  await closeServer(server)
  await sqliteReadWorkerPool.closeSqliteReadWorkerPool()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function customModelRow(id: string): Record<string, unknown> {
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT * FROM custom_provider_models WHERE id = ?')
    .get(id) as unknown as Record<string, unknown> | undefined
  assert(row, `自定义模型不存在：${id}`)
  return row
}

function builtInModelRow(id: string): Record<string, unknown> {
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT * FROM provider_model_catalog WHERE id = ?')
    .get(id) as unknown as Record<string, unknown> | undefined
  assert(row, `内置模型不存在：${id}`)
  return row
}

function changedColumns(before: Record<string, unknown>, after: Record<string, unknown>): string[] {
  return Object.keys(after).filter((key) => before[key] !== after[key]).sort()
}

function setDefaultModelReferences(systemAccountId: string, providerCode: string, model: string): void {
  const now = new Date().toISOString()
  const database = databaseModule.getBusinessDatabase()
  database.prepare(`
    INSERT INTO provider_default_health_check_models (system_account_id, provider_code, model, created_at, updated_at)
    VALUES (?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, provider_code) DO UPDATE SET model = excluded.model, updated_at = excluded.updated_at
  `).run(systemAccountId, providerCode, model, now, now)
  database.prepare(`
    INSERT INTO provider_system_default_health_check_models (provider_code, model, created_at, updated_at)
    VALUES (?, ?, ?, ?)
    ON CONFLICT(provider_code) DO UPDATE SET model = excluded.model, updated_at = excluded.updated_at
  `).run(providerCode, model, now, now)
}

function defaultModelReferenceCount(providerCode: string, model: string): number {
  const database = databaseModule.getBusinessDatabase()
  const personal = database.prepare(`
    SELECT COUNT(*) AS count
    FROM provider_default_health_check_models
    WHERE provider_code = ? AND model = ?
  `).get(providerCode, model) as unknown as { count: number }
  const system = database.prepare(`
    SELECT COUNT(*) AS count
    FROM provider_system_default_health_check_models
    WHERE provider_code = ? AND model = ?
  `).get(providerCode, model) as unknown as { count: number }
  return Number(personal.count) + Number(system.count)
}

function totalDefaultModelReferenceCount(model: string): number {
  const database = databaseModule.getBusinessDatabase()
  const personal = database.prepare(`
    SELECT COUNT(*) AS count
    FROM provider_default_health_check_models
    WHERE model = ?
  `).get(model) as unknown as { count: number }
  const system = database.prepare(`
    SELECT COUNT(*) AS count
    FROM provider_system_default_health_check_models
    WHERE model = ?
  `).get(model) as unknown as { count: number }
  return Number(personal.count) + Number(system.count)
}

function assertNarrowPatchSource(): void {
  const currentDir = dirname(fileURLToPath(import.meta.url))
  const storageRoot = resolve(currentDir, '../..', 'storage')
  const customSource = readFileSync(resolve(storageRoot, 'custom-provider-models.repository.ts'), 'utf8')
  const builtInSource = readFileSync(resolve(storageRoot, 'provider-model-catalog.repository.ts'), 'utf8')
  const customPatch = sourceFunction(customSource, 'export async function patchCustomProviderModelAsync', 'export function deleteCustomProviderModel')
  const builtInPatch = sourceFunction(builtInSource, 'export async function patchBuiltInProviderModelConfigurationAsync', 'export async function updateBuiltInProviderModelPricesAsync')
  assert.match(customSource, /findCustomProviderModelPatchStateAsync[\s\S]*SELECT \$\{selectedColumns\}/, '自定义模型 PATCH 前置读取必须使用字段依赖投影')
  assert.match(builtInSource, /findBuiltInProviderModelPatchStateAsync[\s\S]*SELECT \$\{selectedColumns\}/, '内置模型 PATCH 前置读取必须使用字段依赖投影')
  assert.doesNotMatch(customPatch, /findCustomProviderModelByIdAsync|customProviderModelColumns\(\)/, '自定义模型 PATCH 成功后不得再整行读取')
  assert.doesNotMatch(builtInPatch, /findBuiltInProviderModelByIdAsync|columns\(\)/, '内置模型 PATCH 成功后不得再整行读取')
  assert.match(customSource, /runtimeConfig\.databaseDriver === 'postgres' \? next\.catalogVisible !== false/, 'PostgreSQL 自定义模型 catalogVisible PATCH 必须绑定 boolean，不能绑定 0\/1')
  assert.match(customSource, /clauses\.push\('catalog_visible = TRUE'\)/, 'PostgreSQL 普通自定义目录必须使用 boolean 可见性谓词')
}

function sourceFunction(source: string, startMarker: string, endMarker: string): string {
  const start = source.indexOf(startMarker)
  const end = source.indexOf(endMarker, start + startMarker.length)
  assert(start >= 0 && end > start, `无法定位源码函数：${startMarker}`)
  return source.slice(start, end)
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
  const text = await response.text()
  assert(response.ok, `GET ${path} HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as { data: T }).data
}

async function requestEnvelope<T>(
  baseUrl: string,
  path: string,
  cookie: string,
  method: 'POST' | 'PATCH',
  body: unknown
): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method,
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert(response.ok, `${method} ${path} HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as { data: T }).data
}

async function onceListening(target: ReturnType<typeof express.application.listen>): Promise<void> {
  if (target.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    target.once('listening', resolvePromise)
    target.once('error', rejectPromise)
  })
}

async function closeServer(target?: ReturnType<typeof express.application.listen>): Promise<void> {
  if (!target?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    target.close((error) => error ? rejectPromise(error) : resolvePromise())
    target.closeIdleConnections?.()
  })
}
