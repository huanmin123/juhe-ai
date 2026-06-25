import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import type { ApiKeyHybridRoutingConfig, ProviderCode } from '../../domain/types.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'

interface HybridMockHit {
  authorization: string
  bodyText: string
  marker: string
  method: string
  model: string
  path: string
  rawUrl: string
}

interface HybridMockCase {
  marker: string
  level: number
  expectedModel: string
}

const scoringModel = 'glm-5.2-flash'
const unavailableQualityModel = 'quality-model-unavailable'
const deepseekModel = 'deepseek-ai-v4-flash'
const glmModel = 'glm-5.2'
const gptModel = 'gpt-5.5'
const opusModel = 'claude-opus-4-8'
const scoringFallbackMaxLevel = 5
const mockDefaultLevel = 7
const bulkExperimentCount = 120

const levelRoutes: ApiKeyHybridRoutingConfig['levelRoutes'] = [
  { minLevel: 1, maxLevel: 2, targetModel: deepseekModel, enabled: true },
  { minLevel: 3, maxLevel: 6, targetModel: glmModel, enabled: true },
  { minLevel: 7, maxLevel: 8, targetModel: gptModel, enabled: true },
  { minLevel: 9, maxLevel: 10, targetModel: opusModel, enabled: true }
]

const basicCases: HybridMockCase[] = [
  { marker: 'HYBRID_LEVEL_2_SIMPLE', level: 2, expectedModel: deepseekModel },
  { marker: 'HYBRID_LEVEL_5_NORMAL', level: 5, expectedModel: glmModel },
  { marker: 'HYBRID_LEVEL_8_COMPLEX', level: 8, expectedModel: gptModel },
  { marker: 'HYBRID_LEVEL_10_FRONTIER', level: 10, expectedModel: opusModel }
]

