import { strict as assert } from 'node:assert'

import {
  analyzeTokenIntegritySamples,
  buildExactTokenPadding,
  countModelCheckInputTokens,
  type TokenIntegritySample
} from '../../modules/model-checks/model-checks-token-integrity.js'

const base = '受控 Token 完整性探针。只回复 OK。'
const padding512 = buildExactTokenPadding(512)
const padding2048 = buildExactTokenPadding(2048)
assert.equal(countModelCheckInputTokens(`${base}${padding512}`) - countModelCheckInputTokens(base), 512)
assert.equal(countModelCheckInputTokens(`${base}${padding2048}`) - countModelCheckInputTokens(base), 2048)

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

console.log('模型检测 Token 诚信回归通过：精确填充、斜率、截距、分桶和不支持边界符合预期')

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
