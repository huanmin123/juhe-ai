import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
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
  modelPricingService,
  { providersRouter, dedupeProviderModelOptions },
  { requireAuth },
  { requestContextMiddleware },
  repositories,
  customProviderModelsRepository
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/model-pricing/model-catalog.service.js'),
  import('../../modules/model-pricing/model-pricing.service.js'),
  import('../../modules/providers/providers.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/repositories.js'),
  import('../../storage/custom-provider-models.repository.js')
])

try {
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
    pricingModel: 'gpt-regression-upstream-target',
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
    pricingModel: 'gpt-5.5',
    actorSystemAccountId: 'sys_admin'
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-audio',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    mode: 'audio',
    supportedApiProtocols: ['audio'],
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
  assertCatalogReleaseDateDescending(publicCatalog, 'GPT 公开模型目录')
  const publicModels = new Set(publicCatalog.map((item) => item.model))
  assert(publicCatalog.some((item) => item.model === 'gpt-regression-global' && item.scope === 'global'), '全局自定义模型应进入当前账号模型目录')
  assert(publicModels.has('gpt-regression-personal'), '当前账号个人自定义模型应进入模型目录')
  const personalCapabilityModel = publicCatalog.find((item) => item.model === 'gpt-regression-capabilities')
  assert.deepEqual(personalCapabilityModel?.supportedServiceTiers, ['priority', 'flex'], '自定义模型服务等级能力必须完成 SQLite 往返')
  assert.deepEqual(personalCapabilityModel?.supportedReasoningEfforts, ['low', 'medium', 'high'], '自定义模型思考能力必须完成 SQLite 往返')
  assert.equal(personalCapabilityModel?.defaultReasoningEffort, 'medium', '自定义模型默认思考级别必须完成 SQLite 往返')
  assert.equal(personalCapabilityModel?.supportsServiceTier, true, '自定义模型 supportsServiceTier 必须由精确能力数组派生')
  assert(publicModels.has('gpt-regression-alias'), '带 pricingModel 的个人模型应进入个人公开模型目录')
  assert(publicModels.has('gpt-regression-upstream-target'), '自定义上游目标模型应直接进入公开模型目录')
  assert(publicModels.has('gpt-regression-case-model'), '仅大小写不同的小写自定义模型应进入模型目录')
  assert(publicModels.has('GPT-regression-case-model'), '仅大小写不同的大写自定义模型应进入模型目录')
  assert(publicModels.has('gpt-regression-audio'), '只有音频价格的自定义模型应进入公开模型目录')
  assert(publicModels.has('gpt-regression-image-unit'), '只有按张图片价格的自定义模型应进入公开模型目录')
  assert.equal(publicModels.has('gpt-regression-draft'), false, '草稿模型不应进入公开模型目录')
  assert.equal(publicModels.has('gpt-regression-overridden-pricing-alias'), false, 'pricingModel 目标被无价自定义模型覆盖时别名不应进入公开模型目录')
  assert.equal(publicModels.has('openai-regression-personal'), false, 'GPT 模型目录不应反向包含 OpenAI 兼容自定义模型')

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

  assert.throws(() => catalogService.saveCustomProviderModel({
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
  }), /默认思考级别必须属于支持的思考级别/)

  assert.throws(() => customProviderModelsRepository.upsertCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-image-invalid-capabilities',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    mode: 'image',
    supportedServiceTiers: ['priority'],
    outputUsdPerImage: 0.02,
    actorSystemAccountId: 'sys_admin'
  }), /只有 GPT 文本自定义模型支持服务等级和思考能力配置/)
  assert.throws(() => customProviderModelsRepository.upsertCustomProviderModel({
    providerCode: 'gpt',
    model: 'gpt-regression-audio-invalid-capabilities',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    mode: 'audio',
    supportedReasoningEfforts: ['high'],
    defaultReasoningEffort: 'high',
    audioInputUsdPer1M: 1,
    audioOutputUsdPer1M: 2,
    actorSystemAccountId: 'sys_admin'
  }), /只有 GPT 文本自定义模型支持服务等级和思考能力配置/)
  assert.throws(() => customProviderModelsRepository.upsertCustomProviderModel({
    providerCode: 'openai',
    model: 'openai-regression-invalid-capabilities',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedReasoningEfforts: ['high'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: 'sys_admin'
  }), /只有 GPT 文本自定义模型支持服务等级和思考能力配置/)

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

  const dedupedProviderModelOptions = dedupeProviderModelOptions([
    {
      providerCode: 'gpt',
      model: 'shared-model',
      supportedApiProtocols: ['chat_completions'],
      supportedServiceTiers: ['priority', 'priority'],
      supportedReasoningEfforts: ['low'],
      defaultReasoningEffort: 'medium'
    },
    { providerCode: 'gpt', model: 'Shared-Model', supportedApiProtocols: ['responses'] },
    { providerCode: 'deepseek', model: 'shared-model', supportedApiProtocols: ['chat_completions'] },
    {
      providerCode: 'gpt',
      model: 'shared-model',
      supportedApiProtocols: ['responses'],
      supportedServiceTiers: ['flex', 'priority'],
      supportedReasoningEfforts: ['high'],
      defaultReasoningEffort: 'max'
    },
    {
      providerCode: ' GPT ',
      model: ' shared-model ',
      supportedReasoningEfforts: ['max', 'high'],
      defaultReasoningEffort: 'low'
    },
    { providerCode: '', model: 'shared-model' },
    { providerCode: 'glm', model: ' ' }
  ])
  assert.deepEqual(dedupedProviderModelOptions, [
    {
      providerCode: 'gpt',
      model: 'shared-model',
      supportedApiProtocols: ['chat_completions', 'responses'],
      supportedServiceTiers: ['priority', 'flex'],
      supportedReasoningEfforts: ['low', 'high', 'max'],
      defaultReasoningEffort: 'max'
    },
    { providerCode: 'gpt', model: 'Shared-Model', supportedApiProtocols: ['responses'] },
    { providerCode: 'deepseek', model: 'shared-model', supportedApiProtocols: ['chat_completions'] }
  ], '供应商模型选项必须稳定地按 providerCode + 大小写敏感 model 去重，并合并能力和选择合并后有效的默认思考级别')

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
  assert.equal(codexPersonalModel.default_reasoning_level, 'medium', 'Codex /models 默认 reasoning 应为 medium')
  assert.deepEqual(codexPersonalModel.supported_reasoning_levels, [], '能力未知的自定义模型不能伪造统一 reasoning 选项')
  assert.deepEqual(codexPersonalModel.service_tiers, [], '能力未知的自定义模型不能伪造服务等级')
  assert.equal(typeof codexPersonalModel.base_instructions, 'string', 'Codex /models 必须提供 base_instructions')
  assert.equal(codexPersonalModel.truncation_policy.mode, 'bytes', 'Codex /models 必须提供 truncation_policy')
  assert(codexResponse.models.some((item) => item.slug === 'gpt-regression-upstream-target'), 'Codex /models 应包含启用且可计价的自定义上游目标模型')
  const codexSol = codexResponse.models.find((item) => item.slug === 'gpt-5.6-sol')
  const codexTerra = codexResponse.models.find((item) => item.slug === 'gpt-5.6-terra')
  const codexLuna = codexResponse.models.find((item) => item.slug === 'gpt-5.6-luna')
  assert(codexSol && codexTerra && codexLuna, 'Codex /models 必须包含 GPT-5.6 三个模型')
  assert.deepEqual(codexSol.supported_reasoning_levels.map((item) => item.effort), [...gpt56CodexReasoning, 'ultra'])
  assert.deepEqual(codexTerra.supported_reasoning_levels.map((item) => item.effort), [...gpt56CodexReasoning, 'ultra'])
  assert.deepEqual(codexLuna.supported_reasoning_levels.map((item) => item.effort), gpt56CodexReasoning)
  assert.deepEqual(codexSol.additional_speed_tiers, ['fast'])
  assert.deepEqual(codexTerra.additional_speed_tiers, ['fast'])
  assert.deepEqual(codexLuna.additional_speed_tiers, ['fast'])
  assert.deepEqual(codexSol.service_tiers.map((item) => item.id), ['priority', 'flex'])
  assert.equal(codexSol.multi_agent_version, 'v2')
  assert.equal(codexTerra.multi_agent_version, 'v2')
  assert.equal(codexLuna.multi_agent_version, null)

  const aliasCost = catalogService.estimateCatalogCostUsd({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-alias',
    inputTokens: 1_000_000,
    outputTokens: 1_000_000
  })
  assert.equal(aliasCost, 12, 'pricingModel 应按目标模型直接价格计费')
  const aliasCostAsync = await catalogService.estimateCatalogCostUsdAsync({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-alias',
    inputTokens: 1_000_000,
    outputTokens: 1_000_000
  })
  assert.equal(aliasCostAsync, aliasCost, '异步 pricingModel 成本估算应与同步目录一致')
  const aliasPricingModelAsync = await catalogService.resolveCatalogPricingModelAsync({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-alias'
  })
  assert.equal(aliasPricingModelAsync, 'gpt-regression-upstream-target', '异步 pricingModel 解析应指向目标计价模型')
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
  const audioBreakdownAsync = await catalogService.buildCatalogCostBreakdownAsync({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-audio',
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

  catalogService.saveCustomProviderModel({
    id: pricedModel.id,
    providerCode: 'gpt',
    model: 'gpt-regression-personal',
    scope: 'personal',
    systemAccountId: 'sys_admin',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: null,
    outputUsdPer1M: null,
    pricingModel: 'gpt-regression-upstream-target',
    actorSystemAccountId: 'sys_admin'
  })
  const remappedCost = catalogService.estimateCatalogCostUsd({
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    model: 'gpt-regression-personal',
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
        model: 'gpt-http-forbidden-scope',
        scope: 'global',
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      },
      403,
      '普通用户不应创建全局模型'
    )

    const userAModel = await postEnvelope<{ id: string; model: string; scope: string }>(
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
      supportedServiceTiers: string[]
      supportedReasoningEfforts: string[]
      defaultReasoningEffort?: string
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
    assert.equal(userAGptModel.defaultReasoningEffort, 'high', 'GPT 自定义模型 API 应返回默认思考级别')
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/gpt/models`,
      userACookie,
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
      userACookie,
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
      'GPT 音频自定义模型 API 必须拒绝非空思考能力'
    )
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models`,
      userACookie,
      'POST',
      {
        model: 'openai-http-invalid-capabilities',
        supportedApiProtocols: ['responses'],
        supportedReasoningEfforts: ['high'],
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      },
      400,
      '非 GPT 自定义模型 API 必须拒绝非空服务等级或思考能力'
    )
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/gpt/models`,
      userACookie,
      'POST',
      {
        model: 'gpt-http-invalid-reasoning-default',
        supportedApiProtocols: ['responses'],
        supportedReasoningEfforts: ['low'],
        defaultReasoningEffort: 'high',
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      },
      400,
      'GPT 自定义模型 API 必须拒绝不属于支持集合的默认思考级别'
    )
    const userAGptClearableModel = await postEnvelope<{ id: string; model: string }>(
      baseUrl,
      '/__aisys__/api/providers/gpt/models',
      userACookie,
      {
        model: 'gpt-http-clear-capabilities',
        mode: 'text',
        supportedApiProtocols: ['responses'],
        supportedServiceTiers: ['priority'],
        supportedReasoningEfforts: ['low', 'high'],
        defaultReasoningEffort: 'high',
        inputUsdPer1M: 1,
        outputUsdPer1M: 2
      }
    )
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/gpt/models/${userAGptClearableModel.id}`,
      userACookie,
      'PATCH',
      { mode: 'image' },
      400,
      'GPT 文本模型保留非空能力字段时不应直接切换为图片模式'
    )
    const userAGptClearedModel = await patchEnvelope<{
      mode?: string
      supportedServiceTiers: string[]
      supportedReasoningEfforts: string[]
      defaultReasoningEffort?: string
    }>(
      baseUrl,
      `/__aisys__/api/providers/gpt/models/${userAGptClearableModel.id}`,
      userACookie,
      {
        mode: 'image',
        supportedApiProtocols: ['images'],
        supportedServiceTiers: [],
        supportedReasoningEfforts: [],
        defaultReasoningEffort: null
      }
    )
    assert.equal(userAGptClearedModel.mode, 'image', 'API 显式清空文本能力后应允许切换为 GPT 图片模型')
    assert.deepEqual(userAGptClearedModel.supportedServiceTiers, [], 'API 应接受空数组清理服务等级能力')
    assert.deepEqual(userAGptClearedModel.supportedReasoningEfforts, [], 'API 应接受空数组清理思考能力')
    assert.equal(userAGptClearedModel.defaultReasoningEffort, undefined, 'API 应接受 null 清理默认思考级别')

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
    const userAProviderOptions = await getEnvelope<Array<{ code: string; defaultHealthCheckModel: string; systemDefaultHealthCheckModel?: string }>>(baseUrl, '/__aisys__/api/providers/options', userACookie)
    assert.equal(userAProviderOptions.find((item) => item.code === 'openai')?.defaultHealthCheckModel, userADeletableModel.model, '用户默认检查模型应覆盖自己的供应商选项')
    assert.notEqual(userAProviderOptions.find((item) => item.code === 'openai')?.systemDefaultHealthCheckModel, userADeletableModel.model, '用户默认检查模型不能覆盖管理员系统默认事实')
    const userBProviderOptions = await getEnvelope<Array<{ code: string; defaultHealthCheckModel: string }>>(baseUrl, '/__aisys__/api/providers/options', userBCookie)
    assert.notEqual(userBProviderOptions.find((item) => item.code === 'openai')?.defaultHealthCheckModel, userADeletableModel.model, '用户默认检查模型不能泄露给其他用户')
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/default-health-check-model`,
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

    const adminPersonalModel = await postEnvelope<{ id: string; model: string; scope: string }>(
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
      `${baseUrl}/__aisys__/api/providers/openai/default-health-check-model`,
      adminCookie,
      'PUT',
      { model: adminPersonalModel.model },
      400,
      '管理员自己的个人模型不能设置为系统默认检查模型'
    )

    const adminSystemDefault = await putEnvelope<{ providerCode: string; defaultHealthCheckModel: string }>(
      baseUrl,
      '/__aisys__/api/providers/openai/default-health-check-model',
      adminCookie,
      { model: adminGlobalModel.model }
    )
    assert.equal(adminSystemDefault.defaultHealthCheckModel, adminGlobalModel.model, '管理员应能把全局启用文本模型设置为系统默认检查模型')

    const userAOptionsAfterSystemDefault = await getEnvelope<Array<{
      code: string
      defaultHealthCheckModel: string
      systemDefaultHealthCheckModel?: string
    }>>(baseUrl, '/__aisys__/api/providers/options', userACookie)
    const userAOpenAIOptions = userAOptionsAfterSystemDefault.find((item) => item.code === 'openai')
    assert.equal(userAOpenAIOptions?.defaultHealthCheckModel, userADeletableModel.model, '用户 A 已有个人偏好时应继续以个人检查模型为默认')
    assert.equal(userAOpenAIOptions?.systemDefaultHealthCheckModel, adminGlobalModel.model, '用户 A 仍应看到管理员配置的系统默认检查模型')

    const userBOptionsAfterSystemDefault = await getEnvelope<Array<{
      code: string
      defaultHealthCheckModel: string
      systemDefaultHealthCheckModel?: string
    }>>(baseUrl, '/__aisys__/api/providers/options', userBCookie)
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
      { maxOutputTokens: 4096 }
    )
    const adminUpdatedUserAGptModel = await patchEnvelope<{ model: string; maxOutputTokens?: number }>(
      baseUrl,
      `/__aisys__/api/providers/gpt/models/${userAGptModel.id}`,
      adminCookie,
      { maxOutputTokens: 3072 }
    )
    assert.equal(adminUpdatedUserAGptModel.maxOutputTokens, 3072, '管理员应能维护目标用户个人模型')
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
    const userAProviderOptionsAfterDefaultDelete = await getEnvelope<Array<{ code: string; defaultHealthCheckModel: string }>>(baseUrl, '/__aisys__/api/providers/options', userACookie)
    assert.notEqual(userAProviderOptionsAfterDefaultDelete.find((item) => item.code === 'openai')?.defaultHealthCheckModel, userADeletableModel.model, '删除个人模型时应清理指向该模型的个人默认检查模型')
    await assertHttpStatus(
      `${baseUrl}/__aisys__/api/providers/openai/models/${adminPersonalModel.id}`,
      userACookie,
      'PATCH',
      { maxOutputTokens: 777 },
      403,
      '普通用户不应修改他人的个人自定义模型'
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
    assert(userAVisible.some((item) => item.model === 'gpt-http-user-a-case' && item.scope === 'personal'), '用户应能看到小写大小写变体个人模型')
    assert(userAVisible.some((item) => item.model === 'GPT-http-user-a-case' && item.scope === 'personal'), '用户应能看到大写大小写变体个人模型')
    assert(userAVisible.some((item) => item.model === 'gpt-http-admin-global' && item.scope === 'global'), '用户应能看到管理员创建的全局模型')
    assert(userAVisible.some((item) => item.model === 'gpt-http-admin-created-user-a' && item.scope === 'personal'), '用户应能看到管理员代建的个人模型')
    assert(userAVisible.some((item) => item.model === 'gpt-http-user-a-gpt' && item.providerCode === 'gpt'), 'OpenAI 聚合目录应包含 GPT 目录的个人自定义模型')
    assert.equal(userAVisible.some((item) => item.model === 'gpt-http-admin-personal'), false, '用户不应看到管理员个人模型')
    assert.equal(userAVisible.some((item) => item.model === 'gpt-http-user-a-draft'), false, '默认管理模型目录不应返回草稿模型')

    const userAGlobalModelOptions = await getEnvelope<Array<{
      providerCode: string
      model: string
      supportedApiProtocols?: string[]
      supportedServiceTiers?: string[]
      supportedReasoningEfforts?: string[]
      defaultReasoningEffort?: string
    }>>(
      baseUrl,
      '/__aisys__/api/providers/models/options',
      userACookie
    )
    assert(userAGlobalModelOptions.some((item) => item.providerCode === 'gpt' && item.model === 'gpt-http-user-a-gpt'), '全局模型选项应包含真实供应商模型')
    assert(userAGlobalModelOptions.some((item) => item.providerCode === 'openai' && item.model === 'gpt-http-admin-global'), '模型选项应包含管理员全局模型')
    const userAGptGlobalOption = userAGlobalModelOptions.find((item) => item.providerCode === 'gpt' && item.model === 'gpt-http-user-a-gpt')
    assert(userAGptGlobalOption?.supportedApiProtocols?.includes('responses'), '全局模型选项必须返回模型协议能力，供账号模型别名按协议过滤')
    assert.deepEqual(userAGptGlobalOption?.supportedServiceTiers, ['priority'], '全局模型选项必须返回服务等级能力')
    assert.deepEqual(userAGptGlobalOption?.supportedReasoningEfforts, ['low', 'high'], '全局模型选项必须返回思考能力')
    assert.equal(userAGptGlobalOption?.defaultReasoningEffort, 'high', '全局模型选项必须返回有效默认思考级别')
    assert.equal(userAGlobalModelOptions.some((item) => item.providerCode === 'hybrid'), false, '全局模型选项不应把 hybrid 当作真实供应商目录')
    assert.equal(userAGlobalModelOptions.some((item) => item.model === 'hybrid-regression-should-not-list'), false, '全局模型选项不应返回 hybrid 自身模型')

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
    const userAProviderOptionsAfterHybridDefault = await getEnvelope<Array<{ code: string; defaultHealthCheckModel: string }>>(baseUrl, '/__aisys__/api/providers/options', userACookie)
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
    const adminUserAModelOptions = await getEnvelope<Array<{ providerCode: string; model: string }>>(
      baseUrl,
      `/__aisys__/api/providers/models/options?systemAccountId=${encodeURIComponent(userA.id)}`,
      adminCookie
    )
    assert(adminUserAModelOptions.some((item) => item.providerCode === 'gpt' && item.model === 'gpt-http-user-a-gpt'), '管理员维护目标用户账号时模型选项应包含目标用户真实供应商个人模型')
    assert(adminUserAModelOptions.some((item) => item.providerCode === 'openai' && item.model === 'gpt-http-admin-global'), '管理员维护目标用户账号时模型选项应包含全局模型')
    assert.equal(adminUserAModelOptions.some((item) => item.model === 'gpt-http-admin-personal'), false, '管理员维护目标用户账号时模型选项不应混入管理员自己的个人模型')

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
