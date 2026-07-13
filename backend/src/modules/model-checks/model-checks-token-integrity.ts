import { getEncoding } from 'js-tiktoken'

export const modelCheckTokenizerVersion = 'js-tiktoken@1.0.21:o200k_base'
export const modelCheckTokenProbeVersion = 'token-integrity-v1'

let encoding: ReturnType<typeof getEncoding> | undefined

export type TokenIntegrityStatus = 'consistent' | 'warning' | 'suspected_padding' | 'unsupported' | 'insufficient_evidence'

export interface TokenIntegritySample {
  roundIndex: number
  paddingTokens: number
  localInputTokens: number
  reportedInputTokens?: number
  cachedInputTokens?: number
}

export interface TokenIntegrityAnalysis {
  status: TokenIntegrityStatus
  slope: number
  intercept: number
  slopeConfidenceLow: number
  slopeConfidenceHigh: number
  sampleCount: number
  roundCount: number
  reasonCodes: string[]
}

export function countModelCheckInputTokens(value: string): number {
  encoding ??= getEncoding('o200k_base')
  return encoding.encode(value).length
}

export function buildExactTokenPadding(targetTokens: number, prefix = ''): string {
  const target = Math.max(0, Math.trunc(targetTokens))
  if (target === 0) return ''
  const prefixTokens = countModelCheckInputTokens(prefix)
  let padding = ''
  const unit = ' token-integrity-probe'
  while (countModelCheckInputTokens(prefix + padding) - prefixTokens < target) {
    padding += unit
  }
  while (countModelCheckInputTokens(prefix + padding) - prefixTokens > target) {
    padding = padding.slice(0, -1)
  }
  if (countModelCheckInputTokens(prefix + padding) - prefixTokens !== target) {
    throw new Error(`无法构造 ${target} Token 的精确受控填充块`)
  }
  return padding
}

export function analyzeTokenIntegritySamples(samples: TokenIntegritySample[]): TokenIntegrityAnalysis {
  const valid = samples.filter((sample): sample is TokenIntegritySample & { reportedInputTokens: number } => (
    Number.isFinite(sample.localInputTokens)
    && typeof sample.reportedInputTokens === 'number'
    && Number.isFinite(sample.reportedInputTokens)
  ))
  const roundCount = new Set(valid.map((sample) => sample.roundIndex)).size
  if (valid.length < 6 || roundCount < 3) {
    return emptyAnalysis(valid.length, roundCount, 'reported_usage_missing')
  }
  const regression = linearRegression(valid.map((sample) => ({ x: sample.localInputTokens, y: sample.reportedInputTokens })))
  if (!Number.isFinite(regression.slope) || regression.slope <= 0.1) {
    return emptyAnalysis(valid.length, roundCount, 'reported_usage_incompatible')
  }
  const slopeDistance = Math.abs(regression.slope - 1)
  const bucketRounding = detectsBucketRounding(valid)
  const reasonCodes: string[] = []
  let status: TokenIntegrityStatus = 'consistent'
  if (slopeDistance > 0.05 && (regression.confidenceLow > 1 || regression.confidenceHigh < 1)) {
    status = 'suspected_padding'
    reasonCodes.push('proportional_padding')
  } else if (slopeDistance > 0.03) {
    status = 'warning'
    reasonCodes.push('slope_warning')
  }
  if (bucketRounding) {
    if (status === 'consistent') status = 'warning'
    reasonCodes.push('bucket_rounding')
  }
  return {
    status,
    slope: rounded(regression.slope),
    intercept: rounded(regression.intercept),
    slopeConfidenceLow: rounded(regression.confidenceLow),
    slopeConfidenceHigh: rounded(regression.confidenceHigh),
    sampleCount: valid.length,
    roundCount,
    reasonCodes
  }
}

function linearRegression(points: Array<{ x: number; y: number }>): { slope: number; intercept: number; confidenceLow: number; confidenceHigh: number } {
  const count = points.length
  const meanX = points.reduce((sum, point) => sum + point.x, 0) / count
  const meanY = points.reduce((sum, point) => sum + point.y, 0) / count
  const ssX = points.reduce((sum, point) => sum + ((point.x - meanX) ** 2), 0)
  const covariance = points.reduce((sum, point) => sum + ((point.x - meanX) * (point.y - meanY)), 0)
  const slope = ssX > 0 ? covariance / ssX : Number.NaN
  const intercept = meanY - (slope * meanX)
  const residualSum = points.reduce((sum, point) => sum + ((point.y - (intercept + slope * point.x)) ** 2), 0)
  const standardError = count > 2 && ssX > 0 ? Math.sqrt((residualSum / (count - 2)) / ssX) : Number.POSITIVE_INFINITY
  const margin = 1.96 * standardError
  return { slope, intercept, confidenceLow: slope - margin, confidenceHigh: slope + margin }
}

function detectsBucketRounding(samples: Array<TokenIntegritySample & { reportedInputTokens: number }>): boolean {
  const nonBase = samples.filter((sample) => sample.paddingTokens > 0)
  if (nonBase.length < 4) return false
  const aligned = nonBase.filter((sample) => sample.reportedInputTokens % 64 === 0).length
  return aligned / nonBase.length >= 0.8
}

function emptyAnalysis(sampleCount: number, roundCount: number, reasonCode: string): TokenIntegrityAnalysis {
  return {
    status: 'unsupported',
    slope: 0,
    intercept: 0,
    slopeConfidenceLow: 0,
    slopeConfidenceHigh: 0,
    sampleCount,
    roundCount,
    reasonCodes: [reasonCode]
  }
}

function rounded(value: number): number {
  return Number.isFinite(value) ? Number(value.toFixed(6)) : value
}
