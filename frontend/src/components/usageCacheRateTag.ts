import { formatPercent } from '@/views/stats/statsFormatters'

// 账户列表"用量(日)"缓存率 tag 的展示常量与格式化 helper。
// 契约：docs/functions/缓存率感知调度与用量缓存率展示设计.md 第 9.2 节。
// cacheRate 是后端注入的分数口径占比（cache_read_tokens ÷ input_tokens），
// 前端不读 providerCode、不自行选分母、不算率；>100% 的异常值原样展示不截断。

export type CacheRateTone = 'green' | 'yellow' | 'red'

// 展示分级阈值：>= 90% green；80% <= r < 90% yellow；r < 80% red。
export function cacheRateTone(cacheRate: number): CacheRateTone {
  if (cacheRate >= 0.9) return 'green'
  if (cacheRate >= 0.8) return 'yellow'
  return 'red'
}

// formatPercent 期望百分点数值（toFixed(1)%），分数口径先乘 100；tag 只显示百分比本身。
export function cacheRateTagText(cacheRate: number): string {
  return formatPercent(cacheRate * 100)
}
