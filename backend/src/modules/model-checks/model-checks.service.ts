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
  type ModelCheckAccountOption,
  type ModelCheckProfile,
  type ModelCheckTriggerKind,
  type ModelQualityDecision,
  type ModelQualityPenaltyAction,
  type ModelQualityPolicy,
  type ModelQualityPolicySnapshot,
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
  findOpenAIAccountForGroupAsync,
  listOpenAIAccountsForGroupResultAsync,
  getModelCheckRunDetailAsync,
  listModelCheckRunsAsync,
  type ModelCheckItemCreateInput,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { listModelCheckAccountOptionsAsync } from '../../storage/account-options.repository.js'
import { currentSystemAccountId } from '../../storage/access-scope.js'
import type { OpenAIGatewayRequestIdentity } from '../gateway/routes.js'
import { resolveOpenAIAccountModelMapping } from '../gateway/protocols/openai-v1/model-mapping.js'
import {
  defaultModel,
  defaultProfile,
  distributionSampleCount,
  probeSetVersion,
  quickProbeSetVersion,
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
  buildQuickTrustedComparisonItem,
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
  type ModelCheckGatewayProbeTarget,
  type RunGatewayProbeOptions
} from './model-checks-gateway-probe.js'
import {
  isModelCheckSupportedProtocolProfile,
  modelCheckSupportedProtocolLabel
} from './model-checks.provider-capabilities.js'
import {
  configuredModelCheckModelsForAccount,
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
import { getModelQualityPolicyAsync } from '../../storage/model-quality.repository.js'
import { requestStatsWriter } from '../background/background-stats-writer.js'
import { requestDbService } from '../db-service/db-service-ipc.js'
import { requestBackgroundWorkerDbService } from '../background/background-ipc.js'
import { runtimeConfig } from '../../config/runtime.js'

export class ModelCheckRequestError extends Error {
  constructor(public readonly statusCode: number, message: string) {
    super(message)
    this.name = 'ModelCheckRequestError'
  }
}

class ModelCheckRunAlreadyFinishedError extends Error {
  constructor() {
    super('模型检测已按未验证边界结束')
    this.name = 'ModelCheckRunAlreadyFinishedError'
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
  accountConfigRevision?: number
  ownPhysicalAccount: boolean
}

type ProbeTarget = ModelCheckGatewayProbeTarget

export type ModelCheckProgressEvent = {
  type: 'run_started'
  message: string
  targetId: string
  targetName?: string
  model: string
  profile: ModelCheckProfile
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
  type: 'quality_decision'
  triggered: boolean
  score: number
  threshold: number
  hardFailure: boolean
  configuredAction: ModelQualityPenaltyAction
  message: string
} | {
  type: 'quality_enforcement_started'
  action: ModelQualityPenaltyAction
  message: string
} | {
  type: 'quality_enforcement_completed'
  action: ModelQualityPenaltyAction
  result: ModelQualityDecision['result']
  beforeStatus?: ModelQualityDecision['beforeStatus']
  afterStatus?: ModelQualityDecision['afterStatus']
  recoveryDueAt?: string
  message: string
} | {
  type: 'quality_health_sync'
  result: 'applied' | 'pending_retry' | 'failed'
  statHour: string
  message: string
} | {
  type: 'run_completed'
  message: string
  runId: string
  status: ModelCheckRunStatus
  level: ModelCheckRunDetail['level']
  score: number
  maxScore: number
  profile: ModelCheckProfile
  durationMs?: number
}

type ModelCheckProgressReporter = (event: ModelCheckProgressEvent) => void

export interface ModelCheckExecutionContext {
  triggerKind?: ModelCheckTriggerKind
  scheduleId?: string
  policy?: ModelQualityPolicy
  recovery?: {
    ownerId: string
    enforcementId: string
    generation: number
  }
}

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
        value: 'quick',
        label: '快速检测',
        description: '最多执行 2 个轻量串行探针，快速给出初步判断'
      },
      {
        value: 'full',
        label: '深度检测',
        description: '准确优先，不以成本和耗时为约束，执行多轮协议、行为指纹、长上下文、稳定性和可信对比探针'
      }
    ],
    defaultModel,
    defaultProfile,
    trustedComparison
  }
}

export async function listModelCheckAccountOptions(access: AccessScope | undefined, options: { purpose: 'run' | 'history' | 'schedule'; accountId?: string; keyword?: string; selectedIds?: string[]; limit?: number }): Promise<ModelCheckAccountOption[]> {
  const selectedIds = [...new Set((options.selectedIds ?? []).map((id) => id.trim()).filter(Boolean))].slice(0, 20)
  return listModelCheckAccountOptionsAsync(access, { purpose: options.purpose, accountId: options.accountId?.trim() || undefined, keyword: options.keyword?.trim() || undefined, selectedIds, limit: options.limit ?? 50 })
}

