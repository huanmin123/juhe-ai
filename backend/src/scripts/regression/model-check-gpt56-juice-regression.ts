import { strict as assert } from 'node:assert'

import type { ModelCheckItemSummary } from '../../domain/types.js'
import {
  classifyGpt56JuiceAnswer,
  executeGpt56JuiceProbes,
  gpt56JuiceProbeContract,
  gpt56JuiceProbeContractHash,
  gpt56JuiceRiskFromChecks,
  gpt56JuiceStrongAnomalyPenalty,
  gpt56JuiceWeakAnomalyPenalty,
  gpt56JuiceCoverageMismatchPenalty,
  gpt56JuiceStrongRepeatState,
  isGpt56JuiceComparableFullRun,
  isGpt56JuiceEarlierRun,
  shouldExecuteGpt56JuiceProbes
} from '../../modules/model-checks/model-checks-gpt56-juice.js'
import { summarizeChecks } from '../../modules/model-checks/model-checks-evaluation.js'
import type { GatewayProbeResult } from '../../modules/model-checks/model-checks-evaluation.js'
import type { ModelCheckProbeRequest } from '../../modules/model-checks/model-checks.payloads.js'
import { buildModelCheckTrustReport } from '../../modules/model-checks/model-checks-trust-report.js'

const solClassification = classifyGpt56JuiceAnswer('gpt-5.6-sol', 'high', '40')
assert.equal(solClassification.classification, 'current_success', 'Sol 高档 Juice=40 必须先匹配申报模型')
assert.equal(classifyGpt56JuiceAnswer('gpt-5.6-luna', 'high', '40').classification, 'mixed', 'Luna 高档 Juice=40 必须识别为 Sol 混用')
assert.equal(classifyGpt56JuiceAnswer('gpt-5.6-sol', 'high', '128').classification, 'unknown_numeric', '已知但非高档签名不能伪造成当前成功')
assert.equal(classifyGpt56JuiceAnswer('gpt-5.6-sol', 'high', 'forty').classification, 'non_numeric', '非数字输出必须保留未识别状态')
assert.equal(shouldExecuteGpt56JuiceProbes({ model: 'gpt-5.6-sol', profile: 'full', protocol: 'openai_responses' }), true)
assert.equal(shouldExecuteGpt56JuiceProbes({ model: 'gpt-5.6-sol', profile: 'quick', protocol: 'openai_responses' }), false, '快速检测不得启用 Juice')
assert.equal(shouldExecuteGpt56JuiceProbes({ model: 'gpt-5.6-sol', profile: 'full', protocol: 'openai_chat' }), false, '非 Responses 协议不得启用 Juice')
assert.equal(shouldExecuteGpt56JuiceProbes({ model: 'gpt-5.5', profile: 'full', protocol: 'openai_responses' }), false, '非 GPT-5.6 模型不得启用 Juice')

const validRequests: ModelCheckProbeRequest[] = []
const valid = await executeGpt56JuiceProbes({
  model: 'gpt-5.6-sol',
  prefix: 'target',
  runProbe: async (request, itemKey) => {
    validRequests.push(request)
    return success(itemKey, outputForValidPlan(request, itemKey))
  }
})
assert.equal(valid.item?.status, 'passed', '完整 Sol 专项响应必须通过')
assert.equal(valid.item?.maxScore, 0, 'Juice 专项通过时不改变通用分数分母')
assert.equal(evidence(valid.item).hardAnomaly, false)
assert.equal(evidence(valid.item).strongAnomaly, false)
assert.equal(evidence(valid.item).scorePenalty, 0)
assert.equal(valid.observations.length, 6, '专项契约必须执行六个物理请求')
assert.equal(validRequests.length, 6)
for (const request of validRequests) {
  assert.equal(request.path, '/v1/responses')
  assert.equal(request.responseProtocol, 'openai_responses')
  assert.equal(request.body.stream, false)
  assert.equal(request.body.store, false)
  assert.deepEqual(request.body.reasoning, { effort: 'high' })
  assert.deepEqual(request.body.include, ['reasoning.encrypted_content'])
}

const mixed = await executeGpt56JuiceProbes({
  model: 'gpt-5.6-luna',
  prefix: 'target',
  runProbe: async (request, itemKey) => success(itemKey, itemKey.includes('.high_') ? '40' : outputForValidPlan(request, itemKey))
})
assert.equal(mixed.item?.status, 'failed', '其他 GPT-5.6 高档签名必须导致专项失败')
assert.equal(evidence(mixed.item).hardAnomaly, true)
assert.equal(evidence(mixed.item).strongAnomaly, true, '三个高档变体一致命中同一其他模型必须是强异常')
assert.equal(evidence(mixed.item).scorePenalty, gpt56JuiceStrongAnomalyPenalty)
assert.equal((evidence(mixed.item).observations as Array<{ classification: string }>)[0]?.classification, 'mixed')

