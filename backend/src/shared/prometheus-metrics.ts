const serviceName = 'juhe-ai'
const durationBuckets = [0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, Number.POSITIVE_INFINITY] as const

export type HttpMetricRouteGroup = 'health' | 'management' | 'public_api' | 'gateway' | 'other' | 'observability'
export type HttpMetricOutcome = 'completed' | 'aborted'

export interface HttpMetricRequest {
  routeGroup: Exclude<HttpMetricRouteGroup, 'observability'>
  method: string
  startedAtMs: number
  finished: boolean
}

type HttpMetricLabels = {
  route_group: Exclude<HttpMetricRouteGroup, 'observability'>
  method: string
  status_class: string
  outcome: HttpMetricOutcome
}

interface HistogramValue {
  count: number
  sum: number
  buckets: Map<number, number>
}

const requestCounters = new Map<string, number>()
const requestHistograms = new Map<string, HistogramValue>()
const inFlightRequests = new Map<string, number>()

export const redisStreamQueueMetricNames = ['usage_records', 'public_api_logs', 'record_maintenance'] as const
export type RedisStreamQueueMetricName = typeof redisStreamQueueMetricNames[number]

export interface RedisStreamQueueMetricSample {
  queue: RedisStreamQueueMetricName
  streamLength: number
  pendingCount: number
  lag: number
  lagKnown: number
  consumerCount: number
  oldestPendingIdleSeconds: number
  consumerGroupPresent: number
}

export interface RedisStreamQueueMetricsSnapshot {
  enabled: boolean
  collectionSuccess: boolean
  lastSuccessTimestampSeconds?: number
  queues: readonly RedisStreamQueueMetricSample[]
}

let redisStreamQueueMetrics: RedisStreamQueueMetricsSnapshot = {
  enabled: false,
  collectionSuccess: false,
  queues: []
}

export function classifyHttpMetricRoute(path: string): HttpMetricRouteGroup {
  if (path === '/__aisys__/metrics') return 'observability'
  if (path === '/health' || path === '/__aisys__/health' || path === '/__aisys__/api/health') return 'health'
  if (path === '/__aipublic__' || path.startsWith('/__aipublic__/')) return 'public_api'
  if (path === '/__aisys__' || path.startsWith('/__aisys__/')) return 'management'
  if (path === '/' || path === '/v1' || path.startsWith('/v1/')) return 'gateway'
  return 'other'
}

export function startHttpMetricRequest(path: string, method: string, startedAtMs = Date.now()): HttpMetricRequest | undefined {
  const routeGroup = classifyHttpMetricRoute(path)
  if (routeGroup === 'observability') return undefined
  const normalizedMethod = normalizeMethod(method)
  increment(inFlightRequests, labelsKey({ route_group: routeGroup, method: normalizedMethod }))
  return { routeGroup, method: normalizedMethod, startedAtMs, finished: false }
}

export function finishHttpMetricRequest(
  request: HttpMetricRequest | undefined,
  statusCode: number | undefined,
  outcome: HttpMetricOutcome,
  finishedAtMs = Date.now()
): void {
  if (!request || request.finished) return
  request.finished = true
  decrement(inFlightRequests, labelsKey({ route_group: request.routeGroup, method: request.method }))
  const labels: HttpMetricLabels = {
    route_group: request.routeGroup,
    method: request.method,
    status_class: classifyStatus(statusCode),
    outcome
  }
  increment(requestCounters, labelsKey(labels))
  const durationSeconds = Math.max(0, finishedAtMs - request.startedAtMs) / 1_000
  const histogram = requestHistograms.get(labelsKey(labels)) ?? createHistogram()
  histogram.count += 1
  histogram.sum += durationSeconds
  for (const bucket of durationBuckets) {
    if (durationSeconds <= bucket) histogram.buckets.set(bucket, (histogram.buckets.get(bucket) ?? 0) + 1)
  }
  requestHistograms.set(labelsKey(labels), histogram)
}

