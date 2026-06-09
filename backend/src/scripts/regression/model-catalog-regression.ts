import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-catalog-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'model-catalog-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  catalogService,
  { providersRouter },
  { requireAuth },
  { requestContextMiddleware },
  repositories
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/model-pricing/model-catalog.service.js'),
  import('../../modules/providers/providers.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/repositories.js')
])

try {
  const pricedModel = catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-global',
    scope: 'global',
    visibility: 'public',
    supportedApiProtocols: ['responses'],
    releaseDate: '2026-01-02',
    inputUsdPer1M: 2,
    outputUsdPer1M: 8,
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-hidden-target',
    scope: 'global',
    visibility: 'mapping_target_only',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 3,
    outputUsdPer1M: 9,
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-alias',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    visibility: 'public',
    supportedApiProtocols: ['responses'],
    pricingModel: 'gpt-regression-hidden-target',
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-draft',
    scope: 'global',
    status: 'draft',
    visibility: 'public',
    supportedApiProtocols: ['responses'],
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-5.5',
    scope: 'global',
    visibility: 'public',
    supportedApiProtocols: ['responses'],
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-overridden-pricing-alias',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    visibility: 'public',
    supportedApiProtocols: ['responses'],
    pricingModel: 'gpt-5.5',
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-audio',
    scope: 'global',
    mode: 'audio',
    visibility: 'public',
    supportedApiProtocols: ['audio'],
    audioInputUsdPer1M: 4,
    audioOutputUsdPer1M: 12,
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-image-unit',
    scope: 'global',
    mode: 'image',
    visibility: 'public',
    supportedApiProtocols: ['images'],
    outputUsdPerImage: 0.04,
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'openai',
    model: 'openai-regression-global',
    scope: 'global',
    visibility: 'public',
    supportedApiProtocols: ['responses'],
    releaseDate: '2026-05-03',
    inputUsdPer1M: 1,
    outputUsdPer1M: 3,
    actorSystemAccountId: 'sys_admin'
  })

  const publicCatalog = catalogService.listProviderModelCatalog({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin'
  })
  assertCatalogReleaseDateDescending(publicCatalog, 'GPT 公开模型目录')
  const publicModels = new Set(publicCatalog.map((item) => item.model))
  assert(publicModels.has('gpt-regression-global'), '全局自定义模型应进入公开模型目录')
  assert(publicModels.has('gpt-regression-alias'), '带 pricingModel 的个人模型应进入个人公开模型目录')
  assert(publicModels.has('gpt-regression-audio'), '只有音频价格的自定义模型应进入公开模型目录')
  assert(publicModels.has('gpt-regression-image-unit'), '只有按张图片价格的自定义模型应进入公开模型目录')
  assert.equal(publicModels.has('gpt-regression-hidden-target'), false, 'mapping_target_only 模型不应进入公开模型目录')
  assert.equal(publicModels.has('gpt-regression-draft'), false, '草稿模型不应进入公开模型目录')
  assert.equal(publicModels.has('gpt-regression-overridden-pricing-alias'), false, 'pricingModel 目标被无价自定义模型覆盖时别名不应进入公开模型目录')
  assert.equal(publicModels.has('openai-regression-global'), false, 'GPT 模型目录不应反向包含 OpenAI 兼容自定义模型')

  const openAICompatibleCatalog = catalogService.listProviderModelCatalog({
    providerCode: 'openai',
    systemAccountId: 'sys_admin'
  })
  assertCatalogReleaseDateDescending(openAICompatibleCatalog, 'OpenAI 兼容聚合模型目录')
  assert(openAICompatibleCatalog.some((item) => item.model === 'gpt-regression-global' && item.providerCode === 'gpt'), 'OpenAI 兼容模型目录应聚合 GPT 的 OpenAI v1 模型')
  assert(openAICompatibleCatalog.some((item) => item.model === 'openai-regression-global' && item.providerCode === 'openai'), 'OpenAI 兼容模型目录应保留自身模型')
  assert.equal(openAICompatibleCatalog[0]?.model, 'openai-regression-global', '模型目录应按发布时间倒序展示最新模型')

  const managementCatalog = catalogService.listProviderModelCatalog({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    includeMappingTargets: true,
    includeInactive: true,
    includeUnpriced: true
  })
  assert(managementCatalog.some((item) => item.model === 'gpt-regression-draft'), '管理模型目录应能看到草稿模型')

  const mappingCatalog = catalogService.listProviderModelCatalog({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    includeMappingTargets: true
  })
  assertCatalogReleaseDateDescending(mappingCatalog, 'GPT 映射目标模型目录')
  assert(mappingCatalog.some((item) => item.model === 'gpt-regression-hidden-target'), '映射目标目录应包含 mapping_target_only 模型')

  const response = catalogService.buildOpenAIModelsResponseFromCatalog(publicCatalog)
  assert.equal(response.object, 'list', '/v1/models 顶层 object 必须是 list')
  assert(Array.isArray(response.data), '/v1/models data 必须是数组')
  for (const item of response.data) {
    assert.deepEqual(Object.keys(item).sort(), ['created', 'id', 'object', 'owned_by'], '/v1/models 所有模型项都只能暴露 OpenAI 标准字段')
    assert.equal(item.object, 'model', '/v1/models 所有模型项 object 必须是 model')
    assert(Number.isInteger(item.created), '/v1/models 所有模型项 created 必须是 Unix 秒整数')
    assert.equal(typeof item.owned_by, 'string', '/v1/models 所有模型项 owned_by 必须是字符串')
  }
  const globalModel = response.data.find((item) => item.id === 'gpt-regression-global')
  assert(globalModel, '/v1/models 应包含公开自定义模型')
  assert.deepEqual(Object.keys(globalModel).sort(), ['created', 'id', 'object', 'owned_by'], '/v1/models 模型项只能暴露 OpenAI 标准字段')
  assert.equal(globalModel.object, 'model', '/v1/models 模型项 object 必须是 model')
  assert.equal(globalModel.created, Date.parse('2026-01-02T00:00:00.000Z') / 1000, '/v1/models created 应为 Unix 秒')
  assert.equal(response.data.some((item) => item.id === 'gpt-regression-hidden-target'), false, '/v1/models 不应暴露 mapping_target_only 模型')

  const aliasCost = catalogService.estimateCatalogCostUsd({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-alias',
    inputTokens: 1_000_000,
    outputTokens: 1_000_000
  })
  assert.equal(aliasCost, 12, 'pricingModel 应按目标模型直接价格计费')
  const overriddenAliasCost = catalogService.estimateCatalogCostUsd({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-overridden-pricing-alias',
    inputTokens: 1_000_000,
    outputTokens: 1_000_000
  })
  assert.equal(overriddenAliasCost, undefined, 'pricingModel 目标被无价自定义模型覆盖时不应回退到被覆盖的内置模型价格')
  const audioCost = catalogService.estimateCatalogCostUsd({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-audio',
    inputTokens: 1_000_000,
    outputTokens: 2_000_000
  })
  assert.equal(audioCost, 28, '只有音频价格的自定义模型应按音频 token 成本计费')
  const audioBreakdown = catalogService.buildCatalogCostBreakdown({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-audio',
    inputAudioTokens: 1_000_000,
    outputAudioTokens: 1_000_000
  })
  assert.equal(audioBreakdown?.inputAudioCostUsd, 4, '音频输入成本应进入成本拆解')
  assert.equal(audioBreakdown?.outputAudioCostUsd, 12, '音频输出成本应进入成本拆解')
  assert.equal(audioBreakdown?.inputAudioUsdPer1M, 4, '音频输入单价应进入成本拆解')
  assert.equal(audioBreakdown?.outputAudioUsdPer1M, 12, '音频输出单价应进入成本拆解')
  const imageUnitCost = catalogService.estimateCatalogCostUsd({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-image-unit',
    outputImageCount: 3
  })
  assert.equal(imageUnitCost, 0.12, '只有按张图片价格的自定义模型应按图片张数计费')
  const imageUnitBreakdown = catalogService.buildCatalogCostBreakdown({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-image-unit',
    outputImageCount: 2
  })
  assert.equal(imageUnitBreakdown?.outputImageUnitCostUsd, 0.08, '按张图片成本应进入成本拆解')
  assert.equal(imageUnitBreakdown?.outputUsdPerImage, 0.04, '每张图片单价应进入成本拆解')

  catalogService.saveCustomProviderModel({
    id: pricedModel.id,
    providerCode: 'gpt',
    model: 'gpt-regression-global',
    scope: 'global',
    visibility: 'public',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: null,
    outputUsdPer1M: null,
    pricingModel: 'gpt-regression-hidden-target',
    actorSystemAccountId: 'sys_admin'
  })
  const remappedCost = catalogService.estimateCatalogCostUsd({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-global',
    inputTokens: 1_000_000,
    outputTokens: 1_000_000
  })
  assert.equal(remappedCost, 12, '清空直接价格后应能切换为 pricingModel 计费')

  await assertProviderModelHttpContracts()

  console.log('model catalog regression passed')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertCatalogReleaseDateDescending(items: Array<{ model: string; releaseDate?: string }>, label: string): void {
  for (let index = 1; index < items.length; index += 1) {
    const previous = items[index - 1]
    const current = items[index]
    if (previous.releaseDate && !current.releaseDate) continue
    assert.ok(
      Boolean(previous.releaseDate) || !current.releaseDate,
      `${label}排序错误：未填写发布时间的 ${previous.model} 不应排在 ${current.model}(${current.releaseDate}) 前面`
    )
    if (!previous.releaseDate || !current.releaseDate) continue
    assert.ok(
      previous.releaseDate >= current.releaseDate,
      `${label}排序错误：${previous.model}(${previous.releaseDate}) 应排在 ${current.model}(${current.releaseDate}) 前面`
    )
  }
}

