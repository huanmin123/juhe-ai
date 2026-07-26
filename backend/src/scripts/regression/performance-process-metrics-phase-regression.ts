import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  delayUntilPerformanceProcessMetricsPublishPhaseMs,
  performanceProcessMetricsRegistryKey,
  stablePerformanceProcessMetricsPublishPhaseMs
} from '../../shared/performance-process-metrics-registry.js'

const seeds = [
  'control-a:control:control-a',
  'db-a:db-service:db-a',
  'gateway-1:gateway:gateway-1',
  'gateway-2:gateway:gateway-2',
  'usage-1:usage-worker:1',
  'usage-2:usage-worker:2',
  'log-1:log-worker:1',
  'log-2:log-worker:2',
  'stats-1:stats-worker:1',
  'ops-1:ops-worker:1'
]
const phases = seeds.map(stablePerformanceProcessMetricsPublishPhaseMs)

for (let index = 0; index < seeds.length; index += 1) {
  assert.equal(
    stablePerformanceProcessMetricsPublishPhaseMs(seeds[index]),
    phases[index],
    '相同实例和角色重启后必须得到相同发布相位'
  )
  assert.ok(phases[index] >= 250 && phases[index] < 3_000, 'publisher 必须限制在预留写入波次内')
}
assert.ok(new Set(phases).size >= seeds.length - 1, '默认性能拓扑的 publisher 不应重新聚集到同一毫秒')

assert.equal(delayUntilPerformanceProcessMetricsPublishPhaseMs(500, 1_000), 500, '应等待当前周期内的目标相位')
assert.equal(delayUntilPerformanceProcessMetricsPublishPhaseMs(1_000, 1_000), 5_000, '命中相位时必须等待下一周期，禁止零延迟循环')
assert.equal(delayUntilPerformanceProcessMetricsPublishPhaseMs(991, 1_000), 5_009, '过近相位必须推迟到下一周期')
assert.equal(delayUntilPerformanceProcessMetricsPublishPhaseMs(6_500, 1_000), 4_500, '相位计算必须按墙钟周期锚定而不是进程启动时间')

const deploymentAWorkerKey = performanceProcessMetricsRegistryKey('deployment-a-usage-worker-1', 'usage-worker:1')
const deploymentBWorkerKey = performanceProcessMetricsRegistryKey('deployment-b-usage-worker-1', 'usage-worker:1')
assert.notEqual(
  deploymentAWorkerKey,
  deploymentBWorkerKey,
  '不同部署的同角色同副本必须写入独立 Redis key'
)
assert.equal(
  performanceProcessMetricsRegistryKey('deployment-a-usage-worker-1', 'usage-worker:1'),
  deploymentAWorkerKey,
  '同一稳定实例和角色重启后必须复用同一个 Redis key'
)
assert.match(deploymentAWorkerKey, /:process-event-loop:v2:deployment-a-usage-worker-1:usage-worker:1$/, '新 key 必须包含版本、稳定实例和角色')

const source = readFileSync(new URL('../../shared/performance-process-metrics-registry.ts', import.meta.url), 'utf8')
assert.doesNotMatch(source, /setInterval\(/, 'publisher 不得继续使用相对进程启动时间的 setInterval')
assert.match(source, /scheduleNextPublish\(\)/, 'publisher 启动后必须先安排墙钟相位')
assert.doesNotMatch(
  source.match(/export function startPerformanceProcessMetricsPublisher[\s\S]*?\n\}/)?.[0] ?? '',
  /publishCurrentProcessMetrics\(/,
  'publisher 启动时不得立即形成 Redis SET 洪峰'
)
assert.match(source, /clearTimeout\(publishTimer\)/, 'stop 必须取消尚未触发的相位 timer')
assert.match(
  source,
  /performanceProcessMetricsRegistryKey\(runtimeConfig\.instanceId, sample\.processRole\)/,
  'publisher 必须使用 instanceId 和角色构造注册 key，不能退回 role-only key'
)
assert.match(
  source,
  /'SCAN', cursor, 'MATCH', `\$\{registryKeyPrefix\}\*`/,
  '读侧必须继续扫描公共前缀，使滚动升级期间的旧 key 和 v2 key 都可被发现'
)
assert.match(source, /registryTtlSeconds = 20/, '新旧注册项必须继续依赖短 TTL 清理退出实例')

console.log('performance 进程指标回归通过：发布错峰、多实例 key 隔离、滚动发现与 TTL 清理语义正确')
