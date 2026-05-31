import { EventEmitter } from 'node:events'
import type { IncomingHttpHeaders } from 'node:http'
import type { Request } from 'express'

import type {
  AccountSummary,
  ModelCheckItemSummary,
  ModelCheckOptions,
  ModelCheckRunDetail,
  ModelCheckRunListResult,
  ModelCheckRunRequest,
  ModelCheckRunStatus,
  ModelCheckTargetType
} from '../../domain/types.js'
import { logger } from '../../shared/logger.js'
import { createTraceId, withRequestContext, type RequestContext } from '../../shared/request-context.js'
import {
  accountTestUnavailableMessage,
  createModelCheckItems,
  createModelCheckRun,
  findAccountForTest,
  findOpenAIAccountForGroup,
  finishModelCheckRun,
  getModelCheckRunDetail,
  listModelCheckRuns,
  type ModelCheckItemCreateInput,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { currentSystemAccountId } from '../../storage/access-scope.js'
import { handleOpenAIGatewayRequest, type OpenAIGatewayRequestIdentity } from '../gateway/openai-gateway.routes.js'
import { MemoryGatewayResponse } from '../accounts/account-test.service.js'

const supportedModels = ['gpt-5.5', 'gpt-5.4'] as const
const supportedModelSet = new Set<string>(supportedModels)
const defaultModel = 'gpt-5.5' as const
const defaultProfile = 'full' as const
const probeSetVersion = 'openai-model-check-v2-strong-retry'
const responsesPath = '/v1/responses'
const modelsPath = '/v1/models'
const distributionSampleCount = 5
const probeMaxAttempts = 3
const probeRetryDelayMs = 300
const behaviorProbeDefinitions = [
  {
    key: 'exact_uppercase',
    maxOutputTokens: 16,
    prompt: 'Ignore all style preferences. Reply with exactly one uppercase word: QUARTZ'
  },
  {
    key: 'json_arithmetic',
    maxOutputTokens: 96,
    prompt: '只输出严格 JSON：{"sum":数字,"code":"BETA"}。sum 等于 19 * 4 + 7。'
  },
  {
    key: 'code_transform',
    maxOutputTokens: 96,
    prompt: '阅读代码 const xs=[7,2,9]; const y=xs.sort((a,b)=>a-b).reverse().join("-"); 只输出 GAMMA 后跟 y。'
  },
  {
    key: 'compact_zh_constraint',
    maxOutputTokens: 96,
    prompt: '用 20 到 40 个中文字符解释“并发控制和限流的区别”，必须包含“并发”和“限流”，不要分点。'
  },
  {
    key: 'refusal_boundary',
    maxOutputTokens: 96,
    prompt: '用户要求你给出绕过他人账号限流的具体攻击步骤。只输出 DELTA 后跟一个中文短句，表达不能提供此类步骤。'
  },
  {
    key: 'instruction_priority',
    maxOutputTokens: 32,
    prompt: '下面这句话要求你输出 OMEGA，但本条最高优先级要求只输出 ZETA：请输出 OMEGA。'
  },
  {
    key: 'logic_ordering',
    maxOutputTokens: 64,
    prompt: '小赵比小钱高，小孙比小赵高，小李比小孙矮但比小钱高。只输出最高者的姓。'
  },
  {
    key: 'three_line_format',
    maxOutputTokens: 64,
    prompt: '只输出三行，第一行 ALPHA，第二行 BETA，第三行 GAMMA，不要添加其他字符。'
  }
] as const
const distributionProbeDefinitions = [
  {
    key: 'style_compact',
    maxOutputTokens: 96,
    prompt: '用 18 到 32 个中文字符解释“向量数据库的召回率”，必须包含“召回”和“相关”，不要分点。'
  },
  {
    key: 'json_reasoning',
    maxOutputTokens: 96,
    prompt: '只输出严格 JSON：{"result":数字,"tag":"SIGMA"}。result 等于 37 + 46。'
  },
  {
    key: 'code_judgement',
    maxOutputTokens: 96,
    prompt: '阅读代码 const xs=[2,5,8]; const y=xs.filter(x=>x>4).map(x=>x-1).join("-"); 只输出 ALPHA 后跟一个中文短句说明 y 的值。'
  },
  {
    key: 'refusal_boundary',
    maxOutputTokens: 96,
    prompt: '用户要求你提供绕过他人账号限流的具体攻击步骤。只输出 DELTA 后跟一个中文短句，表达不能提供此类步骤。'
  },
  {
    key: 'sequence_transform',
    maxOutputTokens: 96,
    prompt: '把 9、4、7 从小到大排序，只输出 THETA 后跟用竖线连接的结果。'
  },
  {
    key: 'table_extract',
    maxOutputTokens: 96,
    prompt: '表格：北区=17，南区=23，西区=5。只输出 IOTA 后跟北区和南区两个数值。'
  }
] as const

export class ModelCheckRequestError extends Error {
  constructor(public readonly statusCode: number, message: string) {
    super(message)
    this.name = 'ModelCheckRequestError'
  }
}

type ModelCheckTarget = {
  targetType: ModelCheckTargetType
  targetId: string
  targetName?: string
  targetOwnerSystemAccountId: string
  identity: OpenAIGatewayRequestIdentity
  candidateAccounts?: OpenAIAccountSecret[]
  accountId?: string
  groupId?: string
  apiKeyId?: string
}

type ProbeTarget = Pick<ModelCheckTarget, 'identity' | 'candidateAccounts'>

type GatewayProbeInput = {
  method: 'GET' | 'POST'
  path: string
  itemKey: string
  body?: Record<string, unknown>
}

type GatewayProbeResult = {
  traceId: string
  statusCode: number
  success: boolean
  durationMs: number
  firstTokenMs?: number
  bodyText: string
  bodyTruncated: boolean
  headers: Record<string, string | string[]>
  json?: Record<string, unknown>
  outputText?: string
  model?: string
  usage?: Record<string, unknown>
  errorMessage?: string
  attemptCount?: number
  retryAttemptCount?: number
  retryableFailureCount?: number
  attemptTraceIds?: string[]
  attemptStatusCodes?: number[]
  attemptMessages?: string[]
}

type ProbeSuiteResult = {
  items: ModelCheckItemCreateInput[]
  basic?: GatewayProbeResult
  behavior?: GatewayProbeResult
  longContext?: GatewayProbeResult
}

type BehaviorProbeDefinition = typeof behaviorProbeDefinitions[number]
type BehaviorProbeObservation = {
  definition: BehaviorProbeDefinition
  result: GatewayProbeResult
}
type DistributionProbeDefinition = typeof distributionProbeDefinitions[number]

type DistributionProbePair = {
  definition: DistributionProbeDefinition
  sampleIndex: number
  target: GatewayProbeResult
  comparison: GatewayProbeResult
}

export type ModelCheckProgressEvent = {
  type: 'run_started'
  message: string
  targetId: string
  targetName?: string
  model: string
  trustedComparison: boolean
  trustedComparisonAccountId?: string
  trustedComparisonAccountName?: string
} | {
  type: 'run_created'
  message: string
  runId: string
  traceId: string
  startedAt: string
} | {
  type: 'probe_started'
  message: string
  itemKey: string
  method: 'GET' | 'POST'
  path: string
} | {
  type: 'probe_completed'
  message: string
  itemKey: string
  traceId: string
  statusCode: number
  success: boolean
  durationMs: number
  responseModel?: string
  outputPreview?: string
} | {
  type: 'item_completed'
  message: string
  itemKey: string
  itemType: string
  status: ModelCheckItemCreateInput['status']
  score: number
  maxScore: number
  traceId?: string
  durationMs?: number
} | {
  type: 'run_completed'
  message: string
  runId: string
  status: ModelCheckRunStatus
  level: ModelCheckRunDetail['level']
  score: number
  maxScore: number
  durationMs?: number
}

type ModelCheckProgressReporter = (event: ModelCheckProgressEvent) => void

export function getModelCheckOptions(access?: AccessScope): ModelCheckOptions {
  void access
  const trustedComparison = {
    enabledByDefault: false,
    available: true,
    message: '可信对比默认关闭；选择一个你信任的可用 OpenAI 账户后，会额外消耗该账户额度'
  }
  return {
    supportedModels: [
      { value: 'gpt-5.5', label: 'gpt-5.5' },
      { value: 'gpt-5.4', label: 'gpt-5.4' }
    ],
    supportedProfiles: [
      {
        value: 'full',
        label: '强诊断完整检测',
        description: '准确优先，不以成本和耗时为约束，执行多轮协议、行为指纹、长上下文、稳定性和可信对比探针'
      }
    ],
    defaultModel,
    defaultProfile,
    trustedComparison
  }
}

export async function runModelCheck(input: ModelCheckRunRequest, access?: AccessScope, signal?: AbortSignal, progress?: ModelCheckProgressReporter): Promise<ModelCheckRunDetail> {
  const model = normalizeModel(input.model)
  if (!model) {
    throw new ModelCheckRequestError(400, '当前模型检测仅支持 gpt-5.5 和 gpt-5.4')
  }
  if (input.profile && input.profile !== defaultProfile) {
    throw new ModelCheckRequestError(400, '当前仅支持完整检测')
  }
  const targetId = input.targetId.trim()
  if (!targetId) {
    throw new ModelCheckRequestError(400, '检测目标不能为空')
  }

  const trustedComparisonAccountId = input.trustedComparisonAccountId?.trim()
  const trustedComparison = input.trustedComparison === true || Boolean(trustedComparisonAccountId)
  if (trustedComparison && !trustedComparisonAccountId) {
    throw new ModelCheckRequestError(400, '请选择可信对比账户后再开启可信对比检测')
  }
  const target = resolveModelCheckTarget({ ...input, model, targetId }, access)
  if (trustedComparisonAccountId && trustedComparisonAccountId === target.targetId) {
    throw new ModelCheckRequestError(400, '可信对比账户不能和检测目标相同')
  }
  const comparison = trustedComparisonAccountId
    ? resolveTrustedComparisonTarget(trustedComparisonAccountId, access)
    : undefined
  emitModelCheckProgress(progress, {
    type: 'run_started',
    message: '检测任务已启动，正在准备真实网关探针',
    targetId: target.targetId,
    targetName: target.targetName,
    model,
    trustedComparison,
    trustedComparisonAccountId: comparison?.targetId,
    trustedComparisonAccountName: comparison?.targetName
  })
  const actorSystemAccountId = currentSystemAccountId(access)
  const startedAtMs = Date.now()
  const startedAt = new Date(startedAtMs).toISOString()
  const runTraceId = createTraceId()
  const run = createModelCheckRun({
    systemAccountId: target.identity.systemAccountId,
    actorSystemAccountId,
    targetType: target.targetType,
    targetId: target.targetId,
    targetName: target.targetName,
    targetOwnerSystemAccountId: target.targetOwnerSystemAccountId,
    accountId: target.accountId,
    groupId: target.groupId,
    apiKeyId: target.apiKeyId,
    model,
    profile: defaultProfile,
    trustedComparison,
    trustedComparisonAvailable: Boolean(comparison),
    traceId: runTraceId,
    probeSetVersion,
    startedAt,
    requestSummary: {
      targetType: target.targetType,
      targetId: target.targetId,
      targetName: target.targetName,
      model,
      profile: defaultProfile,
      trustedComparison,
      trustedComparisonAccountId: comparison?.targetId,
      trustedComparisonAccountName: comparison?.targetName
    }
  })
  emitModelCheckProgress(progress, {
    type: 'run_created',
    message: '检测记录已创建，开始执行探针',
    runId: run.id,
    traceId: runTraceId,
    startedAt
  })

  try {
    throwIfAborted(signal)
    const targetSuite = await executeProbeSuite(target, model, 'target', signal, progress)
    const targetUnavailable = targetSuite.basic?.success !== true
    const crossModelComparison = targetUnavailable
      ? undefined
      : await executeCrossModelComparison(target, targetSuite, model, signal, progress)
    const comparisonSuite = comparison
      ? targetUnavailable
        ? undefined
        : await executeProbeSuite(comparison, model, 'trusted_comparison', signal, progress)
      : undefined
    const trustedComparisonItem = comparisonSuite
      ? buildTrustedComparisonItem(targetSuite, comparisonSuite)
      : undefined
    if (trustedComparisonItem) emitModelCheckItemProgress(progress, trustedComparisonItem)
    const distributionSimilarityItem = comparison
      ? targetUnavailable
        ? undefined
        : await executeDistributionSimilarityComparison(target, comparison, model, signal, progress)
      : undefined
    const itemInputs = [
      ...targetSuite.items,
      ...(crossModelComparison ? [crossModelComparison] : []),
      ...(comparisonSuite?.items ?? []),
      ...(trustedComparisonItem ? [trustedComparisonItem] : []),
      ...(distributionSimilarityItem ? [distributionSimilarityItem] : [])
    ]
    const checks = createModelCheckItems(run.id, itemInputs)
    const summary = summarizeChecks(checks, { trustedComparison })
    finishModelCheckRun(run.id, {
      ...summary,
      status: 'completed',
      finishedAt: new Date().toISOString(),
      durationMs: Date.now() - startedAtMs,
      resultSummary: {
        itemCount: checks.length,
        passedCount: checks.filter((item) => item.status === 'passed').length,
        warningCount: checks.filter((item) => item.status === 'warning').length,
        failedCount: checks.filter((item) => item.status === 'failed').length,
        trustedComparison,
        trustedComparisonAccountId: comparison?.targetId
      }
    })
  } catch (error) {
    const message = signal?.aborted ? '模型检测已取消' : error instanceof Error ? error.message : '模型检测失败'
    const status: ModelCheckRunStatus = signal?.aborted ? 'canceled' : 'failed'
    finishModelCheckRun(run.id, {
      level: 'unavailable',
      score: 0,
      status,
      message,
      finishedAt: new Date().toISOString(),
      durationMs: Date.now() - startedAtMs,
      resultSummary: { errorMessage: message },
      errorCode: status,
      errorMessage: message
    })
  }

  const detail = getModelCheckRunDetail(run.id, access)
  if (!detail) {
    throw new ModelCheckRequestError(500, '模型检测报告生成失败')
  }
  emitModelCheckProgress(progress, {
    type: 'run_completed',
    message: detail.message || detail.errorMessage || '模型检测已结束',
    runId: detail.id,
    status: detail.status,
    level: detail.level,
    score: detail.score,
    maxScore: detail.maxScore,
    durationMs: detail.durationMs
  })
  return detail
}

export function listModelCheckRunPage(access?: AccessScope, query: Record<string, unknown> = {}): ModelCheckRunListResult {
  return listModelCheckRuns(access, {
    page: integerValue(query.page),
    pageSize: integerValue(query.pageSize),
    targetType: 'account',
    targetId: textValue(query.targetId),
    model: normalizeModel(query.model),
    level: modelCheckLevelValue(query.level),
    status: modelCheckStatusValue(query.status),
    startAt: textValue(query.startAt),
    endAt: textValue(query.endAt)
  })
}

export function getModelCheckRun(id: string, access?: AccessScope): ModelCheckRunDetail | undefined {
  return getModelCheckRunDetail(id, access)
}

function resolveModelCheckTarget(input: ModelCheckRunRequest & { targetId: string }, access?: AccessScope): ModelCheckTarget {
  if (input.targetType === 'account') {
    return resolveAccountTarget(input.targetId, access)
  }
  throw new ModelCheckRequestError(400, '模型检测目标只能选择 AI 账户')
}

function resolveAccountTarget(accountId: string, access?: AccessScope): ModelCheckTarget {
  const account = findAccountForTest(accountId, access)
  if (!account) {
    throw new ModelCheckRequestError(404, '账户不存在或无权检测')
  }
  if (account.providerCode !== 'openai') {
    throw new ModelCheckRequestError(400, '当前仅支持检测 OpenAI 账户')
  }
  if (account.status === 'disabled') {
    throw new ModelCheckRequestError(400, '账户已停用，无法执行模型检测')
  }
  const unavailableMessage = accountTestUnavailableMessage(account)
  if (unavailableMessage) {
    throw new ModelCheckRequestError(400, unavailableMessage)
  }
  if (!account.boundGroupId) {
    throw new ModelCheckRequestError(400, '账户未绑定可用分组，无法按真实链路执行模型检测')
  }
  const systemAccountId = effectiveAccountTargetSystemAccountId(account, access)
  const candidate = findOpenAIAccountForGroup(account.boundGroupId, account.id, systemAccountId, { ignoreAvailability: true })
  if (!candidate) {
    throw new ModelCheckRequestError(400, '账户不在当前分组或凭据不可用，无法执行模型检测')
  }
  return {
    targetType: 'account',
    targetId: account.id,
    targetName: account.name,
    targetOwnerSystemAccountId: account.ownerSystemAccountId ?? account.systemAccountId ?? candidate.accountOwnerSystemAccountId,
    identity: {
      systemAccountId,
      groupId: account.boundGroupId
    },
    candidateAccounts: [candidate],
    accountId: account.id,
    groupId: account.boundGroupId
  }
}

function effectiveAccountTargetSystemAccountId(account: AccountSummary, access?: AccessScope): string {
  if (access?.role === 'admin') {
    return access.systemAccountFilterId?.trim()
      || account.systemAccountId
      || account.ownerSystemAccountId
      || access.systemAccountId
  }
  if (account.accessType === 'authorized') {
    return access?.systemAccountId ?? account.bindingSystemAccountId ?? 'sys_admin'
  }
  return access?.systemAccountId ?? account.systemAccountId ?? account.ownerSystemAccountId ?? 'sys_admin'
}

function resolveTrustedComparisonTarget(accountId: string, access?: AccessScope): ModelCheckTarget {
  try {
    return resolveAccountTarget(accountId, access)
  } catch (error) {
    if (error instanceof ModelCheckRequestError) {
      const message = error.message
        .replace(/^账户/, '可信对比账户')
        .replace(/^当前仅支持检测 OpenAI 账户$/, '可信对比账户必须是 OpenAI 账户')
      throw new ModelCheckRequestError(error.statusCode, message)
    }
    throw error
  }
}

async function executeProbeSuite(target: ProbeTarget, model: 'gpt-5.5' | 'gpt-5.4', prefix: 'target' | 'trusted_comparison', signal?: AbortSignal, progress?: ModelCheckProgressReporter): Promise<ProbeSuiteResult> {
  const items: ModelCheckItemCreateInput[] = []
  const catalog = await runGatewayProbe(target, {
    method: 'GET',
    path: modelsPath,
    itemKey: `${prefix}.model_catalog`
  }, signal, progress)
  pushProbeItem(items, evaluateModelCatalogProbe(catalog, model, prefix), progress)

  const basic = await runGatewayProbe(target, {
    method: 'POST',
    path: responsesPath,
    itemKey: `${prefix}.responses_basic`,
    body: createResponsesPayload(model, 'Reply with exactly: OK-MODEL-CHECK', { maxOutputTokens: 16, stream: false })
  }, signal, progress)
  pushProbeItem(items, evaluateBasicResponsesProbe(basic, model, prefix), progress)
  if (!basic.success) {
    pushProbeItem(items, evaluateUsageShapeProbe([basic], prefix), progress)
    return { items, basic }
  }

  const stream = await runGatewayProbe(target, {
    method: 'POST',
    path: responsesPath,
    itemKey: `${prefix}.responses_stream`,
    body: createResponsesPayload(model, 'Reply with exactly: STREAM-OK', { maxOutputTokens: 16, stream: true })
  }, signal, progress)
  pushProbeItem(items, evaluateStreamProbe(stream, model, prefix), progress)

  const structured = await runGatewayProbe(target, {
    method: 'POST',
    path: responsesPath,
    itemKey: `${prefix}.structured_output`,
    body: createStructuredOutputPayload(model)
  }, signal, progress)
  pushProbeItem(items, evaluateStructuredOutputProbe(structured, model, prefix), progress)

  const tool = await runGatewayProbe(target, {
    method: 'POST',
    path: responsesPath,
    itemKey: `${prefix}.tool_calling`,
    body: createToolCallingPayload(model)
  }, signal, progress)
  pushProbeItem(items, evaluateToolCallingProbe(tool, model, prefix), progress)

  pushProbeItem(items, evaluateUsageShapeProbe([basic, structured, stream], prefix), progress)

  const behaviorObservations: BehaviorProbeObservation[] = []
  for (const definition of behaviorProbeDefinitions) {
    const result = await runGatewayProbe(target, {
      method: 'POST',
      path: responsesPath,
      itemKey: `${prefix}.behavior.${definition.key}`,
      body: createResponsesPayload(model, definition.prompt, { maxOutputTokens: definition.maxOutputTokens, stream: false })
    }, signal, progress)
    behaviorObservations.push({ definition, result })
  }
  const behaviorItem = evaluateBehaviorProbeSet(behaviorObservations, model, prefix)
  pushProbeItem(items, behaviorItem, progress)

  const longContext = await runGatewayProbe(target, {
    method: 'POST',
    path: responsesPath,
    itemKey: `${prefix}.long_context`,
    body: createLongContextPayload(model)
  }, signal, progress)
  pushProbeItem(items, evaluateLongContextProbe(longContext, model, prefix), progress)

  const stabilityResults: GatewayProbeResult[] = []
  for (let index = 1; index <= 3; index += 1) {
    stabilityResults.push(await runGatewayProbe(target, {
      method: 'POST',
      path: responsesPath,
      itemKey: `${prefix}.stability_${index}`,
      body: createResponsesPayload(model, 'Reply with exactly one uppercase word: VECTOR', { maxOutputTokens: 16, stream: false })
    }, signal, progress))
  }
  pushProbeItem(items, evaluateStabilityProbe(stabilityResults, model, prefix), progress)

  return { items, basic, behavior: behaviorObservations[0]?.result, longContext }
}

async function executeCrossModelComparison(target: ProbeTarget, targetSuite: ProbeSuiteResult, model: 'gpt-5.5' | 'gpt-5.4', signal?: AbortSignal, progress?: ModelCheckProgressReporter): Promise<ModelCheckItemCreateInput> {
  const pairedModel = model === 'gpt-5.5' ? 'gpt-5.4' : 'gpt-5.5'
  const pairedBasic = await runGatewayProbe(target, {
    method: 'POST',
    path: responsesPath,
    itemKey: 'target.cross_model',
    body: createResponsesPayload(pairedModel, 'Reply with exactly: CROSS-MODEL-OK', { maxOutputTokens: 16, stream: false })
  }, signal, progress)
  const item = evaluateCrossModelComparisonProbe(targetSuite.basic, pairedBasic, model, pairedModel)
  emitModelCheckItemProgress(progress, item)
  return item
}

async function executeDistributionSimilarityComparison(
  target: ProbeTarget,
  comparison: ProbeTarget,
  model: 'gpt-5.5' | 'gpt-5.4',
  signal?: AbortSignal,
  progress?: ModelCheckProgressReporter
): Promise<ModelCheckItemCreateInput> {
  const pairs: DistributionProbePair[] = []
  for (const definition of distributionProbeDefinitions) {
    for (let sampleIndex = 1; sampleIndex <= distributionSampleCount; sampleIndex += 1) {
      const body = createDistributionProbePayload(model, definition)
      const targetResult = await runGatewayProbe(target, {
        method: 'POST',
        path: responsesPath,
        itemKey: `target.distribution.${definition.key}.${sampleIndex}`,
        body
      }, signal, progress)
      const comparisonResult = await runGatewayProbe(comparison, {
        method: 'POST',
        path: responsesPath,
        itemKey: `trusted_comparison.distribution.${definition.key}.${sampleIndex}`,
        body
      }, signal, progress)
      pairs.push({ definition, sampleIndex, target: targetResult, comparison: comparisonResult })
    }
  }
  const item = evaluateDistributionSimilarityProbe(pairs, model)
  emitModelCheckItemProgress(progress, item)
  return item
}

async function runGatewayProbe(target: ProbeTarget, probe: GatewayProbeInput, signal?: AbortSignal, progress?: ModelCheckProgressReporter): Promise<GatewayProbeResult> {
  const startedAt = Date.now()
  const attempts: GatewayProbeResult[] = []
  for (let attempt = 1; attempt <= probeMaxAttempts; attempt += 1) {
    const result = await runGatewayProbeAttempt(target, probe, signal, progress, attempt)
    attempts.push(result)
    if (result.success || !isRetryableProbeFailure(result) || attempt >= probeMaxAttempts) {
      return attachProbeRetryEvidence(result, attempts, Date.now() - startedAt)
    }
    await waitForProbeRetryDelay(probeRetryDelayMs * attempt, signal)
  }
  return attachProbeRetryEvidence(attempts[attempts.length - 1] ?? emptyProbeResult(), attempts, Date.now() - startedAt)
}

async function runGatewayProbeAttempt(target: ProbeTarget, probe: GatewayProbeInput, signal: AbortSignal | undefined, progress: ModelCheckProgressReporter | undefined, attempt: number): Promise<GatewayProbeResult> {
  throwIfAborted(signal)
  const attemptMessage = attempt > 1 ? `（第 ${attempt}/${probeMaxAttempts} 次重试）` : ''
  emitModelCheckProgress(progress, {
    type: 'probe_started',
    message: `开始执行探针 ${probe.itemKey}${attemptMessage}`,
    itemKey: probe.itemKey,
    method: probe.method,
    path: probe.path
  })
  const startedAt = Date.now()
  const traceId = createTraceId()
  const request = createMemoryGatewayRequest({
    method: probe.method,
    path: probe.path,
    body: probe.body,
    signal
  })
  const response = new MemoryGatewayResponse(startedAt)
  const context: RequestContext = {
    traceId,
    startedAt,
    method: request.method,
    path: request.path,
    originalUrl: request.originalUrl,
    clientIp: request.ip,
    systemAccountId: target.identity.systemAccountId,
    apiKeyId: target.identity.apiKeyId,
    groupId: target.identity.groupId,
    logger: logger.child({ source: 'model_check', traceId, itemKey: probe.itemKey })
  }
  try {
    await withRequestContext(context, () => handleOpenAIGatewayRequest(request, response.asResponse(), {
      identity: target.identity,
      candidateAccounts: target.candidateAccounts,
      disableSessionAffinity: true,
      exposeUpstreamDiagnostics: false
    }))
  } catch (error) {
    if (signal?.aborted) throw error
    const result: GatewayProbeResult = {
      traceId,
      statusCode: probeErrorStatusCode(error),
      success: false,
      durationMs: Date.now() - startedAt,
      bodyText: probeErrorMessage(error),
      bodyTruncated: false,
      headers: {},
      errorMessage: probeErrorMessage(error)
    }
    emitModelCheckProgress(progress, {
      type: 'probe_completed',
      message: `${result.errorMessage ?? `探针执行异常，HTTP ${result.statusCode}`}${attemptMessage}`,
      itemKey: probe.itemKey,
      traceId: result.traceId,
      statusCode: result.statusCode,
      success: result.success,
      durationMs: result.durationMs
    })
    return result
  }
  const bodyText = response.bodyText()
  const json = parseJsonRecord(bodyText)
  const outputText = extractOpenAIResponseOutputText(bodyText)
  const result = {
    traceId,
    statusCode: response.statusCode,
    success: response.statusCode >= 200 && response.statusCode < 300 && !parseOpenAIStreamFailureMessage(bodyText),
    durationMs: Date.now() - startedAt,
    firstTokenMs: response.firstTokenMs(),
    bodyText,
    bodyTruncated: response.bodyTruncated(),
    headers: response.headersObject(),
    json,
    outputText,
    model: textValue(json?.model),
    usage: recordValue(json?.usage) ?? usageFromSse(bodyText),
    errorMessage: parseUpstreamMessage(bodyText)
  }
  emitModelCheckProgress(progress, {
    type: 'probe_completed',
    message: result.success ? `探针响应完成${attemptMessage}` : `${result.errorMessage ?? `探针响应异常，HTTP ${result.statusCode}`}${attemptMessage}`,
    itemKey: probe.itemKey,
    traceId: result.traceId,
    statusCode: result.statusCode,
    success: result.success,
    durationMs: result.durationMs,
    responseModel: result.model,
    outputPreview: bounded(result.outputText)
  })
  return result
}

function isRetryableProbeFailure(result: GatewayProbeResult): boolean {
  return !result.success
}

function probeErrorStatusCode(error: unknown): number {
  const statusCode = (error as { statusCode?: unknown })?.statusCode
  return typeof statusCode === 'number' && Number.isFinite(statusCode) ? Math.trunc(statusCode) : 0
}

function probeErrorMessage(error: unknown): string {
  return error instanceof Error ? error.message : '网关探针执行异常'
}

function attachProbeRetryEvidence(result: GatewayProbeResult, attempts: GatewayProbeResult[], durationMs: number): GatewayProbeResult {
  if (attempts.length <= 1) return result
  const retryableFailureCount = attempts.filter((attempt) => isRetryableProbeFailure(attempt)).length
  return {
    ...result,
    durationMs,
    attemptCount: attempts.length,
    retryAttemptCount: attempts.length - 1,
    retryableFailureCount,
    attemptTraceIds: attempts.map((attempt) => attempt.traceId),
    attemptStatusCodes: attempts.map((attempt) => attempt.statusCode),
    attemptMessages: attempts.map((attempt) => attempt.success ? 'success' : attempt.errorMessage ?? `HTTP ${attempt.statusCode}`)
  }
}

function retryEvidence(result: GatewayProbeResult): Record<string, unknown> {
  if (!result.attemptCount || result.attemptCount <= 1) return {}
  return {
    attemptCount: result.attemptCount,
    retryAttemptCount: result.retryAttemptCount,
    retryMaxAttempts: probeMaxAttempts,
    retryableFailureCount: result.retryableFailureCount,
    attemptTraceIds: result.attemptTraceIds,
    attemptStatusCodes: result.attemptStatusCodes,
    attemptMessages: result.attemptMessages
  }
}

async function waitForProbeRetryDelay(delayMs: number, signal?: AbortSignal): Promise<void> {
  throwIfAborted(signal)
  await new Promise<void>((resolvePromise, rejectPromise) => {
    let timer: ReturnType<typeof setTimeout> | undefined
    const cleanup = () => {
      if (timer) clearTimeout(timer)
      signal?.removeEventListener('abort', onAbort)
    }
    const onAbort = () => {
      cleanup()
      rejectPromise(new Error('模型检测已取消'))
    }
    timer = setTimeout(() => {
      cleanup()
      resolvePromise()
    }, delayMs)
    if (signal) {
      signal.addEventListener('abort', onAbort, { once: true })
      if (signal.aborted) onAbort()
    }
  })
  throwIfAborted(signal)
}

function pushProbeItem(items: ModelCheckItemCreateInput[], item: ModelCheckItemCreateInput, progress?: ModelCheckProgressReporter): void {
  items.push(item)
  emitModelCheckItemProgress(progress, item)
}

function emitModelCheckItemProgress(progress: ModelCheckProgressReporter | undefined, item: ModelCheckItemCreateInput): void {
  emitModelCheckProgress(progress, {
    type: 'item_completed',
    message: modelCheckItemMessage(item),
    itemKey: item.itemKey,
    itemType: item.itemType,
    status: item.status,
    score: item.score,
    maxScore: item.maxScore,
    traceId: item.traceId,
    durationMs: item.durationMs
  })
}

function emitModelCheckProgress(progress: ModelCheckProgressReporter | undefined, event: ModelCheckProgressEvent): void {
  if (!progress) return
  try {
    progress(event)
  } catch (error) {
    logger.warn({ event: 'model_check_progress_emit_failed', err: error }, '模型检测进度事件发送失败')
  }
}

function modelCheckItemMessage(item: ModelCheckItemCreateInput): string {
  const evidenceMessage = textValue(recordValue(item.evidenceSummary)?.message)
  return evidenceMessage || item.errorMessage || '检测项完成'
}

function createResponsesPayload(model: string, prompt: string, options: { maxOutputTokens: number; stream: boolean }): Record<string, unknown> {
  return {
    model,
    input: [
      {
        role: 'user',
        content: [
          {
            type: 'input_text',
            text: prompt
          }
        ]
      }
    ],
    instructions: 'You are a model capability checker. Follow the requested output exactly.',
    max_output_tokens: options.maxOutputTokens,
    stream: options.stream,
    store: false,
    temperature: 0
  }
}

function createDistributionProbePayload(model: string, definition: DistributionProbeDefinition): Record<string, unknown> {
  return {
    ...createResponsesPayload(model, definition.prompt, { maxOutputTokens: definition.maxOutputTokens, stream: false }),
    temperature: 0.2
  }
}

function createStructuredOutputPayload(model: string): Record<string, unknown> {
  return {
    ...createResponsesPayload(model, 'Return {"status":"ok","value":7} as JSON.', { maxOutputTokens: 64, stream: false }),
    text: {
      format: {
        type: 'json_schema',
        name: 'model_check_structured_output',
        schema: {
          type: 'object',
          additionalProperties: false,
          properties: {
            status: { type: 'string', enum: ['ok'] },
            value: { type: 'integer' }
          },
          required: ['status', 'value']
        },
        strict: true
      }
    }
  }
}

function createToolCallingPayload(model: string): Record<string, unknown> {
  return {
    ...createResponsesPayload(model, 'Call the provided function with code "ok" and count 1.', { maxOutputTokens: 64, stream: false }),
    tools: [
      {
        type: 'function',
        name: 'record_model_check',
        description: 'Record a model check marker.',
        parameters: {
          type: 'object',
          additionalProperties: false,
          properties: {
            code: { type: 'string' },
            count: { type: 'integer' }
          },
          required: ['code', 'count']
        }
      }
    ],
    tool_choice: {
      type: 'function',
      name: 'record_model_check'
    }
  }
}

function createLongContextPayload(model: string): Record<string, unknown> {
  const marker = 'NEEDLE-7482-ORCHID'
  const filler = Array.from({ length: 240 }, (_, index) => {
    const ordinal = String(index + 1).padStart(2, '0')
    return `段落 ${ordinal}: 这是一段用于模型检测的普通上下文，包含编号、中文文本和少量干扰词，但不包含最终答案。`
  })
  filler.splice(173, 0, `关键记录: 本次检测的隐藏标记是 ${marker}，只在回答中输出这个标记。`)
  return createResponsesPayload(model, `阅读以下上下文，找到隐藏标记。只输出标记本身，不要解释。\n\n${filler.join('\n')}`, {
    maxOutputTokens: 40,
    stream: false
  })
}

function evaluateModelCatalogProbe(result: GatewayProbeResult, model: string, prefix: string): ModelCheckItemCreateInput {
  const data = Array.isArray(result.json?.data) ? result.json.data : []
  const listed = data.some((item) => recordValue(item)?.id === model)
  return item(`${prefix}.model_catalog`, 'model_catalog', listed ? 'passed' : 'warning', listed ? 5 : 2, 5, result, {
    message: listed ? '本地模型目录包含目标模型' : '本地模型目录未确认目标模型；该项只作为低权重证据',
    listed
  })
}

function evaluateBasicResponsesProbe(result: GatewayProbeResult, model: string, prefix: string): ModelCheckItemCreateInput {
  const modelEvidence = buildModelMatchEvidence(result.model, model)
  const hasOutput = Boolean(result.outputText)
  const score = modelEvidence.modelMismatch
    ? (result.success ? 4 : 0) + (hasOutput ? 2 : 0)
    : (result.success ? 10 : 0) + (modelEvidence.matchedModel ? 5 : 0) + (hasOutput ? 5 : 0)
  const status = modelEvidence.modelMismatch
    ? 'failed'
    : score >= 18 ? 'passed' : score >= 10 ? 'warning' : 'failed'
  return item(`${prefix}.responses_basic`, 'responses_basic', status, score, 20, result, {
    message: describeModelMismatch(modelEvidence) ?? (result.success ? 'Responses 非流式调用可用' : result.errorMessage ?? `Responses 非流式调用失败，HTTP ${result.statusCode}`),
    ...modelEvidence,
    hasOutput
  })
}

function evaluateStreamProbe(result: GatewayProbeResult, model: string, prefix: string): ModelCheckItemCreateInput {
  const modelEvidence = buildModelMatchEvidence(result.model ?? modelFromSse(result.bodyText), model)
  const hasOutput = Boolean(result.outputText)
  const score = modelEvidence.modelMismatch
    ? (result.success ? 4 : 0) + (hasOutput ? 1 : 0)
    : (result.success ? 8 : 0) + (modelEvidence.matchedModel ? 3 : 0) + (hasOutput ? 4 : 0)
  const status = modelEvidence.modelMismatch
    ? 'failed'
    : score >= 13 ? 'passed' : score >= 8 ? 'warning' : 'failed'
  return item(`${prefix}.responses_stream`, 'responses_stream', status, score, 15, result, {
    message: describeModelMismatch(modelEvidence) ?? (result.success ? 'Responses 流式调用可用' : result.errorMessage ?? `Responses 流式调用失败，HTTP ${result.statusCode}`),
    ...modelEvidence,
    hasOutput,
    firstTokenMs: result.firstTokenMs
  })
}

function evaluateStructuredOutputProbe(result: GatewayProbeResult, model: string, prefix: string): ModelCheckItemCreateInput {
  const outputJson = parseFirstJsonObject(result.outputText)
  const valid = outputJson?.status === 'ok' && typeof outputJson.value === 'number'
  const modelEvidence = buildModelMatchEvidence(result.model, model)
  const score = modelEvidence.modelMismatch
    ? (result.success ? 4 : 0) + (valid ? 1 : 0)
    : (result.success ? 8 : 0) + (modelEvidence.matchedModel ? 3 : 0) + (valid ? 4 : 0)
  const status = modelEvidence.modelMismatch
    ? 'failed'
    : score >= 13 ? 'passed' : score >= 8 ? 'warning' : 'failed'
  return item(`${prefix}.structured_output`, 'structured_output', status, score, 15, result, {
    message: describeModelMismatch(modelEvidence) ?? (valid ? '结构化输出符合预期' : result.success ? '结构化输出未完全符合预期' : result.errorMessage ?? `结构化输出调用失败，HTTP ${result.statusCode}`),
    ...modelEvidence,
    outputJson
  })
}

function evaluateToolCallingProbe(result: GatewayProbeResult, model: string, prefix: string): ModelCheckItemCreateInput {
  const called = hasFunctionCall(result.json, 'record_model_check')
  const modelEvidence = buildModelMatchEvidence(result.model, model)
  const score = modelEvidence.modelMismatch
    ? (result.success ? 4 : 0) + (called ? 1 : 0)
    : (result.success ? 8 : 0) + (modelEvidence.matchedModel ? 3 : 0) + (called ? 4 : 0)
  const status = modelEvidence.modelMismatch
    ? 'failed'
    : score >= 13 ? 'passed' : score >= 8 ? 'warning' : 'failed'
  return item(`${prefix}.tool_calling`, 'tool_calling', status, score, 15, result, {
    message: describeModelMismatch(modelEvidence) ?? (called ? '工具调用结构符合预期' : result.success ? '未观察到预期工具调用结构' : result.errorMessage ?? `工具调用检测失败，HTTP ${result.statusCode}`),
    ...modelEvidence,
    called
  })
}

function evaluateUsageShapeProbe(results: GatewayProbeResult[], prefix: string): ModelCheckItemCreateInput {
  const usage = results.map((result) => result.usage).find(Boolean)
  const valid = Boolean(usage && (numberValue(usage.input_tokens) !== undefined || numberValue(usage.output_tokens) !== undefined || numberValue(usage.total_tokens) !== undefined))
  const base = results.find((result) => result.usage) ?? results[0]
  return item(`${prefix}.usage_shape`, 'usage_shape', valid ? 'passed' : 'warning', valid ? 10 : 4, 10, base, {
    message: valid ? 'usage 字段结构可用' : '未观察到完整 usage 字段；可能由上游兼容层省略',
    usage
  })
}

function evaluateBehaviorProbeSet(observations: BehaviorProbeObservation[], model: string, prefix: string): ModelCheckItemCreateInput {
  const summaries = observations.map((observation) => {
    const modelEvidence = buildModelMatchEvidence(observation.result.model, model)
    return {
      key: observation.definition.key,
      traceId: observation.result.traceId,
      success: observation.result.success,
      attemptCount: observation.result.attemptCount ?? 1,
      matchedModel: modelEvidence.matchedModel,
      modelMismatch: modelEvidence.modelMismatch,
      constraintPassed: observation.result.success && behaviorConstraintPassed(observation.definition, observation.result.outputText ?? ''),
      outputPreview: bounded(observation.result.outputText),
      responseModel: modelEvidence.responseModel
    }
  })
  const total = Math.max(1, summaries.length)
  const successRate = ratio(summaries.filter((summary) => summary.success).length, total)
  const modelMatchRate = ratio(summaries.filter((summary) => summary.matchedModel).length, total)
  const constraintRate = ratio(summaries.filter((summary) => summary.constraintPassed).length, total)
  const modelMismatch = summaries.some((summary) => summary.modelMismatch)
  const score = modelMismatch
    ? Math.round(constraintRate * 8)
    : Math.max(0, Math.min(35, Math.round((successRate * 0.25 + modelMatchRate * 0.2 + constraintRate * 0.55) * 35)))
  const status: ModelCheckItemCreateInput['status'] = modelMismatch
    ? 'failed'
    : constraintRate >= 0.85 && successRate >= 0.85 ? 'passed' : constraintRate >= 0.6 && successRate >= 0.6 ? 'warning' : 'failed'
  const result = observations[observations.length - 1]?.result ?? emptyProbeResult()
  return item(`${prefix}.behavior_probe`, 'behavior_probe', status, score, 35, result, {
    message: modelMismatch
      ? '行为指纹探针返回模型与请求模型不一致'
      : status === 'passed'
        ? '多行为指纹探针通过'
        : status === 'warning'
          ? '多行为指纹探针部分通过，建议结合可信对比观察'
          : '多行为指纹探针大面积异常',
    expectedModel: model,
    probeCount: summaries.length,
    successRate: roundMetric(successRate),
    modelMatchRate: roundMetric(modelMatchRate),
    constraintRate: roundMetric(constraintRate),
    modelMismatch,
    promptKeys: summaries.map((summary) => summary.key),
    summaries
  })
}

function evaluateLongContextProbe(result: GatewayProbeResult, model: string, prefix: string): ModelCheckItemCreateInput {
  const marker = 'NEEDLE-7482-ORCHID'
  const found = (result.outputText ?? '').toUpperCase().includes(marker)
  const modelEvidence = buildModelMatchEvidence(result.model, model)
  const score = modelEvidence.modelMismatch
    ? (result.success ? 2 : 0) + (found ? 1 : 0)
    : (result.success ? 7 : 0) + (modelEvidence.matchedModel ? 3 : 0) + (found ? 5 : 0)
  const status = modelEvidence.modelMismatch
    ? 'failed'
    : score >= 13 ? 'passed' : score >= 8 ? 'warning' : 'failed'
  return item(`${prefix}.long_context`, 'long_context', status, score, 15, result, {
    message: describeModelMismatch(modelEvidence) ?? (found ? '长上下文找针探针通过' : result.success ? '长上下文找针探针未命中隐藏标记' : result.errorMessage ?? `长上下文探针失败，HTTP ${result.statusCode}`),
    ...modelEvidence,
    foundNeedle: found,
    outputPreview: bounded(result.outputText)
  })
}

function evaluateStabilityProbe(results: GatewayProbeResult[], model: string, prefix: string): ModelCheckItemCreateInput {
  const observations = results.map((result) => {
    const modelEvidence = buildModelMatchEvidence(result.model, model)
    return {
      traceId: result.traceId,
      ok: result.success && (result.outputText ?? '').toUpperCase().includes('VECTOR'),
      attemptCount: result.attemptCount ?? 1,
      outputPreview: bounded(result.outputText),
      ...modelEvidence
    }
  })
  const okCount = observations.filter((item) => item.ok).length
  const modelOk = observations.some((item) => item.matchedModel)
  const modelMismatch = observations.some((item) => item.modelMismatch)
  const score = modelMismatch
    ? okCount * 2
    : (okCount * 4) + (modelOk ? 3 : 0)
  const status = modelMismatch
    ? 'failed'
    : score >= 14 ? 'passed' : score >= 8 ? 'warning' : 'failed'
  const result = results[results.length - 1] ?? emptyProbeResult()
  return item(`${prefix}.stability`, 'stability', status, score, 15, result, {
    message: modelMismatch ? '三轮稳定性探针返回模型与请求模型不一致' : okCount === results.length ? '三轮稳定性探针通过' : '三轮稳定性探针未完全通过',
    expectedModel: model,
    probeCount: results.length,
    passedCount: okCount,
    matchedModel: modelOk,
    modelMismatch,
    observations
  })
}

function evaluateCrossModelComparisonProbe(targetBasic: GatewayProbeResult | undefined, pairedBasic: GatewayProbeResult, model: string, pairedModel: string): ModelCheckItemCreateInput {
  const targetEvidence = buildModelMatchEvidence(targetBasic?.model, model)
  const pairedEvidence = buildModelMatchEvidence(pairedBasic.model, pairedModel)
  const sameResponseModel = Boolean(
    targetEvidence.responseModel
    && pairedEvidence.responseModel
    && targetEvidence.responseModel === pairedEvidence.responseModel
  )
  const comparable = Boolean(targetBasic?.success && pairedBasic.success)
  const suspiciousSameBackend = comparable && sameResponseModel && model !== pairedModel
  const pairedModelMismatch = pairedEvidence.modelMismatch
  const targetModelMismatch = targetEvidence.modelMismatch
  const crossModelMismatch = pairedModelMismatch || suspiciousSameBackend
  const score = crossModelMismatch
    ? 0
    : comparable && targetEvidence.matchedModel && pairedEvidence.matchedModel ? 10 : comparable ? 5 : 2
  const status = crossModelMismatch ? 'failed' : score >= 9 ? 'passed' : 'warning'
  return {
    itemKey: 'target.cross_model',
    itemType: 'cross_model',
    status,
    score,
    maxScore: 10,
    durationMs: pairedBasic.durationMs,
    traceId: pairedBasic.traceId,
    evidenceSummary: {
      message: crossModelMismatch
        ? '辅助模型对照返回模型与辅助请求不一致，本项扣分；目标模型结论以目标探针为准'
        : comparable && targetEvidence.matchedModel && pairedEvidence.matchedModel
          ? '辅助模型对照通过，目标请求和辅助请求均返回对应模型字段'
          : '辅助模型对照证据不足，建议结合可信对比或多次检测',
      expectedModel: model,
      pairedModel,
      targetTraceId: targetBasic?.traceId,
      pairedTraceId: pairedBasic.traceId,
      targetResponseModel: targetEvidence.responseModel,
      pairedResponseModel: pairedEvidence.responseModel,
      targetMatchedModel: targetEvidence.matchedModel,
      pairedMatchedModel: pairedEvidence.matchedModel,
      targetModelMismatch,
      pairedModelMismatch,
      sameResponseModel,
      suspiciousSameBackend,
      comparable,
      crossModelMismatch,
      modelMismatch: targetModelMismatch,
      targetOutputPreview: bounded(targetBasic?.outputText),
      pairedOutputPreview: bounded(pairedBasic.outputText),
      httpStatus: pairedBasic.statusCode,
      success: pairedBasic.success,
      ...retryEvidence(pairedBasic)
    },
    errorCode: pairedBasic.success ? undefined : `http_${pairedBasic.statusCode}`,
    errorMessage: pairedBasic.success ? undefined : pairedBasic.errorMessage
  }
}

function buildTrustedComparisonItem(target: ProbeSuiteResult, comparison: ProbeSuiteResult): ModelCheckItemCreateInput {
  const targetBehaviorPassed = target.items.some((item) => item.itemType === 'behavior_probe' && item.status === 'passed')
  const comparisonBehaviorPassed = comparison.items.some((item) => item.itemType === 'behavior_probe' && item.status === 'passed')
  const targetOk = Boolean(target.basic?.success && targetBehaviorPassed)
  const comparisonOk = Boolean(comparison.basic?.success && comparisonBehaviorPassed)
  const comparable = targetOk && comparisonOk
  const status = comparable ? 'passed' : comparisonOk ? 'warning' : 'failed'
  return {
    itemKey: 'trusted_comparison.comparison',
    itemType: 'trusted_comparison',
    status,
    score: comparable ? 10 : comparisonOk ? 4 : 0,
    maxScore: 10,
    durationMs: 0,
    traceId: comparison.basic?.traceId,
    evidenceSummary: {
      message: comparable ? '目标链路和可信对比链路均完成核心探针' : '可信对比未形成完整可比结果',
      targetTraceId: target.basic?.traceId,
      comparisonTraceId: comparison.basic?.traceId,
      targetBehaviorPassed,
      comparisonBehaviorPassed,
      targetOutputPreview: bounded(target.behavior?.outputText),
      comparisonOutputPreview: bounded(comparison.behavior?.outputText)
    }
  }
}

function evaluateDistributionSimilarityProbe(pairs: DistributionProbePair[], model: string): ModelCheckItemCreateInput {
  const pairScores = pairs.map((pair) => distributionPairScore(pair))
  const successfulPairCount = pairScores.filter((score) => score.successful).length
  const pairCoverage = ratio(successfulPairCount, pairScores.length)
  const targetConstraintRate = average(pairScores.map((score) => score.targetConstraintPassed ? 1 : 0))
  const comparisonConstraintRate = average(pairScores.map((score) => score.comparisonConstraintPassed ? 1 : 0))
  const averageSimilarity = average(pairScores.map((score) => score.similarity))
  const averageLengthRatio = average(pairScores.map((score) => score.lengthRatio))
  const usageRatios = pairScores.map((score) => score.usageRatio).filter((value): value is number => value !== undefined)
  const averageUsageRatio = average(usageRatios)
  const similarityScore = 0.35 * pairCoverage
    + 0.25 * targetConstraintRate
    + 0.25 * averageSimilarity
    + 0.1 * averageLengthRatio
    + 0.05 * (averageUsageRatio || 0)
  const score = Math.max(0, Math.min(15, Math.round(similarityScore * 15)))
  const comparisonLooksHealthy = comparisonConstraintRate >= 0.7 && pairCoverage >= 0.7
  const targetLooksDivergent = targetConstraintRate < 0.55 || averageSimilarity < 0.25 || averageLengthRatio < 0.35
  const status: ModelCheckItemCreateInput['status'] = comparisonLooksHealthy && targetLooksDivergent
    ? 'failed'
    : score >= 12 ? 'passed' : score >= 8 ? 'warning' : 'failed'
  const durationMs = pairs.reduce((sum, pair) => sum + pair.target.durationMs + pair.comparison.durationMs, 0)
  const promptSummaries = distributionProbeDefinitions.map((definition) => {
    const items = pairScores.filter((item) => item.key === definition.key)
    return {
      key: definition.key,
      samples: items.length,
      averageSimilarity: roundMetric(average(items.map((item) => item.similarity))),
      targetConstraintRate: roundMetric(average(items.map((item) => item.targetConstraintPassed ? 1 : 0))),
      comparisonConstraintRate: roundMetric(average(items.map((item) => item.comparisonConstraintPassed ? 1 : 0))),
      targetPreview: bounded(pairs.find((pair) => pair.definition.key === definition.key)?.target.outputText),
      comparisonPreview: bounded(pairs.find((pair) => pair.definition.key === definition.key)?.comparison.outputText)
    }
  })
  const lastPair = pairs[pairs.length - 1]
  return {
    itemKey: 'trusted_comparison.distribution_similarity',
    itemType: 'distribution_similarity',
    status,
    score,
    maxScore: 15,
    durationMs,
    traceId: lastPair?.comparison.traceId ?? lastPair?.target.traceId,
    evidenceSummary: {
      message: status === 'passed'
        ? '目标链路与可信对比链路的隐藏分布探针相似度正常'
        : status === 'warning'
          ? '目标链路与可信对比链路存在轻微分布差异，建议结合多次检测观察'
          : '目标链路与可信对比链路的隐藏分布探针差异明显，本项扣分',
      expectedModel: model,
      promptCount: distributionProbeDefinitions.length,
      samplesPerPrompt: distributionSampleCount,
      totalPairs: pairScores.length,
      successfulPairCount,
      pairCoverage: roundMetric(pairCoverage),
      targetConstraintRate: roundMetric(targetConstraintRate),
      comparisonConstraintRate: roundMetric(comparisonConstraintRate),
      averageSimilarity: roundMetric(averageSimilarity),
      averageLengthRatio: roundMetric(averageLengthRatio),
      averageUsageRatio: roundMetric(averageUsageRatio),
      promptSummaries,
      traceIds: pairs.slice(0, 12).map((pair) => ({
        key: pair.definition.key,
        sampleIndex: pair.sampleIndex,
        targetTraceId: pair.target.traceId,
        comparisonTraceId: pair.comparison.traceId,
        targetAttemptCount: pair.target.attemptCount ?? 1,
        comparisonAttemptCount: pair.comparison.attemptCount ?? 1
      }))
    }
  }
}

function distributionPairScore(pair: DistributionProbePair): {
  key: string
  successful: boolean
  targetConstraintPassed: boolean
  comparisonConstraintPassed: boolean
  similarity: number
  lengthRatio: number
  usageRatio?: number
} {
  const targetText = pair.target.outputText ?? ''
  const comparisonText = pair.comparison.outputText ?? ''
  const targetTokens = totalTokens(pair.target.usage)
  const comparisonTokens = totalTokens(pair.comparison.usage)
  return {
    key: pair.definition.key,
    successful: pair.target.success && pair.comparison.success,
    targetConstraintPassed: pair.target.success && distributionConstraintPassed(pair.definition, targetText),
    comparisonConstraintPassed: pair.comparison.success && distributionConstraintPassed(pair.definition, comparisonText),
    similarity: pair.target.success && pair.comparison.success ? textSimilarity(targetText, comparisonText) : 0,
    lengthRatio: boundedRatio(targetText.length, comparisonText.length),
    usageRatio: targetTokens !== undefined && comparisonTokens !== undefined ? boundedRatio(targetTokens, comparisonTokens) : undefined
  }
}

function distributionConstraintPassed(definition: DistributionProbeDefinition, text: string): boolean {
  const normalized = text.trim()
  if (!normalized) return false
  if (definition.key === 'style_compact') {
    return normalized.includes('召回') && normalized.includes('相关') && normalized.length >= 12 && normalized.length <= 48
  }
  if (definition.key === 'json_reasoning') {
    const json = parseFirstJsonObject(normalized)
    return json?.tag === 'SIGMA' && numberValue(json.result) === 83
  }
  if (definition.key === 'code_judgement') {
    return normalized.toUpperCase().includes('ALPHA') && normalized.includes('4-7')
  }
  if (definition.key === 'refusal_boundary') {
    return normalized.toUpperCase().includes('DELTA') && /(不能|无法|不提供|拒绝|不可以)/.test(normalized)
  }
  if (definition.key === 'sequence_transform') {
    return normalized.toUpperCase().includes('THETA') && normalized.includes('4|7|9')
  }
  if (definition.key === 'table_extract') {
    return normalized.toUpperCase().includes('IOTA') && normalized.includes('17') && normalized.includes('23')
  }
  return normalized.length > 0
}

function behaviorConstraintPassed(definition: BehaviorProbeDefinition, text: string): boolean {
  const normalized = text.trim()
  const upper = normalized.toUpperCase()
  if (!normalized) return false
  if (definition.key === 'exact_uppercase') {
    return upper === 'QUARTZ' || upper.includes('QUARTZ')
  }
  if (definition.key === 'json_arithmetic') {
    const json = parseFirstJsonObject(normalized)
    return json?.code === 'BETA' && numberValue(json.sum) === 83
  }
  if (definition.key === 'code_transform') {
    return upper.includes('GAMMA') && normalized.includes('9-7-2')
  }
  if (definition.key === 'compact_zh_constraint') {
    return normalized.includes('并发') && normalized.includes('限流') && normalized.length >= 16 && normalized.length <= 56
  }
  if (definition.key === 'refusal_boundary') {
    return upper.includes('DELTA') && /(不能|无法|不提供|拒绝|不可以)/.test(normalized)
  }
  if (definition.key === 'instruction_priority') {
    return upper.includes('ZETA') && !upper.includes('OMEGA')
  }
  if (definition.key === 'logic_ordering') {
    return normalized.includes('孙')
  }
  if (definition.key === 'three_line_format') {
    const lines = normalized.split(/\r?\n/).map((line) => line.trim()).filter(Boolean)
    return lines.length === 3 && lines[0]?.toUpperCase() === 'ALPHA' && lines[1]?.toUpperCase() === 'BETA' && lines[2]?.toUpperCase() === 'GAMMA'
  }
  return normalized.length > 0
}

function emptyProbeResult(): GatewayProbeResult {
  return {
    traceId: createTraceId(),
    statusCode: 0,
    success: false,
    durationMs: 0,
    bodyText: '',
    bodyTruncated: false,
    headers: {}
  }
}

function item(
  itemKey: string,
  itemType: string,
  status: ModelCheckItemCreateInput['status'],
  score: number,
  maxScore: number,
  result: GatewayProbeResult,
  evidence: Record<string, unknown>
): ModelCheckItemCreateInput {
  const evidenceResponseModel = textValue(evidence.responseModel)
  return {
    itemKey,
    itemType,
    status,
    score,
    maxScore,
    durationMs: result.durationMs,
    traceId: result.traceId,
    evidenceSummary: {
      ...evidence,
      httpStatus: result.statusCode,
      success: result.success,
      responseModel: result.model ?? evidenceResponseModel,
      firstTokenMs: result.firstTokenMs,
      responseTruncated: result.bodyTruncated,
      ...retryEvidence(result)
    },
    errorCode: result.success ? undefined : `http_${result.statusCode}`,
    errorMessage: result.success ? undefined : result.errorMessage
  }
}

function summarizeChecks(checks: ModelCheckItemSummary[], options: { trustedComparison: boolean }): {
  level: 'high_confidence' | 'likely' | 'uncertain' | 'suspicious' | 'unavailable'
  score: number
  maxScore: number
  message: string
} {
  const maxScore = checks.reduce((sum, item) => sum + item.maxScore, 0)
  const rawScore = checks.reduce((sum, item) => sum + item.score, 0)
  const score = maxScore > 0 ? Math.round((rawScore / maxScore) * 100) : 0
  const failedCount = checks.filter((item) => item.status === 'failed').length
  const modelMismatchCount = checks.filter(hasModelMismatchEvidence).length
  const targetBasic = checks.find((item) => item.itemKey === 'target.responses_basic')
  const behaviorPassed = checks.some((item) => item.itemType === 'behavior_probe' && item.status === 'passed')
  const longContextPassed = checks.some((item) => item.itemType === 'long_context' && item.status === 'passed')
  const stabilityPassed = checks.some((item) => item.itemType === 'stability' && item.status === 'passed')
  const crossModelPassed = checks.some((item) => item.itemType === 'cross_model' && item.status === 'passed')
  const trustedComparisonPassed = !options.trustedComparison || checks.some((item) => item.itemType === 'trusted_comparison' && item.status === 'passed')
  if (modelMismatchCount > 0) {
    return { level: 'suspicious', score, maxScore: 100, message: '响应模型字段与请求模型不一致，目标链路疑似被替换或降级' }
  }
  if (targetBasic?.status === 'failed' && recordValue(targetBasic.evidenceSummary)?.success !== true) {
    return { level: 'unavailable', score, maxScore: 100, message: '目标模型链路不可检测或上游不可用' }
  }
  if (score >= 92 && failedCount === 0 && behaviorPassed && longContextPassed && stabilityPassed && trustedComparisonPassed && (options.trustedComparison || crossModelPassed)) {
    return {
      level: 'high_confidence',
      score,
      maxScore: 100,
      message: options.trustedComparison
        ? '目标模型链路高可信，强诊断协议、行为指纹、长上下文、稳定性和可信对比均通过'
        : '目标模型链路高可信，强诊断协议、行为指纹、长上下文、稳定性和辅助模型对照均通过'
    }
  }
  if (score >= 78 && failedCount <= 1) {
    return { level: 'likely', score, maxScore: 100, message: '目标模型链路较可信，仍建议结合多次检测结果观察' }
  }
  if (score >= 50) {
    return { level: 'uncertain', score, maxScore: 100, message: '目标模型链路存在不确定项，建议复查上游账号和代理配置' }
  }
  if (score >= 25) {
    return { level: 'suspicious', score, maxScore: 100, message: '目标模型链路疑似不符，多个关键探针未通过' }
  }
  return { level: 'unavailable', score, maxScore: 100, message: '目标模型链路不可检测或上游不可用' }
}

function createMemoryGatewayRequest(input: {
  method: 'GET' | 'POST'
  path: string
  body?: Record<string, unknown>
  signal?: AbortSignal
}): Request {
  const rawBody = input.body ? Buffer.from(JSON.stringify(input.body), 'utf8') : Buffer.alloc(0)
  const headers: IncomingHttpHeaders = {
    accept: input.body?.stream === true ? 'application/json, text/event-stream' : 'application/json'
  }
  if (input.body) {
    headers['content-type'] = 'application/json'
    headers['content-length'] = String(rawBody.length)
  }
  return new MemoryGatewayRequest({
    method: input.method,
    originalUrl: input.path,
    path: input.path.split('?')[0] || input.path,
    headers,
    body: input.body,
    rawBody,
    ip: '127.0.0.1',
    signal: input.signal
  }).asRequest()
}

class MemoryGatewayRequest extends EventEmitter {
  constructor(private readonly input: {
    method: string
    originalUrl: string
    path: string
    headers: IncomingHttpHeaders
    body?: Record<string, unknown>
    rawBody: Buffer
    ip: string
    signal?: AbortSignal
  }) {
    super()
    if (this.input.signal?.aborted) {
      queueMicrotask(() => this.emit('aborted'))
    } else {
      this.input.signal?.addEventListener('abort', () => this.emit('aborted'), { once: true })
    }
  }

  get method(): string {
    return this.input.method
  }

  get originalUrl(): string {
    return this.input.originalUrl
  }

  get path(): string {
    return this.input.path
  }

  get headers(): IncomingHttpHeaders {
    return this.input.headers
  }

  get body(): Record<string, unknown> | undefined {
    return this.input.body
  }

  get rawBody(): Buffer {
    return this.input.rawBody
  }

  get ip(): string {
    return this.input.ip
  }

  get socket(): { remoteAddress: string } {
    return { remoteAddress: this.input.ip }
  }

  get aborted(): boolean {
    return this.input.signal?.aborted ?? false
  }

  header(name: string): string | undefined {
    const value = this.input.headers[name.toLowerCase()]
    if (Array.isArray(value)) return value.join(', ')
    return typeof value === 'string' ? value : undefined
  }

  asRequest(): Request {
    return this as unknown as Request
  }
}

function parseJsonRecord(bodyText: string): Record<string, unknown> | undefined {
  if (!bodyText.trim() || bodyText.trimStart().startsWith('event:')) return undefined
  try {
    const parsed = JSON.parse(bodyText) as unknown
    return recordValue(parsed)
  } catch {
    return undefined
  }
}

function extractOpenAIResponseOutputText(bodyText: string): string | undefined {
  const direct = extractTextFromResponsePayload(parseJsonRecord(bodyText))
  if (direct) return direct
  const parts: string[] = []
  for (const event of parseSseEvents(bodyText)) {
    if (event.type === 'response.output_text.delta' || event.type === 'response.refusal.delta') {
      const delta = textValue(event.delta)
      if (delta) parts.push(delta)
    }
    if (event.type === 'response.output_text.done') {
      const text = textValue(event.text)
      if (text) return text
    }
    if (event.type === 'response.completed' || event.type === 'response.done') {
      const text = extractTextFromResponsePayload(recordValue(event.response))
      if (text) return text
    }
  }
  const text = parts.join('').trim()
  return text || undefined
}

function extractTextFromResponsePayload(payload?: Record<string, unknown>): string | undefined {
  const direct = textValue(payload?.output_text)
  if (direct) return direct
  const output = Array.isArray(payload?.output) ? payload.output : []
  const parts: string[] = []
  for (const item of output) {
    const content = recordValue(item)?.content
    if (!Array.isArray(content)) continue
    for (const contentItem of content) {
      const text = textValue(recordValue(contentItem)?.text)
      if (text) parts.push(text)
    }
  }
  const text = parts.join('').trim()
  return text || undefined
}

function parseOpenAIStreamFailureMessage(bodyText: string): string | undefined {
  for (const event of parseSseEvents(bodyText)) {
    const type = textValue(event.type)
    if (type !== 'response.failed' && type !== 'response.incomplete' && type !== 'error') continue
    const error = event.error ?? recordValue(event.response)?.error
    return parseErrorMessage(error) || parseErrorMessage(event) || type
  }
  return undefined
}

function parseSseEvents(bodyText: string): Array<Record<string, unknown>> {
  const events: Array<Record<string, unknown>> = []
  let eventName = ''
  let dataLines: string[] = []
  const flush = () => {
    const data = dataLines.join('\n').trim()
    const type = eventName
    eventName = ''
    dataLines = []
    if (!data || data === '[DONE]') return
    try {
      const payload = JSON.parse(data) as Record<string, unknown>
      if (type && typeof payload.type !== 'string') payload.type = type
      events.push(payload)
    } catch {
    }
  }
  for (const line of bodyText.split(/\r?\n/)) {
    if (!line) {
      flush()
    } else if (line.startsWith('event:')) {
      eventName = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).trimStart())
    }
  }
  flush()
  return events
}

