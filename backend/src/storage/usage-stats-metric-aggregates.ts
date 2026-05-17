import { numberFromUnknown } from './usage-stats-helpers.js'
import { trendBucketKey } from './usage-stats-window-helpers.js'

export function aggregateSystemMetricsRows(rows: Array<Record<string, unknown>>, bucketHours: number): Array<Record<string, unknown>> {
  const buckets = new Map<string, Record<string, unknown>>()
  for (const row of rows) {
    const key = trendBucketKey(String(row.stat_hour ?? ''), bucketHours)
    const bucket = buckets.get(key) ?? { stat_hour: key, sample_count: 0 }
    addMetric(bucket, row, 'sample_count')
    addMetric(bucket, row, 'cpu_percent_sum')
    maxMetric(bucket, row, 'cpu_percent_max')
    addMetric(bucket, row, 'memory_used_percent_sum')
    maxMetric(bucket, row, 'memory_used_percent_max')
    addMetric(bucket, row, 'process_rss_bytes_sum')
    maxMetric(bucket, row, 'process_rss_bytes_max')
    addMetric(bucket, row, 'process_heap_used_bytes_sum')
    maxMetric(bucket, row, 'process_heap_used_bytes_max')
    addMetric(bucket, row, 'event_loop_lag_ms_sum')
    maxMetric(bucket, row, 'event_loop_lag_ms_max')
    addMetric(bucket, row, 'network_rx_bytes_per_sec_sum')
    maxMetric(bucket, row, 'network_rx_bytes_per_sec_max')
    addMetric(bucket, row, 'network_rx_bytes_per_sec_count')
    addMetric(bucket, row, 'network_tx_bytes_per_sec_sum')
    maxMetric(bucket, row, 'network_tx_bytes_per_sec_max')
    addMetric(bucket, row, 'network_tx_bytes_per_sec_count')
    maxMetric(bucket, row, 'network_rx_total_bytes_max')
    maxMetric(bucket, row, 'network_tx_total_bytes_max')
    maxMetric(bucket, row, 'db_file_bytes_max')
    maxMetric(bucket, row, 'stats_lag_seconds_max')
    buckets.set(key, bucket)
  }
  return [...buckets.values()]
}

export function nullableNumber(value: unknown): number | null {
  return numberFromUnknown(value) ?? null
}

function addMetric(target: Record<string, unknown>, source: Record<string, unknown>, key: string): void {
  target[key] = Number(target[key] ?? 0) + Number(source[key] ?? 0)
}

function maxMetric(target: Record<string, unknown>, source: Record<string, unknown>, key: string): void {
  const value = numberFromUnknown(source[key])
  if (value === undefined) return
  const current = numberFromUnknown(target[key])
  target[key] = current === undefined ? value : Math.max(current, value)
}
