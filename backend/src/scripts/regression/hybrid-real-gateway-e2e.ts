import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import type { ApiKeyHybridRoutingConfig, ProviderCode } from '../../domain/types.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'

interface RealCase {
  bucket: 'simple' | 'normal' | 'complex' | 'frontier'
  prompt: string
}

interface RealCaseResult {
  bucket: RealCase['bucket']
  durationMs: number
  error?: string
  index: number
  ok: boolean
  responseModel?: string
  status: number
}

interface UsageCountRow {
  traffic_source: string
  model: string | null
  success: number
  count: number
  cost_usd: number | null
}

const realApiKey = requiredEnv('JUHE_REAL_HYBRID_API_KEY', ['HYBRID_REAL_API_KEY'])
const realBaseUrl = envText('JUHE_REAL_HYBRID_BASE_URL', ['HYBRID_REAL_BASE_URL']) || 'https://vsllm.com'
const experimentCount = positiveIntegerEnv('JUHE_REAL_HYBRID_EXPERIMENTS') ?? 120
const requestTimeoutMs = positiveIntegerEnv('JUHE_REAL_HYBRID_REQUEST_TIMEOUT_MS') ?? 180_000
const requestIntervalMs = positiveIntegerEnv('JUHE_REAL_HYBRID_REQUEST_INTERVAL_MS') ?? 500
const requestConcurrency = positiveIntegerEnv('JUHE_REAL_HYBRID_CONCURRENCY') ?? 1
const minSuccessRate = numberEnv('JUHE_REAL_HYBRID_MIN_SUCCESS_RATE') ?? 0.95
const scoringModel = envText('JUHE_REAL_HYBRID_SCORING_MODEL') || 'gpt-5.4-mini'
const deepseekModel = envText('JUHE_REAL_HYBRID_MODEL_1_3') || 'gpt-5.4-mini'
const glmModel = envText('JUHE_REAL_HYBRID_MODEL_4_6') || 'glm-5.2'
const gptModel = envText('JUHE_REAL_HYBRID_MODEL_7_8') || 'gpt-5.5'
const opusModel = envText('JUHE_REAL_HYBRID_MODEL_9_10') || 'claude-opus-4-8'
const scoringUnitCost = numberEnv('JUHE_REAL_HYBRID_SCORING_UNIT_COST') ?? 0.002

const levelRoutes: ApiKeyHybridRoutingConfig['levelRoutes'] = [
  { minLevel: 1, maxLevel: 3, targetModel: deepseekModel, enabled: true },
  { minLevel: 4, maxLevel: 6, targetModel: glmModel, enabled: true },
  { minLevel: 7, maxLevel: 8, targetModel: gptModel, enabled: true },
  { minLevel: 9, maxLevel: 10, targetModel: opusModel, enabled: true }
]

const modelUnitCosts = new Map<string, number>([
  [deepseekModel, numberEnv('JUHE_REAL_HYBRID_COST_1_3') ?? 0.002],
  [glmModel, numberEnv('JUHE_REAL_HYBRID_COST_4_6') ?? 0.01],
  [gptModel, numberEnv('JUHE_REAL_HYBRID_COST_7_8') ?? 0.02],
  [opusModel, numberEnv('JUHE_REAL_HYBRID_COST_9_10') ?? 0.05]
])

