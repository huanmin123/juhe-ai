import type { ModelCheckItemSummary, ModelCheckProfile } from '../../domain/types.js'
import { createTraceId } from '../../shared/request-context.js'
import type { ModelCheckItemCreateInput } from '../../storage/repositories.js'
import { distributionSampleCount } from './model-checks.constants.js'
import {
  average,
  bounded,
  boundedRatio,
  buildModelMatchEvidence,
  describeModelMismatch,
  hasFunctionCall,
  hasModelMismatchEvidence,
  modelFromSse,
  numberValue,
  parseFirstJsonObject,
  ratio,
  recordValue,
  roundMetric,
  textSimilarity,
  textValue,
  totalTokens
} from './model-checks-parsing.js'
import {
  behaviorConstraintPassed,
  distributionConstraintPassed,
  distributionProbeDefinitions,
  longContextConstraintPassed,
  type BehaviorProbeDefinition,
  type DistributionProbeDefinition,
  type LongContextProbeDefinition
} from './model-checks.probes.js'

export type GatewayProbeResult = {
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
  requestModel?: string
  expectedModel?: string
  upstreamModel?: string
  modelMappingApplied?: boolean
  modelMappingSource?: string
  sourceEndpointFamily?: string
  upstreamEndpointFamily?: string
  usage?: Record<string, unknown>
  systemFingerprint?: string
  errorMessage?: string
  upstreamStatusCode?: number
  retryAfterMs?: number
  rateLimited?: boolean
  attemptCount?: number
  retryAttemptCount?: number
  retryableFailureCount?: number
  retryMaxAttempts?: number
  attemptTraceIds?: string[]
  attemptStatusCodes?: number[]
  attemptUpstreamStatusCodes?: number[]
  attemptRetryAfterMs?: number[]
  attemptMessages?: string[]
}

export type ProbeSuiteResult = {
  items: ModelCheckItemCreateInput[]
  basic?: GatewayProbeResult
  behavior?: GatewayProbeResult
  behaviorObservations?: BehaviorProbeObservation[]
  longContext?: GatewayProbeResult
}

export type BehaviorProbeObservation = {
  definition: BehaviorProbeDefinition
  result: GatewayProbeResult
}

export type DistributionProbePair = {
  definition: DistributionProbeDefinition
  sampleIndex: number
  target: GatewayProbeResult
  comparison: GatewayProbeResult
}

export type LongContextProbeObservation = {
  definition: LongContextProbeDefinition
  result: GatewayProbeResult
}

export type ModelCheckProbePrefix = 'target' | 'trusted_comparison'

export type ModelCheckSummaryResult = {
  level: 'high_confidence' | 'likely' | 'uncertain' | 'suspicious' | 'unavailable'
  score: number
  maxScore: number
  message: string
}

export type ModelCheckEvidenceCompletenessSummary = {
  evidenceProbeCount: number
  scoredEvidenceProbeCount: number
  requestFailureProbeCount: number
  skippedItemCount: number
  requestFailureItemCount: number
  evidenceCompletenessScore: number
}

type RequestFailureAggregate = {
  requestFailureCount: number
  scoringProbeCount: number
  requestSuccessRate: number
}

export function evaluateBasicResponsesProbe(result: GatewayProbeResult, model: string, prefix: ModelCheckProbePrefix): ModelCheckItemCreateInput {
  return evaluateBasicProtocolProbe(result, model, prefix, {
    itemKey: `${prefix}.responses_basic`,
    itemType: 'responses_basic',
    successMessage: 'Responses 非流式调用可用',
    failurePrefix: 'Responses 非流式调用失败'
  })
}

export function evaluateBasicProtocolProbe(result: GatewayProbeResult, model: string, prefix: ModelCheckProbePrefix, options: {
  itemKey: string
  itemType: string
  successMessage: string
  failurePrefix: string
}): ModelCheckItemCreateInput {
  if (!result.success) {
    return requestFailureItem(options.itemKey, options.itemType, result, result.errorMessage ?? `${options.failurePrefix}，HTTP ${result.statusCode}`, {
      expectedModel: model
    })
  }
  const modelEvidence = buildProbeModelMatchEvidence(result, result.model, model)
  const hasOutput = Boolean(result.outputText)
  const score = modelEvidence.modelMismatch
    ? (result.success ? 4 : 0) + (hasOutput ? 2 : 0)
    : (result.success ? 10 : 0) + (modelEvidence.matchedModel ? 5 : 0) + (hasOutput ? 5 : 0)
  const status = modelEvidence.modelMismatch
    ? 'failed'
    : score >= 18 ? 'passed' : score >= 10 ? 'warning' : 'failed'
  return item(options.itemKey, options.itemType, status, score, 20, result, {
    message: describeModelMismatch(modelEvidence) ?? (result.success ? options.successMessage : result.errorMessage ?? `${options.failurePrefix}，HTTP ${result.statusCode}`),
    ...modelEvidence,
    hasOutput
  })
}

