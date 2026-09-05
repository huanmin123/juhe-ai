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
  const ordinaryUser = repositories.createSystemAccount({
    username: 'provider_model_ordinary_user',
    displayName: '模型权限回归用户',
    password: 'ProviderModelRegression123!',
    role: 'user',
    mustChangePassword: false
  })
  const ordinaryCookie = `juhe_ai_session=${repositories.createSession(ordinaryUser.id, 1).token}`

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

  const rejectedVisibility = await requestStatus(
    baseUrl,
    `/__aisys__/api/providers/openai/models/${created.id}`,
    cookie,
    'PATCH',
    { expectedUpdatedAt: updated.updatedAt, catalogVisible: false }
  )
  assert.equal(rejectedVisibility.status, 400, '自定义模型不得接受独立 catalogVisible 写状态')
  assert.deepEqual(customModelRow(created.id), rowAfter, '被拒绝的 catalogVisible PATCH 不得写入任何列')

  const unauthorizedPatch = await requestStatus(
    baseUrl,
    `/__aisys__/api/providers/openai/models/${created.id}`,
    ordinaryCookie,
    'PATCH',
    { expectedUpdatedAt: updated.updatedAt, notes: 'cross-owner-overwrite' }
  )
  assert.equal(unauthorizedPatch.status, 404, '普通用户 PATCH 他人模型必须在定位阶段返回 404')
  const unauthorizedDelete = await requestStatus(
    baseUrl,
    `/__aisys__/api/providers/openai/models/${created.id}`,
    ordinaryCookie,
    'DELETE'
  )
  assert.equal(unauthorizedDelete.status, 404, '普通用户 DELETE 他人模型必须在定位阶段返回 404')
  assert.deepEqual(customModelRow(created.id), rowAfter, '跨用户 PATCH/DELETE 不得修改或删除目标行')

  const concurrentResults = await Promise.all([
    requestStatus(baseUrl, `/__aisys__/api/providers/openai/models/${created.id}`, cookie, 'PATCH', {
      expectedUpdatedAt: updated.updatedAt,
      notes: 'concurrent-a'
    }),
    requestStatus(baseUrl, `/__aisys__/api/providers/openai/models/${created.id}`, cookie, 'PATCH', {
      expectedUpdatedAt: updated.updatedAt,
      notes: 'concurrent-b'
    })
  ])
  assert.deepEqual(concurrentResults.map((result) => result.status).sort(), [200, 409], '同版本并发 PATCH 只能有一个成功')
  const concurrentWinner = concurrentResults.find((result) => result.status === 200)?.data as Record<string, unknown> | undefined
  assert(concurrentWinner && typeof concurrentWinner.updatedAt === 'string', '并发 PATCH 成功响应必须返回新版本')
  assert(['concurrent-a', 'concurrent-b'].includes(String(customModelRow(created.id).notes)), '并发 PATCH 只能保留胜出者内容')

  const sharedModel = 'provider-model-shared-alternative-regression'
  const openAiAlternative = await createCustomModel(baseUrl, cookie, 'openai', sharedModel)
  const gptAlternative = await createCustomModel(baseUrl, cookie, 'gpt', sharedModel)
  setDefaultModelReferences(admin.id, 'openai', sharedModel)
  setDefaultModelReferences(admin.id, 'hybrid', sharedModel)
  const openAiExpired = await requestEnvelope<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/providers/openai/models/${openAiAlternative.id}`,
    cookie,
    'PATCH',
    { expectedUpdatedAt: openAiAlternative.updatedAt, shutdownDate: new Date().toISOString().slice(0, 10) }
  )
  assert.equal(openAiExpired.defaultHealthCheckModelCleared, undefined, '同名可用替代存在时不得误报默认模型已清理')
  assert.equal(totalDefaultModelReferenceCount(sharedModel), 4, '单一来源过期时必须保留仍有同名替代的聚合默认引用')
  await requestEnvelope(
    baseUrl,
    '/__aisys__/api/providers/openai/default-health-check-model?viewScope=admin',
    cookie,
    'PUT',
    { model: sharedModel }
  )
  const deleteAlternative = await requestEnvelope<{ deleted: boolean }>(
    baseUrl,
    `/__aisys__/api/providers/gpt/models/${gptAlternative.id}`,
    cookie,
    'DELETE'
  )
  assert.equal(deleteAlternative.deleted, true, '最后一个同名可用替代必须可删除')
  assert.equal(totalDefaultModelReferenceCount(sharedModel), 0, '删除最后一个同名可用替代必须清理相关默认引用')
  await assertHttpStatus(
    baseUrl,
    '/__aisys__/api/providers/openai/default-health-check-model?viewScope=admin',
    cookie,
    'PUT',
    { model: sharedModel },
    400,
    '已过期且无替代的模型不得设为默认检查模型'
  )

  const protocolLoss = await createCustomModel(baseUrl, cookie, 'openai', 'provider-model-protocol-loss-regression')
  setDefaultModelReferences(admin.id, 'openai', protocolLoss.model)
  setDefaultModelReferences(admin.id, 'hybrid', protocolLoss.model)
  const protocolLossResult = await requestEnvelope<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/providers/openai/models/${protocolLoss.id}`,
    cookie,
    'PATCH',
    { expectedUpdatedAt: protocolLoss.updatedAt, supportedApiProtocols: ['count_tokens'] }
  )
  assert.equal(protocolLossResult.defaultHealthCheckModelCleared, true, '移除最后一个文本生成协议必须通知默认模型已清理')
  assert.equal(totalDefaultModelReferenceCount(protocolLoss.model), 0, '仅剩非文本生成协议的模型不得继续被默认检查引用')

  const rollbackModel = await createCustomModel(baseUrl, cookie, 'openai', 'provider-model-cleanup-rollback-regression')
  setDefaultModelReferences(admin.id, 'openai', rollbackModel.model)
  const rollbackBefore = customModelRow(rollbackModel.id)
  installCleanupFailureTrigger(rollbackModel.model)
  try {
    const rollbackResponse = await requestStatus(
      baseUrl,
      `/__aisys__/api/providers/openai/models/${rollbackModel.id}`,
      cookie,
      'PATCH',
      { expectedUpdatedAt: rollbackModel.updatedAt, status: 'disabled' }
    )
    assert.equal(rollbackResponse.status, 400, '默认引用清理失败时 PATCH 必须整体失败')
  } finally {
    removeCleanupFailureTrigger()
  }
  assert.deepEqual(customModelRow(rollbackModel.id), rollbackBefore, '清理失败必须回滚模型字段与版本')
  assert.equal(defaultModelReferenceCount('openai', rollbackModel.model), 2, '清理失败必须保留个人与系统默认引用')

  const legacyVisibility = await createCustomModel(baseUrl, cookie, 'openai', 'provider-model-legacy-visibility-regression')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE custom_provider_models SET catalog_visible = 0 WHERE id = ?')
    .run(legacyVisibility.id)
  const legacyOrdinaryCatalog = await getEnvelope<Array<{ id?: string }>>(
    baseUrl,
    '/__aisys__/api/providers/openai/models?viewScope=self&includeUnpriced=true',
    cookie
  )
  const legacyOrdinaryItem = legacyOrdinaryCatalog.find((model) => model.id === legacyVisibility.id)
  assert(legacyOrdinaryItem, '历史 catalog_visible=false 不得让自定义模型从普通目录消失')
  const legacyTestCatalog = await modelCatalogService.listProviderModelTestCatalogAsync({
    providerCode: legacyVisibility.providerCode,
    systemAccountId: admin.id
  })
  assert(legacyTestCatalog.some((model) => model.id === legacyVisibility.id), '历史 catalog_visible=false 不得让自定义模型从测试目录消失')

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
  await requestEnvelope(
    baseUrl,
    `/__aisys__/api/providers/${encodeURIComponent(builtIn.provider_code)}/default-health-check-model?viewScope=admin`,
    cookie,
    'PUT',
    { model: builtIn.model }
  )
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
  await assertHttpStatus(
    baseUrl,
    `/__aisys__/api/providers/${encodeURIComponent(builtIn.provider_code)}/default-health-check-model?viewScope=admin`,
    cookie,
    'PUT',
    { model: builtIn.model },
    400,
    '隐藏内置模型不得设为默认检查模型'
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

  const expiringBuiltIn = databaseModule.getBusinessDatabase().prepare(`
    SELECT id, provider_code, model, updated_at
    FROM provider_model_catalog
    WHERE status = 'active' AND catalog_visible = 1 AND id <> ?
    ORDER BY provider_code, model
    LIMIT 1
  `).get(builtIn.id) as unknown as {
    id: string
    provider_code: string
    model: string
    updated_at: string
  }
  assert(expiringBuiltIn, '没有可用于到期回归的第二个内置模型')
  setDefaultModelReferences(admin.id, expiringBuiltIn.provider_code, expiringBuiltIn.model)
  const expiringBuiltInResult = await requestEnvelope<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/providers/${encodeURIComponent(expiringBuiltIn.provider_code)}/models/${expiringBuiltIn.id}`,
    cookie,
    'PATCH',
    { expectedUpdatedAt: expiringBuiltIn.updated_at, shutdownDate: today }
  )
  assert.equal(expiringBuiltInResult.defaultHealthCheckModelCleared, true, '内置模型到期必须仅在实际清理默认引用时返回标记')
  assert.equal(defaultModelReferenceCount(expiringBuiltIn.provider_code, expiringBuiltIn.model), 0, '内置模型到期必须清理默认检查模型')
  const expiringBuiltInManagementCatalog = await getEnvelope<Array<{ id?: string }>>(
    baseUrl,
    `/__aisys__/api/providers/${encodeURIComponent(expiringBuiltIn.provider_code)}/models?viewScope=admin&includeInactive=true&includeUnpriced=true`,
    cookie
  )
  assert(expiringBuiltInManagementCatalog.some((model) => model.id === expiringBuiltIn.id), '管理目录必须保留已到期内置模型')
  const expiringBuiltInOrdinaryCatalog = await getEnvelope<Array<{ id?: string }>>(
    baseUrl,
    `/__aisys__/api/providers/${encodeURIComponent(expiringBuiltIn.provider_code)}/models?viewScope=self&includeUnpriced=true`,
    cookie
  )
  assert(!expiringBuiltInOrdinaryCatalog.some((model) => model.id === expiringBuiltIn.id), '普通目录必须排除已到期内置模型')
  const expiringBuiltInTestCatalog = await modelCatalogService.listProviderModelTestCatalogAsync({
    providerCode: expiringBuiltIn.provider_code,
    systemAccountId: admin.id
  })
  assert(!expiringBuiltInTestCatalog.some((model) => model.id === expiringBuiltIn.id), '测试目录必须排除已到期内置模型')
  await assertHttpStatus(
    baseUrl,
    `/__aisys__/api/providers/${encodeURIComponent(expiringBuiltIn.provider_code)}/default-health-check-model?viewScope=admin`,
    cookie,
    'PUT',
    { model: expiringBuiltIn.model },
    400,
    '已到期内置模型不得设为默认检查模型'
  )

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
  assert.match(customPatch, /client\.transaction\(async \(tx\)/, '自定义模型 PATCH 与默认引用清理必须在同一事务')
  assert.match(customPatch, /clearUnavailableProviderModelDefaultReferencesInTransaction\(tx/, '自定义模型 PATCH 不得在事务外清理默认引用')
  const customInput = sourceFunction(customSource, 'export interface UpsertCustomProviderModelInput', 'export type CustomProviderModelPatchField')
  assert.doesNotMatch(customInput, /catalogVisible/, '自定义模型写入约定不得包含 catalogVisible')
  assert.match(customSource, /catalogVisible:\s*true/, '历史自定义模型读取时必须归一为可见')
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
  method: 'POST' | 'PATCH' | 'PUT' | 'DELETE',
  body?: unknown
): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method,
    headers: { cookie, 'content-type': 'application/json' },
    ...(body === undefined ? {} : { body: JSON.stringify(body) })
  })
  const text = await response.text()
  assert(response.ok, `${method} ${path} HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as { data: T }).data
}

