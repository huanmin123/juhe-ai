import {
  COMPACTION_DURATION_THRESHOLDS_MS,
  FIRST_TOKEN_THRESHOLDS_MS,
  NORMAL_DURATION_THRESHOLDS_MS,
  durationSeverity,
  firstTokenSeverity,
  usageRecordLatencyBarVars
} from '../../views/usage-records/usageRecordLatencyHealth'
import { formatUsageOutputTps, usageRecordIsCompaction } from '../../views/usage-records/usageRecordFormatters'

assertEqual(FIRST_TOKEN_THRESHOLDS_MS.warn, 10_000, '首字偏慢阈值应为 10s')
assertEqual(FIRST_TOKEN_THRESHOLDS_MS.slow, 30_000, '首字缓慢阈值应为 30s')
assertEqual(FIRST_TOKEN_THRESHOLDS_MS.critical, 60_000, '首字严重阈值应为 60s')
assertEqual(NORMAL_DURATION_THRESHOLDS_MS.warn, 30_000, '普通总耗时偏慢阈值应为 30s')
assertEqual(NORMAL_DURATION_THRESHOLDS_MS.slow, 60_000, '普通总耗时缓慢阈值应为 1min')
assertEqual(NORMAL_DURATION_THRESHOLDS_MS.critical, 120_000, '普通总耗时严重阈值应为 2min')
assertEqual(COMPACTION_DURATION_THRESHOLDS_MS.warn, 120_000, '压缩总耗时偏慢阈值应为 2min')
assertEqual(COMPACTION_DURATION_THRESHOLDS_MS.slow, 240_000, '压缩总耗时缓慢阈值应为 4min')
assertEqual(COMPACTION_DURATION_THRESHOLDS_MS.critical, 360_000, '压缩总耗时严重阈值应为 6min')

assertEqual(firstTokenSeverity(9_999), 'good', '首字 10s 内为 good')
assertEqual(firstTokenSeverity(10_000), 'warn', '首字达到 10s 归入 warn')
assertEqual(firstTokenSeverity(29_999), 'warn', '首字 30s 内保持 warn')
assertEqual(firstTokenSeverity(30_000), 'slow', '首字达到 30s 归入 slow')
assertEqual(firstTokenSeverity(59_999), 'slow', '首字 60s 内保持 slow')
assertEqual(firstTokenSeverity(60_000), 'critical', '首字达到 60s 归入 critical')
assertEqual(firstTokenSeverity(undefined), 'good', '缺失首字数据时按最稳档处理')

assertEqual(durationSeverity(29_999), 'good', '普通总耗时 30s 内为 good')
assertEqual(durationSeverity(30_000), 'warn', '普通总耗时达到 30s 归入 warn')
assertEqual(durationSeverity(59_999), 'warn', '普通总耗时 1min 内保持 warn')
assertEqual(durationSeverity(60_000), 'slow', '普通总耗时达到 1min 归入 slow')
assertEqual(durationSeverity(119_999), 'slow', '普通总耗时 2min 内保持 slow')
assertEqual(durationSeverity(120_000), 'critical', '普通总耗时达到 2min 归入 critical')

assertEqual(durationSeverity(119_999, true), 'good', '压缩总耗时 2min 内为 good')
assertEqual(durationSeverity(120_000, true), 'warn', '压缩总耗时达到 2min 归入 warn')
assertEqual(durationSeverity(239_999, true), 'warn', '压缩总耗时 4min 内保持 warn')
assertEqual(durationSeverity(240_000, true), 'slow', '压缩总耗时达到 4min 归入 slow')
assertEqual(durationSeverity(359_999, true), 'slow', '压缩总耗时 6min 内保持 slow')
assertEqual(durationSeverity(360_000, true), 'critical', '压缩总耗时达到 6min 归入 critical')
assertEqual(durationSeverity(undefined), 'good', '缺失总耗时数据时按最稳档处理')

