import assert from 'node:assert/strict'

import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { HYBRID_PROVIDER_CODE, OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { providerModelCatalogId } from '../../storage/provider-model-catalog-id.js'
import { DEFAULT_PROVIDER_SEEDS } from '../../storage/schema-defaults.js'
import { seedDefaults } from '../../storage/schema.js'
import {
  listProviderModelSelectionOptionsAsync,
  mergeProviderModelOptionRows
} from '../../modules/providers/provider-model-options.service.js'


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
  clientCatalogService,
  modelPricingService,
  { providersRouter },
  { requireAuth },
  { requestContextMiddleware },
  repositories,
  customProviderModelsRepository,
  providerModelCatalogRepository,
  gatewayCacheInvalidation,
  inflightQuotaService,
  sqliteReadWorkerPool
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/model-pricing/model-catalog.service.js'),
  import('../../modules/model-pricing/client-model-catalog.service.js'),
  import('../../modules/model-pricing/model-pricing.service.js'),
  import('../../modules/providers/providers.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/repositories.js'),
  import('../../storage/custom-provider-models.repository.js'),
  import('../../storage/provider-model-catalog.repository.js'),
  import('../../shared/gateway-cache-invalidation.js'),
  import('../../modules/gateway/quota/api-key-inflight-quota.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const modelCatalogSource = readFileSync(resolve('src/modules/model-pricing/model-catalog.service.ts'), 'utf8')
assert.match(modelCatalogSource, /await providerModelCatalogInvalidationInFlight[\s\S]{0,300}const cacheKey/, '模型目录读取必须等待正在进行的全局失效')
assert.match(modelCatalogSource, /clearProviderModelCatalogCaches[\s\S]{0,1200}Promise\.allSettled\(pendingLoads\)[\s\S]{0,500}clearProviderModelCatalogSharedCacheAsync/, '模型目录失效必须等待旧 loader 和迟到 cache set 完成后再清理 shared cache')

try {
  const providerModelCatalogRepositorySource = readFileSync(
    new URL('../../storage/provider-model-catalog.repository.ts', import.meta.url),
    'utf8'
  )
  assert.match(
    providerModelCatalogRepositorySource,
    /FROM juhe_business\.provider_model_catalog[\s\S]*?catalog_visible = TRUE/,
    'PostgreSQL 内置模型目录必须用 boolean TRUE 过滤 catalog_visible，不能与整数 1 比较'
  )

  const businessDatabase = databaseModule.getBusinessDatabase()
  const legacyIdTarget = businessDatabase.prepare(`
    SELECT id, provider_code, model FROM provider_model_catalog
    WHERE provider_code = 'gpt' AND status = 'active' AND catalog_visible = 1
    ORDER BY model LIMIT 1
  `).get() as unknown as { id: string; provider_code: string; model: string }
  const legacyModelId = `provider_model_${legacyIdTarget.provider_code}_${legacyIdTarget.model.replace(/[^a-zA-Z0-9]+/g, '_')}`
  businessDatabase.prepare('UPDATE provider_model_catalog SET id = ? WHERE id = ?').run(legacyModelId, legacyIdTarget.id)
  seedDefaults(businessDatabase)
  const legacyIdAfterSeed = businessDatabase.prepare(`
    SELECT status, catalog_visible FROM provider_model_catalog WHERE provider_code = ? AND model = ?
  `).get(legacyIdTarget.provider_code, legacyIdTarget.model) as unknown as { status: string; catalog_visible: number }
  assert.equal(legacyIdAfterSeed.status, 'active', '旧版无 hash 内置模型 ID 升级后不得被误判为 stale')
  assert.equal(legacyIdAfterSeed.catalog_visible, 1, '旧版无 hash 内置模型 ID 升级后不得被隐藏')
  businessDatabase.prepare(`
    UPDATE provider_model_catalog SET status = 'disabled', catalog_visible = 0, source = 'legacy-pricing-snapshot'
    WHERE provider_code = ? AND model = ?
  `).run(legacyIdTarget.provider_code, legacyIdTarget.model)
  seedDefaults(businessDatabase)
  const repairedSnapshotVisibility = businessDatabase.prepare(`
    SELECT status, catalog_visible FROM provider_model_catalog WHERE provider_code = ? AND model = ?
  `).get(legacyIdTarget.provider_code, legacyIdTarget.model) as unknown as { status: string; catalog_visible: number }
  assert.equal(repairedSnapshotVisibility.status, 'active', '旧快照隐藏的模型补齐事实后应随新快照恢复状态')
  assert.equal(repairedSnapshotVisibility.catalog_visible, 1, '旧快照隐藏的模型补齐事实后应随新快照恢复可见性')
  const manuallyHidden = await providerModelCatalogRepository.updateBuiltInProviderModelConfigurationAsync(legacyModelId, { catalogVisible: false })
  assert.equal(manuallyHidden?.source, 'manual-visibility-override', '手工隐藏必须与历史快照隐藏使用不同来源标记')
  seedDefaults(businessDatabase)
  assert.equal(
    (businessDatabase.prepare('SELECT catalog_visible FROM provider_model_catalog WHERE id = ?').get(legacyModelId) as { catalog_visible: number }).catalog_visible,
    0,
    '默认 seed 不得重新打开管理员手工隐藏的内置模型'
  )
  await providerModelCatalogRepository.updateBuiltInProviderModelConfigurationAsync(legacyModelId, { catalogVisible: true })
  runtimeConfig.processRole = 'db-service'
  assert.equal(sqliteReadWorkerPool.sqliteReadWorkerPoolEnabled(), true, '模型目录缓存回归必须真实启用 SQLite read worker')
  await gatewayCacheInvalidation.notifyGatewayRuntimeCacheInvalidationAsync('provider_model_configuration_updated')
  const sequentialHandledBefore = sqliteReadWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs
  await catalogService.listProviderModelCatalogAsync({
    providerCode: 'gpt',
    systemAccountId: 'sys_model_cache',
    includeUnpriced: true
  })
  await catalogService.listProviderModelCatalogAsync({
    providerCode: 'gpt',
    systemAccountId: 'sys_model_cache',
    includeUnpriced: true
  })
  assert.equal(
    sqliteReadWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs - sequentialHandledBefore,
    1,
    'SQLite read worker 模式相同模型目录连续读取只能在首次 cache miss 时执行一次 worker job'
  )

  await gatewayCacheInvalidation.notifyGatewayRuntimeCacheInvalidationAsync('provider_model_configuration_updated')
  const concurrentHandledBefore = sqliteReadWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs
  await Promise.all([
    catalogService.listProviderModelCatalogAsync({ providerCode: 'gpt', systemAccountId: 'sys_model_cache', includeUnpriced: true }),
    catalogService.listProviderModelCatalogAsync({ providerCode: 'gpt', systemAccountId: 'sys_model_cache', includeUnpriced: true })
  ])
  assert.equal(
    sqliteReadWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs - concurrentHandledBefore,
    1,
    'SQLite read worker 模式相同模型目录并发 miss 必须共享一个 loader'
  )

  const scopedHandledBefore = sqliteReadWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs
  await Promise.all([
    catalogService.listProviderModelCatalogAsync({ providerCode: 'gpt', systemAccountId: 'sys_model_cache_a', includeUnpriced: true }),
    catalogService.listProviderModelCatalogAsync({ providerCode: 'gpt', systemAccountId: 'sys_model_cache_b', includeUnpriced: true })
  ])
  assert.equal(
    sqliteReadWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs - scopedHandledBefore,
    2,
    '不同系统账户 scope 的个人模型目录不能共享后端缓存'
  )

  await gatewayCacheInvalidation.notifyGatewayRuntimeCacheInvalidationAsync('provider_model_configuration_updated')
  const invalidatedHandledBefore = sqliteReadWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs
  await catalogService.listProviderModelCatalogAsync({
    providerCode: 'gpt',
    systemAccountId: 'sys_model_cache',
    includeUnpriced: true
  })
  assert.equal(
    sqliteReadWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs - invalidatedHandledBefore,
    1,
    '模型目录失效后下一次读取必须重新执行一次 read worker job'
  )
  runtimeConfig.processRole = 'worker'
  assert.equal(
    providerModelCatalogId('gpt', 'gpt-5.6-sol'),
    'provider_model_gpt_gpt_5_6_sol_69ec47b65152',
    'Node 稳定模型 ID 必须与 Go migration generator 一致'
  )
  const sqliteBuiltInModels = databaseModule.getBusinessDatabase().prepare(`
    SELECT id, provider_code, model
    FROM provider_model_catalog
    ORDER BY provider_code, model
  `).all() as Array<{ id: string; provider_code: string; model: string }>
  const expectedSqliteModelKeys = DEFAULT_PROVIDER_SEEDS
    .filter((provider) => provider.code !== HYBRID_PROVIDER_CODE && provider.code !== 'openai')
    .flatMap((provider) => modelPricingService.listProviderModelPricing(provider.code)
      .map((model) => `${provider.code}\u0000${model.model}`))
    .sort()
  const sqliteModelKeys = sqliteBuiltInModels
    .map((row) => `${row.provider_code}\u0000${row.model}`)
    .sort()
  assert.equal(expectedSqliteModelKeys.length, 101, '截至 2026-08-10，当前 Node 权威模型目录应包含 101 个可用完整模型键')
  assert.equal(sqliteBuiltInModels.length, expectedSqliteModelKeys.length, 'SQLite fresh seed 必须落库全部权威模型')
  assert.deepEqual(sqliteModelKeys, expectedSqliteModelKeys, 'SQLite fresh seed 最终模型键集合必须与 Node 权威目录一致')
  assert.equal(new Set(sqliteBuiltInModels.map((row) => row.id)).size, expectedSqliteModelKeys.length, 'SQLite 模型 ID 必须全局唯一')
  assert.equal(sqliteBuiltInModels.some((row) => row.model.includes('antigravity')), false, 'SQLite 不得落库非官方 antigravity 模型')

  const builtInVisibilityFixture = databaseModule.getBusinessDatabase().prepare(`
    SELECT status, shutdown_date
    FROM provider_model_catalog
    WHERE provider_code = 'gpt' AND model = 'gpt-5.6-sol'
  `).get() as { status: string; shutdown_date?: string | null }
  assert(builtInVisibilityFixture, 'GPT-5.6 Sol seed fixture must exist')
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE provider_model_catalog SET status = 'disabled'
    WHERE provider_code = 'gpt' AND model = 'gpt-5.6-sol'
  `).run()
  assert.equal(
    providerModelCatalogRepository.listBuiltInProviderModels(['gpt']).some((item) => item.model === 'gpt-5.6-sol'),
    false,
    'Node built-in catalog repository must exclude disabled rows'
  )
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE provider_model_catalog SET status = ?, shutdown_date = '2026-07-15'
    WHERE provider_code = 'gpt' AND model = 'gpt-5.6-sol'
  `).run(builtInVisibilityFixture.status)
  assert.equal(
    providerModelCatalogRepository.listBuiltInProviderModels(['gpt']).some((item) => item.model === 'gpt-5.6-sol'),
    false,
    'Node built-in catalog repository must exclude rows on and after the shutdown date'
  )
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE provider_model_catalog SET status = ?, shutdown_date = ?
    WHERE provider_code = 'gpt' AND model = 'gpt-5.6-sol'
  `).run(builtInVisibilityFixture.status, builtInVisibilityFixture.shutdown_date ?? null)

  const pricedModel = catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-personal',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedApiProtocols: ['responses'],
    releaseDate: '2026-01-02',
    inputUsdPer1M: 2,
    outputUsdPer1M: 8,
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-capabilities',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedApiProtocols: ['responses'],
    supportedServiceTiers: ['priority', 'flex'],
    supportedReasoningEfforts: ['low', 'medium', 'high'],
    defaultReasoningEffort: 'medium',
    inputUsdPer1M: 2,
    outputUsdPer1M: 8,
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-tier-only',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedApiProtocols: ['responses'],
    supportedServiceTiers: ['priority'],
    serviceTierPrices: {
      priority: { inputUsdPer1M: 4, outputUsdPer1M: 16 }
    },
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-global',
    scope: 'global',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 6,
    outputUsdPer1M: 12,
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-upstream-target',
    scope: 'personal',
    systemAccountId: 'sys_admin',
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
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 3,
    outputUsdPer1M: 9,
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-draft',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    status: 'draft',
    supportedApiProtocols: ['responses'],
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-5.5',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedApiProtocols: ['responses'],
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-overridden-pricing-alias',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedApiProtocols: ['responses'],
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-multimodal',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    mode: 'text',
    supportedApiProtocols: ['responses'],
    audioInputUsdPer1M: 4,
    audioOutputUsdPer1M: 12,
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-image-unit',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    mode: 'image',
    supportedApiProtocols: ['images'],
    outputUsdPerImage: 0.04,
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-case-model',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'GPT-regression-case-model',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 5,
    outputUsdPer1M: 7,
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'openai',
    model: 'openai-regression-personal',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedApiProtocols: ['responses'],
    releaseDate: '2026-05-03',
    inputUsdPer1M: 1,
    outputUsdPer1M: 3,
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'hybrid',
    model: 'hybrid-regression-should-not-list',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 3,
    actorSystemAccountId: 'sys_admin'
  })
  const publicCatalog = catalogService.listProviderModelCatalog({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin'
  })
  const gptCatalogIncludingUnpriced = catalogService.listProviderModelCatalog({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    includeUnpriced: true
  })
  assertCatalogReleaseDateDescending(publicCatalog, 'GPT 公开模型目录')
  const publicModels = new Set(publicCatalog.map((item) => item.model))
  assert.equal(gptCatalogIncludingUnpriced.some((item) => item.model === 'codex-auto-review'), false, '缺少完整元数据的 codex-auto-review 不得进入 GPT 内置目录')
  const gptProviderDefaults = databaseModule.getBusinessDatabase().prepare(`
    SELECT default_supported_models_json
    FROM providers
    WHERE code = 'gpt'
  `).get() as { default_supported_models_json?: string }
  const gptDefaultModels = JSON.parse(gptProviderDefaults.default_supported_models_json ?? '[]') as string[]
  assert.equal(gptDefaultModels.includes('codex-auto-review'), false, 'GPT AI 账户默认支持模型不得包含已移除的 codex-auto-review')
  const openAIProviderDefaults = databaseModule.getBusinessDatabase().prepare(`
    SELECT default_supported_models_json
    FROM providers
    WHERE code = 'openai'
  `).get() as { default_supported_models_json?: string }
  const openAIDefaultModels = JSON.parse(openAIProviderDefaults.default_supported_models_json ?? '[]') as string[]
  assert.equal(openAIDefaultModels.includes('codex-auto-review'), false, '通用 OpenAI-compatible 账户默认模型不得包含 GPT 专属模型')
  assert.equal(publicModels.has('codex-auto-review'), false, '无价格模型不应进入默认有价公开目录')
  assert(publicCatalog.some((item) => item.model === 'gpt-regression-global' && item.scope === 'global'), '全局自定义模型应进入当前账号模型目录')
  assert(publicModels.has('gpt-regression-personal'), '当前账号个人自定义模型应进入模型目录')
  const personalCapabilityModel = publicCatalog.find((item) => item.model === 'gpt-regression-capabilities')
  assert.deepEqual(personalCapabilityModel?.supportedServiceTiers, ['priority', 'flex'], '自定义模型服务等级能力必须完成 SQLite 往返')
  assert.deepEqual(personalCapabilityModel?.supportedReasoningEfforts, ['low', 'medium', 'high'], '自定义模型思考能力必须完成 SQLite 往返')
  assert.equal(personalCapabilityModel?.defaultReasoningEffort, null, '自定义模型默认思考级别必须保持为空并由上游决定')
  assert.equal(personalCapabilityModel?.supportsServiceTier, true, '自定义模型 supportsServiceTier 必须由精确能力数组派生')
  assert(publicModels.has('gpt-regression-alias'), '带直接价格的个人模型应进入个人公开模型目录')
  assert(publicModels.has('gpt-regression-upstream-target'), '自定义上游目标模型应直接进入公开模型目录')
  assert(publicModels.has('gpt-regression-tier-only'), '只有精确档位价格的模型也应进入公开模型目录')
  assert(publicModels.has('gpt-regression-case-model'), '仅大小写不同的小写自定义模型应进入模型目录')
  assert(publicModels.has('GPT-regression-case-model'), '仅大小写不同的大写自定义模型应进入模型目录')
  assert(publicModels.has('gpt-regression-multimodal'), '带音频 Token 价格的多模态文本模型应进入公开模型目录')
  assert(publicModels.has('gpt-regression-image-unit'), '只有按张图片价格的自定义模型应进入公开模型目录')
  assert.equal(publicModels.has('gpt-regression-draft'), false, '草稿模型不应进入公开模型目录')
  assert.equal(publicModels.has('gpt-regression-overridden-pricing-alias'), false, '无价自定义模型不应进入公开模型目录')
  assert.equal(publicModels.has('openai-regression-personal'), false, 'GPT 模型目录不应反向包含 OpenAI 兼容自定义模型')

  const gpt55Snapshot = publicCatalog.find((item) => item.model === 'gpt-5.5-2026-04-23' && item.scope === 'built_in')
  assert(gpt55Snapshot, 'GPT-5.5 版本化内置模型必须进入模型目录')
  assert(gpt55Snapshot.inputModalities.includes('image'), '内置模型目录必须保留静态模型资料中的图片输入能力')
  assert(gpt55Snapshot.supportedTools.includes('web_search'), '内置模型目录必须保留静态模型资料中的联网搜索工具能力')

  const gpt56WireReasoning = ['none', 'low', 'medium', 'high', 'xhigh', 'max']
  const gpt56ServiceTiers = ['priority', 'flex']
  const gpt56CodexReasoning = ['low', 'medium', 'high', 'xhigh', 'max']
  const gpt56Sol = publicCatalog.find((item) => item.model === 'gpt-5.6-sol')
  const gpt56Terra = publicCatalog.find((item) => item.model === 'gpt-5.6-terra')
  const gpt56Luna = publicCatalog.find((item) => item.model === 'gpt-5.6-luna')
  assert(gpt56Sol && gpt56Terra && gpt56Luna, 'GPT-5.6 Sol / Terra / Luna 必须进入模型目录')
  for (const item of [gpt56Sol, gpt56Terra, gpt56Luna]) {
    assert.equal(item.contextWindowTokens, 1_050_000, `${item.model} 上下文窗口必须跟随官方 1,050,000 Token 边界`)
    assert.equal(item.maxInputTokens, 922_000, `${item.model} 最大输入必须跟随官方 922,000 Token 边界`)
    assert.equal(item.maxOutputTokens, 128_000, `${item.model} 最大输出必须跟随官方 128,000 Token 边界`)
    assert.deepEqual(item.supportedServiceTiers, gpt56ServiceTiers, `${item.model} 必须精确声明 Priority 与 Flex`)
    assert.deepEqual(item.supportedReasoningEfforts, gpt56WireReasoning, `${item.model} 必须精确声明 wire reasoning effort`)
    assert.equal(item.supportsServiceTier, true, `${item.model} supportsServiceTier 必须从精确数组派生`)
    assert.equal(item.supportedReasoningEfforts.includes('ultra' as never), false, `${item.model} wire effort 不能包含 Ultra`)
  }
  assert.deepEqual(gpt56Sol.codexSupportedReasoningLevels, [...gpt56CodexReasoning, 'ultra'])
  assert.deepEqual(gpt56Terra.codexSupportedReasoningLevels, [...gpt56CodexReasoning, 'ultra'])
  assert.deepEqual(gpt56Luna.codexSupportedReasoningLevels, gpt56CodexReasoning)
  assert.equal(gpt56Sol.codexDefaultReasoningLevel, 'low')
  assert.equal(gpt56Terra.codexDefaultReasoningLevel, 'medium')
  assert.equal(gpt56Luna.codexDefaultReasoningLevel, 'medium')
  assert.equal(gpt56Sol.codexMultiAgentVersion, 'v2')
  assert.equal(gpt56Terra.codexMultiAgentVersion, 'v2')
  assert.equal(gpt56Luna.codexMultiAgentVersion, undefined)

  const ignoredCustomDefaultReasoning = catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-invalid-default-reasoning',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedApiProtocols: ['responses'],
    supportedReasoningEfforts: ['low'],
    defaultReasoningEffort: 'high',
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: 'sys_admin'
  })
  assert.equal(ignoredCustomDefaultReasoning.defaultReasoningEffort, null, '自定义模型应忽略默认思考级别输入')

  assert.throws(() => customProviderModelsRepository.upsertCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-image-invalid-capabilities',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    mode: 'image',
    supportedServiceTiers: ['priority'],
    outputUsdPerImage: 0.02,
    actorSystemAccountId: 'sys_admin'
  }), /只有文本自定义模型支持服务等级和思考能力配置/)
  assert.throws(() => customProviderModelsRepository.upsertCustomProviderModel({
    providerCode: 'gpt', model: 'gpt-regression-image-orphan-tier-price', scope: 'personal', systemAccountId: 'sys_admin',
    mode: 'image', serviceTierPrices: { priority: { inputUsdPer1M: 1 } }, actorSystemAccountId: 'sys_admin'
  }), /只有文本自定义模型支持服务档位价格/)
  assert.throws(() => customProviderModelsRepository.upsertCustomProviderModel({
    providerCode: 'anthropic', model: 'anthropic-regression-orphan-tier-price', scope: 'personal', systemAccountId: 'sys_admin',
    mode: 'text', supportedServiceTiers: ['fast'], serviceTierPrices: { priority: { inputUsdPer1M: 1 } }, actorSystemAccountId: 'sys_admin'
  }), /服务档位价格必须属于模型支持的服务等级/)
  assert.throws(() => customProviderModelsRepository.upsertCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-audio-unsupported',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    mode: 'audio',
    supportedReasoningEfforts: ['high'],
    defaultReasoningEffort: 'high',
    audioInputUsdPer1M: 1,
    audioOutputUsdPer1M: 2,
    actorSystemAccountId: 'sys_admin'
  }), /当前只支持文本和图像自定义模型/)
  const openAIProviderCapabilities = customProviderModelsRepository.upsertCustomProviderModel({
    providerCode: 'openai',
    model: 'openai-regression-invalid-capabilities',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedReasoningEfforts: ['high'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: 'sys_admin'
  })
  assert.deepEqual(openAIProviderCapabilities.supportedReasoningEfforts, ['high'], '非 GPT 文本模型应保存供应商原生思考能力字符串')

  const repositoryClearableModel = customProviderModelsRepository.upsertCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-repository-clear-capabilities',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    mode: 'text',
    supportedServiceTiers: ['priority'],
    supportedReasoningEfforts: ['low', 'high'],
    defaultReasoningEffort: 'high',
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: 'sys_admin'
  })
  const repositoryClearedModel = customProviderModelsRepository.upsertCustomProviderModel({
    id: repositoryClearableModel.id,
    providerCode: 'gpt',
    model: repositoryClearableModel.model,
    scope: repositoryClearableModel.scope,
    systemAccountId: repositoryClearableModel.systemAccountId,
    mode: 'image',
    supportedServiceTiers: [],
    supportedReasoningEfforts: [],
    defaultReasoningEffort: null,
    outputUsdPerImage: 0.02,
    actorSystemAccountId: 'sys_admin'
  })
  assert.equal(repositoryClearedModel.mode, 'image', 'repository 显式清空文本能力后应允许切换为 GPT 图片模型')
  assert.deepEqual(repositoryClearedModel.supportedServiceTiers, [], 'repository 应接受空数组清理服务等级能力')
  assert.deepEqual(repositoryClearedModel.supportedReasoningEfforts, [], 'repository 应接受空数组清理思考能力')
  assert.equal(repositoryClearedModel.defaultReasoningEffort, undefined, 'repository 应接受 null 清理默认思考级别')
  const repositoryClearedCatalogModel = catalogService.listProviderModelCatalog({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    includeInactive: true,
    includeUnpriced: true
  }).find((item) => item.id === repositoryClearedModel.id)
  assert.equal(repositoryClearedCatalogModel?.defaultReasoningEffort, null, '模型目录响应必须把已清空默认思考级别显式返回为 null')

  const gpt56Alias = modelPricingService.getProviderModelPricing('gpt', 'gpt-5.6')
  assert.equal(gpt56Alias?.model, 'gpt-5.6-sol', 'gpt-5.6 稳定别名必须继承 Sol 能力')
  assert.deepEqual(gpt56Alias?.supportedReasoningEfforts, gpt56WireReasoning)
  assert.deepEqual(gpt56Alias?.codexSupportedReasoningLevels, [...gpt56CodexReasoning, 'ultra'])

  const otherUserCatalog = catalogService.listProviderModelCatalog({
    providerCode: 'gpt',
    systemAccountId: 'sys_other'
  })
  assert(otherUserCatalog.some((item) => item.model === 'gpt-regression-global' && item.scope === 'global'), '全局模型应对其他用户可见')
  assert.equal(otherUserCatalog.some((item) => item.model === 'gpt-regression-personal'), false, '其他用户不应看到当前账号个人模型')

  const openAICompatibleCatalog = catalogService.listProviderModelCatalog({
    providerCode: 'openai',
    systemAccountId: 'sys_admin'
  })
  assertCatalogReleaseDateDescending(openAICompatibleCatalog, 'OpenAI 兼容聚合模型目录')
  assert(openAICompatibleCatalog.some((item) => item.model === 'gpt-regression-personal' && item.providerCode === 'gpt'), 'OpenAI 兼容模型目录应聚合 GPT 的 OpenAI v1 模型')
  assert(openAICompatibleCatalog.some((item) => item.model === 'openai-regression-personal' && item.providerCode === 'openai'), 'OpenAI 兼容模型目录应保留自身模型')
  assert(openAICompatibleCatalog.some((item) => item.providerCode === 'deepseek'), 'OpenAI 兼容模型目录应聚合 DeepSeek OpenAI 协议模型')
  assert(openAICompatibleCatalog.some((item) => item.providerCode === 'glm'), 'OpenAI 兼容模型目录应聚合 GLM OpenAI 协议模型')
  assert(openAICompatibleCatalog.some((item) => item.model === 'openai-regression-personal'), '通用 OpenAI-compatible 自身模型不要求排在其他 OpenAI 协议供应商模型之前')

  const gptModelOptions = await listProviderModelSelectionOptionsAsync({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    limit: 50,
    selectedIds: []
  })
  const gptReleaseDates = new Map(
    catalogService.listProviderModelCatalog({ providerCode: 'gpt', systemAccountId: 'sys_admin' })
      .map((item) => [item.model, item.releaseDate] as const)
  )
  assertCatalogReleaseDateDescending(
    gptModelOptions.map((item) => ({ model: item.id, releaseDate: gptReleaseDates.get(item.id) })),
    'AI 账户和人工测试共用的轻量模型选项'
  )

  const dedupedProviderModelOptions = mergeProviderModelOptionRows([
    { id: 'gpt-built-in', providerCode: 'gpt', model: 'shared-model', scope: 'built_in' },
    { id: 'gpt-global', providerCode: 'gpt', model: 'shared-model', scope: 'global' },
    { id: 'gpt-case', providerCode: 'gpt', model: 'Shared-Model', scope: 'built_in' },
    { id: 'deepseek-built-in', providerCode: 'deepseek', model: 'shared-model', scope: 'built_in' },
    { id: 'blank-provider', providerCode: '', model: 'shared-model', scope: 'built_in' },
    { id: 'blank-model', providerCode: 'glm', model: ' ', scope: 'built_in' }
  ], { limit: 50, selectedIds: [] })
  assert.deepEqual(dedupedProviderModelOptions, [
    {
      id: 'shared-model',
      name: 'shared-model',
      supportedApiProtocols: [],
      supportedServiceTiers: [],
      supportedReasoningEfforts: []
    },
    {
      id: 'Shared-Model',
      name: 'Shared-Model',
      supportedApiProtocols: [],
      supportedServiceTiers: [],
      supportedReasoningEfforts: []
    }
  ], '跨供应商模型选项必须稳定地按大小写敏感 model 去重，并携带能力数组')

  const deepSeekCatalog = catalogService.listProviderModelCatalog({
    providerCode: 'deepseek',
    systemAccountId: 'sys_admin'
  })
  const deepSeekModels = new Set(deepSeekCatalog.map((item) => item.model))
  assert(deepSeekModels.has('deepseek-v4-flash'), 'DeepSeek 模型目录应包含官方 V4 Flash')
  assert(deepSeekModels.has('deepseek-v4-pro'), 'DeepSeek 模型目录应包含官方 V4 Pro')
  assert.deepEqual(
    deepSeekCatalog.find((item) => item.model === 'deepseek-v4-flash')?.supportedApiProtocols,
    ['chat_completions', 'responses', 'messages'],
    'DeepSeek V4 Flash 目录应声明官方原生 Responses、Chat 与 Anthropic Messages 协议'
  )
  assert.deepEqual(
    deepSeekCatalog.find((item) => item.model === 'deepseek-v4-pro')?.supportedApiProtocols,
    ['chat_completions', 'responses', 'messages', 'completions'],
    'DeepSeek V4 Pro 应按产品预兼容策略声明原生 Responses、Chat、Anthropic Messages 与 Completions 协议'
  )
  assert.equal(deepSeekModels.has('deepseek-ai-v4-flash'), false, 'DeepSeek 模型目录不得暴露官方列表不存在的 deepseek-ai V4 Flash 别名')
  assert.equal(deepSeekModels.has('deepseek-ai-v4-pro'), false, 'DeepSeek 模型目录不得暴露官方列表不存在的 deepseek-ai V4 Pro 别名')
  if (new Date().toISOString().slice(0, 10) < '2026-07-24') {
    assert(deepSeekModels.has('deepseek-chat'), 'DeepSeek 模型目录在 deepseek-chat 退役前应包含官方历史兼容名')
    assert(deepSeekModels.has('deepseek-reasoner'), 'DeepSeek 模型目录在 deepseek-reasoner 退役前应包含官方历史兼容名')
  }
  assert.deepEqual(
    deepSeekCatalog.map((item) => item.model),
    [
      'deepseek-v4-flash',
      'deepseek-v4-pro',
      ...(new Date().toISOString().slice(0, 10) < '2026-07-24' ? ['deepseek-chat', 'deepseek-reasoner'] : [])
    ],
    'DeepSeek 模型目录应按当前官方优先模型到历史兼容名排序'
  )

  for (const providerCode of ['gpt', 'deepseek']) {
    catalogService.saveCustomProviderModel({
      providerCode,
      model: 'shared-hybrid-template-name',
      scope: 'personal',
      systemAccountId: 'sys_admin',
      supportedApiProtocols: ['chat_completions'],
      inputUsdPer1M: providerCode === 'gpt' ? 1 : 2,
      outputUsdPer1M: providerCode === 'gpt' ? 3 : 4,
      actorSystemAccountId: 'sys_admin'
    })
  }
  const hybridSameNameCatalog = catalogService.listProviderModelCatalog({
    providerCode: 'hybrid',
    systemAccountId: 'sys_admin'
  }).filter((item) => item.model === 'shared-hybrid-template-name')
  assert.deepEqual(
    new Set(hybridSameNameCatalog.map((item) => item.providerCode)),
    new Set(['gpt', 'deepseek']),
    'Hybrid 配置模板目录必须保留不同供应商的同名模型'
  )

  const glmCatalog = catalogService.listProviderModelCatalog({
    providerCode: 'glm',
    systemAccountId: 'sys_admin',
    includeUnpriced: true
  })
  const glmModels = new Set(glmCatalog.map((item) => item.model))
  for (const id of [
    'glm-5.2',
    'glm-5.1',
    'glm-5',
    'glm-5-turbo',
    'glm-4.7',
    'glm-4.7-flashx',
    'glm-4.7-flash',
    'glm-4.6',
    'glm-4.5',
    'glm-4.5-x',
    'glm-4.5-air',
    'glm-4.5-airx',
    'glm-4.5-flash'
  ]) {
    assert(glmModels.has(id), `GLM 模型目录应包含官方文本模型 ${id}`)
  }
  assert.equal(glmModels.has('glm-5.2-free'), false, 'GLM 可见模型目录不应包含非官方 glm-5.2-free')
  for (const removedModel of ['glm-4-32b-0414-128k', 'glm-4-flashx-250414', 'glm-4-flash-250414']) {
    assert.equal(glmModels.has(removedModel), false, `GLM 4.5 之前的模型 ${removedModel} 应从权威目录删除`)
  }
  assert.equal(glmCatalog.some((item) => item.catalogDisplay?.some((section) => section.key === 'batch' || section.key === 'currency_conversion')), false, 'GLM 目录不应生成批量处理或美元换算列')
  assert.deepEqual(
    glmCatalog.map((item) => item.model),
    [
      'glm-5.2',
      'glm-5.1',
      'glm-5-turbo',
      'glm-5',
      'glm-4.7',
      'glm-4.7-flashx',
      'glm-4.7-flash',
      'glm-4.6',
      'glm-4.5',
      'glm-4.5-x',
      'glm-4.5-air',
      'glm-4.5-airx',
      'glm-4.5-flash'
    ],
    'GLM 模型目录应按官方当前模型从新到旧排序'
  )

  const geminiCatalog = catalogService.listProviderModelCatalog({
    providerCode: 'gemini',
    systemAccountId: 'sys_admin'
  })
  const geminiModels = new Set(geminiCatalog.map((item) => item.model))
  for (const id of [
    'gemini-3.5-flash',
    'gemini-3.1-pro-preview',
    'gemini-3.1-pro-preview-customtools',
    'gemini-3-flash-preview',
    'gemini-3.1-flash-lite',
    'gemini-2.5-pro',
    'gemini-2.5-flash',
    'gemini-2.5-flash-lite',
    'gemini-embedding-2'
  ]) {
    assert(geminiModels.has(id), `Gemini 模型目录应包含 Google 官方模型 ${id}`)
  }
  assert.equal(geminiModels.has('gemini-embedding-001'), false, '已于 2026-07-14 关闭的 Gemini Embedding 001 不得进入当前可用目录')
  assert(geminiCatalog.find((item) => item.model === 'gemini-3.5-flash')?.supportedApiProtocols.includes('interactions'), 'Gemini 3.5 Flash 应声明官方 Interactions 协议')
  assert(geminiCatalog.find((item) => item.model === 'gemini-2.5-pro')?.supportedApiProtocols.includes('interactions'), 'Gemini 2.5 Pro 应声明官方 Interactions 协议')
  assert.equal(geminiCatalog.find((item) => item.model === 'gemini-3.1-pro-preview-customtools')?.supportedApiProtocols.includes('interactions'), false, '未在官方 Interactions 模型表列出的 customtools 别名不得推断为支持')
  for (const id of [
    'gemini-3.5-flash-antigravity',
    'gemini-3.5-flash-antigravity-ultra'
  ]) {
    assert.equal(geminiModels.has(id), false, `${id} 是中转自定义型号，不应进入 Gemini 官方内置目录`)
  }
  assert.deepEqual(
    geminiCatalog.map((item) => item.model),
    [
      'gemini-3.6-flash',
      'gemini-3.5-flash-lite',
      'gemini-3.5-flash',
      'gemini-3.1-flash-lite',
      'gemini-embedding-2',
      'gemini-3.1-pro-preview',
      'gemini-3.1-pro-preview-customtools',
      'gemini-3-flash-preview',
      'gemini-2.5-flash-lite',
      'gemini-2.5-pro',
      'gemini-2.5-flash'
    ],
    'Gemini 模型目录应只包含当前收录的 Google 官方模型，并按官网当前主序排序'
  )

  const managementCatalog = catalogService.listProviderModelCatalog({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    includeInactive: true,
    includeUnpriced: true
  })
  assert(managementCatalog.some((item) => item.model === 'gpt-regression-draft'), '管理模型目录应能看到草稿模型')

  const anthropicCatalog = catalogService.listProviderModelCatalog({
    providerCode: 'anthropic',
    systemAccountId: 'sys_admin'
  })
  const anthropicModels = new Set(anthropicCatalog.map((item) => item.model))
  for (const id of [
    'best',
    'fable',
    'opus',
    'opus[1m]',
    'opusplan',
    'sonnet',
    'sonnet[1m]',
    'haiku'
  ]) {
    assert.equal(anthropicModels.has(id), false, `Claude Code 本地别名 ${id} 不得作为 Anthropic 公开模型目录项`)
  }
  for (const id of [
    'claude-opus-5',
    'claude-fable-5',
    'claude-sonnet-5',
    'claude-opus-4-8',
    'claude-opus-4-7',
    'claude-opus-4-6',
    'claude-sonnet-4-6',
    'claude-haiku-4-5',
    'claude-sonnet-4-5',
    'claude-opus-4-5'
  ]) {
    assert(anthropicModels.has(id), `Anthropic 模型目录应包含当前官网模型 ${id}`)
  }
  assert.equal(anthropicModels.has('claude-mythos-5'), false, 'Claude Mythos 5 仅限获批客户，不得进入公开目录')
  assert(anthropicModels.has('claude-haiku-4-5-20251001'), 'Anthropic 模型目录应包含 Haiku 4.5 官方 dated ID')
  assert(anthropicModels.has('claude-sonnet-4-5-20250929'), 'Anthropic 模型目录应包含 Sonnet 4.5 官方 dated ID')
  assert(anthropicModels.has('claude-opus-4-5-20251101'), 'Anthropic 模型目录应包含 Opus 4.5 官方 dated ID')
  assert.equal(anthropicModels.has('claude-mythos-preview'), false, 'Mythos preview 已退休，不应进入 Anthropic 模型目录')
  assert.equal(anthropicModels.has('claude-opus-4-1'), false, 'Opus 4.1 已于 2026-08-05 shutdown，不应进入当前目录')
  assert.equal(anthropicModels.has('claude-opus-4-1-20250805'), false, 'Opus 4.1 dated ID 已于 2026-08-05 shutdown，不应进入当前目录')
  for (const id of [
    'default',
    'claude-opus-4-20250514',
    'claude-sonnet-4-20250514',
    'claude-3-7-sonnet-20250219',
    'claude-3-5-haiku-20241022',
    'google/antigravity-claude-opus-4-5-thinking',
    'claude-fake-5',
    'claude-opus-4-6-antigravity',
    'antigravity-claude-opus-4-6-thinking',
    'antigravity/claude-opus-4-6-thinking',
    'google/antigravity-claude-opus-4-6-thinking',
    'google-antigravity/claude-opus-4-6-thinking',
    'google-antigravity:claude-opus-4-6-thinking',
    'claude-sonnet-4-6-antigravity',
    'antigravity-claude-sonnet-4-6',
    'antigravity/claude-sonnet-4-6',
    'google/antigravity-claude-sonnet-4-6',
    'google-antigravity/claude-sonnet-4-6',
    'google-antigravity:claude-sonnet-4-6',
    'antigravity-claude-sonnet-4-6-thinking',
    'antigravity/claude-sonnet-4-6-thinking',
    'google/antigravity-claude-sonnet-4-6-thinking',
    'google-antigravity/claude-sonnet-4-6-thinking',
    'google-antigravity:claude-sonnet-4-6-thinking'
  ]) {
    assert.equal(anthropicModels.has(id), false, `${id} 不应进入 Anthropic 模型目录`)
  }
  assert.deepEqual(
    anthropicCatalog.map((item) => item.model),
    [
      'claude-opus-5',
      'claude-sonnet-5',
      'claude-fable-5',
      'claude-opus-4-8',
      'claude-opus-4-7',
      'claude-sonnet-4-6',
      'claude-opus-4-6',
      'claude-opus-4-5',
      'claude-opus-4-5-20251101',
      'claude-haiku-4-5',
      'claude-haiku-4-5-20251001',
      'claude-sonnet-4-5',
      'claude-sonnet-4-5-20250929'
    ],
    'Anthropic 模型目录应按官方当前模型从新到旧排序，并隐藏 Claude Code 本地别名'
  )

  const publicClientCatalog = await clientCatalogService.listClientModelCatalogAsync()
  const publicClientProviderCodes = new Set(publicClientCatalog.map((item) => item.providerCode))
  for (const providerCode of ['gpt', 'deepseek', 'glm', 'anthropic', 'gemini', 'xai']) {
    assert(publicClientProviderCodes.has(providerCode), `公开客户端目录必须包含启用供应商 ${providerCode}`)
  }
  assert.equal(publicClientProviderCodes.has(HYBRID_PROVIDER_CODE), false, '公开客户端目录不得返回 hybrid 虚拟供应商模型')
  assert.equal(
    publicClientCatalog.filter((item) => item.scope === 'built_in').every((item) => Boolean(item.releaseDate)),
    true,
    '全量客户端目录中的内置可见模型必须全部带发布时间'
  )

  const scopedClientCatalog = await clientCatalogService.listClientModelCatalogAsync({
    systemAccountId: 'sys_model_regression',
    providerCodes: ['gpt', 'gemini']
  })
  const scopedClientProviderCodes = new Set(scopedClientCatalog.map((item) => item.providerCode))
  assert(scopedClientProviderCodes.has('gpt'), 'API Key 多供应商作用域必须包含 GPT 目录')
  assert(scopedClientProviderCodes.has('gemini'), 'API Key 多供应商作用域必须包含 Gemini 目录')
  assert.deepEqual(
    [...scopedClientProviderCodes].sort(),
    ['gemini', 'gpt'],
    'API Key 多供应商作用域不得泄漏未绑定供应商模型'
  )
  assert.deepEqual(
    await clientCatalogService.listClientModelCatalogAsync({ providerCodes: [] }),
    [],
    'API Key 没有有效供应商绑定时不得回退公开全量目录'
  )

  const response = catalogService.buildOpenAIModelsResponseFromCatalog(publicCatalog)
  assert.equal(response.object, 'list', '/v1/models 顶层 object 必须是 list')
  assert(Array.isArray(response.data), '/v1/models data 必须是数组')
  for (const item of response.data) {
    assert.deepEqual(Object.keys(item).sort(), ['created', 'id', 'object', 'owned_by'], '/v1/models 所有模型项都只能暴露 OpenAI 标准字段')
    assert.equal(item.object, 'model', '/v1/models 所有模型项 object 必须是 model')
    assert(Number.isInteger(item.created), '/v1/models 所有模型项 created 必须是 Unix 秒整数')
    assert.equal(typeof item.owned_by, 'string', '/v1/models 所有模型项 owned_by 必须是字符串')
  }
  const personalModel = response.data.find((item) => item.id === 'gpt-regression-personal')
  assert(personalModel, '/v1/models 应包含当前账号个人自定义模型')
  assert.deepEqual(Object.keys(personalModel).sort(), ['created', 'id', 'object', 'owned_by'], '/v1/models 模型项只能暴露 OpenAI 标准字段')
  assert.equal(personalModel.object, 'model', '/v1/models 模型项 object 必须是 model')
  assert.equal(personalModel.created, Date.parse('2026-01-02T00:00:00.000Z') / 1000, '/v1/models created 应为 Unix 秒')
  assert(response.data.some((item) => item.id === 'gpt-regression-upstream-target'), '/v1/models 应包含启用且可计价的自定义上游目标模型')

  const codexResponse = catalogService.buildCodexModelsResponseFromCatalog(publicCatalog)
  assert(Array.isArray(codexResponse.models), 'Codex /models 顶层 models 必须是数组')
  assert.equal(Object.prototype.hasOwnProperty.call(codexResponse, 'data'), false, 'Codex /models 不应返回 OpenAI data 字段')
  const codexPersonalModel = codexResponse.models.find((item) => item.slug === 'gpt-regression-personal')
  assert(codexPersonalModel, 'Codex /models 应包含当前账号个人自定义模型')
  assert.equal(codexPersonalModel.display_name, 'gpt-regression-personal', 'Codex /models display_name 默认使用模型名')
  assert.equal(codexPersonalModel.shell_type, 'shell_command', 'Codex /models shell_type 必须匹配 Codex ModelInfo')
  assert.equal(codexPersonalModel.visibility, 'list', 'Codex /models visibility 必须可进入列表')
  assert.equal(codexPersonalModel.supported_in_api, true, 'Codex /models 模型必须标记 API 可用')
  assert.equal(Object.prototype.hasOwnProperty.call(codexPersonalModel, 'default_reasoning_level'), false, '能力未知的自定义模型不能伪造默认 reasoning')
  assert.equal(Object.prototype.hasOwnProperty.call(codexPersonalModel, 'supported_reasoning_levels'), false, '能力未知的自定义模型不能伪造统一 reasoning 选项')
  assert.deepEqual(codexPersonalModel.service_tiers, [], '能力未知的自定义模型不能伪造服务等级')
  assert.equal(typeof codexPersonalModel.base_instructions, 'string', 'Codex /models 必须提供 base_instructions')
  assert.equal(codexPersonalModel.truncation_policy.mode, 'bytes', 'Codex /models 必须提供 truncation_policy')
  assert(codexResponse.models.some((item) => item.slug === 'gpt-regression-upstream-target'), 'Codex /models 应包含启用且可计价的自定义上游目标模型')
  const codexSol = codexResponse.models.find((item) => item.slug === 'gpt-5.6-sol')
  const codexTerra = codexResponse.models.find((item) => item.slug === 'gpt-5.6-terra')
  const codexLuna = codexResponse.models.find((item) => item.slug === 'gpt-5.6-luna')
  assert(codexSol && codexTerra && codexLuna, 'Codex /models 必须包含 GPT-5.6 三个模型')
  assert.deepEqual((codexSol.supported_reasoning_levels ?? []).map((item) => item.effort), [...gpt56CodexReasoning, 'ultra'])
  assert.deepEqual((codexTerra.supported_reasoning_levels ?? []).map((item) => item.effort), [...gpt56CodexReasoning, 'ultra'])
  assert.deepEqual((codexLuna.supported_reasoning_levels ?? []).map((item) => item.effort), gpt56CodexReasoning)
  assert.deepEqual(codexSol.additional_speed_tiers, ['fast'])
  assert.deepEqual(codexTerra.additional_speed_tiers, ['fast'])
  assert.deepEqual(codexLuna.additional_speed_tiers, ['fast'])
  assert.deepEqual(codexSol.service_tiers.map((item) => item.id), ['priority', 'flex'])
  assert.equal(codexSol.multi_agent_version, 'v2')
  assert.equal(codexTerra.multi_agent_version, 'v2')
  assert.equal(codexLuna.multi_agent_version, null)

  catalogService.saveCustomProviderModel({
    providerCode: 'gemini',
    model: 'gemini-regression-missing-tier-prices',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedApiProtocols: ['generate_content'],
    supportedServiceTiers: ['priority', 'flex'],
    inputUsdPer1M: 2,
    outputUsdPer1M: 8,
    actorSystemAccountId: 'sys_admin'
  })

  const aliasCost = catalogService.estimateCatalogCostUsd({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-alias',
    inputTokens: 1_000_000,
    outputTokens: 1_000_000
  })
  assert.equal(aliasCost, 12, '模型应按自身目录行直接价格计费')
  const aliasCostAsync = await catalogService.estimateCatalogCostUsdAsync({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-alias',
    inputTokens: 1_000_000,
    outputTokens: 1_000_000
  })
  assert.equal(aliasCostAsync, aliasCost, '异步目录成本估算应与同步目录一致')
  const aliasPricingModelAsync = await catalogService.resolveCatalogPricingModelAsync({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-alias'
  })
  assert.equal(aliasPricingModelAsync, 'gpt-regression-alias', '异步计价模型应保持最终上游模型自身')
  const tierOnlyCost = catalogService.estimateCatalogCostUsd({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-tier-only',
    serviceTier: 'priority',
    inputTokens: 1_000_000,
    outputTokens: 1_000_000
  })
  assert.equal(tierOnlyCost, 20, '只有精确档位价格的模型必须按 serviceTierPrices 计价')
  for (const serviceTier of ['priority', 'flex'] as const) {
    const missingGeminiTierCost = catalogService.estimateCatalogCostUsd({
      providerCode: 'gemini',
      systemAccountId: 'sys_admin',
      model: 'gemini-regression-missing-tier-prices',
      serviceTier,
      inputTokens: 1_000_000,
      outputTokens: 1_000_000
    })
    assert.equal(missingGeminiTierCost, undefined, `Gemini SQLite 目录缺少 ${serviceTier} 精确价格时不得回退 Standard`)
    const geminiAudioCapableMissingTierCost = catalogService.estimateCatalogCostUsd({
      providerCode: 'gemini',
      systemAccountId: 'sys_admin',
      model: 'gemini-2.5-flash',
      serviceTier,
      inputTokens: 1_000,
      outputTokens: 100
    })
    assert.equal(geminiAudioCapableMissingTierCost, serviceTier === 'priority' ? 0.00099 : 0.000275, `Gemini 2.5 Flash SQLite 目录必须按 ${serviceTier} token 价格计费，不能把普通 token 误算为音频`)
  }
  const lowerCaseModelCost = catalogService.estimateCatalogCostUsd({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-case-model',
    inputTokens: 1_000_000,
    outputTokens: 1_000_000
  })
  const upperCaseModelCost = catalogService.estimateCatalogCostUsd({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'GPT-regression-case-model',
    inputTokens: 1_000_000,
    outputTokens: 1_000_000
  })
  assert.equal(lowerCaseModelCost, 3, '大小写不同的自定义模型应按小写模型自己的价格计费')
  assert.equal(upperCaseModelCost, 12, '大小写不同的自定义模型应按大写模型自己的价格计费')
  const overriddenAliasCost = catalogService.estimateCatalogCostUsd({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-overridden-pricing-alias',
    inputTokens: 1_000_000,
    outputTokens: 1_000_000
  })
  assert.equal(overriddenAliasCost, undefined, '无价模型不应按其他目录行回落计价')
  const audioCost = catalogService.estimateCatalogCostUsd({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-multimodal',
    inputAudioTokens: 1_000_000,
    outputAudioTokens: 2_000_000
  })
  assert.equal(audioCost, 28, '只有音频价格的自定义模型必须使用协议解析出的音频 token 成本计费')
  const audioBreakdown = catalogService.buildCatalogCostBreakdown({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-multimodal',
    inputAudioTokens: 1_000_000,
    outputAudioTokens: 1_000_000
  })
  assert.equal(audioBreakdown?.inputAudioCostUsd, 4, '音频输入成本应进入成本拆解')
  assert.equal(audioBreakdown?.outputAudioCostUsd, 12, '音频输出成本应进入成本拆解')
  assert.equal(audioBreakdown?.inputAudioUsdPer1M, 4, '音频输入单价应进入成本拆解')
  assert.equal(audioBreakdown?.outputAudioUsdPer1M, 12, '音频输出单价应进入成本拆解')
  const audioBreakdownAsync = await catalogService.buildCatalogCostBreakdownAsync({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-multimodal',
    inputAudioTokens: 1_000_000,
    outputAudioTokens: 1_000_000
  })
  assert.deepEqual(audioBreakdownAsync, audioBreakdown, '异步成本拆解应与同步目录一致')
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
  const scopedInflightEstimate = await inflightQuotaService.estimateGatewayRequestCostUsd({
    originalUrl: '/v1/responses',
    path: '/v1/responses',
    body: { model: 'gpt-regression-personal' },
    gatewayRequestBody: {
      rawBodyBytes: 4,
      model: 'gpt-regression-personal',
      maxOutputTokens: 1
    }
  } as never, 'gpt', 'sys_admin')
  assert.notEqual(scopedInflightEstimate, undefined, 'API Key 在途额度估算应命中所属账户的个人模型价格')

  catalogService.saveCustomProviderModel({
    id: pricedModel.id,
    providerCode: 'gpt',
    model: 'gpt-regression-personal',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: null,
    outputUsdPer1M: null,
    actorSystemAccountId: 'sys_admin'
  })
  const remappedCost = catalogService.estimateCatalogCostUsd({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-personal',
    inputTokens: 1_000_000,
    outputTokens: 1_000_000
  })
  assert.equal(remappedCost, undefined, '清空直接价格后不得回落其他模型价格')

  await assertProviderModelHttpContracts()

  const partialPatchTarget = providerModelCatalogRepository.listBuiltInProviderModels(['gpt'])
    .find((item) => item.inputUsdPer1M !== undefined && item.outputUsdPer1M !== undefined)
  assert(partialPatchTarget, '缺少可验证部分价格 PATCH 的内置模型')
  const partialPatchSaved = await providerModelCatalogRepository.updateBuiltInProviderModelPricesAsync(partialPatchTarget.id, {
    inputUsdPer1M: (partialPatchTarget.inputUsdPer1M ?? 0) + 0.125
  })
  assert(partialPatchSaved, '内置模型部分价格 PATCH 后应返回模型')
  assert.equal(partialPatchSaved.source, 'manual-override', '内置模型人工配置必须留下可识别的覆盖来源')
  assert.equal(partialPatchSaved.outputUsdPer1M, partialPatchTarget.outputUsdPer1M, '部分价格 PATCH 不得清空未提交的输出价格')
  assert.deepEqual(partialPatchSaved.serviceTierPrices, partialPatchTarget.serviceTierPrices, '部分价格 PATCH 不得清空未提交的档位价格')
  seedDefaults(databaseModule.getBusinessDatabase())
  const partialPatchAfterSeed = await providerModelCatalogRepository.findBuiltInProviderModelByIdAsync(partialPatchTarget.id)
  assert.equal(partialPatchAfterSeed?.inputUsdPer1M, partialPatchSaved.inputUsdPer1M, '默认 seed 不得覆盖明确标记的管理员价格')

  const deleteCacheTarget = await customProviderModelsRepository.upsertCustomProviderModelAsync({
    providerCode: 'gpt', model: 'gpt-delete-cache-sync', scope: 'personal', systemAccountId: 'sys_admin',
    status: 'draft', actorSystemAccountId: 'sys_admin'
  })
  let deleteCacheSyncCompleted = false
  const unregisterDelayedInvalidator = gatewayCacheInvalidation.registerGatewayRuntimeCacheInvalidator(async (reason) => {
    if (reason !== 'custom_provider_model_deleted') return
    await new Promise((resolve) => setTimeout(resolve, 0))
    deleteCacheSyncCompleted = true
  })
  const deleteCacheTargetDeleted = await customProviderModelsRepository.deleteCustomProviderModelAsync(deleteCacheTarget.id)
  unregisterDelayedInvalidator()
  assert.equal(deleteCacheTargetDeleted, true, '自定义模型删除主写应成功')
  assert.equal(deleteCacheSyncCompleted, true, '异步删除必须等待提交后缓存失效流程完成')

  const fullConfigurationSaved = await providerModelCatalogRepository.updateBuiltInProviderModelConfigurationAsync(partialPatchTarget.id, {
    status: 'disabled',
    mode: 'text',
    supportedApiProtocols: ['responses', 'chat_completions'],
    supportedServiceTiers: ['priority'],
    supportedReasoningEfforts: ['low', 'high'],
    defaultReasoningEffort: 'high',
    releaseDate: '2026-07-16',
    shutdownDate: null,
    contextWindowTokens: 1_050_000,
    maxInputTokens: 922_000,
    maxOutputTokens: 128_000
  })
  assert.equal(fullConfigurationSaved?.status, 'disabled', '内置模型应能编辑状态')
  assert.deepEqual(fullConfigurationSaved?.supportedServiceTiers, ['priority'], '内置模型应能编辑服务等级')
  assert.deepEqual(fullConfigurationSaved?.supportedReasoningEfforts, ['low', 'high'], '内置模型应能编辑思考级别')
  assert.equal(fullConfigurationSaved?.cachedInputUsdPer1M, partialPatchSaved.cachedInputUsdPer1M, '完整配置 PATCH 不得清空未提交的缓存读价格')
  await providerModelCatalogRepository.updateBuiltInProviderModelConfigurationAsync(partialPatchTarget.id, {
    status: partialPatchTarget.status,
    mode: partialPatchTarget.mode === 'image' || partialPatchTarget.mode === 'audio' ? partialPatchTarget.mode : 'text',
    supportedApiProtocols: partialPatchTarget.supportedApiProtocols ?? [],
    supportedServiceTiers: partialPatchTarget.supportedServiceTiers ?? [],
    supportedReasoningEfforts: partialPatchTarget.supportedReasoningEfforts ?? [],
    defaultReasoningEffort: partialPatchTarget.defaultReasoningEffort ?? null,
    releaseDate: partialPatchTarget.releaseDate ?? null,
    shutdownDate: partialPatchTarget.shutdownDate ?? null,
    contextWindowTokens: partialPatchTarget.contextWindowTokens ?? null,
    maxInputTokens: partialPatchTarget.maxInputTokens ?? null,
    maxOutputTokens: partialPatchTarget.maxOutputTokens ?? null
  })

  const unregisterFailingInvalidator = gatewayCacheInvalidation.registerGatewayRuntimeCacheInvalidator(async () => {
    throw new Error('model catalog invalidation regression sentinel')
  })
  const cacheWarningBuiltIn = await providerModelCatalogRepository.updateBuiltInProviderModelPricesAsync(partialPatchTarget.id, {
	  inputUsdPer1M: (partialPatchSaved.inputUsdPer1M ?? 0) + 0.125
	})
	assert(cacheWarningBuiltIn, '缓存同步失败不得误报内置价格主写失败')
	const committedBuiltIn = await providerModelCatalogRepository.findBuiltInProviderModelByIdAsync(partialPatchTarget.id)
	assert.equal(committedBuiltIn?.inputUsdPer1M, (partialPatchSaved.inputUsdPer1M ?? 0) + 0.125, '缓存同步失败不得回滚已提交内置价格')
	const cacheWarningCustom = await customProviderModelsRepository.upsertCustomProviderModelAsync({
		providerCode: 'gpt', model: 'gpt-cache-sync-committed', scope: 'personal', systemAccountId: 'sys_admin',
		status: 'draft', supportedReasoningEfforts: ['high'], defaultReasoningEffort: 'high', actorSystemAccountId: 'sys_admin'
	})
	assert.equal(cacheWarningCustom.model, 'gpt-cache-sync-committed', '缓存同步失败不得误报自定义模型主写失败')
	assert((await customProviderModelsRepository.listCustomProviderModelsForCatalogAsync({
	  providerCode: 'gpt', systemAccountId: 'sys_admin', includeInactive: true
	})).some((item) => item.model === 'gpt-cache-sync-committed'), '缓存同步失败不得回滚已提交自定义模型')
  unregisterFailingInvalidator()

  console.log('model catalog regression passed')
} finally {
  runtimeConfig.processRole = 'worker'
  await sqliteReadWorkerPool.closeSqliteReadWorkerPool()
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

    const invalidProtocolResponse = await fetch(`${baseUrl}/__aisys__/api/providers/models/options?protocol=invalid`, { headers: { cookie: userACookie } })
    assert.equal(invalidProtocolResponse.status, 400, '非法 protocol 必须明确返回 400')
    const missingProviderResponse = await fetch(`${baseUrl}/__aisys__/api/providers/models/options?providerCode=missing-provider`, { headers: { cookie: userACookie } })
    assert.equal(missingProviderResponse.status, 404, '不存在或停用 providerCode 必须明确返回 404')
    const lightweightProviders = await getEnvelope<Array<Record<string, unknown>>>(baseUrl, '/__aisys__/api/providers/options', userACookie)
    assert(lightweightProviders.length > 0, '供应商轻量选项不得为空')
    assert(lightweightProviders.every((item) => Object.keys(item).sort().join(',') === 'code,enabled,id,name'), '供应商 options 只能返回 id/code/name/enabled')
    const providerListItems = await getEnvelope<Array<Record<string, unknown>>>(baseUrl, '/__aisys__/api/providers/list', userACookie)
    assert(providerListItems.length > 0, '供应商目录列表不得为空')
    assert(providerListItems.every((item) => !('protocolProfiles' in item)), '供应商目录列表不得预加载 protocolProfiles')
    const providerDetail = await getEnvelope<Record<string, unknown>>(baseUrl, '/__aisys__/api/providers/gpt', userACookie)
    assert(Array.isArray(providerDetail.protocolProfiles), '供应商详情必须按需返回 protocolProfiles')
    const providerDefinitions = await getEnvelope<Array<{ code: string; protocolProfiles: unknown[] }>>(baseUrl, '/__aisys__/api/providers/definitions', userACookie)
    assert(providerDefinitions.some((item) => item.code === 'openai' && Array.isArray(item.protocolProfiles)), '账户创建所需完整供应商定义必须走 definitions')

    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models`,
      userACookie,
      'POST',
      {
        model: 'gpt-http-forbidden-scope',
        scope: 'global',
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      },
      403,
      '普通用户不应创建全局模型'
    )

    const trustedConfigurationTemplate = await postEnvelope<{
      id: string
      releaseDate?: string
      shutdownDate?: string
      pricingNotes?: string
      capabilityNotes?: string
      notes?: string
    }>(
      baseUrl,
      '/__aisys__/api/providers/gpt/models',
      adminCookie,
      {
        scope: 'global',
        model: 'gpt-http-trusted-template',
        status: 'active',
        mode: 'text',
        supportedApiProtocols: ['responses'],
        supportedReasoningEfforts: ['low', 'high'],
        defaultReasoningEffort: 'high',
        releaseDate: '2026-06-01',
        shutdownDate: '2027-06-01',
        inputUsdPer1M: 5,
        outputUsdPer1M: 30,
        pricingNotes: '可信模板计费说明',
        capabilityNotes: '可信模板能力说明',
        notes: '可信模板内部备注'
      }
    )
    const trustedTemplateCopy = await postEnvelope<{
      model: string
      scope: string
      inputUsdPer1M?: number
      releaseDate?: string
      shutdownDate?: string
      pricingNotes?: string
      capabilityNotes?: string
      notes?: string
    }>(
      baseUrl,
      '/__aisys__/api/providers/gpt/models',
      userACookie,
      {
        configurationTemplateId: trustedConfigurationTemplate.id,
        model: 'gpt-http-trusted-template-copy'
      }
    )
    assert.equal(trustedTemplateCopy.scope, 'personal', '普通用户应能从可信模板创建个人模型')
    assert.equal(trustedTemplateCopy.inputUsdPer1M, 5, '普通用户只提交模板和模型 ID 时应继承可信价格')
    assert.equal(trustedTemplateCopy.releaseDate, '2026-06-01', '配置模板必须复制发布时间')
    assert.equal(trustedTemplateCopy.shutdownDate, '2027-06-01', '配置模板必须复制停用时间')
    assert.equal(trustedTemplateCopy.pricingNotes, '可信模板计费说明', '配置模板必须在服务端复制计费备注')
    assert.equal(trustedTemplateCopy.capabilityNotes, '可信模板能力说明', '配置模板必须在服务端复制能力备注')
    assert.equal(trustedTemplateCopy.notes, '可信模板内部备注', '配置模板必须在服务端复制内部备注')

    const userATemplateCatalog = await getEnvelope<Array<{
      id?: string
      model: string
      status: string
      updatedAt: string
      contextWindowTokens?: number
      maxInputTokens?: number
      maxOutputTokens?: number
	  defaultReasoningEffort?: string | null
    }>>(
      baseUrl,
      '/__aisys__/api/providers/gpt/models?includeInactive=true&includeUnpriced=true',
      userACookie
    )
    const userATemplate = userATemplateCatalog.find((item) => item.model === 'gpt-5.6-sol' && item.status === 'active')
    assert(userATemplate?.id, '普通用户配置复制回归需要可见的 GPT 内置模型')
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/gpt/models/${userATemplate.id}`,
      adminCookie,
      'PATCH',
      { expectedUpdatedAt: userATemplate.updatedAt, releaseDate: null },
      400,
      '管理员不得清空内置可见模型的发布时间'
    )
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/gpt/models/${userATemplate.id}`,
      adminCookie,
      'PATCH',
      { expectedUpdatedAt: userATemplate.updatedAt, supportedApiProtocols: [] },
      400,
      '管理员不得清空内置可见模型的接口协议'
    )
    const copiedUserModel = await postEnvelope<{
      model: string
      status: string
      scope: string
      contextWindowTokens?: number
      maxInputTokens?: number
      maxOutputTokens?: number
      inputUsdPer1M?: number
      supportedServiceTiers?: string[]
	  defaultReasoningEffort?: string | null
    }>(
      baseUrl,
      '/__aisys__/api/providers/gpt/models',
      userACookie,
      {
        configurationTemplateId: userATemplate.id,
        model: 'gpt-http-user-a-copied',
        status: 'active'
      }
    )
    assert.equal(copiedUserModel.scope, 'personal', '普通用户配置复制模型应固定为个人范围')
    assert.equal(copiedUserModel.status, 'active', '普通用户应能通过可信配置复制创建启用模型')
    assert.equal(copiedUserModel.contextWindowTokens, userATemplate.contextWindowTokens, '配置复制应继承总上下文')
    assert.equal(copiedUserModel.maxInputTokens, userATemplate.maxInputTokens, '配置复制应继承最大输入')
    assert.equal(copiedUserModel.maxOutputTokens, userATemplate.maxOutputTokens, '配置复制应继承最大输出')
    assert.equal(typeof copiedUserModel.inputUsdPer1M, 'number', '普通用户未提交价格时应由服务端可信继承价格')
    assert.deepEqual(copiedUserModel.supportedServiceTiers, ['priority', 'flex'], '配置复制应继承服务等级')
    assert.equal(copiedUserModel.defaultReasoningEffort, null, '配置复制不得继承默认思考级别，新增模型应由上游决定')

    const userAModel = await postEnvelope<{ id: string; model: string; scope: string; updatedAt: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      userACookie,
      {
        model: 'gpt-http-user-a',
        supportedApiProtocols: ['chat_completions'],
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      }
    )
    assert.equal(userAModel.scope, 'personal', '普通用户创建的模型应固定为个人模型')

    const geminiInteractionsModel = await postEnvelope<{ model: string; supportedApiProtocols: string[] }>(
      baseUrl,
      '/__aisys__/api/providers/gemini/models',
      userACookie,
      {
        model: 'gemini-http-interactions-custom',
        supportedApiProtocols: ['interactions'],
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      }
    )
    assert.deepEqual(geminiInteractionsModel.supportedApiProtocols, ['interactions'], 'Gemini 自定义模型应允许保存 Interactions 协议')

    const userAUpstreamTarget = await postEnvelope<{ id: string; model: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      userACookie,
      {
        model: 'gpt-http-user-a-upstream-target',
        supportedApiProtocols: ['chat_completions'],
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      }
    )
    assert.equal(userAUpstreamTarget.model, 'gpt-http-user-a-upstream-target', '个人自定义模型应直接保存')

    const userACaseLowerModel = await postEnvelope<{ id: string; model: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      userACookie,
      {
        model: 'gpt-http-user-a-case',
        supportedApiProtocols: ['responses'],
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      }
    )
    const userACaseUpperModel = await postEnvelope<{ id: string; model: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      userACookie,
      {
        model: 'GPT-http-user-a-case',
        supportedApiProtocols: ['responses'],
        inputUsdPer1M: 3,
        outputUsdPer1M: 4
      }
    )
    assert.notEqual(userACaseLowerModel.id, userACaseUpperModel.id, '同一供应商同一用户应允许创建仅大小写不同的自定义模型')

    const userAGptModel = await postEnvelope<{
      id: string
      model: string
      providerCode: string
      updatedAt: string
      supportedServiceTiers: string[]
      supportedReasoningEfforts: string[]
      defaultReasoningEffort?: string | null
    }>(
      baseUrl,
      '/__aisys__/api/providers/gpt/models',
      userACookie,
      {
        model: 'gpt-http-user-a-gpt',
        supportedApiProtocols: ['responses'],
        supportedServiceTiers: ['priority'],
        supportedReasoningEfforts: ['low', 'high'],
		defaultReasoningEffort: 'high',
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      }
    )
    assert.equal(userAGptModel.providerCode, 'gpt', 'GPT 目录新建的个人模型应归属 GPT 供应商')
    assert.deepEqual(userAGptModel.supportedServiceTiers, ['priority'], 'GPT 自定义模型 API 应返回服务等级能力')
    assert.deepEqual(userAGptModel.supportedReasoningEfforts, ['low', 'high'], 'GPT 自定义模型 API 应返回思考能力')
    assert.equal(userAGptModel.defaultReasoningEffort, null, 'GPT 新增自定义模型必须忽略默认思考级别并交给上游决定')
    const userAGptUnrelatedPatch = await patchEnvelope<{ id: string; providerCode: string; model: string; status: string; updatedAt: string }>(
	  baseUrl,
	  `/__aisys__/api/providers/gpt/models/${userAGptModel.id}`,
	  userACookie,
	  { expectedUpdatedAt: userAGptModel.updatedAt, notes: 'round-trip' }
	)
	assert.deepEqual(Object.keys(userAGptUnrelatedPatch).sort(), ['id', 'model', 'providerCode', 'status', 'updatedAt'], '模型 PATCH 只应返回当前行局部更新所需字段')
	const userAGptClearedDefault = await patchEnvelope<{ updatedAt: string }>(
	  baseUrl,
	  `/__aisys__/api/providers/gpt/models/${userAGptModel.id}`,
	  userACookie,
	  { expectedUpdatedAt: userAGptUnrelatedPatch.updatedAt, defaultReasoningEffort: null }
	)
	assert.equal(userAGptClearedDefault.updatedAt, userAGptUnrelatedPatch.updatedAt, '同值 PATCH 必须是零写入且不得推进版本')
	const userAGptRestoredDefault = await patchEnvelope<{ updatedAt: string }>(
	  baseUrl,
	  `/__aisys__/api/providers/gpt/models/${userAGptModel.id}`,
	  userACookie,
	  { expectedUpdatedAt: userAGptClearedDefault.updatedAt, defaultReasoningEffort: 'high' }
	)
	assert.equal(userAGptRestoredDefault.updatedAt, userAGptClearedDefault.updatedAt, 'GPT 默认思考级别由上游决定时不得产生无效写入')
	const userAGptAfterPatches = (await getEnvelope<Array<{ id?: string; notes?: string; defaultReasoningEffort?: string | null }>>(
	  baseUrl,
	  '/__aisys__/api/providers/gpt/models?includeInactive=true&includeUnpriced=true',
	  userACookie
	)).find((item) => item.id === userAGptModel.id)
	assert.equal(userAGptAfterPatches?.notes, 'round-trip', '字段级 PATCH 必须保存实际变化字段')
	assert.equal(userAGptAfterPatches?.defaultReasoningEffort, null, '无关 PATCH 必须保持默认思考级别为空')
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/gpt/models`,
      adminCookie,
      'POST',
      {
        model: 'gpt-http-image-invalid-capabilities',
        mode: 'image',
        supportedApiProtocols: ['images'],
        supportedServiceTiers: ['priority'],
        outputUsdPerImage: 0.02
      },
      400,
      'GPT 图片自定义模型 API 必须拒绝非空服务等级能力'
    )
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/gpt/models`,
      adminCookie,
      'POST',
      {
        model: 'gpt-http-audio-invalid-capabilities',
        mode: 'audio',
        supportedApiProtocols: ['audio'],
        supportedReasoningEfforts: ['high'],
        defaultReasoningEffort: 'high',
        audioInputUsdPer1M: 1,
        audioOutputUsdPer1M: 2
      },
      400,
      'GPT 音频自定义模型 API 必须整体拒绝'
    )
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models`,
      adminCookie,
      'POST',
      {
        model: 'openai-http-invalid-capabilities',
        supportedApiProtocols: ['responses'],
        supportedReasoningEfforts: ['high'],
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      },
      201,
      '非 GPT 文本自定义模型 API 必须接受供应商原生思考能力'
    )
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/gpt/models`,
      adminCookie,
      'POST',
      {
        model: 'gpt-http-invalid-reasoning-default',
        supportedApiProtocols: ['responses'],
        supportedReasoningEfforts: ['low'],
        defaultReasoningEffort: 'high',
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      },
      201,
	  'GPT 自定义模型 API 必须忽略默认思考级别并交给上游决定'
    )
    const userAGptClearableModel = await postEnvelope<{ id: string; model: string; updatedAt: string }>(
      baseUrl,
      '/__aisys__/api/providers/gpt/models',
      userACookie,
      {
        model: 'gpt-http-clear-capabilities',
        mode: 'text',
        supportedApiProtocols: ['responses'],
        supportedServiceTiers: ['priority'],
        supportedReasoningEfforts: ['low', 'high'],
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      }
    )
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/gpt/models/${userAGptClearableModel.id}`,
      userACookie,
      'PATCH',
      { expectedUpdatedAt: userAGptClearableModel.updatedAt, mode: 'image' },
      400,
      'GPT 文本模型保留非空能力字段时不应直接切换为图片模式'
    )
    const userAGptClearedModel = await patchEnvelope<{ updatedAt: string }>(
      baseUrl,
      `/__aisys__/api/providers/gpt/models/${userAGptClearableModel.id}`,
      userACookie,
      {
        expectedUpdatedAt: userAGptClearableModel.updatedAt,
        status: 'draft',
        mode: 'image',
        supportedApiProtocols: ['images'],
        supportedServiceTiers: [],
        supportedReasoningEfforts: []
      }
    )
    const clearedModelFromCatalog = (await getEnvelope<Array<{
      id?: string
      mode?: string
      supportedServiceTiers: string[]
      supportedReasoningEfforts: string[]
      defaultReasoningEffort: string | null
    }>>(baseUrl, '/__aisys__/api/providers/gpt/models?includeInactive=true&includeUnpriced=true', userACookie))
      .find((item) => item.id === userAGptClearableModel.id)
    assert.equal(clearedModelFromCatalog?.mode, 'image', 'API 显式清空文本能力后应允许切换为 GPT 图片模型')
    assert.deepEqual(clearedModelFromCatalog?.supportedServiceTiers, [], 'API 应接受空数组清理服务等级能力')
    assert.deepEqual(clearedModelFromCatalog?.supportedReasoningEfforts, [], 'API 应接受空数组清理思考能力')
    assert.equal(clearedModelFromCatalog?.defaultReasoningEffort, null, 'API 更新后必须保持空默认思考级别')

    const userADraft = await postEnvelope<{ id: string; model: string; status: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      userACookie,
      {
        model: 'gpt-http-user-a-draft',
        status: 'draft'
      }
    )
    assert.equal(userADraft.status, 'draft', '草稿模型允许暂不配置价格')

    const userADeletableModel = await postEnvelope<{ id: string; model: string; scope: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      userACookie,
      {
        model: 'gpt-http-user-a-deletable',
        supportedApiProtocols: ['responses'],
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      }
    )
    assert.equal(userADeletableModel.scope, 'personal', '普通用户应能创建可删除的个人模型')

    const userADefaultPreference = await putEnvelope<{ providerCode: string; defaultHealthCheckModel: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/default-health-check-model',
      userACookie,
      { model: userADeletableModel.model }
    )
    assert.equal(userADefaultPreference.defaultHealthCheckModel, userADeletableModel.model, '用户应能把自己可见的个人模型设置为默认检查模型')
    const userAProviderOptions = await getEnvelope<Array<{ code: string; defaultHealthCheckModel: string; systemDefaultHealthCheckModel?: string }>>(baseUrl, '/__aisys__/api/providers/definitions', userACookie)
    assert.equal(userAProviderOptions.find((item) => item.code === 'openai')?.defaultHealthCheckModel, userADeletableModel.model, '用户默认检查模型应覆盖自己的供应商选项')
    assert.notEqual(userAProviderOptions.find((item) => item.code === 'openai')?.systemDefaultHealthCheckModel, userADeletableModel.model, '用户默认检查模型不能覆盖管理员系统默认事实')
    const userBProviderOptions = await getEnvelope<Array<{ code: string; defaultHealthCheckModel: string }>>(baseUrl, '/__aisys__/api/providers/definitions', userBCookie)
    assert.notEqual(userBProviderOptions.find((item) => item.code === 'openai')?.defaultHealthCheckModel, userADeletableModel.model, '用户默认检查模型不能泄露给其他用户')
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/default-health-check-model?viewScope=admin`,
      userACookie,
      'PUT',
      { model: userADraft.model },
      400,
      '草稿模型不应允许设置为默认检查模型'
    )

    const adminGlobalModel = await postEnvelope<{ id: string; model: string; scope: string; systemAccountId?: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      adminCookie,
      {
        model: 'gpt-http-admin-global',
        scope: 'global',
        supportedApiProtocols: ['responses'],
        inputUsdPer1M: 2,
        outputUsdPer1M: 4
      }
    )
    assert.equal(adminGlobalModel.scope, 'global', '管理员应能创建全局模型')
    assert.equal(adminGlobalModel.systemAccountId, undefined, '全局模型不应绑定系统账户')

    const adminPersonalModel = await postEnvelope<{ id: string; model: string; scope: string; updatedAt: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      adminCookie,
      {
        model: 'gpt-http-admin-personal',
        supportedApiProtocols: ['responses'],
        inputUsdPer1M: 2,
        outputUsdPer1M: 4
      }
    )
    assert.equal(adminPersonalModel.scope, 'personal', '管理员应能维护自己账号下的个人模型')

    const adminProvidersBeforeSystemDefault = await getEnvelope<Array<{
      code: string
      protocolProfiles: Array<{ id: string; defaultHealthCheckModel: string }>
    }>>(baseUrl, '/__aisys__/api/providers', adminCookie)
    const openAIProviderBeforeSystemDefault = adminProvidersBeforeSystemDefault.find((item) => item.code === 'openai')
    assert(openAIProviderBeforeSystemDefault, '管理员应能读取 OpenAI 供应商定义')
    const builtInProfileDefaults = new Map(
      openAIProviderBeforeSystemDefault.protocolProfiles.map((profile) => [profile.id, profile.defaultHealthCheckModel])
    )

    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/default-health-check-model?viewScope=admin`,
      adminCookie,
      'PUT',
      { model: adminPersonalModel.model },
      400,
      '管理员自己的个人模型不能设置为系统默认检查模型'
    )

    const adminSystemDefault = await putEnvelope<{ providerCode: string; defaultHealthCheckModel: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/default-health-check-model?viewScope=admin',
      adminCookie,
      { model: adminGlobalModel.model }
    )
    assert.equal(adminSystemDefault.defaultHealthCheckModel, adminGlobalModel.model, '管理员应能把全局启用文本模型设置为系统默认检查模型')

    const userAOptionsAfterSystemDefault = await getEnvelope<Array<{
      code: string
      defaultHealthCheckModel: string
      systemDefaultHealthCheckModel?: string
    }>>(baseUrl, '/__aisys__/api/providers/definitions', userACookie)
    const userAOpenAIOptions = userAOptionsAfterSystemDefault.find((item) => item.code === 'openai')
    assert.equal(userAOpenAIOptions?.defaultHealthCheckModel, userADeletableModel.model, '用户 A 已有个人偏好时应继续以个人检查模型为默认')
    assert.equal(userAOpenAIOptions?.systemDefaultHealthCheckModel, adminGlobalModel.model, '用户 A 仍应看到管理员配置的系统默认检查模型')

    const userBOptionsAfterSystemDefault = await getEnvelope<Array<{
      code: string
      defaultHealthCheckModel: string
      systemDefaultHealthCheckModel?: string
    }>>(baseUrl, '/__aisys__/api/providers/definitions', userBCookie)
    const userBOpenAIOptions = userBOptionsAfterSystemDefault.find((item) => item.code === 'openai')
    assert.equal(userBOpenAIOptions?.defaultHealthCheckModel, adminGlobalModel.model, '用户 B 未设置个人偏好时应使用管理员系统默认检查模型')
    assert.equal(userBOpenAIOptions?.systemDefaultHealthCheckModel, adminGlobalModel.model, '用户 B 应看到管理员系统默认检查模型')

    const adminProvidersAfterSystemDefault = await getEnvelope<Array<{
      code: string
      protocolProfiles: Array<{ id: string; defaultHealthCheckModel: string }>
    }>>(baseUrl, '/__aisys__/api/providers', adminCookie)
    const openAIProviderAfterSystemDefault = adminProvidersAfterSystemDefault.find((item) => item.code === 'openai')
    assert(openAIProviderAfterSystemDefault, '系统默认更新后管理员仍应能读取 OpenAI 供应商定义')
    assert.deepEqual(
      new Map(openAIProviderAfterSystemDefault.protocolProfiles.map((profile) => [profile.id, profile.defaultHealthCheckModel])),
      builtInProfileDefaults,
      '管理员设置系统默认检查模型不能改写协议档案内置 defaultHealthCheckModel'
    )

    const adminCreatedUserAModel = await postEnvelope<{ id: string; model: string; scope: string; systemAccountId?: string }>(
      baseUrl,
      `/__aisys__/api/providers/openai/models?systemAccountId=${encodeURIComponent(userA.id)}`,
      adminCookie,
      {
        model: 'gpt-http-admin-created-user-a',
        scope: 'personal',
        supportedApiProtocols: ['responses'],
        inputUsdPer1M: 2,
        outputUsdPer1M: 4
      }
    )
    assert.equal(adminCreatedUserAModel.scope, 'personal', '管理员切换目标用户后应能创建目标用户个人模型')
    assert.equal(adminCreatedUserAModel.systemAccountId, userA.id, '管理员代建个人模型应归属目标用户')

    const userBModel = await postEnvelope<{ id: string; model: string; scope: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      userBCookie,
      {
        model: 'gpt-http-user-b',
        supportedApiProtocols: ['responses'],
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      }
    )
    assert.equal(userBModel.scope, 'personal', '用户 B 应能维护自己的个人模型')

    await patchEnvelope(
      baseUrl,
      `/__aisys__/api/providers/openai/models/${adminPersonalModel.id}`,
      adminCookie,
      { expectedUpdatedAt: adminPersonalModel.updatedAt, maxOutputTokens: 4096 }
    )
    const adminUpdatedUserAGptModel = await patchEnvelope<{ updatedAt: string }>(
      baseUrl,
      `/__aisys__/api/providers/gpt/models/${userAGptModel.id}`,
      adminCookie,
      { expectedUpdatedAt: userAGptRestoredDefault.updatedAt, maxOutputTokens: 3072 }
    )
    assert.notEqual(adminUpdatedUserAGptModel.updatedAt, userAGptRestoredDefault.updatedAt, '管理员修改目标用户模型必须推进版本')
    const userAUpdatedModel = await patchEnvelope<{ updatedAt: string }>(
      baseUrl,
      `/__aisys__/api/providers/openai/models/${userAModel.id}`,
      userACookie,
      { expectedUpdatedAt: userAModel.updatedAt, maxOutputTokens: 2048 }
    )
    assert.notEqual(userAUpdatedModel.updatedAt, userAModel.updatedAt, '普通用户编辑自己的个人模型必须推进版本')
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models/${userAModel.id}`,
      userACookie,
      'PATCH',
      { expectedUpdatedAt: userAModel.updatedAt, maxOutputTokens: 1024 },
      409,
      '过期版本的模型 PATCH 必须拒绝覆盖较新的修改'
    )
    const userAModelAfterPatch = (await getEnvelope<Array<{ id?: string; maxOutputTokens?: number }>>(
      baseUrl,
      '/__aisys__/api/providers/openai/models?includeInactive=true&includeUnpriced=true',
      userACookie
    )).find((item) => item.id === userAModel.id)
    assert.equal(userAModelAfterPatch?.maxOutputTokens, 2048, '字段级模型 PATCH 必须只保留已提交的新值')
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models/${userAModel.id}`,
      adminCookie,
      'PATCH',
      { expectedUpdatedAt: userAUpdatedModel.updatedAt, model: 'gpt-http-user-a-renamed' },
      400,
      '自定义模型创建后不应允许修改模型 ID'
    )
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models/${userADeletableModel.id}`,
      userACookie,
      'DELETE',
      undefined,
      200,
      '普通用户应能删除自己未绑定账户的个人模型'
    )
    const userAProviderOptionsAfterDefaultDelete = await getEnvelope<Array<{ code: string; defaultHealthCheckModel: string }>>(baseUrl, '/__aisys__/api/providers/definitions', userACookie)
    assert.notEqual(userAProviderOptionsAfterDefaultDelete.find((item) => item.code === 'openai')?.defaultHealthCheckModel, userADeletableModel.model, '删除个人模型时应清理指向该模型的个人默认检查模型')
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models/${adminPersonalModel.id}`,
      userACookie,
      'PATCH',
      { maxOutputTokens: 777 },
      404,
      '普通用户不应发现他人的个人自定义模型'
    )
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models/${userAModel.id}`,
      userBCookie,
      'PATCH',
      { maxOutputTokens: 777 },
      404,
      '普通用户不应发现他人的个人自定义模型'
    )
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models/${userAModel.id}`,
      userBCookie,
      'DELETE',
      undefined,
      404,
      '普通用户不应发现他人的个人自定义模型'
    )

    const userAAccess = { systemAccountId: userA.id, role: 'user' as const }
    const userAGroup = repositories.createGroup({
      name: '模型目录绑定回归分组',
      providerCode: 'openai',
    }, userAAccess)
    repositories.createAccount({
      providerCode: 'openai',
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      name: '模型目录绑定回归账户',
      type: 'api_key',
      status: 'active',
      credentials: { api_key: 'sk-model-catalog-bound', base_url: 'https://api.openai.com/v1' },
      groupId: userAGroup.id,
      supportedModels: [userAModel.model, userAUpstreamTarget.model],
      healthCheckModel: userAModel.model,
      modelMappings: [
        {
          sourceModel: userAModel.model,
          sourceEndpointFamily: 'chat_completions',
          upstreamModel: userAUpstreamTarget.model,
          upstreamEndpointFamily: 'chat_completions',
          enabled: true
        }
      ]
    }, userAAccess)
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models/${userAModel.id}`,
      userACookie,
      'DELETE',
      undefined,
      409,
      '绑定为账户支持模型或映射源模型后不应允许删除个人模型'
    )
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models/${userAUpstreamTarget.id}`,
      userACookie,
      'DELETE',
      undefined,
      409,
      '绑定为账户映射上游模型后不应允许删除个人模型'
    )
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models/${userAModel.id}`,
      adminCookie,
      'PATCH',
      { inputUsdPer1M: null, outputUsdPer1M: null },
      400,
      '启用模型清空价格时应拒绝保存'
    )

    const userAVisible = await getEnvelope<Array<{ model: string; scope: string; status: string; providerCode: string }>>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      userACookie
    )
    assert(userAVisible.some((item) => item.model === 'gpt-http-user-a' && item.scope === 'personal'), '用户应能看到自己的公开个人模型')
    assert(userAVisible.some((item) => item.model === 'gpt-http-user-a-upstream-target' && item.scope === 'personal'), '用户应能看到自己的个人自定义模型')
    assert(userAVisible.some((item) => item.model === 'gpt-http-user-a-case' && item.scope === 'personal'), '用户应能看到小写大小写变体个人模型')
    assert(userAVisible.some((item) => item.model === 'GPT-http-user-a-case' && item.scope === 'personal'), '用户应能看到大写大小写变体个人模型')
    assert(userAVisible.some((item) => item.model === 'gpt-http-admin-global' && item.scope === 'global'), '用户应能看到管理员创建的全局模型')
    assert(userAVisible.some((item) => item.model === 'gpt-http-admin-created-user-a' && item.scope === 'personal'), '用户应能看到管理员代建的个人模型')
    assert(userAVisible.some((item) => item.model === 'gpt-http-user-a-gpt' && item.providerCode === 'gpt'), 'OpenAI 聚合目录应包含 GPT 目录的个人自定义模型')
    assert.equal(userAVisible.some((item) => item.model === 'gpt-http-admin-personal'), false, '用户不应看到管理员个人模型')
    assert.equal(userAVisible.some((item) => item.model === 'gpt-http-user-a-draft'), false, '默认管理模型目录不应返回草稿模型')

    const userAGptModelOptions = await getEnvelope<Array<{ id: string; name: string }>>(
      baseUrl,
      '/__aisys__/api/providers/models/options?providerCode=gpt&keyword=gpt-http-user-a-gpt&selectedIds=gpt-http-user-a-gpt&limit=10',
      userACookie
    )
    assert.deepEqual(userAGptModelOptions.find((item) => item.id === 'gpt-http-user-a-gpt'), {
      id: 'gpt-http-user-a-gpt',
      name: 'gpt-http-user-a-gpt',
      supportedApiProtocols: ['responses'],
      supportedServiceTiers: ['priority'],
      supportedReasoningEfforts: ['low', 'high']
    }, '单供应商模型选项应包含个人模型及目录能力')
    assert(userAGptModelOptions.every((item) => Object.keys(item).sort().join(',') === 'id,name,supportedApiProtocols,supportedReasoningEfforts,supportedServiceTiers'), '单供应商模型选项应返回能力且不得返回价格或供应商详情')

    const selectedWithWindow = await getEnvelope<Array<{ id: string; name: string }>>(
      baseUrl,
      '/__aisys__/api/providers/models/options?providerCode=gpt&selectedIds=gpt-http-user-a-gpt&limit=1',
      userACookie
    )
    assert(selectedWithWindow.some((item) => item.id === 'gpt-http-user-a-gpt'), '已选模型必须在窗口查询中补齐')
    assert(selectedWithWindow.length > 1, 'selectedIds 不得把基础 limit 窗口误收窄为仅已选项')

    const bracketSelectedWithKeyword = await getEnvelope<Array<{ id: string; name: string }>>(
      baseUrl,
      '/__aisys__/api/providers/models/options?providerCode=gpt&keyword=does-not-match&selectedIds[]=gpt-http-user-a-gpt&limit=1',
      userACookie
    )
    assert(
      bracketSelectedWithKeyword.some((item) => item.id === 'gpt-http-user-a-gpt'),
      'selectedIds[] 必须保留搜索结果之外的已选模型'
    )

    const capability = await getEnvelope<{
      id: string
      name: string
      supportedApiProtocols: string[]
      supportedServiceTiers: string[]
      supportedReasoningEfforts: string[]
    }>(
      baseUrl,
      `/__aisys__/api/providers/gpt/models/${encodeURIComponent('gpt-http-user-a-gpt')}/capabilities`,
      userACookie
    )
    assert.equal(capability.id, 'gpt-http-user-a-gpt', '模型能力接口必须按 providerCode/modelId 定点读取')
    assert(Array.isArray(capability.supportedApiProtocols), '模型能力接口应返回协议能力')
    assert(Array.isArray(capability.supportedServiceTiers), '模型能力接口应返回服务等级能力')

    const userACrossProviderOptions = await getEnvelope<Array<{ id: string; name: string }>>(
      baseUrl,
      '/__aisys__/api/providers/models/options?keyword=gpt-http-admin-global&limit=10',
      userACookie
    )
    assert(userACrossProviderOptions.some((item) => item.id === 'gpt-http-admin-global'), '跨供应商模型选项应包含管理员全局模型')
    assert(userACrossProviderOptions.every((item) => Object.keys(item).sort().join(',') === 'id,name,supportedApiProtocols,supportedReasoningEfforts,supportedServiceTiers'), '跨供应商模型选项也必须返回目录能力')

    const userAHybridVisible = await getEnvelope<Array<{ model: string; providerCode: string }>>(
      baseUrl,
      '/__aisys__/api/providers/hybrid/models',
      userACookie
    )
    assert(userAHybridVisible.some((item) => item.providerCode === 'gpt' && item.model === 'gpt-http-user-a-gpt'), '混合供应商模型目录应聚合真实供应商模型')
    assert.equal(userAHybridVisible.some((item) => item.providerCode === 'hybrid'), false, '混合供应商模型目录不应返回 hybrid 自身模型')
    assert.equal(userAHybridVisible.some((item) => item.model === 'hybrid-regression-should-not-list'), false, '混合供应商模型目录不应返回 hybrid 自身模型')
    const userAHybridDefaultPreference = await putEnvelope<{ providerCode: string; defaultHealthCheckModel: string }>(
      baseUrl,
      '/__aisys__/api/providers/hybrid/default-health-check-model',
      userACookie,
      { model: 'gpt-http-user-a-gpt' }
    )
    assert.equal(userAHybridDefaultPreference.defaultHealthCheckModel, 'gpt-http-user-a-gpt', '混合供应商应允许把聚合目录中的真实文本模型设为默认检查模型')
    const userAProviderOptionsAfterHybridDefault = await getEnvelope<Array<{ code: string; defaultHealthCheckModel: string }>>(baseUrl, '/__aisys__/api/providers/definitions', userACookie)
    assert.equal(userAProviderOptionsAfterHybridDefault.find((item) => item.code === 'hybrid')?.defaultHealthCheckModel, 'gpt-http-user-a-gpt', '混合供应商默认检查模型偏好应回填到用户供应商选项')

    const userAMaintenanceVisible = await getEnvelope<Array<{ model: string; status: string; providerCode: string }>>(
      baseUrl,
      '/__aisys__/api/providers/openai/models?includeInactive=true&includeUnpriced=true',
      userACookie
    )
    assert(userAMaintenanceVisible.some((item) => item.model === 'gpt-http-user-a-draft' && item.status === 'draft'), '普通用户维护视图应能看到自己的草稿模型')
    assert(userAMaintenanceVisible.some((item) => item.model === 'gpt-http-user-a-gpt' && item.providerCode === 'gpt'), '普通用户维护视图应能看到 GPT 目录的个人模型')
    assert(userAMaintenanceVisible.some((item) => item.model === 'gpt-http-admin-global'), '普通用户维护视图应能看到全局模型')

    const adminDefaultVisible = await getEnvelope<Array<{ model: string; scope: string; providerCode: string }>>(
      baseUrl,
      '/__aisys__/api/providers/openai/models?includeInactive=true&includeUnpriced=true',
      adminCookie
    )
    assert(adminDefaultVisible.some((item) => item.model === 'gpt-http-admin-personal' && item.scope === 'personal'), '管理员默认模型目录应按管理员自己账号查看个人模型')
    assert(adminDefaultVisible.some((item) => item.model === 'gpt-http-admin-global' && item.scope === 'global'), '管理员默认模型目录应包含全局模型')
    assert.equal(adminDefaultVisible.some((item) => item.model === 'gpt-http-user-a'), false, '管理员默认模型目录不应混入其他用户个人模型')

    const adminUserAVisible = await getEnvelope<Array<{ model: string; status: string; providerCode: string }>>(
      baseUrl,
      `/__aisys__/api/providers/openai/models?systemAccountId=${encodeURIComponent(userA.id)}&includeInactive=true&includeUnpriced=true`,
      adminCookie
    )
    assert(adminUserAVisible.some((item) => item.model === 'gpt-http-user-a'), '管理员传 systemAccountId 应看到目标用户个人模型，便于维护目标用户 AI 账户模型映射')
    assert(adminUserAVisible.some((item) => item.model === 'gpt-http-user-a-draft'), '管理员维护目标用户模型目录时应看到目标用户草稿模型')
    assert(adminUserAVisible.some((item) => item.model === 'gpt-http-user-a-gpt'), '管理员传 systemAccountId 应看到目标用户 GPT 个人模型')
    assert(adminUserAVisible.some((item) => item.model === 'gpt-http-admin-created-user-a'), '管理员传 systemAccountId 应看到自己代目标用户创建的个人模型')
    assert(adminUserAVisible.some((item) => item.model === 'gpt-http-admin-global'), '管理员传 systemAccountId 应继续看到全局模型')
    assert.equal(adminUserAVisible.some((item) => item.model === 'gpt-http-admin-personal'), false, '管理员传 systemAccountId 时不应继续固定查看自己的个人模型')
    assert.equal(adminUserAVisible.some((item) => item.model === 'gpt-http-user-b'), false, '管理员传 systemAccountId 后不应混入其他用户个人模型')
    const adminUserAModelOptions = await getEnvelope<Array<{ id: string; name: string }>>(
      baseUrl,
      `/__aisys__/api/providers/models/options?providerCode=gpt&keyword=gpt-http-user-a-gpt&selectedIds=gpt-http-user-a-gpt&limit=10&systemAccountId=${encodeURIComponent(userA.id)}`,
      adminCookie
    )
    assert(adminUserAModelOptions.some((item) => item.id === 'gpt-http-user-a-gpt'), '管理员维护目标用户账号时模型选项应包含目标用户真实供应商个人模型')
    assert.equal(adminUserAModelOptions.some((item) => item.id === 'gpt-http-admin-personal'), false, '管理员维护目标用户账号时模型选项不应混入管理员自己的个人模型')

    const userBVisible = await getEnvelope<Array<{ model: string; providerCode: string }>>(
      baseUrl,
      `/__aisys__/api/providers/openai/models?systemAccountId=${encodeURIComponent(userA.id)}&includeInactive=true&includeUnpriced=true`,
      userBCookie
    )
    assert.equal(userBVisible.some((item) => item.model === 'gpt-http-user-a'), false, '个人模型不应泄露给其他用户')
    assert.equal(userBVisible.some((item) => item.model === 'gpt-http-user-a-upstream-target'), false, '个人自定义模型不应泄露给其他用户')
    assert.equal(userBVisible.some((item) => item.model === 'gpt-http-user-a-gpt'), false, '其他用户不应看到 GPT 目录的个人模型')
    assert.equal(userBVisible.some((item) => item.model === 'gpt-http-user-a-draft'), false, '个人草稿模型不应泄露给其他用户')
    assert(userBVisible.some((item) => item.model === 'gpt-http-admin-global'), '全局模型应对其他用户可见')
    assert(userBVisible.some((item) => item.model === 'gpt-http-user-b'), '普通用户传 systemAccountId 时仍应固定查看自己的个人模型')
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

async function putEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'PUT',
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
  method: 'POST' | 'PUT' | 'PATCH' | 'DELETE',
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