export function renderPrometheusMetrics(): string {
  const lines = [
    '# HELP juhe_ai_http_requests_total Completed HTTP requests grouped without paths, identifiers, or error text.',
    '# TYPE juhe_ai_http_requests_total counter'
  ]
  for (const [key, count] of sortedEntries(requestCounters)) {
    lines.push(`juhe_ai_http_requests_total{${renderLabels(parseMetricLabels(key))}} ${count}`)
  }

  lines.push('# HELP juhe_ai_http_request_duration_seconds HTTP request duration grouped without paths, identifiers, or error text.')
  lines.push('# TYPE juhe_ai_http_request_duration_seconds histogram')
  for (const [key, histogram] of sortedEntries(requestHistograms)) {
    const labels = parseMetricLabels(key)
    for (const bucket of durationBuckets) {
      const le = Number.isFinite(bucket) ? String(bucket) : '+Inf'
      lines.push(`juhe_ai_http_request_duration_seconds_bucket{${renderLabels({ ...labels, le })}} ${histogram.buckets.get(bucket) ?? 0}`)
    }
    lines.push(`juhe_ai_http_request_duration_seconds_sum{${renderLabels(labels)}} ${histogram.sum}`)
    lines.push(`juhe_ai_http_request_duration_seconds_count{${renderLabels(labels)}} ${histogram.count}`)
  }

  lines.push('# HELP juhe_ai_http_requests_in_flight In-flight HTTP requests grouped without paths or identifiers.')
  lines.push('# TYPE juhe_ai_http_requests_in_flight gauge')
  for (const [key, count] of sortedEntries(inFlightRequests)) {
    lines.push(`juhe_ai_http_requests_in_flight{${renderLabels(parseMetricLabels(key))}} ${count}`)
  }
  lines.push('# HELP juhe_ai_process_resident_memory_bytes Node process resident memory.')
  lines.push('# TYPE juhe_ai_process_resident_memory_bytes gauge')
  lines.push(`juhe_ai_process_resident_memory_bytes{service="${serviceName}"} ${process.memoryUsage().rss}`)
  lines.push('# HELP juhe_ai_process_heap_used_bytes Node process heap currently used.')
  lines.push('# TYPE juhe_ai_process_heap_used_bytes gauge')
  lines.push(`juhe_ai_process_heap_used_bytes{service="${serviceName}"} ${process.memoryUsage().heapUsed}`)
  lines.push('# HELP juhe_ai_process_uptime_seconds Node process uptime.')
  lines.push('# TYPE juhe_ai_process_uptime_seconds gauge')
  lines.push(`juhe_ai_process_uptime_seconds{service="${serviceName}"} ${process.uptime()}`)
  renderRedisStreamQueueMetrics(lines)
  return `${lines.join('\n')}\n`
}

export function setRedisStreamQueueMetricsSnapshot(snapshot: RedisStreamQueueMetricsSnapshot): void {
  const allowedQueues = new Set<RedisStreamQueueMetricName>(redisStreamQueueMetricNames)
  const queues = snapshot.queues
    .filter((sample) => allowedQueues.has(sample.queue))
    .map((sample) => ({
      queue: sample.queue,
      streamLength: finiteMetric(sample.streamLength),
      pendingCount: finiteMetric(sample.pendingCount),
      lag: finiteMetric(sample.lag),
      lagKnown: sample.lagKnown > 0 ? 1 : 0,
      consumerCount: finiteMetric(sample.consumerCount),
      oldestPendingIdleSeconds: finiteMetric(sample.oldestPendingIdleSeconds),
      consumerGroupPresent: sample.consumerGroupPresent > 0 ? 1 : 0
    }))
  redisStreamQueueMetrics = {
    enabled: snapshot.enabled,
    collectionSuccess: snapshot.collectionSuccess,
    ...(finiteMetric(snapshot.lastSuccessTimestampSeconds) > 0
      ? { lastSuccessTimestampSeconds: finiteMetric(snapshot.lastSuccessTimestampSeconds) }
      : {}),
    queues
  }
}

export function resetPrometheusMetricsForTest(): void {
  requestCounters.clear()
  requestHistograms.clear()
  inFlightRequests.clear()
  redisStreamQueueMetrics = { enabled: false, collectionSuccess: false, queues: [] }
}