const weakMixed = await executeGpt56JuiceProbes({
  model: 'gpt-5.6-luna',
  prefix: 'target',
  runProbe: async (request, itemKey) => success(itemKey,
    itemKey.endsWith('.high_1') ? '40'
      : itemKey.includes('.high_') ? '48'
        : outputForValidPlan(request, itemKey)
  )
})
assert.equal(evidence(weakMixed.item).hardAnomaly, true)
assert.equal(evidence(weakMixed.item).strongAnomaly, false, '单个高档混用只能作为弱诊断异常')
assert.equal(evidence(weakMixed.item).scorePenalty, gpt56JuiceWeakAnomalyPenalty, '单个高档混用也必须扣分')
assert.equal(gpt56JuiceRiskFromChecks([{
  itemKey: 'target.gpt56_juice',
  itemType: 'gpt56_juice',
  evidenceSummary: evidence(weakMixed.item)
}]).scorePenalty, gpt56JuiceWeakAnomalyPenalty)

const unrecognizedHighOutput = await executeGpt56JuiceProbes({
  model: 'gpt-5.6-sol',
  prefix: 'target',
  runProbe: async (request, itemKey) => success(itemKey,
    itemKey.endsWith('.high_1') ? 'not a juice number' : outputForValidPlan(request, itemKey)
  )
})
assert.equal(evidence(unrecognizedHighOutput.item).scorePenalty, gpt56JuiceWeakAnomalyPenalty, 'HTTP 200 的未知 Juice 输出也必须扣分')
assert((evidence(unrecognizedHighOutput.item).weakReasonCodes as string[]).includes('gpt56_juice_unrecognized_output'))

const coverageMismatch = await executeGpt56JuiceProbes({
  model: 'gpt-5.6-sol',
  prefix: 'target',
  runProbe: async (request, itemKey) => {
    if (!itemKey.endsWith('.coverage')) return success(itemKey, outputForValidPlan(request, itemKey))
    const matched = String(request.body.instructions ?? '').match(/Juice=(\d+)/)
    assert(matched, '覆盖请求必须包含随机权威值')
    return success(itemKey, String(Number(matched[1]) + 1))
  }
})
assert.equal(evidence(coverageMismatch.item).scorePenalty, gpt56JuiceCoverageMismatchPenalty, '覆盖权威值的任意错误数值也必须扣分')

const overridden = await executeGpt56JuiceProbes({
  model: 'gpt-5.6-sol',
  prefix: 'target',
  runProbe: async (request, itemKey) => success(itemKey, itemKey.endsWith('.coverage') ? '40' : outputForValidPlan(request, itemKey))
})
assert.equal(overridden.item?.status, 'failed', '覆盖值被固定 Juice 替换时必须失败')
assert((evidence(overridden.item).observations as Array<{ classification: string }>).some((item) => item.classification === 'explicit_hidden_override'))
assert.equal(evidence(overridden.item).strongAnomaly, true, '覆盖权威值被覆盖必须是强异常')
assert.equal(evidence(overridden.item).scorePenalty, gpt56JuiceStrongAnomalyPenalty)

const outputReplaced = await executeGpt56JuiceProbes({
  model: 'gpt-5.6-sol',
  prefix: 'target',
  runProbe: async (request, itemKey) => success(itemKey, itemKey.endsWith('.output_32') ? '48' : outputForValidPlan(request, itemKey))
})
assert.equal(evidence(outputReplaced.item).strongAnomaly, true, '固定输出被替换必须是强异常')
assert((evidence(outputReplaced.item).strongReasonCodes as string[]).includes('gpt56_juice_output_replaced'))

const invalidResponse = await executeGpt56JuiceProbes({
  model: 'gpt-5.6-sol',
  prefix: 'target',
  runProbe: async (request, itemKey) => itemKey.endsWith('.high_1')
    ? invalidHttp200(itemKey)
    : success(itemKey, outputForValidPlan(request, itemKey))
})
assert.equal(invalidResponse.item?.status, 'failed', 'HTTP 200 但内容校验失败必须保留为专项失败')
assert.equal(evidence(invalidResponse.item).hardAnomaly, true, 'HTTP 200 但响应不可判定必须形成内容异常证据')
assert.equal(evidence(invalidResponse.item).scorePenalty, gpt56JuiceWeakAnomalyPenalty, 'HTTP 200 但响应不可判定必须扣分')
assert.equal((evidence(invalidResponse.item).observations as Array<{ classification: string }>)[0]?.classification, 'response_invalid')

