import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const productionReadBenchmark = readFileSync(
  resolve('src/scripts/performance/production-system-api-read-benchmark.ts'),
  'utf8'
)
const mixedLoadBenchmark = readFileSync(
  resolve('src/scripts/performance/usage-real-entry-mixed-load.ts'),
  'utf8'
)

for (const [name, source] of [
  ['生产 System API 读取基准', productionReadBenchmark],
  ['真实入口混合压测', mixedLoadBenchmark]
] as const) {
  assert.doesNotMatch(
    source,
    /\/stats\/usage-overview(?:[?'"`]|$)/,
    `${name} 不得请求已退场的管理侧 usage-overview 组合端点`
  )
  assert.match(
    source,
    /\/stats\/usage-overview\/summary/,
    `${name} 必须使用按需拆分后的管理侧 summary 端点`
  )
}

assert.doesNotMatch(
  productionReadBenchmark,
  /\/my-stats\/usage-overview(?:[?'"`]|$)/,
  '生产 System API 读取基准不得请求已退场的个人 usage-overview 组合端点'
)
assert.match(
  productionReadBenchmark,
  /\/my-stats\/usage-overview\/summary/,
  '生产 System API 读取基准必须使用按需拆分后的个人 summary 端点'
)
assert.doesNotMatch(
  productionReadBenchmark,
  /\/stats\/system-metrics(?:[?'"`]|$)/,
  '生产 System API 读取基准不得请求全量 system-metrics 组合端点'
)
assert.match(
  productionReadBenchmark,
  /\/stats\/system-metrics\/trend/,
  '生产 System API 读取基准必须只请求独立趋势端点'
)

console.log('性能入口按需端点契约回归通过：组合统计端点不会重新进入读取基准或混合压测')
