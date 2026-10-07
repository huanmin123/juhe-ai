import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')

const usageStatsViewSource = readSource('src/views/usage-stats/UsageStatsView.vue')
const usageStatsHelpersSource = readSource('src/views/usage-stats/usageStatsHelpers.ts')
const usageStatsPageConfigSource = readSource('src/views/usage-stats/usageStatsPageConfig.ts')
const usageTrendChartOptionsSource = readSource('src/views/usage-stats/usageTrendChartOptions.ts')
const accountUsageStatsTableSource = readSource('src/views/usage-stats/AccountUsageStatsTable.vue')

for (const [name, source] of [
  ['UsageStatsView.vue', usageStatsViewSource],
  ['usageStatsHelpers.ts', usageStatsHelpersSource],
  ['usageStatsPageConfig.ts', usageStatsPageConfigSource],
  ['usageTrendChartOptions.ts', usageTrendChartOptionsSource]
] as const) {
  assert.doesNotMatch(
    source,
    /GPT_VENDOR_CODE|ANTHROPIC_PROVIDER_CODE|OPENAI_COMPATIBLE_PROVIDER_CODE|isGptVendorCode|isOpenAICompatibleProviderCode|isAnthropicProtocolProfile/,
    `${name} 不应直接持有供应商常量或协议判断`
  )
}

assert.match(usageStatsViewSource, /providerDisplayName/, '用量统计页应通过通用 providerDisplayName 展示供应商')
assert.match(usageStatsPageConfigSource, /dataIndex: 'providerCode'/, '用量统计表只应保留 providerCode 作为通用展示字段')
assert.doesNotMatch(usageStatsPageConfigSource, /媒体用量/, '账户统计明细不应保留独立媒体用量列')
assert.doesNotMatch(usageStatsPageConfigSource, /title: 'Token'/, '账户统计用量列标题不再以 Token 命名')
assert.match(usageStatsPageConfigSource, /title: '用量', key: 'tokens'/, '账户统计用量列标题统一为「用量」')
assert.doesNotMatch(accountUsageStatsTableSource, /column\.key === 'media'/, '账户统计明细不得保留媒体用量模板分支')
assert.match(
  accountUsageStatsTableSource,
  /v-if="record\.rangeUsage\.totalTokens > 0 \|\| !usageRecordHasMediaMetering\(record\.rangeUsage\)"/,
  '纯媒体聚合行不得展示 0 Token 总数'
)
assert.match(
  accountUsageStatsTableSource,
  /column\.key === 'tokens'[\s\S]*?usageRecordMediaParts\(record\.rangeUsage\)/,
  '聚合行的媒体计量必须堆叠在 Token 列内'
)

console.log('用量统计供应商展示回归通过：统计页不直接依赖 GPT/OpenAI/Anthropic 常量')

function readSource(relativePath: string): string {
  return readFileSync(resolve(frontendRoot, relativePath), 'utf8')
}
