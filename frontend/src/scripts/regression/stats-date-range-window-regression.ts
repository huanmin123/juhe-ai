import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import assert from 'node:assert/strict'

const frontendRoot = resolve(import.meta.dirname, '..', '..')
const statsViewSource = readFileSync(resolve(frontendRoot, 'views', 'stats', 'StatsView.vue'), 'utf8')
const systemMetricsViewSource = readFileSync(resolve(frontendRoot, 'views', 'stats', 'SystemMetricsStatsView.vue'), 'utf8')
const usageStatsViewSource = readFileSync(resolve(frontendRoot, 'views', 'usage-stats', 'UsageStatsView.vue'), 'utf8')
const usageStatsPageConfigSource = readFileSync(resolve(frontendRoot, 'views', 'usage-stats', 'usageStatsPageConfig.ts'), 'utf8')
const aiPerformanceViewSource = readFileSync(resolve(frontendRoot, 'views', 'ai-performance', 'AiPerformanceView.vue'), 'utf8')
const ipStatsViewSource = readFileSync(resolve(frontendRoot, 'views', 'ip-stats', 'IpStatsView.vue'), 'utf8')
const usageStatsWindowSource = readFileSync(resolve(frontendRoot, 'composables', 'useUsageStatsWindow.ts'), 'utf8')
const statsRoutesSource = readFileSync(resolve(frontendRoot, '..', '..', 'backend', 'src', 'modules', 'stats', 'stats.routes.ts'), 'utf8')

assert.match(
  usageStatsWindowSource,
  /type\s+UsageStatsWindowLoadOptions\s*=\s*\{[\s\S]*force\?:\s*boolean[\s\S]*\}/,
  'usage stats window loader must expose a force refresh option'
)

assert.match(
  usageStatsWindowSource,
  /Date\.now\(\)\s*-\s*scopeState\.loadedAtMs\s*<\s*windowCacheTtlMs/,
  'each usage stats window scope cache must expire instead of staying process-lifetime stale'
)

assert.match(
  usageStatsWindowSource,
  /const\s+scopeStates:\s*Record<UsageStatsWindowScope,\s*UsageStatsWindowScopeState>/,
  'usage stats window cache must isolate self and admin view scopes'
)

assert.match(
  usageStatsWindowSource,
  /usageStatsWindowIdentitySignature[\s\S]*user\?\.id[\s\S]*user\?\.role[\s\S]*authState\.revision\.value/,
  'usage stats window cache must be isolated by the current identity and auth revision'
)

