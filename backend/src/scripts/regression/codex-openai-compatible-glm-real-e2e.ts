import { strict as assert } from 'node:assert'
import { channel } from 'node:diagnostics_channel'
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
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
const hybridModel1To3 = envText('JUHE_REAL_CODEX_HYBRID_MODEL_1_3') || 'gpt-5.4-mini'
const hybridModel4To6 = envText('JUHE_REAL_CODEX_HYBRID_MODEL_4_6') || 'gpt-5.4'
const hybridModel7To10 = envText('JUHE_REAL_CODEX_HYBRID_MODEL_7_10') || 'gpt-5.5'
const requestTimeoutMs = positiveIntegerEnv('JUHE_REAL_CODEX_CLI_TIMEOUT_MS') ?? 240_000
const streamRequestTimeoutSecondsOverride = positiveIntegerEnv('JUHE_REAL_CODEX_STREAM_REQUEST_TIMEOUT_SECONDS')
const streamIdleTimeoutSecondsOverride = positiveIntegerEnv('JUHE_REAL_CODEX_STREAM_IDLE_TIMEOUT_SECONDS')
const programmingTaskEnabled = booleanEnv('JUHE_REAL_CODEX_PROGRAMMING_TASK')
const programmingTaskKind = envText('JUHE_REAL_CODEX_PROGRAMMING_TASK_KIND') || 'balanced_brackets'
const programmingProjectRootOverride = envText('JUHE_REAL_CODEX_PROGRAMMING_PROJECT_ROOT')
const programmingSandboxMode = envText('JUHE_REAL_CODEX_PROGRAMMING_SANDBOX') || 'workspace-write'
const programmingDisableShellTool = booleanEnv('JUHE_REAL_CODEX_PROGRAMMING_DISABLE_SHELL_TOOL')
const programmingForceApplyPatchToolChoice = booleanEnv('JUHE_REAL_CODEX_PROGRAMMING_FORCE_APPLY_PATCH_TOOL_CHOICE')
const programmingForceApplyPatchToolChoiceCount = positiveIntegerEnv('JUHE_REAL_CODEX_PROGRAMMING_FORCE_APPLY_PATCH_TOOL_CHOICE_COUNT') ?? 1
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
      const testResult = await runProjectTests(programmingProjectRoot)
      if (testResult.exitCode !== 0) {
        throw new Error(`Codex 编程任务完成后测试应通过；codex=${summarizeCliFailure(result)}；check=${summarizeCliFailure(testResult)}`)
      }
    } else {
      assert.match(result.stdout, new RegExp(marker), `Codex CLI 输出应包含 marker：${sanitizeSecretText(result.stdout).slice(0, 2000)}`)
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
    const expectedSuccessModels = hybridMode
      ? new Set(hybridLevelRoutes().map((route) => route.targetModel))
      : new Set([downstreamModel])
    assert(
      successfulGatewayModels.some((model) => expectedSuccessModels.has(model)),
      `使用记录应保存成功模型，实际：${successfulGatewayModels.join(', ') || '无'}`
    )

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
      mode: programmingProjectRoot ? 'programming_task' : 'marker',
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
  app.use('/v1', express.raw({ type: () => true, limit: '12mb' }), captureGatewayRawBody, captureIncomingGatewayRequest, openAIGatewayRouter)
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
  const body = maybeForceApplyPatchToolChoice(req, parseJsonObject(requestBodyText(req)))
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
    { minLevel: 1, maxLevel: 3, targetModel: hybridModel1To3, enabled: true },
    { minLevel: 4, maxLevel: 6, targetModel: hybridModel4To6, enabled: true },
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
      'model_providers.local_gateway.request_max_retries=0',
      '-c',
      'model_providers.local_gateway.stream_max_retries=0',
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
  return runCli({
    args: [
      'exec',
      '--json',
      '--ephemeral',
      '--ignore-user-config',
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
      'model_providers.local_gateway.request_max_retries=0',
      '-c',
      'model_providers.local_gateway.stream_max_retries=0',
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
  const projectRoot = programmingProjectRootOverride ? resolve(programmingProjectRootOverride) : join(tempRoot, 'programming-task')
  if (programmingProjectRootOverride && existsSync(projectRoot)) {
    throw new Error(`编程任务输出目录已存在，为避免覆盖请换一个新目录：${projectRoot}`)
  }
  if (programmingTaskKind === 'snake_html') return seedSnakeHtmlProject(projectRoot)
  return seedBalancedBracketProject(projectRoot)
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

function programmingTaskPrompt(): string {
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

function runProjectTests(projectRoot: string): Promise<CliRunResult> {
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

async function runSnakeHtmlProjectChecks(projectRoot: string): Promise<CliRunResult> {
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
  const sourceChecks = [
    /keydown|keyup|pointerdown|touchstart|click/i.test(js),
    /score|得分|分数/i.test(js + html),
    /restart|重新|reset/i.test(js + html),
    /@media|grid|flex|canvas|button/i.test(css + html)
  ]
  const failedChecks = [
    ...htmlChecks.map((ok, index) => ok ? undefined : `HTML 检查 ${index + 1} 失败`),
    ...sourceChecks.map((ok, index) => ok ? undefined : `源码检查 ${index + 1} 失败`)
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
