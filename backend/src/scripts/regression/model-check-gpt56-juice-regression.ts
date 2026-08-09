import { strict as assert } from 'node:assert'

import {
  classifyGpt56JuiceAnswer,
  executeGpt56JuiceProbes,
  gpt56JuiceProbeContract,
  gpt56JuiceProbeContractHash
} from '../../modules/model-checks/model-checks-gpt56-juice.js'
import type { GatewayProbeResult } from '../../modules/model-checks/model-checks-evaluation.js'
import type { ModelCheckProbeRequest } from '../../modules/model-checks/model-checks.payloads.js'

const solClassification = classifyGpt56JuiceAnswer('gpt-5.6-sol', 'high', '40')
assert.equal(solClassification.classification, 'current_success', 'Sol 高档 Juice=40 必须先匹配申报模型')
assert.equal(classifyGpt56JuiceAnswer('gpt-5.6-luna', 'high', '40').classification, 'mixed', 'Luna 高档 Juice=40 必须识别为 Sol 混用')
assert.equal(classifyGpt56JuiceAnswer('gpt-5.6-sol', 'high', '128').classification, 'unknown_numeric', '已知但非高档签名不能伪造成当前成功')
assert.equal(classifyGpt56JuiceAnswer('gpt-5.6-sol', 'high', 'forty').classification, 'non_numeric', '非数字输出必须保留未识别状态')

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
assert.equal(valid.item?.maxScore, 0, 'Juice 专项不得进入通用分数')
assert.equal(evidence(valid.item).hardAnomaly, false)
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
assert.equal((evidence(mixed.item).observations as Array<{ classification: string }>)[0]?.classification, 'mixed')

const overridden = await executeGpt56JuiceProbes({
  model: 'gpt-5.6-sol',
  prefix: 'target',
  runProbe: async (request, itemKey) => success(itemKey, itemKey.endsWith('.coverage') ? '40' : outputForValidPlan(request, itemKey))
})
assert.equal(overridden.item?.status, 'failed', '覆盖值被固定 Juice 替换时必须失败')
assert((evidence(overridden.item).observations as Array<{ classification: string }>).some((item) => item.classification === 'explicit_hidden_override'))

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
assert.equal(contract.version, 'gpt56-juice-v1')
assert.equal(contract.hash, gpt56JuiceProbeContractHash)
assert.match(gpt56JuiceProbeContractHash, /^[a-f0-9]{64}$/)

console.log('GPT-5.6 Juice 专项回归通过：签名分类、协议契约、覆盖覆写、混用和模型隔离符合预期')

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

function evidence(item: { evidenceSummary?: unknown } | undefined): Record<string, unknown> {
  assert(item, 'Juice 探针应返回聚合项')
  assert(item.evidenceSummary && typeof item.evidenceSummary === 'object' && !Array.isArray(item.evidenceSummary), 'Juice 聚合项必须包含对象证据')
  return item.evidenceSummary as Record<string, unknown>
}