const tempRoot = resolve(tmpdir(), `juhe-ai-hybrid-real-gateway-e2e-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'hybrid-real-gateway-e2e.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'hybrid-real-gateway-e2e-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { handleOpenAIGatewayRequest },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue,
  hybridAffinity
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/gateway/hybrid/affinity.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, async (req, res, next) => {
  try {
    await handleOpenAIGatewayRequest(req, res, { exposeUpstreamDiagnostics: true })
  } catch (error) {
    next(error)
  }
})

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()
  hybridAffinity.clearHybridRouteAffinityForTest()
  let appServer: http.Server | undefined
  try {
    registerRealHybridCustomModels()
    const scoring = createRealHybridGroupAccount({
      groupName: 'Hybrid Real 评分分组',
      accountName: 'Hybrid Real 评分账户',
      supportedModel: scoringModel
    })
    const deepseek = createRealHybridGroupAccount({
      groupName: 'Hybrid Real DeepSeek 低价分组',
      accountName: 'Hybrid Real DeepSeek 低价账户',
      supportedModel: deepseekModel
    })
    const glm = createRealHybridGroupAccount({
      groupName: 'Hybrid Real GLM 中低价分组',
      accountName: 'Hybrid Real GLM 中低价账户',
      supportedModel: glmModel
    })
    const gpt = createRealHybridGroupAccount({
      groupName: 'Hybrid Real GPT 中高价分组',
      accountName: 'Hybrid Real GPT 中高价账户',
      supportedModel: gptModel
    })
    const opus = createRealHybridGroupAccount({
      groupName: 'Hybrid Real Opus 高价分组',
      accountName: 'Hybrid Real Opus 高价账户',
      supportedModel: opusModel
    })

    const apiKey = repositories.createApiKeyRecord({
      name: 'Hybrid Real 智能路由 Key',
      routeMode: 'hybrid',
      groupRouteStrategy: 'priority_failover',
      groupBindings: [scoring, deepseek, glm, gpt, opus].map((item, index) => ({
        groupId: item.groupId,
        priority: index + 1,
        weight: 1,
        status: 'active'
      })),
      hybridRoutingConfig: {
        scoringGroupId: scoring.groupId,
        scoringModel,
        scoringContextMode: 'full_request',
        qualityPreference: 'balanced',
        scoringTimeoutMs: 30_000,
        failureDefaultLevel: 7,
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
    assert(apiKey.key, '真实混合路由本地 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`
    const cases = buildRealCases(experimentCount)
    const results = await runRealCases({
      apiKeyId: apiKey.id,
      baseUrl,
      cases,
      localApiKey: apiKey.key
    })
    usageRecordQueue.flushAllUsageRecordQueue()
    const usageCounts = usageCountsForApiKey(apiKey.id)
    const successCount = results.filter((item) => item.ok).length
    const successRate = results.length ? successCount / results.length : 0
    const statusCounts = countBy(results, (item) => String(item.status))
    const bucketCounts = countBy(results, (item) => item.bucket)
    const responseModelCounts = countBy(results.filter((item) => item.responseModel), (item) => item.responseModel ?? '')
    const usageModelCounts = usageCounts
      .filter((row) => row.traffic_source === 'gateway')
      .reduce<Record<string, number>>((output, row) => {
        output[row.model ?? 'unknown'] = (output[row.model ?? 'unknown'] ?? 0) + row.count
        return output
      }, {})
    const scoringUsageCount = usageCounts
      .filter((row) => row.traffic_source === 'hybrid_scoring')
      .reduce((sum, row) => sum + row.count, 0)
    const estimatedCost = estimateUnitCost(usageModelCounts, scoringUsageCount)
    const failures = results.filter((item) => !item.ok).slice(0, 10)

    const summary = {
      ok: successRate >= minSuccessRate && scoringUsageCount >= results.length,
      baseUrl: sanitizeBaseUrl(realBaseUrl),
      experiments: results.length,
      successCount,
      successRate: Number(successRate.toFixed(4)),
      minSuccessRate,
      requestConcurrency,
      requestTimeoutMs,
      statusCounts,
      bucketCounts,
      responseModelCounts,
      usageModelCounts,
      scoringUsageCount,
      estimatedUnitCost: estimatedCost,
      routeModels: levelRoutes.map((route) => `${route.minLevel}-${route.maxLevel}:${route.targetModel}`),
      failures
    }

    console.log(JSON.stringify(summary, null, 2))
    assert(successRate >= minSuccessRate, `真实混合路由成功率不足，期望 >= ${minSuccessRate}，实际 ${successRate}`)
    assert(scoringUsageCount >= results.length, `评分使用记录数量不足，期望至少 ${results.length}，实际 ${scoringUsageCount}`)
  } finally {
    await closeServer(appServer)
  }
} finally {
  hybridAffinity.clearHybridRouteAffinityForTest()
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function registerRealHybridCustomModels(): void {
  saveRealHybridCustomModel(OPENAI_COMPATIBLE_PROVIDER_CODE, scoringModel, scoringUnitCost, scoringUnitCost)
  saveRealHybridCustomModel(OPENAI_COMPATIBLE_PROVIDER_CODE, deepseekModel, modelUnitCosts.get(deepseekModel) ?? 0.002, modelUnitCosts.get(deepseekModel) ?? 0.002)
  saveRealHybridCustomModel(OPENAI_COMPATIBLE_PROVIDER_CODE, glmModel, modelUnitCosts.get(glmModel) ?? 0.01, modelUnitCosts.get(glmModel) ?? 0.01)
  saveRealHybridCustomModel(OPENAI_COMPATIBLE_PROVIDER_CODE, gptModel, modelUnitCosts.get(gptModel) ?? 0.02, modelUnitCosts.get(gptModel) ?? 0.02)
  saveRealHybridCustomModel(OPENAI_COMPATIBLE_PROVIDER_CODE, opusModel, modelUnitCosts.get(opusModel) ?? 0.05, modelUnitCosts.get(opusModel) ?? 0.05)
}

function saveRealHybridCustomModel(providerCode: ProviderCode, model: string, inputUsdPer1M: number, outputUsdPer1M: number): void {
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

function createRealHybridGroupAccount(input: {
  accountName: string
  groupName: string
  supportedModel: string
}): { accountId: string; groupId: string } {
  const group = repositories.createGroup({
    name: input.groupName,
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    name: input.accountName,
    type: 'api_key',
    clientCompatibility: 'openai_standard',
    credentials: {
      api_key: realApiKey,
      base_url: realBaseUrl,
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: Math.max(8, requestConcurrency * 2),
    supportedModels: [input.supportedModel]
  }, access)
  assert.deepEqual(account.supportedModels, [input.supportedModel])
  return { accountId: account.id, groupId: group.id }
}

function buildRealCases(count: number): RealCase[] {
  const simple: RealCase = {
    bucket: 'simple',
    prompt: '只把这句话改成更自然的中文：今天接口延迟有点高。只输出改写结果。'
  }
  const normal: RealCase = {
    bucket: 'normal',
    prompt: '用 5 条以内要点总结：API Key 绑定多个分组时，需要保证额度、状态、日志和错误提示都一致。'
  }
  const complex: RealCase = {
    bucket: 'complex',
    prompt: '请在 120 字以内给出一个多模型混合路由的排障顺序，覆盖评分失败、目标模型不可用、缓存亲和和成本异常。'
  }
  const frontier: RealCase = {
    bucket: 'frontier',
    prompt: '请在 160 字以内设计一个高可靠混合模型调度策略，要求兼顾跨供应商失败隔离、缓存命中、成本预算和最终质量复检。'
  }
  const weighted = [
    ...Array.from({ length: 70 }, () => simple),
    ...Array.from({ length: 20 }, () => normal),
    ...Array.from({ length: 8 }, () => complex),
    ...Array.from({ length: 2 }, () => frontier)
  ]
  return Array.from({ length: count }, (_, index) => weighted[index % weighted.length]!)
}

async function runRealCases(input: {
  apiKeyId: string
  baseUrl: string
  cases: RealCase[]
  localApiKey: string
}): Promise<RealCaseResult[]> {
  const results: RealCaseResult[] = new Array(input.cases.length)
  let nextIndex = 0
  const workers = Array.from({ length: Math.max(1, requestConcurrency) }, async () => {
    while (nextIndex < input.cases.length) {
      const index = nextIndex
      nextIndex += 1
      if (index > 0 && requestIntervalMs > 0) {
        await wait(requestIntervalMs)
      }
      results[index] = await runRealCase({
        apiKeyId: input.apiKeyId,
        baseUrl: input.baseUrl,
        index,
        localApiKey: input.localApiKey,
        testCase: input.cases[index]!
      })
      accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
      gatewayCache.clearGatewayRuntimeCache()
    }
  })
  await Promise.all(workers)
  return results
}

async function runRealCase(input: {
  apiKeyId: string
  baseUrl: string
  index: number
  localApiKey: string
  testCase: RealCase
}): Promise<RealCaseResult> {
  const startedAt = Date.now()
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), requestTimeoutMs)
  timer.unref()
  try {
    const response = await fetch(`${input.baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${input.localApiKey}`,
        'content-type': 'application/json',
        'x-session-id': `hybrid-real-${input.index}`,
        'x-client-request-id': `hybrid-real-${input.apiKeyId}-${input.index}`
      },
      body: JSON.stringify({
        model: 'hybrid-client-router',
        messages: [
          { role: 'system', content: '你是一个简洁的中文助手。不要输出推理过程，只给最终答案。' },
          { role: 'user', content: `${input.testCase.prompt}\n请求编号：${input.index}` }
        ],
        stream: false,
        max_tokens: 300,
        temperature: 0.2
      }),
      signal: controller.signal
    })
    const text = await response.text()
    const body = safeJsonObject(text)
    const content = firstAssistantContent(body)
    return {
      bucket: input.testCase.bucket,
      durationMs: Date.now() - startedAt,
      error: response.ok && content ? undefined : sanitizeErrorSnippet(text),
      index: input.index,
      ok: response.ok && Boolean(content),
      responseModel: typeof body.model === 'string' ? body.model : undefined,
      status: response.status
    }
  } catch (error) {
    return {
      bucket: input.testCase.bucket,
      durationMs: Date.now() - startedAt,
      error: sanitizeErrorSnippet(error instanceof Error ? error.message : String(error)),
      index: input.index,
      ok: false,
      status: 0
    }
  } finally {
    clearTimeout(timer)
  }
}

