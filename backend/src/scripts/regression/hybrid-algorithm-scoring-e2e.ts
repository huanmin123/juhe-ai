import { strict as assert } from 'node:assert'
import { channel } from 'node:diagnostics_channel'
import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import { join, resolve } from 'node:path'
import { tmpdir } from 'node:os'

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

interface CompletionResult {
  attempts?: number
  clientRequestId: string
  content: string
  durationMs: number
  error?: string
  model?: string
  ok: boolean
  retryErrors?: string[]
  status: number
}

interface ValidationResult {
  actual?: unknown
  error?: string
  ok: boolean
  reason: string
}

interface AlgorithmCase {
  id: string
  title: string
  difficulty: 'easy' | 'medium' | 'hard'
  prompt: string
  validate(answer: unknown): ValidationResult
}

interface HybridRouteEvent {
  affinityApplied?: boolean
  affinityReason?: string
  clientRequestId?: string
  confidence?: number
  level?: number
  levelRange?: [number, number]
  outcome?: string
  scoringCacheHit?: boolean
  scoringDefaulted?: boolean
  scoringErrorCode?: string
  scoringErrorMessage?: string
  scoringFactors?: string[]
  scoringReason?: string
  sessionId?: string
  targetModel?: string
}

interface CaseRunResult {
  case: {
    difficulty: AlgorithmCase['difficulty']
    id: string
    title: string
  }
  completion: CompletionResult
  parsedAnswer?: unknown
  routeEvents: HybridRouteEvent[]
  selectedRoute?: HybridRouteEvent
  validation: ValidationResult
}

const realApiKey = requiredEnv('JUHE_REAL_HYBRID_ALGORITHM_API_KEY', [
  'JUHE_REAL_HYBRID_API_KEY',
  'JUHE_REAL_HYBRID_QUALITY_API_KEY',
  'HYBRID_REAL_API_KEY'
])
const realBaseUrl = envText('JUHE_REAL_HYBRID_ALGORITHM_BASE_URL', [
  'JUHE_REAL_HYBRID_BASE_URL',
  'JUHE_REAL_HYBRID_QUALITY_BASE_URL',
  'HYBRID_REAL_BASE_URL'
]) || 'https://vsllm.com'
const scoringModel = envText('JUHE_REAL_HYBRID_ALGORITHM_SCORING_MODEL') || 'gpt-5.4-mini'
const lowModel = envText('JUHE_REAL_HYBRID_ALGORITHM_LOW_MODEL') || 'gpt-5.4-mini'
const midModel = envText('JUHE_REAL_HYBRID_ALGORITHM_MID_MODEL') || 'gpt-5.4'
const highModel = envText('JUHE_REAL_HYBRID_ALGORITHM_HIGH_MODEL') || 'gpt-5.5'
const caseLimit = Math.min(algorithmCases().length, Math.max(1, positiveIntegerEnv('JUHE_REAL_HYBRID_ALGORITHM_CASES') ?? algorithmCases().length))
const requestTimeoutMs = positiveIntegerEnv('JUHE_REAL_HYBRID_ALGORITHM_REQUEST_TIMEOUT_MS') ?? 120_000
const requestIntervalMs = positiveIntegerEnv('JUHE_REAL_HYBRID_ALGORITHM_REQUEST_INTERVAL_MS') ?? 6_500
const upstreamRetryCount = positiveIntegerEnv('JUHE_REAL_HYBRID_ALGORITHM_UPSTREAM_RETRIES') ?? 5
const upstreamRetryDelayMs = positiveIntegerEnv('JUHE_REAL_HYBRID_ALGORITHM_UPSTREAM_RETRY_DELAY_MS') ?? 5_000
const outputMaxTokens = positiveIntegerEnv('JUHE_REAL_HYBRID_ALGORITHM_OUTPUT_MAX_TOKENS') ?? 1_200
const outputPath = envText('JUHE_REAL_HYBRID_ALGORITHM_OUTPUT_PATH')
const levelRoutes = configuredLevelRoutes()
const routeTargetModels = [...new Set(levelRoutes.map((route) => route.targetModel))]
const hybridRouteEvents: HybridRouteEvent[] = []
const hybridRouteDiagnosticsChannel = channel('juhe-ai:hybrid-route-decision')
const hybridRouteDiagnosticsSubscriber = (message: unknown): void => {
  if (typeof message === 'object' && message !== null) {
    hybridRouteEvents.push(message as HybridRouteEvent)
  }
}
hybridRouteDiagnosticsChannel.subscribe(hybridRouteDiagnosticsSubscriber)