export async function runModelCheck(input: ModelCheckRunRequest, access?: AccessScope, signal?: AbortSignal, progress?: ModelCheckProgressReporter, execution: ModelCheckExecutionContext = {}): Promise<ModelCheckRunDetail> {
  const model = normalizeModel(input.model)
  if (!model) {
    throw new ModelCheckRequestError(400, modelCheckUnsupportedModelMessage())
  }
  const requestedProfile: ModelCheckProfile = input.profile ?? defaultProfile
  if (requestedProfile !== 'quick' && requestedProfile !== 'full') {
    throw new ModelCheckRequestError(400, '检测 profile 仅支持 quick 或 full')
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
  const triggerKind = execution.triggerKind ?? 'manual'
  const target = await resolveModelCheckTargetAsync({ ...input, model, targetId }, access, triggerKind === 'quality_recovery')
  const policy = execution.policy ?? await getModelQualityPolicyAsync(target.identity.systemAccountId)
  const profile = requestedProfile
  const policySnapshot: ModelQualityPolicySnapshot = {
    policyRevision: policy.revision,
    configSource: execution.scheduleId ? 'schedule' : 'manual',
    profile,
    manualEnforcementEnabled: policy.manualEnforcementEnabled,
    threshold: policy.penaltyThreshold,
    action: policy.penaltyAction,
    recoveryIntervalMinutes: policy.recoveryIntervalMinutes,
    scheduleId: execution.scheduleId,
    accountConfigRevision: target.accountConfigRevision ?? 0
  }
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
    profile,
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
      profile,
      triggerKind,
      scheduleId: execution.scheduleId,
      trustedComparison,
      trustedComparisonAvailable: Boolean(comparison),
      traceId: runTraceId,
      probeSetVersion: profile === 'quick' ? quickProbeSetVersion : probeSetVersion,
      startedAt,
      policySnapshot,
      requestSummary: {
        targetType: target.targetType,
        targetId: target.targetId,
        targetName: target.targetName,
        providerCode: target.providerCode,
        providerProtocolProfileId: target.providerProtocolProfileId,
        modelCheckProfileId: target.modelCheckProfile.id,
        modelCheckProtocol: target.modelCheckProfile.protocol,
        model,
        profile,
        triggerKind,
        scheduleId: execution.scheduleId,
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
    const targetSuite = await executeProbeSuite(target, model, 'target', profile, signal, progress)
    if (hasTerminalNon200Probe(targetSuite.items)) {
      await finishModelCheckRunWithoutQualityEvidence({
        runId: run.id,
        items: targetSuite.items,
        model,
        profile,
        trustedComparison,
        comparisonTargetId: comparison?.targetId,
        startedAtMs,
        reason: '同一探针第二次 HTTP 非 200，已终止后续探针；未形成质量判定证据'
      })
      throw new ModelCheckRunAlreadyFinishedError()
    } else if (profile === 'quick') {
      let comparisonSuite: ProbeSuiteResult | undefined
      const tokenTarget = targetSuite.basic?.success === true && target.modelCheckProfile.protocol === 'openai_responses' && target.accountId && target.candidateAccounts?.[0]?.baseUrl
        ? await executeModelCheckTokenIntegrityProbes({
            model,
            providerCode: target.providerCode,
            providerProtocolProfileId: target.providerProtocolProfileId ?? target.modelCheckProfile.id,
            baseUrl: target.candidateAccounts[0].baseUrl,
            credentialMode: target.candidateAccounts[0].type,
            probeSetVersion: quickProbeSetVersion,
            profileMode: 'quick',
            prefix: 'target',
            observationEnabled: false,
            signal,
            runProbe: async (request, itemKey) => await runModelCheckProbeRequest(target, request, itemKey, signal, progress)
          })
        : undefined
      if (tokenTarget) targetSuite.items.push(tokenTarget.item)
      if (hasTerminalNon200Probe(targetSuite.items)) {
        await finishModelCheckRunWithoutQualityEvidence({
          runId: run.id,
          items: targetSuite.items,
          model,
          profile,
          trustedComparison,
          comparisonTargetId: comparison?.targetId,
          startedAtMs,
          reason: 'Token 探针第二次 HTTP 非 200，已终止后续探针；未形成质量判定证据'
        })
        throw new ModelCheckRunAlreadyFinishedError()
      }
      comparisonSuite = comparison && targetSuite.basic?.success === true
        ? await executeProbeSuite(comparison, model, 'trusted_comparison', profile, signal, progress)
        : undefined
      if (comparisonSuite && hasTerminalNon200Probe(comparisonSuite.items)) {
        await finishModelCheckRunWithoutQualityEvidence({
          runId: run.id,
          items: [...targetSuite.items, ...comparisonSuite.items],
          model,
          profile,
          trustedComparison,
          comparisonTargetId: comparison?.targetId,
          startedAtMs,
          reason: '可信对比探针第二次 HTTP 非 200，已终止后续探针；未形成质量判定证据'
        })
        throw new ModelCheckRunAlreadyFinishedError()
      }
      const tokenComparison = comparisonSuite && comparison?.modelCheckProfile.protocol === 'openai_responses' && comparison.accountId && comparison.candidateAccounts?.[0]?.baseUrl
        ? await executeModelCheckTokenIntegrityProbes({
            model,
            providerCode: comparison.providerCode,
            providerProtocolProfileId: comparison.providerProtocolProfileId ?? comparison.modelCheckProfile.id,
            baseUrl: comparison.candidateAccounts[0].baseUrl,
            credentialMode: comparison.candidateAccounts[0].type,
            probeSetVersion: quickProbeSetVersion,
            profileMode: 'quick',
            prefix: 'trusted_comparison',
            observationEnabled: false,
            signal,
            runProbe: async (request, itemKey) => await runModelCheckProbeRequest(comparison, request, itemKey, signal, progress)
          })
        : undefined
      if (tokenComparison && comparisonSuite) comparisonSuite.items.push(tokenComparison.item)
      if (comparisonSuite && hasTerminalNon200Probe(comparisonSuite.items)) {
        await finishModelCheckRunWithoutQualityEvidence({
          runId: run.id,
          items: [...targetSuite.items, ...comparisonSuite.items],
          model,
          profile,
          trustedComparison,
          comparisonTargetId: comparison?.targetId,
          startedAtMs,
          reason: '可信对比 Token 探针第二次 HTTP 非 200，已终止后续探针；未形成质量判定证据'
        })
        throw new ModelCheckRunAlreadyFinishedError()
      }
      const targetCrossModel = targetSuite.basic?.success === true
        ? await executeCrossModelComparison(target, targetSuite, model, signal, progress, 'target')
        : undefined
      if (targetCrossModel) targetSuite.items.push(targetCrossModel)
      if (hasTerminalNon200Probe(targetSuite.items)) {
        await finishModelCheckRunWithoutQualityEvidence({
          runId: run.id,
          items: targetSuite.items,
          model,
          profile,
          trustedComparison,
          comparisonTargetId: comparison?.targetId,
          startedAtMs,
          reason: '跨模型探针第二次 HTTP 非 200，已终止后续探针；未形成质量判定证据'
        })
        throw new ModelCheckRunAlreadyFinishedError()
      }
      const comparisonCrossModel = comparison && comparisonSuite?.basic?.success === true
        ? await executeCrossModelComparison(comparison, comparisonSuite, model, signal, progress, 'trusted_comparison')
        : undefined
      if (comparisonCrossModel && comparisonSuite) comparisonSuite.items.push(comparisonCrossModel)
      if (comparisonSuite && hasTerminalNon200Probe(comparisonSuite.items)) {
        await finishModelCheckRunWithoutQualityEvidence({
          runId: run.id,
          items: [...targetSuite.items, ...comparisonSuite.items],
          model,
          profile,
          trustedComparison,
          comparisonTargetId: comparison?.targetId,
          startedAtMs,
          reason: '可信对比探针第二次 HTTP 非 200，已终止后续探针；未形成质量判定证据'
        })
        throw new ModelCheckRunAlreadyFinishedError()
      }
      const trustedComparisonItem = comparisonSuite
        ? buildQuickTrustedComparisonItem(targetSuite, comparisonSuite)
        : undefined
      if (trustedComparisonItem) emitModelCheckItemProgress(progress, trustedComparisonItem)
      const checks = await requestDatasetWriter({
        type: 'create_model_check_items',
        runId: run.id,
        items: [
          ...targetSuite.items,
          ...(comparisonSuite?.items ?? []),
          ...(trustedComparisonItem ? [trustedComparisonItem] : [])
        ]
      })
      const summary = summarizeChecks(checks, { trustedComparison, profile })
      const evidenceCompleteness = summarizeEvidenceCompleteness(checks)
      const trustReport = buildModelCheckTrustReport(checks, {
        requestedModel: model,
        probeSetVersion: quickProbeSetVersion,
        evidenceCoverage: evidenceCompleteness.evidenceCompletenessScore
      })
      throwIfAborted(signal)
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
            trustedComparisonAccountId: comparison?.targetId,
            profile
          }
        }
      })
    } else {
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
    if (tokenIntegrity && hasTerminalNon200Probe([tokenIntegrity.item])) {
      await finishModelCheckRunWithoutQualityEvidence({
        runId: run.id,
        items: [...targetSuite.items, tokenIntegrity.item],
        model,
        profile,
        trustedComparison,
        comparisonTargetId: comparison?.targetId,
        startedAtMs,
        reason: 'Token 探针第二次 HTTP 非 200，已终止后续探针；未形成质量判定证据'
      })
      throw new ModelCheckRunAlreadyFinishedError()
    }
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
    if (identityObservation && hasTerminalNon200Probe([identityObservation.item])) {
      await finishModelCheckRunWithoutQualityEvidence({
        runId: run.id,
        items: [...targetSuite.items, ...(tokenIntegrity ? [tokenIntegrity.item] : []), identityObservation.item],
        model,
        profile,
        trustedComparison,
        comparisonTargetId: comparison?.targetId,
        startedAtMs,
        reason: '身份探针第二次 HTTP 非 200，已终止后续探针；未形成质量判定证据'
      })
      throw new ModelCheckRunAlreadyFinishedError()
    }
    const crossModelComparison = targetUnavailable
      ? undefined
      : await executeCrossModelComparison(target, targetSuite, model, signal, progress)
    if (crossModelComparison && hasTerminalNon200Probe([crossModelComparison])) {
      await finishModelCheckRunWithoutQualityEvidence({
        runId: run.id,
        items: [...targetSuite.items, ...(tokenIntegrity ? [tokenIntegrity.item] : []), ...(identityObservation ? [identityObservation.item] : []), crossModelComparison],
        model,
        profile,
        trustedComparison,
        comparisonTargetId: comparison?.targetId,
        startedAtMs,
        reason: '跨模型探针第二次 HTTP 非 200，已终止后续探针；未形成质量判定证据'
      })
      throw new ModelCheckRunAlreadyFinishedError()
    }
    const comparisonSuite = comparison
      ? targetUnavailable
        ? undefined
        : await executeProbeSuite(comparison, model, 'trusted_comparison', profile, signal, progress)
      : undefined
    if (comparisonSuite && hasTerminalNon200Probe(comparisonSuite.items)) {
      await finishModelCheckRunWithoutQualityEvidence({
        runId: run.id,
        items: [...targetSuite.items, ...(tokenIntegrity ? [tokenIntegrity.item] : []), ...(identityObservation ? [identityObservation.item] : []), ...(crossModelComparison ? [crossModelComparison] : []), ...comparisonSuite.items],
        model,
        profile,
        trustedComparison,
        comparisonTargetId: comparison?.targetId,
        startedAtMs,
        reason: '可信对比探针第二次 HTTP 非 200，已终止后续探针；未形成质量判定证据'
      })
      throw new ModelCheckRunAlreadyFinishedError()
    }
    const trustedComparisonItem = comparisonSuite
      ? buildTrustedComparisonItem(targetSuite, comparisonSuite)
      : undefined
    if (trustedComparisonItem) emitModelCheckItemProgress(progress, trustedComparisonItem)
    const distributionSimilarityItem = comparison
      ? targetUnavailable
        ? undefined
        : await executeDistributionSimilarityComparison(target, comparison, model, signal, progress)
      : undefined
    if (distributionSimilarityItem && hasTerminalNon200Probe([distributionSimilarityItem])) {
      await finishModelCheckRunWithoutQualityEvidence({
        runId: run.id,
        items: [
          ...targetSuite.items,
          ...(tokenIntegrity ? [tokenIntegrity.item] : []),
          ...(identityObservation ? [identityObservation.item] : []),
          ...(crossModelComparison ? [crossModelComparison] : []),
          ...(comparisonSuite?.items ?? []),
          ...(trustedComparisonItem ? [trustedComparisonItem] : []),
          distributionSimilarityItem
        ],
        model,
        profile,
        trustedComparison,
        comparisonTargetId: comparison?.targetId,
        startedAtMs,
        reason: '分布相似度探针第二次 HTTP 非 200，已终止后续探针；未形成质量判定证据'
      })
      throw new ModelCheckRunAlreadyFinishedError()
    }
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
    const summary = summarizeChecks(checks, { trustedComparison, profile })
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
    throwIfAborted(signal)
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
          trustedComparisonAccountId: comparison?.targetId,
          profile
        }
      }
    })
    }
  } catch (error) {
    if (!(error instanceof ModelCheckRunAlreadyFinishedError)) {
      const canceled = signal?.aborted === true
      const message = canceled ? '模型检测已取消，未形成质量判定证据' : error instanceof Error ? error.message : '模型检测失败'
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
          resultSummary: canceled
            ? {
                errorMessage: message,
                modelCheckUnverified: true,
                qualityDecisionSuppressedReason: '未形成质量判定证据'
              }
            : { errorMessage: message },
          errorCode: status,
          errorMessage: message
        }
      })
    }
  }

  const detail = await getModelCheckRunDetailAsync(run.id, access)
  if (!detail) {
    throw new ModelCheckRequestError(500, '模型检测报告生成失败')
  }
  const enrichedDetail = await withLatestModelTrustResult(detail, target.identity.systemAccountId)
  const completedDetail = await applyModelQualityOutcome(enrichedDetail, target, policySnapshot, triggerKind, progress, execution)
  emitModelCheckProgress(progress, {
    type: 'run_completed',
    message: completedDetail.message || completedDetail.errorMessage || '模型检测已结束',
    runId: completedDetail.id,
    status: completedDetail.status,
    profile: completedDetail.profile,
    level: completedDetail.level,
    score: completedDetail.score,
    maxScore: completedDetail.maxScore,
    durationMs: completedDetail.durationMs
  })
  return completedDetail
}

