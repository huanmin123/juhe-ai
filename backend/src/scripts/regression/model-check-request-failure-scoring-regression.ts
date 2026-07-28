import { strict as assert } from 'node:assert'

import type { ModelCheckItemSummary } from '../../domain/types.js'
import type { ModelCheckItemCreateInput } from '../../storage/repositories.js'
import {
  evaluateBehaviorProbeSet,
  evaluateBasicResponsesProbe,
  evaluateLongContextProbeSet,
  evaluateStabilityProbe,
  evaluateUsageShapeProbe,
  summarizeChecks,
  summarizeEvidenceCompleteness,
  type GatewayProbeResult
} from '../../modules/model-checks/model-checks-evaluation.js'
import { behaviorProbeDefinitions, longContextProbeDefinitions } from '../../modules/model-checks/model-checks.probes.js'
import { buildModelCheckTrustReport } from '../../modules/model-checks/model-checks-trust-report.js'

const model = 'gpt-5.5'
const basicTimeout = evaluateBasicResponsesProbe(timeoutProbe('trace_basic_timeout'), model, 'target')
assert.equal(basicTimeout.status, 'skipped', '基础探针超时应落未计分，而不是失败')
assert.equal(basicTimeout.score, 0, '基础探针超时不应计入得分')
assert.equal(basicTimeout.maxScore, 0, '基础探针超时不应进入评分分母')
assert.equal(record(basicTimeout.evidenceSummary).requestFailure, true, '基础探针超时应标记为请求失败')
assert.equal(record(basicTimeout.evidenceSummary).excludedFromScoring, true, '基础探针超时应标记为不参与评分')

const basicSuccess = evaluateBasicResponsesProbe(successProbe('trace_basic_success', 'OK-MODEL-CHECK'), model, 'target')
assert.equal(basicSuccess.score, 0, '基础连通成功不应贡献模型可信度得分')
assert.equal(basicSuccess.maxScore, 0, '基础连通成功不应进入模型可信度评分分母')
assert.equal(record(basicSuccess.evidenceSummary).qualificationOnly, true, '基础连通成功只能作为检测资格门槛')
assert.equal(record(basicSuccess.evidenceSummary).excludedFromScoring, true, '基础连通成功必须明确排除评分')

const usageFromTimeout = evaluateUsageShapeProbe([timeoutProbe('trace_usage_timeout')], 'target')
assert.equal(usageFromTimeout.status, 'skipped', '无成功响应时 usage 项应落未计分')
assert.equal(usageFromTimeout.maxScore, 0, '无成功响应时 usage 项不应进入评分分母')

const longContextPartial = evaluateLongContextProbeSet([
  {
    definition: longContextProbeDefinitions[0],
    result: successProbe('trace_long_8k', longContextProbeDefinitions[0]?.marker ?? '')
  },
  {
    definition: longContextProbeDefinitions[1],
    result: timeoutProbe('trace_long_20k')
  },
  {
    definition: longContextProbeDefinitions[2],
    result: successProbe('trace_long_60k', longContextProbeDefinitions[2]?.marker ?? '')
  }
], model, 'target')
assert.equal(longContextPartial.status, 'warning', '部分长上下文请求失败只应提示证据不足')
assert.equal(longContextPartial.score, longContextPartial.maxScore, '请求失败窗口不应拉低长上下文可用证据评分')
assert.equal(record(longContextPartial.evidenceSummary).requestFailureCount, 1, '长上下文应记录请求失败窗口数量')
assert.equal(record(longContextPartial.evidenceSummary).scoringProbeCount, 2, '长上下文只应统计成功响应窗口')
assert.equal(record(longContextPartial.evidenceSummary).modelMismatch, false, '请求失败不能制造模型字段不一致证据')

const longContextAllTimeout = evaluateLongContextProbeSet(longContextProbeDefinitions.map((definition, index) => ({
  definition,
  result: timeoutProbe(`trace_long_timeout_${index}`)
})), model, 'target')
assert.equal(longContextAllTimeout.status, 'skipped', '长上下文全部请求失败应未计分')
assert.equal(longContextAllTimeout.maxScore, 0, '长上下文全部请求失败不应进入评分分母')

const stabilityPartial = evaluateStabilityProbe([
  successProbe('trace_stability_1', 'VECTOR'),
  timeoutProbe('trace_stability_timeout'),
  successProbe('trace_stability_3', 'VECTOR')
], model, 'target')
assert.equal(stabilityPartial.status, 'warning', '稳定性部分请求失败只应提示证据不足')
assert.equal(stabilityPartial.score, stabilityPartial.maxScore, '请求失败轮次不应拉低稳定性可用证据评分')