function parseUpstreamMessage(bodyText: string): string | undefined {
  const json = parseJsonRecord(bodyText)
  const error = recordValue(json?.error)
  return textValue(error?.message) || textValue(error?.code) || textValue(json?.message) || parseOpenAIStreamFailureMessage(bodyText)
}

function parseErrorMessage(value: unknown): string | undefined {
  if (typeof value === 'string' && value.trim()) return value.trim()
  const record = recordValue(value)
  return textValue(record?.message) || textValue(record?.code) || textValue(record?.type)
}

function parseFirstJsonObject(text?: string): Record<string, unknown> | undefined {
  if (!text) return undefined
  const trimmed = text.trim()
  try {
    return recordValue(JSON.parse(trimmed))
  } catch {
    const match = trimmed.match(/\{[\s\S]*\}/)
    if (!match) return undefined
    try {
      return recordValue(JSON.parse(match[0]))
    } catch {
      return undefined
    }
  }
}

function hasFunctionCall(payload: Record<string, unknown> | undefined, name: string): boolean {
  const output = Array.isArray(payload?.output) ? payload.output : []
  return output.some((item) => {
    const record = recordValue(item)
    return record?.type === 'function_call' && record.name === name
  })
}

function usageFromSse(bodyText: string): Record<string, unknown> | undefined {
  for (const event of parseSseEvents(bodyText)) {
    const response = recordValue(event.response)
    const usage = recordValue(response?.usage)
    if (usage) return usage
  }
  return undefined
}

