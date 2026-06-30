import type { ModelCheckItemSummary } from '../../domain/types.js'
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
  usage?: Record<string, unknown>
  errorMessage?: string
  attemptCount?: number
  retryAttemptCount?: number
  retryableFailureCount?: number
  retryMaxAttempts?: number
  attemptTraceIds?: string[]
  attemptStatusCodes?: number[]
  attemptMessages?: string[]
}

export type ProbeSuiteResult = {
  items: ModelCheckItemCreateInput[]
  basic?: GatewayProbeResult
  behavior?: GatewayProbeResult
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

export function evaluateModelCatalogProbe(result: GatewayProbeResult, model: string, prefix: ModelCheckProbePrefix): ModelCheckItemCreateInput {
  const listedModels = listedModelIds(result.json)
  const listed = listedModels.includes(model)
  return item(`${prefix}.model_catalog`, 'model_catalog', listed ? 'passed' : 'warning', listed ? 5 : 2, 5, result, {
    message: listed ? '本地模型目录包含目标模型' : '本地模型目录未确认目标模型；该项只作为低权重证据',
    listed,
    listedModelCount: listedModels.length
  })
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
  const modelEvidence = buildModelMatchEvidence(result.model, model)
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
  const modelEvidence = buildModelMatchEvidence(result.model ?? modelFromSse(result.bodyText), model)
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

export function evaluateToolCallingProbe(result: GatewayProbeResult, model: string, prefix: ModelCheckProbePrefix): ModelCheckItemCreateInput {
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

export function evaluateUsageShapeProbe(results: GatewayProbeResult[], prefix: ModelCheckProbePrefix): ModelCheckItemCreateInput {
  const usage = results.map((result) => result.usage).find(Boolean)
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
  const base = results.find((result) => result.usage) ?? results[0]
  return item(`${prefix}.usage_shape`, 'usage_shape', valid ? 'passed' : 'warning', valid ? 10 : 4, 10, base, {
    message: valid ? 'usage 字段结构可用' : '未观察到完整 usage 字段；可能由上游实现省略',
    usage
  })
}

export function evaluateBehaviorProbeSet(observations: BehaviorProbeObservation[], model: string, prefix: ModelCheckProbePrefix): ModelCheckItemCreateInput {
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

export function evaluateLongContextProbeSet(observations: LongContextProbeObservation[], model: string, prefix: ModelCheckProbePrefix): ModelCheckItemCreateInput {
  const summaries = observations.map((observation) => {
    const modelEvidence = buildModelMatchEvidence(observation.result.model, model)
    return {
      key: observation.definition.key,
      targetInputTokens: observation.definition.targetInputTokens,
      traceId: observation.result.traceId,
      success: observation.result.success,
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
  const total = Math.max(1, summaries.length)
  const successRate = ratio(summaries.filter((summary) => summary.success).length, total)
  const modelMatchRate = ratio(summaries.filter((summary) => summary.matchedModel).length, total)
  const markerRate = ratio(summaries.filter((summary) => summary.foundNeedle).length, total)
  const modelMismatch = summaries.some((summary) => summary.modelMismatch)
  const score = modelMismatch
    ? Math.round(markerRate * 4)
    : Math.max(0, Math.min(15, Math.round((successRate * 0.3 + modelMatchRate * 0.2 + markerRate * 0.5) * 15)))
  const status: ModelCheckItemCreateInput['status'] = modelMismatch
    ? 'failed'
    : successRate === 1 && markerRate === 1 ? 'passed' : markerRate > 0 && successRate > 0 ? 'warning' : 'failed'
  const result = observations[observations.length - 1]?.result ?? emptyProbeResult()
  return item(`${prefix}.long_context`, 'long_context', status, score, 15, result, {
    message: modelMismatch
      ? '长上下文探针返回模型与请求模型不一致'
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
    successRate: roundMetric(successRate),
    modelMatchRate: roundMetric(modelMatchRate),
    markerRate: roundMetric(markerRate),
    modelMismatch,
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

export function evaluateCrossModelComparisonProbe(targetBasic: GatewayProbeResult | undefined, pairedBasic: GatewayProbeResult, model: string, pairedModel: string): ModelCheckItemCreateInput {
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

export function buildTrustedComparisonItem(target: ProbeSuiteResult, comparison: ProbeSuiteResult): ModelCheckItemCreateInput {
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

export function evaluateDistributionSimilarityProbe(pairs: DistributionProbePair[], model: string): ModelCheckItemCreateInput {
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

export function summarizeChecks(checks: ModelCheckItemSummary[], options: { trustedComparison: boolean }): ModelCheckSummaryResult {
  const maxScore = checks.reduce((sum, item) => sum + item.maxScore, 0)
  const rawScore = checks.reduce((sum, item) => sum + item.score, 0)
  const score = maxScore > 0 ? Math.round((rawScore / maxScore) * 100) : 0
  const failedCount = checks.filter((item) => item.status === 'failed').length
  const modelMismatchCount = checks.filter(hasModelMismatchEvidence).length
  const targetBasic = checks.find((item) => item.itemKey === 'target.responses_basic' || item.itemKey === 'target.protocol_basic')
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
  const targetLongContext = checks.find((item) => item.itemKey === 'target.long_context')
  if (targetLongContext?.status === 'failed') {
    return { level: 'suspicious', score, maxScore: 100, message: '长上下文探针未通过，目标链路可能在长输入下被降级或上下文能力不足' }
  }
  if (targetLongContext?.status === 'warning') {
    return { level: 'uncertain', score, maxScore: 100, message: '长上下文探针仅部分通过，建议重点排查中转是否按上下文长度切换模型' }
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

function listedModelIds(json: Record<string, unknown> | undefined): string[] {
  const values: string[] = []
  const data = Array.isArray(json?.data) ? json.data : []
  for (const item of data) {
    const id = textValue(recordValue(item)?.id)
    if (id) values.push(id)
  }
  const models = Array.isArray(json?.models) ? json.models : []
  for (const item of models) {
    const record = recordValue(item)
    const name = textValue(record?.name)
    const version = textValue(record?.version)
    if (name) values.push(name.replace(/^models\//, ''))
    if (version) values.push(version)
  }
  return Array.from(new Set(values))
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
    responseModel: result.model ?? evidenceResponseModel,
    firstTokenMs: result.firstTokenMs,
    responseTruncated: result.bodyTruncated,
    ...retrySummary,
    ...evidence
  }
  evidenceSummary.httpStatus = result.statusCode
  evidenceSummary.success = result.success
  evidenceSummary.responseModel = result.model ?? evidenceResponseModel
  evidenceSummary.firstTokenMs = result.firstTokenMs
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
