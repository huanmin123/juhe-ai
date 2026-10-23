/**
 * 使用记录延迟健康度分档（契约见
 * docs/functions/使用记录延迟健康分档与输出吞吐展示设计.md）。
 *
 * 首字（TTFT）：10s 内正常，10-30s 偏慢，30-60s 缓慢，60s 及以上严重。
 * 总耗时（普通）：大于 30s 即偏慢，60s 缓慢，2min 及以上严重；「慢但吞吐正常」
 * 的长输出失真由同格展示的输出 TPS 兜底区分。
 * 总耗时（压缩）：压缩请求需先对超大上下文做预填充，天然长耗时，放宽为
 * 2min / 4min / 6min，且首字不参与分档。
 */
export type LatencySeverity = 'good' | 'warn' | 'slow' | 'critical'

export const FIRST_TOKEN_THRESHOLDS_MS = {
  warn: 10_000,
  slow: 30_000,
  critical: 60_000
} as const

export const NORMAL_DURATION_THRESHOLDS_MS = {
  warn: 30_000,
  slow: 60_000,
  critical: 120_000
} as const

export const COMPACTION_DURATION_THRESHOLDS_MS = {
  warn: 120_000,
  slow: 240_000,
  critical: 360_000
} as const

interface SeverityThresholds {
  warn: number
  slow: number
  critical: number
}

function classifySeverity(ms: number, thresholds: SeverityThresholds): LatencySeverity {
  if (ms >= thresholds.critical) return 'critical'
  if (ms >= thresholds.slow) return 'slow'
  if (ms >= thresholds.warn) return 'warn'
  return 'good'
}

export function firstTokenSeverity(ms?: number): LatencySeverity {
  return classifySeverity(numberValue(ms) ?? 0, FIRST_TOKEN_THRESHOLDS_MS)
}

export function durationSeverity(ms?: number, compaction = false): LatencySeverity {
  return classifySeverity(numberValue(ms) ?? 0, compaction ? COMPACTION_DURATION_THRESHOLDS_MS : NORMAL_DURATION_THRESHOLDS_MS)
}

/**
 * 档位 → 渐变色条颜色（`--juhe-latency-*`，对照 sub2api 面板口径的明亮色阶；
 * 数值文字用 `--juhe-latency-*-text`，由组件 CSS 的 .latency-sev-* 承担）。
 */
export const LATENCY_SEVERITY_COLOR_VARS: Record<LatencySeverity, string> = {
  good: 'var(--juhe-latency-good)',
  warn: 'var(--juhe-latency-warn)',
  slow: 'var(--juhe-latency-slow)',
  critical: 'var(--juhe-latency-critical)'
}

/** 压缩记录专用紫色色阶：底色随压缩总耗时档加深，顶色固定浅紫（首字不参与判读）。 */
export const LATENCY_COMPACTION_BAR_FROM_VAR = 'var(--juhe-latency-compaction-light)'

export const LATENCY_COMPACTION_BAR_TO_VARS: Record<LatencySeverity, string> = {
  good: 'var(--juhe-latency-compaction-good)',
  warn: 'var(--juhe-latency-compaction-warn)',
  slow: 'var(--juhe-latency-compaction-slow)',
  critical: 'var(--juhe-latency-compaction-critical)'
}

/**
 * 延迟列渐变条的上下端颜色变量：普通记录上端随首字档、下端随总耗时档；
 * 无首字数据时上下同色退化为总耗时档纯色条；
 * 压缩记录首字不看档，条为「固定浅紫顶 → 压缩总耗时档紫底」的渐变，
 * 紫色本身即压缩标记（不再使用模型列 tag）。
 */
export function usageRecordLatencyBarVars(
  record: Pick<UsageRecordLatencyFields, 'firstTokenMs' | 'durationMs'>,
  compaction = false
): Record<string, string> {
  const firstTokenMs = numberValue(record.firstTokenMs)
  if (compaction) {
    return {
      '--latency-bar-from': LATENCY_COMPACTION_BAR_FROM_VAR,
      '--latency-bar-to': LATENCY_COMPACTION_BAR_TO_VARS[durationSeverity(record.durationMs, true)]
    }
  }
  const firstTokenColor = firstTokenMs != null
    ? LATENCY_SEVERITY_COLOR_VARS[firstTokenSeverity(firstTokenMs)]
    : LATENCY_SEVERITY_COLOR_VARS[durationSeverity(record.durationMs)]
  return {
    '--latency-bar-from': firstTokenColor,
    '--latency-bar-to': LATENCY_SEVERITY_COLOR_VARS[durationSeverity(record.durationMs)]
  }
}

export interface UsageRecordLatencyFields {
  firstTokenMs?: number
  durationMs?: number
}

function numberValue(value: unknown): number | undefined {
  const numericValue = typeof value === 'string' ? Number(value.trim()) : value
  return typeof numericValue === 'number' && Number.isFinite(numericValue) ? numericValue : undefined
}
