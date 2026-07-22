import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
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

type TargetKey = 'hybrid' | 'gpt55' | 'opus'

interface QualityCase {
  acceptanceCriteria: string[]
  category: string
  id: string
  prompt: string
  title: string
}

interface CompletionResult {
  attempts?: number
  content: string
  durationMs: number
  error?: string
  model?: string
  ok: boolean
  retryErrors?: string[]
  status: number
}

interface JudgedTargetResult {
  fatalIssues: string[]
  missingCriteria: string[]
  pass: boolean
  reason: string
  totalScore: number
}

interface CaseComparisonResult {
  caseId: string
  category: string
  judgeOk: boolean
  judgeRaw?: string
  results: Record<TargetKey, CompletionResult>
  scores: Record<TargetKey, JudgedTargetResult>
  title: string
}

interface UsageCountRow {
  traffic_source: string
  model: string | null
  success: number
  count: number
  cost_usd: number | null
}

const allQualityCases = qualityCases()
const realApiKey = requiredEnv('JUHE_REAL_HYBRID_QUALITY_API_KEY', [
  'JUHE_REAL_HYBRID_API_KEY',
  'HYBRID_REAL_API_KEY'
])
const realBaseUrl = envText('JUHE_REAL_HYBRID_QUALITY_BASE_URL', [
  'JUHE_REAL_HYBRID_BASE_URL',
  'HYBRID_REAL_BASE_URL'
]) || 'https://vsllm.com'
const caseLimit = Math.min(
  allQualityCases.length,
  Math.max(1, positiveIntegerEnv('JUHE_REAL_HYBRID_QUALITY_CASES') ?? 20)
)
const requestTimeoutMs = positiveIntegerEnv('JUHE_REAL_HYBRID_QUALITY_REQUEST_TIMEOUT_MS') ?? 240_000
const requestIntervalMs = positiveIntegerEnv('JUHE_REAL_HYBRID_QUALITY_REQUEST_INTERVAL_MS') ?? 6_500
const upstreamRetryCount = positiveIntegerEnv('JUHE_REAL_HYBRID_QUALITY_UPSTREAM_RETRIES') ?? 20
const upstreamRetryDelayMs = positiveIntegerEnv('JUHE_REAL_HYBRID_QUALITY_UPSTREAM_RETRY_DELAY_MS') ?? 5_000
const parallelTargetRequests = booleanEnv('JUHE_REAL_HYBRID_QUALITY_PARALLEL_REQUESTS') ?? false
const parallelJudgeRequests = booleanEnv('JUHE_REAL_HYBRID_QUALITY_PARALLEL_JUDGES') ?? false
const outputPath = envText('JUHE_REAL_HYBRID_QUALITY_OUTPUT_PATH')
const outputMaxTokens = positiveIntegerEnv('JUHE_REAL_HYBRID_QUALITY_OUTPUT_MAX_TOKENS') ?? 1_400
const judgeMaxTokens = positiveIntegerEnv('JUHE_REAL_HYBRID_QUALITY_JUDGE_MAX_TOKENS') ?? 1_200
const scoringModel = envText('JUHE_REAL_HYBRID_QUALITY_SCORING_MODEL') || 'gpt-5.4-mini'
const lowModel = envText('JUHE_REAL_HYBRID_QUALITY_MODEL_1_2') || envText('JUHE_REAL_HYBRID_QUALITY_MODEL_1_3') || 'gpt-5.4-mini'
const glmModel = envText('JUHE_REAL_HYBRID_QUALITY_MODEL_4_6') || 'glm-5.2'
const gptModel = envText('JUHE_REAL_HYBRID_QUALITY_GPT_MODEL') || 'gpt-5.5'
const opusModel = envText('JUHE_REAL_HYBRID_QUALITY_OPUS_MODEL') || 'claude-opus-4-7'
const judgeModel = envText('JUHE_REAL_HYBRID_QUALITY_JUDGE_MODEL') || gptModel
const scoringUnitCost = numberEnv('JUHE_REAL_HYBRID_QUALITY_SCORING_UNIT_COST') ?? 0.002
const judgeUnitCost = numberEnv('JUHE_REAL_HYBRID_QUALITY_JUDGE_UNIT_COST') ?? modelCostDefault(judgeModel)
const minPassRate = numberEnv('JUHE_REAL_HYBRID_QUALITY_MIN_PASS_RATE')

const levelRoutes: ApiKeyHybridRoutingConfig['levelRoutes'] = [
  { minLevel: 1, maxLevel: 2, targetModel: lowModel, enabled: true },
  { minLevel: 3, maxLevel: 6, targetModel: glmModel, enabled: true },
  { minLevel: 7, maxLevel: 8, targetModel: gptModel, enabled: true },
  { minLevel: 9, maxLevel: 10, targetModel: opusModel, enabled: true }
]

const modelUnitCosts = new Map<string, number>([
  [lowModel, numberEnv('JUHE_REAL_HYBRID_QUALITY_COST_1_2') ?? numberEnv('JUHE_REAL_HYBRID_QUALITY_COST_1_3') ?? modelCostDefault(lowModel)],
  [glmModel, numberEnv('JUHE_REAL_HYBRID_QUALITY_COST_4_6') ?? modelCostDefault(glmModel)],
  [gptModel, numberEnv('JUHE_REAL_HYBRID_QUALITY_GPT_COST') ?? modelCostDefault(gptModel)],
  [opusModel, numberEnv('JUHE_REAL_HYBRID_QUALITY_OPUS_COST') ?? modelCostDefault(opusModel)]
])

