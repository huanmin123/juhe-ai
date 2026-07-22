import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const viewSource = readFileSync(fileURLToPath(new URL('../../views/usage-stats/UsageStatsView.vue', import.meta.url)), 'utf8')
const statsApiSource = readFileSync(fileURLToPath(new URL('../../api/domains/stats.ts', import.meta.url)), 'utf8')
const legacyCellUrl = new URL('../../views/usage-stats/UsageStatCell.vue', import.meta.url)

assert.equal(existsSync(fileURLToPath(legacyCellUrl)), false, '旧 UsageStatCell wrapper 已由 UsageStatsView 当前渲染 owner 替代，必须删除')
assert.match(statsApiSource, /accountUsageTrend:/, '账户趋势必须使用独立 API')
assert.match(viewSource, /api\.(?:stats|myStats)\.accountUsageTrend/, '账户用量页必须单独请求趋势')
assert.doesNotMatch(viewSource, /Promise\.all\(\[[\s\S]{0,1200}loadUsageStatsOptions/, '账户用量首屏不得加载供应商选项')
assert.doesNotMatch(viewSource, /loadUsageStatsWindow\(\{ force: true \}\)/, '账户用量列表刷新不得反复强制刷新共享统计窗口')
assert.match(viewSource, /handleProviderAwareAccountOptionsDropdown/, '供应商选项应在账户选择下拉打开时再加载')

console.log('账户用量渐进加载回归通过：列表、趋势、供应商选项与共享窗口按需加载')