async function finishModelCheckRunWithoutQualityEvidence(input: {
  runId: string
  items: ModelCheckItemCreateInput[]
  model: string
  profile: ModelCheckProfile
  trustedComparison: boolean
  comparisonTargetId?: string
  startedAtMs: number
  reason: string
}): Promise<void> {
  const checks = await requestDatasetWriter({
    type: 'create_model_check_items',
    runId: input.runId,
    items: input.items
  })
  const summary = summarizeChecks(checks, { trustedComparison: input.trustedComparison, profile: input.profile })
  const evidenceCompleteness = summarizeEvidenceCompleteness(checks)
  const trustReport = buildModelCheckTrustReport(checks, {
    requestedModel: input.model,
    probeSetVersion: input.profile === 'quick' ? quickProbeSetVersion : probeSetVersion,
    evidenceCoverage: evidenceCompleteness.evidenceCompletenessScore
  })
  await requestDatasetWriter({
    type: 'finish_model_check_run',
    runId: input.runId,
    input: {
      ...summary,
      level: 'unavailable',
      score: 0,
      message: input.reason,
      status: 'completed',
      finishedAt: new Date().toISOString(),
      durationMs: Date.now() - input.startedAtMs,
      resultSummary: {
        itemCount: checks.length,
        passedCount: checks.filter((item) => item.status === 'passed').length,
        warningCount: checks.filter((item) => item.status === 'warning').length,
        failedCount: checks.filter((item) => item.status === 'failed').length,
        skippedCount: checks.filter((item) => item.status === 'skipped').length,
        requestFailureCount: checks.filter((item) => recordValue(item.evidenceSummary)?.requestFailure === true).length,
        evidenceCompleteness,
        trustReport,
        trustedComparison: input.trustedComparison,
        trustedComparisonAccountId: input.comparisonTargetId,
        profile: input.profile,
        modelCheckUnverified: true,
        qualityDecisionSuppressedReason: '未形成质量判定证据'
      }
    }
  })
}