const tempRoot = resolve(tmpdir(), `juhe-ai-hybrid-quality-comparison-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'hybrid-quality-comparison.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'hybrid-quality-comparison-secret'
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
app.use('/v1', express.raw({ type: () => true, limit: '10mb' }), captureGatewayRawBody, async (req, res, next) => {
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
    registerQualityCustomModels()
    const scoring = createRealGroupAccount('Hybrid Quality 评分分组', 'Hybrid Quality 评分账户', scoringModel)
    const low = createRealGroupAccount('Hybrid Quality 低档分组', 'Hybrid Quality 低档账户', lowModel)
    const glm = createRealGroupAccount('Hybrid Quality GLM 分组', 'Hybrid Quality GLM 账户', glmModel)
    const gpt = createRealGroupAccount('Hybrid Quality GPT 分组', 'Hybrid Quality GPT 账户', gptModel)
    const opus = createRealGroupAccount('Hybrid Quality Opus 分组', 'Hybrid Quality Opus 账户', opusModel)

    const hybridApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'Hybrid Quality 智能路由 Key',
      routeMode: 'hybrid',
      groupRouteStrategy: 'priority_failover',
      groupBindings: [scoring, low, glm, gpt, opus].map((item, index) => ({
        groupId: item.groupId,
        priority: index + 1,
        weight: 1,
        status: 'active'
      })),
      hybridRoutingConfig: {
        scoringModel,
        scoringContextMode: 'full_request',
        qualityPreference: 'balanced',
        scoringTimeoutMs: 45_000,
        scoringFallbackMaxLevel: 5,
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
    assert(gpt.accountId && opus.accountId, '质量对比固定模型账户创建失败')
    assert(hybridApiKey.key, '质量对比 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`
    const selectedCases = allQualityCases.slice(0, caseLimit)
    const comparisons: CaseComparisonResult[] = []

    for (const [index, testCase] of selectedCases.entries()) {
      if (index > 0 && requestIntervalMs > 0) {
        await wait(requestIntervalMs)
      }
      const results = await runTargetCompletions({
        baseUrl,
        hybridApiKey: hybridApiKey.key,
        index,
        testCase
      })
      const judged = await judgeCase(testCase, results)
      comparisons.push(judged)
      writePartialSummary({
        comparisons,
        hybridUsageCounts: usageCountsForApiKey(hybridApiKey.id),
        selectedCases: selectedCases.slice(0, comparisons.length)
      })
      accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
      gatewayCache.clearGatewayRuntimeCache()
    }

    usageRecordQueue.flushAllUsageRecordQueue()
    const hybridUsageCounts = usageCountsForApiKey(hybridApiKey.id)
    const summary = buildSummary({
      comparisons,
      hybridUsageCounts,
      selectedCases
    })

    console.log(JSON.stringify(summary, null, 2))
    if (typeof minPassRate === 'number') {
      assert(summary.passRates.hybrid >= minPassRate, `混合模型正确率不足，期望 >= ${minPassRate}，实际 ${summary.passRates.hybrid}`)
    }
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

function registerQualityCustomModels(): void {
  saveQualityCustomModel(OPENAI_COMPATIBLE_PROVIDER_CODE, scoringModel, scoringUnitCost)
  saveQualityCustomModel(OPENAI_COMPATIBLE_PROVIDER_CODE, lowModel, modelUnitCosts.get(lowModel) ?? 0.002)
  saveQualityCustomModel(OPENAI_COMPATIBLE_PROVIDER_CODE, glmModel, modelUnitCosts.get(glmModel) ?? 0.01)
  saveQualityCustomModel(OPENAI_COMPATIBLE_PROVIDER_CODE, gptModel, modelUnitCosts.get(gptModel) ?? 0.02)
  saveQualityCustomModel(OPENAI_COMPATIBLE_PROVIDER_CODE, opusModel, modelUnitCosts.get(opusModel) ?? 0.05)
}

function saveQualityCustomModel(providerCode: ProviderCode, model: string, unitCost: number): void {
  saveCustomProviderModel({
    providerCode,
    model,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: unitCost,
    outputUsdPer1M: unitCost,
    cachedInputUsdPer1M: unitCost / 10,
    actorSystemAccountId: access.systemAccountId
  })
}

function createRealGroupAccount(groupName: string, accountName: string, supportedModel: string): { accountId: string; groupId: string } {
  const group = repositories.createGroup({
    name: groupName,
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    name: accountName,
    type: 'api_key',
    credentials: {
      api_key: realApiKey,
      base_url: realBaseUrl,
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 1,
    supportedModels: [supportedModel]
  }, access)
  assert.deepEqual(account.supportedModels, [supportedModel])
  return { accountId: account.id, groupId: group.id }
}

async function callGatewayCompletion(
  baseUrl: string,
  localApiKey: string,
  model: string,
  testCase: QualityCase,
  sessionId: string
): Promise<CompletionResult> {
  return callWithCompletionRetries((attempt) => callGatewayCompletionOnce(baseUrl, localApiKey, model, testCase, `${sessionId}-attempt-${attempt}`))
}

async function callGatewayCompletionOnce(
  baseUrl: string,
  localApiKey: string,
  model: string,
  testCase: QualityCase,
  requestId: string
): Promise<CompletionResult> {
  const startedAt = Date.now()
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), requestTimeoutMs)
  timer.unref()
  try {
    const response = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json',
        'x-session-id': requestId,
        'x-client-request-id': `${requestId}-${testCase.id}`
      },
      body: JSON.stringify({
        model,
        messages: [
          {
            role: 'system',
            content: '你是资深全栈工程师。只输出最终方案，要求结构清晰、可执行、不要输出推理过程。'
          },
          {
            role: 'user',
            content: buildTaskPrompt(testCase)
          }
        ],
        stream: false,
        max_tokens: outputMaxTokens,
        temperature: 0.2
      }),
      signal: controller.signal
    })
    const text = await response.text()
    const body = safeJsonObject(text)
    const content = firstAssistantContent(body)
    return {
      content: content ?? '',
      durationMs: Date.now() - startedAt,
      error: response.ok && content ? undefined : sanitizeErrorSnippet(text),
      model: typeof body.model === 'string' ? body.model : undefined,
      ok: response.ok && Boolean(content),
      status: response.status
    }
  } catch (error) {
    return {
      content: '',
      durationMs: Date.now() - startedAt,
      error: sanitizeErrorSnippet(error instanceof Error ? error.message : String(error)),
      ok: false,
      status: 0
    }
  } finally {
    clearTimeout(timer)
  }
}

async function callDirectModelCompletion(model: string, testCase: QualityCase): Promise<CompletionResult> {
  return callUpstreamChatCompletion({
    model,
    messages: [
      {
        role: 'system',
        content: '你是资深全栈工程师。只输出最终方案，要求结构清晰、可执行、不要输出推理过程。'
      },
      {
        role: 'user',
        content: buildTaskPrompt(testCase)
      }
    ],
    stream: false,
    max_tokens: outputMaxTokens,
    temperature: 0.2
  })
}