assertDeepEqual(usageRecordLatencyBarVars({ firstTokenMs: 45_000, durationMs: 120_000 }), {
  '--latency-bar-from': 'var(--juhe-latency-slow)',
  '--latency-bar-to': 'var(--juhe-latency-critical)'
}, '渐变条上端随首字档、下端随普通总耗时档')
assertDeepEqual(usageRecordLatencyBarVars({ firstTokenMs: 1_000, durationMs: 2_000 }), {
  '--latency-bar-from': 'var(--juhe-latency-good)',
  '--latency-bar-to': 'var(--juhe-latency-good)'
}, '双 good 记录的渐变条为纯绿')
assertDeepEqual(usageRecordLatencyBarVars({ durationMs: 400_000 }), {
  '--latency-bar-from': 'var(--juhe-latency-critical)',
  '--latency-bar-to': 'var(--juhe-latency-critical)'
}, '无首字数据的渐变条上下同色退化为总耗时档纯色')
assertDeepEqual(usageRecordLatencyBarVars({ firstTokenMs: 1_000, durationMs: 150_000 }, true), {
  '--latency-bar-from': 'var(--juhe-latency-compaction-light)',
  '--latency-bar-to': 'var(--juhe-latency-compaction-warn)'
}, '压缩记录首字不看档，渐变条为浅紫顶 + warn 档紫底')
assertDeepEqual(usageRecordLatencyBarVars({ firstTokenMs: 88_000, durationMs: 380_000 }, true), {
  '--latency-bar-from': 'var(--juhe-latency-compaction-light)',
  '--latency-bar-to': 'var(--juhe-latency-compaction-critical)'
}, '压缩总耗时越久紫底越深（critical 最深）')

assertEqual(formatUsageOutputTps({ outputTokens: 4018, durationMs: 38_540 }), '104.3 t/s', '输出 TPS 按输出 Token 除以完整耗时并固定一位小数')
assertEqual(formatUsageOutputTps({ outputTokens: 250, durationMs: 1250 }), '200.0 t/s', '常规记录的输出 TPS 保留一位小数')
assertEqual(formatUsageOutputTps({ durationMs: 1250 }), '—', '输出 Token 缺失时输出 TPS 显示占位符')
assertEqual(formatUsageOutputTps({ outputTokens: 0, durationMs: 1250 }), '—', '输出 Token 为 0 时输出 TPS 显示占位符')
assertEqual(formatUsageOutputTps({ outputTokens: 250 }), '—', '总耗时缺失时输出 TPS 显示占位符')
assertEqual(formatUsageOutputTps({ outputTokens: 250, durationMs: 0 }), '—', '总耗时为 0 时输出 TPS 显示占位符')
assertEqual(formatUsageOutputTps({ outputTokens: 250, durationMs: 1250, inputAudioTokens: 11 }), '—', '媒体计量记录不计算输出 TPS')

assertEqual(usageRecordIsCompaction({ compactionExpected: false, endpoint: 'POST /v1/responses/compact' }), true, '字段为 false 的存量行按显式压缩路径回退识别')
assertEqual(usageRecordIsCompaction({ compactionExpected: false, endpoint: 'POST /backendapi/responses/compact' }), true, '带前缀的压缩路径同样回退识别')
assertEqual(usageRecordIsCompaction({ compactionExpected: true, endpoint: '' }), true, 'compactionExpected 字段为 true 时不依赖 endpoint 直接识别')
assertEqual(usageRecordIsCompaction({ compactionExpected: false, endpoint: 'POST /v1/responses' }), false, '普通 responses 请求不按压缩识别')
assertEqual(usageRecordIsCompaction({ compactionExpected: false, endpoint: 'GET /v1/responses/compact' }), false, '非 POST 方法不按压缩识别')
assertEqual(usageRecordIsCompaction({ compactionExpected: true, endpoint: 'POST /v1/responses' }), true, '字段命中时不再依赖路径推断，普通 responses 路径也按压缩识别')
assertEqual(usageRecordIsCompaction({ compactionExpected: false }), false, '缺失端点且字段为 false 的记录不按压缩识别')

console.log('使用记录延迟健康与输出吞吐回归通过：两套分档阈值、压缩色条退化、渐变变量、TPS 门槛与字段优先压缩识别契约均符合设计')

function assertEqual(actual: unknown, expected: unknown, message: string): void {
  if (actual !== expected) {
    throw new Error(`${message}：实际 ${String(actual)}，期望 ${String(expected)}`)
  }
}

function assertDeepEqual(actual: unknown, expected: unknown, message: string): void {
  const actualText = JSON.stringify(actual)
  const expectedText = JSON.stringify(expected)
  if (actualText !== expectedText) {
    throw new Error(`${message}：实际 ${actualText}，期望 ${expectedText}`)
  }
}