async function applyModelQualityOutcome(
  detail: ModelCheckRunDetail,
  target: ModelCheckTarget,
  snapshot: ModelQualityPolicySnapshot,
  triggerKind: ModelCheckTriggerKind,
  progress?: ModelCheckProgressReporter,
  execution: ModelCheckExecutionContext = {}
): Promise<ModelCheckRunDetail> {
  const decidedAt = new Date().toISOString()
  const trustReport = recordValue(detail.resultSummary.trustReport)
  const modelCheckUnverified = detail.resultSummary.modelCheckUnverified === true
  if (modelCheckUnverified) {
    const decision: ModelQualityDecision = {
      triggerKind,
      triggered: false,
      hardFailure: false,
      threshold: snapshot.threshold,
      score: detail.score,
      configuredAction: snapshot.action,
      result: 'not_triggered',
      reasonCodes: ['quality_evidence_not_formed'],
      message: '未形成质量判定证据，本次不执行质量处罚、质量隔离/降级或健康统计失败写入',
      decidedAt
    }
    emitModelCheckProgress(progress, {
      type: 'quality_decision',
      triggered: false,
      score: detail.score,
      threshold: snapshot.threshold,
      hardFailure: false,
      configuredAction: snapshot.action,
      message: decision.message
    })
    await requestDatasetWriter({ type: 'update_model_check_quality_decision', runId: detail.id, decision })
    const persisted = await getModelCheckRunDetailAsync(detail.id)
    return persisted ? await withLatestModelTrustResult(persisted, target.identity.systemAccountId) : { ...detail, policySnapshot: snapshot, qualityDecision: decision }
  }
  const hardFailure = detail.level === 'suspicious'
    || textValue(trustReport?.mappingStatus) === 'undeclared_mismatch'
    || textValue(trustReport?.protocolStatus) === 'failed'
  const completed = detail.status === 'completed'
  const unavailable = completed && detail.level === 'unavailable'
  const qualityFailed = completed && !unavailable && (hardFailure || detail.score < snapshot.threshold)
  const decisionReasonCodes = [
    ...reasonCodes(trustReport?.reasonCodes),
    ...(hardFailure ? ['hard_quality_conflict'] : []),
    ...(!hardFailure && qualityFailed ? ['score_below_threshold'] : []),
    ...(unavailable ? ['quality_evidence_unavailable'] : [])
  ]
  const decisionMessage = unavailable
    ? '未形成有效质量证据，本次不执行质量处罚'
    : qualityFailed
      ? `质量判定不达标：${detail.score} 分，处罚阈值 ${snapshot.threshold} 分${hardFailure ? '，并命中硬失败证据' : ''}`
      : completed
        ? `质量达标：${detail.score} 分，处罚阈值 ${snapshot.threshold} 分，未触发处罚`
        : `检测未正常完成（${detail.status}），不执行质量处罚`
  emitModelCheckProgress(progress, {
    type: 'quality_decision',
    triggered: qualityFailed,
    score: detail.score,
    threshold: snapshot.threshold,
    hardFailure,
    configuredAction: snapshot.action,
    message: decisionMessage
  })

  let result: ModelQualityDecision['result'] = 'not_triggered'
  let enforcement: Awaited<ReturnType<typeof requestModelQualityEnforcement>> | undefined
  if (triggerKind === 'quality_recovery' && execution.recovery && target.accountId) {
    emitModelCheckProgress(progress, {
      type: 'quality_enforcement_started',
      action: 'quality_isolate',
      message: qualityFailed || unavailable ? '质量恢复检查未达标，正在续期隔离' : '质量恢复检查达标，正在解除隔离'
    })
    try {
      const recovery = await requestModelQualityRecoveryCompletion({
        ownerId: execution.recovery.ownerId,
        accountId: target.accountId,
        enforcementId: execution.recovery.enforcementId,
        generation: execution.recovery.generation,
        policyRevision: snapshot.policyRevision,
        runId: detail.id,
        passed: completed && !unavailable && !qualityFailed,
        recoveryIntervalMinutes: snapshot.recoveryIntervalMinutes,
        completedAt: decidedAt
      })
      result = recovery.result === 'recovered' ? 'applied' : recovery.result === 'stale' ? 'stale' : 'already_effective'
      enforcement = {
        result,
        beforeStatus: recovery.beforeStatus,
        afterStatus: recovery.afterStatus,
        recoveryDueAt: recovery.nextRecoveryAt,
        enforcementId: execution.recovery.enforcementId,
        generation: execution.recovery.generation,
        message: recovery.message
      }
      emitModelCheckProgress(progress, {
        type: 'quality_enforcement_completed',
        action: 'quality_isolate',
        result,
        beforeStatus: recovery.beforeStatus,
        afterStatus: recovery.afterStatus,
        recoveryDueAt: recovery.nextRecoveryAt,
        message: recovery.message
      })
    } catch (error) {
      result = 'failed'
      logger.error({ event: 'model_quality_recovery_completion_failed', runId: detail.id, accountId: target.accountId, err: error }, '质量隔离恢复写回失败')
    }
  } else if (qualityFailed) {
    const enforcementAllowed = triggerKind !== 'manual'
      || (snapshot.manualEnforcementEnabled && target.ownPhysicalAccount)
    if (!enforcementAllowed) {
      result = 'skipped'
    } else if (!target.accountId || !target.accountConfigRevision) {
      result = 'skipped'
    } else {
      emitModelCheckProgress(progress, {
        type: 'quality_enforcement_started',
        action: snapshot.action,
        message: `开始执行质量处罚：${qualityPenaltyActionLabel(snapshot.action)}`
      })
      try {
        const appliedEnforcement = await requestModelQualityEnforcement({
          systemAccountId: target.identity.systemAccountId,
          accountId: target.accountId,
          runId: detail.id,
          action: snapshot.action,
          policyRevision: snapshot.policyRevision,
          scheduleId: snapshot.scheduleId,
          profile: snapshot.profile,
          penaltyThreshold: snapshot.threshold,
          model: detail.model,
          accountConfigRevision: snapshot.accountConfigRevision,
          recoveryIntervalMinutes: snapshot.recoveryIntervalMinutes,
          message: decisionMessage,
          decidedAt
        })
        if (!appliedEnforcement) throw new Error('DB service 未返回质量处罚结果')
        enforcement = appliedEnforcement
        result = appliedEnforcement.result
        emitModelCheckProgress(progress, {
          type: 'quality_enforcement_completed',
          action: snapshot.action,
          result,
          beforeStatus: appliedEnforcement.beforeStatus,
          afterStatus: appliedEnforcement.afterStatus,
          recoveryDueAt: appliedEnforcement.recoveryDueAt,
          message: appliedEnforcement.message
        })
      } catch (error) {
        result = 'failed'
        const message = error instanceof Error ? error.message : '质量处罚执行失败'
        logger.error({ event: 'model_quality_enforcement_failed', runId: detail.id, accountId: target.accountId, err: error }, '模型质量处罚执行失败')
        emitModelCheckProgress(progress, {
          type: 'quality_enforcement_completed',
          action: snapshot.action,
          result,
          message: `处罚执行失败：${message}`
        })
      }
    }
  }

  let healthSyncResult: ModelQualityDecision['healthSyncResult']
  let healthStatHour: string | undefined
  if (target.accountId && completed && (qualityFailed || unavailable)) {
    try {
      const health = await requestStatsWriter({
        type: 'record_model_quality_health_failure',
        input: {
          accountId: target.accountId,
          systemAccountId: target.identity.systemAccountId,
          providerCode: target.providerCode,
          observedAt: detail.finishedAt ?? decidedAt,
          runId: detail.id,
          model: detail.model,
          profile: detail.profile,
          score: detail.score,
          threshold: snapshot.threshold,
          level: detail.level,
          errorCode: unavailable ? 'model_quality_unavailable' : 'model_quality_failed',
          errorMessage: decisionMessage
        }
      }, 10_000)
      healthSyncResult = 'applied'
      healthStatHour = health.statHour
      emitModelCheckProgress(progress, {
        type: 'quality_health_sync',
        result: 'applied',
        statHour: health.statHour,
        message: '健康监控当前小时已标记为不可用'
      })
    } catch (error) {
      healthSyncResult = 'failed'
      healthStatHour = (detail.finishedAt ?? decidedAt).slice(0, 13)
      logger.error({ event: 'model_quality_health_sync_failed', runId: detail.id, accountId: target.accountId, err: error }, '模型质量健康小时同步失败')
      emitModelCheckProgress(progress, {
        type: 'quality_health_sync',
        result: 'failed',
        statHour: healthStatHour,
        message: '健康监控当前小时同步失败，已记录错误等待后台复查'
      })
    }
  }

  const skipMessage = qualityFailed && result === 'skipped'
    ? triggerKind === 'manual' && !snapshot.manualEnforcementEnabled
      ? '手工检测处罚已关闭，本次仅生成诊断报告'
      : triggerKind === 'manual' && !target.ownPhysicalAccount
        ? '授权账户仅诊断，未执行处罚'
        : '当前账户不满足自动处罚条件，本次仅保留质量事实'
    : undefined
  const decision: ModelQualityDecision = {
    triggerKind,
    triggered: qualityFailed,
    hardFailure,
    threshold: snapshot.threshold,
    score: detail.score,
    configuredAction: snapshot.action,
    result,
    reasonCodes: [...new Set(decisionReasonCodes)],
    beforeStatus: enforcement?.beforeStatus,
    afterStatus: enforcement?.afterStatus,
    recoveryDueAt: enforcement?.recoveryDueAt,
    enforcementId: enforcement?.enforcementId,
    generation: enforcement?.generation,
    healthSyncResult,
    healthStatHour,
    message: skipMessage ?? enforcement?.message ?? decisionMessage,
    decidedAt
  }
  await requestDatasetWriter({ type: 'update_model_check_quality_decision', runId: detail.id, decision })
  const persisted = await getModelCheckRunDetailAsync(detail.id)
  return persisted ? await withLatestModelTrustResult(persisted, target.identity.systemAccountId) : { ...detail, policySnapshot: snapshot, qualityDecision: decision }
}

