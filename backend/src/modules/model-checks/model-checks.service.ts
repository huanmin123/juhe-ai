import {
  ANTHROPIC_MESSAGES_FAMILY,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_RESPONSES_FAMILY
} from '../../domain/provider-protocol.js'
import {
  isAdminRole,
  type AccountModelMappingSourceEndpointFamily,
  type AccountSummary,
  type ModelCheckOptions,
  type ModelCheckRunDetail,
  type ModelCheckRunListResult,
  type ModelCheckRunRequest,
  type ModelCheckRunStatus,
  type ModelCheckTargetType
} from '../../domain/types.js'
import { logger } from '../../shared/logger.js'
import { createTraceId } from '../../shared/request-context.js'
import {
  accountTestUnavailableMessage,
  findAccountForTestAsync,
  listOpenAIAccountsForGroupResultAsync,
  getModelCheckRunDetailAsync,
  listModelCheckRunsAsync,
  type ModelCheckItemCreateInput,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { currentSystemAccountId } from '../../storage/access-scope.js'
import type { OpenAIGatewayRequestIdentity } from '../gateway/routes.js'
import { resolveOpenAIAccountModelMapping } from '../gateway/protocols/openai-v1/model-mapping.js'
import {
  defaultModel,
  defaultProfile,
  distributionSampleCount,
  probeSetVersion,
  supportedModels,
  type SupportedModel
} from './model-checks.constants.js'
import {
  integerValue,
  modelCheckLevelValue,
  modelCheckStatusValue,
  normalizeModel,
  recordValue,
  textValue,
  throwIfAborted
} from './model-checks-parsing.js'
import {
  behaviorProbeDefinitions,
  distributionProbeDefinitions,
  longContextProbeDefinitionsForModel
} from './model-checks.probes.js'
import {
  createModelCheckDistributionProbeRequest,
  createModelCheckLongContextRequest,
  createModelCheckProbeRequest,
  createModelCheckStructuredOutputRequest,
  createModelCheckToolCallingRequest,
  type ModelCheckProbeRequest
} from './model-checks.payloads.js'
import {
  evaluateBasicProtocolProbe,
  buildTrustedComparisonItem,
  evaluateBasicResponsesProbe,
  evaluateBehaviorProbeSet,
  evaluateCrossModelComparisonProbe,
  evaluateDistributionSimilarityProbe,
  evaluateLongContextProbeSet,
  evaluateProtocolStreamProbe,
  evaluateStabilityProbe,
  evaluateStreamProbe,
  evaluateStructuredOutputProbe,
  evaluateToolCallingProbe,
  evaluateUsageShapeProbe,
  summarizeChecks,
  summarizeEvidenceCompleteness,
  type BehaviorProbeObservation,
  type DistributionProbePair,
  type GatewayProbeResult,
  type LongContextProbeObservation,
  type ModelCheckProbePrefix,
  type ProbeSuiteResult
} from './model-checks-evaluation.js'
import { buildModelCheckTrustReport } from './model-checks-trust-report.js'
import {
  runGatewayProbe,
  type ModelCheckGatewayProbeTarget
} from './model-checks-gateway-probe.js'
import {
  isModelCheckSupportedProtocolProfile,
  modelCheckSupportedProtocolLabel
} from './model-checks.provider-capabilities.js'
import {
  findModelCheckProfileForAccount,
  findModelCheckProfileForAccountModel,
  pairedModelForProfile,
  sameModelCheckComparisonProfile,
  type ModelCheckProtocolProfile
} from './model-checks.profiles.js'
import { requestDatasetWriter } from '../background/background-dataset-writer.js'
import { findModelAccountTrustResultAsync, type ModelCheckObservationInput } from '../../storage/model-trust.repository.js'
import { executeModelCheckTokenIntegrityProbes } from './model-checks-token-probes.js'
import {
  createControlledBehaviorObservations,
  executeModelIdentityObservationProbes
} from './model-checks-identity-features.js'

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
  providerProtocolProfileId?: string
  modelCheckProfile: ModelCheckProtocolProfile
  identity: OpenAIGatewayRequestIdentity
  candidateAccounts?: OpenAIAccountSecret[]
  accountId?: string
  groupId?: string
  apiKeyId?: string
}