let nonGptCalls = 0
const nonGpt = await executeGpt56JuiceProbes({
  model: 'gpt-5.5',
  prefix: 'target',
  runProbe: async () => {
    nonGptCalls += 1
    return success('unexpected', '40')
  }
})
assert.equal(nonGpt.item, undefined, '非 GPT-5.6 模型不得生成 Juice 项')
assert.equal(nonGptCalls, 0, '非 GPT-5.6 模型不得发送 Juice 请求')

const contract = gpt56JuiceProbeContract()
assert.equal(contract.version, 'gpt56-juice-v2')
assert.equal(contract.hash, gpt56JuiceProbeContractHash)
assert.equal(contract.strongAnomalyPenalty, gpt56JuiceStrongAnomalyPenalty)
assert.equal(contract.weakAnomalyPenalty, gpt56JuiceWeakAnomalyPenalty)
assert.equal(contract.coverageMismatchPenalty, gpt56JuiceCoverageMismatchPenalty)
assert.match(gpt56JuiceProbeContractHash, /^[a-f0-9]{64}$/)
assert.equal(gpt56JuiceStrongRepeatState({ currentStrongAnomaly: false, previousComparable: false }), 'not_applicable')
assert.equal(gpt56JuiceStrongRepeatState({ currentStrongAnomaly: true, previousComparable: false }), 'not_repeated')
assert.equal(gpt56JuiceStrongRepeatState({ currentStrongAnomaly: true, previousComparable: true, previousStrongAnomaly: false }), 'not_repeated')
assert.equal(gpt56JuiceStrongRepeatState({ currentStrongAnomaly: true, previousComparable: true, previousStrongAnomaly: true }), 'repeated')

const comparableHistory = {
  id: 'mcr_0001',
  createdAt: '2026-08-09T00:00:00.000Z',
  status: 'completed',
  profile: 'full',
  probeSetVersion: 'model-checks-v1+gpt56-juice-v2',
  requestSummary: { gpt56Juice: gpt56JuiceProbeContract() },
  resultSummary: {},
  checks: [{
    itemKey: 'target.gpt56_juice',
    itemType: 'gpt56_juice',
    evidenceSummary: {
      probeVersion: 'gpt56-juice-v2',
      probeContractHash: gpt56JuiceProbeContractHash,
      requiredProbeCount: 6,
      completedProbeCount: 6,
      strongAnomaly: true
    }
  }]
}
assert.equal(isGpt56JuiceComparableFullRun(comparableHistory), true, '完整同契约 full 运行才可作为连续异常历史')
assert.equal(isGpt56JuiceComparableFullRun({
  ...comparableHistory,
  resultSummary: { modelCheckUnverified: true }
}), false, '未形成完整质量证据的运行不得作为连续异常历史')
assert.equal(isGpt56JuiceComparableFullRun({
  ...comparableHistory,
  checks: [{
    ...comparableHistory.checks[0],
    evidenceSummary: {
      ...comparableHistory.checks[0]?.evidenceSummary,
      completedProbeCount: 5
    }
  }]
}), false, '未完整执行六个专项请求的运行不得作为连续异常历史')
assert.equal(isGpt56JuiceEarlierRun(comparableHistory, { ...comparableHistory, id: 'mcr_0002', createdAt: '2026-08-09T00:00:01.000Z' }), true)
assert.equal(isGpt56JuiceEarlierRun({ ...comparableHistory, id: 'mcr_0003' }, { ...comparableHistory, id: 'mcr_0002' }), false, '同一创建时间必须按 run id 维持稳定先后顺序')
assert.equal(isGpt56JuiceEarlierRun({ ...comparableHistory, createdAt: '2026-08-09T00:00:02.000Z' }, comparableHistory), false, '晚于当前创建的并发运行不得作为上一轮')