export function evaluateStreamProbe(result: GatewayProbeResult, model: string, prefix: ModelCheckProbePrefix): ModelCheckItemCreateInput {
  return evaluateProtocolStreamProbe(result, model, prefix, {
    itemKey: `${prefix}.responses_stream`,
    itemType: 'responses_stream',
    successMessage: 'Responses 流式调用可用',
    failurePrefix: 'Responses 流式调用失败'
  })
}

export function evaluateProtocolStreamProbe(result: GatewayProbeResult, model: string, prefix: ModelCheckProbePrefix, options: {
  itemKey: string
  itemType: string
  successMessage: string
  failurePrefix: string
}): ModelCheckItemCreateInput {
  if (!result.success) {
    return requestFailureItem(options.itemKey, options.itemType, result, result.errorMessage ?? `${options.failurePrefix}，HTTP ${result.statusCode}`, {
      expectedModel: model,
      firstTokenMs: result.firstTokenMs
    })
  }
  const modelEvidence = buildProbeModelMatchEvidence(result, result.model ?? modelFromSse(result.bodyText), model)
  const hasOutput = Boolean(result.outputText)
  const score = modelEvidence.modelMismatch
    ? (result.success ? 4 : 0) + (hasOutput ? 1 : 0)
    : (result.success ? 8 : 0) + (modelEvidence.matchedModel ? 3 : 0) + (hasOutput ? 4 : 0)
  const status = modelEvidence.modelMismatch
    ? 'failed'
    : score >= 13 ? 'passed' : score >= 8 ? 'warning' : 'failed'
  return item(options.itemKey, options.itemType, status, score, 15, result, {
    message: describeModelMismatch(modelEvidence) ?? (result.success ? options.successMessage : result.errorMessage ?? `${options.failurePrefix}，HTTP ${result.statusCode}`),
    ...modelEvidence,
    hasOutput,
    firstTokenMs: result.firstTokenMs
  })
}