type ProbeTarget = ModelCheckGatewayProbeTarget

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
  requestModel?: string
  expectedModel?: string
  upstreamModel?: string
  modelMappingApplied?: boolean
  modelMappingSource?: string
  sourceEndpointFamily?: string
  upstreamEndpointFamily?: string
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
    message: `可信对比默认关闭；选择一个你信任的可用 ${modelCheckSupportedProtocolLabel} 协议账户后，会额外消耗该账户额度`
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
    throw new ModelCheckRequestError(400, modelCheckUnsupportedModelMessage())
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
  const target = await resolveModelCheckTargetAsync({ ...input, model, targetId }, access)
  if (trustedComparisonAccountId && trustedComparisonAccountId === target.targetId) {
    throw new ModelCheckRequestError(400, '可信对比账户不能和检测目标相同')
  }
  const comparison = trustedComparisonAccountId
    ? await resolveTrustedComparisonTargetAsync(trustedComparisonAccountId, target, model, access)
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
  const run = await requestDatasetWriter({
    type: 'create_model_check_run',
    input: {
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
        providerCode: target.providerCode,
        providerProtocolProfileId: target.providerProtocolProfileId,
        modelCheckProfileId: target.modelCheckProfile.id,
        modelCheckProtocol: target.modelCheckProfile.protocol,
        model,
        profile: defaultProfile,
        trustedComparison,
        trustedComparisonAccountId: comparison?.targetId,
        trustedComparisonAccountName: comparison?.targetName
      }
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
    const tokenIntegrity = targetUnavailable || target.modelCheckProfile.protocol !== 'openai_responses' || !target.accountId || !target.candidateAccounts?.[0]?.baseUrl
      ? undefined
      : await executeModelCheckTokenIntegrityProbes({
          model,
          providerCode: target.providerCode,
          providerProtocolProfileId: target.providerProtocolProfileId ?? target.modelCheckProfile.id,
          baseUrl: target.candidateAccounts[0].baseUrl,
          credentialMode: target.candidateAccounts[0].type,
          probeSetVersion,
          signal,
          runProbe: async (request, itemKey) => await runModelCheckProbeRequest(target, request, itemKey, signal, progress)
        })
    const identityObservation = targetUnavailable || target.modelCheckProfile.protocol !== 'openai_responses' || !target.accountId || !target.candidateAccounts?.[0]?.baseUrl
      ? undefined
      : await executeModelIdentityObservationProbes({
          model,
          providerCode: target.providerCode,
          providerProtocolProfileId: target.providerProtocolProfileId ?? target.modelCheckProfile.id,
          baseUrl: target.candidateAccounts[0].baseUrl,
          credentialMode: target.candidateAccounts[0].type,
          probeSetVersion,
          runProbe: async (request, itemKey) => await runModelCheckProbeRequest(target, request, itemKey, signal, progress)
        })
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
      ...(tokenIntegrity ? [tokenIntegrity.item] : []),
      ...(identityObservation ? [identityObservation.item] : []),
      ...(crossModelComparison ? [crossModelComparison] : []),
      ...(comparisonSuite?.items ?? []),
      ...(trustedComparisonItem ? [trustedComparisonItem] : []),
      ...(distributionSimilarityItem ? [distributionSimilarityItem] : [])
    ]
    const checks = await requestDatasetWriter({
      type: 'create_model_check_items',
      runId: run.id,
      items: itemInputs
    })
    const summary = summarizeChecks(checks, { trustedComparison })
    const evidenceCompleteness = summarizeEvidenceCompleteness(checks)
    const trustReport = buildModelCheckTrustReport(checks, {
      requestedModel: model,
      probeSetVersion,
      evidenceCoverage: evidenceCompleteness.evidenceCompletenessScore
    })
    if (tokenIntegrity || identityObservation) {
      const controlledBehavior = targetSuite.behaviorObservations && target.candidateAccounts?.[0]?.baseUrl
        ? createControlledBehaviorObservations({
            model,
            providerCode: target.providerCode,
            providerProtocolProfileId: target.providerProtocolProfileId ?? target.modelCheckProfile.id,
            baseUrl: target.candidateAccounts[0].baseUrl,
            credentialMode: target.candidateAccounts[0].type,
            probeSetVersion,
            observations: targetSuite.behaviorObservations
          })
        : []
      await requestDatasetWriter({
        type: 'create_model_check_observations',
        observations: [...(tokenIntegrity?.observations ?? []), ...(identityObservation?.observations ?? []), ...controlledBehavior].map((observation): ModelCheckObservationInput => ({
          ...observation,
          runId: run.id,
          systemAccountId: target.identity.systemAccountId,
          accountId: target.accountId as string,
          providerCode: target.providerCode,
          providerProtocolProfileId: target.providerProtocolProfileId ?? target.modelCheckProfile.id,
          identityStatus: trustReport.identityStatus,
          mappingStatus: trustReport.mappingStatus,
          protocolStatus: trustReport.protocolStatus,
          evidenceCoverage: trustReport.evidenceCoverage
        }))
      })
    }
    await requestDatasetWriter({
      type: 'finish_model_check_run',
      runId: run.id,
      input: {
        ...summary,
        status: 'completed',
        finishedAt: new Date().toISOString(),
        durationMs: Date.now() - startedAtMs,
        resultSummary: {
          itemCount: checks.length,
          passedCount: checks.filter((item) => item.status === 'passed').length,
          warningCount: checks.filter((item) => item.status === 'warning').length,
          failedCount: checks.filter((item) => item.status === 'failed').length,
          skippedCount: checks.filter((item) => item.status === 'skipped').length,
          requestFailureCount: checks.filter((item) => recordValue(item.evidenceSummary)?.requestFailure === true).length,
          evidenceCompleteness,
          trustReport,
          trustedComparison,
          trustedComparisonAccountId: comparison?.targetId
        }
      }
    })
  } catch (error) {
    const message = signal?.aborted ? '模型检测已取消' : error instanceof Error ? error.message : '模型检测失败'
    const status: ModelCheckRunStatus = signal?.aborted ? 'canceled' : 'failed'
    await requestDatasetWriter({
      type: 'finish_model_check_run',
      runId: run.id,
      input: {
        level: 'unavailable',
        score: 0,
        status,
        message,
        finishedAt: new Date().toISOString(),
        durationMs: Date.now() - startedAtMs,
        resultSummary: { errorMessage: message },
        errorCode: status,
        errorMessage: message
      }
    })
  }

  const detail = await getModelCheckRunDetailAsync(run.id, access)
  if (!detail) {
    throw new ModelCheckRequestError(500, '模型检测报告生成失败')
  }
  const enrichedDetail = await withLatestModelTrustResult(detail, target.identity.systemAccountId)
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
  return enrichedDetail
}

