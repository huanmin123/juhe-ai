import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const viewSource = readFileSync(fileURLToPath(new URL('../../views/usage-stats/UsageStatsView.vue', import.meta.url)), 'utf8')
const accountOptionsSource = readFileSync(fileURLToPath(new URL('../../views/usage-stats/useUsageStatsAccountOptions.ts', import.meta.url)), 'utf8')
const statsApiSource = readFileSync(fileURLToPath(new URL('../../api/domains/stats.ts', import.meta.url)), 'utf8')
const pageConfigSource = readFileSync(fileURLToPath(new URL('../../views/usage-stats/usageStatsPageConfig.ts', import.meta.url)), 'utf8')

assert.match(statsApiSource, /accountUsageTrend:/, '账户趋势必须使用独立 API')
assert.match(statsApiSource, /accountUsageSummary:/, '账户汇总必须使用独立 API')
assert.match(statsApiSource, /accountUsageOptions:[\s\S]{0,180}\/stats\/account-usage\/options/, '账户候选必须使用统计域窄接口')
assert.match(viewSource, /api\.(?:stats|myStats)\.accountUsageTrend/, '账户用量页必须单独请求趋势')
assert.match(viewSource, /api\.(?:stats|myStats)\.accountUsageSummary/, '账户用量页必须单独请求汇总')
assert.doesNotMatch(pageConfigSource, /includeSummary/, '账户列表请求不得携带已废弃 includeSummary 参数')
assert.match(viewSource, /resourceRequestSeq !== usageStatsResourceRequestSeq/, '被淘汰的列表响应不得误报接口未返回数据')
assert.match(viewSource, /\.\.\.addedTrendAccountIds\.value,[\s\S]{0,160}\.\.\.usageOverview\.defaultTrendAccountIds/, '趋势请求只能组合已选和默认账户 ID')
assert.doesNotMatch(viewSource, /loadProviderOptionsResource|api\.providers\.(?:list|modelOptions)|loadUsageStatsOptions/, '账户用量页不得为账户标签额外加载供应商或模型选项')
assert.doesNotMatch(viewSource, /loadUsageStatsWindow\(\{ force: true \}\)/, '账户用量列表刷新不得反复强制刷新共享统计窗口')
assert.match(viewSource, /@dropdown-visible-change="handleAccountOptionsDropdown"/, '账户候选只能由账户下拉展开事件触发')
assert.match(accountOptionsSource, /function handleAccountOptionsDropdown\(open: boolean\)[\s\S]{0,100}if \(open\)[\s\S]{0,80}loadAccountOptions\(\)/, '展开账户下拉时才允许加载账户候选')
assert.doesNotMatch(accountOptionsSource, /onMounted|watchEffect|watch\(/, '账户候选 composable 不得在首载或状态变化时预取')
assert.doesNotMatch(accountOptionsSource, /api\.(?:accounts|myAccounts)\.options|api\.providers/, '用量统计不得复用宽账户候选或供应商候选接口')
assert.match(accountOptionsSource, /accountUsageOptions\(\{ systemAccountId, \.\.\.request \}\)/, '管理侧应调用统计域账户候选接口')
assert.match(accountOptionsSource, /accountUsageOptions\(request\)/, '用户侧应调用统计域账户候选接口')
assert.match(accountOptionsSource, /selectedIds/, '单次候选请求必须携带已选账户用于回填')

const addHandler = sourceFunction(viewSource, 'handleAddedTrendAccountsChange', 'removeAddedTrendAccount')
const removeHandler = sourceFunction(viewSource, 'removeAddedTrendAccount', 'clearTrendAccountState')
for (const [name, source] of [['选择账户', addHandler], ['移除账户', removeHandler]] as const) {
  assert.doesNotMatch(source, /loadAccountOptions|api\.(?:accounts|myAccounts)\.options/, `${name}后不得重新请求账户候选`)
  assert.match(source, /loadData\(\{ quiet: true \}\)/, `${name}后仍应按需刷新趋势数据`)
}

console.log('账户用量渐进加载回归通过：首载不取候选，展开只取账户候选，选择和移除不重复加载下拉')

function sourceFunction(source: string, startName: string, nextName: string): string {
  const start = source.indexOf(`function ${startName}`)
  const end = source.indexOf(`function ${nextName}`, start + 1)
  assert.ok(start >= 0 && end > start, `应能定位 ${startName} 实现`)
  return source.slice(start, end)
}