const behaviorPartial = evaluateBehaviorProbeSet([
  {
    definition: behaviorProbeDefinitions[0],
    result: successProbe('trace_behavior_quartz', 'QUARTZ')
  },
  {
    definition: behaviorProbeDefinitions[1],
    result: timeoutProbe('trace_behavior_timeout')
  },
  {
    definition: behaviorProbeDefinitions[2],
    result: successProbe('trace_behavior_gamma', 'GAMMA 9-7-2')
  }
], model, 'target')
assert.equal(behaviorPartial.status, 'warning', '行为探针部分请求失败只应提示证据不足')
assert.equal(behaviorPartial.score, behaviorPartial.maxScore, '请求失败行为探针不应拉低可用证据评分')

const partialSummary = summarizeChecks([
  summary(evaluateBasicResponsesProbe(successProbe('trace_basic_ok', 'OK-MODEL-CHECK'), model, 'target')),
  summary(evaluateUsageShapeProbe([successProbe('trace_usage_ok', 'OK-MODEL-CHECK')], 'target')),
  summary(passedItem('target.behavior_probe', 'behavior_probe', 35)),
  summary(longContextPartial),
  summary(stabilityPartial),
  summary(passedItem('target.cross_model', 'cross_model', 10))
], { trustedComparison: false })
assert.equal(partialSummary.level, 'uncertain', '关键探针请求失败导致证据不足时总览应为不确定')
assert.notEqual(partialSummary.level, 'suspicious', '请求失败不能被汇总成疑似模型不符')
assert.equal(partialSummary.score, 100, '请求失败不应计入总分分母')

const partialEvidenceCompleteness = summarizeEvidenceCompleteness([
  summary(evaluateBasicResponsesProbe(successProbe('trace_basic_ok_for_completeness', 'OK-MODEL-CHECK'), model, 'target')),
  summary(longContextPartial),
  summary(stabilityPartial)
])
assert.equal(partialEvidenceCompleteness.evidenceProbeCount, 6, '资格预检不应进入模型证据完整度统计')
assert.equal(partialEvidenceCompleteness.scoredEvidenceProbeCount, 4, '证据完整度应只统计成功请求形成的模型证据')
assert.equal(partialEvidenceCompleteness.requestFailureProbeCount, 2, '证据完整度应统计请求失败探针数量')
assert.equal(partialEvidenceCompleteness.evidenceCompletenessScore, 67, '证据完整度应给出独立百分比')

const trustedIsolationSummary = summarizeChecks([
  summary(passedItem('target.behavior_probe', 'behavior_probe', 35)),
  summary({
    ...passedItem('trusted_comparison.behavior_probe', 'behavior_probe', 35),
    status: 'failed',
    score: 0,
    evidenceSummary: { success: true, modelMismatch: true }
  }),
  summary(passedItem('trusted_comparison.comparison', 'trusted_comparison', 10))
], { trustedComparison: true, profile: 'quick' })
assert.equal(trustedIsolationSummary.score, 100, '可信账户原始探针失败不得污染目标账户评分')
assert.equal(trustedIsolationSummary.level, 'likely', '可信账户原始模型名异常不得把目标账户误判为可疑')

const behaviorPartialSummary = summarizeChecks([
  summary(evaluateBasicResponsesProbe(successProbe('trace_basic_behavior_ok', 'OK-MODEL-CHECK'), model, 'target')),
  summary(evaluateUsageShapeProbe([successProbe('trace_usage_behavior_ok', 'OK-MODEL-CHECK')], 'target')),
  summary(behaviorPartial),
  summary(passedItem('target.long_context', 'long_context', 25)),
  summary(passedItem('target.stability', 'stability', 15)),
  summary(passedItem('target.cross_model', 'cross_model', 10))
], { trustedComparison: false })
assert.equal(behaviorPartialSummary.level, 'uncertain', '行为探针部分请求失败时总览应为不确定，不能因满分证据汇总成 likely')

const stabilityPartialSummary = summarizeChecks([
  summary(evaluateBasicResponsesProbe(successProbe('trace_basic_stability_ok', 'OK-MODEL-CHECK'), model, 'target')),
  summary(evaluateUsageShapeProbe([successProbe('trace_usage_stability_ok', 'OK-MODEL-CHECK')], 'target')),
  summary(passedItem('target.behavior_probe', 'behavior_probe', 35)),
  summary(passedItem('target.long_context', 'long_context', 25)),
  summary(stabilityPartial),
  summary(passedItem('target.cross_model', 'cross_model', 10))
], { trustedComparison: false })
assert.equal(stabilityPartialSummary.level, 'uncertain', '稳定性部分请求失败时总览应为不确定，不能因满分证据汇总成 likely')