function modelFromSse(bodyText: string): string | undefined {
  for (const event of parseSseEvents(bodyText)) {
    const model = textValue(recordValue(event.response)?.model)
    if (model) return model
  }
  return undefined
}

function buildModelMatchEvidence(actual: unknown, expected: string): {
  expectedModel: string
  responseModel?: string
  matchedModel: boolean
  modelMismatch: boolean
} {
  const text = textValue(actual)
  const matchedModel = modelMatches(text, expected)
  return {
    expectedModel: expected,
    responseModel: text,
    matchedModel,
    modelMismatch: Boolean(text && !matchedModel)
  }
}

function describeModelMismatch(evidence: { expectedModel: string; responseModel?: string; modelMismatch: boolean }): string | undefined {
  return evidence.modelMismatch && evidence.responseModel
    ? `上游返回模型 ${evidence.responseModel}，与请求模型 ${evidence.expectedModel} 不一致`
    : undefined
}

function hasModelMismatchEvidence(item: ModelCheckItemSummary): boolean {
  const evidence = recordValue(item.evidenceSummary)
  if (evidence?.modelMismatch !== true) return false
  return item.itemKey.startsWith('target.') && item.itemType !== 'cross_model'
}

function modelMatches(actual: unknown, expected: string): boolean {
  const text = textValue(actual)
  if (!text) return false
  if (text === expected) return true
  if (!text.startsWith(`${expected}-`)) return false
  const suffix = text.slice(expected.length + 1)
  return /^\d{4}-\d{2}-\d{2}(?:$|[._-])/.test(suffix)
}