export async function listModelCheckRunPage(access?: AccessScope, query: Record<string, unknown> = {}): Promise<ModelCheckRunListResult> {
  return await listModelCheckRunsAsync(access, {
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

export async function getModelCheckRun(id: string, access?: AccessScope): Promise<ModelCheckRunDetail | undefined> {
  const detail = await getModelCheckRunDetailAsync(id, access)
  return detail ? await withLatestModelTrustResult(detail, access?.systemAccountFilterId ?? access?.systemAccountId) : undefined
}

async function withLatestModelTrustResult(detail: ModelCheckRunDetail, fallbackSystemAccountId?: string): Promise<ModelCheckRunDetail> {
  const systemAccountId = detail.systemAccountId ?? fallbackSystemAccountId
  if (!systemAccountId || !detail.accountId) return detail
  const current = recordValue(detail.resultSummary.trustReport) ?? {}
  if (reasonCodes(current.reasonCodes).includes('model_response_evidence_unavailable')) return detail
  if (detail.level === 'unavailable' && !textValue(current.observedModel)) return detail
  const latest = await findModelAccountTrustResultAsync(systemAccountId, detail.accountId, detail.model)
  if (!latest) return detail
  return {
    ...detail,
    resultSummary: {
      ...detail.resultSummary,
      trustReport: {
        ...current,
        ...latest,
        requestedModel: current.requestedModel ?? detail.model
      }
    }
  }
}

function reasonCodes(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === 'string')
    : []
}

async function resolveModelCheckTargetAsync(input: ModelCheckRunRequest & { targetId: string; model: string }, access?: AccessScope): Promise<ModelCheckTarget> {
  if (input.targetType === 'account') {
    return await resolveAccountTargetAsync(input.targetId, input.model, access)
  }
  throw new ModelCheckRequestError(400, '模型检测目标只能选择 AI 账户')
}

async function resolveAccountTargetAsync(accountId: string, model: string, access?: AccessScope): Promise<ModelCheckTarget> {
  const account = await findAccountForTestAsync(accountId, access)
  if (!account) {
    throw new ModelCheckRequestError(404, '账户不存在或无权检测')
  }
  const modelCheckProfile = findModelCheckProfileForAccountModel(account, model)
  if (!modelCheckProfile) {
    if (isModelCheckSupportedProtocolProfile(account)) {
      throw new ModelCheckRequestError(400, `模型 ${model} 不适用于该账户的供应商协议 profile；请选择完整模型 ID：${modelCheckModelsForAccountMessage(account)}`)
    }
    throw new ModelCheckRequestError(400, modelCheckUnsupportedProtocolMessage())
  }
  if (!accountAllowsModel(account, model, modelCheckProfile)) {
    throw new ModelCheckRequestError(400, `账户模型限制未包含 ${model}，请先在 AI 账户中配置完整模型 ID`)
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
  const candidate = (await listOpenAIAccountsForGroupResultAsync(account.boundGroupId, systemAccountId, {
    includeUnavailable: true
  })).accounts.find((item) => item.id === account.id || item.credentialSourceAccountId === account.id)
  if (!candidate) {
    throw new ModelCheckRequestError(400, '账户不在当前分组或凭据不可用，无法执行模型检测')
  }
  return {
    targetType: 'account',
    targetId: account.id,
    targetName: account.name,
    targetOwnerSystemAccountId: account.ownerSystemAccountId ?? account.systemAccountId ?? candidate.accountOwnerSystemAccountId,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    modelCheckProfile,
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

async function resolveTrustedComparisonTargetAsync(accountId: string, target: ModelCheckTarget, model: string, access?: AccessScope): Promise<ModelCheckTarget> {
  try {
    const comparison = await resolveAccountTargetAsync(accountId, model, access)
    if (!sameModelCheckComparisonProfile(target, comparison)) {
      throw new ModelCheckRequestError(400, '可信对比账户必须与检测目标使用相同供应商和相同供应商协议 profile')
    }
    return comparison
  } catch (error) {
    if (error instanceof ModelCheckRequestError) {
      const message = error.message
        .replace(/^账户/, '可信对比账户')
        .replace(modelCheckUnsupportedProtocolMessage(), `可信对比账户必须是支持模型检测的同供应商同协议 profile 账户`)
      throw new ModelCheckRequestError(error.statusCode, message)
    }
    throw error
  }
}

function modelCheckUnsupportedProtocolMessage(): string {
  return `当前仅支持检测 ${modelCheckSupportedProtocolLabel} 协议账户`
}

function modelCheckUnsupportedModelMessage(): string {
  return `当前模型检测仅支持完整模型 ID：${supportedModels.join('、')}`
}

function modelCheckModelsForAccountMessage(account: AccountSummary): string {
  return findModelCheckProfileForAccount(account)?.models.join('、') || supportedModels.join('、')
}

function accountAllowsModel(account: AccountSummary, model: string, modelCheckProfile: ModelCheckProtocolProfile): boolean {
  const models = account.supportedModels?.map((item) => item.trim()).filter(Boolean) ?? []
  return models.length === 0 || models.includes(model) || mappedModelCheckSourceAllowed(account, model, models, modelCheckProfile)
}

function mappedModelCheckSourceAllowed(
  account: AccountSummary,
  model: string,
  supportedModels: string[],
  modelCheckProfile: ModelCheckProtocolProfile
): boolean {
  return modelCheckSourceEndpointFamilies(modelCheckProfile).some((sourceEndpointFamily) => {
    const mapping = resolveOpenAIAccountModelMapping(account, model, sourceEndpointFamily)
    return Boolean(mapping && supportedModels.includes(mapping.upstreamModel))
  })
}

function modelCheckSourceEndpointFamilies(
  profile: ModelCheckProtocolProfile
): Array<'chat_completions' | 'responses' | 'messages' | 'generate_content' | 'stream_generate_content'> {
  if (profile.protocol === 'openai_responses') return ['responses']
  if (profile.protocol === 'openai_chat') return ['chat_completions']
  if (profile.protocol === 'anthropic_messages') return ['messages']
  return ['generate_content', 'stream_generate_content']
}

async function executeProbeSuite(
  target: ModelCheckTarget,
  model: SupportedModel,
  prefix: ModelCheckProbePrefix,
  signal?: AbortSignal,
  progress?: ModelCheckProgressReporter
): Promise<ProbeSuiteResult> {
  const profile = target.modelCheckProfile
  const items: ModelCheckItemCreateInput[] = []

  const basicRequest = createModelCheckProbeRequest(profile.protocol, model, 'Reply with exactly: OK-MODEL-CHECK', { maxOutputTokens: 16, stream: false })
  const basic = await runModelCheckProbeRequest(target, basicRequest, basicProbeItemKey(profile, prefix), signal, progress)
  pushProbeItem(items, evaluateBasicForProfile(profile, basic, model, prefix), progress)
  if (!basic.success) {
    pushProbeItem(items, evaluateUsageShapeProbe([basic], prefix), progress)
    return { items, basic }
  }

  const streamRequest = createModelCheckProbeRequest(profile.protocol, model, 'Reply with exactly: STREAM-OK', { maxOutputTokens: 16, stream: true })
  const stream = await runModelCheckProbeRequest(target, streamRequest, streamProbeItemKey(profile, prefix), signal, progress)
  pushProbeItem(items, evaluateStreamForProfile(profile, stream, model, prefix), progress)

  const structured = await runModelCheckProbeRequest(
    target,
    createModelCheckStructuredOutputRequest(profile.protocol, model),
    `${prefix}.structured_output`,
    signal,
    progress
  )
  pushProbeItem(items, evaluateStructuredOutputProbe(structured, model, prefix), progress)

  const tool = await runModelCheckProbeRequest(
    target,
    createModelCheckToolCallingRequest(profile.protocol, model),
    `${prefix}.tool_calling`,
    signal,
    progress
  )
  pushProbeItem(items, evaluateToolCallingProbe(tool, model, prefix), progress)

  pushProbeItem(items, evaluateUsageShapeProbe([basic, structured, stream], prefix), progress)

  const behaviorObservations: BehaviorProbeObservation[] = []
  for (const definition of behaviorProbeDefinitions) {
    const request = createModelCheckProbeRequest(profile.protocol, model, definition.prompt, {
      maxOutputTokens: definition.maxOutputTokens,
      stream: false
    })
    const result = await runModelCheckProbeRequest(target, request, `${prefix}.behavior.${definition.key}`, signal, progress)
    behaviorObservations.push({ definition, result })
  }
  const behaviorItem = evaluateBehaviorProbeSet(behaviorObservations, model, prefix)
  pushProbeItem(items, behaviorItem, progress)

  const longContextObservations: LongContextProbeObservation[] = []
  for (const definition of longContextProbeDefinitionsForModel(target.providerCode, model)) {
    const longContext = await runModelCheckProbeRequest(
      target,
      createModelCheckLongContextRequest(profile.protocol, model, definition),
      `${prefix}.long_context.${definition.key}`,
      signal,
      progress
    )
    longContextObservations.push({ definition, result: longContext })
  }
  pushProbeItem(items, evaluateLongContextProbeSet(longContextObservations, model, prefix), progress)

  const stabilityResults: GatewayProbeResult[] = []
  for (let index = 1; index <= 3; index += 1) {
    const request = createModelCheckProbeRequest(profile.protocol, model, 'Reply with exactly one uppercase word: VECTOR', {
      maxOutputTokens: 16,
      stream: false
    })
    stabilityResults.push(await runModelCheckProbeRequest(target, request, `${prefix}.stability_${index}`, signal, progress))
  }
  pushProbeItem(items, evaluateStabilityProbe(stabilityResults, model, prefix), progress)

  return { items, basic, behavior: behaviorObservations[0]?.result, behaviorObservations, longContext: longContextObservations[longContextObservations.length - 1]?.result }
}

async function executeCrossModelComparison(target: ModelCheckTarget, targetSuite: ProbeSuiteResult, model: SupportedModel, signal?: AbortSignal, progress?: ModelCheckProgressReporter): Promise<ModelCheckItemCreateInput> {
  const pairedModel = pairedModelForProfile(target.modelCheckProfile, model)
  if (!pairedModel) {
    return {
      itemKey: 'target.cross_model',
      itemType: 'cross_model',
      status: 'skipped',
      score: 0,
      maxScore: 10,
      evidenceSummary: {
        message: '当前供应商协议 profile 未配置辅助模型对照',
        expectedModel: model
      }
    }
  }
  const request = createModelCheckProbeRequest(target.modelCheckProfile.protocol, pairedModel, 'Reply with exactly: CROSS-MODEL-OK', {
    maxOutputTokens: 16,
    stream: false
  })
  const pairedBasic = await runModelCheckProbeRequest(target, request, 'target.cross_model', signal, progress)
  const item = evaluateCrossModelComparisonProbe(targetSuite.basic, pairedBasic, model, pairedModel)
  emitModelCheckItemProgress(progress, item)
  return item
}

async function executeDistributionSimilarityComparison(
  target: ModelCheckTarget,
  comparison: ModelCheckTarget,
  model: SupportedModel,
  signal?: AbortSignal,
  progress?: ModelCheckProgressReporter
): Promise<ModelCheckItemCreateInput> {
  const pairs: DistributionProbePair[] = []
  for (const definition of distributionProbeDefinitions) {
    for (let sampleIndex = 1; sampleIndex <= distributionSampleCount; sampleIndex += 1) {
      const request = createModelCheckDistributionProbeRequest(target.modelCheckProfile.protocol, model, definition)
      const targetResult = await runModelCheckProbeRequest(target, request, `target.distribution.${definition.key}.${sampleIndex}`, signal, progress)
      const comparisonResult = await runModelCheckProbeRequest(comparison, request, `trusted_comparison.distribution.${definition.key}.${sampleIndex}`, signal, progress)
      pairs.push({ definition, sampleIndex, target: targetResult, comparison: comparisonResult })
    }
  }
  const item = evaluateDistributionSimilarityProbe(pairs, model)
  emitModelCheckItemProgress(progress, item)
  return item
}

function basicProbeItemKey(profile: ModelCheckProtocolProfile, prefix: ModelCheckProbePrefix): string {
  return profile.protocol === 'openai_responses' ? `${prefix}.responses_basic` : `${prefix}.protocol_basic`
}

function streamProbeItemKey(profile: ModelCheckProtocolProfile, prefix: ModelCheckProbePrefix): string {
  return profile.protocol === 'openai_responses' ? `${prefix}.responses_stream` : `${prefix}.protocol_stream`
}

function evaluateBasicForProfile(profile: ModelCheckProtocolProfile, result: GatewayProbeResult, model: string, prefix: ModelCheckProbePrefix): ModelCheckItemCreateInput {
  if (profile.protocol === 'openai_responses') {
    return evaluateBasicResponsesProbe(result, model, prefix)
  }
  return evaluateBasicProtocolProbe(result, model, prefix, {
    itemKey: basicProbeItemKey(profile, prefix),
    itemType: 'protocol_basic',
    successMessage: `${profile.protocolLabel} 非流式调用可用`,
    failurePrefix: `${profile.protocolLabel} 非流式调用失败`
  })
}

function evaluateStreamForProfile(profile: ModelCheckProtocolProfile, result: GatewayProbeResult, model: string, prefix: ModelCheckProbePrefix): ModelCheckItemCreateInput {
  if (profile.protocol === 'openai_responses') {
    return evaluateStreamProbe(result, model, prefix)
  }
  return evaluateProtocolStreamProbe(result, model, prefix, {
    itemKey: streamProbeItemKey(profile, prefix),
    itemType: 'protocol_stream',
    successMessage: `${profile.protocolLabel} 流式调用可用`,
    failurePrefix: `${profile.protocolLabel} 流式调用失败`
  })
}

async function runModelCheckProbeRequest(
  target: ProbeTarget,
  request: ModelCheckProbeRequest,
  itemKey: string,
  signal?: AbortSignal,
  progress?: ModelCheckProgressReporter
): Promise<GatewayProbeResult> {
  const resolvedRequest = resolveModelCheckProbeModelMapping(target, request)
  return await runGatewayProbe(target, {
    method: 'POST',
    path: resolvedRequest.path,
    itemKey,
    body: resolvedRequest.body,
    responseProtocol: resolvedRequest.responseProtocol,
    requestModel: resolvedRequest.requestModel,
    expectedModel: resolvedRequest.expectedModel,
    upstreamModel: resolvedRequest.upstreamModel,
    modelMappingApplied: resolvedRequest.modelMappingApplied,
    modelMappingSource: resolvedRequest.modelMappingSource,
    sourceEndpointFamily: resolvedRequest.sourceEndpointFamily,
    upstreamEndpointFamily: resolvedRequest.upstreamEndpointFamily
  }, signal, progress)
}

function resolveModelCheckProbeModelMapping(target: ProbeTarget, request: ModelCheckProbeRequest): ModelCheckProbeRequest {
  const requestModel = request.requestModel ?? request.expectedModel
  const sourceEndpointFamily = request.sourceEndpointFamily ?? modelCheckProbeSourceEndpointFamily(request)
  const mapping = resolveOpenAIAccountModelMapping(target.candidateAccounts?.[0], requestModel, sourceEndpointFamily)
  const upstreamModel = mapping?.upstreamModel ?? request.upstreamModel ?? requestModel
  return {
    ...request,
    requestModel,
    expectedModel: upstreamModel,
    upstreamModel,
    modelMappingApplied: Boolean(mapping),
    modelMappingSource: mapping ? mapping.runtimeSource ?? 'account' : request.modelMappingSource,
    sourceEndpointFamily: mapping?.sourceEndpointFamily ?? sourceEndpointFamily,
    upstreamEndpointFamily: mapping?.upstreamEndpointFamily ?? request.upstreamEndpointFamily
  }
}

function modelCheckProbeSourceEndpointFamily(request: ModelCheckProbeRequest): AccountModelMappingSourceEndpointFamily {
  if (request.responseProtocol === 'openai_responses') return OPENAI_RESPONSES_FAMILY
  if (request.responseProtocol === 'openai_chat') return OPENAI_CHAT_COMPLETIONS_FAMILY
  if (request.responseProtocol === 'anthropic_messages') return ANTHROPIC_MESSAGES_FAMILY
  return request.body.stream === true ? GEMINI_STREAM_GENERATE_CONTENT_FAMILY : GEMINI_GENERATE_CONTENT_FAMILY
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
