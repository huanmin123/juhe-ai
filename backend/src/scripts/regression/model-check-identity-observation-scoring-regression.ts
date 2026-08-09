import { strict as assert } from 'node:assert'

import { executeModelIdentityObservationProbes } from '../../modules/model-checks/model-checks-identity-features.js'
import type { ModelCheckProbeRequest } from '../../modules/model-checks/model-checks.payloads.js'

const allFailed = await executeModelIdentityObservationProbes({
  model: 'gpt-5.6-sol',
  providerCode: 'openai',
  providerProtocolProfileId: 'openai_responses',
  baseUrl: 'https://example.invalid/v1',
  credentialMode: 'api_key',
  probeSetVersion: 'identity-observation-score-regression',
  runProbe: async (_request, itemKey) => ({
    traceId: `trace_${itemKey}`,
    statusCode: 200,
    success: true,
    durationMs: 1,
    bodyText: '',
    bodyTruncated: false,
    headers: {},
    outputText: 'wrong deterministic answer',
    model: itemKey.includes('.gpt-5.6-sol.') ? 'gpt-5.6-sol' : 'gpt-5.6-terra',
    usage: { input_tokens: 10, output_tokens: 3 }
  })
})

assert.equal(allFailed.item.status, 'failed', 'HTTP 200 的生成式 canary 全部违反确定性约束必须失败')
assert.equal(allFailed.item.score, 0, '生成式 canary 全部失败必须失去身份评分')
assert.equal(allFailed.item.maxScore, 10, '生成式 canary 内容失败必须进入评分分母')
const allFailedEvidence = allFailed.item.evidenceSummary as Record<string, unknown>
assert.equal(allFailedEvidence.diagnosticOnly, false, '生成式 canary 内容失败不得标记为仅诊断')
assert.equal(allFailedEvidence.observationCount, 21, 'GPT-5.6 组必须覆盖三个模型和七个 canary')
assert.equal(allFailedEvidence.constraintPassedCount, 0)

let remainingDeterministicFailures = 1
const oneFailed = await executeModelIdentityObservationProbes({
  model: 'gpt-5.6-sol',
  providerCode: 'openai',
  providerProtocolProfileId: 'openai_responses',
  baseUrl: 'https://example.invalid/v1',
  credentialMode: 'api_key',
  probeSetVersion: 'identity-observation-single-failure-regression',
  runProbe: async (request, itemKey) => ({
    traceId: `trace_${itemKey}`,
    statusCode: 200,
    success: true,
    durationMs: 1,
    bodyText: '',
    bodyTruncated: false,
    headers: {},
    outputText: remainingDeterministicFailures-- > 0 ? 'wrong deterministic answer' : successfulCanaryOutput(request),
    model: request.expectedModel,
    usage: { input_tokens: 10, output_tokens: 3 }
  })
})

assert.equal(oneFailed.item.status, 'warning', '单个 HTTP 200 canary 确定性失败必须保留内容异常')
assert.equal(oneFailed.item.score, 9, '任一确定性失败都必须至少失去一分')
assert.equal(oneFailed.item.maxScore, 10)
const oneFailedEvidence = oneFailed.item.evidenceSummary as Record<string, unknown>
assert.equal(oneFailedEvidence.observationCount, 21)
assert.equal(oneFailedEvidence.constraintPassedCount, 20)

const terminalFailure = await executeModelIdentityObservationProbes({
  model: 'gpt-5.6-sol',
  providerCode: 'openai',
  providerProtocolProfileId: 'openai_responses',
  baseUrl: 'https://example.invalid/v1',
  credentialMode: 'api_key',
  probeSetVersion: 'identity-observation-terminal-regression',
  runProbe: async (_request, itemKey) => ({
    traceId: `trace_${itemKey}`,
    statusCode: 503,
    success: false,
    durationMs: 1,
    bodyText: '',
    bodyTruncated: false,
    headers: {},
    errorMessage: 'upstream unavailable',
    attemptCount: 2,
    attemptStatusCodes: [503, 503],
    attemptTraceIds: [`trace_${itemKey}_1`, `trace_${itemKey}_2`]
  })
})

assert.equal(terminalFailure.item.status, 'skipped', '第二次 HTTP 非 200 必须保留为未验证')
assert.equal(terminalFailure.item.maxScore, 0, '网络失败不能进入内容评分分母')
const terminalEvidence = terminalFailure.item.evidenceSummary as Record<string, unknown>
assert.equal(terminalEvidence.requestFailure, true)
assert.equal(terminalEvidence.excludedFromScoring, true)

console.log('模型检测身份 observation 评分回归通过：HTTP 200 确定性失败扣分，非 200 证据不足不计分')

function successfulCanaryOutput(request: ModelCheckProbeRequest): string {
  const prompt = responsePrompt(request)
  const tag = matchRequired(prompt, /CANARY-[A-Z0-9]+/, 'tag')
  if (prompt.includes('result 等于')) {
    const [, left, right] = prompt.match(/等于 (\d+) \+ (\d+)/) ?? []
    return JSON.stringify({ result: Number(left) + Number(right), tag })
  }
  if (prompt.includes('过滤为大于')) return `values.filter((value) => value > 0).sort() // ${tag}`
  if (prompt.includes('中第二大值加')) {
    const [, valuesText, deltaText] = prompt.match(/largest 是 ([\d、]+) 中第二大值加 (-?\d+)/) ?? []
    const values = String(valuesText).split('、').map(Number).sort((left, right) => right - left)
    return JSON.stringify({ largest: (values[1] as number) + Number(deltaText), tag })
  }
  if (prompt.includes('中间结论错误地声称')) {
    const [, left, right] = prompt.match(/声称 (\d+)\+(\d+)=/) ?? []
    return JSON.stringify({ correct: Number(left) + Number(right), tag })
  }
  if (prompt.includes('"队列超时"')) return JSON.stringify({ zh: '队列超时', en: 'queue timeout', tag })
  if (prompt.includes('payload 必须含 ids 数组')) {
    const ids = matchRequired(prompt, /ids 数组 \[([\d,]+)\]/, 'ids').split(',').map(Number)
    return JSON.stringify({ action: 'inspect', payload: { ids, dryRun: true }, tag })
  }
  if (prompt.includes('封闭时间线')) return JSON.stringify({ version: 'B', tag })
  throw new Error(`未知 canary 提示：${prompt}`)
}

function responsePrompt(request: ModelCheckProbeRequest): string {
  const input = request.body.input
  if (!Array.isArray(input)) throw new Error('Responses canary 请求缺少 input')
  const firstMessage = input[0]
  if (!firstMessage || typeof firstMessage !== 'object') throw new Error('Responses canary 请求缺少首条消息')
  const content = (firstMessage as Record<string, unknown>).content
  if (!Array.isArray(content)) throw new Error('Responses canary 请求缺少 content')
  const firstPart = content[0]
  const text = firstPart && typeof firstPart === 'object' ? (firstPart as Record<string, unknown>).text : undefined
  if (typeof text !== 'string') throw new Error('Responses canary 请求缺少文本题面')
  return text
}

function matchRequired(value: string, expression: RegExp, label: string): string {
  const match = value.match(expression)
  const matchedValue = match?.[1] ?? match?.[0]
  if (!matchedValue) throw new Error(`canary 提示缺少 ${label}`)
  return matchedValue
}
