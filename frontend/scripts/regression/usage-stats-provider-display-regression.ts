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

console.log('用量统计供应商展示回归通过：统计页不直接依赖 GPT/OpenAI/Anthropic 常量')

function readSource(relativePath: string): string {
  return readFileSync(resolve(frontendRoot, relativePath), 'utf8')
}