assert.match(
  statsViewSource,
  /function\s+selectedRangeParams\(\):\s*\{ startDate\?: string; endDate\?: string \}\s*\{[\s\S]*if\s*\(!dateRangeExplicit\.value\)\s*return\s*\{\}/,
  'stats overview must not submit browser-local dates when the range is not explicit'
)

assert.match(
  statsViewSource,
  /const viewScope = isManagementView\.value \? 'admin' : 'self'[\s\S]*loadUsageStatsWindow\(\{ force: options\.forceUsageWindow === true, viewScope \}\)/,
  'stats overview dynamic ranges must force-refresh the scoped stats window when requested'
)

assert.match(
  statsViewSource,
  /async\s+function\s+handleQuickRangeChange\(value: string \| number\)\s*\{[\s\S]*await\s+loadUsageStatsWindow\(\{ force: true, viewScope: isManagementView\.value \? 'admin' : 'self' \}\)[\s\S]*rangeMode\.value = mode/,
  'stats overview quick ranges must resolve the shared window before calculating today'
)

for (const [name, source] of [
  ['stats overview', statsViewSource],
  ['system metrics', systemMetricsViewSource]
] as const) {
  assert.match(
    source,
    /:value="quickRangeValue \?\? ''"/,
    `${name} must pass an empty string instead of undefined when no quick range matches`
  )
}

assert.match(
  systemMetricsViewSource,
  /async\s+function\s+loadPageData\(options: \{ forceUsageWindow\?: boolean \} = \{\}\)[\s\S]*loadUsageStatsWindow\(\{ force: options\.forceUsageWindow === true, viewScope: 'admin' \}\)[\s\S]*return\s+loadData\(\)/,
  'system metrics must resolve the scoped stats window before recalculating a dynamic range'
)

assert.match(
  systemMetricsViewSource,
  /api\.stats\.systemMetricsTrend\(rangeParams,\s*\{\s*signal:\s*controller\.signal\s*\}\)/,
  'system metrics must load the split trend endpoint with cancellation'
)

assert.match(
  systemMetricsViewSource,
  /function\s+selectedRangeParams\(\):\s*\{ startDate\?: string; endDate\?: string \}\s*\{[\s\S]*if\s*\(!dateRangeExplicit\.value\)\s*return\s*\{\}/,
  'system metrics must omit browser-local dates when the range is not explicit'
)

assert.match(
  systemMetricsViewSource,
  /syncImplicitDateRangeToStatsWindow\(\)[\s\S]*systemMetrics\.value = metrics/,
  'system metrics must align the displayed implicit range after the server window is available'
)

assert.match(
  statsRoutesSource,
  /function normalizeSystemMetricsDateRangeAsync[\s\S]*const today = dateKey\(new Date\(\), timezone\)[\s\S]*startDate = input\.startDate \?\? input\.endDate \?\? today[\s\S]*endDate = input\.endDate \?\? input\.startDate \?\? today/,
  'system metrics omitted dates must resolve to one server-timezone day'
)

assert.match(
  usageStatsViewSource,
  /dateRange:\s*dateRangeExplicit\.value\s*\?\s*selectedRange\.value\s*:\s*undefined/,
  'account usage stats must not submit browser-local default date ranges'
)

assert.match(
  usageStatsViewSource,
  /loadUsageStatsWindow\(\{[\s\S]*force:\s*options\?\.forceCache === true,[\s\S]*viewScope:\s*isManagementView\.value \? 'admin' : 'self'[\s\S]*\}\)/,
  'account usage stats must use the scoped stats window and refresh its metadata on explicit refresh'
)

assert.match(
  usageStatsPageConfigSource,
  /dateRange\?:\s*readonly\s*\[string,\s*string\]/,
  'account usage stats params must allow omitted date ranges'
)

assert.match(
  aiPerformanceViewSource,
  /const windowScope = isManagementView\.value \? 'admin' : 'self'[\s\S]*const windowLoad = loadUsageStatsWindow\(\{\s*viewScope:\s*windowScope\s*\}\)[\s\S]*await\s+loadPerformanceBase\(\)/,
  'AI performance must start the scoped stats window metadata without delaying its base data'
)

assert.doesNotMatch(
  aiPerformanceViewSource,
  /function\s+refreshPerformance\(\)[\s\S]{0,120}force:\s*true/,
  'AI performance manual refresh must reuse valid usage-window metadata'
)

assert.match(
  aiPerformanceViewSource,
  /function\s+currentPerformanceRequest[\s\S]*systemAccountId:\s*selectedPerformanceSystemAccountId\(\)/,
  'AI performance requests must include the selected system account'
)

assert.match(
  aiPerformanceViewSource,
  /function\s+selectedRangeParams\(\):\s*\{ startDate\?: string; endDate\?: string \}\s*\{[\s\S]*if\s*\(!dateRangeExplicit\.value\) return \{\}/,
  'AI performance default range must be normalized by the Node service timezone without browser-local dates'
)

assert.match(
  ipStatsViewSource,
  /await\s+loadUsageStatsWindow\(\{[\s\S]*force:\s*options\.force === true,[\s\S]*viewScope:\s*'admin'[\s\S]*\}\)[\s\S]*api\.ipStats\.list\(buildListParams\(\)\)/,
  'IP stats must use the scoped cached stats window before calculating segmented date ranges'
)

assert.match(usageStatsWindowSource, /lastLoadFailed:\s*boolean/, 'usage-window must retain an explicit failure state')
assert.match(usageStatsWindowSource, /scopeState\.value = undefined[\s\S]*scopeState\.lastLoadFailed = true/, 'failed usage-window requests must not be cached as successful fallback windows')
assert.doesNotMatch(aiPerformanceViewSource, /didUsageStatsWindowLoadFail\(windowScope\)/, 'AI performance base must remain usable when optional usage-window metadata fails')
assert.match(ipStatsViewSource, /didUsageStatsWindowLoadFail\('admin'\)/, 'IP stats must stop before using a failed server window')

for (const [name, source, version] of [
  ['stats overview', statsViewSource, 6],
  ['system metrics', systemMetricsViewSource, 3]
] as const) {
  assert.match(source, /type\s+RangeMode\s*=\s*'auto'\s*\|\s*QuickRange\s*\|\s*'custom'/, `${name} must persist the range semantic`)
  assert.match(source, /rangeMode\?:\s*RangeMode/, `${name} page-state cache must include range mode`)
  assert.match(source, new RegExp(`usePageStateCache<[\\s\\S]*version: ${version}`), `${name} must bump its cache version`)
  assert.match(source, /return state\.range \? 'custom' : 'auto'/, `${name} must treat old fixed ranges as custom`)
  assert.match(source, /rangeMode: 'custom'/, `${name} must migrate versioned legacy fixed ranges as custom`)
  assert.match(source, /rangeMode\.value = 'custom'[\s\S]*dateRangeExplicit\.value = true/, `${name} manual date selection must become custom`)
  assert.match(source, /rangeMode\.value = 'auto'[\s\S]*dateRangeExplicit\.value = false/, `${name} reset must restore auto mode`)
  assert.match(source, /async\s+function\s+handleQuickRangeChange\(value: string \| number\)[\s\S]*loadUsageStatsWindow\(\{ force: true, viewScope:/, `${name} quick selection must refresh the server window`)
}

assert.match(
  statsViewSource,
  /function\s+quickRangeModeForSelection\(value: RangeMode\): QuickRange \| undefined\s*\{[\s\S]*if \(value === 'auto'\) return 'recent1m'[\s\S]*return isQuickRangeMode\(value\) \? value : undefined/,
  'stats overview auto mode must map only to the recent1m visual option'
)
assert.match(
  statsViewSource,
  /const mode = quickRangeModeForSelection\(rangeMode\.value\)[\s\S]*if \(!mode\) return undefined[\s\S]*if \(didUsageStatsWindowLoadFail\(isManagementView\.value \? 'admin' : 'self'\)\) return undefined[\s\S]*const range = quickRangeDateRange\(mode\)[\s\S]*if \(!range\) return undefined[\s\S]*return startDate === formatDateKey\(range\[0\]\) && endDate === formatDateKey\(range\[1\]\) \? mode : undefined/,
  'stats overview quick selection must remain empty when the server window fails and otherwise require the mapped server window to match exactly'
)
assert.match(
  systemMetricsViewSource,
  /if \(!isQuickRangeMode\(rangeMode\.value\)\) return undefined[\s\S]*return startDate === formatDateKey\(range\[0\]\) && endDate === formatDateKey\(range\[1\]\) \? rangeMode\.value : undefined/,
  'system metrics quick selection must require both mode and the current server window'
)

assert.doesNotMatch(statsViewSource, /document\.addEventListener\('visibilitychange'/, 'stats overview must not refresh when a browser tab becomes visible')
assert.doesNotMatch(statsViewSource, /window\.addEventListener\('focus'/, 'stats overview must not refresh when the browser window regains focus')
assert.doesNotMatch(statsViewSource, /millisecondsUntilNextStatsDay|dynamicRangeRolloverTimer/, 'stats overview must not schedule automatic cross-day refreshes')
assert.match(statsViewSource, /function isDynamicRangeMode\(value: RangeMode\): value is Exclude<RangeMode, 'custom'>\s*\{\s*return value !== 'custom'/, 'stats overview manual refresh must keep auto and quick ranges dynamic while custom remains fixed')
assert.match(statsViewSource, /function refreshData\(\): void \{[\s\S]*force: true,[\s\S]*forceUsageWindow: isDynamicRangeMode\(rangeMode\.value\)/, 'stats overview manual refresh must update dynamic date windows')
assert.match(statsViewSource, /<a-button\b(?=[^>]*\s:loading="loading")(?=[^>]*\s@click="refreshData")[^>]*>/, 'stats overview refresh button must invoke manual refresh while loading state is wired')

assert.match(systemMetricsViewSource, /document\.addEventListener\('visibilitychange', handleDynamicRangeVisibilityChange\)/, 'system metrics must refresh dynamic ranges when returning to a visible tab')
assert.match(systemMetricsViewSource, /window\.addEventListener\('focus', refreshDynamicRangeAfterRollover\)/, 'system metrics must refresh dynamic ranges on window focus')
assert.match(systemMetricsViewSource, /millisecondsUntilNextStatsDay[\s\S]*dynamicRangeRolloverTimer = window\.setTimeout/, 'system metrics must schedule the next server-timezone day boundary')
assert.match(systemMetricsViewSource, /forceUsageWindow: true/, 'system metrics dynamic lifecycle must force-refresh the server usage window')
