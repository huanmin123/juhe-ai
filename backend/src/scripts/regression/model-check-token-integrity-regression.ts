import { strict as assert } from 'node:assert'

import {
  analyzeTokenIntegritySamples,
  buildExactTokenPadding,
  countModelCheckInputTokens,
  modelCheckTokenPaddingMaxTokens,
  type TokenIntegritySample
} from '../../modules/model-checks/model-checks-token-integrity.js'
import { executeModelCheckTokenIntegrityProbes } from '../../modules/model-checks/model-checks-token-probes.js'
import {
  getModelCheckTokenWorkerRuntime,
  prepareModelCheckTokenProbePromptInWorker,
  stopModelCheckTokenWorker
} from '../../modules/model-checks/model-checks-token-worker.service.js'

const base = '受控 Token 完整性探针。只回复 OK。'
const padding512 = buildExactTokenPadding(512)
const padding2048 = buildExactTokenPadding(2048)
assert.equal(countModelCheckInputTokens(`${base}${padding512}`) - countModelCheckInputTokens(base), 512)
assert.equal(countModelCheckInputTokens(`${base}${padding2048}`) - countModelCheckInputTokens(base), 2048)
assert.throws(() => buildExactTokenPadding(modelCheckTokenPaddingMaxTokens + 1), /0 到 2048/, '填充目标必须有硬上限')
assert.throws(() => buildExactTokenPadding(1.5), /必须是整数/, '填充目标不能被静默截断')

try {
  let eventLoopTurnObserved = false
  const eventLoopTurn = new Promise<void>((resolve) => setImmediate(() => {
    eventLoopTurnObserved = true
    resolve()
  }))
  const preparedPromise = prepareModelCheckTokenProbePromptInWorker(base, modelCheckTokenPaddingMaxTokens)
  const [prepared] = await Promise.all([preparedPromise, eventLoopTurn])
  assert(eventLoopTurnObserved, '精确填充计算期间主事件循环必须可继续运行')
  assert.notEqual(prepared.workerPid, process.pid, '精确填充必须在有界 worker 子进程中执行')
  assert.equal(countModelCheckInputTokens(prepared.prompt) - countModelCheckInputTokens(base), modelCheckTokenPaddingMaxTokens)
  assert.equal(getModelCheckTokenWorkerRuntime().handledJobs, 1)
} finally {
  await stopModelCheckTokenWorker()
}

const honest = samples((local) => local + 17)
const honestResult = analyzeTokenIntegritySamples(honest)
assert.equal(honestResult.status, 'consistent')
assert(Math.abs(honestResult.slope - 1) < 0.001)
assert(Math.abs(honestResult.intercept - 17) < 0.001)

const padded = analyzeTokenIntegritySamples(samples((local) => Math.round(local * 1.1) + 9))
assert.equal(padded.status, 'suspected_padding')
assert(padded.reasonCodes.includes('proportional_padding'))

const calibrationBoundary = analyzeTokenIntegritySamples(samples((local) => Math.round(local * 1.05) + 9))
assert.equal(calibrationBoundary.status, 'warning', '5% 校准边界不能在未观察真实样本前直接强判')

const fixed = analyzeTokenIntegritySamples(samples((local) => local + 120))
assert.equal(fixed.status, 'consistent', '固定开销没有 cohort 基线时不能强判')
assert(Math.abs(fixed.intercept - 120) < 0.001)

const rounded = analyzeTokenIntegritySamples(samples((local) => Math.ceil((local + 7) / 64) * 64))
assert.equal(rounded.status, 'warning')
assert(rounded.reasonCodes.includes('bucket_rounding'))

const missing = analyzeTokenIntegritySamples(samples(() => undefined))
assert.equal(missing.status, 'unsupported')
assert(missing.reasonCodes.includes('reported_usage_missing'))

