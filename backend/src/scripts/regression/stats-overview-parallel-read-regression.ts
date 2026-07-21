import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const source = readFileSync(resolve('src/storage/usage-stats.repository.ts'), 'utf8')
const asyncOverview = source.slice(
  source.indexOf('export async function getUsageStatsOverviewAsync'),
  source.indexOf('export async function latestUsageStatsLagSecondsForRuntime')
)

assert.match(asyncOverview, /await Promise\.all\(\[/, 'PostgreSQL 统计总览的摘要、趋势、模型和错误窗口应并行读取')
assert.match(asyncOverview, /loadUsageOverviewSummaryRowAsync/, '并行读取必须包含摘要窗口')
assert.match(asyncOverview, /usage_overview_trend_windows/, '并行读取必须包含趋势窗口')
assert.match(asyncOverview, /usage_model_rank_windows/, '并行读取必须包含模型排行窗口')
assert.match(asyncOverview, /usage_error_rank_windows/, '并行读取必须包含错误排行窗口')

console.log('统计总览并行读取回归通过：PostgreSQL 四组预聚合窗口并行查询')
