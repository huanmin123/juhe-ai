import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const helperSource = readFileSync(fileURLToPath(new URL('../../views/stats/statsPageDataResource.ts', import.meta.url)), 'utf8')
const statsSource = readFileSync(fileURLToPath(new URL('../../views/stats/StatsView.vue', import.meta.url)), 'utf8')
const usageStatsSource = readFileSync(fileURLToPath(new URL('../../views/usage-stats/UsageStatsView.vue', import.meta.url)), 'utf8')
const aiPerformanceSource = readFileSync(fileURLToPath(new URL('../../views/ai-performance/AiPerformanceView.vue', import.meta.url)), 'utf8')

assert.match(helperSource, /getDefaultPageDataResourceCache/, '统计资源应复用统一 IndexedDB page-data resource cache')
assert.match(helperSource, /pageDataApi\.confirm/, '统计缓存命中后应只调用 page-data confirm')
assert.match(helperSource, /authState\.currentUser/, '统计缓存 scope 应包含当前用户和角色')
assert.match(statsSource, /domain:\s*'stats\.overview'/, '统计总览应接入 stats.overview domain')
assert.match(usageStatsSource, /domain:\s*'stats\.accountUsage'/, '账户用量页应接入 stats.accountUsage domain')
assert.match(aiPerformanceSource, /domain:\s*'stats\.aiPerformance'/, 'AI 性能页应接入 stats.aiPerformance domain')

console.log('统计首屏 IndexedDB 缓存接线回归通过：总览、账户用量、AI 性能使用独立 domain')