async function requestModelQualityEnforcement(input: import('../../storage/model-quality.repository.js').ModelQualityEnforcementInput) {
  const operation = { type: 'model_quality_command' as const, command: { kind: 'apply_enforcement' as const, input } }
  const result = runtimeConfig.processRole === 'worker'
    ? await requestBackgroundWorkerDbService(operation)
    : await requestDbService(operation)
  if (!result || result.kind !== 'enforcement') throw new Error('模型质量处罚返回类型无效')
  return result.enforcement
}

async function requestModelQualityRecoveryCompletion(input: Parameters<typeof import('../../storage/model-quality.repository.js').completeModelQualityRecoveryAsync>[0]) {
  const operation = { type: 'model_quality_command' as const, command: { kind: 'complete_recovery' as const, input } }
  const response = runtimeConfig.processRole === 'worker'
    ? await requestBackgroundWorkerDbService(operation)
    : await requestDbService(operation)
  if (!response || response.kind !== 'recovery_completed') throw new Error('质量恢复写回返回类型无效')
  return response.recovery
}

function qualityPenaltyActionLabel(action: ModelQualityPenaltyAction): string {
  if (action === 'disable') return '停用'
  if (action === 'quality_isolate') return '质量隔离'
  return '降级备用'
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
    triggerKind: modelCheckTriggerKindValue(query.triggerKind),
    startAt: textValue(query.startAt),
    endAt: textValue(query.endAt)
  })
}