const ordinaryFailureChecks = [
  check('target.responses_stream', 'responses_stream', 'passed', 20, 20, { success: true }),
  check('target.behavior_probe', 'behavior_probe', 'failed', 10, 20, {}),
  check('target.long_context', 'long_context', 'passed', 20, 20, {}),
  check('target.stability', 'stability', 'passed', 15, 15, {}),
  check('target.cross_model', 'cross_model', 'passed', 10, 10, {})
]
assert.equal(summarizeChecks(ordinaryFailureChecks, { trustedComparison: false, profile: 'full' }).level, 'likely', '普通单项失败但总分足够时不应被视为硬失败')
const ordinaryFailureWithWeakJuice = [
  ...ordinaryFailureChecks,
  check('target.gpt56_juice', 'gpt56_juice', 'failed', 0, 0, evidence(weakMixed.item))
]
assert.equal(summarizeChecks(ordinaryFailureWithWeakJuice, { trustedComparison: false, profile: 'full' }).level, 'suspicious', 'Juice 仍应在总览中可见')
assert.equal(
  summarizeChecks(ordinaryFailureWithWeakJuice, { trustedComparison: false, profile: 'full' }).score,
  80,
  '单个 Juice 混用必须直接扣减总分'
)
assert.equal(
  summarizeChecks(ordinaryFailureWithWeakJuice.filter((item) => item.itemKey !== 'target.gpt56_juice'), { trustedComparison: false, profile: 'full' }).level,
  'likely',
  '移除 Juice 后应能单独观察通用质量结论'
)

const juiceSummaryChecks = [
  check('target.responses_stream', 'responses_stream', 'passed', 20, 20, { success: true, requestModel: 'gpt-5.6-luna', responseModel: 'gpt-5.6-luna' }),
  check('target.gpt56_juice', 'gpt56_juice', 'failed', 0, 0, { hardAnomaly: true, strongAnomaly: true })
]
const juiceSummary = summarizeChecks(juiceSummaryChecks, { trustedComparison: false, profile: 'full' })
assert.equal(juiceSummary.level, 'suspicious', 'Juice 硬异常必须进入总览疑似结论')
assert.equal(juiceSummary.score, 100 - gpt56JuiceStrongAnomalyPenalty, '强 Juice 异常必须直接扣减总分，不改变通用项目分值')
assert.equal(gpt56JuiceRiskFromChecks(juiceSummaryChecks).scorePenalty, gpt56JuiceStrongAnomalyPenalty)
const juiceTrustReport = buildModelCheckTrustReport(juiceSummaryChecks, {
  requestedModel: 'gpt-5.6-luna',
  probeSetVersion: 'model-checks-v1+gpt56-juice-v2',
  evidenceCoverage: 100
})
assert.equal(juiceTrustReport.identityStatus, 'suspected_downgrade', 'Juice 硬异常必须进入身份风险摘要')
assert(juiceTrustReport.reasonCodes.includes('gpt56_juice_mixed_or_replaced'), '信任报告必须说明 Juice 风险来源')

console.log('GPT-5.6 Juice 专项回归通过：签名分类、HTTP 200 内容异常扣分、强异常连续复现和质量处罚边界符合预期')

function outputForValidPlan(request: ModelCheckProbeRequest, itemKey: string): string {
  if (itemKey.endsWith('.output_32')) return '32'
  if (itemKey.endsWith('.output_48')) return '48'
  if (itemKey.endsWith('.coverage')) {
    const instructions = String(request.body.instructions ?? '')
    const matched = instructions.match(/Juice=(\d+)/)
    assert(matched, '覆盖请求必须包含随机权威值')
    return matched[1]
  }
  return '40'
}

function success(itemKey: string, outputText: string): GatewayProbeResult {
  return {
    traceId: `trace_${itemKey}`,
    statusCode: 200,
    success: true,
    durationMs: 1,
    bodyText: JSON.stringify({ outputText }),
    bodyTruncated: false,
    headers: {},
    outputText,
    attemptCount: 1
  }
}

function invalidHttp200(itemKey: string): GatewayProbeResult {
  return {
    traceId: `trace_${itemKey}`,
    statusCode: 200,
    success: false,
    durationMs: 1,
    bodyText: '{"status":"completed","output":[]}',
    bodyTruncated: false,
    headers: {},
    errorMessage: '响应缺少可判定输出',
    attemptCount: 1
  }
}

function evidence(item: { evidenceSummary?: unknown } | undefined): Record<string, unknown> {
  assert(item, 'Juice 探针应返回聚合项')
  assert(item.evidenceSummary && typeof item.evidenceSummary === 'object' && !Array.isArray(item.evidenceSummary), 'Juice 聚合项必须包含对象证据')
  return item.evidenceSummary as Record<string, unknown>
}

function check(
  itemKey: string,
  itemType: string,
  status: ModelCheckItemSummary['status'],
  score: number,
  maxScore: number,
  evidenceSummary: Record<string, unknown>
): ModelCheckItemSummary {
  return {
    id: `item_${itemKey}`,
    runId: 'run_gpt56_juice',
    itemKey,
    itemType,
    status,
    score,
    maxScore,
    evidenceSummary,
    createdAt: '2026-08-09T00:00:00.000Z',
    updatedAt: '2026-08-09T00:00:00.000Z'
  }
}
