import { strict as assert } from 'node:assert'
import { spawnSync } from 'node:child_process'
import { channel } from 'node:diagnostics_channel'
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
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

interface PythonCodingCase {
  id: string
  title: string
  expectedComplexity: 'low' | 'medium' | 'high' | 'very_high'
  prompt: string
  tests: string
  timeoutMs: number
}

interface PythonValidationResult {
  codeBytes: number
  parseOk: boolean
  parseError?: string
  staticChecks: {
    issues: string[]
    ok: boolean
  }
  test: {
    durationMs: number
    exitCode: number | null
    ok: boolean
    output: string
  }
  ok: boolean
}

interface CaseRunResult {
  case: {
    expectedComplexity: PythonCodingCase['expectedComplexity']
    id: string
    title: string
  }
  completion: CompletionResult
  initial: {
    completion: CompletionResult
    routeEvents: HybridRouteEvent[]
    selectedRoute?: HybridRouteEvent
    validation: PythonValidationResult
  }
  repair?: {
    completion: CompletionResult
    round: number
    routeEvents: HybridRouteEvent[]
    selectedRoute?: HybridRouteEvent
    validation: PythonValidationResult
  }
  routeEvents: HybridRouteEvent[]
  selectedRoute?: HybridRouteEvent
  validation: PythonValidationResult
}

const realApiKey = requiredEnv('JUHE_REAL_HYBRID_PYTHON_API_KEY', [
  'JUHE_REAL_HYBRID_ALGORITHM_API_KEY',
  'JUHE_REAL_HYBRID_API_KEY',
  'JUHE_REAL_HYBRID_QUALITY_API_KEY',
  'HYBRID_REAL_API_KEY'
])
const realBaseUrl = envText('JUHE_REAL_HYBRID_PYTHON_BASE_URL', [
  'JUHE_REAL_HYBRID_ALGORITHM_BASE_URL',
  'JUHE_REAL_HYBRID_BASE_URL',
  'JUHE_REAL_HYBRID_QUALITY_BASE_URL',
  'HYBRID_REAL_BASE_URL'
]) || 'https://vsllm.com'
const pythonExecutable = envText('JUHE_REAL_HYBRID_PYTHON_EXECUTABLE') || envText('PYTHON') || 'python'
const scoringModel = envText('JUHE_REAL_HYBRID_PYTHON_SCORING_MODEL') || 'gpt-5.4-mini'
const lowModel = envText('JUHE_REAL_HYBRID_PYTHON_LOW_MODEL') || 'gpt-5.4-mini'
const midModel = envText('JUHE_REAL_HYBRID_PYTHON_MID_MODEL') || 'gpt-5.4'
const highModel = envText('JUHE_REAL_HYBRID_PYTHON_HIGH_MODEL') || 'gpt-5.5'
const allCases = pythonCodingCases()
const selectedCaseIds = configuredCaseIds()
const selectedCasePool = selectedCaseIds
  ? allCases.filter((item) => selectedCaseIds.includes(item.id))
  : allCases
if (selectedCaseIds && selectedCasePool.length !== selectedCaseIds.length) {
  const found = new Set(selectedCasePool.map((item) => item.id))
  const missing = selectedCaseIds.filter((item) => !found.has(item))
  throw new Error(`未知 Python 编程评测用例：${missing.join(', ')}`)
}
const caseLimit = Math.min(selectedCasePool.length, Math.max(1, positiveIntegerEnv('JUHE_REAL_HYBRID_PYTHON_CASES') ?? selectedCasePool.length))
const requestTimeoutMs = positiveIntegerEnv('JUHE_REAL_HYBRID_PYTHON_REQUEST_TIMEOUT_MS') ?? 180_000
const requestIntervalMs = positiveIntegerEnv('JUHE_REAL_HYBRID_PYTHON_REQUEST_INTERVAL_MS') ?? 6_500
const upstreamRetryCount = positiveIntegerEnv('JUHE_REAL_HYBRID_PYTHON_UPSTREAM_RETRIES') ?? 5
const upstreamRetryDelayMs = positiveIntegerEnv('JUHE_REAL_HYBRID_PYTHON_UPSTREAM_RETRY_DELAY_MS') ?? 5_000
const repairMaxRounds = nonNegativeIntegerEnv('JUHE_REAL_HYBRID_PYTHON_REPAIR_ROUNDS') ?? 1
const outputMaxTokens = positiveIntegerEnv('JUHE_REAL_HYBRID_PYTHON_OUTPUT_MAX_TOKENS') ?? 6_000
const outputPath = envText('JUHE_REAL_HYBRID_PYTHON_OUTPUT_PATH')
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