const tempRoot = resolve(tmpdir(), `juhe-ai-hybrid-algorithm-scoring-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'hybrid-algorithm-scoring.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'hybrid-algorithm-scoring-secret'
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
  hybridAffinity,
  scoringService
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
const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '2mb' }), captureGatewayRawBody, async (req, res, next) => {
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
  scoringService.clearHybridScoringCacheForTest()
  let appServer: http.Server | undefined
  try {
    registerAlgorithmCustomModels()
    const scoring = createRealGroupAccount('Hybrid Algorithm 评分分组', 'Hybrid Algorithm 评分账户', scoringModel)
    const groupsByModel = new Map<string, { accountId: string; groupId: string }>([[scoringModel, scoring]])
    for (const model of routeTargetModels) {
      if (!groupsByModel.has(model)) {
        groupsByModel.set(model, createRealGroupAccount(`Hybrid Algorithm ${model} 分组`, `Hybrid Algorithm ${model} 账户`, model))
      }
    }
    const hybridApiKey = repositories.createApiKeyRecord({
      name: 'Hybrid Algorithm Scoring Key',
      routeMode: 'hybrid',
      groupRouteStrategy: 'priority_failover',
      groupBindings: uniqueGroupBindings([scoring, ...groupsByModel.values()]).map((item, index) => ({
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
    assert(hybridApiKey.key, '算法评分混合 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`
    const selectedCases = algorithmCases().slice(0, caseLimit)
    const results: CaseRunResult[] = []

    for (const [index, testCase] of selectedCases.entries()) {
      if (index > 0 && requestIntervalMs > 0) {
        await wait(requestIntervalMs)
      }
      console.error(`[hybrid-algorithm-scoring] ${testCase.id}: start`)
      const result = await runCase(baseUrl, hybridApiKey.key, testCase)
      results.push(result)
      console.error(`[hybrid-algorithm-scoring] ${testCase.id}: level=${result.selectedRoute?.level ?? 'n/a'} model=${result.selectedRoute?.targetModel ?? 'n/a'} validation=${result.validation.ok ? 'ok' : 'failed'}`)
      writeSummary({ results, selectedCases })
    }

    usageRecordQueue.flushAllUsageRecordQueue()
    const summary = buildSummary({ results, selectedCases })
    console.log(JSON.stringify(summary, null, 2))
  } finally {
    await closeServer(appServer)
  }
} finally {
  hybridRouteDiagnosticsChannel.unsubscribe(hybridRouteDiagnosticsSubscriber)
  hybridAffinity.clearHybridRouteAffinityForTest()
  scoringService.clearHybridScoringCacheForTest()
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function registerAlgorithmCustomModels(): void {
  for (const model of new Set([scoringModel, ...routeTargetModels])) {
    saveAlgorithmCustomModel(OPENAI_COMPATIBLE_PROVIDER_CODE, model, modelCostDefault(model))
  }
}

function saveAlgorithmCustomModel(providerCode: ProviderCode, model: string, unitCost: number): void {
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
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    name: accountName,
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
    concurrencyLimit: 1,
    supportedModels: [supportedModel]
  }, access)
  assert.deepEqual(account.supportedModels, [supportedModel])
  return { accountId: account.id, groupId: group.id }
}

