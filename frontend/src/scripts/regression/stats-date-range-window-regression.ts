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
  statsViewSource,
  /function\s+selectedRangeParams\(\):\s*\{ startDate\?: string; endDate\?: string \}\s*\{[\s\S]*if\s*\(!dateRangeExplicit\.value\)\s*return\s*\{\}/,
  'stats overview must not submit browser-local dates when the range is not explicit'
)

assert.match(
  statsViewSource,
  /loadUsageStatsWindow\(\{[\s\S]*force:\s*options\.force === true,[\s\S]*viewScope:\s*isManagementView\.value \? 'admin' : 'self'[\s\S]*\}\)/,
  'stats overview loads must use the scoped stats window and let manual refresh bypass its metadata cache'
)

assert.match(
  statsViewSource,
  /async\s+function\s+handleQuickRangeChange\(value: string \| number\)\s*\{[\s\S]*await\s+loadUsageStatsWindow\(\{\s*viewScope:\s*isManagementView\.value \? 'admin' : 'self'\s*\}\)[\s\S]*quickRangeDateRange/,
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
  /void\s+loadUsageStatsWindow\(\{\s*force,\s*viewScope:\s*'admin'\s*\}\)[\s\S]*return\s+loadData\(\)/,
  'system metrics must start the scoped cached-window request without blocking its trend request'
)

assert.match(
  systemMetricsViewSource,
  /api\.stats\.systemMetricsTrend\(rangeParams\)/,
  'system metrics must load the split trend endpoint'
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
  /const windowScope = isManagementView\.value \? 'admin' : 'self'[\s\S]*await\s+loadUsageStatsWindow\(\{[\s\S]*force:\s*options\.force === true,[\s\S]*viewScope:\s*windowScope[\s\S]*\}\)[\s\S]*loadPerformanceBase\(\)/,
  'AI performance must use the scoped cached stats window before loading its base data'
)

assert.match(
  aiPerformanceViewSource,
  /function\s+currentPerformanceRequest[\s\S]*systemAccountId:\s*selectedPerformanceSystemAccountId\(\)/,
  'AI performance requests must include the selected system account after the stats window resolves'
)

assert.match(
  aiPerformanceViewSource,
  /function\s+selectedRangeParams\(\):\s*\{ startDate\?: string; endDate\?: string \}\s*\{[\s\S]*if\s*\(!dateRangeExplicit\.value\)[\s\S]*defaultDateRange\(\)/,
  'AI performance default range must be derived after the stats window is current'
)

assert.match(
  ipStatsViewSource,
  /await\s+loadUsageStatsWindow\(\{[\s\S]*force:\s*options\.force === true,[\s\S]*viewScope:\s*'admin'[\s\S]*\}\)[\s\S]*api\.ipStats\.list\(buildListParams\(\)\)/,
  'IP stats must use the scoped cached stats window before calculating segmented date ranges'
)

assert.match(usageStatsWindowSource, /lastLoadFailed:\s*boolean/, 'usage-window must retain an explicit failure state')
assert.match(usageStatsWindowSource, /scopeState\.value = undefined[\s\S]*scopeState\.lastLoadFailed = true/, 'failed usage-window requests must not be cached as successful fallback windows')
assert.match(aiPerformanceViewSource, /didUsageStatsWindowLoadFail\(windowScope\)/, 'AI performance must stop before using a failed server window')
assert.match(ipStatsViewSource, /didUsageStatsWindowLoadFail\('admin'\)/, 'IP stats must stop before using a failed server window')
