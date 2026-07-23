import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const viewSource = readFileSync(fileURLToPath(new URL('../../views/usage-stats/UsageStatsView.vue', import.meta.url)), 'utf8')
const statsApiSource = readFileSync(fileURLToPath(new URL('../../api/domains/stats.ts', import.meta.url)), 'utf8')
const pageConfigSource = readFileSync(fileURLToPath(new URL('../../views/usage-stats/usageStatsPageConfig.ts', import.meta.url)), 'utf8')

assert.match(statsApiSource, /accountUsageTrend:/, '账户趋势必须使用独立 API')
assert.match(statsApiSource, /accountUsageSummary:/, '账户汇总必须使用独立 API')
assert.match(viewSource, /api\.(?:stats|myStats)\.accountUsageTrend/, '账户用量页必须单独请求趋势')
assert.match(viewSource, /api\.(?:stats|myStats)\.accountUsageSummary/, '账户用量页必须单独请求汇总')
assert.doesNotMatch(pageConfigSource, /includeSummary/, '账户列表请求不得携带已废弃 includeSummary 参数')
assert.match(viewSource, /resourceRequestSeq !== usageStatsResourceRequestSeq/, '被淘汰的列表响应不得误报接口未返回数据')
assert.match(viewSource, /\.\.\.addedTrendAccountIds\.value,[\s\S]{0,160}\.\.\.usageOverview\.defaultTrendAccountIds/, '趋势请求只能组合已选和默认账户 ID')
assert.doesNotMatch(viewSource, /Promise\.all\(\[[\s\S]{0,1200}loadUsageStatsOptions/, '账户用量首屏不得加载供应商选项')
assert.doesNotMatch(viewSource, /loadUsageStatsWindow\(\{ force: true \}\)/, '账户用量列表刷新不得反复强制刷新共享统计窗口')
assert.match(viewSource, /handleProviderAwareAccountOptionsDropdown/, '供应商选项应在账户选择下拉打开时再加载')

console.log('账户用量渐进加载回归通过：列表、趋势、供应商选项与共享窗口按需加载')