function usageCountsForApiKey(apiKeyId: string): UsageCountRow[] {
  return databaseModule.getDatasetDatabase()
    .prepare(`
      SELECT traffic_source, model, success, COUNT(*) AS count, SUM(cost_usd) AS cost_usd
      FROM usage_record_shard_entries
      WHERE api_key_id = ?
      GROUP BY traffic_source, model, success
      ORDER BY traffic_source ASC, model ASC, success ASC
    `)
    .all(apiKeyId) as unknown as UsageCountRow[]
}

function estimateUnitCost(usageModelCounts: Record<string, number>, scoringUsageCount: number): {
  averagePerRequest: number
  scoringCost: number
  targetCost: number
  totalCost: number
} {
  const scoringCost = scoringUsageCount * scoringUnitCost
  const targetCost = Object.entries(usageModelCounts)
    .reduce((sum, [model, count]) => sum + count * (modelUnitCosts.get(model) ?? 0), 0)
  const totalCost = scoringCost + targetCost
  return {
    averagePerRequest: Number((totalCost / Math.max(1, experimentCount)).toFixed(6)),
    scoringCost: Number(scoringCost.toFixed(6)),
    targetCost: Number(targetCost.toFixed(6)),
    totalCost: Number(totalCost.toFixed(6))
  }
}

