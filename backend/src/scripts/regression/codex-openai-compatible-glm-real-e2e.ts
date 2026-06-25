import { strict as assert } from 'node:assert'
import { channel } from 'node:diagnostics_channel'
import { spawn } from 'node:child_process'
import { createWriteStream, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { delimiter as pathDelimiter, dirname, join, resolve } from 'node:path'

import express, { type NextFunction, type Request, type Response } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import type { ApiKeyHybridRoutingConfig } from '../../domain/types.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { replaceGatewayJsonBody } from '../../modules/gateway/request/body.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'

interface GatewayIncomingHit {
  path: string
  method: string
  authorizationPresent: boolean
  codexTurnMetadata?: string
  bodySummary: Record<string, unknown>
}

interface CliRunResult {
  exitCode: number | null
  stderr: string
  stdout: string
}

process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = '0'

const realApiKey = requiredEnv('JUHE_REAL_CODEX_OPENAI_COMPATIBLE_API_KEY', ['JUHE_REAL_OPENAI_COMPATIBLE_API_KEY', 'JUHE_REAL_HYBRID_API_KEY'])
const realBaseUrl = envText('JUHE_REAL_CODEX_OPENAI_COMPATIBLE_BASE_URL', ['JUHE_REAL_OPENAI_COMPATIBLE_BASE_URL', 'JUHE_REAL_HYBRID_BASE_URL']) || 'https://vsllm.com'
const realProvider = providerFromEnv()
const downstreamModel = envText('JUHE_REAL_CODEX_DOWNSTREAM_MODEL') || 'gpt-5.3-codex'
const upstreamModel = envText('JUHE_REAL_CODEX_UPSTREAM_MODEL') || 'glm-4.7-flash'
const hybridMode = booleanEnv('JUHE_REAL_CODEX_HYBRID')
const hybridScoringModel = envText('JUHE_REAL_CODEX_HYBRID_SCORING_MODEL') || 'deepseek-ai-v4-flash'
const hybridModel1To2 = envText('JUHE_REAL_CODEX_HYBRID_MODEL_1_2') || envText('JUHE_REAL_CODEX_HYBRID_MODEL_1_3') || 'gpt-5.4-mini'
const hybridModel4To6 = envText('JUHE_REAL_CODEX_HYBRID_MODEL_4_6') || 'gpt-5.4'
const hybridModel7To10 = envText('JUHE_REAL_CODEX_HYBRID_MODEL_7_10') || 'gpt-5.5'
const requestTimeoutMs = positiveIntegerEnv('JUHE_REAL_CODEX_CLI_TIMEOUT_MS') ?? 240_000
const codexRequestMaxRetries = positiveIntegerEnv('JUHE_REAL_CODEX_REQUEST_MAX_RETRIES') ?? 0
const codexStreamMaxRetries = positiveIntegerEnv('JUHE_REAL_CODEX_STREAM_MAX_RETRIES') ?? 0
const streamRequestTimeoutSecondsOverride = positiveIntegerEnv('JUHE_REAL_CODEX_STREAM_REQUEST_TIMEOUT_SECONDS')
const streamIdleTimeoutSecondsOverride = positiveIntegerEnv('JUHE_REAL_CODEX_STREAM_IDLE_TIMEOUT_SECONDS')
const programmingTaskEnabled = booleanEnv('JUHE_REAL_CODEX_PROGRAMMING_TASK')
const programmingTaskKind = envText('JUHE_REAL_CODEX_PROGRAMMING_TASK_KIND') || 'balanced_brackets'
const programmingProjectRootOverride = envText('JUHE_REAL_CODEX_PROGRAMMING_PROJECT_ROOT')
const programmingSandboxMode = envText('JUHE_REAL_CODEX_PROGRAMMING_SANDBOX') || 'workspace-write'
const programmingDisableShellTool = booleanEnv('JUHE_REAL_CODEX_PROGRAMMING_DISABLE_SHELL_TOOL')
const programmingChatCompatibleToolsOnly = envText('JUHE_REAL_CODEX_PROGRAMMING_CHAT_COMPATIBLE_TOOLS_ONLY') !== '0'
const programmingMinimalInstructions = envText('JUHE_REAL_CODEX_PROGRAMMING_MINIMAL_INSTRUCTIONS') !== '0'
const programmingApplyPatchOnlyTools = envText('JUHE_REAL_CODEX_PROGRAMMING_APPLY_PATCH_ONLY_TOOLS') !== '0'
const programmingTextArtifactFallback = booleanEnv('JUHE_REAL_CODEX_PROGRAMMING_TEXT_ARTIFACT_FALLBACK')
const programmingForceApplyPatchToolChoice = booleanEnv('JUHE_REAL_CODEX_PROGRAMMING_FORCE_APPLY_PATCH_TOOL_CHOICE')
const programmingForceApplyPatchToolChoiceCount = positiveIntegerEnv('JUHE_REAL_CODEX_PROGRAMMING_FORCE_APPLY_PATCH_TOOL_CHOICE_COUNT') ?? 1
const debugDumpIncomingRequestPath = envText('JUHE_REAL_CODEX_DEBUG_DUMP_INCOMING_REQUEST_PATH')
const debugDumpGatewayResponsePath = envText('JUHE_REAL_CODEX_DEBUG_DUMP_RESPONSE_PATH')
const expectMarker = booleanEnv('JUHE_REAL_CODEX_EXPECT_MARKER')
const expectGuidance = booleanEnv('JUHE_REAL_CODEX_EXPECT_GUIDANCE') || (!programmingTaskEnabled && !expectMarker)
const marker = `CODEX_GPT_TO_GLM_OK_${Date.now()}`
const hybridRouteEvents: Array<Record<string, unknown>> = []
const hybridRouteDiagnosticsChannel = channel('juhe-ai:hybrid-route-decision')
const hybridRouteDiagnosticsSubscriber = (message: unknown): void => {
  if (typeof message === 'object' && message !== null) {
    hybridRouteEvents.push(message as Record<string, unknown>)
  }
}
hybridRouteDiagnosticsChannel.subscribe(hybridRouteDiagnosticsSubscriber)

const tempRoot = resolve(tmpdir(), `juhe-ai-codex-openai-compatible-glm-real-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'codex-openai-compatible-glm-real.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'codex-openai-compatible-glm-real-secret'
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
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const gatewayIncomingHits: GatewayIncomingHit[] = []
let programmingForceApplyPatchToolChoiceRemaining = programmingForceApplyPatchToolChoiceCount

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()
  let gatewayServer: http.Server | undefined
  try {
    registerCustomModels()
    updateGatewayStreamTimeoutsForTest()
    const apiKey = hybridMode ? createHybridCodexApiKey() : createSingleCodexApiKey()
    assert(apiKey.key, '真实联调本地 API Key 未返回明文密钥')

    gatewayServer = createGatewayServer()
    await listen(gatewayServer)
    const gatewayBaseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`
    const programmingProjectRoot = programmingTaskEnabled ? seedProgrammingProject() : undefined
    const result = programmingProjectRoot
      ? await runCodexProgrammingCli(gatewayBaseUrl, apiKey.key, programmingProjectRoot)
      : await runCodexCli(gatewayBaseUrl, apiKey.key)
    assert.equal(result.exitCode, 0, `Codex CLI 应成功退出：${summarizeCliFailure(result)}`)
    if (programmingProjectRoot) {
      if (programmingTextArtifactFallback) {
        materializeProgrammingTextArtifacts(result.stdout, programmingProjectRoot)
      }
      const testResult = await runProjectTests(programmingProjectRoot)
      if (testResult.exitCode !== 0) {
        throw new Error(`Codex 编程任务完成后测试应通过；codex=${summarizeCliFailure(result)}；check=${summarizeCliFailure(testResult)}`)
      }
    } else {
      if (expectGuidance) {
        assertGuidanceCliOutput(result.stdout)
      } else {
        assert.match(result.stdout, new RegExp(marker), `Codex CLI 输出应包含 marker：${sanitizeSecretText(result.stdout).slice(0, 2000)}`)
      }
    }

    const responsesHits = gatewayIncomingHits.filter((hit) => hit.path.split('?', 1)[0].endsWith('/responses'))
    assert(responsesHits.length > 0, `Codex CLI 应命中本地 /v1/responses：${JSON.stringify(gatewayIncomingHits)}`)
    assert(responsesHits.some((hit) => hit.authorizationPresent), 'Codex CLI 应通过 Bearer 携带本地 API Key')
    assert(responsesHits.some((hit) => hasValidCodexTurnId(hit.codexTurnMetadata)), 'Codex CLI 应携带有效 x-codex-turn-metadata.turn_id')
    assert(responsesHits.some((hit) => hit.bodySummary.model === downstreamModel), 'Codex CLI 下游请求应保留 GPT/Codex 模型名')

    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const records = repositories.listUsageRecords(undefined, { page: 1, pageSize: 50 }).items
    const successfulGatewayModels = records
      .filter((record) => record.success === true && record.trafficSource === 'gateway')
      .map((record) => record.model)
      .filter((model): model is string => typeof model === 'string')
    if (!expectGuidance) {
      const expectedSuccessModels = hybridMode
        ? new Set(hybridLevelRoutes().map((route) => route.targetModel))
        : new Set([downstreamModel])
      assert(
        successfulGatewayModels.some((model) => expectedSuccessModels.has(model)),
        `使用记录应保存成功模型，实际：${successfulGatewayModels.join(', ') || '无'}`
      )
    }

    console.log(JSON.stringify({
      ok: true,
      cli: 'codex',
      provider: realProvider.providerCode,
      providerProfile: realProvider.profileId,
      baseUrl: sanitizeBaseUrl(realBaseUrl),
      downstreamModel,
      upstreamModel: hybridMode ? undefined : upstreamModel,
      hybridMode,
      hybridScoringModel: hybridMode ? hybridScoringModel : undefined,
      hybridRouteModels: hybridMode ? hybridLevelRoutes().map((route) => `${route.minLevel}-${route.maxLevel}:${route.targetModel}`) : undefined,
      gatewayStreamTimeoutOverrides: streamRequestTimeoutSecondsOverride || streamIdleTimeoutSecondsOverride
        ? {
            streamRequestTimeoutSeconds: streamRequestTimeoutSecondsOverride,
            streamIdleTimeoutSeconds: streamIdleTimeoutSecondsOverride
          }
        : undefined,
      successfulGatewayModels,
      hybridRouteEvents: hybridMode ? hybridRouteEvents.map((event) => ({
        outcome: event.outcome,
        level: event.level,
        targetModel: event.targetModel,
        levelRange: event.levelRange,
        scoringCacheHit: event.scoringCacheHit,
        scoringFallbackApplied: event.scoringFallbackApplied,
        scoringErrorCode: event.scoringErrorCode,
        scoringErrorMessage: event.scoringErrorMessage,
        statusCode: event.statusCode,
        affinityApplied: event.affinityApplied
      })) : undefined,
      mode: programmingProjectRoot ? 'programming_task' : expectGuidance ? 'guidance' : 'marker',
      programmingTaskKind: programmingProjectRoot ? programmingTaskKind : undefined,
      marker: programmingProjectRoot ? undefined : marker,
      programmingProjectRoot,
      responsesRequests: responsesHits.map((hit) => ({
        path: hit.path,
        model: hit.bodySummary.model,
        stream: hit.bodySummary.stream,
        inputType: hit.bodySummary.inputType,
        toolCount: hit.bodySummary.toolCount,
        toolChoice: hit.bodySummary.toolChoice,
        tools: hit.bodySummary.tools,
        codexTurnMetadataPresent: Boolean(hit.codexTurnMetadata)
      }))
    }, null, 2))
  } finally {
    await closeServer(gatewayServer)
  }
} catch (error) {
  const routeDebug = hybridMode && hybridRouteEvents.length
    ? `\nhybridRouteEvents=${JSON.stringify(hybridRouteEvents.map((event) => ({
      outcome: event.outcome,
      level: event.level,
      targetModel: event.targetModel,
      levelRange: event.levelRange,
      scoringCacheHit: event.scoringCacheHit,
      scoringFallbackApplied: event.scoringFallbackApplied,
      scoringErrorCode: event.scoringErrorCode,
      scoringErrorMessage: event.scoringErrorMessage,
      statusCode: event.statusCode,
      affinityApplied: event.affinityApplied
    })))}`
    : ''
  const requestDebug = gatewayIncomingHits.length
    ? `\nresponsesRequests=${JSON.stringify(gatewayIncomingHits.map((hit) => ({
      path: hit.path,
      model: hit.bodySummary.model,
      stream: hit.bodySummary.stream,
      inputType: hit.bodySummary.inputType,
      toolCount: hit.bodySummary.toolCount,
      toolChoice: hit.bodySummary.toolChoice,
      tools: hit.bodySummary.tools
    })))}`
    : ''
  throw new Error(sanitizeSecretText(`${error instanceof Error ? error.stack ?? error.message : String(error)}${routeDebug}${requestDebug}`))
} finally {
  hybridRouteDiagnosticsChannel.unsubscribe(hybridRouteDiagnosticsSubscriber)
  usageRecordQueue.flushAllUsageRecordQueue()
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  auditLogQueue.flushAllAuditLogQueue()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function createGatewayServer(): http.Server {
  const app = express()
  app.use(requestContextMiddleware)
  app.use('/v1', express.raw({ type: () => true, limit: '12mb' }), captureGatewayRawBody, captureIncomingGatewayRequest, dumpGatewayResponseForDebug, openAIGatewayRouter)
  return http.createServer(app)
}

function updateGatewayStreamTimeoutsForTest(): void {
  if (!streamRequestTimeoutSecondsOverride && !streamIdleTimeoutSecondsOverride) return
  repositories.updateSettings({
    ...(streamRequestTimeoutSecondsOverride ? { streamRequestTimeoutSeconds: streamRequestTimeoutSecondsOverride } : {}),
    ...(streamIdleTimeoutSecondsOverride ? { streamIdleTimeoutSeconds: streamIdleTimeoutSecondsOverride } : {})
  })
}

function captureIncomingGatewayRequest(req: Request, _res: Response, next: NextFunction): void {
  let body = parseJsonObject(requestBodyText(req))
  body = maybeForceTextArtifactNoTools(req, body)
  body = maybeForceApplyPatchToolChoice(req, body)
  body = maybeForceApplyPatchOnlyTools(req, body)
  dumpIncomingGatewayRequestForDebug(body)
  gatewayIncomingHits.push({
    path: req.originalUrl || req.url,
    method: req.method,
    authorizationPresent: Boolean(req.headers.authorization),
    codexTurnMetadata: headerText(req.headers['x-codex-turn-metadata']),
    bodySummary: {
      model: typeof body.model === 'string' ? body.model : undefined,
      stream: body.stream === true,
      inputType: Array.isArray(body.input) ? 'array' : typeof body.input,
      toolCount: Array.isArray(body.tools) ? body.tools.length : 0,
      toolChoice: typeof body.tool_choice === 'string' ? body.tool_choice : typeof body.tool_choice,
      tools: summarizeResponseTools(body.tools)
    }
  })
  next()
}

function maybeForceTextArtifactNoTools(req: Request, body: Record<string, unknown>): Record<string, unknown> {
  if (!programmingTaskEnabled || !programmingTextArtifactFallback) {
    return body
  }
  if (!Array.isArray(body.tools) && body.tool_choice === undefined) {
    return body
  }
  const nextBody = {
    ...body,
    tools: [],
    tool_choice: 'none'
  }
  replaceGatewayJsonBody(req, nextBody)
  return nextBody
}

function maybeForceApplyPatchOnlyTools(req: Request, body: Record<string, unknown>): Record<string, unknown> {
  if (!programmingTaskEnabled || !programmingApplyPatchOnlyTools || !Array.isArray(body.tools)) {
    return body
  }
  const tools = body.tools.filter((tool) => isResponseApplyPatchTool(tool))
  if (tools.length === body.tools.length) return body
  const nextBody = {
    ...body,
    tools,
    tool_choice: isToolChoiceStillAvailable(body.tool_choice, tools) ? body.tool_choice : 'auto'
  }
  replaceGatewayJsonBody(req, nextBody)
  return nextBody
}

function isToolChoiceStillAvailable(toolChoice: unknown, tools: unknown[]): boolean {
  if (typeof toolChoice === 'string') return true
  if (!toolChoice || typeof toolChoice !== 'object' || Array.isArray(toolChoice)) return false
  const record = toolChoice as Record<string, unknown>
  if (record.type !== 'custom' || record.name !== 'apply_patch') return false
  return tools.some((tool) => isResponseApplyPatchTool(tool))
}

function dumpIncomingGatewayRequestForDebug(body: Record<string, unknown>): void {
  if (!debugDumpIncomingRequestPath) return
  writeFileSync(debugDumpIncomingRequestPath, `${JSON.stringify(body, null, 2)}\n`, 'utf8')
}

function dumpGatewayResponseForDebug(req: Request, res: Response, next: NextFunction): void {
  if (!debugDumpGatewayResponsePath) {
    next()
    return
  }
  mkdirSync(dirname(debugDumpGatewayResponsePath), { recursive: true })
  const stream = createWriteStream(debugDumpGatewayResponsePath, { flags: 'a' })
  let closed = false
  const writeDump = (chunk: unknown): void => {
    if (closed || chunk === undefined || chunk === null) return
    if (Buffer.isBuffer(chunk)) {
      stream.write(chunk)
      return
    }
    if (chunk instanceof Uint8Array) {
      stream.write(Buffer.from(chunk.buffer, chunk.byteOffset, chunk.byteLength))
      return
    }
    stream.write(String(chunk))
  }
  const finishDump = (label: string): void => {
    if (closed) return
    closed = true
    stream.end(`\n\n--- ${label} ${res.statusCode} ${req.method} ${req.originalUrl || req.url} ---\n`)
  }
  stream.write(`--- response ${req.method} ${req.originalUrl || req.url} ---\n`)
  const originalWrite = res.write.bind(res)
  const originalEnd = res.end.bind(res)
  res.write = ((chunk: unknown, ...args: unknown[]) => {
    writeDump(chunk)
    return originalWrite(chunk as never, ...args as never[])
  }) as typeof res.write
  res.end = ((chunk?: unknown, ...args: unknown[]) => {
    writeDump(chunk)
    finishDump('end')
    return originalEnd(chunk as never, ...args as never[])
  }) as typeof res.end
  res.once('close', () => finishDump('close'))
  next()
}

function maybeForceApplyPatchToolChoice(req: Request, body: Record<string, unknown>): Record<string, unknown> {
  if (!programmingTaskEnabled || !programmingForceApplyPatchToolChoice || programmingForceApplyPatchToolChoiceRemaining <= 0) {
    return body
  }
  if (!Array.isArray(body.tools) || !body.tools.some((tool) => isResponseApplyPatchTool(tool))) {
    return body
  }
  programmingForceApplyPatchToolChoiceRemaining -= 1
  const nextBody = {
    ...body,
    tool_choice: {
      type: 'custom',
      name: 'apply_patch'
    }
  }
  replaceGatewayJsonBody(req, nextBody)
  return nextBody
}

function isResponseApplyPatchTool(value: unknown): boolean {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const record = value as Record<string, unknown>
  return record.type === 'custom' && record.name === 'apply_patch'
}

function summarizeResponseTools(value: unknown): Array<Record<string, unknown>> | undefined {
  if (!Array.isArray(value)) return undefined
  return value.slice(0, 12).map((item) => {
    if (!item || typeof item !== 'object' || Array.isArray(item)) return { type: typeof item }
    const record = item as Record<string, unknown>
    return {
      type: typeof record.type === 'string' ? record.type : undefined,
      name: typeof record.name === 'string' ? record.name : undefined,
      namespace: typeof record.namespace === 'string' ? record.namespace : undefined
    }
  })
}

function registerCustomModels(): void {
  const models = hybridMode
    ? [downstreamModel, hybridScoringModel, ...hybridLevelRoutes().map((route) => route.targetModel)]
    : [downstreamModel, upstreamModel]
  for (const model of new Set(models)) {
    saveCustomProviderModel({
      providerCode: realProvider.providerCode,
      model,
      scope: 'personal',
      systemAccountId: access.systemAccountId,
      status: 'active',
      supportedApiProtocols: ['chat_completions', 'responses'],
      inputUsdPer1M: 0.002,
      outputUsdPer1M: 0.002,
      cachedInputUsdPer1M: 0.0002,
      actorSystemAccountId: access.systemAccountId
    })
  }
}

function createSingleCodexApiKey(): { id: string; key?: string } {
  const group = repositories.createGroup({
    name: `Codex GPT 转 ${realProvider.label} 真实网关分组`,
    providerCode: realProvider.providerCode,
    providerProtocolProfileId: realProvider.profileId,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: realProvider.providerCode,
    providerProtocolProfileId: realProvider.profileId,
    name: `Codex GPT 转 ${realProvider.label} 真实上游账户`,
    type: 'api_key',
    credentials: {
      api_key: realApiKey,
      base_url: realBaseUrl,
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: [upstreamModel],
    modelMappings: [codexBridgeModelMapping(downstreamModel, upstreamModel)]
  }, access)
  return repositories.createApiKeyRecord({
    name: `Codex GPT 转 ${realProvider.label} 真实网关 Key`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
}

function createHybridCodexApiKey(): { id: string; key?: string } {
  const scoring = createRealGroupAccount({
    accountName: 'Codex Hybrid 评分账户',
    clientCompatibility: 'openai_standard',
    groupName: 'Codex Hybrid 评分分组',
    modelMappings: [],
    supportedModel: hybridScoringModel
  })
  const targetGroups = new Map<string, { accountId: string; groupId: string }>()
  for (const route of hybridLevelRoutes()) {
    if (targetGroups.has(route.targetModel)) continue
    targetGroups.set(route.targetModel, createRealGroupAccount({
      accountName: `Codex Hybrid ${route.targetModel} 目标账户`,
      clientCompatibility: 'openai_standard',
      groupName: `Codex Hybrid ${route.targetModel} 目标分组`,
      modelMappings: [
        codexBridgeModelMapping(downstreamModel, route.targetModel),
        codexBridgeModelMapping(route.targetModel, route.targetModel)
      ],
      supportedModel: route.targetModel
    }))
  }
  const groupBindings = [scoring, ...targetGroups.values()].map((item, index) => ({
    groupId: item.groupId,
    priority: index + 1,
    weight: 1,
    status: 'active' as const
  }))
  return repositories.createApiKeyRecord({
    name: 'Codex Hybrid 真实混合路由 Key',
    routeMode: 'hybrid',
    groupRouteStrategy: 'priority_failover',
    groupBindings,
    hybridRoutingConfig: {
      scoringModel: hybridScoringModel,
      scoringContextMode: 'full_request',
      qualityPreference: 'balanced',
      scoringTimeoutMs: 45_000,
      scoringFallbackMaxLevel: 5,
      scoringCacheEnabled: true,
      scoringCacheTtlSeconds: 300,
      cacheAffinityEnabled: true,
      affinityTtlSeconds: 900,
      switchMinLevelDelta: 0,
      downgradeConsecutiveLowCount: 1,
      levelRoutes: hybridLevelRoutes()
    } satisfies ApiKeyHybridRoutingConfig,
    status: 'active'
  }, access)
}

function createRealGroupAccount(input: {
  accountName: string
  clientCompatibility: 'openai_standard' | 'codex_responses'
  groupName: string
  modelMappings: Array<ReturnType<typeof codexBridgeModelMapping>>
  supportedModel: string
}): { accountId: string; groupId: string } {
  const group = repositories.createGroup({
    name: input.groupName,
    providerCode: realProvider.providerCode,
    providerProtocolProfileId: realProvider.profileId,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: realProvider.providerCode,
    providerProtocolProfileId: realProvider.profileId,
    name: input.accountName,
    type: 'api_key',
    clientCompatibility: input.clientCompatibility,
    credentials: {
      api_key: realApiKey,
      base_url: realBaseUrl,
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: [input.supportedModel],
    modelMappings: input.modelMappings
  }, access)
  return { accountId: account.id, groupId: group.id }
}

function codexBridgeModelMapping(sourceModel: string, targetModel: string) {
  return {
    sourceModel,
    sourceEndpointFamily: 'responses' as const,
    upstreamModel: targetModel,
    upstreamEndpointFamily: 'chat_completions' as const,
    enabled: true
  }
}

function hybridLevelRoutes(): ApiKeyHybridRoutingConfig['levelRoutes'] {
  return [
    { minLevel: 1, maxLevel: 2, targetModel: hybridModel1To2, enabled: true },
    { minLevel: 3, maxLevel: 6, targetModel: hybridModel4To6, enabled: true },
    { minLevel: 7, maxLevel: 10, targetModel: hybridModel7To10, enabled: true }
  ]
}

function runCodexCli(gatewayBaseUrl: string, localApiKey: string): Promise<CliRunResult> {
  const cliRoot = join(tempRoot, 'codex-cli')
  const codexHome = join(cliRoot, '.codex')
  mkdirSync(codexHome, { recursive: true })
  return runCli({
    args: [
      'exec',
      '--json',
      '--ephemeral',
      '--ignore-user-config',
      '--skip-git-repo-check',
      '-C',
      cliRoot,
      '-s',
      'read-only',
      '-c',
      'approval_policy=never',
      '-c',
      'model_provider=local_gateway',
      '-c',
      `model=${downstreamModel}`,
      '-c',
      'model_providers.local_gateway.name=LocalGateway',
      '-c',
      `model_providers.local_gateway.base_url=${gatewayBaseUrl}/v1`,
      '-c',
      'model_providers.local_gateway.env_key=JUHE_CODEX_API_KEY',
      '-c',
      'model_providers.local_gateway.wire_api=responses',
      '-c',
      'model_providers.local_gateway.requires_openai_auth=false',
      '-c',
      `model_providers.local_gateway.request_max_retries=${codexRequestMaxRetries}`,
      '-c',
      `model_providers.local_gateway.stream_max_retries=${codexStreamMaxRetries}`,
      '-'
    ],
    cwd: cliRoot,
    env: isolatedCliEnv(cliRoot, {
      CODEX_HOME: codexHome,
      JUHE_CODEX_API_KEY: localApiKey,
      OPENAI_API_KEY: '',
      DISABLE_TELEMETRY: '1'
    }),
    stdinText: `Reply with exactly this marker and nothing else. Do not run tools: ${marker}`,
    timeoutMs: requestTimeoutMs
  })
}

function runCodexProgrammingCli(gatewayBaseUrl: string, localApiKey: string, projectRoot: string): Promise<CliRunResult> {
  const codexHome = join(tempRoot, 'codex-programming-home')
  mkdirSync(codexHome, { recursive: true })
  const chatCompatibleToolArgs = programmingChatCompatibleToolsOnly
    ? [
        '--disable',
        'apps',
        '--disable',
        'plugins',
        '--disable',
        'tool_suggest',
        '--disable',
        'remote_plugin',
        '--disable',
        'multi_agent',
        '--disable',
        'multi_agent_v2',
        '--disable',
        'enable_mcp_apps',
        '--disable',
        'standalone_web_search',
        '--disable',
        'image_generation',
        '--disable',
        'computer_use',
        '--disable',
        'browser_use',
        '--disable',
        'browser_use_external',
        '--disable',
        'in_app_browser',
        '-c',
        'tools.experimental_request_user_input.enabled=false',
        '-c',
        'include_apps_instructions=false',
        '-c',
        'skills.include_instructions=false',
        '-c',
        'skills.bundled.enabled=false',
        '-c',
        'include_permissions_instructions=false',
        '-c',
        'include_collaboration_mode_instructions=false',
        '-c',
        'web_search="disabled"'
      ]
    : []
  const minimalInstructionArgs = programmingMinimalInstructions
    ? [
        '-c',
        `instructions="${programmingMinimalInstructionsText()}"`
      ]
    : []
  return runCli({
    args: [
      'exec',
      '--json',
      '--ephemeral',
      '--ignore-user-config',
      ...chatCompatibleToolArgs,
      ...minimalInstructionArgs,
      ...(programmingDisableShellTool ? ['--disable', 'shell_tool'] : []),
      '--skip-git-repo-check',
      '-C',
      projectRoot,
      '-s',
      programmingSandboxMode,
      '-c',
      'approval_policy=never',
      '-c',
      'model_provider=local_gateway',
      '-c',
      `model=${downstreamModel}`,
      '-c',
      'model_providers.local_gateway.name=LocalGateway',
      '-c',
      `model_providers.local_gateway.base_url=${gatewayBaseUrl}/v1`,
      '-c',
      'model_providers.local_gateway.env_key=JUHE_CODEX_API_KEY',
      '-c',
      'model_providers.local_gateway.wire_api=responses',
      '-c',
      'model_providers.local_gateway.requires_openai_auth=false',
      '-c',
      `model_providers.local_gateway.request_max_retries=${codexRequestMaxRetries}`,
      '-c',
      `model_providers.local_gateway.stream_max_retries=${codexStreamMaxRetries}`,
      '-'
    ],
    cwd: projectRoot,
    env: isolatedCliEnv(join(tempRoot, 'programming-cli-root'), {
      CODEX_HOME: codexHome,
      JUHE_CODEX_API_KEY: localApiKey,
      OPENAI_API_KEY: '',
      DISABLE_TELEMETRY: '1'
    }),
    stdinText: programmingTaskPrompt(),
    timeoutMs: requestTimeoutMs
  })
}

function assertGuidanceCliOutput(stdout: string): void {
  const sanitized = sanitizeSecretText(stdout)
  assert.match(sanitized, /"type":"item\.completed"/, `Codex CLI guidance 应输出 completed item：${sanitized.slice(0, 2000)}`)
  assert.match(sanitized, /能力未执行/, `Codex CLI guidance 应包含能力未执行说明：${sanitized.slice(0, 2000)}`)
  assert.match(sanitized, /tool_search/, `Codex CLI guidance 应包含 tool_search：${sanitized.slice(0, 2000)}`)
  assert.match(sanitized, /web_search/, `Codex CLI guidance 应包含 web_search：${sanitized.slice(0, 2000)}`)
  assert.match(sanitized, /建议下一步/, `Codex CLI guidance 应包含下一步建议：${sanitized.slice(0, 2000)}`)
  assert.doesNotMatch(sanitized, /turn\.failed|stream disconnected|response\.failed|unsupported_codex_native_tool/, `Codex CLI guidance 不应失败或暴露旧错误：${sanitized.slice(0, 2000)}`)
}

function providerFromEnv(): { providerCode: string; profileId: string; label: string } {
  const value = (envText('JUHE_REAL_CODEX_PROVIDER') || 'openai-compatible').toLowerCase()
  if (value === 'openai-compatible' || value === 'openai' || value === 'gpt') {
    return {
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      profileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      label: 'OpenAI Compatible'
    }
  }
  if (value === 'glm') {
    return {
      providerCode: GLM_PROVIDER_CODE,
      profileId: GLM_CODING_OPENAI_V1_PROFILE_ID,
      label: 'GLM'
    }
  }
  if (value === 'deepseek' || value === 'ds') {
    return {
      providerCode: DEEPSEEK_PROVIDER_CODE,
      profileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      label: 'DeepSeek'
    }
  }
  throw new Error(`JUHE_REAL_CODEX_PROVIDER 只支持 openai-compatible、glm、deepseek，实际为 ${value}`)
}

function seedProgrammingProject(): string {
  const projectRoot = programmingProjectRootOverride ? resolve(programmingProjectRootOverride) : defaultProgrammingProjectRoot()
  if (programmingProjectRootOverride && existsSync(projectRoot)) {
    throw new Error(`编程任务输出目录已存在，为避免覆盖请换一个新目录：${projectRoot}`)
  }
  if (programmingTaskKind === 'tetris_html') return seedTetrisHtmlProject(projectRoot)
  if (programmingTaskKind === 'snake_html') return seedSnakeHtmlProject(projectRoot)
  return seedBalancedBracketProject(projectRoot)
}

function defaultProgrammingProjectRoot(): string {
  if (programmingTaskKind === 'tetris_html') {
    return resolve('D:\\Downloads\\temp', `codex-glm-tetris-${Date.now()}`)
  }
  return join(tempRoot, 'programming-task')
}

function seedBalancedBracketProject(projectRoot: string): string {
  mkdirSync(join(projectRoot, 'src'), { recursive: true })
  mkdirSync(join(projectRoot, 'test'), { recursive: true })
  writeFileSync(join(projectRoot, 'package.json'), `${JSON.stringify({
    type: 'module',
    scripts: {
      test: 'node --test test/isBalanced.test.js'
    }
  }, null, 2)}\n`, 'utf8')
  writeFileSync(join(projectRoot, 'src', 'isBalanced.js'), [
    'export function isBalanced(input) {',
    '  throw new Error("TODO: implement");',
    '}',
    ''
  ].join('\n'), 'utf8')
  writeFileSync(join(projectRoot, 'test', 'isBalanced.test.js'), [
    'import test from "node:test";',
    'import assert from "node:assert/strict";',
    'import { isBalanced } from "../src/isBalanced.js";',
    '',
    'test("accepts balanced bracket strings", () => {',
    '  assert.equal(isBalanced(""), true);',
    '  assert.equal(isBalanced("([]{})"), true);',
    '  assert.equal(isBalanced("a(b[c]{d}e)f"), true);',
    '});',
    '',
    'test("rejects unbalanced or misordered brackets", () => {',
    '  assert.equal(isBalanced("("), false);',
    '  assert.equal(isBalanced("([)]"), false);',
    '  assert.equal(isBalanced("(()"), false);',
    '  assert.equal(isBalanced("())"), false);',
    '});',
    ''
  ].join('\n'), 'utf8')
  return projectRoot
}

function seedSnakeHtmlProject(projectRoot: string): string {
  mkdirSync(projectRoot, { recursive: true })
  writeFileSync(join(projectRoot, 'README.md'), [
    '# 贪吃蛇小游戏',
    '',
    '请使用纯 HTML、CSS 和 JavaScript 实现一个可直接打开的贪吃蛇小游戏。',
    ''
  ].join('\n'), 'utf8')
  return projectRoot
}

function seedTetrisHtmlProject(projectRoot: string): string {
  mkdirSync(projectRoot, { recursive: true })
  writeFileSync(join(projectRoot, 'README.md'), [
    '# 俄罗斯方块小游戏',
    '',
    '请使用纯 HTML、CSS 和 JavaScript 实现一个可直接打开的俄罗斯方块小游戏。',
    '必须生成 index.html、styles.css、game.js 三个文件，不要使用外部依赖。',
    ''
  ].join('\n'), 'utf8')
  return projectRoot
}

function programmingTaskPrompt(): string {
  if (programmingTaskKind === 'tetris_html') {
    if (programmingTextArtifactFallback) {
      return [
        '这是一个很小的前端编程任务。',
        '当前上游不提供可靠工具调用能力，所以不要调用工具，也不要回复计划。',
        '请直接生成一个可打开的俄罗斯方块小游戏的三个文件内容。',
        '必须使用纯 HTML、CSS、JavaScript，不要依赖外部 CDN，不要安装依赖。',
        '游戏需要包含：开始、暂停、重新开始、左右移动、软降、硬降、旋转、得分、等级、已消除行数、游戏结束提示。',
        '实现完整的俄罗斯方块核心逻辑：7 种方块、棋盘网格、碰撞检测、方块锁定、消行、速度随等级提升。',
        '需要支持键盘控制和移动端按钮控制，界面文案使用中文，样式完整。',
        '只按下面格式输出，不要使用 Markdown 代码围栏，不要输出额外说明：',
        '@@FILE:index.html',
        '<完整 index.html 内容>',
        '@@END_FILE',
        '@@FILE:styles.css',
        '<完整 styles.css 内容>',
        '@@END_FILE',
        '@@FILE:game.js',
        '<完整 game.js 内容>',
        '@@END_FILE'
      ].join('\n')
    }
    return [
      '这是一个很小的前端编程任务。',
      '请在当前目录创建一个可直接用浏览器打开的俄罗斯方块小游戏。',
      '不要只回复计划或说明；第一步必须创建或修改文件。',
      '请优先使用 apply_patch 工具创建或修改文件，不要用 PowerShell here-string 或 Set-Content 写长 HTML、CSS、JavaScript 内容。',
      '如果最终没有生成 index.html、styles.css、game.js 三个文件，任务就是失败。',
      '必须使用纯 HTML、CSS、JavaScript，至少创建 index.html、styles.css、game.js 三个文件。',
      '游戏需要包含：开始、暂停、重新开始、左右移动、软降、硬降、旋转、得分、等级、已消除行数、游戏结束提示。',
      '实现完整的俄罗斯方块核心逻辑：7 种方块、棋盘网格、碰撞检测、方块锁定、消行、速度随等级提升。',
      '需要支持键盘控制和移动端按钮控制，界面文案使用中文，样式完整，不要依赖外部 CDN，不要安装依赖。',
      '完成后请运行 node --check game.js 做语法检查；如果失败请修复。'
    ].join('\n')
  }
  if (programmingTaskKind === 'snake_html') {
    return [
      '这是一个很小的前端编程任务。',
      '请在当前目录创建一个可直接用浏览器打开的贪吃蛇小游戏。',
      '不要只回复计划或说明；第一步必须创建或修改文件。',
      '请优先使用 apply_patch 工具创建或修改文件，不要用 PowerShell here-string 或 Set-Content 写长 HTML、CSS、JavaScript 内容。',
      '如果最终没有生成 index.html、styles.css、game.js 三个文件，任务就是失败。',
      '必须使用纯 HTML、CSS、JavaScript，至少创建 index.html、styles.css、game.js 三个文件。',
      '游戏需要包含：开始/暂停/重新开始、方向键和 WASD 控制、得分显示、速度随得分提升、撞墙或撞到自己结束、移动端按钮控制。',
      '界面文案使用中文，样式要完整，不要依赖外部 CDN，不要安装依赖。',
      '完成后请运行 node --check game.js 做语法检查；如果失败请修复。'
    ].join('\n')
  }
  return [
    '这是一个很小的编程任务。',
    '请实现 src/isBalanced.js 中的 isBalanced(input) 函数，判断字符串里的 (), [], {} 是否括号平衡。',
    '要求忽略非括号字符，导出函数名保持 isBalanced。',
    '请运行 node --test test/isBalanced.test.js，并修复直到测试通过。',
    '不要安装依赖。'
  ].join('\n')
}

function programmingMinimalInstructionsText(): string {
  if (programmingTextArtifactFallback) {
    return 'You are a coding agent. Return requested file artifacts as plain text in the exact delimiter format. Do not call tools. Keep non-file text out of the response.'
  }
  return 'You are a coding agent. Use apply_patch to create or edit files when needed. Run requested checks when available. Keep final output concise.'
}

function runProjectTests(projectRoot: string): Promise<CliRunResult> {
  if (programmingTaskKind === 'tetris_html') return runTetrisHtmlProjectChecks(projectRoot)
  if (programmingTaskKind === 'snake_html') return runSnakeHtmlProjectChecks(projectRoot)
  return new Promise((resolvePromise, rejectPromise) => {
    const child = spawn(process.execPath, ['--test', 'test/isBalanced.test.js'], {
      cwd: projectRoot,
      env: process.env,
      stdio: ['ignore', 'pipe', 'pipe']
    })
    const stdout: Buffer[] = []
    const stderr: Buffer[] = []
    child.stdout.on('data', (chunk: Buffer) => stdout.push(chunk))
    child.stderr.on('data', (chunk: Buffer) => stderr.push(chunk))
    child.on('error', rejectPromise)
    child.on('close', (code) => resolvePromise({
      exitCode: code,
      stdout: Buffer.concat(stdout).toString('utf8'),
      stderr: Buffer.concat(stderr).toString('utf8')
    }))
  })
}

function materializeProgrammingTextArtifacts(stdout: string, projectRoot: string): void {
  const text = latestCodexAgentMessageText(stdout)
  const requiredFiles = ['index.html', 'styles.css', 'game.js']
  const delimitedArtifacts = parseDelimitedFileArtifacts(text)
  const patchArtifacts = requiredFiles.every((file) => delimitedArtifacts.has(file))
    ? delimitedArtifacts
    : parseApplyPatchFileArtifacts(text)
  const artifacts = requiredFiles.every((file) => patchArtifacts.has(file))
    ? patchArtifacts
    : parseHeaderDelimitedFileArtifacts(text)
  const missing = requiredFiles.filter((file) => !artifacts.has(file))
  if (missing.length > 0) {
    throw new Error(`Codex 文本产物缺少文件块：${missing.join(', ')}；输出片段=${sanitizeSecretText(text).slice(0, 1200)}`)
  }
  mkdirSync(projectRoot, { recursive: true })
  for (const file of requiredFiles) {
    writeFileSync(join(projectRoot, file), `${artifacts.get(file) ?? ''}`.replace(/\s+$/u, '') + '\n', 'utf8')
  }
}

function latestCodexAgentMessageText(stdout: string): string {
  let latest = ''
  for (const line of stdout.split(/\r?\n/)) {
    const trimmed = line.trim()
    if (!trimmed) continue
    const event = parseJsonObject(trimmed)
    const item = event.item
    if (!item || typeof item !== 'object' || Array.isArray(item)) continue
    const record = item as Record<string, unknown>
    if (event.type === 'item.completed' && record.type === 'agent_message' && typeof record.text === 'string') {
      latest = record.text
    }
  }
  if (!latest) {
    throw new Error(`未在 Codex CLI JSONL 输出中找到 agent_message：${sanitizeSecretText(stdout).slice(0, 1200)}`)
  }
  return latest
}

function parseDelimitedFileArtifacts(text: string): Map<string, string> {
  const allowedFiles = new Set(['index.html', 'styles.css', 'game.js'])
  const files = new Map<string, string>()
  const pattern = /^@@FILE:([^\r\n]+)\r?\n([\s\S]*?)^@@END_FILE[ \t]*$/gm
  let match: RegExpExecArray | null
  while ((match = pattern.exec(text)) !== null) {
    const filename = match[1]?.trim()
    if (!filename || !allowedFiles.has(filename)) continue
    files.set(filename, match[2] ?? '')
  }
  return files
}

function parseApplyPatchFileArtifacts(text: string): Map<string, string> {
  const allowedFiles = new Set(['index.html', 'styles.css', 'game.js'])
  const files = new Map<string, string>()
  let currentFile: string | undefined
  let currentLines: string[] = []
  const flush = () => {
    if (currentFile && allowedFiles.has(currentFile)) {
      files.set(currentFile, currentLines.join('\n'))
    }
    currentFile = undefined
    currentLines = []
  }
  for (const rawLine of text.split(/\r?\n/)) {
    if (rawLine.startsWith('*** Add File: ') || rawLine.startsWith('*** Update File: ')) {
      flush()
      const filename = rawLine.replace(/^\*\*\* (?:Add|Update) File:\s*/, '').trim()
      currentFile = allowedFiles.has(filename) ? filename : undefined
      currentLines = []
      continue
    }
    if (rawLine.startsWith('*** End Patch') || rawLine.startsWith('*** Delete File: ') || rawLine.startsWith('*** Move to: ')) {
      flush()
      continue
    }
    if (!currentFile) continue
    if (rawLine.startsWith('+') && !rawLine.startsWith('+++')) {
      currentLines.push(rawLine.slice(1))
    }
  }
  flush()
  return files
}

function parseHeaderDelimitedFileArtifacts(text: string): Map<string, string> {
  const allowedFiles = new Set(['index.html', 'styles.css', 'game.js'])
  const files = new Map<string, string>()
  let currentFile: string | undefined
  let currentLines: string[] = []
  const flush = () => {
    if (currentFile && allowedFiles.has(currentFile)) {
      files.set(currentFile, trimCodeFence(currentLines.join('\n')))
    }
    currentFile = undefined
    currentLines = []
  }
  for (const rawLine of text.split(/\r?\n/)) {
    const trimmed = rawLine.trim()
    const header = /^-{3,}\s*(index\.html|styles\.css|game\.js)\s*-{3,}\s*$/i.exec(trimmed)
    if (header) {
      flush()
      currentFile = header[1]
      currentLines = []
      continue
    }
    if (/^-{3,}.+-{3,}$/u.test(trimmed)) {
      flush()
      continue
    }
    if (currentFile) {
      currentLines.push(rawLine)
    }
  }
  flush()
  return files
}

function trimCodeFence(value: string): string {
  const lines = value.replace(/\s+$/u, '').split(/\r?\n/)
  if (lines[0]?.trim().startsWith('```')) {
    lines.shift()
  }
  if (lines.at(-1)?.trim() === '```') {
    lines.pop()
  }
  return lines.join('\n')
}

async function runSnakeHtmlProjectChecks(projectRoot: string): Promise<CliRunResult> {
  return runHtmlGameProjectChecks(projectRoot, [
    sourceMatches(/keydown|keyup|pointerdown|touchstart|click/i),
    sourceMatches(/score|得分|分数/i),
    sourceMatches(/restart|重新|reset/i),
    sourceMatches(/@media|grid|flex|canvas|button/i)
  ])
}

async function runTetrisHtmlProjectChecks(projectRoot: string): Promise<CliRunResult> {
  return runHtmlGameProjectChecks(projectRoot, [
    sourceMatches(/keydown|keyup|pointerdown|touchstart|click/i),
    sourceMatches(/score|得分|分数/i),
    sourceMatches(/start|pause|restart|reset|开始|暂停|重新/i),
    sourceMatches(/rotate|旋转/i),
    sourceMatches(/line|clear|消除/i),
    sourceMatches(/collision|collide|lock|碰撞|锁定/i),
    sourceMatches(/tetromino|piece|shape|[IOTSZJL]\s*[:=]/i),
    sourceMatches(/game\s*over|结束/i),
    sourceMatches(/@media|grid|flex|canvas|button/i)
  ])
}

function sourceMatches(pattern: RegExp): (source: string) => boolean {
  return (source) => pattern.test(source)
}

async function runHtmlGameProjectChecks(projectRoot: string, sourceChecks: Array<(source: string) => boolean>): Promise<CliRunResult> {
  const requiredFiles = ['index.html', 'styles.css', 'game.js']
  const missing = requiredFiles.filter((file) => !existsSync(join(projectRoot, file)))
  if (missing.length) {
    return {
      exitCode: 1,
      stdout: '',
      stderr: `缺少文件：${missing.join(', ')}`
    }
  }
  const html = readFileSync(join(projectRoot, 'index.html'), 'utf8')
  const js = readFileSync(join(projectRoot, 'game.js'), 'utf8')
  const css = readFileSync(join(projectRoot, 'styles.css'), 'utf8')
  const htmlChecks = [
    html.includes('styles.css'),
    html.includes('game.js'),
    /<canvas\b|class=["'][^"']*(board|game|grid)/i.test(html)
  ]
  const source = `${js}\n${html}\n${css}`
  const sourceCheckResults = sourceChecks.map((check) => check(source))
  const failedChecks = [
    ...htmlChecks.map((ok, index) => ok ? undefined : `HTML 检查 ${index + 1} 失败`),
    ...sourceCheckResults.map((ok, index) => ok ? undefined : `源码检查 ${index + 1} 失败`)
  ].filter(Boolean)
  if (failedChecks.length) {
    return {
      exitCode: 1,
      stdout: '',
      stderr: failedChecks.join('; ')
    }
  }
  return new Promise((resolvePromise, rejectPromise) => {
    const child = spawn(process.execPath, ['--check', 'game.js'], {
      cwd: projectRoot,
      env: process.env,
      stdio: ['ignore', 'pipe', 'pipe']
    })
    const stdout: Buffer[] = []
    const stderr: Buffer[] = []
    child.stdout.on('data', (chunk: Buffer) => stdout.push(chunk))
    child.stderr.on('data', (chunk: Buffer) => stderr.push(chunk))
    child.on('error', rejectPromise)
    child.on('close', (code) => resolvePromise({
      exitCode: code,
      stdout: Buffer.concat(stdout).toString('utf8'),
      stderr: Buffer.concat(stderr).toString('utf8')
    }))
  })
}

function runCli(input: {
  args: string[]
  cwd: string
  env: Record<string, string>
  stdinText: string
  timeoutMs: number
}): Promise<CliRunResult> {
  return new Promise((resolvePromise, rejectPromise) => {
    let settled = false
    const launcher = process.platform === 'win32' ? resolveWindowsNodeCliLauncher('codex') : undefined
    const child = spawn(launcher?.command ?? 'codex', [...(launcher?.args ?? []), ...input.args], {
      cwd: input.cwd,
      env: input.env,
      stdio: ['pipe', 'pipe', 'pipe'],
      windowsHide: true
    })
    const stdout: Buffer[] = []
    const stderr: Buffer[] = []
    const timeout = setTimeout(() => {
      child.kill()
      settle(() => rejectPromise(new Error(`codex 超时；stdout=${sanitizeSecretText(Buffer.concat(stdout).toString('utf8')).slice(0, 1200)}；stderr=${sanitizeSecretText(Buffer.concat(stderr).toString('utf8')).slice(0, 1200)}`)))
    }, input.timeoutMs)
    const settle = (fn: () => void) => {
      if (settled) return
      settled = true
      fn()
    }
    child.stdout.on('data', (chunk: Buffer) => stdout.push(chunk))
    child.stderr.on('data', (chunk: Buffer) => stderr.push(chunk))
    child.stdin.end(input.stdinText)
    child.on('error', (error) => {
      clearTimeout(timeout)
      settle(() => rejectPromise(error))
    })
    child.on('close', (code) => {
      clearTimeout(timeout)
      settle(() => resolvePromise({
        exitCode: code,
        stdout: Buffer.concat(stdout).toString('utf8'),
        stderr: Buffer.concat(stderr).toString('utf8')
      }))
    })
  })
}

function resolveWindowsNodeCliLauncher(command: string): { command: string; args: string[] } | undefined {
  const entryByCommand: Record<string, string> = {
    codex: 'node_modules\\@openai\\codex\\bin\\codex.js'
  }
  const entry = entryByCommand[command]
  if (!entry) return undefined
  const commandPath = findOnPath(`${command}.cmd`) ?? findOnPath(`${command}.ps1`) ?? findOnPath(command)
  if (!commandPath) return undefined
  const baseDir = dirname(commandPath)
  const entryPath = join(baseDir, entry)
  if (!existsSync(entryPath)) return undefined
  const bundledNode = join(baseDir, 'node.exe')
  return {
    command: existsSync(bundledNode) ? bundledNode : 'node.exe',
    args: [entryPath]
  }
}

function findOnPath(filename: string): string | undefined {
  const pathValue = process.env.PATH ?? process.env.Path ?? ''
  for (const rawDir of pathValue.split(pathDelimiter)) {
    const dir = rawDir.trim().replace(/^"|"$/g, '')
    if (!dir) continue
    const candidate = join(dir, filename)
    if (existsSync(candidate)) return candidate
  }
  return undefined
}

function isolatedCliEnv(cliRoot: string, extra: Record<string, string>): Record<string, string> {
  const appData = join(cliRoot, 'AppData', 'Roaming')
  const localAppData = join(cliRoot, 'AppData', 'Local')
  const xdgConfig = join(cliRoot, '.config')
  const xdgData = join(cliRoot, '.local', 'share')
  const xdgCache = join(cliRoot, '.cache')
  mkdirSync(appData, { recursive: true })
  mkdirSync(localAppData, { recursive: true })
  mkdirSync(xdgConfig, { recursive: true })
  mkdirSync(xdgData, { recursive: true })
  mkdirSync(xdgCache, { recursive: true })
  const env = { ...process.env } as Record<string, string>
  for (const key of Object.keys(env)) {
    if (isAiCredentialEnvName(key)) delete env[key]
  }
  return {
    ...env,
    HOME: cliRoot,
    USERPROFILE: cliRoot,
    APPDATA: appData,
    LOCALAPPDATA: localAppData,
    XDG_CONFIG_HOME: xdgConfig,
    XDG_DATA_HOME: xdgData,
    XDG_CACHE_HOME: xdgCache,
    ...extra
  }
}

function isAiCredentialEnvName(key: string): boolean {
  const normalized = key.toUpperCase()
  return normalized.includes('OPENAI')
    || normalized.includes('ANTHROPIC')
    || normalized.includes('CLAUDE')
    || normalized.includes('CODEX')
    || normalized.includes('OPENCODE')
    || normalized.includes('DEEPSEEK')
    || normalized.includes('GLM')
    || normalized.endsWith('API_KEY')
    || normalized.endsWith('AUTH_TOKEN')
    || normalized.endsWith('ACCESS_TOKEN')
}

function parseJsonObject(text: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(text) as unknown
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function requestBodyText(req: Request): string {
  const rawBody = (req as { rawBody?: Buffer }).rawBody
  return rawBody ? rawBody.toString('utf8') : ''
}

function hasValidCodexTurnId(value: string | undefined): boolean {
  if (!value) return false
  const parsed = parseJsonObject(value)
  return typeof parsed.turn_id === 'string' && Boolean(parsed.turn_id.trim())
}

function headerText(value: string | string[] | undefined): string | undefined {
  return Array.isArray(value) ? value[0] : value
}

function requiredEnv(name: string, aliases: string[] = []): string {
  const value = envText(name, aliases)
  if (!value) throw new Error(`${name} 未设置`)
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

function booleanEnv(name: string): boolean {
  const value = envText(name)?.toLowerCase()
  return value === '1' || value === 'true' || value === 'yes'
}

function sanitizeBaseUrl(value: string): string {
  try {
    const url = new URL(value)
    url.username = ''
    url.password = ''
    return url.toString().replace(/\/$/, '')
  } catch {
    return sanitizeSecretText(value)
  }
}

function sanitizeSecretText(text: string): string {
  return text
    .replaceAll(realApiKey, '[redacted-real-api-key]')
    .replaceAll(encodeURIComponent(realApiKey), '[redacted-real-api-key]')
}

function summarizeCliFailure(result: CliRunResult): string {
  return JSON.stringify({
    exitCode: result.exitCode,
    stdout: sanitizeSecretText(result.stdout).slice(0, 1200),
    stderr: sanitizeSecretText(result.stderr).slice(0, 1200)
  })
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  server.listen(0, '127.0.0.1')
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(typeof address === 'object' && address !== null, 'server 未监听端口')
  return address.port
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