function uniqueGroupBindings(items: Iterable<{ accountId: string; groupId: string }>): Array<{ accountId: string; groupId: string }> {
  const seen = new Set<string>()
  const result: Array<{ accountId: string; groupId: string }> = []
  for (const item of items) {
    if (seen.has(item.groupId)) continue
    seen.add(item.groupId)
    result.push(item)
  }
  return result
}

async function runCase(baseUrl: string, localApiKey: string, testCase: AlgorithmCase): Promise<CaseRunResult> {
  const completion = await callGatewayCompletion(baseUrl, localApiKey, [
    {
      role: 'system',
      content: '你是算法题解答助手。必须只输出严格 JSON，不要 Markdown，不要代码块。'
    },
    {
      role: 'user',
      content: testCase.prompt
    }
  ], `hybrid-algorithm-${testCase.id}`)
  const routeEvents = routeEventsForClientRequest(completion.clientRequestId)
  const selectedRoute = lastSelectedRouteEvent(routeEvents)
  const parsedAnswer = parseAnswerValue(completion.content)
  const validation = completion.ok
    ? parsedAnswer.ok
      ? testCase.validate(parsedAnswer.value)
      : { ok: false, reason: '未能从响应中解析 answer JSON', error: parsedAnswer.error }
    : { ok: false, reason: '请求未成功', error: completion.error }
  return {
    case: {
      difficulty: testCase.difficulty,
      id: testCase.id,
      title: testCase.title
    },
    completion,
    parsedAnswer: parsedAnswer.ok ? parsedAnswer.value : undefined,
    routeEvents,
    selectedRoute,
    validation
  }
}

async function callGatewayCompletion(
  baseUrl: string,
  localApiKey: string,
  messages: Array<{ role: string; content: string }>,
  sessionId: string
): Promise<CompletionResult> {
  const clientRequestId = `${sessionId}-${Date.now()}-${Math.random().toString(16).slice(2)}`
  return callWithRetries(() => callGatewayCompletionOnce(baseUrl, localApiKey, messages, sessionId, clientRequestId), clientRequestId)
}

