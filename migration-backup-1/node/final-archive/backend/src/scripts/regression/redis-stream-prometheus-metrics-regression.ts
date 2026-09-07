import assert from 'node:assert/strict'

import {
  renderPrometheusMetrics,
  resetPrometheusMetricsForTest,
  setRedisStreamQueueMetricsSnapshot
} from '../../shared/prometheus-metrics.js'
import { redisStreamDrainContracts } from '../../shared/redis-stream-drain.js'
import { redisStreamMetricSamples } from '../../shared/redis-stream-metrics.js'

resetPrometheusMetricsForTest()
const usageContract = redisStreamDrainContracts.find((contract) => contract.name === 'usage-records')
const publicApiContract = redisStreamDrainContracts.find((contract) => contract.name === 'public-api-logs')
assert.ok(usageContract)
assert.ok(publicApiContract)
const samples = redisStreamMetricSamples({
  checkedAt: '2026-08-18T00:00:00.000Z',
  drained: false,
  streams: [
    {
      name: 'usage-records',
      streamKey: 'must-not-be-rendered',
      length: 7,
      drained: false,
      groups: [{ name: usageContract.groupName, pending: 2, lag: 3, consumers: 1, oldestPendingIdleMs: 4_000 }]
    },
    {
      name: 'public-api-logs',
      streamKey: 'must-not-be-rendered',
      length: 5,
      drained: false,
      groups: [{ name: publicApiContract.groupName, pending: 1, lag: 0 }]
    },
    {
      name: 'record-maintenance',
      streamKey: 'must-not-be-rendered',
      length: 0,
      drained: true,
      groups: []
    }
  ]
})
setRedisStreamQueueMetricsSnapshot({
  enabled: true,
  collectionSuccess: true,
  lastSuccessTimestampSeconds: 1_000,
  queues: samples
})

const rendered = renderPrometheusMetrics()
assert.match(rendered, /juhe_ai_redis_stream_queue_collection_success\{service="juhe-ai"\} 1/)
assert.match(rendered, /juhe_ai_redis_stream_queue_length\{queue="usage_records",service="juhe-ai"\} 7/)
assert.match(rendered, /juhe_ai_redis_stream_queue_pending\{queue="usage_records",service="juhe-ai"\} 2/)
assert.match(rendered, /juhe_ai_redis_stream_queue_lag\{queue="usage_records",service="juhe-ai"\} 3/)
assert.match(rendered, /juhe_ai_redis_stream_queue_lag_known\{queue="usage_records",service="juhe-ai"\} 1/)
assert.match(rendered, /juhe_ai_redis_stream_queue_consumers\{queue="usage_records",service="juhe-ai"\} 1/)
assert.match(rendered, /juhe_ai_redis_stream_queue_oldest_pending_idle_seconds\{queue="usage_records",service="juhe-ai"\} 4/)
assert.match(rendered, /juhe_ai_redis_stream_queue_consumer_group_present\{queue="record_maintenance",service="juhe-ai"\} 0/)
assert.doesNotMatch(rendered, /must-not-be-rendered|requestId|traceId/i)

console.log('redis stream prometheus metrics regression passed')