async function assertProviderModelHttpContracts(): Promise<void> {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  repositories.updateSystemAccount(admin.id, { mustChangePassword: false })
  const userA = repositories.createSystemAccount({
    username: 'model_catalog_user_a',
    displayName: '模型目录用户A',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const userB = repositories.createSystemAccount({
    username: 'model_catalog_user_b',
    displayName: '模型目录用户B',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const adminCookie = sessionCookie(admin.id)
  const userACookie = sessionCookie(userA.id)
  const userBCookie = sessionCookie(userB.id)

  const app = express()
  app.use(requestContextMiddleware)
  app.use(express.json({ limit: '1mb' }))
  app.use('/__aisys__/api', requireAuth)
  app.use('/__aisys__/api/providers', providersRouter)

  let server: ReturnType<typeof app.listen> | undefined
  try {
    server = app.listen(0, '127.0.0.1')
    await onceListening(server)
    const address = server.address()
    if (!address || typeof address === 'string') {
      throw new Error('模型目录 HTTP 回归服务地址不可用')
    }
    const baseUrl = `http://127.0.0.1:${address.port}`

    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models`,
      userACookie,
      'POST',
      {
        model: 'gpt-http-forbidden-global',
        scope: 'global',
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      },
      403,
      '普通用户不应创建全局自定义模型'
    )

    const userAModel = await postEnvelope<{ id: string; model: string; scope: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      userACookie,
      {
        model: 'gpt-http-user-a',
        scope: 'personal',
        visibility: 'public',
        supportedApiProtocols: ['responses'],
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      }
    )
    assert.equal(userAModel.scope, 'personal', '普通用户创建的模型应固定为个人模型')

    const userAHiddenTarget = await postEnvelope<{ id: string; model: string; visibility: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      userACookie,
      {
        model: 'gpt-http-user-a-hidden-target',
        scope: 'personal',
        visibility: 'mapping_target_only',
        supportedApiProtocols: ['responses'],
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      }
    )
    assert.equal(userAHiddenTarget.visibility, 'mapping_target_only', '个人映射目标模型应保存 visibility')

    const userADraft = await postEnvelope<{ id: string; model: string; status: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      userACookie,
      {
        model: 'gpt-http-user-a-draft',
        scope: 'personal',
        status: 'draft',
        visibility: 'public'
      }
    )
    assert.equal(userADraft.status, 'draft', '草稿模型允许暂不配置价格')

    const adminGlobalModel = await postEnvelope<{ id: string; model: string; scope: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      adminCookie,
      {
        model: 'gpt-http-global',
        scope: 'global',
        visibility: 'public',
        supportedApiProtocols: ['responses'],
        inputUsdPer1M: 2,
        outputUsdPer1M: 4
      }
    )
    assert.equal(adminGlobalModel.scope, 'global', '管理员应能创建全局自定义模型')

    await patchEnvelope(
      baseUrl,
      `/__aisys__/api/providers/openai/models/${adminGlobalModel.id}`,
      adminCookie,
      { displayName: 'HTTP 全局模型' }
    )
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models/${adminGlobalModel.id}`,
      userACookie,
      'PATCH',
      { displayName: '越权修改' },
      403,
      '普通用户不应修改全局自定义模型'
    )
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models/${userAModel.id}`,
      userBCookie,
      'PATCH',
      { displayName: '越权修改' },
      403,
      '普通用户不应修改他人的个人自定义模型'
    )
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models/${userAModel.id}`,
      userBCookie,
      'DELETE',
      undefined,
      403,
      '普通用户不应删除他人的个人自定义模型'
    )
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models/${userAModel.id}`,
      userACookie,
      'PATCH',
      { inputUsdPer1M: null, outputUsdPer1M: null },
      400,
      '启用模型清空价格且没有 pricingModel 时应拒绝保存'
    )

    const userAVisible = await getEnvelope<Array<{ model: string; scope: string; visibility: string; status: string }>>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      userACookie
    )
    assert(userAVisible.some((item) => item.model === 'gpt-http-user-a' && item.scope === 'personal'), '用户应能看到自己的公开个人模型')
    assert(userAVisible.some((item) => item.model === 'gpt-http-global' && item.scope === 'global'), '用户应能看到管理员全局模型')
    assert.equal(userAVisible.some((item) => item.model === 'gpt-http-user-a-hidden-target'), false, '默认管理模型目录不应返回 mapping_target_only')
    assert.equal(userAVisible.some((item) => item.model === 'gpt-http-user-a-draft'), false, '默认管理模型目录不应返回草稿模型')

    const userAMappingVisible = await getEnvelope<Array<{ model: string; visibility: string }>>(
      baseUrl,
      '/__aisys__/api/providers/openai/models?includeMappingTargets=true',
      userACookie
    )
    assert(userAMappingVisible.some((item) => item.model === 'gpt-http-user-a-hidden-target' && item.visibility === 'mapping_target_only'), 'includeMappingTargets 应返回当前用户可见的映射目标模型')

    const userAMaintenanceVisible = await getEnvelope<Array<{ model: string; status: string }>>(
      baseUrl,
      '/__aisys__/api/providers/openai/models?includeInactive=true&includeUnpriced=true',
      userACookie
    )
    assert(userAMaintenanceVisible.some((item) => item.model === 'gpt-http-user-a-draft' && item.status === 'draft'), '普通用户维护视图应能看到自己的草稿模型')

    const userBVisible = await getEnvelope<Array<{ model: string }>>(
      baseUrl,
      '/__aisys__/api/providers/openai/models?includeInactive=true&includeUnpriced=true&includeMappingTargets=true',
      userBCookie
    )
    assert.equal(userBVisible.some((item) => item.model === 'gpt-http-user-a'), false, '个人模型不应泄露给其他用户')
    assert.equal(userBVisible.some((item) => item.model === 'gpt-http-user-a-hidden-target'), false, '个人映射目标不应泄露给其他用户')
    assert.equal(userBVisible.some((item) => item.model === 'gpt-http-user-a-draft'), false, '个人草稿模型不应泄露给其他用户')
  } finally {
    await closeServer(server)
  }
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
  return await unwrapEnvelope<T>(response, path)
}

async function postEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  return await unwrapEnvelope<T>(response, path)
}

async function patchEnvelope<T = unknown>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'PATCH',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  return await unwrapEnvelope<T>(response, path)
}

async function unwrapEnvelope<T>(response: Response, path: string): Promise<T> {
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text}`)
  }
  return (JSON.parse(text) as { data: T }).data
}

async function assertHttpStatus(
  url: string,
  cookie: string,
  method: 'POST' | 'PATCH' | 'DELETE',
  body: unknown,
  expectedStatus: number,
  message: string
): Promise<void> {
  const response = await fetch(url, {
    method,
    headers: body === undefined ? { cookie } : { cookie, 'content-type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body)
  })
  assert.equal(response.status, expectedStatus, `${message}，实际状态 ${response.status}: ${await response.text()}`)
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function onceListening(server: ReturnType<typeof express.application.listen>): Promise<void> {
  if (server.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

async function closeServer(server?: ReturnType<typeof express.application.listen>): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    const timeout = setTimeout(() => {
      server.closeAllConnections?.()
      resolvePromise()
    }, 1000)
    server.close((error) => {
      clearTimeout(timeout)
      if (error) {
        rejectPromise(error)
      } else {
        resolvePromise()
      }
    })
    server.closeIdleConnections?.()
  })
}