async function callGatewayCompletionOnce(
  baseUrl: string,
  localApiKey: string,
  messages: Array<{ role: string; content: string }>,
  sessionId: string,
  clientRequestId: string
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
        'x-session-id': sessionId,
        'x-client-request-id': clientRequestId
      },
      body: JSON.stringify({
        model: 'algorithm-hybrid-router',
        messages,
        stream: false,
        max_tokens: outputMaxTokens,
        temperature: 0
      }),
      signal: controller.signal
    })
    const text = await response.text()
    const body = safeJsonObject(text)
    const content = firstAssistantContent(body)
    return {
      clientRequestId,
      content: content ?? '',
      durationMs: Date.now() - startedAt,
      error: response.ok && content ? undefined : sanitizeErrorSnippet(text),
      model: typeof body.model === 'string' ? body.model : undefined,
      ok: response.ok && Boolean(content),
      status: response.status
    }
  } catch (error) {
    return {
      clientRequestId,
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

async function callWithRetries(callOnce: () => Promise<CompletionResult>, clientRequestId: string): Promise<CompletionResult> {
  const startedAt = Date.now()
  const retryErrors: string[] = []
  let lastResult: CompletionResult | undefined
  for (let attempt = 1; attempt <= upstreamRetryCount + 1; attempt += 1) {
    const result = await callOnce()
    lastResult = result
    if (result.ok || !isRetryableCallResult(result) || attempt > upstreamRetryCount) {
      return {
        ...result,
        attempts: attempt,
        durationMs: Date.now() - startedAt,
        retryErrors: retryErrors.length ? retryErrors : undefined
      }
    }
    retryErrors.push(callRetrySummary(result))
    await wait(upstreamRetryDelayMs)
  }
  return {
    ...(lastResult ?? {
      clientRequestId,
      content: '',
      error: 'retry loop exited without result',
      ok: false,
      status: 0
    }),
    attempts: upstreamRetryCount + 1,
    durationMs: Date.now() - startedAt,
    retryErrors: retryErrors.length ? retryErrors : undefined
  }
}

function isRetryableCallResult(result: CompletionResult): boolean {
  if (result.ok) return false
  if ([0, 401, 408, 409, 425, 429, 500, 502, 503, 504, 520, 522, 524].includes(result.status)) {
    return true
  }
  const error = result.error?.toLowerCase() ?? ''
  return error.includes('aborted') ||
    error.includes('timeout') ||
    error.includes('timed out') ||
    error.includes('auth_unavailable') ||
    error.includes('invalid authentication credentials')
}

function callRetrySummary(result: CompletionResult): string {
  const error = result.error ? result.error.replace(/\s+/g, ' ').slice(0, 180) : 'empty error'
  return `status=${result.status} ${error}`
}

function buildSummary(input: {
  results: CaseRunResult[]
  selectedCases: AlgorithmCase[]
}): Record<string, unknown> {
  const completed = input.results.filter((item) => item.completion.ok)
  const valid = input.results.filter((item) => item.validation.ok)
  const selectedRoutes = input.results.map((item) => item.selectedRoute).filter((item): item is HybridRouteEvent => Boolean(item))
  const levels = selectedRoutes.map((item) => item.level).filter((item): item is number => typeof item === 'number')
  return {
    ok: input.results.length === input.selectedCases.length && input.results.every((item) => item.completion.ok && item.validation.ok && item.selectedRoute),
    baseUrl: sanitizeBaseUrl(realBaseUrl),
    caseCount: input.selectedCases.length,
    completedCount: completed.length,
    validationPassCount: valid.length,
    retryPolicy: {
      maxRetries: upstreamRetryCount,
      maxAttempts: upstreamRetryCount + 1,
      retryDelayMs: upstreamRetryDelayMs,
      requestIntervalMs
    },
    models: {
      scoringModel,
      routeModels: levelRoutes.map((route) => `${route.minLevel}-${route.maxLevel}:${route.targetModel}`)
    },
    scoringDistribution: {
      minLevel: levels.length ? Math.min(...levels) : undefined,
      maxLevel: levels.length ? Math.max(...levels) : undefined,
      averageLevel: levels.length ? roundNumber(levels.reduce((sum, item) => sum + item, 0) / levels.length) : undefined,
      countsByLevel: countBy(levels.map(String))
    },
    targetModelCounts: countBy(selectedRoutes.map((item) => item.targetModel ?? 'unknown')),
    responseModelCounts: countBy(input.results.map((item) => item.completion.model ?? 'unknown')),
    cases: input.results.map((item) => ({
      id: item.case.id,
      title: item.case.title,
      difficulty: item.case.difficulty,
      completionOk: item.completion.ok,
      status: item.completion.status,
      attempts: item.completion.attempts,
      durationMs: item.completion.durationMs,
      validation: item.validation,
      route: item.selectedRoute ? {
        level: item.selectedRoute.level,
        confidence: item.selectedRoute.confidence,
        factors: item.selectedRoute.scoringFactors,
        reason: item.selectedRoute.scoringReason,
        targetModel: item.selectedRoute.targetModel,
        levelRange: item.selectedRoute.levelRange,
        defaulted: item.selectedRoute.scoringDefaulted,
        cacheHit: item.selectedRoute.scoringCacheHit,
        affinityApplied: item.selectedRoute.affinityApplied
      } : undefined,
      responseModel: item.completion.model,
      retryErrors: item.completion.retryErrors
    }))
  }
}

function writeSummary(input: {
  results: CaseRunResult[]
  selectedCases: AlgorithmCase[]
}): void {
  if (!outputPath) return
  writeFileSync(outputPath, `${JSON.stringify(buildSummary(input), null, 2)}\n`, 'utf8')
}

function configuredLevelRoutes(): ApiKeyHybridRoutingConfig['levelRoutes'] {
  const configured = envText('JUHE_REAL_HYBRID_ALGORITHM_LEVEL_ROUTES_JSON')
  if (!configured) {
    return [
      { minLevel: 1, maxLevel: 2, targetModel: lowModel, enabled: true },
      { minLevel: 3, maxLevel: 7, targetModel: midModel, enabled: true },
      { minLevel: 8, maxLevel: 10, targetModel: highModel, enabled: true }
    ]
  }
  const parsed = JSON.parse(configured) as unknown
  if (!Array.isArray(parsed)) {
    throw new Error('JUHE_REAL_HYBRID_ALGORITHM_LEVEL_ROUTES_JSON 必须是数组')
  }
  return parsed.map((item) => {
    if (!item || typeof item !== 'object' || Array.isArray(item)) {
      throw new Error('算法评分档位配置项必须是对象')
    }
    const record = item as Record<string, unknown>
    const minLevel = Number(record.minLevel)
    const maxLevel = Number(record.maxLevel)
    const targetModel = typeof record.targetModel === 'string' ? record.targetModel.trim() : ''
    if (!Number.isInteger(minLevel) || !Number.isInteger(maxLevel) || minLevel < 1 || maxLevel > 10 || minLevel > maxLevel || !targetModel) {
      throw new Error('算法评分档位配置必须包含合法 minLevel、maxLevel 和 targetModel')
    }
    return {
      minLevel,
      maxLevel,
      targetModel,
      enabled: record.enabled !== false
    }
  })
}

function routeEventsForClientRequest(clientRequestId: string): HybridRouteEvent[] {
  return hybridRouteEvents.filter((item) => item.clientRequestId === clientRequestId)
}

function lastSelectedRouteEvent(events: HybridRouteEvent[]): HybridRouteEvent | undefined {
  for (let index = events.length - 1; index >= 0; index -= 1) {
    if (events[index]?.outcome === 'selected') return events[index]
  }
  return undefined
}

function parseAnswerValue(content: string): { error?: string; ok: false } | { ok: true; value: unknown } {
  const parsed = parseJsonObjectLoose(content)
  if (!parsed) return { ok: false, error: content.slice(0, 500) }
  return { ok: true, value: Object.hasOwn(parsed, 'answer') ? parsed.answer : parsed }
}

function parseJsonObjectLoose(text: string): Record<string, unknown> | undefined {
  const trimmed = text.trim()
  const candidates = [
    trimmed,
    trimmed.replace(/^```(?:json)?/i, '').replace(/```$/i, '').trim(),
    jsonObjectSlice(trimmed)
  ].filter((item): item is string => Boolean(item))
  for (const candidate of candidates) {
    try {
      const parsed = JSON.parse(candidate) as unknown
      if (typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)) {
        return parsed as Record<string, unknown>
      }
    } catch {
      // Try next candidate.
    }
  }
  return undefined
}

function jsonObjectSlice(text: string): string | undefined {
  const start = text.indexOf('{')
  const end = text.lastIndexOf('}')
  return start >= 0 && end > start ? text.slice(start, end + 1) : undefined
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

function algorithmCases(): AlgorithmCase[] {
  return [
    {
      id: 'sum-small',
      title: '简单加法',
      difficulty: 'easy',
      prompt: algorithmPrompt([
        '计算 37 + 58。',
        'answer 必须是数字。'
      ]),
      validate: (answer) => validateNumber(answer, 95)
    },
    {
      id: 'factorial',
      title: '阶乘计算',
      difficulty: 'easy',
      prompt: algorithmPrompt([
        '计算 7!。',
        'answer 必须是数字。'
      ]),
      validate: (answer) => validateNumber(answer, 5040)
    },
    {
      id: 'binary-search',
      title: '二分查找索引',
      difficulty: 'easy',
      prompt: algorithmPrompt([
        '数组 nums = [1, 4, 9, 12, 17, 23, 31]，target = 17。',
        '返回 target 的 0 基索引。',
        'answer 必须是数字。'
      ]),
      validate: (answer) => validateNumber(answer, 4)
    },
    {
      id: 'two-sum',
      title: '两数之和',
      difficulty: 'medium',
      prompt: algorithmPrompt([
        '数组 nums = [2, 7, 11, 15]，target = 9。',
        '返回两个数的 0 基索引，顺序不限。',
        'answer 必须是数字数组，例如 [0,1]。'
      ]),
      validate: (answer) => validateNumberArray(answer, [0, 1], { orderMatters: false })
    },
    {
      id: 'valid-parentheses',
      title: '括号有效性',
      difficulty: 'medium',
      prompt: algorithmPrompt([
        '判断两个括号字符串是否有效。',
        'case1 = "({[]})[]"，case2 = "([)]"。',
        'answer 必须是对象 {"case1":true,"case2":false}。'
      ]),
      validate: validateParenthesesAnswer
    },
    {
      id: 'lru-sequence',
      title: 'LRU 缓存序列',
      difficulty: 'medium',
      prompt: algorithmPrompt([
        'LRU 缓存容量为 2，初始为空。',
        '依次执行：put(1,1), put(2,2), get(1), put(3,3), get(2), put(4,4), get(1), get(3), get(4)。',
        '只返回每次 get 的结果。',
        'answer 必须是数字数组。'
      ]),
      validate: (answer) => validateNumberArray(answer, [1, -1, -1, 3, 4], { orderMatters: true })
    },
    {
      id: 'dijkstra-path',
      title: '最短路径',
      difficulty: 'hard',
      prompt: algorithmPrompt([
        '无向加权图边如下：A-B=1，A-C=4，B-C=2，B-D=5，C-D=1。',
        '求 A 到 D 的最短路径和总代价。',
        'answer 必须是对象 {"cost":数字,"path":["A","B","C","D"]}，如果存在等价最短路径也要返回其中一条。'
      ]),
      validate: validateDijkstraAnswer
    },
    {
      id: 'weighted-interval',
      title: '加权区间调度',
      difficulty: 'hard',
      prompt: algorithmPrompt([
        '有 5 个任务，格式为 [id,start,end,value]：',
        '[A,1,3,5]，[B,2,5,6]，[C,4,6,5]，[D,6,7,4]，[E,5,8,11]。',
        '选择互不重叠任务，end <= next.start 视为不重叠，使总 value 最大。',
        'answer 必须是对象 {"maxValue":数字,"jobs":["..."]}。'
      ]),
      validate: validateWeightedIntervalAnswer
    }
  ]
}

function algorithmPrompt(lines: string[]): string {
  return [
    '请解决下面算法题。',
    '只输出严格 JSON，不要 Markdown，不要代码块。',
    'JSON 结构必须是 {"answer":...,"explanation":"一句简短说明"}。',
    ...lines
  ].join('\n')
}

function validateNumber(answer: unknown, expected: number): ValidationResult {
  const actual = numericValue(answer)
  if (actual === expected) {
    return { actual, ok: true, reason: `answer=${expected}` }
  }
  return { actual, ok: false, reason: `期望 ${expected}，实际 ${String(actual)}` }
}

function validateNumberArray(answer: unknown, expected: number[], options: { orderMatters: boolean }): ValidationResult {
  const actual = numberArrayValue(answer)
  const normalizedActual = options.orderMatters ? actual : [...actual].sort((left, right) => left - right)
  const normalizedExpected = options.orderMatters ? expected : [...expected].sort((left, right) => left - right)
  const ok = normalizedActual.length === normalizedExpected.length && normalizedActual.every((item, index) => item === normalizedExpected[index])
  return {
    actual,
    ok,
    reason: ok ? `answer=[${expected.join(',')}]` : `期望 [${expected.join(',')}]，实际 [${actual.join(',')}]`
  }
}

function validateParenthesesAnswer(answer: unknown): ValidationResult {
  const record = objectValue(answer)
  const case1 = booleanValue(record.case1 ?? record.valid1 ?? record.first)
  const case2 = booleanValue(record.case2 ?? record.valid2 ?? record.second)
  const ok = case1 === true && case2 === false
  return {
    actual: { case1, case2 },
    ok,
    reason: ok ? 'case1=true, case2=false' : `期望 case1=true 且 case2=false，实际 ${JSON.stringify({ case1, case2 })}`
  }
}

function validateDijkstraAnswer(answer: unknown): ValidationResult {
  const record = objectValue(answer)
  const cost = numericValue(record.cost ?? record.distance ?? record.totalCost)
  const path = stringArrayValue(record.path)
  const ok = cost === 4 && path.join('>') === 'A>B>C>D'
  return {
    actual: { cost, path },
    ok,
    reason: ok ? 'cost=4,path=A>B>C>D' : `期望 cost=4,path=A>B>C>D，实际 ${JSON.stringify({ cost, path })}`
  }
}

function validateWeightedIntervalAnswer(answer: unknown): ValidationResult {
  const record = objectValue(answer)
  const maxValue = numericValue(record.maxValue ?? record.value ?? record.totalValue)
  const jobs = stringArrayValue(record.jobs ?? record.selectedJobs ?? record.path).map((item) => item.toUpperCase()).sort()
  const ok = maxValue === 17 && jobs.join(',') === 'B,E'
  return {
    actual: { jobs, maxValue },
    ok,
    reason: ok ? 'maxValue=17,jobs=B,E' : `期望 maxValue=17,jobs=B,E，实际 ${JSON.stringify({ jobs, maxValue })}`
  }
}

function objectValue(value: unknown): Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {}
}

function numericValue(value: unknown): number | undefined {
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'string') {
    const parsed = Number(value.trim())
    return Number.isFinite(parsed) ? parsed : undefined
  }
  return undefined
}