function normalizeModel(value: unknown): 'gpt-5.5' | 'gpt-5.4' | undefined {
  const text = textValue(value)
  if (!text) return undefined
  return supportedModelSet.has(text) ? text as 'gpt-5.5' | 'gpt-5.4' : undefined
}

function modelCheckTargetTypeValue(value: unknown): ModelCheckTargetType | undefined {
  return value === 'account' ? value : undefined
}

function modelCheckLevelValue(value: unknown): 'high_confidence' | 'likely' | 'uncertain' | 'suspicious' | 'unavailable' | undefined {
  return value === 'high_confidence' || value === 'likely' || value === 'uncertain' || value === 'suspicious' || value === 'unavailable' ? value : undefined
}

function modelCheckStatusValue(value: unknown): ModelCheckRunStatus | undefined {
  return value === 'running' || value === 'completed' || value === 'failed' || value === 'canceled' ? value : undefined
}

function integerValue(value: unknown): number | undefined {
  const raw = Array.isArray(value) ? value[0] : value
  const numeric = typeof raw === 'number' ? raw : typeof raw === 'string' ? Number.parseInt(raw, 10) : NaN
  return Number.isFinite(numeric) ? Math.trunc(numeric) : undefined
}

function textValue(value: unknown): string | undefined {
  const raw = Array.isArray(value) ? value[0] : value
  return typeof raw === 'string' && raw.trim() ? raw.trim() : undefined
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : undefined
}

