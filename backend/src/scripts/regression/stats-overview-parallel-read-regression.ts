import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const source = readFileSync(resolve('src/storage/usage-stats.repository.ts'), 'utf8')
assert.doesNotMatch(source, /export (?:async )?function getUsageStatsOverview(?:Async)?\(/, '无消费者的宽组合 overview loader 必须退场')
for (const name of ['Summary', 'DailyTrend', 'HourlyTrend', 'ModelDistribution', 'Errors']) {
  assert.match(source, new RegExp(`export async function getUsageStatsOverview${name}Async\\(`), `${name} 必须保留独立异步读取`)
}

console.log('统计总览窄读取回归通过：旧宽组合 loader 已退场，各区块保留独立异步读取')