function renderRedisStreamQueueMetrics(lines: string[]): void {
  lines.push('# HELP juhe_ai_redis_stream_queue_enabled Whether Redis Stream queue monitoring is enabled for this process.')
  lines.push('# TYPE juhe_ai_redis_stream_queue_enabled gauge')
  lines.push(`juhe_ai_redis_stream_queue_enabled{service="${serviceName}"} ${redisStreamQueueMetrics.enabled ? 1 : 0}`)
  if (!redisStreamQueueMetrics.enabled) return

  lines.push('# HELP juhe_ai_redis_stream_queue_collection_success Whether the last bounded Redis Stream inspection succeeded.')
  lines.push('# TYPE juhe_ai_redis_stream_queue_collection_success gauge')
  lines.push(`juhe_ai_redis_stream_queue_collection_success{service="${serviceName}"} ${redisStreamQueueMetrics.collectionSuccess ? 1 : 0}`)
  if (redisStreamQueueMetrics.lastSuccessTimestampSeconds !== undefined) {
    lines.push('# HELP juhe_ai_redis_stream_queue_last_success_timestamp_seconds Unix timestamp of the last successful Redis Stream inspection.')
    lines.push('# TYPE juhe_ai_redis_stream_queue_last_success_timestamp_seconds gauge')
    lines.push(`juhe_ai_redis_stream_queue_last_success_timestamp_seconds{service="${serviceName}"} ${redisStreamQueueMetrics.lastSuccessTimestampSeconds}`)
  }

  for (const sample of redisStreamQueueMetrics.queues) {
    const labels = `queue="${sample.queue}",service="${serviceName}"`
    lines.push(`juhe_ai_redis_stream_queue_length{${labels}} ${sample.streamLength}`)
    lines.push(`juhe_ai_redis_stream_queue_pending{${labels}} ${sample.pendingCount}`)
    lines.push(`juhe_ai_redis_stream_queue_lag{${labels}} ${sample.lag}`)
    lines.push(`juhe_ai_redis_stream_queue_lag_known{${labels}} ${sample.lagKnown}`)
    lines.push(`juhe_ai_redis_stream_queue_consumers{${labels}} ${sample.consumerCount}`)
    lines.push(`juhe_ai_redis_stream_queue_oldest_pending_idle_seconds{${labels}} ${sample.oldestPendingIdleSeconds}`)
    lines.push(`juhe_ai_redis_stream_queue_consumer_group_present{${labels}} ${sample.consumerGroupPresent}`)
  }
}

function createHistogram(): HistogramValue {
  return { count: 0, sum: 0, buckets: new Map() }
}

function normalizeMethod(method: string): string {
  return ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'].includes(method) ? method : 'OTHER'
}

function classifyStatus(statusCode: number | undefined): string {
  if (!Number.isInteger(statusCode) || !statusCode || statusCode < 100 || statusCode > 599) return 'unknown'
  return `${Math.floor(statusCode / 100)}xx`
}

function labelsKey(labels: Record<string, string>): string {
  return Object.entries(labels).sort(([left], [right]) => left.localeCompare(right)).map(([key, value]) => `${key}=${value}`).join('|')
}

function parseMetricLabels(key: string): Record<string, string> {
  return Object.fromEntries(key.split('|').map((entry) => entry.split('=', 2)))
}

function renderLabels(labels: Record<string, string>): string {
  return Object.entries({ service: serviceName, ...labels })
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, value]) => `${key}="${value.replaceAll('\\', '\\\\').replaceAll('"', '\\"')}"`)
    .join(',')
}

function increment(target: Map<string, number>, key: string): void {
  target.set(key, (target.get(key) ?? 0) + 1)
}

function decrement(target: Map<string, number>, key: string): void {
  const nextValue = Math.max(0, (target.get(key) ?? 0) - 1)
  if (nextValue === 0) {
    target.delete(key)
    return
  }
  target.set(key, nextValue)
}

function finiteMetric(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : 0
}

function sortedEntries<T>(source: Map<string, T>): [string, T][] {
  return [...source.entries()].sort(([left], [right]) => left.localeCompare(right))
}