const tempRoot = resolve(tmpdir(), `juhe-ai-hybrid-python-scoring-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'hybrid-python-scoring.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'hybrid-python-scoring-secret'
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
app.use('/v1', express.raw({ type: () => true, limit: '4mb' }), captureGatewayRawBody, async (req, res, next) => {
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
    assertPythonAvailable()
    registerPythonCustomModels()
    const scoring = createRealGroupAccount('Hybrid Python 评分分组', 'Hybrid Python 评分账户', scoringModel)
    const groupsByModel = new Map<string, { accountId: string; groupId: string }>([[scoringModel, scoring]])
    for (const model of routeTargetModels) {
      if (!groupsByModel.has(model)) {
        groupsByModel.set(model, createRealGroupAccount(`Hybrid Python ${model} 分组`, `Hybrid Python ${model} 账户`, model))
      }
    }
    const hybridApiKey = repositories.createApiKeyRecord({
      name: 'Hybrid Python Programming Scoring Key',
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
        scoringTimeoutMs: 60_000,
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
    assert(hybridApiKey.key, 'Python 编程混合 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`
    const selectedCases = selectedCasePool.slice(0, caseLimit)
    const results: CaseRunResult[] = []

    for (const [index, testCase] of selectedCases.entries()) {
      if (index > 0 && requestIntervalMs > 0) {
        await wait(requestIntervalMs)
      }
      console.error(`[hybrid-python-scoring] ${testCase.id}: start`)
      const result = await runCase(baseUrl, hybridApiKey.key, testCase, index)
      results.push(result)
      console.error(`[hybrid-python-scoring] ${testCase.id}: level=${result.selectedRoute?.level ?? 'n/a'} model=${result.selectedRoute?.targetModel ?? 'n/a'} validation=${result.validation.ok ? 'ok' : 'failed'}`)
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

function registerPythonCustomModels(): void {
  for (const model of new Set([scoringModel, ...routeTargetModels])) {
    savePythonCustomModel(OPENAI_COMPATIBLE_PROVIDER_CODE, model, modelCostDefault(model))
  }
}

function savePythonCustomModel(providerCode: ProviderCode, model: string, unitCost: number): void {
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

async function runCase(
  baseUrl: string,
  localApiKey: string,
  testCase: PythonCodingCase,
  index: number
): Promise<CaseRunResult> {
  const completion = await callGatewayCompletion(baseUrl, localApiKey, [
    {
      role: 'system',
      content: [
        '你是资深 Python 工程师。只输出指定文件内容，不要 Markdown，不要解释。',
        '必须写出可运行、可维护、边界清晰、性能合理的 Python 代码。',
        '只能使用 Python 标准库；不要读写无关文件、不要网络请求、不要命令执行。'
      ].join('\n')
    },
    {
      role: 'user',
      content: testCase.prompt
    }
  ], `hybrid-python-${testCase.id}`)
  const routeEvents = routeEventsForClientRequest(completion.clientRequestId)
  const selectedRoute = lastSelectedRouteEvent(routeEvents)
  const validation = validatePythonCase(testCase, completion, index)
  let finalCompletion = completion
  let finalRouteEvents = routeEvents
  let finalSelectedRoute = selectedRoute
  let finalValidation = validation
  let repair: CaseRunResult['repair']
  if (!validation.ok && completion.ok && repairMaxRounds > 0) {
    await wait(requestIntervalMs)
    const repairCompletion = await callGatewayCompletion(baseUrl, localApiKey, repairMessages(testCase, completion.content, validation), `hybrid-python-${testCase.id}-repair-1`)
    const repairRouteEvents = routeEventsForClientRequest(repairCompletion.clientRequestId)
    const repairSelectedRoute = lastSelectedRouteEvent(repairRouteEvents)
    const repairValidation = validatePythonCase(testCase, repairCompletion, index)
    repair = {
      completion: repairCompletion,
      round: 1,
      routeEvents: repairRouteEvents,
      selectedRoute: repairSelectedRoute,
      validation: repairValidation
    }
    finalCompletion = repairCompletion
    finalRouteEvents = repairRouteEvents
    finalSelectedRoute = repairSelectedRoute
    finalValidation = repairValidation
  }
  return {
    case: {
      expectedComplexity: testCase.expectedComplexity,
      id: testCase.id,
      title: testCase.title
    },
    completion: finalCompletion,
    initial: {
      completion,
      routeEvents,
      selectedRoute,
      validation
    },
    repair,
    routeEvents: finalRouteEvents,
    selectedRoute: finalSelectedRoute,
    validation: finalValidation
  }
}

function repairMessages(
  testCase: PythonCodingCase,
  previousOutput: string,
  validation: PythonValidationResult
): Array<{ role: string; content: string }> {
  return [
    {
      role: 'system',
      content: [
        '你是资深 Python 工程师。当前任务是修复上一版未通过的代码。',
        '只输出指定文件内容，不要 Markdown，不要解释。',
        '必须根据失败信息修复正确性、性能或质量问题，不能删除题目要求的能力。'
      ].join('\n')
    },
    {
      role: 'user',
      content: [
        testCase.prompt,
        '',
        '上一版没有通过本地验收。请基于以下失败信息修复，并重新输出完整 solution.py。',
        '',
        '失败信息：',
        lastLines(validation.test.output || validation.parseError || validation.staticChecks.issues.join('\n'), 80),
        '',
        '上一版输出：',
        previousOutput.slice(0, 20_000)
      ].join('\n')
    }
  ]
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
        model: 'python-hybrid-router',
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

function validatePythonCase(testCase: PythonCodingCase, completion: CompletionResult, index: number): PythonValidationResult {
  const workdir = join(tempRoot, `case-${String(index + 1).padStart(2, '0')}-${testCase.id}`)
  mkdirSync(workdir, { recursive: true })
  const parsed = extractSolutionPy(completion.content)
  if (!completion.ok) {
    return {
      codeBytes: 0,
      parseOk: parsed.ok,
      parseError: completion.error,
      staticChecks: {
        issues: completion.error ? [completion.error] : ['请求未成功'],
        ok: false
      },
      test: {
        durationMs: 0,
        exitCode: null,
        ok: false,
        output: completion.error ?? '请求未成功'
      },
      ok: false
    }
  }
  if (!parsed.ok) {
    return {
      codeBytes: 0,
      parseOk: false,
      parseError: parsed.error,
      staticChecks: {
        issues: [parsed.error],
        ok: false
      },
      test: {
        durationMs: 0,
        exitCode: null,
        ok: false,
        output: parsed.error
      },
      ok: false
    }
  }
  const solutionPath = join(workdir, 'solution.py')
  const testPath = join(workdir, 'test_solution.py')
  writeFileSync(solutionPath, parsed.code, 'utf8')
  writeFileSync(testPath, testCase.tests, 'utf8')
  const staticChecks = inspectPythonSource(parsed.code)
  const startedAt = Date.now()
  const testProcess = spawnSync(pythonExecutable, ['-m', 'unittest', '-v', 'test_solution.py'], {
    cwd: workdir,
    encoding: 'utf8',
    timeout: testCase.timeoutMs,
    windowsHide: true
  })
  const testOutput = sanitizeProcessOutput(`${testProcess.stdout ?? ''}\n${testProcess.stderr ?? ''}`)
  const test = {
    durationMs: Date.now() - startedAt,
    exitCode: testProcess.status,
    ok: testProcess.status === 0 && !testProcess.error,
    output: testOutput
  }
  return {
    codeBytes: Buffer.byteLength(parsed.code, 'utf8'),
    parseOk: true,
    staticChecks,
    test,
    ok: staticChecks.ok && test.ok
  }
}

function extractSolutionPy(content: string): { code: string; ok: true } | { error: string; ok: false } {
  const beginMatch = content.match(/BEGIN_FILE:solution\.py\s*\r?\n([\s\S]*?)\r?\nEND_FILE/)
  if (beginMatch?.[1]?.trim()) {
    return { code: normalizePythonSource(beginMatch[1]), ok: true }
  }
  const fenceMatch = content.match(/```(?:python|py)?\s*\r?\n([\s\S]*?)\r?\n```/i)
  if (fenceMatch?.[1]?.trim()) {
    return { code: normalizePythonSource(fenceMatch[1]), ok: true }
  }
  const likelyPython = content.includes('def ') || content.includes('class ')
  if (likelyPython && !content.includes('BEGIN_FILE:')) {
    return { code: normalizePythonSource(content), ok: true }
  }
  return { error: content.slice(0, 800) || 'empty response', ok: false }
}

function normalizePythonSource(value: string): string {
  return `${value.replace(/\r\n/g, '\n').replace(/\r/g, '\n').trim()}\n`
}

function inspectPythonSource(source: string): { issues: string[]; ok: boolean } {
  const issues: string[] = []
  const lowered = source.toLowerCase()
  const bannedPatterns = [
    { pattern: /\beval\s*\(/, reason: '禁止使用 eval' },
    { pattern: /\bexec\s*\(/, reason: '禁止使用 exec' },
    { pattern: /\b__import__\s*\(/, reason: '禁止动态导入' },
    { pattern: /\bsubprocess\b/, reason: '禁止 subprocess' },
    { pattern: /\bos\.system\b/, reason: '禁止 os.system' },
    { pattern: /\brequests\b|\burllib\b|\bsocket\b/, reason: '禁止网络访问' },
    { pattern: /\bpandas\b|\bnumpy\b/, reason: '禁止外部数据科学依赖' },
    { pattern: /\binput\s*\(/, reason: '禁止交互式输入' }
  ]
  for (const item of bannedPatterns) {
    if (item.pattern.test(lowered)) issues.push(item.reason)
  }
  if (Buffer.byteLength(source, 'utf8') > 80_000) {
    issues.push('solution.py 过大')
  }
  if (!/\bdef\s+|\bclass\s+/.test(source)) {
    issues.push('缺少可复用函数或类')
  }
  return { issues, ok: issues.length === 0 }
}

function buildSummary(input: {
  results: CaseRunResult[]
  selectedCases: PythonCodingCase[]
}): Record<string, unknown> {
  const completed = input.results.filter((item) => item.completion.ok)
  const valid = input.results.filter((item) => item.validation.ok)
  const selectedRoutes = input.results.map((item) => item.selectedRoute).filter((item): item is HybridRouteEvent => Boolean(item))
  const levels = selectedRoutes.map((item) => item.level).filter((item): item is number => typeof item === 'number')
  return {
    ok: input.results.length === input.selectedCases.length && input.results.every((item) => item.completion.ok && item.validation.ok && item.selectedRoute),
    baseUrl: sanitizeBaseUrl(realBaseUrl),
    pythonExecutable,
    caseCount: input.selectedCases.length,
    completedCount: completed.length,
    validationPassCount: valid.length,
    retryPolicy: {
      maxRetries: upstreamRetryCount,
      maxAttempts: upstreamRetryCount + 1,
      retryDelayMs: upstreamRetryDelayMs,
      requestIntervalMs,
      repairMaxRounds
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
      expectedComplexity: item.case.expectedComplexity,
      completionOk: item.completion.ok,
      status: item.completion.status,
      attempts: item.completion.attempts,
      durationMs: item.completion.durationMs,
      validation: {
        ok: item.validation.ok,
        codeBytes: item.validation.codeBytes,
        staticIssues: item.validation.staticChecks.issues,
        testExitCode: item.validation.test.exitCode,
        testDurationMs: item.validation.test.durationMs,
        testOutput: item.validation.test.ok ? lastLines(item.validation.test.output, 8) : lastLines(item.validation.test.output, 20)
      },
      initial: {
        completionOk: item.initial.completion.ok,
        status: item.initial.completion.status,
        attempts: item.initial.completion.attempts,
        validationOk: item.initial.validation.ok,
        route: item.initial.selectedRoute ? {
          level: item.initial.selectedRoute.level,
          confidence: item.initial.selectedRoute.confidence,
          factors: item.initial.selectedRoute.scoringFactors,
          reason: item.initial.selectedRoute.scoringReason,
          targetModel: item.initial.selectedRoute.targetModel,
          levelRange: item.initial.selectedRoute.levelRange
        } : undefined
      },
      repair: item.repair ? {
        completionOk: item.repair.completion.ok,
        status: item.repair.completion.status,
        attempts: item.repair.completion.attempts,
        validationOk: item.repair.validation.ok,
        route: item.repair.selectedRoute ? {
          level: item.repair.selectedRoute.level,
          confidence: item.repair.selectedRoute.confidence,
          factors: item.repair.selectedRoute.scoringFactors,
          reason: item.repair.selectedRoute.scoringReason,
          targetModel: item.repair.selectedRoute.targetModel,
          levelRange: item.repair.selectedRoute.levelRange
        } : undefined
      } : undefined,
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
  selectedCases: PythonCodingCase[]
}): void {
  if (!outputPath) return
  writeFileSync(outputPath, `${JSON.stringify(buildSummary(input), null, 2)}\n`, 'utf8')
}

function configuredLevelRoutes(): ApiKeyHybridRoutingConfig['levelRoutes'] {
  const configured = envText('JUHE_REAL_HYBRID_PYTHON_LEVEL_ROUTES_JSON')
  if (!configured) {
    return [
      { minLevel: 1, maxLevel: 3, targetModel: lowModel, enabled: true },
      { minLevel: 4, maxLevel: 7, targetModel: midModel, enabled: true },
      { minLevel: 8, maxLevel: 10, targetModel: highModel, enabled: true }
    ]
  }
  const parsed = JSON.parse(configured) as unknown
  if (!Array.isArray(parsed)) {
    throw new Error('JUHE_REAL_HYBRID_PYTHON_LEVEL_ROUTES_JSON 必须是数组')
  }
  return parsed.map((item) => {
    if (!item || typeof item !== 'object' || Array.isArray(item)) {
      throw new Error('Python 编程档位配置项必须是对象')
    }
    const record = item as Record<string, unknown>
    const minLevel = Number(record.minLevel)
    const maxLevel = Number(record.maxLevel)
    const targetModel = typeof record.targetModel === 'string' ? record.targetModel.trim() : ''
    if (!Number.isInteger(minLevel) || !Number.isInteger(maxLevel) || minLevel < 1 || maxLevel > 10 || minLevel > maxLevel || !targetModel) {
      throw new Error('Python 编程档位配置必须包含合法 minLevel、maxLevel 和 targetModel')
    }
    return {
      minLevel,
      maxLevel,
      targetModel,
      enabled: record.enabled !== false
    }
  })
}

function configuredCaseIds(): string[] | undefined {
  const configured = envText('JUHE_REAL_HYBRID_PYTHON_CASE_IDS')
  if (!configured) return undefined
  const values = configured.split(',').map((item) => item.trim()).filter(Boolean)
  return values.length ? [...new Set(values)] : undefined
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

function pythonCodingCases(): PythonCodingCase[] {
  return [
    {
      id: 'py-slugify',
      title: '文本 slug 生成',
      expectedComplexity: 'low',
      timeoutMs: 10_000,
      prompt: pythonCodingPrompt([
        '实现函数 slugify(text: str, max_length: int = 60) -> str。',
        '规则：转小写；只保留 ASCII 字母和数字；任意连续非字母数字字符转成单个连字符；去掉首尾连字符；超过 max_length 时截断并再次去掉尾部连字符；结果为空时返回 "item"。',
        '需要处理 None、空字符串、全符号、连续空白、非 ASCII 字符、max_length <= 0 等边界。'
      ]),
      tests: pythonTest(`
        import unittest
        from solution import slugify

        class SlugifyTests(unittest.TestCase):
            def test_basic(self):
                self.assertEqual(slugify(" Hello, AI Gateway!! "), "hello-ai-gateway")
                self.assertEqual(slugify("A---B___C"), "a-b-c")

            def test_edges(self):
                self.assertEqual(slugify("中文 !!!"), "item")
                self.assertEqual(slugify("", 10), "item")
                self.assertEqual(slugify(None), "item")
                self.assertEqual(slugify("abc def ghi", 7), "abc-def")
                self.assertEqual(slugify("abc", 0), "item")
      `)
    },
    {
      id: 'py-merge-intervals',
      title: '区间归并',
      expectedComplexity: 'low',
      timeoutMs: 10_000,
      prompt: pythonCodingPrompt([
        '实现函数 merge_intervals(intervals) -> list[tuple[int, int]]。',
        '输入是可迭代对象，元素为二元组或二元素列表。每个区间如果 start > end 需要先自动交换。',
        '按闭区间处理，重叠或相邻区间都要合并；结果按 start 升序返回 tuple 列表。',
        '非法元素需要抛出 ValueError；不要修改输入对象。'
      ]),
      tests: pythonTest(`
        import unittest
        from solution import merge_intervals

        class MergeIntervalsTests(unittest.TestCase):
            def test_merge(self):
                self.assertEqual(merge_intervals([(5, 1), (2, 3), (8, 10), (10, 12)]), [(1, 5), (8, 12)])
                self.assertEqual(merge_intervals([[1, 1], [2, 2], [3, 4]]), [(1, 4)])

            def test_empty_and_invalid(self):
                self.assertEqual(merge_intervals([]), [])
                with self.assertRaises(ValueError):
                    merge_intervals([(1, 2, 3)])
                with self.assertRaises(ValueError):
                    merge_intervals([("a", 2)])
      `)
    },
    {
      id: 'py-lru-cache',
      title: 'LRU 缓存类',
      expectedComplexity: 'medium',
      timeoutMs: 12_000,
      prompt: pythonCodingPrompt([
        '实现类 LRUCache。',
        '构造：LRUCache(capacity: int)。capacity 必须为正整数，否则抛出 ValueError。',
        '方法：get(key) -> value | None；put(key, value) -> None；__len__ 返回当前缓存数量。',
        '要求 get 和 put 平均 O(1)，更新已有 key 时刷新最近使用顺序。',
        '不能用外部依赖；需要兼容任意可哈希 key 和任意 value。'
      ]),
      tests: pythonTest(`
        import time
        import unittest
        from solution import LRUCache

        class LRUCacheTests(unittest.TestCase):
            def test_behavior(self):
                cache = LRUCache(2)
                cache.put("a", 1)
                cache.put("b", 2)
                self.assertEqual(cache.get("a"), 1)
                cache.put("c", 3)
                self.assertIsNone(cache.get("b"))
                cache.put("a", 10)
                cache.put("d", 4)
                self.assertIsNone(cache.get("c"))
                self.assertEqual(cache.get("a"), 10)
                self.assertEqual(len(cache), 2)

            def test_invalid_and_performance(self):
                with self.assertRaises(ValueError):
                    LRUCache(0)
                cache = LRUCache(1000)
                started = time.perf_counter()
                for i in range(20000):
                    cache.put(i, i)
                    cache.get(i // 2)
                self.assertLess(time.perf_counter() - started, 1.2)
      `)
    },
    {
      id: 'py-log-summarizer',
      title: '日志汇总器',
      expectedComplexity: 'medium',
      timeoutMs: 12_000,
      prompt: pythonCodingPrompt([
        '实现函数 summarize_events(lines) -> dict。',
        'lines 是 JSON Lines 字符串可迭代对象。每行可能无效，必须跳过并计入 invalid。',
        '有效事件字段：service: str、status: str、duration_ms: number。缺字段或字段类型错误也算 invalid。',
        '返回：{"total":有效数量,"invalid":无效数量,"services":{service:{"count":数量,"errors":非 ok 数量,"avg_duration_ms":平均耗时保留 2 位小数}},"slowest_service":平均耗时最高的服务名或 None}。',
        '同平均耗时时 slowest_service 取字典序更小的服务名。要求单次遍历，不能把全部行先转成大列表。'
      ]),
      tests: pythonTest(`
        import json
        import unittest
        from solution import summarize_events

        class SummarizeEventsTests(unittest.TestCase):
            def test_summary(self):
                lines = [
                    json.dumps({"service": "api", "status": "ok", "duration_ms": 100}),
                    json.dumps({"service": "api", "status": "error", "duration_ms": 140}),
                    json.dumps({"service": "worker", "status": "ok", "duration_ms": 200}),
                    "not-json",
                    json.dumps({"service": "api", "status": "ok"}),
                ]
                result = summarize_events(lines)
                self.assertEqual(result["total"], 3)
                self.assertEqual(result["invalid"], 2)
                self.assertEqual(result["services"]["api"], {"count": 2, "errors": 1, "avg_duration_ms": 120.0})
                self.assertEqual(result["services"]["worker"], {"count": 1, "errors": 0, "avg_duration_ms": 200.0})
                self.assertEqual(result["slowest_service"], "worker")

            def test_empty(self):
                self.assertEqual(summarize_events([]), {"total": 0, "invalid": 0, "services": {}, "slowest_service": None})
      `)
    },
    {
      id: 'py-topological-sort',
      title: '稳定拓扑排序',
      expectedComplexity: 'high',
      timeoutMs: 12_000,
      prompt: pythonCodingPrompt([
        '实现函数 build_order(tasks, dependencies) -> list[str]。',
        'tasks 是任务名可迭代对象，dependencies 是 (before, after) 二元组可迭代对象，表示 before 必须在 after 之前。',
        '返回满足依赖的顺序；当多个任务同时可执行时，必须按任务名字典序稳定选择。',
        '如果依赖引用了未知任务，抛出 KeyError；如果存在环，抛出 ValueError，错误信息中尽量包含 cycle。',
        '要求能处理数千任务和依赖，不能用递归深度容易爆栈的实现。'
      ]),
      tests: pythonTest(`
        import unittest
        from solution import build_order

        class TopologicalSortTests(unittest.TestCase):
            def test_order(self):
                tasks = ["deploy", "build", "test", "lint", "package"]
                deps = [("build", "test"), ("lint", "test"), ("test", "package"), ("package", "deploy")]
                self.assertEqual(build_order(tasks, deps), ["build", "lint", "test", "package", "deploy"])

            def test_errors_and_scale(self):
                with self.assertRaises(KeyError):
                    build_order(["a"], [("a", "b")])
                with self.assertRaises(ValueError):
                    build_order(["a", "b"], [("a", "b"), ("b", "a")])
                tasks = [f"t{i:04d}" for i in range(1500)]
                deps = [(tasks[i], tasks[i + 1]) for i in range(len(tasks) - 1)]
                self.assertEqual(build_order(tasks, deps), tasks)
      `)
    },
    {
      id: 'py-dijkstra',
      title: '最短路径模块',
      expectedComplexity: 'high',
      timeoutMs: 12_000,
      prompt: pythonCodingPrompt([
        '实现函数 shortest_path(edges, start, goal)。',
        'edges 是 (source, target, weight) 可迭代对象，表示有向边；weight 必须是非负数字，否则抛出 ValueError。',
        '返回 (cost, path)。可达时 cost 为最小总代价，path 为节点列表；不可达时返回 (float("inf"), [])。',
        '要求使用适合稀疏图的实现，能处理上万条边；不能枚举所有路径。',
        '如果有多条相同最短路径，返回字典序最小的 path。'
      ]),
      tests: pythonTest(`
        import math
        import time
        import unittest
        from solution import shortest_path

        class DijkstraTests(unittest.TestCase):
            def test_shortest_path(self):
                edges = [
                    ("A", "B", 1), ("A", "C", 1), ("B", "D", 2),
                    ("C", "D", 2), ("B", "C", 1), ("D", "E", 3)
                ]
                self.assertEqual(shortest_path(edges, "A", "E"), (6, ["A", "B", "D", "E"]))
                self.assertEqual(shortest_path(edges, "E", "A"), (float("inf"), []))
                with self.assertRaises(ValueError):
                    shortest_path([("a", "b", -1)], "a", "b")

            def test_large_sparse_graph(self):
                edges = []
                for i in range(4000):
                    edges.append((i, i + 1, 1))
                    if i + 10 <= 4000:
                        edges.append((i, i + 10, 12))
                started = time.perf_counter()
                cost, path = shortest_path(edges, 0, 4000)
                elapsed = time.perf_counter() - started
                self.assertEqual(cost, 4000)
                self.assertEqual(path[0], 0)
                self.assertEqual(path[-1], 4000)
                self.assertLess(elapsed, 1.5)
      `)
    },
    {
      id: 'py-expression-evaluator',
      title: '表达式求值器',
      expectedComplexity: 'very_high',
      timeoutMs: 12_000,
      prompt: pythonCodingPrompt([
        '实现函数 evaluate(expression: str, variables: dict[str, float] | None = None) -> float。',
        '支持数字、小数、变量名、括号、+、-、*、/、一元正负号和空白。',
        '必须正确处理运算优先级、左结合、嵌套括号和一元负号；未知变量抛出 KeyError；语法错误或除零抛出 ValueError。',
        '严禁使用 eval、exec、ast.literal_eval 或把表达式交给 Python 自己执行；需要自己 tokenize 和 parse。',
        '代码需要结构清晰，方便后续扩展操作符。'
      ]),
      tests: pythonTest(`
        import math
        import unittest
        from solution import evaluate

        class ExpressionEvaluatorTests(unittest.TestCase):
            def test_arithmetic(self):
                self.assertAlmostEqual(evaluate("1 + 2 * 3"), 7)
                self.assertAlmostEqual(evaluate("(1 + 2) * 3"), 9)
                self.assertAlmostEqual(evaluate("-2 * (3 + 4)"), -14)
                self.assertAlmostEqual(evaluate("x * 2 + y / 4", {"x": 3, "y": 8}), 8)
                self.assertAlmostEqual(evaluate("--5 + +2"), 7)

            def test_errors(self):
                with self.assertRaises(KeyError):
                    evaluate("missing + 1", {})
                with self.assertRaises(ValueError):
                    evaluate("1 / (2 - 2)")
                with self.assertRaises(ValueError):
                    evaluate("1 + * 2")
                with self.assertRaises(ValueError):
                    evaluate("")
      `)
    },
    {
      id: 'py-json-patch',
      title: 'JSON Patch 应用器',
      expectedComplexity: 'very_high',
      timeoutMs: 12_000,
      prompt: pythonCodingPrompt([
        '实现函数 apply_json_patch(document, patch) -> object。',
        'document 是 JSON 兼容的 dict/list/scalar，patch 是操作列表。必须返回新对象，不得修改原 document。',
        '支持操作：add、remove、replace、copy、move、test。路径使用 JSON Pointer 规则，支持 ~0 和 ~1 转义；数组 add 支持 "-" 表示追加。',
        '非法路径、非法操作、test 失败、数组越界都要抛出合适异常。',
        '需要正确处理深拷贝、移动后源路径删除、向对象和数组写入、根路径 "" 替换等边界。',
        '只能使用标准库，代码要保持可维护，不要通过字符串拼接访问路径。'
      ]),
      tests: pythonTest(`
        import copy
        import unittest
        from solution import apply_json_patch

        class JsonPatchTests(unittest.TestCase):
            def test_operations(self):
                doc = {"foo": ["bar", "baz"], "obj": {"a/b": 1, "tilde~key": 2}}
                original = copy.deepcopy(doc)
                patch = [
                    {"op": "test", "path": "/foo/0", "value": "bar"},
                    {"op": "add", "path": "/foo/-", "value": "qux"},
                    {"op": "replace", "path": "/obj/a~1b", "value": 10},
                    {"op": "copy", "from": "/obj/tilde~0key", "path": "/copied"},
                    {"op": "move", "from": "/foo/1", "path": "/moved"},
                    {"op": "remove", "path": "/foo/0"},
                ]
                result = apply_json_patch(doc, patch)
                self.assertEqual(doc, original)
                self.assertEqual(result, {"foo": ["qux"], "obj": {"a/b": 10, "tilde~key": 2}, "copied": 2, "moved": "baz"})

            def test_root_and_errors(self):
                self.assertEqual(apply_json_patch({"a": 1}, [{"op": "replace", "path": "", "value": [1, 2]}]), [1, 2])
                with self.assertRaises(Exception):
                    apply_json_patch({"a": 1}, [{"op": "test", "path": "/a", "value": 2}])
                with self.assertRaises(Exception):
                    apply_json_patch({"a": []}, [{"op": "remove", "path": "/a/3"}])
                with self.assertRaises(Exception):
                    apply_json_patch({}, [{"op": "unknown", "path": "/a"}])
      `)
    },
    {
      id: 'py-repair-token-bucket',
      title: '生产限流器修复',
      expectedComplexity: 'very_high',
      timeoutMs: 12_000,
      prompt: pythonCodingPrompt([
        '这是一个生产网关限流器修复任务。上一版已经在线上验收失败，会导致不同 API Key 的额度互相污染、时间回退后错误放行、以及并发下状态错乱。',
        '请输出完整 solution.py，提供类 TokenBucketLimiter。',
        '构造：TokenBucketLimiter(rate_per_second: float, capacity: float, now: Callable[[], float] | None = None)。rate_per_second 和 capacity 必须为正数，否则抛出 ValueError；now 未传时使用 time.monotonic。',
        '方法：allow(key: object = "global", tokens: float = 1.0) -> bool；remaining(key: object = "global") -> float；reset(key: object | None = None) -> None。',
        '语义：每个 key 拥有独立令牌桶；新 key 初始满桶；按 elapsed * rate_per_second 补充但不能超过 capacity；tokens <= 0 抛出 ValueError；tokens > capacity 直接返回 False 且不能改变桶状态。',
        '必须处理时钟回退：当前时间小于该 key 上次记录时间时，不允许产生负补充，也不能通过回退时间绕过限流。',
        '要求线程安全，允许多个 key 并发访问；不能 sleep；不能使用外部依赖；remaining 会先刷新桶再返回剩余令牌数。',
        '上一版失败代码如下，请不要照抄 bug，只把它作为失败上下文：',
        'class TokenBucketLimiter:',
        '    def __init__(self, rate_per_second, capacity, now=None):',
        '        self.rate = rate_per_second',
        '        self.capacity = capacity',
        '        self.tokens = 0',
        '        self.updated = 0',
        '        self.now = now',
        '    def allow(self, key="global", tokens=1):',
        '        current = self.now() if self.now else 0',
        '        self.tokens += (current - self.updated) * self.rate',
        '        self.updated = current',
        '        if self.tokens >= tokens:',
        '            self.tokens -= tokens',
        '            return True',
        '        return False',
        '    def remaining(self, key="global"):',
        '        return self.tokens',
        '    def reset(self, key=None):',
        '        self.tokens = self.capacity'
      ]),
      tests: pythonTest(`
        import threading
        import unittest
        from solution import TokenBucketLimiter

        class FakeClock:
            def __init__(self, value=0.0):
                self.value = float(value)
            def __call__(self):
                return self.value
            def set(self, value):
                self.value = float(value)
            def advance(self, seconds):
                self.value += float(seconds)

        class TokenBucketLimiterTests(unittest.TestCase):
            def test_basic_refill_and_capacity(self):
                clock = FakeClock(100)
                limiter = TokenBucketLimiter(rate_per_second=2, capacity=3, now=clock)
                self.assertAlmostEqual(limiter.remaining(), 3)
                self.assertTrue(limiter.allow())
                self.assertTrue(limiter.allow())
                self.assertTrue(limiter.allow())
                self.assertFalse(limiter.allow())
                clock.advance(0.49)
                self.assertFalse(limiter.allow())
                clock.advance(0.01)
                self.assertTrue(limiter.allow())
                clock.advance(10)
                self.assertAlmostEqual(limiter.remaining(), 3)
                self.assertFalse(limiter.allow(tokens=4))
                self.assertAlmostEqual(limiter.remaining(), 3)

            def test_key_isolation_reset_and_clock_rollback(self):
                clock = FakeClock(10)
                limiter = TokenBucketLimiter(rate_per_second=1, capacity=1, now=clock)
                self.assertTrue(limiter.allow("a"))
                self.assertFalse(limiter.allow("a"))
                self.assertTrue(limiter.allow("b"))
                clock.set(5)
                self.assertFalse(limiter.allow("a"))
                clock.set(11)
                self.assertTrue(limiter.allow("a"))
                limiter.reset("a")
                self.assertTrue(limiter.allow("a"))
                self.assertFalse(limiter.allow("a"))
                limiter.reset()
                self.assertTrue(limiter.allow("a"))
                self.assertTrue(limiter.allow("b"))

            def test_validation_and_thread_safety(self):
                clock = FakeClock(0)
                with self.assertRaises(ValueError):
                    TokenBucketLimiter(0, 1, now=clock)
                with self.assertRaises(ValueError):
                    TokenBucketLimiter(1, 0, now=clock)
                limiter = TokenBucketLimiter(rate_per_second=100, capacity=20, now=clock)
                with self.assertRaises(ValueError):
                    limiter.allow(tokens=0)
                results = []
                def worker(i):
                    results.append(limiter.allow("shared"))
                threads = [threading.Thread(target=worker, args=(i,)) for i in range(60)]
                for thread in threads:
                    thread.start()
                for thread in threads:
                    thread.join()
                self.assertEqual(sum(1 for item in results if item), 20)
      `)
    },
    {
      id: 'py-repair-ledger-reconcile',
      title: '生产账本对账修复',
      expectedComplexity: 'very_high',
      timeoutMs: 12_000,
      prompt: pythonCodingPrompt([
        '这是一个生产计费账本修复任务。上一版已经造成重复事件重复入账、退款/冲正方向错误、浮点金额精度丢失和失败事件部分污染状态。',
        '请输出完整 solution.py，提供函数 reconcile_ledger(events) -> dict。',
        'events 是事件可迭代对象，每个事件是 dict，字段包括：id: str、account: str、type: "credit" | "debit" | "reversal"、amount: str、reverses: str 可选。',
        '金额必须使用 Decimal 精确处理，只接受非负且最多两位小数的十进制字符串；输出金额必须是两位小数字符串。',
        'credit 增加账户余额；debit 扣减账户余额但不能透支，透支事件要 reject 且不能改变状态；reversal 必须引用一个已接受且未被冲正的 credit/debit 事件，并执行反向金额影响；不能冲正 reversal。',
        '事件 id 必须幂等：重复 id 只能忽略，不能重复入账，也不能计入 rejected。',
        '非法事件要加入 rejected 列表，元素至少包含 id 和 reason；非法事件不能污染余额、applied_ids 或可冲正状态。',
        '返回结构：{"balances": {account: amount_str}, "accepted": 数量, "rejected": 列表, "applied_ids": 按接受顺序的 id 列表}。',
        '结果 balances 按账户名字典序构造；applied_ids 只包含真正改变账本状态的事件 id。',
        '上一版失败代码如下，请不要照抄 bug，只把它作为失败上下文：',
        'def reconcile_ledger(events):',
        '    balances = {}',
        '    seen = set()',
        '    for e in events:',
        '        if e["id"] in seen:',
        '            pass',
        '        seen.add(e["id"])',
        '        amount = float(e.get("amount", 0))',
        '        account = e.get("account")',
        '        if e.get("type") == "credit":',
        '            balances[account] = balances.get(account, 0) + amount',
        '        elif e.get("type") == "debit":',
        '            balances[account] = balances.get(account, 0) - amount',
        '        elif e.get("type") == "reversal":',
        '            balances[account] = balances.get(account, 0) - amount',
        '    return {"balances": balances}'
      ]),
      tests: pythonTest(`
        import unittest
        from solution import reconcile_ledger

        class LedgerReconcileTests(unittest.TestCase):
            def test_idempotency_reversal_and_rejections(self):
                events = [
                    {"id": "c1", "account": "acct-a", "type": "credit", "amount": "10.00"},
                    {"id": "d1", "account": "acct-a", "type": "debit", "amount": "3.33"},
                    {"id": "d1", "account": "acct-a", "type": "debit", "amount": "3.33"},
                    {"id": "bad-overdraft", "account": "acct-a", "type": "debit", "amount": "99.00"},
                    {"id": "r1", "account": "acct-a", "type": "reversal", "amount": "3.33", "reverses": "d1"},
                    {"id": "r2", "account": "acct-a", "type": "reversal", "amount": "10.00", "reverses": "r1"},
                    {"id": "c2", "account": "acct-b", "type": "credit", "amount": "0.10"},
                    {"id": "c3", "account": "acct-b", "type": "credit", "amount": "0.20"},
                    {"id": "bad-scale", "account": "acct-b", "type": "credit", "amount": "0.001"},
                    {"id": "bad-ref", "account": "acct-b", "type": "reversal", "amount": "0.10", "reverses": "missing"},
                ]
                result = reconcile_ledger(events)
                self.assertEqual(result["balances"], {"acct-a": "10.00", "acct-b": "0.30"})
                self.assertEqual(result["accepted"], 5)
                self.assertEqual(result["applied_ids"], ["c1", "d1", "r1", "c2", "c3"])
                rejected_ids = [item["id"] for item in result["rejected"]]
                self.assertEqual(rejected_ids, ["bad-overdraft", "r2", "bad-scale", "bad-ref"])

            def test_atomic_validation_and_input_streaming(self):
                def event_stream():
                    yield {"id": "c1", "account": "x", "type": "credit", "amount": "1.00"}
                    yield {"id": "bad", "account": "x", "type": "debit", "amount": "-0.01"}
                    yield {"id": "d1", "account": "x", "type": "debit", "amount": "1.00"}
                    yield {"id": "too-much", "account": "x", "type": "debit", "amount": "0.01"}
                result = reconcile_ledger(event_stream())
                self.assertEqual(result["balances"], {"x": "0.00"})
                self.assertEqual(result["accepted"], 2)
                self.assertEqual(result["applied_ids"], ["c1", "d1"])
                self.assertEqual([item["id"] for item in result["rejected"]], ["bad", "too-much"])
      `)
    }
  ]
}

function pythonCodingPrompt(lines: string[]): string {
  return [
    '请完成一个 Python 编程作业。',
    '输出格式必须严格如下，不能输出 Markdown、解释或额外文本：',
    'BEGIN_FILE:solution.py',
    '<完整 Python 代码>',
    'END_FILE',
    '',
    '通用要求：',
    '- 只能使用 Python 标准库。',
    '- 暴露题目要求的函数或类名，不能依赖命令行输入。',
    '- 优先写清晰、可维护、边界明确的实现。',
    '- 需要考虑隐藏测试、异常路径、性能和输入不变性。',
    '- 不要为了通过样例硬编码答案。',
    '',
    '题目：',
    ...lines
  ].join('\n')
}

function pythonTest(source: string): string {
  return `${dedent(source)}\nif __name__ == "__main__":\n    unittest.main()\n`
}

function dedent(value: string): string {
  const lines = value.replace(/\r\n/g, '\n').replace(/\r/g, '\n').split('\n')
  while (lines.length && !lines[0]?.trim()) lines.shift()
  while (lines.length && !lines[lines.length - 1]?.trim()) lines.pop()
  const indents = lines
    .filter((line) => line.trim())
    .map((line) => line.match(/^\s*/)?.[0].length ?? 0)
  const minIndent = indents.length ? Math.min(...indents) : 0
  return lines.map((line) => line.slice(minIndent)).join('\n')
}

function assertPythonAvailable(): void {
  const result = spawnSync(pythonExecutable, ['--version'], {
    encoding: 'utf8',
    timeout: 5_000,
    windowsHide: true
  })
  if (result.status !== 0 || result.error) {
    throw new Error(`Python 不可用：${result.error?.message ?? `${result.stdout}\n${result.stderr}`}`)
  }
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

function nonNegativeIntegerEnv(name: string): number | undefined {
  const value = envText(name)
  if (!value) return undefined
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed >= 0 ? parsed : undefined
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

function countBy(values: string[]): Record<string, number> {
  return values.reduce<Record<string, number>>((accumulator, item) => {
    accumulator[item] = (accumulator[item] ?? 0) + 1
    return accumulator
  }, {})
}

function roundNumber(value: number): number {
  return Number(value.toFixed(2))
}

function sanitizeErrorSnippet(value: string): string {
  return value.replaceAll(realApiKey, '[redacted-real-api-key]').slice(0, 1_200) || 'empty response'
}

function sanitizeProcessOutput(value: string): string {
  return value.replaceAll(realApiKey, '[redacted-real-api-key]').slice(-4_000)
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

function lastLines(value: string, count: number): string {
  return value.split(/\r?\n/).filter(Boolean).slice(-count).join('\n')
}

function callRetrySummary(result: CompletionResult): string {
  const error = result.error ? result.error.replace(/\s+/g, ' ').slice(0, 180) : 'empty error'
  return `status=${result.status} ${error}`
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
