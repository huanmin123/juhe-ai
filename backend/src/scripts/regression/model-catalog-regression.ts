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
  { providersRouter, dedupeProviderModelOptions },
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
    supportedApiProtocols: ['responses'],
    releaseDate: '2026-01-02',
    inputUsdPer1M: 2,
    outputUsdPer1M: 8,
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-upstream-target',
    scope: 'global',
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
    pricingModel: 'gpt-regression-upstream-target',
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-draft',
    scope: 'global',
    status: 'draft',
    supportedApiProtocols: ['responses'],
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-5.5',
    scope: 'global',
    supportedApiProtocols: ['responses'],
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-overridden-pricing-alias',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedApiProtocols: ['responses'],
    pricingModel: 'gpt-5.5',
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-audio',
    scope: 'global',
    mode: 'audio',
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
    supportedApiProtocols: ['images'],
    outputUsdPerImage: 0.04,
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'openai',
    model: 'openai-regression-global',
    scope: 'global',
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
  assert(publicModels.has('gpt-regression-upstream-target'), '自定义上游目标模型应直接进入公开模型目录')
  assert(publicModels.has('gpt-regression-audio'), '只有音频价格的自定义模型应进入公开模型目录')
  assert(publicModels.has('gpt-regression-image-unit'), '只有按张图片价格的自定义模型应进入公开模型目录')
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
  assert(openAICompatibleCatalog.some((item) => item.providerCode === 'deepseek'), 'OpenAI 兼容模型目录应聚合 DeepSeek OpenAI 协议模型')
  assert(openAICompatibleCatalog.some((item) => item.providerCode === 'glm'), 'OpenAI 兼容模型目录应聚合 GLM OpenAI 协议模型')
  assert(openAICompatibleCatalog.some((item) => item.model === 'openai-regression-global'), '通用 OpenAI-compatible 自身模型不要求排在其他 OpenAI 协议供应商模型之前')

  const dedupedProviderModelOptions = dedupeProviderModelOptions([
    { providerCode: 'gpt', model: 'shared-model' },
    { providerCode: 'deepseek', model: 'shared-model' },
    { providerCode: ' GPT ', model: ' shared-model ' },
    { providerCode: '', model: 'shared-model' },
    { providerCode: 'glm', model: ' ' }
  ])
  assert.deepEqual(dedupedProviderModelOptions, [
    { providerCode: 'gpt', model: 'shared-model' },
    { providerCode: 'deepseek', model: 'shared-model' }
  ], '供应商模型选项必须按 providerCode + model 去重，不能吞掉跨供应商同名模型')

  const deepSeekCatalog = catalogService.listProviderModelCatalog({
    providerCode: 'deepseek',
    systemAccountId: 'sys_admin'
  })
  const deepSeekModels = new Set(deepSeekCatalog.map((item) => item.model))
  assert(deepSeekModels.has('deepseek-v4-flash'), 'DeepSeek 模型目录应包含官方 V4 Flash')
  assert(deepSeekModels.has('deepseek-v4-pro'), 'DeepSeek 模型目录应包含官方 V4 Pro')
  assert(deepSeekModels.has('deepseek-ai-v4-flash'), 'DeepSeek 模型目录应包含上游 deepseek-ai V4 Flash 别名')
  assert(deepSeekModels.has('deepseek-ai-v4-pro'), 'DeepSeek 模型目录应包含上游 deepseek-ai V4 Pro 别名')
  if (new Date().toISOString().slice(0, 10) < '2026-07-24') {
    assert(deepSeekModels.has('deepseek-chat'), 'DeepSeek 模型目录在 deepseek-chat 退役前应包含官方历史兼容名')
    assert(deepSeekModels.has('deepseek-reasoner'), 'DeepSeek 模型目录在 deepseek-reasoner 退役前应包含官方历史兼容名')
  }
  assert.deepEqual(
    deepSeekCatalog.map((item) => item.model),
    [
      'deepseek-v4-flash',
      'deepseek-v4-pro',
      'deepseek-ai-v4-flash',
      'deepseek-ai-v4-pro',
      ...(new Date().toISOString().slice(0, 10) < '2026-07-24' ? ['deepseek-chat', 'deepseek-reasoner'] : [])
    ],
    'DeepSeek 模型目录应按当前官方优先模型到历史兼容名排序'
  )

  const glmCatalog = catalogService.listProviderModelCatalog({
    providerCode: 'glm',
    systemAccountId: 'sys_admin'
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
    'glm-4.5-flash',
    'glm-4-32b-0414-128k',
    'glm-4-long',
    'glm-4-flashx-250414',
    'glm-4-flash-250414'
  ]) {
    assert(glmModels.has(id), `GLM 模型目录应包含官方文本模型 ${id}`)
  }
  assert.equal(glmModels.has('glm-5.2-free'), false, 'GLM 可见模型目录不应包含非官方 glm-5.2-free')
  assert.deepEqual(
    glmCatalog.map((item) => item.model),
    [
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
      'glm-4.5-flash',
      'glm-4-32b-0414-128k',
      'glm-4-long',
      'glm-4-flashx-250414',
      'glm-4-flash-250414'
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
    'gemini-embedding-2',
    'gemini-embedding-001'
  ]) {
    assert(geminiModels.has(id), `Gemini 模型目录应包含 Google 官方模型 ${id}`)
  }
  for (const id of [
    'gemini-3.5-flash-antigravity',
    'gemini-3.5-flash-antigravity-ultra'
  ]) {
    assert.equal(geminiModels.has(id), false, `${id} 是中转自定义型号，不应进入 Gemini 官方内置目录`)
  }
  assert.deepEqual(
    geminiCatalog.map((item) => item.model),
    [
      'gemini-3.5-flash',
      'gemini-3.1-pro-preview',
      'gemini-3.1-pro-preview-customtools',
      'gemini-3-flash-preview',
      'gemini-3.1-flash-lite',
      'gemini-2.5-pro',
      'gemini-2.5-flash',
      'gemini-2.5-flash-lite',
      'gemini-embedding-2',
      'gemini-embedding-001'
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
    assert(anthropicModels.has(id), `Anthropic 模型目录应包含 Claude Code 模型别名 ${id}`)
  }
  for (const id of [
    'claude-fable-5',
    'claude-mythos-5',
    'claude-opus-4-8',
    'claude-opus-4-7',
    'claude-opus-4-6',
    'claude-opus-4-6-thinking',
    'claude-sonnet-4-6',
    'claude-sonnet-4-6-thinking',
    'claude-haiku-4-5',
    'claude-sonnet-4-5',
    'claude-opus-4-5'
  ]) {
    assert(anthropicModels.has(id), `Anthropic 模型目录应包含当前有效 Claude / Claude Code 兼容模型 ${id}`)
  }
  assert(anthropicModels.has('claude-haiku-4-5-20251001'), 'Anthropic 模型目录应包含 Haiku 4.5 官方 dated ID')
  assert(anthropicModels.has('claude-sonnet-4-5-20250929'), 'Anthropic 模型目录应包含 Sonnet 4.5 官方 dated ID')
  assert(anthropicModels.has('claude-opus-4-5-20251101'), 'Anthropic 模型目录应包含 Opus 4.5 官方 dated ID')
  if (new Date().toISOString().slice(0, 10) < '2026-06-30') {
    assert(anthropicModels.has('claude-mythos-preview'), 'Anthropic 模型目录在 Mythos preview 退休前应包含该模型')
  } else {
    assert.equal(anthropicModels.has('claude-mythos-preview'), false, 'Mythos preview 已退休，不应进入 Anthropic 模型目录')
  }
  if (new Date().toISOString().slice(0, 10) < '2026-08-05') {
    assert(anthropicModels.has('claude-opus-4-1'), 'Anthropic 模型目录在 Opus 4.1 shutdown date 前应包含稳定别名')
    assert(anthropicModels.has('claude-opus-4-1-20250805'), 'Anthropic 模型目录在 Opus 4.1 shutdown date 前应包含 dated ID')
  }
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
      'claude-fable-5',
      'claude-mythos-5',
      ...(new Date().toISOString().slice(0, 10) < '2026-06-30' ? ['claude-mythos-preview'] : []),
      'claude-opus-4-8',
      'claude-opus-4-7',
      'claude-opus-4-6',
      'claude-opus-4-6-thinking',
      'claude-opus-4-5',
      'claude-opus-4-5-20251101',
      ...(new Date().toISOString().slice(0, 10) < '2026-08-05' ? ['claude-opus-4-1', 'claude-opus-4-1-20250805'] : []),
      'claude-sonnet-4-6',
      'claude-sonnet-4-6-thinking',
      'claude-sonnet-4-5',
      'claude-sonnet-4-5-20250929',
      'claude-haiku-4-5',
      'claude-haiku-4-5-20251001',
      'best',
      'fable',
      'opus',
      'opus[1m]',
      'opusplan',
      'sonnet',
      'sonnet[1m]',
      'haiku'
    ],
    'Anthropic 模型目录应按官方当前模型从新到旧排序，Claude Code 别名排在官方模型后'
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
  const globalModel = response.data.find((item) => item.id === 'gpt-regression-global')
  assert(globalModel, '/v1/models 应包含公开自定义模型')
  assert.deepEqual(Object.keys(globalModel).sort(), ['created', 'id', 'object', 'owned_by'], '/v1/models 模型项只能暴露 OpenAI 标准字段')
  assert.equal(globalModel.object, 'model', '/v1/models 模型项 object 必须是 model')
  assert.equal(globalModel.created, Date.parse('2026-01-02T00:00:00.000Z') / 1000, '/v1/models created 应为 Unix 秒')
  assert(response.data.some((item) => item.id === 'gpt-regression-upstream-target'), '/v1/models 应包含启用且可计价的自定义上游目标模型')

  const codexResponse = catalogService.buildCodexModelsResponseFromCatalog(publicCatalog)
  assert(Array.isArray(codexResponse.models), 'Codex /models 顶层 models 必须是数组')
  assert.equal(Object.prototype.hasOwnProperty.call(codexResponse, 'data'), false, 'Codex /models 不应返回 OpenAI data 字段')
  const codexGlobalModel = codexResponse.models.find((item) => item.slug === 'gpt-regression-global')
  assert(codexGlobalModel, 'Codex /models 应包含公开自定义模型')
  assert.equal(codexGlobalModel.display_name, 'gpt-regression-global', 'Codex /models display_name 默认使用模型名')
  assert.equal(codexGlobalModel.shell_type, 'shell_command', 'Codex /models shell_type 必须匹配 Codex ModelInfo')
  assert.equal(codexGlobalModel.visibility, 'list', 'Codex /models visibility 必须可进入列表')
  assert.equal(codexGlobalModel.supported_in_api, true, 'Codex /models 模型必须标记 API 可用')
  assert.equal(codexGlobalModel.default_reasoning_level, 'medium', 'Codex /models 默认 reasoning 应为 medium')
  assert(codexGlobalModel.supported_reasoning_levels.some((item) => item.effort === 'medium'), 'Codex /models 应包含 medium reasoning 选项')
  assert.equal(typeof codexGlobalModel.base_instructions, 'string', 'Codex /models 必须提供 base_instructions')
  assert.equal(codexGlobalModel.truncation_policy.mode, 'bytes', 'Codex /models 必须提供 truncation_policy')
  assert(codexResponse.models.some((item) => item.slug === 'gpt-regression-upstream-target'), 'Codex /models 应包含启用且可计价的自定义上游目标模型')

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
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: null,
    outputUsdPer1M: null,
    pricingModel: 'gpt-regression-upstream-target',
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
        supportedApiProtocols: ['responses'],
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      }
    )
    assert.equal(userAModel.scope, 'personal', '普通用户创建的模型应固定为个人模型')

    const userAUpstreamTarget = await postEnvelope<{ id: string; model: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      userACookie,
      {
        model: 'gpt-http-user-a-upstream-target',
        scope: 'personal',
        supportedApiProtocols: ['responses'],
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      }
    )
    assert.equal(userAUpstreamTarget.model, 'gpt-http-user-a-upstream-target', '个人自定义模型应直接保存')

    const userAGptModel = await postEnvelope<{ id: string; model: string; providerCode: string }>(
      baseUrl,
      '/__aisys__/api/providers/gpt/models',
      userACookie,
      {
        model: 'gpt-http-user-a-gpt',
        scope: 'personal',
        supportedApiProtocols: ['responses'],
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      }
    )
    assert.equal(userAGptModel.providerCode, 'gpt', 'GPT 目录新建的个人模型应归属 GPT 供应商')

    const userADraft = await postEnvelope<{ id: string; model: string; status: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      userACookie,
      {
        model: 'gpt-http-user-a-draft',
        scope: 'personal',
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
        scope: 'personal',
        supportedApiProtocols: ['responses'],
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      }
    )
    assert.equal(userADeletableModel.scope, 'personal', '普通用户应能创建可删除的个人模型')

    const adminGlobalModel = await postEnvelope<{ id: string; model: string; scope: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      adminCookie,
      {
        model: 'gpt-http-global',
        scope: 'global',
        supportedApiProtocols: ['responses'],
        inputUsdPer1M: 2,
        outputUsdPer1M: 4
      }
    )
    assert.equal(adminGlobalModel.scope, 'global', '管理员应能创建全局自定义模型')

    const adminPersonalModel = await postEnvelope<{ id: string; model: string; scope: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      adminCookie,
      {
        model: 'gpt-http-admin-personal',
        scope: 'personal',
        supportedApiProtocols: ['responses'],
        inputUsdPer1M: 2,
        outputUsdPer1M: 4
      }
    )
    assert.equal(adminPersonalModel.scope, 'personal', '管理员应能维护自己账号下的个人模型')

    const userBModel = await postEnvelope<{ id: string; model: string; scope: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      userBCookie,
      {
        model: 'gpt-http-user-b',
        scope: 'personal',
        supportedApiProtocols: ['responses'],
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      }
    )
    assert.equal(userBModel.scope, 'personal', '用户 B 应能维护自己的个人模型')

    await patchEnvelope(
      baseUrl,
      `/__aisys__/api/providers/openai/models/${adminGlobalModel.id}`,
      adminCookie,
      { maxOutputTokens: 4096 }
    )
    const adminUpdatedUserAGptModel = await patchEnvelope<{ providerCode: string; maxOutputTokens?: number }>(
      baseUrl,
      `/__aisys__/api/providers/gpt/models/${userAGptModel.id}`,
      adminCookie,
      { maxOutputTokens: 3072 }
    )
    assert.equal(adminUpdatedUserAGptModel.maxOutputTokens, 3072, '管理员应能操作可见的目标用户个人模型')
    const userAUpdatedModel = await patchEnvelope<{ model: string; maxOutputTokens?: number }>(
      baseUrl,
      `/__aisys__/api/providers/openai/models/${userAModel.id}`,
      userACookie,
      { maxOutputTokens: 2048 }
    )
    assert.equal(userAUpdatedModel.maxOutputTokens, 2048, '普通用户应能编辑自己的个人模型')
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models/${userAModel.id}`,
      userACookie,
      'PATCH',
      { model: 'gpt-http-user-a-renamed' },
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
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models/${adminGlobalModel.id}`,
      userACookie,
      'PATCH',
      { maxOutputTokens: 777 },
      403,
      '普通用户不应修改全局自定义模型'
    )
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models/${userAModel.id}`,
      userBCookie,
      'PATCH',
      { maxOutputTokens: 777 },
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

    const userAAccess = { systemAccountId: userA.id, role: 'user' as const }
    const userAGroup = repositories.createGroup({
      name: '模型目录绑定回归分组',
      providerCode: 'openai'
    }, userAAccess)
    repositories.createAccount({
      providerCode: 'openai',
      name: '模型目录绑定回归账户',
      type: 'api_key',
      status: 'active',
      credentials: { api_key: 'sk-model-catalog-bound', base_url: 'https://api.openai.com/v1' },
      groupId: userAGroup.id,
      supportedModels: [userAModel.model, userAUpstreamTarget.model],
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
      userACookie,
      'PATCH',
      { inputUsdPer1M: null, outputUsdPer1M: null },
      400,
      '启用模型清空价格且没有 pricingModel 时应拒绝保存'
    )

    const userAVisible = await getEnvelope<Array<{ model: string; scope: string; status: string; providerCode: string }>>(
      baseUrl,
      '/__aisys__/api/providers/openai/models',
      userACookie
    )
    assert(userAVisible.some((item) => item.model === 'gpt-http-user-a' && item.scope === 'personal'), '用户应能看到自己的公开个人模型')
    assert(userAVisible.some((item) => item.model === 'gpt-http-user-a-upstream-target' && item.scope === 'personal'), '用户应能看到自己的个人自定义模型')
    assert(userAVisible.some((item) => item.model === 'gpt-http-user-a-gpt' && item.providerCode === 'gpt'), 'OpenAI 聚合目录应包含 GPT 目录的个人自定义模型')
    assert(userAVisible.some((item) => item.model === 'gpt-http-global' && item.scope === 'global'), '用户应能看到管理员全局模型')
    assert.equal(userAVisible.some((item) => item.model === 'gpt-http-user-a-draft'), false, '默认管理模型目录不应返回草稿模型')

    const userAMaintenanceVisible = await getEnvelope<Array<{ model: string; status: string; providerCode: string }>>(
      baseUrl,
      '/__aisys__/api/providers/openai/models?includeInactive=true&includeUnpriced=true',
      userACookie
    )
    assert(userAMaintenanceVisible.some((item) => item.model === 'gpt-http-user-a-draft' && item.status === 'draft'), '普通用户维护视图应能看到自己的草稿模型')
    assert(userAMaintenanceVisible.some((item) => item.model === 'gpt-http-user-a-gpt' && item.providerCode === 'gpt'), '普通用户维护视图应能看到 GPT 目录的个人模型')

    const adminDefaultVisible = await getEnvelope<Array<{ model: string; scope: string; providerCode: string }>>(
      baseUrl,
      '/__aisys__/api/providers/openai/models?includeInactive=true&includeUnpriced=true',
      adminCookie
    )
    assert(adminDefaultVisible.some((item) => item.model === 'gpt-http-admin-personal' && item.scope === 'personal'), '管理员默认模型目录应按管理员自己账号查看个人模型')
    assert.equal(adminDefaultVisible.some((item) => item.model === 'gpt-http-user-a'), false, '管理员默认模型目录不应混入其他用户个人模型')

    const adminUserAVisible = await getEnvelope<Array<{ model: string; status: string; providerCode: string }>>(
      baseUrl,
      `/__aisys__/api/providers/openai/models?systemAccountId=${encodeURIComponent(userA.id)}&includeInactive=true&includeUnpriced=true`,
      adminCookie
    )
    assert(adminUserAVisible.some((item) => item.model === 'gpt-http-user-a'), '管理员切换目标用户后应能看到该用户个人模型')
    assert(adminUserAVisible.some((item) => item.model === 'gpt-http-user-a-draft' && item.status === 'draft'), '管理员切换目标用户维护视图应能看到该用户草稿模型')
    assert(adminUserAVisible.some((item) => item.model === 'gpt-http-user-a-gpt' && item.providerCode === 'gpt'), '管理员切换目标用户后应能看到该用户 GPT 个人模型')
    assert.equal(adminUserAVisible.some((item) => item.model === 'gpt-http-admin-personal'), false, '管理员切换目标用户后不应混入管理员个人模型')
    assert.equal(adminUserAVisible.some((item) => item.model === 'gpt-http-user-b'), false, '管理员切换目标用户后不应混入其他用户个人模型')

    const userBVisible = await getEnvelope<Array<{ model: string; providerCode: string }>>(
      baseUrl,
      `/__aisys__/api/providers/openai/models?systemAccountId=${encodeURIComponent(userA.id)}&includeInactive=true&includeUnpriced=true`,
      userBCookie
    )
    assert.equal(userBVisible.some((item) => item.model === 'gpt-http-user-a'), false, '个人模型不应泄露给其他用户')
    assert.equal(userBVisible.some((item) => item.model === 'gpt-http-user-a-upstream-target'), false, '个人自定义模型不应泄露给其他用户')
    assert.equal(userBVisible.some((item) => item.model === 'gpt-http-user-a-gpt'), false, '其他用户不应看到 GPT 目录的个人模型')
    assert.equal(userBVisible.some((item) => item.model === 'gpt-http-user-a-draft'), false, '个人草稿模型不应泄露给其他用户')
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
