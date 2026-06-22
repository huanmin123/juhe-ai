import { getStatsDatabase, nowIso } from '../../../../storage/database.js'
import { hourKey, usageStatsTimezone } from '../../../../storage/usage-stats-helpers.js'
import {
  dayMs,
  emptyMetricRow,
  idPrefix,
  minuteMs,
  numeric,
  roundNumber,
  type AccountMetricRow,
  type MockdataOptions,
  type ProcessMetricRow
} from '../shared.js'

type StatsDatabase = ReturnType<typeof getStatsDatabase>

export function createMonitoringMockdata(options: MockdataOptions): void {
  const database = getStatsDatabase()
  const now = Date.now() - 10 * minuteMs
  const start = now - (options.days * dayMs)
  const insertMetric = database.prepare(`
    INSERT INTO system_metrics_samples (
      id, sampled_at, cpu_percent, memory_used_percent, memory_total_bytes, memory_free_bytes,
      process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes, event_loop_lag_ms,
      network_rx_bytes_per_sec, network_tx_bytes_per_sec, network_rx_total_bytes, network_tx_total_bytes,
      db_file_bytes, stats_lag_seconds, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const insertProcess = database.prepare(`
    INSERT INTO process_event_loop_samples (
      id, sampled_at, process_role, process_pid, event_loop_lag_ms,
      process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes,
      process_external_bytes, process_array_buffers_bytes, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const roles = ['server', 'worker', 'metrics-worker', 'ingest-worker', 'stats-worker', 'snapshot-worker', 'probe-worker', 'maintenance-worker', 'temporary-maintenance-worker', 'db-service'] as const
  let metricIndex = 0
  database.exec('BEGIN')
  try {
    for (let timestamp = start; timestamp <= now; timestamp += 60 * minuteMs) {
      const sampledAt = new Date(timestamp).toISOString()
      const wave = Math.sin(metricIndex / 9)
      const cpu = 18 + wave * 8 + (metricIndex % 11)
      const memoryPercent = 46 + Math.cos(metricIndex / 13) * 9
      insertMetric.run(
        `${idPrefix}metric_${String(metricIndex + 1).padStart(5, '0')}`,
        sampledAt,
        roundNumber(cpu, 2),
        roundNumber(memoryPercent, 2),
        16 * 1024 * 1024 * 1024,
        Math.floor((100 - memoryPercent) / 100 * 16 * 1024 * 1024 * 1024),
        420 * 1024 * 1024 + metricIndex * 2048,
        130 * 1024 * 1024 + metricIndex * 1024,
        256 * 1024 * 1024,
        roundNumber(5 + Math.abs(Math.sin(metricIndex / 5)) * 18, 2),
        roundNumber(1024 * (20 + (metricIndex % 30)), 2),
        roundNumber(1024 * (14 + (metricIndex % 24)), 2),
        2_000_000_000 + metricIndex * 1024 * 20,
        1_400_000_000 + metricIndex * 1024 * 14,
        180 * 1024 * 1024 + metricIndex * 2048,
        metricIndex % 19 === 0 ? 30 + metricIndex % 120 : 0,
        sampledAt
      )
      roles.forEach((role, roleIndex) => {
        const rssBytes = (180 + roleIndex * 24) * 1024 * 1024 + metricIndex * (roleIndex + 1) * 512
        const heapUsedBytes = (58 + roleIndex * 7) * 1024 * 1024 + metricIndex * (roleIndex + 1) * 256
        insertProcess.run(
          `${idPrefix}process_metric_${String(metricIndex + 1).padStart(5, '0')}_${role}`,
          sampledAt,
          role,
          31000 + roleIndex,
          roundNumber(2 + Math.abs(Math.sin((metricIndex + roleIndex) / 4)) * (roleIndex + 3), 2),
          rssBytes,
          heapUsedBytes,
          Math.max(heapUsedBytes, (128 + roleIndex * 8) * 1024 * 1024),
          (20 + roleIndex * 2) * 1024 * 1024,
          (8 + roleIndex) * 1024 * 1024,
          sampledAt
        )
      })
      metricIndex += 1
    }
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
  rebuildSystemMetricsHourly(database)
  rebuildProcessEventLoopHourly(database)
}

function rebuildSystemMetricsHourly(database: StatsDatabase): void {
  const timezone = usageStatsTimezone()
  const rows = database.prepare('SELECT * FROM system_metrics_samples ORDER BY sampled_at ASC, id ASC').all() as unknown as Array<Record<string, unknown>>
  const buckets = new Map<string, AccountMetricRow>()
  for (const row of rows) {
    const statHour = hourKey(new Date(String(row.sampled_at)), timezone)
    const bucket = buckets.get(statHour) ?? emptyMetricRow()
    bucket.sample_count += 1
    addMetric(bucket, 'cpu_percent', row.cpu_percent)
    addMetric(bucket, 'memory_used_percent', row.memory_used_percent)
    addMetric(bucket, 'process_rss_bytes', row.process_rss_bytes)
    addMetric(bucket, 'process_heap_used_bytes', row.process_heap_used_bytes)
    addMetric(bucket, 'event_loop_lag_ms', row.event_loop_lag_ms)
    addMetric(bucket, 'network_rx_bytes_per_sec', row.network_rx_bytes_per_sec, true)
    addMetric(bucket, 'network_tx_bytes_per_sec', row.network_tx_bytes_per_sec, true)
    maxMetric(bucket, 'network_rx_total_bytes', row.network_rx_total_bytes)
    maxMetric(bucket, 'network_tx_total_bytes', row.network_tx_total_bytes)
    maxMetric(bucket, 'db_file_bytes', row.db_file_bytes)
    maxMetric(bucket, 'stats_lag_seconds', row.stats_lag_seconds)
    buckets.set(statHour, bucket)
  }
  database.exec('BEGIN')
  try {
    database.prepare('DELETE FROM system_metrics_hourly').run()
    const insert = database.prepare(`
      INSERT INTO system_metrics_hourly (
        stat_hour, sample_count, cpu_percent_sum, cpu_percent_max, memory_used_percent_sum,
        memory_used_percent_max, process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum,
        process_heap_used_bytes_max, event_loop_lag_ms_sum, event_loop_lag_ms_max,
        network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_max, network_rx_bytes_per_sec_count,
        network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_max, network_tx_bytes_per_sec_count,
        network_rx_total_bytes_max, network_tx_total_bytes_max, db_file_bytes_max, stats_lag_seconds_max, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    const updatedAt = nowIso()
    for (const [statHour, bucket] of buckets) {
      insert.run(
        statHour,
        bucket.sample_count,
        bucket.cpu_percent_sum,
        bucket.cpu_percent_max,
        bucket.memory_used_percent_sum,
        bucket.memory_used_percent_max,
        bucket.process_rss_bytes_sum,
        bucket.process_rss_bytes_max,
        bucket.process_heap_used_bytes_sum,
        bucket.process_heap_used_bytes_max,
        bucket.event_loop_lag_ms_sum,
        bucket.event_loop_lag_ms_max,
        bucket.network_rx_bytes_per_sec_sum,
        bucket.network_rx_bytes_per_sec_max,
        bucket.network_rx_bytes_per_sec_count,
        bucket.network_tx_bytes_per_sec_sum,
        bucket.network_tx_bytes_per_sec_max,
        bucket.network_tx_bytes_per_sec_count,
        bucket.network_rx_total_bytes_max,
        bucket.network_tx_total_bytes_max,
        bucket.db_file_bytes_max,
        bucket.stats_lag_seconds_max,
        updatedAt
      )
    }
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
}

function rebuildProcessEventLoopHourly(database: StatsDatabase): void {
  const timezone = usageStatsTimezone()
  const rows = database.prepare('SELECT * FROM process_event_loop_samples ORDER BY sampled_at ASC, id ASC').all() as unknown as Array<Record<string, unknown>>
  const buckets = new Map<string, ProcessMetricRow>()
  for (const row of rows) {
    const statHour = hourKey(new Date(String(row.sampled_at)), timezone)
    const processRole = String(row.process_role ?? '')
    if (!processRole) continue
    const key = `${statHour}:${processRole}`
    const bucket = buckets.get(key) ?? {
      sample_count: 0,
      event_loop_lag_ms_sum: 0,
      event_loop_lag_ms_count: 0,
      event_loop_lag_ms_max: null,
      process_rss_bytes_sum: 0,
      process_rss_bytes_max: null,
      process_heap_used_bytes_sum: 0,
      process_heap_used_bytes_max: null,
      process_heap_total_bytes_sum: 0,
      process_heap_total_bytes_max: null,
      process_external_bytes_sum: 0,
      process_external_bytes_max: null,
      process_array_buffers_bytes_sum: 0,
      process_array_buffers_bytes_max: null
    }
    bucket.sample_count += 1
    const lag = numeric(row.event_loop_lag_ms)
    if (lag !== undefined) {
      bucket.event_loop_lag_ms_count += 1
      bucket.event_loop_lag_ms_sum += lag
      bucket.event_loop_lag_ms_max = bucket.event_loop_lag_ms_max === null ? lag : Math.max(bucket.event_loop_lag_ms_max, lag)
    }
    addProcessMetric(bucket, 'process_rss_bytes', row.process_rss_bytes)
    addProcessMetric(bucket, 'process_heap_used_bytes', row.process_heap_used_bytes)
    addProcessMetric(bucket, 'process_heap_total_bytes', row.process_heap_total_bytes)
    addProcessMetric(bucket, 'process_external_bytes', row.process_external_bytes)
    addProcessMetric(bucket, 'process_array_buffers_bytes', row.process_array_buffers_bytes)
    buckets.set(key, bucket)
  }
  database.exec('BEGIN')
  try {
    database.prepare('DELETE FROM process_event_loop_hourly').run()
    const insert = database.prepare(`
      INSERT INTO process_event_loop_hourly (
        stat_hour, process_role, sample_count, event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max,
        process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum, process_heap_used_bytes_max,
        process_heap_total_bytes_sum, process_heap_total_bytes_max, process_external_bytes_sum, process_external_bytes_max,
        process_array_buffers_bytes_sum, process_array_buffers_bytes_max, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    const updatedAt = nowIso()
    for (const [key, bucket] of buckets) {
      const [statHour, processRole] = key.split(':')
      insert.run(
        statHour,
        processRole,
        bucket.sample_count,
        bucket.event_loop_lag_ms_sum,
        bucket.event_loop_lag_ms_count,
        bucket.event_loop_lag_ms_max,
        bucket.process_rss_bytes_sum,
        bucket.process_rss_bytes_max,
        bucket.process_heap_used_bytes_sum,
        bucket.process_heap_used_bytes_max,
        bucket.process_heap_total_bytes_sum,
        bucket.process_heap_total_bytes_max,
        bucket.process_external_bytes_sum,
        bucket.process_external_bytes_max,
        bucket.process_array_buffers_bytes_sum,
        bucket.process_array_buffers_bytes_max,
        updatedAt
      )
    }
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
}

function addProcessMetric(row: ProcessMetricRow, key: string, value: unknown): void {
  const number = numeric(value)
  if (number === undefined) return
  const sumKey = `${key}_sum` as keyof ProcessMetricRow
  const maxKey = `${key}_max` as keyof ProcessMetricRow
  row[sumKey] = Number(row[sumKey] ?? 0) + number as never
  row[maxKey] = row[maxKey] === null ? number as never : Math.max(Number(row[maxKey]), number) as never
}

function addMetric(row: AccountMetricRow, key: string, value: unknown, counted = false): void {
  const number = numeric(value)
  if (number === undefined) return
  const sumKey = `${key}_sum` as keyof AccountMetricRow
  const maxKey = `${key}_max` as keyof AccountMetricRow
  row[sumKey] = Number(row[sumKey] ?? 0) + number as never
  row[maxKey] = row[maxKey] === null ? number as never : Math.max(Number(row[maxKey]), number) as never
  if (counted) {
    const countKey = `${key}_count` as keyof AccountMetricRow
    row[countKey] = Number(row[countKey] ?? 0) + 1 as never
  }
}

function maxMetric(row: AccountMetricRow, key: string, value: unknown): void {
  const number = numeric(value)
  if (number === undefined) return
  const maxKey = `${key}_max` as keyof AccountMetricRow
  row[maxKey] = row[maxKey] === null ? number as never : Math.max(Number(row[maxKey]), number) as never
}
