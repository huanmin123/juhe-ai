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

assert.match(
  usageStatsWindowSource,
  /type\s+UsageStatsWindowLoadOptions\s*=\s*\{[\s\S]*force\?:\s*boolean[\s\S]*\}/,
  'usage stats window loader must expose a force refresh option'
)

assert.match(
  usageStatsWindowSource,
  /Date\.now\(\)\s*-\s*windowLoadedAtMs\s*<\s*windowCacheTtlMs/,
  'usage stats window cache must expire instead of staying process-lifetime stale'
)

assert.match(
  statsViewSource,
  /function\s+selectedRangeParams\(\):\s*\{ startDate\?: string; endDate\?: string \}\s*\{[\s\S]*if\s*\(!dateRangeExplicit\.value\)\s*return\s*\{\}/,
  'stats overview must not submit browser-local dates when the range is not explicit'
)

assert.match(
  statsViewSource,
  /loadUsageStatsWindow\(\{ force: true \}\)/,
  'stats overview loads must force-refresh the stats window'
)

assert.match(
  statsViewSource,
  /async\s+function\s+handleQuickRangeChange\(value: string \| number\)\s*\{[\s\S]*await\s+loadUsageStatsWindow\(\{ force: true \}\)[\s\S]*quickRangeDateRange/,
  'stats overview quick ranges must refresh the window before calculating today'
)

assert.match(
  systemMetricsViewSource,
  /await\s+loadUsageStatsWindow\(\{ force: true \}\)[\s\S]*const rangeParams = selectedRangeParams\(\)/,
  'system metrics must refresh the stats window before building default date params'
)

assert.match(
  systemMetricsViewSource,
  /function\s+selectedRangeParams\(\):\s*\{ startDate\?: string; endDate\?: string \}\s*\{[\s\S]*if\s*\(!dateRangeExplicit\.value\)[\s\S]*statsWindowEndDate/,
  'system metrics default range must use the server stats window end date'
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