export function evaluateStructuredOutputProbe(result: GatewayProbeResult, model: string, prefix: ModelCheckProbePrefix): ModelCheckItemCreateInput {
  if (!result.success) {
    return requestFailureItem(`${prefix}.structured_output`, 'structured_output', result, result.errorMessage ?? `结构化输出调用失败，HTTP ${result.statusCode}`, {
      expectedModel: model
    })
  }
  const outputJson = parseFirstJsonObject(result.outputText)
  const valid = outputJson?.status === 'ok' && typeof outputJson.value === 'number'
  const modelEvidence = buildProbeModelMatchEvidence(result, result.model, model)
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

export function evaluateToolCallingProbe(result: GatewayProbeResult, model: string, prefix: ModelCheckProbePrefix): ModelCheckItemCreateInput {
  if (!result.success) {
    return requestFailureItem(`${prefix}.tool_calling`, 'tool_calling', result, result.errorMessage ?? `工具调用检测失败，HTTP ${result.statusCode}`, {
      expectedModel: model
    })
  }
  const called = hasFunctionCall(result.json, 'record_model_check')
  const modelEvidence = buildProbeModelMatchEvidence(result, result.model, model)
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

export function evaluateUsageShapeProbe(results: GatewayProbeResult[], prefix: ModelCheckProbePrefix): ModelCheckItemCreateInput {
  const successfulResults = results.filter((result) => result.success)
  const base = successfulResults.find((result) => result.usage) ?? successfulResults[0] ?? results[0] ?? emptyProbeResult()
  if (!successfulResults.length) {
    return requestFailureItem(`${prefix}.usage_shape`, 'usage_shape', base, '探针请求失败，未形成 usage 字段证据', {
      requestFailureCount: results.filter((result) => !result.success).length,
      probeCount: results.length
    })
  }
  const usage = successfulResults.map((result) => result.usage).find(Boolean)
  const valid = Boolean(usage && (
    numberValue(usage.input_tokens) !== undefined
    || numberValue(usage.output_tokens) !== undefined
    || numberValue(usage.total_tokens) !== undefined
    || numberValue(usage.prompt_tokens) !== undefined
    || numberValue(usage.completion_tokens) !== undefined
    || numberValue(usage.promptTokenCount) !== undefined
    || numberValue(usage.candidatesTokenCount) !== undefined
    || numberValue(usage.totalTokenCount) !== undefined
  ))
  return item(`${prefix}.usage_shape`, 'usage_shape', valid ? 'passed' : 'warning', valid ? 10 : 4, 10, base, {
    message: valid ? 'usage 字段结构可用' : '未观察到完整 usage 字段；可能由上游实现省略',
    usage
  })
}

export function evaluateBehaviorProbeSet(observations: BehaviorProbeObservation[], model: string, prefix: ModelCheckProbePrefix): ModelCheckItemCreateInput {
  const quickProbe = observations.length === 1
  const probeLabel = quickProbe ? '快速行为探针' : '多行为指纹探针'
  const summaries = observations.map((observation) => {
    const modelEvidence = buildProbeModelMatchEvidence(observation.result, observation.result.success ? observation.result.model : undefined, model)
    return {
      key: observation.definition.key,
      traceId: observation.result.traceId,
      success: observation.result.success,
      requestFailure: !observation.result.success,
      attemptCount: observation.result.attemptCount ?? 1,
      matchedModel: modelEvidence.matchedModel,
      modelMismatch: modelEvidence.modelMismatch,
      constraintPassed: observation.result.success && behaviorConstraintPassed(observation.definition, observation.result.outputText ?? ''),
      outputPreview: bounded(observation.result.outputText),
      responseModel: modelEvidence.responseModel,
      httpStatus: observation.result.statusCode,
      errorMessage: observation.result.errorMessage
    }
  })
  const scorableSummaries = summaries.filter((summary) => summary.success)
  const aggregate = requestFailureAggregate(summaries.length, scorableSummaries.length)
  const result = observations[observations.length - 1]?.result ?? emptyProbeResult()
  if (!scorableSummaries.length) {
    return requestFailureItem(`${prefix}.behavior_probe`, 'behavior_probe', result, `${probeLabel}请求失败，未形成模型行为证据`, {
      expectedModel: model,
      probeCount: summaries.length,
      promptKeys: summaries.map((summary) => summary.key),
      summaries,
      ...aggregate
    })
  }
  const total = Math.max(1, scorableSummaries.length)
  const modelMatchRate = ratio(scorableSummaries.filter((summary) => summary.matchedModel).length, total)
  const constraintRate = ratio(scorableSummaries.filter((summary) => summary.constraintPassed).length, total)
  const modelMismatch = scorableSummaries.some((summary) => summary.modelMismatch)
  const score = modelMismatch
    ? Math.round(constraintRate * 8)
    : Math.max(0, Math.min(35, Math.round((modelMatchRate * 0.3 + constraintRate * 0.7) * 35)))
  const evidencePassed = constraintRate >= 0.85 && modelMatchRate >= 0.85
  const status: ModelCheckItemCreateInput['status'] = modelMismatch
    ? 'failed'
    : evidencePassed ? aggregate.requestFailureCount > 0 ? 'warning' : 'passed' : constraintRate >= 0.6 && modelMatchRate >= 0.6 ? 'warning' : 'failed'
  return item(`${prefix}.behavior_probe`, 'behavior_probe', status, score, 35, result, {
    message: modelMismatch
      ? `${probeLabel}返回模型与请求模型不一致`
      : evidencePassed && aggregate.requestFailureCount > 0
        ? `${probeLabel}可用证据通过，部分探针请求失败未计入评分`
      : status === 'passed'
        ? `${probeLabel}通过`
        : status === 'warning'
          ? quickProbe ? '快速行为探针结果不完整，建议开启深度检测复核' : '多行为指纹探针部分通过，建议结合可信对比观察'
          : quickProbe ? '快速行为探针异常' : '多行为指纹探针大面积异常',
    expectedModel: model,
    probeCount: summaries.length,
    successRate: roundMetric(aggregate.requestSuccessRate),
    modelMatchRate: roundMetric(modelMatchRate),
    constraintRate: roundMetric(constraintRate),
    modelMismatch,
    ...aggregate,
    promptKeys: summaries.map((summary) => summary.key),
    summaries
  })
}

export function evaluateLongContextProbeSet(observations: LongContextProbeObservation[], model: string, prefix: ModelCheckProbePrefix): ModelCheckItemCreateInput {
  const summaries = observations.map((observation) => {
    const modelEvidence = buildProbeModelMatchEvidence(observation.result, observation.result.success ? observation.result.model : undefined, model)
    return {
      key: observation.definition.key,
      targetInputTokens: observation.definition.targetInputTokens,
      traceId: observation.result.traceId,
      success: observation.result.success,
      requestFailure: !observation.result.success,
      attemptCount: observation.result.attemptCount ?? 1,
      matchedModel: modelEvidence.matchedModel,
      modelMismatch: modelEvidence.modelMismatch,
      foundNeedle: observation.result.success && longContextConstraintPassed(observation.definition, observation.result.outputText ?? ''),
      reportedInputTokens: longContextInputTokens(observation.result),
      outputPreview: bounded(observation.result.outputText),
      responseModel: modelEvidence.responseModel,
      httpStatus: observation.result.statusCode,
      errorMessage: observation.result.errorMessage
    }
  })
  const scorableSummaries = summaries.filter((summary) => summary.success)
  const aggregate = requestFailureAggregate(summaries.length, scorableSummaries.length)
  const result = observations[observations.length - 1]?.result ?? emptyProbeResult()
  if (!scorableSummaries.length) {
    return requestFailureItem(`${prefix}.long_context`, 'long_context', result, '长上下文探针请求均失败，未形成长上下文模型证据', {
      expectedModel: model,
      probeCount: summaries.length,
      targetInputTokens: summaries.map((summary) => ({
        key: summary.key,
        targetInputTokens: summary.targetInputTokens,
        reportedInputTokens: summary.reportedInputTokens
      })),
      promptKeys: summaries.map((summary) => summary.key),
      summaries,
      ...aggregate
    })
  }
  const total = Math.max(1, scorableSummaries.length)
  const modelMatchRate = ratio(scorableSummaries.filter((summary) => summary.matchedModel).length, total)
  const markerRate = ratio(scorableSummaries.filter((summary) => summary.foundNeedle).length, total)
  const modelMismatch = scorableSummaries.some((summary) => summary.modelMismatch)
  const score = modelMismatch
    ? Math.round(markerRate * 4)
    : Math.max(0, Math.min(15, Math.round((modelMatchRate * 0.3 + markerRate * 0.7) * 15)))
  const evidencePassed = markerRate === 1 && modelMatchRate === 1
  const status: ModelCheckItemCreateInput['status'] = modelMismatch
    ? 'failed'
    : evidencePassed ? aggregate.requestFailureCount > 0 ? 'warning' : 'passed' : markerRate > 0 ? 'warning' : 'failed'
  return item(`${prefix}.long_context`, 'long_context', status, score, 15, result, {
    message: modelMismatch
      ? '长上下文探针返回模型与请求模型不一致'
      : evidencePassed && aggregate.requestFailureCount > 0
        ? '长上下文可用窗口通过，部分窗口请求失败未计入评分'
      : status === 'passed'
        ? '多窗口长上下文找针探针通过'
        : status === 'warning'
          ? '长上下文探针仅部分窗口通过，目标链路可能按上下文长度降级'
          : '长上下文探针未通过，目标链路可能在长输入下被降级或上下文能力不足',
    expectedModel: model,
    probeCount: summaries.length,
    targetInputTokens: summaries.map((summary) => ({
      key: summary.key,
      targetInputTokens: summary.targetInputTokens,
      reportedInputTokens: summary.reportedInputTokens
    })),
    successRate: roundMetric(aggregate.requestSuccessRate),
    modelMatchRate: roundMetric(modelMatchRate),
    markerRate: roundMetric(markerRate),
    modelMismatch,
    ...aggregate,
    promptKeys: summaries.map((summary) => summary.key),
    summaries
  })
}

function longContextInputTokens(result: GatewayProbeResult): number | undefined {
  const usage = recordValue(result.usage)
  return numberValue(usage?.input_tokens)
    ?? numberValue(usage?.prompt_tokens)
    ?? numberValue(usage?.promptTokenCount)
}

export function evaluateStabilityProbe(results: GatewayProbeResult[], model: string, prefix: ModelCheckProbePrefix): ModelCheckItemCreateInput {
  const observations = results.map((result) => {
    const modelEvidence = buildProbeModelMatchEvidence(result, result.success ? result.model : undefined, model)
    return {
      traceId: result.traceId,
      ok: result.success && (result.outputText ?? '').toUpperCase().includes('VECTOR'),
      success: result.success,
      requestFailure: !result.success,
      attemptCount: result.attemptCount ?? 1,
      httpStatus: result.statusCode,
      errorMessage: result.errorMessage,
      outputPreview: bounded(result.outputText),
      ...modelEvidence
    }
  })
  const scorableObservations = observations.filter((item) => item.success)
  const aggregate = requestFailureAggregate(observations.length, scorableObservations.length)
  const result = results[results.length - 1] ?? emptyProbeResult()
  if (!scorableObservations.length) {
    return requestFailureItem(`${prefix}.stability`, 'stability', result, '稳定性探针请求均失败，未形成稳定性证据', {
      expectedModel: model,
      probeCount: results.length,
      observations,
      ...aggregate
    })
  }
  const total = Math.max(1, scorableObservations.length)
  const okCount = scorableObservations.filter((item) => item.ok).length
  const okRate = ratio(okCount, total)
  const modelMatchRate = ratio(scorableObservations.filter((item) => item.matchedModel).length, total)
  const modelOk = scorableObservations.some((item) => item.matchedModel)
  const modelMismatch = scorableObservations.some((item) => item.modelMismatch)
  const score = modelMismatch
    ? Math.round(okRate * 4)
    : Math.max(0, Math.min(15, Math.round((okRate * 0.75 + modelMatchRate * 0.25) * 15)))
  const evidencePassed = okRate === 1 && modelMatchRate === 1
  const status = modelMismatch
    ? 'failed'
    : evidencePassed ? aggregate.requestFailureCount > 0 ? 'warning' : 'passed' : score >= 8 ? 'warning' : 'failed'
  return item(`${prefix}.stability`, 'stability', status, score, 15, result, {
    message: modelMismatch
      ? '三轮稳定性探针返回模型与请求模型不一致'
      : evidencePassed && aggregate.requestFailureCount > 0
        ? '稳定性可用证据通过，部分轮次请求失败未计入评分'
        : okCount === results.length ? '三轮稳定性探针通过' : '三轮稳定性探针未完全通过',
    expectedModel: model,
    probeCount: results.length,
    passedCount: okCount,
    matchedModel: modelOk,
    modelMismatch,
    okRate: roundMetric(okRate),
    modelMatchRate: roundMetric(modelMatchRate),
    successRate: roundMetric(aggregate.requestSuccessRate),
    ...aggregate,
    observations
  })
}

export function evaluateCrossModelComparisonProbe(targetBasic: GatewayProbeResult | undefined, pairedBasic: GatewayProbeResult, model: string, pairedModel: string): ModelCheckItemCreateInput {
  if (!pairedBasic.success) {
    return requestFailureItem('target.cross_model', 'cross_model', pairedBasic, pairedBasic.errorMessage ?? `辅助模型对照请求失败，HTTP ${pairedBasic.statusCode}`, {
      expectedModel: model,
      pairedModel,
      targetTraceId: targetBasic?.traceId,
      targetResponseModel: targetBasic?.model,
      pairedTraceId: pairedBasic.traceId
    })
  }
  const targetEvidence = targetBasic
    ? buildProbeModelMatchEvidence(targetBasic, targetBasic.model, model)
    : buildModelMatchEvidence(undefined, model)
  const pairedEvidence = buildProbeModelMatchEvidence(pairedBasic, pairedBasic.model, pairedModel)
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

export function buildTrustedComparisonItem(target: ProbeSuiteResult, comparison: ProbeSuiteResult): ModelCheckItemCreateInput {
  const targetBehavior = target.items.find((item) => item.itemType === 'behavior_probe')
  const comparisonBehavior = comparison.items.find((item) => item.itemType === 'behavior_probe')
  const targetBehaviorPassed = targetBehavior?.status === 'passed'
  const comparisonBehaviorPassed = comparisonBehavior?.status === 'passed'
  const targetOk = Boolean(target.basic?.success && targetBehaviorPassed)
  const comparisonOk = Boolean(comparison.basic?.success && comparisonBehaviorPassed)
  const comparable = targetOk && comparisonOk
  const requestFailure = target.basic?.success !== true
    || comparison.basic?.success !== true
    || targetBehavior?.status === 'skipped'
    || comparisonBehavior?.status === 'skipped'
  if (requestFailure) {
    return {
      itemKey: 'trusted_comparison.comparison',
      itemType: 'trusted_comparison',
      status: 'skipped',
      score: 0,
      maxScore: 0,
      durationMs: 0,
      traceId: comparison.basic?.traceId,
      evidenceSummary: {
        message: '可信对比核心探针请求失败，未形成可比模型证据',
        targetTraceId: target.basic?.traceId,
        comparisonTraceId: comparison.basic?.traceId,
        targetBehaviorPassed,
        comparisonBehaviorPassed,
        requestFailure: true,
        excludedFromScoring: true,
        targetBasicSuccess: target.basic?.success,
        comparisonBasicSuccess: comparison.basic?.success,
        targetBehaviorStatus: targetBehavior?.status,
        comparisonBehaviorStatus: comparisonBehavior?.status,
        targetOutputPreview: bounded(target.behavior?.outputText),
        comparisonOutputPreview: bounded(comparison.behavior?.outputText)
      }
    }
  }
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

export function evaluateDistributionSimilarityProbe(pairs: DistributionProbePair[], model: string): ModelCheckItemCreateInput {
  const pairScores = pairs.map((pair) => distributionPairScore(pair))
  const successfulPairScores = pairScores.filter((score) => score.successful)
  const successfulPairCount = successfulPairScores.length
  const aggregate = requestFailureAggregate(pairScores.length, successfulPairCount)
  const pairCoverage = aggregate.requestSuccessRate
  const durationMs = pairs.reduce((sum, pair) => sum + pair.target.durationMs + pair.comparison.durationMs, 0)
  const lastPair = pairs[pairs.length - 1]
  if (!successfulPairScores.length) {
    return {
      itemKey: 'trusted_comparison.distribution_similarity',
      itemType: 'distribution_similarity',
      status: 'skipped',
      score: 0,
      maxScore: 0,
      durationMs,
      traceId: lastPair?.comparison.traceId ?? lastPair?.target.traceId,
      evidenceSummary: {
        message: '分布相似度探针请求均失败，未形成可比模型证据',
        expectedModel: model,
        promptCount: distributionProbeDefinitions.length,
        samplesPerPrompt: distributionSampleCount,
        totalPairs: pairScores.length,
        successfulPairCount,
        pairCoverage: roundMetric(pairCoverage),
        ...aggregate,
        requestFailure: true,
        excludedFromScoring: true,
        traceIds: pairs.slice(0, 12).map((pair) => ({
          key: pair.definition.key,
          sampleIndex: pair.sampleIndex,
          targetTraceId: pair.target.traceId,
          comparisonTraceId: pair.comparison.traceId,
          targetAttemptCount: pair.target.attemptCount ?? 1,
          comparisonAttemptCount: pair.comparison.attemptCount ?? 1,
          targetSuccess: pair.target.success,
          comparisonSuccess: pair.comparison.success
        }))
      }
    }
  }
  const targetConstraintRate = average(successfulPairScores.map((score) => score.targetConstraintPassed ? 1 : 0))
  const comparisonConstraintRate = average(successfulPairScores.map((score) => score.comparisonConstraintPassed ? 1 : 0))
  const averageSimilarity = average(successfulPairScores.map((score) => score.similarity))
  const averageLengthRatio = average(successfulPairScores.map((score) => score.lengthRatio))
  const usageRatios = successfulPairScores.map((score) => score.usageRatio).filter((value): value is number => value !== undefined)
  const averageUsageRatio = average(usageRatios)
  const similarityScore = 0.3 * targetConstraintRate
    + 0.25 * comparisonConstraintRate
    + 0.3 * averageSimilarity
    + 0.1 * averageLengthRatio
    + 0.05 * (averageUsageRatio || 0)
  const score = Math.max(0, Math.min(15, Math.round(similarityScore * 15)))
  const comparisonLooksHealthy = comparisonConstraintRate >= 0.7
  const targetLooksDivergent = targetConstraintRate < 0.55 || averageSimilarity < 0.25 || averageLengthRatio < 0.35
  const status: ModelCheckItemCreateInput['status'] = comparisonLooksHealthy && targetLooksDivergent
    ? 'failed'
    : score >= 12 ? aggregate.requestFailureCount > 0 ? 'warning' : 'passed' : score >= 8 ? 'warning' : 'failed'
  const promptSummaries = distributionProbeDefinitions.map((definition) => {
    const items = successfulPairScores.filter((item) => item.key === definition.key)
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
          ? aggregate.requestFailureCount > 0
            ? '分布相似度可用样本正常，部分采样请求失败未计入评分'
            : '目标链路与可信对比链路存在轻微分布差异，建议结合多次检测观察'
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
      ...aggregate,
      promptSummaries,
      traceIds: pairs.slice(0, 12).map((pair) => ({
        key: pair.definition.key,
        sampleIndex: pair.sampleIndex,
        targetTraceId: pair.target.traceId,
        comparisonTraceId: pair.comparison.traceId,
        targetAttemptCount: pair.target.attemptCount ?? 1,
        comparisonAttemptCount: pair.comparison.attemptCount ?? 1,
        targetSuccess: pair.target.success,
        comparisonSuccess: pair.comparison.success
      }))
    }
  }
}

export function distributionPairScore(pair: DistributionProbePair): {
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

export function emptyProbeResult(): GatewayProbeResult {
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

export function summarizeChecks(checks: ModelCheckItemSummary[], options: { trustedComparison: boolean; profile?: ModelCheckProfile }): ModelCheckSummaryResult {
  const scoredChecks = checks.filter((item) => item.maxScore > 0)
  const maxScore = scoredChecks.reduce((sum, item) => sum + item.maxScore, 0)
  const rawScore = scoredChecks.reduce((sum, item) => sum + item.score, 0)
  const score = maxScore > 0 ? Math.round((rawScore / maxScore) * 100) : 0
  const failedCount = scoredChecks.filter((item) => item.status === 'failed').length
  const modelMismatchCount = checks.filter(hasModelMismatchEvidence).length
  const targetBasic = checks.find((item) => item.itemKey === 'target.responses_basic' || item.itemKey === 'target.protocol_basic')
  const targetBehavior = checks.find((item) => item.itemKey === 'target.behavior_probe')
  const targetLongContext = checks.find((item) => item.itemKey === 'target.long_context')
  const targetStability = checks.find((item) => item.itemKey === 'target.stability')
  const trustedComparisonItem = checks.find((item) => item.itemKey === 'trusted_comparison.comparison')
  const behaviorPassed = targetBehavior?.status === 'passed'
  const longContextPassed = targetLongContext?.status === 'passed'
  const stabilityPassed = targetStability?.status === 'passed'
  const crossModelPassed = checks.some((item) => item.itemKey === 'target.cross_model' && item.status === 'passed')
  const trustedComparisonPassed = !options.trustedComparison || checks.some((item) => item.itemType === 'trusted_comparison' && item.status === 'passed')
  if (modelMismatchCount > 0) {
    return { level: 'suspicious', score, maxScore: 100, message: '响应模型字段与请求模型不一致，目标链路疑似被替换或降级' }
  }
  if (targetBasic?.status === 'failed' && recordValue(targetBasic.evidenceSummary)?.success !== true) {
    return { level: 'unavailable', score, maxScore: 100, message: '目标模型链路不可检测或上游不可用' }
  }
  if (targetBasic && recordValue(targetBasic.evidenceSummary)?.success !== true) {
    return { level: 'unavailable', score, maxScore: 100, message: '目标模型链路不可检测或上游不可用' }
  }
  if (targetLongContext?.status === 'failed') {
    return { level: 'suspicious', score, maxScore: 100, message: '长上下文探针未通过，目标链路可能在长输入下被降级或上下文能力不足' }
  }
  if (targetLongContext?.status === 'warning') {
    const requestFailureCount = numberValue(recordValue(targetLongContext.evidenceSummary)?.requestFailureCount) ?? 0
    if (requestFailureCount > 0) {
      return { level: 'uncertain', score, maxScore: 100, message: '长上下文部分窗口请求失败，未形成完整长上下文模型证据' }
    }
    return { level: 'uncertain', score, maxScore: 100, message: '长上下文探针仅部分通过，建议重点排查中转是否按上下文长度切换模型' }
  }
  if (targetLongContext?.status === 'skipped') {
    return { level: 'uncertain', score, maxScore: 100, message: '长上下文探针请求失败，未形成足够长上下文模型证据' }
  }
  if (targetBehavior?.status === 'skipped' || targetStability?.status === 'skipped') {
    return { level: 'uncertain', score, maxScore: 100, message: '部分关键探针请求失败，未形成足够模型可信度证据' }
  }
  const behaviorRequestFailureCount = numberValue(recordValue(targetBehavior?.evidenceSummary)?.requestFailureCount) ?? 0
  const stabilityRequestFailureCount = numberValue(recordValue(targetStability?.evidenceSummary)?.requestFailureCount) ?? 0
  if ((targetBehavior?.status === 'warning' && behaviorRequestFailureCount > 0) || (targetStability?.status === 'warning' && stabilityRequestFailureCount > 0)) {
    return { level: 'uncertain', score, maxScore: 100, message: '关键行为或稳定性探针存在请求失败，未形成完整模型可信度证据' }
  }
  if (options.trustedComparison && trustedComparisonItem?.status === 'skipped') {
    return { level: 'uncertain', score, maxScore: 100, message: '可信对比探针请求失败，未形成完整可比模型证据' }
  }
  if (options.profile === 'quick') {
    if (score >= 78 && failedCount <= 1 && behaviorPassed) {
      return { level: 'likely', score, maxScore: 100, message: '快速检测未发现明显异常，仅形成初步估计；需要更高准确度请开启深度检测' }
    }
    if (score >= 50) {
      return { level: 'uncertain', score, maxScore: 100, message: '快速检测存在不确定项，建议开启深度检测复核' }
    }
    if (score >= 25) {
      return { level: 'suspicious', score, maxScore: 100, message: '快速检测发现明显异常，建议检查上游配置并使用深度检测复核' }
    }
    return { level: 'unavailable', score, maxScore: 100, message: '快速检测未形成可用模型证据' }
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

export function summarizeEvidenceCompleteness(checks: ModelCheckItemSummary[]): ModelCheckEvidenceCompletenessSummary {
  const totals = checks.reduce((summary, item) => {
    const evidence = recordValue(item.evidenceSummary)
    const explicitRequestFailureCount = numberValue(evidence?.requestFailureCount)
    const explicitScoringProbeCount = numberValue(evidence?.scoringProbeCount)
    if (explicitRequestFailureCount !== undefined || explicitScoringProbeCount !== undefined) {
      const requestFailureCount = Math.max(0, Math.trunc(explicitRequestFailureCount ?? 0))
      const scoringProbeCount = Math.max(0, Math.trunc(explicitScoringProbeCount ?? 0))
      summary.evidenceProbeCount += requestFailureCount + scoringProbeCount
      summary.scoredEvidenceProbeCount += scoringProbeCount
      summary.requestFailureProbeCount += requestFailureCount
      return summary
    }
    if (evidence?.requestFailure === true || evidence?.excludedFromScoring === true) {
      summary.evidenceProbeCount += 1
      summary.requestFailureProbeCount += 1
      return summary
    }
    if (item.status !== 'skipped') {
      summary.evidenceProbeCount += 1
      summary.scoredEvidenceProbeCount += 1
    }
    return summary
  }, {
    evidenceProbeCount: 0,
    scoredEvidenceProbeCount: 0,
    requestFailureProbeCount: 0
  })
  const evidenceCompletenessScore = totals.evidenceProbeCount > 0
    ? Math.round((totals.scoredEvidenceProbeCount / totals.evidenceProbeCount) * 100)
    : 0
  return {
    ...totals,
    skippedItemCount: checks.filter((item) => item.status === 'skipped').length,
    requestFailureItemCount: checks.filter((item) => recordValue(item.evidenceSummary)?.requestFailure === true).length,
    evidenceCompletenessScore
  }
}

function requestFailureAggregate(totalProbeCount: number, scoringProbeCount: number): RequestFailureAggregate {
  const safeTotal = Math.max(0, totalProbeCount)
  const safeScoring = Math.max(0, Math.min(scoringProbeCount, safeTotal))
  return {
    requestFailureCount: safeTotal - safeScoring,
    scoringProbeCount: safeScoring,
    requestSuccessRate: safeTotal > 0 ? ratio(safeScoring, safeTotal) : 0
  }
}

function buildProbeModelMatchEvidence(result: GatewayProbeResult, actual: unknown, fallbackModel: string): ReturnType<typeof buildModelMatchEvidence> {
  return buildModelMatchEvidence(actual, probeExpectedModel(result, fallbackModel), probeModelContext(result, fallbackModel))
}

function probeExpectedModel(result: GatewayProbeResult, fallbackModel: string): string {
  return result.expectedModel ?? result.upstreamModel ?? fallbackModel
}

function probeModelContext(result: GatewayProbeResult, fallbackModel: string): {
  requestModel?: string
  upstreamModel?: string
  modelMappingApplied?: boolean
  modelMappingSource?: string
  sourceEndpointFamily?: string
  upstreamEndpointFamily?: string
} {
  return {
    requestModel: result.requestModel ?? fallbackModel,
    upstreamModel: result.upstreamModel,
    modelMappingApplied: result.modelMappingApplied,
    modelMappingSource: result.modelMappingSource,
    sourceEndpointFamily: result.sourceEndpointFamily,
    upstreamEndpointFamily: result.upstreamEndpointFamily
  }
}

function requestFailureItem(
  itemKey: string,
  itemType: string,
  result: GatewayProbeResult,
  message: string,
  evidence: Record<string, unknown> = {}
): ModelCheckItemCreateInput {
  return item(itemKey, itemType, 'skipped', 0, 0, result, {
    message,
    requestFailure: true,
    excludedFromScoring: true,
    ...evidence
  })
}

function retryEvidence(result: GatewayProbeResult): Record<string, unknown> {
  if (!result.attemptCount || result.attemptCount <= 1) return {}
  return {
    attemptCount: result.attemptCount,
    retryAttemptCount: result.retryAttemptCount,
    retryMaxAttempts: result.retryMaxAttempts ?? result.attemptCount,
    retryableFailureCount: result.retryableFailureCount,
    attemptTraceIds: result.attemptTraceIds,
    attemptStatusCodes: result.attemptStatusCodes,
    attemptUpstreamStatusCodes: result.attemptUpstreamStatusCodes,
    attemptRetryAfterMs: result.attemptRetryAfterMs,
    attemptMessages: result.attemptMessages
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
  const retrySummary = retryEvidence(result)
  const evidenceSummary: Record<string, unknown> = {
    httpStatus: result.statusCode,
    success: result.success,
    requestModel: result.requestModel,
    expectedModel: result.expectedModel,
    upstreamModel: result.upstreamModel,
    modelMappingApplied: result.modelMappingApplied,
    modelMappingSource: result.modelMappingSource,
    sourceEndpointFamily: result.sourceEndpointFamily,
    upstreamEndpointFamily: result.upstreamEndpointFamily,
    responseModel: result.model ?? evidenceResponseModel,
    firstTokenMs: result.firstTokenMs,
    upstreamStatusCode: result.upstreamStatusCode,
    rateLimited: result.rateLimited,
    responseTruncated: result.bodyTruncated,
    ...retrySummary,
    ...evidence
  }
  evidenceSummary.httpStatus = result.statusCode
  evidenceSummary.success = result.success
  evidenceSummary.requestModel = result.requestModel
  evidenceSummary.expectedModel = result.expectedModel
  evidenceSummary.upstreamModel = result.upstreamModel
  evidenceSummary.modelMappingApplied = result.modelMappingApplied
  evidenceSummary.modelMappingSource = result.modelMappingSource
  evidenceSummary.sourceEndpointFamily = result.sourceEndpointFamily
  evidenceSummary.upstreamEndpointFamily = result.upstreamEndpointFamily
  evidenceSummary.responseModel = result.model ?? evidenceResponseModel
  evidenceSummary.firstTokenMs = result.firstTokenMs
  evidenceSummary.upstreamStatusCode = result.upstreamStatusCode
  evidenceSummary.rateLimited = result.rateLimited
  evidenceSummary.responseTruncated = result.bodyTruncated
  for (const [key, value] of Object.entries(retrySummary)) {
    evidenceSummary[key] = value
  }
  return {
    itemKey,
    itemType,
    status,
    score,
    maxScore,
    durationMs: result.durationMs,
    traceId: result.traceId,
    evidenceSummary,
    errorCode: result.success ? undefined : `http_${result.statusCode}`,
    errorMessage: result.success ? undefined : result.errorMessage
  }
}
