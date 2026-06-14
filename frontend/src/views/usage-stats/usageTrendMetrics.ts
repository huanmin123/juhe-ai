export type UsageTrendMetric = 'cost' | 'tokens' | 'requests'

export function metricText(metric: UsageTrendMetric): string {
  if (metric === 'cost') return '成本'
  if (metric === 'tokens') return 'Token'
  return '请求'
}

export function metricValue(point: { requestCount: number; totalTokens: number; totalCost: number }, metric: UsageTrendMetric): number {
  if (metric === 'cost') return point.totalCost
  if (metric === 'tokens') return point.totalTokens
  return point.requestCount
}