const unavailableSummary = summarizeChecks([
  summary(basicTimeout),
  summary(usageFromTimeout)
], { trustedComparison: false })
assert.equal(unavailableSummary.level, 'unavailable', '基础链路请求失败时整体应不可检测')
assert.equal(unavailableSummary.score, 0, '基础链路请求失败且没有有效证据时总分应为 0')
const unavailableTrustReport = buildModelCheckTrustReport([
  summary(basicTimeout),
  summary(usageFromTimeout)
], { requestedModel: model, probeSetVersion: 'request-failure-v1', evidenceCoverage: 100 })
assert.equal(unavailableTrustReport.identityStatus, 'insufficient_evidence', '全部请求失败不能形成身份一致结论')
assert.equal(unavailableTrustReport.mappingStatus, 'unknown', '没有 response model 不能形成 direct 映射结论')
assert.equal(unavailableTrustReport.protocolStatus, 'insufficient_evidence', '全部请求失败不能降格成协议 warning')
assert.equal(unavailableTrustReport.evidenceCoverage, 0, '没有有效模型响应时证据覆盖率必须归零')
assert(unavailableTrustReport.reasonCodes.includes('model_response_evidence_unavailable'))

const missingModelTrustReport = buildModelCheckTrustReport([
  summary(passedItem('target.responses_basic', 'responses_basic', 20))
], { requestedModel: model, probeSetVersion: 'missing-model-v1', evidenceCoverage: 100 })
assert.equal(missingModelTrustReport.identityStatus, 'insufficient_evidence', '成功响应缺少 response model 仍不能形成身份一致结论')
assert.equal(missingModelTrustReport.mappingStatus, 'unknown')
assert.equal(missingModelTrustReport.protocolStatus, 'insufficient_evidence')
assert.equal(missingModelTrustReport.evidenceCoverage, 0)

const allLongTimeoutSummary = summarizeChecks([
  summary(evaluateBasicResponsesProbe(successProbe('trace_basic_ok_2', 'OK-MODEL-CHECK'), model, 'target')),
  summary(passedItem('target.behavior_probe', 'behavior_probe', 35)),
  summary(longContextAllTimeout),
  summary(passedItem('target.stability', 'stability', 15)),
  summary(passedItem('target.cross_model', 'cross_model', 10))
], { trustedComparison: false })
assert.equal(allLongTimeoutSummary.level, 'uncertain', '长上下文请求失败应压为证据不足，而不是疑似不符')
assert.notEqual(allLongTimeoutSummary.level, 'suspicious', '长上下文请求失败不能作为模型异常')

console.log('模型检测请求失败评分回归通过：超时和请求失败不进入可信度评分与异常计数')

function successProbe(traceId: string, outputText: string): GatewayProbeResult {
  return {
    traceId,
    statusCode: 200,
    success: true,
    durationMs: 12,
    bodyText: outputText,
    bodyTruncated: false,
    headers: {},
    outputText,
    model,
    usage: {
      input_tokens: 10,
      output_tokens: 3,
      total_tokens: 13
    }
  }
}

function timeoutProbe(traceId: string): GatewayProbeResult {
  return {
    traceId,
    statusCode: 0,
    success: false,
    durationMs: 60_000,
    bodyText: '模型检测探针超时',
    bodyTruncated: false,
    headers: {},
    errorMessage: '模型检测探针超时',
    attemptCount: 3,
    retryAttemptCount: 2,
    retryableFailureCount: 3,
    retryMaxAttempts: 3,
    attemptTraceIds: [`${traceId}_1`, `${traceId}_2`, `${traceId}_3`],
    attemptStatusCodes: [0, 0, 0],
    attemptMessages: ['模型检测探针超时', '模型检测探针超时', '模型检测探针超时']
  }
}

function passedItem(itemKey: string, itemType: string, maxScore: number): ModelCheckItemCreateInput {
  return {
    itemKey,
    itemType,
    status: 'passed',
    score: maxScore,
    maxScore,
    evidenceSummary: {
      success: true,
      message: 'mock pass'
    }
  }
}

function summary(item: ModelCheckItemCreateInput): ModelCheckItemSummary {
  const now = new Date(0).toISOString()
  return {
    id: `mci_${item.itemKey}`,
    runId: 'mcr_request_failure_scoring',
    itemKey: item.itemKey,
    itemType: item.itemType,
    status: item.status,
    score: item.score,
    maxScore: item.maxScore,
    durationMs: item.durationMs,
    traceId: item.traceId,
    evidenceSummary: record(item.evidenceSummary),
    errorCode: item.errorCode,
    errorMessage: item.errorMessage,
    createdAt: now,
    updatedAt: now
  }
}

function record(value: unknown): Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : {}
}
