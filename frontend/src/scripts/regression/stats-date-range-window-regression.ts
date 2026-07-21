import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import assert from 'node:assert/strict'

const frontendRoot = resolve(import.meta.dirname, '..', '..')
const usageStatsWindowSource = readFileSync(resolve(frontendRoot, 'composables', 'useUsageStatsWindow.ts'), 'utf8')
const statsViewSource = readFileSync(resolve(frontendRoot, 'views', 'stats', 'StatsView.vue'), 'utf8')
const systemMetricsViewSource = readFileSync(resolve(frontendRoot, 'views', 'stats', 'SystemMetricsStatsView.vue'), 'utf8')
const usageStatsViewSource = readFileSync(resolve(frontendRoot, 'views', 'usage-stats', 'UsageStatsView.vue'), 'utf8')
const usageStatsPageConfigSource = readFileSync(resolve(frontendRoot, 'views', 'usage-stats', 'usageStatsPageConfig.ts'), 'utf8')
const aiPerformanceViewSource = readFileSync(resolve(frontendRoot, 'views', 'ai-performance', 'AiPerformanceView.vue'), 'utf8')
const ipStatsViewSource = readFileSync(resolve(frontendRoot, 'views', 'ip-stats', 'IpStatsView.vue'), 'utf8')
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
  /loadUsageStatsWindow\(\)/,
  'stats overview loads must reuse the shared stats window cache'
)

assert.match(
  statsViewSource,
  /async\s+function\s+handleQuickRangeChange\(value: string \| number\)\s*\{[\s\S]*await\s+loadUsageStatsWindow\(\)[\s\S]*quickRangeDateRange/,
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
  /Promise\.all\(\[[\s\S]*api\.stats\.systemMetrics\(rangeParams\)[\s\S]*loadUsageStatsWindow\(\)/,
  'system metrics must load the cached window and business data in parallel'
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
  usageStatsPageConfigSource,
  /dateRange\?:\s*readonly\s*\[string,\s*string\]/,
  'account usage stats params must allow omitted date ranges'
)

assert.match(
  aiPerformanceViewSource,
  /await\s+loadUsageStatsWindow\(\{ force: true \}\)[\s\S]*const systemAccountId = selectedPerformanceSystemAccountId\(\)/,
  'AI performance must refresh the stats window before building default date params'
)

assert.match(
  aiPerformanceViewSource,
  /function\s+selectedRangeParams\(\):\s*\{ startDate\?: string; endDate\?: string \}\s*\{[\s\S]*if\s*\(!dateRangeExplicit\.value\)[\s\S]*defaultDateRange\(\)/,
  'AI performance default range must be derived after the stats window is current'
)

assert.match(
  ipStatsViewSource,
  /await\s+loadUsageStatsWindow\(\{ force: true \}\)[\s\S]*api\.ipStats\.list\(buildListParams\(\)\)/,
  'IP stats must refresh the stats window before calculating segmented date ranges'
)