function countBy<T>(items: T[], keyFor: (item: T) => string): Record<string, number> {
  const output: Record<string, number> = {}
  for (const item of items) {
    const key = keyFor(item)
    output[key] = (output[key] ?? 0) + 1
  }
  return output
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

function firstAssistantContent(body: Record<string, unknown>): string | undefined {
  const choices = Array.isArray(body.choices) ? body.choices : []
  const first = choices[0] as { message?: { content?: unknown, reasoning_content?: unknown } } | undefined
  if (typeof first?.message?.content === 'string' && first.message.content.trim()) {
    return first.message.content
  }
  if (typeof first?.message?.reasoning_content === 'string' && first.message.reasoning_content.trim()) {
    return first.message.reasoning_content
  }
  return choices.length > 0 ? '[non-empty-choice]' : undefined
}

function requiredEnv(name: string, aliases: string[] = []): string {
  const value = envText(name, aliases)
  if (!value) {
    throw new Error(`缺少环境变量 ${name}`)
  }
  return value
}

function envText(name: string, aliases: string[] = []): string | undefined {
  for (const key of [name, ...aliases]) {
    const value = process.env[key]?.trim()
    if (value) return value
  }
  return undefined
}

function positiveIntegerEnv(name: string): number | undefined {
  const value = envText(name)
  if (!value) return undefined
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed > 0 ? parsed : undefined
}

function numberEnv(name: string): number | undefined {
  const value = envText(name)
  if (!value) return undefined
  const parsed = Number(value)
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : undefined
}

function sanitizeErrorSnippet(value: string): string {
  const redacted = value
    .replaceAll(realApiKey, '[redacted-real-api-key]')
    .slice(0, 600)
  return redacted || 'empty response'
}

function sanitizeBaseUrl(value: string): string {
  try {
    const url = new URL(value)
    url.username = ''
    url.password = ''
    return url.toString().replace(/\/$/, '')
  } catch {
    return value.replaceAll(realApiKey, '[redacted-real-api-key]')
  }
}

function wait(ms: number): Promise<void> {
  if (ms <= 0) return Promise.resolve()
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
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