async function runTargetCompletions(input: {
  baseUrl: string
  hybridApiKey: string
  index: number
  testCase: QualityCase
}): Promise<Record<TargetKey, CompletionResult>> {
  if (parallelTargetRequests) {
    const [hybridResult, gptResult, opusResult] = await Promise.all([
      callGatewayCompletion(input.baseUrl, input.hybridApiKey, 'hybrid-client-router', input.testCase, `quality-hybrid-${input.index}`),
      callDirectModelCompletion(gptModel, input.testCase),
      callDirectModelCompletion(opusModel, input.testCase)
    ])
    return {
      hybrid: hybridResult,
      gpt55: gptResult,
      opus: opusResult
    }
  }
  const hybridResult = await callGatewayCompletion(input.baseUrl, input.hybridApiKey, 'hybrid-client-router', input.testCase, `quality-hybrid-${input.index}`)
  await wait(requestIntervalMs)
  const gptResult = await callDirectModelCompletion(gptModel, input.testCase)
  await wait(requestIntervalMs)
  const opusResult = await callDirectModelCompletion(opusModel, input.testCase)
  return {
    hybrid: hybridResult,
    gpt55: gptResult,
    opus: opusResult
  }
}

async function judgeCase(
  testCase: QualityCase,
  results: Record<TargetKey, CompletionResult>
): Promise<CaseComparisonResult> {
  const [hybridJudgement, gptJudgement, opusJudgement] = parallelJudgeRequests
    ? await Promise.all([
      judgeSingleAnswer(testCase, results.hybrid),
      judgeSingleAnswer(testCase, results.gpt55),
      judgeSingleAnswer(testCase, results.opus)
    ])
    : await judgeAnswersSerially(testCase, results)
  const failedJudges = [hybridJudgement, gptJudgement, opusJudgement]
    .filter((item) => !item.ok && item.raw)
    .map((item) => item.raw)
  return {
    caseId: testCase.id,
    category: testCase.category,
    judgeOk: failedJudges.length === 0,
    judgeRaw: failedJudges.length ? failedJudges.join('\n---\n').slice(0, 1_000) : undefined,
    results,
    scores: {
      hybrid: hybridJudgement.score,
      gpt55: gptJudgement.score,
      opus: opusJudgement.score
    },
    title: testCase.title
  }
}

async function judgeSingleAnswer(testCase: QualityCase, result: CompletionResult): Promise<{
  ok: boolean
  raw?: string
  score: JudgedTargetResult
}> {
  if (!result.ok || !result.content.trim()) {
    return { ok: true, score: failedCompletionScore(result.error) }
  }
  const judgePayload = {
    model: judgeModel,
    messages: [
      {
        role: 'system',
        content: [
          '你是严格的软件工程评审。你只根据题目和验收点评价答案质量。',
          '不要因为答案长就给高分；缺少关键边界、测试、数据结构或错误处理要扣分。',
          '只输出严格 JSON，不要 Markdown，不要解释 JSON 外的内容。'
        ].join('\n')
      },
      {
        role: 'user',
        content: buildSingleJudgePrompt(testCase, result.content)
      }
    ],
    stream: false,
    max_tokens: judgeMaxTokens,
    temperature: 0
  }
  const judgeResponse = await callUpstreamChatCompletion(judgePayload)
  const parsed = parseSingleJudgeOutput(judgeResponse.content)
  return {
    ok: judgeResponse.ok && parsed.ok,
    raw: parsed.ok ? undefined : sanitizeErrorSnippet(judgeResponse.content || judgeResponse.error || ''),
    score: parsed.score
  }
}

async function judgeAnswersSerially(
  testCase: QualityCase,
  results: Record<TargetKey, CompletionResult>
): Promise<[
  Awaited<ReturnType<typeof judgeSingleAnswer>>,
  Awaited<ReturnType<typeof judgeSingleAnswer>>,
  Awaited<ReturnType<typeof judgeSingleAnswer>>
]> {
  const hybridJudgement = await judgeSingleAnswer(testCase, results.hybrid)
  await wait(requestIntervalMs)
  const gptJudgement = await judgeSingleAnswer(testCase, results.gpt55)
  await wait(requestIntervalMs)
  const opusJudgement = await judgeSingleAnswer(testCase, results.opus)
  return [hybridJudgement, gptJudgement, opusJudgement]
}

async function callUpstreamChatCompletion(payload: Record<string, unknown>): Promise<CompletionResult> {
  return callWithCompletionRetries(() => callUpstreamChatCompletionOnce(payload))
}

async function callWithCompletionRetries(callOnce: (attempt: number) => Promise<CompletionResult>): Promise<CompletionResult> {
  const startedAt = Date.now()
  const retryErrors: string[] = []
  let lastResult: CompletionResult | undefined
  for (let attempt = 1; attempt <= upstreamRetryCount + 1; attempt += 1) {
    const result = await callOnce(attempt)
    lastResult = result
    if (result.ok || !isRetryableUpstreamResult(result) || attempt > upstreamRetryCount) {
      return {
        ...result,
        attempts: attempt,
        durationMs: Date.now() - startedAt,
        retryErrors: retryErrors.length ? retryErrors : undefined
      }
    }
    retryErrors.push(completionRetrySummary(result))
    await wait(upstreamRetryDelayMs)
  }
  return {
    ...(lastResult ?? {
      content: '',
      durationMs: 0,
      error: 'upstream retry exhausted',
      ok: false,
      status: 0
    }),
    attempts: upstreamRetryCount + 1,
    durationMs: Date.now() - startedAt,
    retryErrors: retryErrors.length ? retryErrors : undefined
  }
}