function numberArrayValue(value: unknown): number[] {
  if (Array.isArray(value)) {
    return value.map((item) => numericValue(item)).filter((item): item is number => typeof item === 'number')
  }
  if (typeof value === 'string') {
    return value.match(/-?\d+(?:\.\d+)?/g)?.map(Number).filter(Number.isFinite) ?? []
  }
  return []
}

function stringArrayValue(value: unknown): string[] {
  if (Array.isArray(value)) {
    return value.map((item) => typeof item === 'string' ? item : String(item)).map((item) => item.trim()).filter(Boolean)
  }
  if (typeof value === 'string') {
    return value.split(/[^A-Za-z0-9_.-]+/).map((item) => item.trim()).filter(Boolean)
  }
  return []
}

function booleanValue(value: unknown): boolean | undefined {
  if (typeof value === 'boolean') return value
  if (typeof value === 'string') {
    if (value.toLowerCase() === 'true') return true
    if (value.toLowerCase() === 'false') return false
  }
  return undefined
}

function countBy(values: string[]): Record<string, number> {
  return values.reduce<Record<string, number>>((accumulator, item) => {
    accumulator[item] = (accumulator[item] ?? 0) + 1
    return accumulator
  }, {})
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

function modelCostDefault(model: string): number {
  if (model.includes('mini') || model.includes('flash')) return 0.002
  if (model.includes('glm-5.1')) return 0.006
  if (model.includes('glm')) return 0.01
  if (model.includes('gpt-5.4') && !model.includes('mini')) return 0.015
  if (model.includes('gpt-5.5')) return 0.02
  if (model.includes('opus')) return 0.05
  return 0.02
}

function roundNumber(value: number): number {
  return Number(value.toFixed(2))
}

function sanitizeErrorSnippet(value: string): string {
  return value.replaceAll(realApiKey, '[redacted-real-api-key]').slice(0, 1_200) || 'empty response'
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