export async function getModelCheckRun(id: string, access?: AccessScope): Promise<ModelCheckRunDetail | undefined> {
  const detail = await getModelCheckRunDetailAsync(id, access)
  return detail ? await withLatestModelTrustResult(detail, access?.systemAccountFilterId ?? access?.systemAccountId) : undefined
}

async function withLatestModelTrustResult(detail: ModelCheckRunDetail, fallbackSystemAccountId?: string): Promise<ModelCheckRunDetail> {
  if (detail.resultSummary.modelCheckUnverified === true) return detail
  if (detail.profile === 'quick') return detail
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

function modelCheckTriggerKindValue(value: unknown): ModelCheckTriggerKind | undefined {
  return value === 'manual' || value === 'scheduled' || value === 'quality_recovery' ? value : undefined
}

async function resolveModelCheckTargetAsync(input: ModelCheckRunRequest & { targetId: string; model: string }, access?: AccessScope, allowQualityIsolated = false): Promise<ModelCheckTarget> {
  if (input.targetType === 'account') {
    return await resolveAccountTargetAsync(input.targetId, input.model, access, allowQualityIsolated)
  }
  throw new ModelCheckRequestError(400, '模型检测目标只能选择 AI 账户')
}

async function resolveAccountTargetAsync(accountId: string, model: string, access?: AccessScope, allowQualityIsolated = false): Promise<ModelCheckTarget> {
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
  if (!accountAllowsModel(account, model)) {
    throw new ModelCheckRequestError(400, `账户模型限制未包含 ${model}，请先在 AI 账户中配置完整模型 ID`)
  }
  if (account.status === 'disabled') {
    throw new ModelCheckRequestError(400, '账户已停用，无法执行模型检测')
  }
  const unavailableMessage = allowQualityIsolated && account.status === 'quality_isolated' ? undefined : accountTestUnavailableMessage(account)
  if (unavailableMessage) {
    throw new ModelCheckRequestError(400, unavailableMessage)
  }
  if (!account.boundGroupId) {
    throw new ModelCheckRequestError(400, '账户未绑定可用分组，无法按真实链路执行模型检测')
  }
  const systemAccountId = effectiveAccountTargetSystemAccountId(account, access)
  const candidate = allowQualityIsolated && account.status === 'quality_isolated'
    ? await findOpenAIAccountForGroupAsync(account.boundGroupId, account.id, systemAccountId, { includeUnavailable: true, ignoreAvailability: true })
    : (await listOpenAIAccountsForGroupResultAsync(account.boundGroupId, systemAccountId, {
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
    groupId: account.boundGroupId,
    accountConfigRevision: account.configRevision,
    ownPhysicalAccount: account.accessType !== 'authorized'
      && (account.ownerSystemAccountId ?? account.systemAccountId) === systemAccountId
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

function accountAllowsModel(account: AccountSummary, model: string): boolean {
  return configuredModelCheckModelsForAccount(account).includes(model)
}

async function executeProbeSuite(
  target: ModelCheckTarget,
  model: SupportedModel,
  prefix: ModelCheckProbePrefix,
  profileMode: ModelCheckProfile,
  signal?: AbortSignal,
  progress?: ModelCheckProgressReporter
): Promise<ProbeSuiteResult> {
  const profile = target.modelCheckProfile
  const items: ModelCheckItemCreateInput[] = []
  // quick and full profiles share the same transport retry boundary.
  const quickProbeOptions: RunGatewayProbeOptions | undefined = undefined

  const basicRequest = createModelCheckProbeRequest(profile.protocol, model, 'Reply with exactly: OK-MODEL-CHECK', { maxOutputTokens: 16, stream: false })
  const basic = await runModelCheckProbeRequest(target, basicRequest, basicProbeItemKey(profile, prefix), signal, progress, quickProbeOptions)
  const basicItem = evaluateBasicForProfile(profile, basic, model, prefix)
  if (!basic.success || basicItem.status === 'failed') pushProbeItem(items, basicItem, progress)
  if (!basic.success) {
    if (profileMode === 'full') pushProbeItem(items, evaluateUsageShapeProbe([basic], prefix), progress)
    return { items, basic }
  }
  const streamRequest = createModelCheckProbeRequest(profile.protocol, model, 'Reply with exactly: STREAM-OK', { maxOutputTokens: 16, stream: true })
  const stream = await runModelCheckProbeRequest(target, streamRequest, streamProbeItemKey(profile, prefix), signal, progress, quickProbeOptions)
  pushProbeItem(items, evaluateStreamForProfile(profile, stream, model, prefix), progress)
  if (isTerminalNon200Probe(stream)) return { items, basic, behavior: undefined }

  const structured = await runModelCheckProbeRequest(
    target,
    createModelCheckStructuredOutputRequest(profile.protocol, model),
    `${prefix}.structured_output`,
    signal,
    progress,
    quickProbeOptions
  )
  pushProbeItem(items, evaluateStructuredOutputProbe(structured, model, prefix), progress)
  if (isTerminalNon200Probe(structured)) return { items, basic, behavior: undefined }

  const tool = await runModelCheckProbeRequest(
    target,
    createModelCheckToolCallingRequest(profile.protocol, model),
    `${prefix}.tool_calling`,
    signal,
    progress,
    quickProbeOptions
  )
  pushProbeItem(items, evaluateToolCallingProbe(tool, model, prefix), progress)
  if (isTerminalNon200Probe(tool)) return { items, basic, behavior: undefined }

  pushProbeItem(items, evaluateUsageShapeProbe([basic, stream, structured, tool], prefix), progress)

  if (profileMode === 'quick') return { items, basic, behavior: undefined }

  const behaviorObservations: BehaviorProbeObservation[] = []
  for (const definition of behaviorProbeDefinitions) {
    const request = createModelCheckProbeRequest(profile.protocol, model, definition.prompt, {
      maxOutputTokens: definition.maxOutputTokens,
      stream: false
    })
    const result = await runModelCheckProbeRequest(target, request, `${prefix}.behavior.${definition.key}`, signal, progress, quickProbeOptions)
    behaviorObservations.push({ definition, result })
    if (isTerminalNon200Probe(result)) {
      const behaviorItem = evaluateBehaviorProbeSet(behaviorObservations, model, prefix)
      pushProbeItem(items, behaviorItem, progress)
      return { items, basic, behavior: result, behaviorObservations }
    }
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
      progress,
      quickProbeOptions
    )
    longContextObservations.push({ definition, result: longContext })
    if (isTerminalNon200Probe(longContext)) {
      pushProbeItem(items, evaluateLongContextProbeSet(longContextObservations, model, prefix), progress)
      return { items, basic, behavior: behaviorObservations[0]?.result, behaviorObservations, longContext }
    }
  }
  pushProbeItem(items, evaluateLongContextProbeSet(longContextObservations, model, prefix), progress)

  const stabilityResults: GatewayProbeResult[] = []
  for (let index = 1; index <= 3; index += 1) {
    const request = createModelCheckProbeRequest(profile.protocol, model, 'Reply with exactly one uppercase word: VECTOR', {
      maxOutputTokens: 16,
      stream: false
    })
    const stabilityResult = await runModelCheckProbeRequest(target, request, `${prefix}.stability_${index}`, signal, progress, quickProbeOptions)
    stabilityResults.push(stabilityResult)
    if (isTerminalNon200Probe(stabilityResult)) {
      pushProbeItem(items, evaluateStabilityProbe(stabilityResults, model, prefix), progress)
      return { items, basic, behavior: behaviorObservations[0]?.result, behaviorObservations }
    }
  }
  pushProbeItem(items, evaluateStabilityProbe(stabilityResults, model, prefix), progress)

  return { items, basic, behavior: behaviorObservations[0]?.result, behaviorObservations, longContext: longContextObservations[longContextObservations.length - 1]?.result }
}

async function executeCrossModelComparison(target: ModelCheckTarget, targetSuite: ProbeSuiteResult, model: SupportedModel, signal?: AbortSignal, progress?: ModelCheckProgressReporter, prefix: ModelCheckProbePrefix = 'target'): Promise<ModelCheckItemCreateInput> {
  const pairedModel = pairedModelForProfile(target.modelCheckProfile, model)
  if (!pairedModel) {
    return {
      itemKey: `${prefix}.cross_model`,
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
  const pairedBasic = await runModelCheckProbeRequest(target, request, `${prefix}.cross_model`, signal, progress)
  const item = evaluateCrossModelComparisonProbe(targetSuite.basic, pairedBasic, model, pairedModel, prefix)
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
      if (isTerminalNon200Probe(targetResult)) {
        return terminalDistributionSimilarityItem([{ definition, sampleIndex, target: targetResult, comparison: targetResult }], model, targetResult)
      }
      const comparisonResult = await runModelCheckProbeRequest(comparison, request, `trusted_comparison.distribution.${definition.key}.${sampleIndex}`, signal, progress)
      pairs.push({ definition, sampleIndex, target: targetResult, comparison: comparisonResult })
      if (isTerminalNon200Probe(comparisonResult)) {
        return terminalDistributionSimilarityItem(pairs, model, comparisonResult)
      }
    }
  }
  const item = evaluateDistributionSimilarityProbe(pairs, model)
  emitModelCheckItemProgress(progress, item)
  return item
}

function terminalDistributionSimilarityItem(
  pairs: DistributionProbePair[],
  model: SupportedModel,
  result: GatewayProbeResult
): ModelCheckItemCreateInput {
  const item = evaluateDistributionSimilarityProbe(pairs, model)
  return {
    ...item,
    evidenceSummary: {
      ...recordValue(item.evidenceSummary),
      attemptCount: result.attemptCount ?? 1,
      httpStatus: result.statusCode,
      attemptStatusCodes: result.attemptStatusCodes ?? [result.statusCode],
      attemptTraceIds: result.attemptTraceIds ?? [result.traceId]
    }
  }
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
  progress?: ModelCheckProgressReporter,
  options?: RunGatewayProbeOptions
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
  }, signal, progress, options)
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

function hasTerminalNon200Probe(items: ModelCheckItemCreateInput[]): boolean {
  return items.some((item) => {
    const evidence = recordValue(item.evidenceSummary)
    const attemptCount = integerValue(evidence?.attemptCount)
    const httpStatus = integerValue(evidence?.httpStatus)
    return attemptCount !== undefined && attemptCount >= 2 && httpStatus !== undefined && httpStatus !== 200
  })
}

function isTerminalNon200Probe(result: GatewayProbeResult): boolean {
  return (result.attemptCount ?? 0) >= 2 && result.statusCode !== 200
}