async function requestStatus(
  baseUrl: string,
  path: string,
  cookie: string,
  method: 'POST' | 'PATCH' | 'PUT' | 'DELETE',
  body?: unknown
): Promise<{ status: number; data?: unknown; text: string }> {
  const response = await fetch(`${baseUrl}${path}`, {
    method,
    headers: { cookie, 'content-type': 'application/json' },
    ...(body === undefined ? {} : { body: JSON.stringify(body) })
  })
  const responseText = await response.text()
  let data: unknown
  try {
    data = (JSON.parse(responseText) as { data?: unknown }).data
  } catch {
    data = undefined
  }
  return { status: response.status, data, text: responseText }
}

async function assertHttpStatus(
  baseUrl: string,
  path: string,
  cookie: string,
  method: 'POST' | 'PATCH' | 'PUT' | 'DELETE',
  body: unknown,
  expectedStatus: number,
  message: string
): Promise<void> {
  const response = await requestStatus(baseUrl, path, cookie, method, body)
  assert.equal(response.status, expectedStatus, `${message}: ${response.text}`)
}

async function createCustomModel(
  baseUrl: string,
  cookie: string,
  providerCode: string,
  model: string
): Promise<{ id: string; providerCode: string; model: string; updatedAt: string }> {
  return requestEnvelope(baseUrl, `/__aisys__/api/providers/${encodeURIComponent(providerCode)}/models`, cookie, 'POST', {
    scope: 'global',
    model,
    status: 'active',
    mode: 'text',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2
  })
}

function installCleanupFailureTrigger(model: string): void {
  databaseModule.getBusinessDatabase().exec(`
    CREATE TRIGGER provider_model_cleanup_failure_regression
    BEFORE DELETE ON provider_default_health_check_models
    WHEN OLD.model = '${model.replaceAll("'", "''")}'
    BEGIN
      SELECT RAISE(ABORT, 'simulated default reference cleanup failure');
    END
  `)
}

function removeCleanupFailureTrigger(): void {
  databaseModule.getBusinessDatabase().exec('DROP TRIGGER IF EXISTS provider_model_cleanup_failure_regression')
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