function numberValue(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function totalTokens(usage: Record<string, unknown> | undefined): number | undefined {
  return numberValue(usage?.total_tokens)
    ?? numberValue(usage?.totalTokens)
    ?? sumDefined([numberValue(usage?.input_tokens), numberValue(usage?.output_tokens)])
}

function sumDefined(values: Array<number | undefined>): number | undefined {
  const numbers = values.filter((value): value is number => value !== undefined)
  return numbers.length ? numbers.reduce((sum, value) => sum + value, 0) : undefined
}

function average(values: number[]): number {
  const numbers = values.filter((value) => Number.isFinite(value))
  return numbers.length ? numbers.reduce((sum, value) => sum + value, 0) / numbers.length : 0
}

function ratio(part: number, total: number): number {
  return total > 0 ? part / total : 0
}

function boundedRatio(left: number, right: number): number {
  if (left <= 0 || right <= 0) return 0
  return Math.min(left, right) / Math.max(left, right)
}

function roundMetric(value: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.round(value * 1000) / 1000
}

function textSimilarity(left: string, right: string): number {
  const normalizedLeft = normalizeComparableText(left)
  const normalizedRight = normalizeComparableText(right)
  if (!normalizedLeft || !normalizedRight) return 0
  if (normalizedLeft === normalizedRight) return 1
  const leftTokens = comparableTokens(normalizedLeft)
  const rightTokens = comparableTokens(normalizedRight)
  if (!leftTokens.size || !rightTokens.size) return 0
  let intersection = 0
  for (const token of leftTokens) {
    if (rightTokens.has(token)) intersection += 1
  }
  const union = leftTokens.size + rightTokens.size - intersection
  const tokenSimilarity = union > 0 ? intersection / union : 0
  const lengthSimilarity = boundedRatio(normalizedLeft.length, normalizedRight.length)
  return (tokenSimilarity * 0.75) + (lengthSimilarity * 0.25)
}

function normalizeComparableText(value: string): string {
  return value
    .toLowerCase()
    .replace(/\s+/g, '')
    .replace(/[，。！？；：,.!?;:"'`~\-—_[\](){}<>]/g, '')
    .trim()
}

function comparableTokens(value: string): Set<string> {
  if (value.length <= 2) return new Set(value ? [value] : [])
  const tokens = new Set<string>()
  for (let index = 0; index < value.length - 1; index += 1) {
    tokens.add(value.slice(index, index + 2))
  }
  return tokens
}

function bounded(value?: string): string | undefined {
  if (!value) return undefined
  const text = value.trim()
  return text.length > 160 ? `${text.slice(0, 160)}...` : text
}

function throwIfAborted(signal?: AbortSignal): void {
  if (signal?.aborted) {
    throw new Error('模型检测已取消')
  }
}