const tempRoot = resolve(tmpdir(), `juhe-ai-hybrid-gateway-mock-ai-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'hybrid-gateway-mock-ai.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'hybrid-gateway-mock-ai-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue,
  hybridAffinity,
  hybridScoring
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/gateway/hybrid/affinity.service.js'),
  import('../../modules/gateway/hybrid/scoring.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const upstreamHits: HybridMockHit[] = []
let scoringFailoverFailureConsumed = false

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  repositories.updateSettings({
    temporaryUnschedulableRetryAttempts: 0,
    temporaryUnschedulableRetryIntervalSeconds: 0
  })
  gatewayCache.clearGatewayRuntimeCache()
  hybridAffinity.clearHybridRouteAffinityForTest()
  hybridScoring.clearHybridScoringCacheForTest()
  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    upstreamServer = createHybridMockUpstream()
    await listen(upstreamServer)
    const upstreamRootUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}`
    const upstreamBaseUrl = `${upstreamRootUrl}/v1`

    registerHybridCustomModels()

    const scoring = createHybridGroupAccount({
      groupName: 'Hybrid Mock 评分分组',
      accountName: 'Hybrid Mock GLM 评分主账户',
      providerCode: GLM_PROVIDER_CODE,
      providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
      upstreamApiKey: 'sk-hybrid-scoring-primary',
      baseUrl: upstreamRootUrl,
      supportedModel: scoringModel
    })
    createHybridAdditionalAccount({
      groupId: scoring.groupId,
      accountName: 'Hybrid Mock GLM 评分备用账户',
      providerCode: GLM_PROVIDER_CODE,
      providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
      upstreamApiKey: 'sk-hybrid-scoring-backup',
      baseUrl: upstreamRootUrl,
      supportedModel: scoringModel
    })
    const deepseek = createHybridGroupAccount({
      groupName: 'Hybrid Mock DeepSeek 低价分组',
      accountName: 'Hybrid Mock DeepSeek 低价账户',
      providerCode: DEEPSEEK_PROVIDER_CODE,
      providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      upstreamApiKey: 'sk-hybrid-deepseek',
      baseUrl: upstreamBaseUrl,
      supportedModel: deepseekModel
    })
    const glm = createHybridGroupAccount({
      groupName: 'Hybrid Mock GLM 中低价分组',
      accountName: 'Hybrid Mock GLM 中低价账户',
      providerCode: GLM_PROVIDER_CODE,
      providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
      upstreamApiKey: 'sk-hybrid-glm',
      baseUrl: upstreamRootUrl,
      supportedModel: glmModel
    })
    const gpt = createHybridGroupAccount({
      groupName: 'Hybrid Mock GPT 中高价分组',
      accountName: 'Hybrid Mock GPT 中高价账户',
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      upstreamApiKey: 'sk-hybrid-gpt',
      baseUrl: upstreamBaseUrl,
      supportedModel: gptModel
    })
    const opus = createHybridGroupAccount({
      groupName: 'Hybrid Mock Opus 高价分组',
      accountName: 'Hybrid Mock Opus 高价账户',
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      upstreamApiKey: 'sk-hybrid-opus',
      baseUrl: upstreamBaseUrl,
      supportedModel: opusModel
    })

    const apiKey = repositories.createApiKeyRecord({
      name: 'Hybrid Mock 智能路由 Key',
      routeMode: 'hybrid',
      groupRouteStrategy: 'priority_failover',
      groupBindings: [scoring, deepseek, glm, gpt, opus].map((item, index) => ({
        groupId: item.groupId,
        priority: index + 1,
        weight: 1,
        status: 'active'
      })),
      hybridRoutingConfig: {
        scoringModel,
        scoringContextMode: 'full_request',
        qualityPreference: 'balanced',
        scoringTimeoutMs: 10_000,
        scoringFallbackMaxLevel,
        scoringCacheEnabled: true,
        scoringCacheTtlSeconds: 300,
        cacheAffinityEnabled: true,
        affinityTtlSeconds: 900,
        switchMinLevelDelta: 2,
        downgradeConsecutiveLowCount: 2,
        levelRoutes
      } satisfies ApiKeyHybridRoutingConfig,
      status: 'active'
    }, access)
    assert(apiKey.key, '混合路由回归 API Key 未返回明文密钥')
    assert.equal(apiKey.routeMode, 'hybrid')
    assert.equal(apiKey.hybridRoutingConfig?.scoringGroupId, undefined)
    assert.equal(apiKey.hybridRoutingConfig?.qualityInspection?.enabled, true)
    assert.equal(apiKey.hybridRoutingConfig?.qualityInspection?.scoringGroupId, undefined)
    assert.equal(apiKey.hybridRoutingConfig?.qualityInspection?.scoringModel, scoringModel)
    assert.equal(apiKey.hybridRoutingConfig?.qualityInspection?.triggerMode, 'risk_based')
    const qualityApiKey = repositories.createApiKeyRecord({
      name: 'Hybrid Mock 质量评分 Key',
      routeMode: 'hybrid',
      groupRouteStrategy: 'priority_failover',
      groupBindings: [scoring, deepseek, glm, gpt, opus].map((item, index) => ({
        groupId: item.groupId,
        priority: index + 1,
        weight: 1,
        status: 'active'
      })),
      hybridRoutingConfig: {
        scoringModel,
        scoringContextMode: 'full_request',
        qualityPreference: 'balanced',
        scoringTimeoutMs: 10_000,
        scoringFallbackMaxLevel,
        scoringCacheEnabled: true,
        scoringCacheTtlSeconds: 300,
        cacheAffinityEnabled: true,
        affinityTtlSeconds: 900,
        switchMinLevelDelta: 2,
        downgradeConsecutiveLowCount: 2,
        qualityInspection: {
          enabled: true,
          scoringModel,
          triggerMode: 'always_for_hybrid',
          maxTriggerLevel: 6,
          maxRetries: 2,
          failureAction: 'repair_then_upgrade',
          unavailableAction: 'pass_through'
        },
        levelRoutes
      } satisfies ApiKeyHybridRoutingConfig,
      status: 'active'
    }, access)
    assert(qualityApiKey.key, '混合质量评分回归 API Key 未返回明文密钥')
    const qualityUnavailableApiKey = repositories.createApiKeyRecord({
      name: 'Hybrid Mock 质量评分不可用 Key',
      routeMode: 'hybrid',
      groupRouteStrategy: 'priority_failover',
      groupBindings: [scoring, glm].map((item, index) => ({
        groupId: item.groupId,
        priority: index + 1,
        weight: 1,
        status: 'active'
      })),
      hybridRoutingConfig: {
        scoringModel,
        scoringContextMode: 'full_request',
        qualityPreference: 'balanced',
        scoringTimeoutMs: 10_000,
        scoringFallbackMaxLevel: 2,
        scoringCacheEnabled: true,
        scoringCacheTtlSeconds: 300,
        cacheAffinityEnabled: true,
        affinityTtlSeconds: 900,
        switchMinLevelDelta: 2,
        downgradeConsecutiveLowCount: 2,
        qualityInspection: {
          enabled: true,
          scoringModel: unavailableQualityModel,
          triggerMode: 'always_for_hybrid',
          maxTriggerLevel: 6,
          maxRetries: 2,
          failureAction: 'repair_then_upgrade',
          unavailableAction: 'pass_through'
        },
        levelRoutes
      } satisfies ApiKeyHybridRoutingConfig,
      status: 'active'
    }, access)
    assert(qualityUnavailableApiKey.key, '混合质量评分不可用回归 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    for (const item of basicCases) {
      await assertHybridRequest({
        baseUrl,
        localApiKey: apiKey.key,
        marker: item.marker,
        expectedModel: item.expectedModel,
        expectedQualityScoringHits: item.level <= 6 ? 1 : 0,
        sessionId: `basic-${item.marker}`
      })
    }

    await assertHybridRequest({
      baseUrl,
      localApiKey: apiKey.key,
      marker: 'HYBRID_LEVEL_5_SCORING_FAILOVER',
      expectedModel: glmModel,
      expectedRouteScoringHits: 2,
      expectedDistinctRouteScoringAuthorizations: 2,
      expectedQualityScoringHits: 1,
      sessionId: 'scoring-account-failover'
    })

    await assertHybridRequest({
      baseUrl,
      localApiKey: apiKey.key,
      marker: 'HYBRID_SCORE_INVALID',
      expectedModel: deepseekModel,
      expectedQualityScoringHits: 0,
      sessionId: 'invalid-scoring'
    })
    await assertHybridRequest({
      baseUrl,
      localApiKey: apiKey.key,
      marker: 'HYBRID_LEVEL_8_COMPLEX',
      expectedModel: gptModel,
      expectedQualityScoringHits: 0,
      sessionId: 'affinity-small-delta'
    })
    await assertHybridRequest({
      baseUrl,
      localApiKey: apiKey.key,
      marker: 'HYBRID_LEVEL_6_MEDIUM',
      expectedModel: gptModel,
      expectedQualityScoringHits: 0,
      sessionId: 'affinity-small-delta'
    })

    await assertHybridRequest({
      baseUrl,
      localApiKey: apiKey.key,
      marker: 'HYBRID_LEVEL_10_FRONTIER',
      expectedModel: opusModel,
      expectedQualityScoringHits: 0,
      sessionId: 'affinity-downgrade-confirm'
    })
    await assertHybridRequest({
      baseUrl,
      localApiKey: apiKey.key,
      marker: 'HYBRID_LEVEL_2_SIMPLE',
      expectedModel: opusModel,
      expectedQualityScoringHits: 0,
      sessionId: 'affinity-downgrade-confirm'
    })
    await assertHybridRequest({
      baseUrl,
      localApiKey: apiKey.key,
      marker: 'HYBRID_LEVEL_2_SIMPLE_REPEAT',
      expectedModel: deepseekModel,
      expectedQualityScoringHits: 1,
      sessionId: 'affinity-downgrade-confirm'
    })

    await assertHybridScoringCacheRequest({
      baseUrl,
      localApiKey: apiKey.key,
      marker: 'HYBRID_LEVEL_5_CACHE',
      expectedModel: glmModel,
      sessionId: 'scoring-cache'
    })

    for (let index = 0; index < bulkExperimentCount; index += 1) {
      const item = basicCases[index % basicCases.length]!
      await assertHybridRequest({
        baseUrl,
        localApiKey: apiKey.key,
        marker: `${item.marker}_BULK_${index}`,
        expectedModel: item.expectedModel,
        expectedQualityScoringHits: item.level <= 6 ? 1 : 0,
        sessionId: `bulk-${index}`
      })
    }

    usageRecordQueue.flushAllUsageRecordQueue()
    const scoringUsageCount = scoringUsageRecordCount()
    const expectedRequests = basicCases.length + 1 + 1 + 2 + 3 + 2 + bulkExperimentCount
    const expectedScoringUsageCount = expectedRequests - 1
    assert(scoringUsageCount >= expectedScoringUsageCount, `评分使用记录数量不足，期望至少 ${expectedScoringUsageCount}，实际 ${scoringUsageCount}`)
    assertHybridScoringLargeBodyGuard()
    await assertHybridQualityRepairRequest({
      baseUrl,
      localApiKey: qualityApiKey.key,
      marker: 'HYBRID_LEVEL_5_QUALITY_REPAIR',
      sessionId: 'quality-repair'
    })
    await assertHybridQualityUpgradeRequest({
      baseUrl,
      localApiKey: qualityApiKey.key,
      marker: 'HYBRID_LEVEL_5_QUALITY_FAIL',
      sessionId: 'quality-upgrade'
    })
    await assertHybridRequest({
      baseUrl,
      localApiKey: qualityUnavailableApiKey.key,
      marker: 'HYBRID_LEVEL_5_QUALITY_FAIL',
      expectedModel: glmModel,
      expectedQualityScoringHits: 0,
      sessionId: 'quality-unavailable'
    })
    await assertHybridGatewayFailure({
      baseUrl,
      localApiKey: qualityUnavailableApiKey.key,
      marker: 'HYBRID_SCORE_INVALID_NO_LOW_POOL',
      sessionId: 'quality-unavailable',
      expectedStatus: 503,
      expectedErrorCode: 'hybrid_scoring_fallback_unavailable',
      expectedScoringHits: 1,
      expectedTargetHits: 0
    })
    usageRecordQueue.flushAllUsageRecordQueue()
    const qualityScoringUsageCount = qualityScoringUsageRecordCount()
    assert(qualityScoringUsageCount >= 5, `质量评分使用记录数量不足，期望至少 5，实际 ${qualityScoringUsageCount}`)

    console.log(JSON.stringify({
      ok: true,
      experiments: expectedRequests,
      upstreamHits: upstreamHits.length,
      scoringUsageCount,
      qualityScoringUsageCount,
      routeModels: levelRoutes.map((route) => `${route.minLevel}-${route.maxLevel}:${route.targetModel}`)
    }, null, 2))
  } finally {
    await closeServer(appServer)
    await closeServer(upstreamServer)
  }
} finally {
  hybridAffinity.clearHybridRouteAffinityForTest()
  hybridScoring.clearHybridScoringCacheForTest()
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function registerHybridCustomModels(): void {
  saveHybridCustomModel(GLM_PROVIDER_CODE, scoringModel, 0.001, 0.001)
  saveHybridCustomModel(GLM_PROVIDER_CODE, unavailableQualityModel, 0.001, 0.001)
  saveHybridCustomModel(GLM_PROVIDER_CODE, glmModel, 0.01, 0.01)
  saveHybridCustomModel(GPT_VENDOR_CODE, gptModel, 0.02, 0.02)
  saveHybridCustomModel(OPENAI_COMPATIBLE_PROVIDER_CODE, opusModel, 0.05, 0.05)
}

function saveHybridCustomModel(providerCode: ProviderCode, model: string, inputUsdPer1M: number, outputUsdPer1M: number): void {
  saveCustomProviderModel({
    providerCode,
    model,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M,
    outputUsdPer1M,
    cachedInputUsdPer1M: inputUsdPer1M / 10,
    actorSystemAccountId: access.systemAccountId
  })
}

function assertHybridScoringLargeBodyGuard(): void {
  const source = readFileSync(resolve('src/modules/gateway/hybrid/scoring.service.ts'), 'utf8')
  assert.match(source, /const hybridScoringRawBodyParseMaxBytes = hybridScoringContextMaxBytes/, '混合评分大 body 解析上限应绑定评分上下文上限')
  assert.match(source, /request\.rawBody\.length > hybridScoringRawBodyParseMaxBytes[\s\S]*?return undefined/, '混合评分不应为了评分上下文完整解析超限 raw body')
  assert.match(source, /getGatewayRequestBodyState\(req\)/, '混合评分跳过大 body 解析后应使用网关请求体元数据')
  assert.match(source, /raw_body_exceeds_hybrid_scoring_parse_limit/, '混合评分上下文应标记大 body 省略原因')
}

function createHybridGroupAccount(input: {
  accountName: string
  baseUrl: string
  groupName: string
  providerCode: ProviderCode
  providerProtocolProfileId: string
  supportedModel: string
  upstreamApiKey: string
  clientCompatibility?: 'openai_standard' | 'codex_responses'
}): { accountId: string; groupId: string } {
  const group = repositories.createGroup({
    name: input.groupName,
    providerCode: input.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: input.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    name: input.accountName,
    type: 'api_key',
    clientCompatibility: input.clientCompatibility ?? 'openai_standard',
    credentials: {
      api_key: input.upstreamApiKey,
      base_url: input.baseUrl,
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 16,
    supportedModels: [input.supportedModel]
  }, access)
  assert.deepEqual(account.supportedModels, [input.supportedModel])
  return { accountId: account.id, groupId: group.id }
}

function createHybridAdditionalAccount(input: {
  accountName: string
  baseUrl: string
  groupId: string
  providerCode: ProviderCode
  providerProtocolProfileId: string
  supportedModel: string
  upstreamApiKey: string
  clientCompatibility?: 'openai_standard' | 'codex_responses'
}): string {
  const account = repositories.createAccount({
    providerCode: input.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    name: input.accountName,
    type: 'api_key',
    clientCompatibility: input.clientCompatibility ?? 'openai_standard',
    credentials: {
      api_key: input.upstreamApiKey,
      base_url: input.baseUrl,
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    groupId: input.groupId,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 16,
    supportedModels: [input.supportedModel]
  }, access)
  assert.deepEqual(account.supportedModels, [input.supportedModel])
  return account.id
}

async function assertHybridRequest(input: {
  baseUrl: string
  expectedModel: string
  expectedDistinctRouteScoringAuthorizations?: number
  expectedQualityScoringHits?: number
  expectedRouteScoringHits?: number
  localApiKey: string
  marker: string
  sessionId: string
}): Promise<HybridMockHit> {
  const start = upstreamHits.length
  const response = await fetch(`${input.baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${input.localApiKey}`,
      'content-type': 'application/json',
      'x-session-id': input.sessionId
    },
    body: JSON.stringify({
      model: 'client-request-model',
      messages: [
        { role: 'system', content: 'hybrid mock regression' },
        { role: 'user', content: input.marker }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `混合路由请求应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { choices?: Array<{ message?: { content?: string } }> }
  assert.equal(body.choices?.[0]?.message?.content, `target:${input.expectedModel}:${input.marker}`)
  const hits = upstreamHits.slice(start)
  const routeScoringHits = hits.filter(isRouteScoringHit)
  const qualityScoringHits = hits.filter(isQualityScoringHit)
  const targetHits = hits.filter(isTargetHit)
  assert.equal(routeScoringHits.length, input.expectedRouteScoringHits ?? 1, `混合请求入口评分模型调用次数错误，实际 ${routeScoringHits.length}`)
  if (input.expectedDistinctRouteScoringAuthorizations !== undefined) {
    assert.equal(
      new Set(routeScoringHits.map((hit) => hit.authorization)).size,
      input.expectedDistinctRouteScoringAuthorizations,
      `混合请求入口评分模型账号切换错误，实际不同上游凭据数 ${new Set(routeScoringHits.map((hit) => hit.authorization)).size}`
    )
  }
  if (input.expectedQualityScoringHits !== undefined) {
    assert.equal(qualityScoringHits.length, input.expectedQualityScoringHits, `混合请求质量评分触发次数错误，实际 ${qualityScoringHits.length}`)
  } else {
    assert(qualityScoringHits.length <= 1, `风险触发质量评分时每次混合请求最多调用一次质量评分模型，实际 ${qualityScoringHits.length}`)
  }
  assert.equal(targetHits.length, 1, `每次混合请求应恰好调用一次目标模型，实际 ${targetHits.length}`)
  assert.equal(routeScoringHits[0]!.path, '/chat/completions', 'GLM 评分账号 baseUrl 不带 /v1 时不应被硬拼成 /v1/chat/completions')
  const targetHit = targetHits[0]!
  assert.equal(targetHit.model, input.expectedModel, `目标模型路由错误：${input.marker}`)
  assert(routeScoringHits[0]!.bodyText.includes(input.marker), '评分上下文应包含原始请求内容')
  return targetHit
}

async function assertHybridGatewayFailure(input: {
  baseUrl: string
  localApiKey: string
  marker: string
  sessionId: string
  expectedStatus: number
  expectedErrorCode: string
  expectedScoringHits: number
  expectedTargetHits: number
}): Promise<void> {
  const start = upstreamHits.length
  const response = await fetch(`${input.baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${input.localApiKey}`,
      'content-type': 'application/json',
      'x-session-id': input.sessionId
    },
    body: JSON.stringify({
      model: 'client-request-model',
      messages: [
        { role: 'system', content: 'hybrid mock regression' },
        { role: 'user', content: input.marker }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, input.expectedStatus, `混合路由失败请求应返回 HTTP ${input.expectedStatus}，实际 HTTP ${response.status}: ${text}`)
  assert(text.includes(input.expectedErrorCode), `混合路由失败响应应包含错误码 ${input.expectedErrorCode}：${text}`)
  const hits = upstreamHits.slice(start)
  const routeScoringHits = hits.filter(isRouteScoringHit)
  const qualityScoringHits = hits.filter(isQualityScoringHit)
  const targetHits = hits.filter(isTargetHit)
  assert.equal(routeScoringHits.length, input.expectedScoringHits, `混合路由失败请求的评分模型调用次数错误，实际 ${routeScoringHits.length}`)
  assert.equal(qualityScoringHits.length, 0, `失败请求不应调用质量评分模型，实际 ${qualityScoringHits.length}`)
  assert.equal(targetHits.length, input.expectedTargetHits, `混合路由失败请求的目标模型调用次数错误，实际 ${targetHits.length}`)
}

async function assertHybridQualityUpgradeRequest(input: {
  baseUrl: string
  localApiKey: string
  marker: string
  sessionId: string
}): Promise<void> {
  const start = upstreamHits.length
  const response = await fetch(`${input.baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${input.localApiKey}`,
      'content-type': 'application/json',
      'x-session-id': input.sessionId
    },
    body: JSON.stringify({
      model: 'client-request-model',
      messages: [
        { role: 'system', content: 'hybrid quality mock regression' },
        { role: 'user', content: input.marker }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `质量评分升级请求应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { choices?: Array<{ message?: { content?: string } }> }
  assert.equal(body.choices?.[0]?.message?.content, `target:${gptModel}:${input.marker}`)
  const hits = upstreamHits.slice(start)
  const routeScoringHits = hits.filter(isRouteScoringHit)
  const qualityScoringHits = hits.filter(isQualityScoringHit)
  const targetHits = hits.filter(isTargetHit)
  assert.equal(routeScoringHits.length, 1, `质量升级请求应只做一次入口评分，实际 ${routeScoringHits.length}`)
  assert.equal(qualityScoringHits.length, 3, `质量升级请求应做原模型失败、同档修复失败和升级通过三次质量评分，实际 ${qualityScoringHits.length}`)
  assert.equal(targetHits.length, 3, `质量升级请求应产生原模型、同档修复和升级模型三次目标请求，实际 ${targetHits.length}`)
  assert.equal(targetHits[0]!.model, glmModel, '质量失败前应先落到 4-6 档 GLM')
  assert.equal(targetHits[1]!.model, glmModel, '第一次质量失败后应先同档修复 GLM')
  assert(targetHits[1]!.bodyText.includes('上一次回答没有通过混合路由质量评分'), '同档修复请求应携带质量评分反馈')
  assert.equal(targetHits[2]!.model, gptModel, '同档修复仍失败后应升级到下一档 GPT')
}

async function assertHybridQualityRepairRequest(input: {
  baseUrl: string
  localApiKey: string
  marker: string
  sessionId: string
}): Promise<void> {
  const start = upstreamHits.length
  const response = await fetch(`${input.baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${input.localApiKey}`,
      'content-type': 'application/json',
      'x-session-id': input.sessionId
    },
    body: JSON.stringify({
      model: 'client-request-model',
      messages: [
        { role: 'system', content: 'hybrid quality repair mock regression' },
        { role: 'user', content: input.marker }
      ],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `质量评分同档修复请求应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { choices?: Array<{ message?: { content?: string } }> }
  assert.equal(body.choices?.[0]?.message?.content, `target:${glmModel}:${input.marker}`)
  const hits = upstreamHits.slice(start)
  const routeScoringHits = hits.filter(isRouteScoringHit)
  const qualityScoringHits = hits.filter(isQualityScoringHit)
  const targetHits = hits.filter(isTargetHit)
  assert.equal(routeScoringHits.length, 1, `质量同档修复请求应只做一次入口评分，实际 ${routeScoringHits.length}`)
  assert.equal(qualityScoringHits.length, 2, `质量同档修复请求应做失败和修复通过两次质量评分，实际 ${qualityScoringHits.length}`)
  assert.equal(targetHits.length, 2, `质量同档修复请求应产生原模型和同档修复两次目标请求，实际 ${targetHits.length}`)
  assert.equal(targetHits[0]!.model, glmModel, '质量失败前应先落到 4-6 档 GLM')
  assert.equal(targetHits[1]!.model, glmModel, '质量失败后应先同档修复 GLM')
  assert(targetHits[1]!.bodyText.includes('上一次回答没有通过混合路由质量评分'), '同档修复请求应携带质量评分反馈')
}

async function assertHybridScoringCacheRequest(input: {
  baseUrl: string
  expectedModel: string
  localApiKey: string
  marker: string
  sessionId: string
}): Promise<void> {
  const start = upstreamHits.length
  for (let index = 0; index < 2; index += 1) {
    const response = await fetch(`${input.baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${input.localApiKey}`,
        'content-type': 'application/json',
        'x-session-id': input.sessionId,
        'x-client-request-id': `${input.sessionId}-trace-${index}`
      },
      body: JSON.stringify({
        model: 'client-request-model',
        messages: [
          { role: 'system', content: 'hybrid mock regression' },
          { role: 'user', content: input.marker }
        ],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 200, `评分缓存混合请求应成功，实际 HTTP ${response.status}: ${text}`)
    const body = JSON.parse(text) as { choices?: Array<{ message?: { content?: string } }> }
    assert.equal(body.choices?.[0]?.message?.content, `target:${input.expectedModel}:${input.marker}`)
  }
  const hits = upstreamHits.slice(start)
  const routeScoringHits = hits.filter(isRouteScoringHit)
  const targetHits = hits.filter(isTargetHit)
  assert.equal(routeScoringHits.length, 1, `完全相同请求即使追踪 ID 不同，短 TTL 内也应只调用一次入口评分模型，实际 ${routeScoringHits.length}`)
  assert.equal(targetHits.length, 2, `评分缓存只应省评分调用，目标请求仍应执行两次，实际 ${targetHits.length}`)
  assert(targetHits.every((hit) => hit.model === input.expectedModel), '评分缓存命中后目标模型应保持一致')
}

function isRouteScoringHit(hit: HybridMockHit): boolean {
  return hit.model === scoringModel && !hit.bodyText.includes('响应质量评分器')
}

function isQualityScoringHit(hit: HybridMockHit): boolean {
  return hit.model === scoringModel && hit.bodyText.includes('响应质量评分器')
}

function isTargetHit(hit: HybridMockHit): boolean {
  return !isRouteScoringHit(hit) && !isQualityScoringHit(hit)
}

function scoringUsageRecordCount(): number {
  return usageTrafficSourceRecordCount('hybrid_scoring')
}

function qualityScoringUsageRecordCount(): number {
  return usageTrafficSourceRecordCount('hybrid_quality_scoring')
}

function usageTrafficSourceRecordCount(trafficSource: string): number {
  const database = databaseModule.getUsageCatalogDatabase()
  const tableName = sqliteTableExists('usage_record_shard_entries')
    ? 'usage_record_shard_entries'
    : sqliteTableExists('usage_records')
      ? 'usage_records'
      : undefined
  if (!tableName) return 0
  const row = database
    .prepare(`SELECT COUNT(*) AS count FROM ${tableName} WHERE traffic_source = ?`)
    .get(trafficSource) as unknown as { count: number } | undefined
  return Number(row?.count ?? 0)
}

function sqliteTableExists(tableName: string): boolean {
  const row = databaseModule.getUsageCatalogDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?")
    .get(tableName) as unknown as { name?: string } | undefined
  return Boolean(row?.name)
}

function createHybridMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const body = safeJsonObject(bodyText)
      const model = typeof body.model === 'string' ? body.model : ''
      const marker = markerForBodyText(bodyText)
      upstreamHits.push({
        authorization: String(req.headers.authorization ?? ''),
        bodyText,
        marker,
        method: req.method ?? '',
        model,
        path: (req.url ?? '').split('?')[0] ?? '',
        rawUrl: req.url ?? ''
      })
      if (model === scoringModel) {
        if (
          marker === 'HYBRID_LEVEL_5_SCORING_FAILOVER'
          && !scoringFailoverFailureConsumed
        ) {
          scoringFailoverFailureConsumed = true
          writeJson(res, {
            error: {
              message: 'mock scoring account failed once',
              type: 'server_error',
              code: 'mock_scoring_account_failed_once'
            }
          }, 500)
          return
        }
        writeJson(res, bodyText.includes('响应质量评分器')
          ? qualityResponseBody(bodyText)
          : scoringResponseBody(bodyText))
        return
      }
      if (body.stream === true) {
        writeChatSse(res, `target:${model}:${marker}`)
        return
      }
      writeJson(res, {
        id: `chatcmpl-hybrid-target-${upstreamHits.length}`,
        object: 'chat.completion',
        choices: [
          {
            index: 0,
            message: { role: 'assistant', content: `target:${model}:${marker}` },
            finish_reason: 'stop'
          }
        ],
        usage: {
          prompt_tokens: 11,
          completion_tokens: 7,
          total_tokens: 18,
          prompt_tokens_details: {
            cached_tokens: marker.includes('BULK') ? 4 : 0
          }
        }
      })
    })
  })
}

function writeChatSse(res: http.ServerResponse, content: string): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  res.write(`data: ${JSON.stringify({
    id: `chatcmpl-hybrid-stream-${upstreamHits.length}`,
    object: 'chat.completion.chunk',
    choices: [
      {
        index: 0,
        delta: { role: 'assistant', content },
        finish_reason: null
      }
    ]
  })}\n\n`)
  res.write(`data: ${JSON.stringify({
    id: `chatcmpl-hybrid-stream-${upstreamHits.length}`,
    object: 'chat.completion.chunk',
    choices: [
      {
        index: 0,
        delta: {},
        finish_reason: 'stop'
      }
    ],
    usage: {
      prompt_tokens: 11,
      completion_tokens: 7,
      total_tokens: 18
    }
  })}\n\n`)
  res.end('data: [DONE]\n\n')
}

function scoringResponseBody(bodyText: string): Record<string, unknown> {
  if (bodyText.includes('HYBRID_SCORE_INVALID')) {
    return openAIChatResponse('not-json', {
      prompt_tokens: 9,
      completion_tokens: 2,
      total_tokens: 11
    })
  }
  const level = levelForBodyText(bodyText)
  return openAIChatResponse(JSON.stringify({ level, confidence: 0.99, reason: 'mock-score' }), {
    prompt_tokens: 9,
    completion_tokens: 4,
    total_tokens: 13,
    prompt_tokens_details: {
      cached_tokens: 3
    }
  })
}

function qualityResponseBody(bodyText: string): Record<string, unknown> {
  const isGlmResponse = bodyText.includes(`target:${glmModel}`)
  const repairInstructionPresent = bodyText.includes('上一次回答没有通过混合路由质量评分')
  const shouldFail = isGlmResponse && (
    bodyText.includes('HYBRID_LEVEL_5_QUALITY_FAIL')
    || (bodyText.includes('HYBRID_LEVEL_5_QUALITY_REPAIR') && !repairInstructionPresent)
  )
  return openAIChatResponse(JSON.stringify({
    pass: !shouldFail,
    score: shouldFail ? 35 : 92,
    confidence: 0.98,
    failureType: shouldFail ? 'missing_required_output' : undefined,
    reason: shouldFail ? 'mock-quality-repair-required' : 'mock-quality-pass',
    retryRecommendation: shouldFail ? 'retry_same_model' : 'accept'
  }), {
    prompt_tokens: 15,
    completion_tokens: 6,
    total_tokens: 21
  })
}

function openAIChatResponse(content: string, usage: Record<string, unknown>): Record<string, unknown> {
  return {
    id: `chatcmpl-hybrid-scoring-${upstreamHits.length}`,
    object: 'chat.completion',
    choices: [
      {
        index: 0,
        message: { role: 'assistant', content },
        finish_reason: 'stop'
      }
    ],
    usage
  }
}

function levelForBodyText(bodyText: string): number {
  const match = bodyText.match(/HYBRID_LEVEL_(10|[1-9])_/)
  if (match?.[1]) return Number(match[1])
  return mockDefaultLevel
}

function markerForBodyText(bodyText: string): string {
  const match = bodyText.match(/HYBRID_[A-Z0-9_]+(?:_BULK_\d+)?/)
  return match?.[0] ?? 'HYBRID_UNKNOWN'
}

function safeJsonObject(text: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(text) as unknown
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
      ? parsed as Record<string, unknown>
      : {}
  } catch {
    return {}
  }
}

function writeJson(res: http.ServerResponse, body: Record<string, unknown>, statusCode = 200): void {
  res.writeHead(statusCode, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify(body))
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  server.listen(0, '127.0.0.1')
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  assert(typeof address === 'object' && address !== null, 'server 未监听端口')
  return { port: address.port }
}

function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server || !server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.close((error) => {
      if (error) rejectPromise(error)
      else resolvePromise()
    })
  })
}
