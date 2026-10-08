import { readFileSync } from 'node:fs'
import path from 'node:path'

import { describe, expect, it } from 'vitest'

import { cacheRateTagText, cacheRateTone } from './usageCacheRateTag'

// 契约：docs/functions/缓存率感知调度与用量缓存率展示设计.md 第 9.2、9.3 节。
// 前端只测格式化与渲染契约，不复制 provider 分母逻辑。
// 注：vitest 配置未接入 @vitejs/plugin-vue（依赖也无 @vue/test-utils），
// .vue 文件无法在单测中导入挂载，组件渲染契约按项目回归脚本惯例做源码级断言。

const componentSource = readFileSync(path.resolve(process.cwd(), 'src/components/UsageSummaryTags.vue'), 'utf8')

describe('usageCacheRateTag 纯 helper', () => {
  it('颜色分级：90%/80% 阈值两侧与异常高值', () => {
    expect(cacheRateTone(0.9)).toBe('green')
    expect(cacheRateTone(0.91)).toBe('green')
    expect(cacheRateTone(0.89)).toBe('yellow')
    expect(cacheRateTone(0.8)).toBe('yellow')
    expect(cacheRateTone(0.81)).toBe('yellow')
    expect(cacheRateTone(0.79)).toBe('red')
    expect(cacheRateTone(0)).toBe('red')
    expect(cacheRateTone(1.25)).toBe('green')
  })

  it('tag 文案：只显示百分比本身，不带前缀', () => {
    expect(cacheRateTagText(0.873)).toBe('87.3%')
    expect(cacheRateTagText(0)).toBe('0.0%')
    expect(cacheRateTagText(0.45)).toBe('45.0%')
    expect(cacheRateTagText(0.1234)).toBe('12.3%')
  })

  it('tag 文案：>100% 异常值原样展示不截断', () => {
    expect(cacheRateTagText(1.234)).toBe('123.4%')
    expect(cacheRateTagText(2)).toBe('200.0%')
  })
})

describe('UsageSummaryTags 组件模板契约（源码级）', () => {
  const legacyTagLines = [
    '<a-tag class="usage-summary-tag">{{ formatRequestCountTag(usage?.requestCount) }}</a-tag>',
    '<a-tag class="usage-summary-tag">{{ formatCompactUsageAmount(usage?.totalTokens) }}</a-tag>',
    '<a-tag class="usage-summary-tag">{{ formatUsd(usage?.totalCost) }}</a-tag>',
  ]
  const templateSource = componentSource.slice(componentSource.indexOf('<template>'), componentSource.indexOf('</template>'))

  it('既有三个 tag 的模板、类名、顺序保持不变，且位于缓存 tag 之前', () => {
    let cursor = -1
    for (const line of legacyTagLines) {
      const index = templateSource.indexOf(line)
      expect(index, `缺失既有 tag 模板行：${line}`).toBeGreaterThan(-1)
      expect(index, `既有 tag 模板行顺序被改变：${line}`).toBeGreaterThan(cursor)
      cursor = index
    }
  })

  it('缓存 tag 追加在既有 tag 之后，默认（不传新 props）不渲染，且无 tooltip 包裹', () => {
    const lastLegacyIndex = templateSource.indexOf(legacyTagLines[2])
    const cacheTagIndex = templateSource.indexOf('cacheRateTagText(cacheRate)', lastLegacyIndex)
    expect(cacheTagIndex, '缓存 tag 必须位于既有三个 tag 之后').toBeGreaterThan(lastLegacyIndex)

    const appendedBlock = templateSource.slice(templateSource.indexOf('<a-tag', lastLegacyIndex), templateSource.indexOf('</a-tag>', cacheTagIndex) + '</a-tag>'.length)
    expect(appendedBlock).toContain('v-if="showCacheRate && cacheRate != null"')
    expect(appendedBlock).toContain('class="usage-summary-tag"')
    expect(appendedBlock).toContain(':color="cacheRateTone(cacheRate)"')
    expect(appendedBlock).not.toContain('a-tooltip')
    expect(appendedBlock).not.toContain(':title')
    expect(templateSource).not.toContain('cacheRateWindowLabel')
    expect(appendedBlock).not.toContain('providerCode')
  })

  it('缓存 tag 不读取 providerCode、不自行计算分母', () => {
    expect(componentSource).not.toContain('providerCode')
    expect(componentSource).not.toContain('inputTokens')
    expect(componentSource).not.toContain('cacheReadTokens')
  })
})