const constant = analyzeTokenIntegritySamples(samples(() => 8))
assert.equal(constant.status, 'unsupported')
assert(constant.reasonCodes.includes('reported_usage_incompatible'))

try {
  const consistentProbe = await executeModelCheckTokenIntegrityProbes({
    model: 'gpt-5.6-sol',
    providerCode: 'openai',
    providerProtocolProfileId: 'openai_responses',
    baseUrl: 'https://example.invalid/v1',
    credentialMode: 'api_key',
    probeSetVersion: 'token-integrity-consistent-score-regression',
    profileMode: 'full',
    observationEnabled: false,
    runProbe: async (request, itemKey) => tokenProbeResult(request, itemKey, (localInputTokens) => localInputTokens + 9)
  })
  assert.equal(consistentProbe.item.status, 'passed', 'Token 一致性必须形成通过项')
  assert.equal(consistentProbe.item.score, 10, 'Token 一致性必须贡献正向分数')
  assert.equal(consistentProbe.item.maxScore, 10)
  assert.equal((consistentProbe.item.evidenceSummary as { diagnosticOnly?: unknown }).diagnosticOnly, false)

  const paddedProbe = await executeModelCheckTokenIntegrityProbes({
    model: 'gpt-5.6-sol',
    providerCode: 'openai',
    providerProtocolProfileId: 'openai_responses',
    baseUrl: 'https://example.invalid/v1',
    credentialMode: 'api_key',
    probeSetVersion: 'token-integrity-score-regression',
    profileMode: 'full',
    observationEnabled: false,
    runProbe: async (request, itemKey) => tokenProbeResult(request, itemKey, (localInputTokens) => Math.round(localInputTokens * 1.1) + 9)
  })
  assert.equal(paddedProbe.item.status, 'failed', '已判定比例灌水必须标记失败')
  assert.equal(paddedProbe.item.score, 0, '已判定比例灌水必须失去 Token 诚信分')
  assert.equal(paddedProbe.item.maxScore, 10, '已判定比例灌水必须进入评分分母')
  const paddedProbeEvidence = paddedProbe.item.evidenceSummary as { diagnosticOnly?: unknown }
  assert.equal(paddedProbeEvidence.diagnosticOnly, false, '已判定比例灌水不能保持仅诊断')
} finally {
  await stopModelCheckTokenWorker()
}

console.log('模型检测 Token 诚信回归通过：精确填充、斜率、截距、分桶、不支持边界和已判定灌水扣分符合预期')

function samples(reported: (local: number) => number | undefined): TokenIntegritySample[] {
  const result: TokenIntegritySample[] = []
  for (let round = 0; round < 3; round += 1) {
    for (const paddingTokens of [0, 512, 2048]) {
      const localInputTokens = 100 + paddingTokens + round
      result.push({
        roundIndex: round,
        paddingTokens,
        localInputTokens,
        reportedInputTokens: reported(localInputTokens)
      })
    }
  }
  return result
}

function tokenProbeResult(
  request: { body: Record<string, unknown> },
  itemKey: string,
  reportedInputTokens: (localInputTokens: number) => number
) {
  const input = Array.isArray(request.body.input) ? request.body.input[0] : undefined
  const content = input && typeof input === 'object' && !Array.isArray(input)
    ? (input as { content?: unknown }).content
    : undefined
  const inputText = Array.isArray(content) && content[0] && typeof content[0] === 'object' && !Array.isArray(content[0])
    ? String((content[0] as { text?: unknown }).text ?? '')
    : ''
  const localInputTokens = countModelCheckInputTokens(inputText)
  return {
    traceId: `trace_${itemKey}`,
    statusCode: 200,
    success: true,
    durationMs: 1,
    bodyText: '',
    bodyTruncated: false,
    headers: {},
    outputText: 'OK',
    model: 'gpt-5.6-sol',
    usage: { input_tokens: reportedInputTokens(localInputTokens), output_tokens: 1 }
  }
}
