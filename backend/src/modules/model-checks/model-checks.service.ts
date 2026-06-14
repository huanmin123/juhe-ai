import { EventEmitter } from 'node:events'
import type { IncomingHttpHeaders } from 'node:http'
import type { Request } from 'express'

import {
  isAdminRole,
  type AccountSummary,
  type ModelCheckOptions,
  type ModelCheckRunDetail,
  type ModelCheckRunListResult,
  type ModelCheckRunRequest,
  type ModelCheckRunStatus,
  type ModelCheckTargetType
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
import { isOpenAIProtocolProfile } from '../../domain/provider-protocol.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { currentSystemAccountId } from '../../storage/access-scope.js'
import { handleOpenAIGatewayRequest, type OpenAIGatewayRequestIdentity } from '../gateway/routes.js'
import { MemoryGatewayResponse } from '../accounts/account-test.service.js'
import {
  accountDiagnosticRetryTimeoutMs,
  diagnosticAccountTestGatewaySettingsOverride,
  diagnosticAttemptSignal,
  isDiagnosticTimeoutSignal
} from '../accounts/account-diagnostic-retry-policy.js'
import {
  defaultModel,
  defaultProfile,
  distributionSampleCount,
  modelsPath,
  probeSetVersion,
  responsesPath,
  supportedModels,
  type SupportedModel
} from './model-checks.constants.js'
import {
  bounded,
  extractOpenAIResponseOutputText,
  integerValue,
  modelCheckLevelValue,
  modelCheckStatusValue,
  modelFromSse,
  normalizeModel,
  parseJsonRecord,
  parseOpenAIStreamFailureMessage,
  parseUpstreamMessage,
  recordValue,
  textValue,
  throwIfAborted,
  usageFromSse
} from './model-checks-parsing.js'
import {
  behaviorProbeDefinitions,
  distributionProbeDefinitions
} from './model-checks.probes.js'
import {
  createDistributionProbePayload,
  createLongContextPayload,
  createResponsesPayload,
  createStructuredOutputPayload,
  createToolCallingPayload
} from './model-checks.payloads.js'
import {
  buildTrustedComparisonItem,
  emptyProbeResult,
  evaluateBasicResponsesProbe,
  evaluateBehaviorProbeSet,
  evaluateCrossModelComparisonProbe,
  evaluateDistributionSimilarityProbe,
  evaluateLongContextProbe,
  evaluateModelCatalogProbe,
  evaluateStabilityProbe,
  evaluateStreamProbe,
  evaluateStructuredOutputProbe,
  evaluateToolCallingProbe,
  evaluateUsageShapeProbe,
  summarizeChecks,
  type BehaviorProbeObservation,
  type DistributionProbePair,
  type GatewayProbeResult,
  type ModelCheckProbePrefix,
  type ProbeSuiteResult
} from './model-checks-evaluation.js'

const probeMaxAttempts = accountDiagnosticRetryTimeoutMs.length

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
  providerCode: string
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
    message: '可信对比默认关闭；选择一个你信任的可用 GPT 账户后，会额外消耗该账户额度'
  }
  return {
    supportedModels: supportedModels.map((model) => ({ value: model, label: model })),
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
    providerCode: target.providerCode,
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
  if (!isOpenAIProtocolProfile(account)) {
    throw new ModelCheckRequestError(400, '当前仅支持检测 OpenAI v1 协议账户')
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
    providerCode: account.providerCode,
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
  if (isAdminRole(access?.role)) {
    const systemAccountId = access.systemAccountFilterId?.trim()
      || account.systemAccountId
      || account.ownerSystemAccountId
    if (!systemAccountId) {
      throw new ModelCheckRequestError(400, '账户归属数据异常，无法执行模型检测')
    }
    return systemAccountId
  }
  if (account.accessType === 'authorized') {
    const systemAccountId = access?.systemAccountId ?? account.bindingSystemAccountId
    if (!systemAccountId) {
      throw new ModelCheckRequestError(400, '授权账户归属数据异常，无法执行模型检测')
    }
    return systemAccountId
  }
  const systemAccountId = access?.systemAccountId ?? account.systemAccountId ?? account.ownerSystemAccountId
  if (!systemAccountId) {
    throw new ModelCheckRequestError(400, '账户归属数据异常，无法执行模型检测')
  }
  return systemAccountId
}

function resolveTrustedComparisonTarget(accountId: string, access?: AccessScope): ModelCheckTarget {
  try {
    return resolveAccountTarget(accountId, access)
  } catch (error) {
    if (error instanceof ModelCheckRequestError) {
      const message = error.message
        .replace(/^账户/, '可信对比账户')
        .replace(/^当前仅支持检测 OpenAI v1 协议账户$/, '可信对比账户必须是 OpenAI v1 协议账户')
      throw new ModelCheckRequestError(error.statusCode, message)
    }
    throw error
  }
}

async function executeProbeSuite(target: ProbeTarget, model: SupportedModel, prefix: ModelCheckProbePrefix, signal?: AbortSignal, progress?: ModelCheckProgressReporter): Promise<ProbeSuiteResult> {
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

async function executeCrossModelComparison(target: ProbeTarget, targetSuite: ProbeSuiteResult, model: SupportedModel, signal?: AbortSignal, progress?: ModelCheckProgressReporter): Promise<ModelCheckItemCreateInput> {
  const pairedModel: SupportedModel = model === 'gpt-5.5' ? 'gpt-5.4' : 'gpt-5.5'
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
  model: SupportedModel,
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
    const timeoutMs = accountDiagnosticRetryTimeoutMs[attempt - 1] ?? accountDiagnosticRetryTimeoutMs[accountDiagnosticRetryTimeoutMs.length - 1]
    const result = await runGatewayProbeAttempt(target, probe, signal, progress, attempt, timeoutMs)
    attempts.push(result)
    if (result.success || !isRetryableProbeFailure(result) || attempt >= probeMaxAttempts) {
      return attachProbeRetryEvidence(result, attempts, Date.now() - startedAt)
    }
  }
  return attachProbeRetryEvidence(attempts[attempts.length - 1] ?? emptyProbeResult(), attempts, Date.now() - startedAt)
}

async function runGatewayProbeAttempt(target: ProbeTarget, probe: GatewayProbeInput, signal: AbortSignal | undefined, progress: ModelCheckProgressReporter | undefined, attempt: number, timeoutMs: number): Promise<GatewayProbeResult> {
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
  const attemptSignal = diagnosticAttemptSignal(signal, timeoutMs)
  const request = createMemoryGatewayRequest({
    method: probe.method,
    path: probe.path,
    body: probe.body,
    signal: attemptSignal
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
      exposeUpstreamDiagnostics: false,
      disableAccountStateMutation: true,
      settingsOverride: diagnosticAccountTestGatewaySettingsOverride(undefined, timeoutMs)
    }))
  } catch (error) {
    if (signal?.aborted) throw error
    const responseBodyText = response.bodyText()
    const hasGatewayResponse = response.statusCode !== 200 || Boolean(responseBodyText)
    const responseErrorMessage = hasGatewayResponse ? parseUpstreamMessage(responseBodyText) : undefined
    const statusCode = attemptSignal.aborted
      ? 0
      : probeErrorStatusCode(error) || (hasGatewayResponse ? response.statusCode : 0)
    const message = attemptSignal.aborted
      ? probeAbortMessage(attemptSignal)
      : responseErrorMessage ?? probeErrorMessage(error)
    const result: GatewayProbeResult = {
      traceId,
      statusCode,
      success: false,
      durationMs: Date.now() - startedAt,
      bodyText: responseBodyText || message,
      bodyTruncated: hasGatewayResponse ? response.bodyTruncated() : false,
      headers: hasGatewayResponse ? response.headersObject() : {},
      errorMessage: message
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
  throwIfAborted(signal)
  if (attemptSignal.aborted) {
    const message = probeAbortMessage(attemptSignal)
    const result: GatewayProbeResult = {
      traceId,
      statusCode: 0,
      success: false,
      durationMs: Date.now() - startedAt,
      bodyText: message,
      bodyTruncated: false,
      headers: {},
      errorMessage: message
    }
    emitModelCheckProgress(progress, {
      type: 'probe_completed',
      message: `${message}${attemptMessage}`,
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
    model: textValue(json?.model) ?? modelFromSse(bodyText),
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

function probeAbortMessage(signal: AbortSignal): string {
  return isDiagnosticTimeoutSignal(signal) ? '模型检测探针超时' : '模型检测已取消'
}

function attachProbeRetryEvidence(result: GatewayProbeResult, attempts: GatewayProbeResult[], durationMs: number): GatewayProbeResult {
  if (attempts.length <= 1) return result
  const retryableFailureCount = attempts.filter((attempt) => isRetryableProbeFailure(attempt)).length
  return {
    ...result,
    durationMs,
    attemptCount: attempts.length,
    retryAttemptCount: attempts.length - 1,
    retryMaxAttempts: probeMaxAttempts,
    retryableFailureCount,
    attemptTraceIds: attempts.map((attempt) => attempt.traceId),
    attemptStatusCodes: attempts.map((attempt) => attempt.statusCode),
    attemptMessages: attempts.map((attempt) => attempt.success ? 'success' : attempt.errorMessage ?? `HTTP ${attempt.statusCode}`)
  }
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