async function callUpstreamChatCompletionOnce(payload: Record<string, unknown>): Promise<CompletionResult> {
  const startedAt = Date.now()
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), requestTimeoutMs)
  timer.unref()
  try {
    const response = await fetch(chatCompletionsUrl(realBaseUrl), {
      method: 'POST',
      headers: {
        authorization: `Bearer ${realApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify(payload),
      signal: controller.signal
    })
    const text = await response.text()
    const body = safeJsonObject(text)
    const content = firstAssistantContent(body)
    return {
      content: content ?? '',
      durationMs: Date.now() - startedAt,
      error: response.ok && content ? undefined : sanitizeErrorSnippet(text),
      model: typeof body.model === 'string' ? body.model : undefined,
      ok: response.ok && Boolean(content),
      status: response.status
    }
  } catch (error) {
    return {
      content: '',
      durationMs: Date.now() - startedAt,
      error: sanitizeErrorSnippet(error instanceof Error ? error.message : String(error)),
      ok: false,
      status: 0
    }
  } finally {
    clearTimeout(timer)
  }
}

function isRetryableUpstreamResult(result: CompletionResult): boolean {
  if (result.status === 0 || result.status === 408 || result.status === 409 || result.status === 429) return true
  if (result.status >= 500 && result.status < 600) {
    return !(result.error ?? '').includes('auth_unavailable')
  }
  const error = result.error ?? ''
  return error.includes('bad_response_body') || error.includes('corrupted upstream response')
}

function completionRetrySummary(result: CompletionResult): string {
  const error = result.error ? result.error.replace(/\s+/g, ' ').slice(0, 180) : 'empty error'
  return `status=${result.status} ${error}`
}

function buildTaskPrompt(testCase: QualityCase): string {
  return [
    `任务：${testCase.title}`,
    '',
    testCase.prompt,
    '',
    '请按以下结构回答：',
    '1. 目标和约束',
    '2. 数据结构或接口设计',
    '3. 核心流程或实现步骤',
    '4. 错误处理和边界',
    '5. 测试与验收',
    '',
    '总字数控制在 450 字以内，每节 1-3 条。',
    '不要写空泛建议，尽量给出字段、接口、状态流转、测试点或伪代码。'
  ].join('\n')
}

function buildJudgePrompt(
  testCase: QualityCase,
  aliases: TargetKey[],
  results: Record<TargetKey, CompletionResult>,
  targetToAlias: Map<TargetKey, string>
): string {
  const answerBlocks = aliases.map((target) => {
    const alias = targetToAlias.get(target)
    const result = results[target]
    return [
      `答案 ${alias}:`,
      result.ok ? truncateForJudge(result.content) : `请求失败：${result.error ?? 'unknown error'}`
    ].join('\n')
  })
  return [
    `题目 ID：${testCase.id}`,
    `题目：${testCase.title}`,
    '',
    testCase.prompt,
    '',
    '验收点：',
    ...testCase.acceptanceCriteria.map((item, index) => `${index + 1}. ${item}`),
    '',
    ...answerBlocks,
    '',
    '请分别评价答案 A、B、C。评分标准：totalScore 0-100；>=80 且没有 fatalIssues 才算 pass=true。',
    '重点看是否满足验收点、是否可落地、是否覆盖风险和测试。允许答案与参考做法不同，但必须自洽可执行。',
    '只输出如下 JSON 结构：',
    '{"A":{"totalScore":0,"pass":false,"reason":"","missingCriteria":[],"fatalIssues":[]},"B":{"totalScore":0,"pass":false,"reason":"","missingCriteria":[],"fatalIssues":[]},"C":{"totalScore":0,"pass":false,"reason":"","missingCriteria":[],"fatalIssues":[]}}'
  ].join('\n')
}

function buildSingleJudgePrompt(testCase: QualityCase, answer: string): string {
  return [
    `题目 ID：${testCase.id}`,
    `题目：${testCase.title}`,
    '',
    testCase.prompt,
    '',
    '验收点：',
    ...testCase.acceptanceCriteria.map((item, index) => `${index + 1}. ${item}`),
    '',
    '待评审答案：',
    truncateForJudge(answer),
    '',
    '评分标准：totalScore 0-100；>=80 且没有 fatalIssues 才算 pass=true。',
    '重点看是否满足验收点、是否可落地、是否覆盖风险和测试。允许答案与参考做法不同，但必须自洽可执行。',
    '只输出如下 JSON 结构：',
    '{"totalScore":0,"pass":false,"reason":"","missingCriteria":[],"fatalIssues":[]}'
  ].join('\n')
}

function parseJudgeOutput(text: string, aliasToTarget: Map<string, TargetKey>): {
  ok: boolean
  scores: Record<TargetKey, JudgedTargetResult>
} {
  const defaults = defaultScores()
  const parsed = parseJsonObjectLoose(text)
  if (!parsed) return { ok: false, scores: defaults }
  let ok = true
  for (const [alias, target] of aliasToTarget.entries()) {
    const item = parsed[alias] as Record<string, unknown> | undefined
    if (!item || typeof item !== 'object') {
      ok = false
      continue
    }
    const totalScore = boundedNumber(item.totalScore, 0, 100)
    const fatalIssues = stringArray(item.fatalIssues)
    defaults[target] = {
      fatalIssues,
      missingCriteria: stringArray(item.missingCriteria),
      pass: typeof item.pass === 'boolean' ? item.pass && totalScore >= 80 && fatalIssues.length === 0 : totalScore >= 80 && fatalIssues.length === 0,
      reason: typeof item.reason === 'string' ? item.reason.slice(0, 500) : '',
      totalScore
    }
  }
  return { ok, scores: defaults }
}

function parseSingleJudgeOutput(text: string): {
  ok: boolean
  score: JudgedTargetResult
} {
  const parsed = parseJsonObjectLoose(text)
  if (!parsed) return parseSingleJudgeOutputFallback(text)
  const totalScore = boundedNumber(parsed.totalScore, 0, 100)
  const fatalIssues = stringArray(parsed.fatalIssues)
  return {
    ok: true,
    score: {
      fatalIssues,
      missingCriteria: stringArray(parsed.missingCriteria),
      pass: typeof parsed.pass === 'boolean' ? parsed.pass && totalScore >= 80 && fatalIssues.length === 0 : totalScore >= 80 && fatalIssues.length === 0,
      reason: typeof parsed.reason === 'string' ? parsed.reason.slice(0, 500) : '',
      totalScore
    }
  }
}

function parseSingleJudgeOutputFallback(text: string): {
  ok: boolean
  score: JudgedTargetResult
} {
  const totalScoreMatch = text.match(/\\?"totalScore\\?"\s*:\s*(\d+(?:\.\d+)?)/)
  const passMatch = text.match(/\\?"pass\\?"\s*:\s*(true|false)/)
  if (!totalScoreMatch) return { ok: false, score: failedJudgeScore() }
  const totalScore = boundedNumber(totalScoreMatch[1], 0, 100)
  const declaredPass = passMatch?.[1] === 'true'
  const hasFatalIssues = /\\?"fatalIssues\\?"\s*:\s*\[\s*\\?"[^"]/.test(text)
  return {
    ok: true,
    score: {
      fatalIssues: hasFatalIssues ? ['judge_returned_unparseable_fatal_issues'] : [],
      missingCriteria: [],
      pass: declaredPass && totalScore >= 80 && !hasFatalIssues,
      reason: '评审模型返回了非严格 JSON，已按 totalScore/pass 做保守兜底解析。',
      totalScore
    }
  }
}

function buildSummary(input: {
  comparisons: CaseComparisonResult[]
  hybridUsageCounts: UsageCountRow[]
  selectedCases: QualityCase[]
}): Record<string, unknown> & { passRates: Record<TargetKey, number> } {
  const passRates = passRateByTarget(input.comparisons)
  const averageScores = averageScoreByTarget(input.comparisons)
  const hybridUsageModelCounts = usageModelCounts(input.hybridUsageCounts, 'gateway')
  const scoringUsageCount = usageCount(input.hybridUsageCounts, 'hybrid_scoring')
  const failedRequestItems = failedRequests(input.comparisons)
  return {
    ok: input.comparisons.every((item) =>
      item.judgeOk
      && item.results.hybrid.ok
      && item.results.gpt55.ok
      && item.results.opus.ok
    ),
    baseUrl: sanitizeBaseUrl(realBaseUrl),
    cases: input.selectedCases.map((item) => ({ id: item.id, title: item.title, category: item.category })),
    caseCount: input.selectedCases.length,
    retryPolicy: {
      retryableFailuresAreStabilityNoise: true,
      maxRetries: upstreamRetryCount,
      maxAttempts: upstreamRetryCount + 1,
      retryDelayMs: upstreamRetryDelayMs,
      requestIntervalMs,
      qualityMetricsScope: 'only_successful_target_answers'
    },
    judgeModel,
    baselineModels: {
      hybridClientModel: 'hybrid-client-router',
      gpt55: gptModel,
      opus: opusModel
    },
    routeModels: levelRoutes.map((route) => `${route.minLevel}-${route.maxLevel}:${route.targetModel}`),
    requestSuccessRates: requestSuccessRateByTarget(input.comparisons),
    qualityEvaluatedCounts: qualityEvaluatedCountByTarget(input.comparisons),
    stabilityFailureCounts: stabilityFailureCountByTarget(input.comparisons),
    passRates,
    averageScores,
    hybridUsageModelCounts,
    responseModelCounts: {
      hybrid: responseModelCounts(input.comparisons, 'hybrid'),
      gpt55: responseModelCounts(input.comparisons, 'gpt55'),
      opus: responseModelCounts(input.comparisons, 'opus')
    },
    estimatedUnitCost: {
      hybrid: estimateHybridCost(hybridUsageModelCounts, scoringUsageCount, input.selectedCases.length),
      gpt55: estimateFixedCost(input.selectedCases.length, gptModel, input.selectedCases.length),
      opus: estimateFixedCost(input.selectedCases.length, opusModel, input.selectedCases.length),
      judge: Number((input.selectedCases.length * judgeUnitCost).toFixed(6))
    },
    failedRequests: failedRequestItems,
    stabilityFailures: failedRequestItems,
    failedJudges: input.comparisons
      .filter((item) => !item.judgeOk)
      .map((item) => ({ caseId: item.caseId, title: item.title, judgeRaw: item.judgeRaw }))
      .slice(0, 5),
    perCase: input.comparisons.map((item) => ({
      caseId: item.caseId,
      title: item.title,
      scores: {
        hybrid: pickScore(item.scores.hybrid),
        gpt55: pickScore(item.scores.gpt55),
        opus: pickScore(item.scores.opus)
      },
      models: {
        hybrid: item.results.hybrid.model,
        gpt55: item.results.gpt55.model,
        opus: item.results.opus.model
      },
      attempts: {
        hybrid: item.results.hybrid.attempts,
        gpt55: item.results.gpt55.attempts,
        opus: item.results.opus.attempts
      }
    }))
  }
}

function writePartialSummary(input: {
  comparisons: CaseComparisonResult[]
  hybridUsageCounts: UsageCountRow[]
  selectedCases: QualityCase[]
}): void {
  if (!outputPath) return
  const summary = buildSummary({
    comparisons: input.comparisons,
    hybridUsageCounts: input.hybridUsageCounts,
    selectedCases: input.selectedCases
  })
  writeFileSync(outputPath, `${JSON.stringify(summary, null, 2)}\n`, 'utf8')
}

function requestSuccessRateByTarget(items: CaseComparisonResult[]): Record<TargetKey, number> {
  const denominator = Math.max(1, items.length)
  return {
    hybrid: roundRate(items.filter((item) => item.results.hybrid.ok).length / denominator),
    gpt55: roundRate(items.filter((item) => item.results.gpt55.ok).length / denominator),
    opus: roundRate(items.filter((item) => item.results.opus.ok).length / denominator)
  }
}

function usageCountsForApiKey(apiKeyId: string): UsageCountRow[] {
  return databaseModule.getUsageCatalogDatabase()
    .prepare(`
      SELECT traffic_source, model, success, COUNT(*) AS count, SUM(cost_usd) AS cost_usd
      FROM usage_record_shard_entries
      WHERE api_key_id = ?
      GROUP BY traffic_source, model, success
      ORDER BY traffic_source ASC, model ASC, success ASC
    `)
    .all(apiKeyId) as unknown as UsageCountRow[]
}

function usageModelCounts(rows: UsageCountRow[], trafficSource: string): Record<string, number> {
  return rows
    .filter((row) => row.traffic_source === trafficSource)
    .reduce<Record<string, number>>((output, row) => {
      const model = row.model ?? 'unknown'
      output[model] = (output[model] ?? 0) + row.count
      return output
    }, {})
}

function usageCount(rows: UsageCountRow[], trafficSource: string): number {
  return rows
    .filter((row) => row.traffic_source === trafficSource)
    .reduce((sum, row) => sum + row.count, 0)
}

function passRateByTarget(items: CaseComparisonResult[]): Record<TargetKey, number> {
  const denominators = qualityEvaluatedCountByTarget(items)
  return {
    hybrid: rateForSuccessfulAnswers(items, 'hybrid', denominators.hybrid),
    gpt55: rateForSuccessfulAnswers(items, 'gpt55', denominators.gpt55),
    opus: rateForSuccessfulAnswers(items, 'opus', denominators.opus)
  }
}

function averageScoreByTarget(items: CaseComparisonResult[]): Record<TargetKey, number> {
  const denominators = qualityEvaluatedCountByTarget(items)
  return {
    hybrid: averageScoreForSuccessfulAnswers(items, 'hybrid', denominators.hybrid),
    gpt55: averageScoreForSuccessfulAnswers(items, 'gpt55', denominators.gpt55),
    opus: averageScoreForSuccessfulAnswers(items, 'opus', denominators.opus)
  }
}

function qualityEvaluatedCountByTarget(items: CaseComparisonResult[]): Record<TargetKey, number> {
  return {
    hybrid: items.filter((item) => item.results.hybrid.ok).length,
    gpt55: items.filter((item) => item.results.gpt55.ok).length,
    opus: items.filter((item) => item.results.opus.ok).length
  }
}

function stabilityFailureCountByTarget(items: CaseComparisonResult[]): Record<TargetKey, number> {
  return {
    hybrid: items.filter((item) => !item.results.hybrid.ok).length,
    gpt55: items.filter((item) => !item.results.gpt55.ok).length,
    opus: items.filter((item) => !item.results.opus.ok).length
  }
}

function rateForSuccessfulAnswers(items: CaseComparisonResult[], target: TargetKey, denominator: number): number {
  if (denominator <= 0) return 0
  return roundRate(items.filter((item) => item.results[target].ok && item.scores[target].pass).length / denominator)
}

function averageScoreForSuccessfulAnswers(items: CaseComparisonResult[], target: TargetKey, denominator: number): number {
  if (denominator <= 0) return 0
  return roundScore(
    items
      .filter((item) => item.results[target].ok)
      .reduce((sum, item) => sum + item.scores[target].totalScore, 0) / denominator
  )
}

function responseModelCounts(items: CaseComparisonResult[], target: TargetKey): Record<string, number> {
  return items.reduce<Record<string, number>>((output, item) => {
    const model = item.results[target].model ?? 'unknown'
    output[model] = (output[model] ?? 0) + 1
    return output
  }, {})
}

function estimateHybridCost(usageCounts: Record<string, number>, scoringCount: number, caseCount: number): {
  averagePerCase: number
  scoringCost: number
  targetCost: number
  totalCost: number
} {
  const scoringCost = scoringCount * scoringUnitCost
  const targetCost = Object.entries(usageCounts)
    .reduce((sum, [model, count]) => sum + count * (modelUnitCosts.get(model) ?? modelCostDefault(model)), 0)
  const totalCost = scoringCost + targetCost
  return {
    averagePerCase: Number((totalCost / Math.max(1, caseCount)).toFixed(6)),
    scoringCost: Number(scoringCost.toFixed(6)),
    targetCost: Number(targetCost.toFixed(6)),
    totalCost: Number(totalCost.toFixed(6))
  }
}

function estimateFixedCost(count: number, model: string, caseCount: number): {
  averagePerCase: number
  requestCost: number
  totalCost: number
} {
  const totalCost = count * (modelUnitCosts.get(model) ?? modelCostDefault(model))
  return {
    averagePerCase: Number((totalCost / Math.max(1, caseCount)).toFixed(6)),
    requestCost: Number(totalCost.toFixed(6)),
    totalCost: Number(totalCost.toFixed(6))
  }
}

function failedRequests(items: CaseComparisonResult[]): Array<Record<string, unknown>> {
  const output: Array<Record<string, unknown>> = []
  for (const item of items) {
    for (const target of ['hybrid', 'gpt55', 'opus'] as TargetKey[]) {
      const result = item.results[target]
      if (!result.ok) {
        output.push({
          attempts: result.attempts,
          caseId: item.caseId,
          error: result.error,
          retryErrors: result.retryErrors,
          status: result.status,
          target
        })
      }
    }
  }
  return output.slice(0, 10)
}

function pickScore(score: JudgedTargetResult): Record<string, unknown> {
  return {
    pass: score.pass,
    totalScore: score.totalScore,
    missingCriteria: score.missingCriteria,
    fatalIssues: score.fatalIssues
  }
}

function defaultScores(): Record<TargetKey, JudgedTargetResult> {
  return {
    hybrid: failedJudgeScore(),
    gpt55: failedJudgeScore(),
    opus: failedJudgeScore()
  }
}

function failedJudgeScore(): JudgedTargetResult {
  return {
    fatalIssues: ['judge_parse_failed'],
    missingCriteria: [],
    pass: false,
    reason: '评审结果解析失败',
    totalScore: 0
  }
}

function failedCompletionScore(error: string | undefined): JudgedTargetResult {
  return {
    fatalIssues: [error ? `request_failed: ${error.slice(0, 180)}` : 'request_failed'],
    missingCriteria: [],
    pass: false,
    reason: '目标模型请求失败或无有效答案',
    totalScore: 0
  }
}

function shuffledAliases(index: number): TargetKey[] {
  const orders: TargetKey[][] = [
    ['hybrid', 'gpt55', 'opus'],
    ['gpt55', 'opus', 'hybrid'],
    ['opus', 'hybrid', 'gpt55'],
    ['hybrid', 'opus', 'gpt55'],
    ['gpt55', 'hybrid', 'opus'],
    ['opus', 'gpt55', 'hybrid']
  ]
  return orders[index % orders.length]!
}

function truncateForJudge(value: string): string {
  return value.length > 7_000 ? `${value.slice(0, 7_000)}\n[内容已截断]` : value
}

function parseJsonObjectLoose(text: string): Record<string, unknown> | undefined {
  try {
    const parsed = JSON.parse(text) as unknown
    return isRecord(parsed) ? parsed : undefined
  } catch {
    const start = text.indexOf('{')
    const end = text.lastIndexOf('}')
    if (start < 0 || end <= start) return undefined
    try {
      const parsed = JSON.parse(text.slice(start, end + 1)) as unknown
      return isRecord(parsed) ? parsed : undefined
    } catch {
      return undefined
    }
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function stringArray(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value
    .filter((item): item is string => typeof item === 'string')
    .map((item) => item.slice(0, 300))
}

function boundedNumber(value: unknown, min: number, max: number): number {
  const parsed = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(parsed)) return min
  return Math.min(max, Math.max(min, Number(parsed.toFixed(2))))
}

function roundRate(value: number): number {
  return Number(value.toFixed(4))
}

function roundScore(value: number): number {
  return Number(value.toFixed(2))
}

function safeJsonObject(text: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(text) as unknown
    return isRecord(parsed) ? parsed : {}
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

function chatCompletionsUrl(baseUrl: string): string {
  const url = new URL(baseUrl)
  const normalizedPath = url.pathname.replace(/\/+$/, '')
  url.pathname = `${normalizedPath.endsWith('/v1') ? normalizedPath : `${normalizedPath}/v1`}/chat/completions`
  return url.toString()
}

function modelCostDefault(model: string): number {
  if (model === lowModel || model.includes('mini') || model.includes('flash')) return 0.002
  if (model.includes('glm')) return 0.01
  if (model.includes('gpt-5.5')) return 0.02
  if (model.includes('opus')) return 0.05
  return 0.02
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

function booleanEnv(name: string): boolean | undefined {
  const value = envText(name)
  if (!value) return undefined
  if (['1', 'true', 'yes', 'on'].includes(value.toLowerCase())) return true
  if (['0', 'false', 'no', 'off'].includes(value.toLowerCase())) return false
  return undefined
}

function sanitizeErrorSnippet(value: string): string {
  const redacted = value
    .replaceAll(realApiKey, '[redacted-real-api-key]')
    .slice(0, 800)
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

function qualityCases(): QualityCase[] {
  return [
  {
    id: 'quality-001',
    title: '轻量项目管理 API 设计',
    category: 'small_project_backend',
    prompt: '设计一个轻量项目管理服务，支持项目、任务、成员、任务状态流转和按项目统计未完成任务。要求使用 Node.js + SQLite，面向单机部署，不引入 Redis 或消息队列。',
    acceptanceCriteria: [
      '给出核心表结构和关键字段',
      '覆盖项目、任务、成员和状态流转接口',
      '说明权限或成员边界',
      '说明统计不能实时全表扫描的处理方式',
      '包含关键测试用例'
    ]
  },
  {
    id: 'quality-002',
    title: 'API Key 多分组故障定位',
    category: 'gateway_debug',
    prompt: '一个 OpenAI-compatible 网关里，API Key 绑定了 3 个分组。用户反馈同一个 model 有时 503，有时成功。请设计排障流程和需要新增的审计字段，要求能定位到分组、账号、模型映射和上游错误。',
    acceptanceCriteria: [
      '按认证、分组筛选、账号预检、上游响应顺序排查',
      '明确记录 groupId、accountId、targetModel、mappedModel',
      '区分本地无可用账号和上游返回错误',
      '包含最小复现或自动回归思路',
      '说明敏感凭据不能写入日志'
    ]
  },
  {
    id: 'quality-003',
    title: 'Vue API Key 编辑弹窗改造',
    category: 'frontend_form',
    prompt: '为 Vue 3 + Ant Design Vue 的 API Key 编辑弹窗增加“混合路由”配置。用户可以选择评分模型、1-10 等级区间、目标模型和缓存亲和参数；模型只能来自模型目录下拉。请给出组件状态、校验规则和交互方案。',
    acceptanceCriteria: [
      '区分 normal 和 hybrid 两种模式',
      '等级区间必须覆盖 1-10 且不能重叠',
      '混合模式允许绑定不同供应商协议分组',
      '给出中文错误提示和空态',
      '说明保存 payload 和回显转换'
    ]
  },
  {
    id: 'quality-004',
    title: '使用记录预聚合设计',
    category: 'stats_design',
    prompt: '设计一个 usage_record_shard_entries 的按小时预聚合 worker。要求支持 apiKey、account、model、trafficSource 维度，不能在 API 请求链路实时 SUM 明细表。',
    acceptanceCriteria: [
      '给出聚合表或窗口表字段',
      '说明游标推进和断点恢复',
      '说明轮转、重复消费或幂等处理',
      'API 只读取预聚合结果',
      '包含重建脚本或校验方式'
    ]
  },
  {
    id: 'quality-005',
    title: '流式响应污染检测',
    category: 'streaming',
    prompt: '网关需要检测 SSE 流式响应里是否出现广告污染文本。一旦命中，需要停止向客户端继续输出并切换备用账号重试。请设计状态机、缓冲策略和测试方案。',
    acceptanceCriteria: [
      '说明 SSE chunk 解析和跨 chunk 文本匹配',
      '说明命中后如何停止旧流并避免半截污染继续输出',
      '说明备用账号重试条件和最大重试次数',
      '兼顾 JSON 非流式响应',
      '包含 mock 上游测试场景'
    ]
  },
  {
    id: 'quality-006',
    title: '模型价格目录同步',
    category: 'model_catalog',
    prompt: '设计一个第三方模型价格目录同步功能。输入是多个供应商返回的模型列表和价格字段，输出是本地统一模型目录。要求处理别名、小写模型 ID、缓存输入价和人工覆盖。',
    acceptanceCriteria: [
      '模型 ID 按真实可调用小写 ID 保存',
      '区分供应商价格和用户人工覆盖',
      '覆盖 cached input 价格',
      '说明清洗、去重和冲突处理',
      '包含回归测试点'
    ]
  },
  {
    id: 'quality-007',
    title: '混合路由缓存亲和算法',
    category: 'hybrid_routing',
    prompt: '为多模型混合路由设计缓存亲和算法。输入包括本次评分 level、上次命中模型、上次 level、cacheReadTokens、inputTokens 和目标等级规则。输出是否切换模型。',
    acceptanceCriteria: [
      '同档位不切换',
      '小等级波动不切换',
      '连续低分后允许高价模型降档',
      '缓存命中率高时提高切换门槛',
      '给出伪代码和边界测试'
    ]
  },
  {
    id: 'quality-008',
    title: '账户导入字段解析',
    category: 'import_parser',
    prompt: '用户上传 CSV 批量导入 OpenAI-compatible 账户，字段可能包含 name、api_key、base_url、models、proxy、group。请设计解析、校验、预览和提交流程，要求失败行不影响成功行。',
    acceptanceCriteria: [
      '解析阶段和提交阶段分离',
      '逐行错误定位',
      '模型列表要 trim、去空、保留小写真实 ID',
      '重复 key 或重复名称处理清晰',
      '包含预览 payload 和测试点'
    ]
  },
  {
    id: 'quality-009',
    title: '权限矩阵设计',
    category: 'authorization',
    prompt: '设计系统账户、团队、资源授权的权限矩阵。资源包括 AI 账户、分组、API Key、使用记录和审计日志。要求普通团队成员只能看授权资源，管理员能看全局。',
    acceptanceCriteria: [
      '列出角色和资源动作',
      '说明列表、详情、创建、更新、删除边界',
      '使用记录和审计日志按资源授权过滤',
      '避免前端单独承担权限',
      '包含接口测试用例'
    ]
  },
  {
    id: 'quality-010',
    title: '网关错误分类',
    category: 'error_handling',
    prompt: '为网关设计错误分类和响应策略。需要区分认证失败、无可用分组、账号冷却、上游 429、上游 5xx、模型不存在、上下文过长和余额不足。',
    acceptanceCriteria: [
      '给出本地错误码和 HTTP 状态映射',
      '区分可重试和不可重试',
      '说明哪些错误会触发账号冷却',
      '说明返回给客户端的信息如何脱敏',
      '包含审计和 usage 记录口径'
    ]
  },
  {
    id: 'quality-011',
    title: '小型配额系统',
    category: 'quota',
    prompt: '设计一个 API Key 配额系统，支持日额度、月额度、并发限制和成本预算提醒。要求请求链路快速判断，统计由后台增量聚合。',
    acceptanceCriteria: [
      '给出配额字段和窗口表',
      '说明请求前快速判断用的数据来源',
      '后台增量聚合不能扫明细',
      '说明超额响应和提醒触发',
      '包含并发冲突或延迟统计的处理'
    ]
  },
  {
    id: 'quality-012',
    title: '代码审查题',
    category: 'code_review',
    prompt: [
      '审查下面的 TypeScript 伪代码，指出 bug 和修复方式：',
      '```ts',
      'async function listUsage(page: number, pageSize: number) {',
      '  const rows = db.prepare("select * from usage_record_shard_entries").all()',
      '  const filtered = rows.filter(row => row.success === 1)',
      '  const totalCost = filtered.reduce((sum, row) => sum + row.cost_usd, 0)',
      '  return { totalCost, rows: filtered.slice((page - 1) * pageSize, page * pageSize) }',
      '}',
      '```',
      '要求结合大数据量、分页、统计口径和空值处理给出修改建议。'
    ].join('\n'),
    acceptanceCriteria: [
      '指出全表读取和内存过滤问题',
      '指出请求链路实时聚合问题',
      '指出 cost_usd 为空或类型问题',
      '给出 cursor/window 或预聚合替代方案',
      '包含测试或性能验证'
    ]
  },
  {
    id: 'quality-013',
    title: 'OpenAI-compatible 代理健康检查',
    category: 'account_health',
    prompt: '设计账户健康检查功能。账号可能绑定代理，支持 OpenAI-compatible /v1/models 和 /v1/chat/completions。要求测试失败不能误伤生产调度，健康检查要有冷却和恢复机制。',
    acceptanceCriteria: [
      '区分手动测试、后台探活和真实网关请求',
      '说明代理错误、认证错误、模型错误的分类',
      '说明冷却、恢复和状态流转',
      '测试成本和 usage 口径清晰',
      '包含真实上游不稳定时的处理'
    ]
  },
  {
    id: 'quality-014',
    title: '小项目测试计划',
    category: 'test_plan',
    prompt: '给一个“API Key 级混合模型路由”的完整测试计划，覆盖配置保存、绑定分组、评分模型失败、目标模型不可用、缓存亲和、usage 统计和真实上游验证。',
    acceptanceCriteria: [
      '覆盖后端单元或回归测试',
      '覆盖前端表单校验',
      '覆盖 mock AI 和真实 AI 两类验证',
      '包含成本和正确率指标',
      '说明失败时如何定位'
    ]
  },
  {
    id: 'quality-015',
    title: '审计日志 payload 存储',
    category: 'audit_storage',
    prompt: '设计审计日志 payload 存储方案。小 payload 可以直接入库，大 payload 写 blob 文件并建索引。要求支持分页查看、关键字搜索和按请求 ID 追踪。',
    acceptanceCriteria: [
      '区分元数据表和 blob 文件',
      '大文件读取必须 offset/window',
      '说明索引字段和请求 ID 关联',
      '说明脱敏规则',
      '包含轮转、清理和测试'
    ]
  },
  {
    id: 'quality-016',
    title: '端到端小项目实现拆解',
    category: 'small_project_fullstack',
    prompt: '拆解一个“模型用量看板”小项目：后端提供按 API Key、模型、小时聚合的接口，前端提供筛选、趋势图、TopN 和异常请求列表。要求给出端到端实现步骤。',
    acceptanceCriteria: [
      '后端接口和查询参数清晰',
      '数据来自预聚合表而不是明细实时扫描',
      '前端状态、加载、空态和错误态明确',
      '说明 TopN 和趋势图数据结构',
      '包含端到端验收用例'
    ]
  },
  {
    id: 'quality-017',
    title: 'SQLite 数据结构调整方案',
    category: 'data_migration',
    prompt: '当前 API Key 表需要新增 hybrid_quality_inspection_json 字段，并调整 usage 记录来源枚举。项目不做运行时代码兼容旧结构。请设计当前 schema 调整、离线同步边界、repository 校验和回归测试。',
    acceptanceCriteria: [
      '说明当前 schema 字段和默认值',
      '明确不在运行时代码做旧结构兼容',
      '给出 repository 写入和读取校验',
      '说明 usage trafficSource 枚举扩展影响',
      '包含离线同步或部署前检查建议'
    ]
  },
  {
    id: 'quality-018',
    title: '前端筛选状态回归',
    category: 'frontend_state',
    prompt: '一个使用记录页面新增 trafficSource=hybrid_quality_scoring 后，用户反馈筛选项能选但刷新页面后状态丢失。请设计定位和修复方案，覆盖类型定义、URL/query 状态、表格标签和移动端卡片。',
    acceptanceCriteria: [
      '检查 domain 类型和筛选工具栏类型',
      '检查页面状态序列化和恢复',
      '检查标签文案和颜色映射',
      '覆盖桌面表格和移动端卡片',
      '给出前端回归测试点'
    ]
  },
  {
    id: 'quality-019',
    title: '发布失败回滚预案',
    category: 'deploy_ops',
    prompt: '设计一次轻量 Node + Vue 项目发布失败回滚预案。场景：新版本引入混合路由配置后，部分 API Key 保存失败。要求给出发布前检查、灰度验证、回滚触发条件和数据处理边界。',
    acceptanceCriteria: [
      '包含发布前 schema 和环境检查',
      '包含灰度 API Key 保存和真实请求验证',
      '明确回滚触发条件和日志定位',
      '说明新增字段数据不在运行时代码回滚中双写',
      '包含用户影响和告警口径'
    ]
  },
  {
    id: 'quality-020',
    title: '长上下文缓存收益分析',
    category: 'cache_cost',
    prompt: '一个会话连续 12 次请求，前 2 次规划很长，后 10 次是按计划执行。模型切换可能破坏 KV/prompt cache。请设计混合路由如何结合评分、缓存亲和、cacheReadTokens、inputTokens 和模型价格判断是否切换。',
    acceptanceCriteria: [
      '区分任务难度评分和缓存收益判断',
      '说明长上下文高 cache read 时提高切换门槛',
      '说明低风险短请求可以更积极降档',
      '给出成本估算公式或伪代码',
      '包含边界测试和审计字段'
    ]
  }
  ]
}
