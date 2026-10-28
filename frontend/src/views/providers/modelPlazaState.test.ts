import { describe, expect, it } from 'vitest'

import type { ProviderPlazaMeta, ProviderModelPricing } from '@/types/domain'

import { getModelCategory } from './providerModelFormatters'
import {
  buildPlazaProvidersMap,
  buildPlazaVendorTabs,
  clampPlazaPage,
  filterPlazaModels,
  isAllVendor
} from './modelPlazaState'

// 契约：docs/functions/模型广场页与供应商目录退役设计.md §4.1（厂商标签/筛选管线/分页收敛）。
// 组件渲染契约由回归脚本做源码级断言，本文件覆盖广场页纯逻辑域。

function providerMeta(overrides: Partial<ProviderPlazaMeta> = {}): ProviderPlazaMeta {
  return { code: 'gpt', name: 'OpenAI', enabled: true, defaultHealthCheckModel: '', ...overrides }
}

function plazaModel(overrides: Partial<ProviderModelPricing> = {}): ProviderModelPricing {
  return {
    providerCode: 'gpt',
    model: 'gpt-5.5',
    source: 'built-in',
    scope: 'built_in',
    status: 'active',
    supportsPromptCaching: false,
    supportsServiceTier: false,
    mode: 'text',
    defaultReasoningEffort: null,
    ...overrides
  }
}

describe('buildPlazaVendorTabs（厂商标签 pills）', () => {
  it('「全部」打头并聚合总数，各供应商按摘要顺序携带行数；无行启用供应商也出标签', () => {
    const providers = [
      providerMeta({ code: 'gpt', name: 'OpenAI' }),
      providerMeta({ code: 'deepseek', name: 'DeepSeek' }),
      providerMeta({ code: 'legacy', name: 'Legacy', enabled: false })
    ]
    const items = [
      plazaModel({ providerCode: 'gpt', model: 'gpt-5.5' }),
      plazaModel({ providerCode: 'gpt', model: 'gpt-5.5-pro' }),
      plazaModel({ providerCode: 'deepseek', model: 'deepseek-v4' })
    ]
    expect(buildPlazaVendorTabs(providers, items)).toEqual([
      { code: 'all', name: '全部', count: 3, enabled: true },
      { code: 'gpt', name: 'OpenAI', count: 2, enabled: true },
      { code: 'deepseek', name: 'DeepSeek', count: 1, enabled: true },
      { code: 'legacy', name: 'Legacy', count: 0, enabled: false }
    ])
  })

  it('isAllVendor 只认 all 哨兵', () => {
    expect(isAllVendor('all')).toBe(true)
    expect(isAllVendor('gpt')).toBe(false)
  })
})

describe('buildPlazaProvidersMap（供应商徽标/默认检查查表）', () => {
  it('按 code 建映射，供卡片供应商徽标与 defaultHealthCheckModel 派生', () => {
    const map = buildPlazaProvidersMap([
      providerMeta({ code: 'gpt', name: 'OpenAI', defaultHealthCheckModel: 'gpt-5.2' })
    ])
    expect(map.get('gpt')?.defaultHealthCheckModel).toBe('gpt-5.2')
    expect(map.get('deepseek')).toBeUndefined()
  })
})

describe('filterPlazaModels（厂商 × 用途 × 关键词组合过滤）', () => {
  const items = [
    plazaModel({ providerCode: 'gpt', model: 'gpt-5.5' }),
    plazaModel({ providerCode: 'gpt', model: 'gpt-image-2', mode: 'image_generation' }),
    plazaModel({ providerCode: 'deepseek', model: 'deepseek-v4-chat' }),
    plazaModel({ providerCode: 'deepseek', model: 'deepseek-ocr', mode: 'image_generation' })
  ]

  it('厂商 + 用途组合：只留该供应商该分类的行', () => {
    const filtered = filterPlazaModels(items, { vendor: 'gpt', category: 'image', keyword: '' })
    expect(filtered.map((item) => item.model)).toEqual(['gpt-image-2'])
  })

  it('all 厂商跨供应商聚合：同名/同分类行都保留（跨供应商不合并）', () => {
    const filtered = filterPlazaModels(items, { vendor: 'all', category: 'image', keyword: '' })
    expect(filtered.map((item) => item.model)).toEqual(['gpt-image-2', 'deepseek-ocr'])
  })

  it('关键词与厂商、用途叠加生效', () => {
    const matched = filterPlazaModels(items, { vendor: 'all', category: 'text', keyword: 'deepseek' })
    expect(matched.map((item) => item.model)).toEqual(['deepseek-v4-chat'])
    expect(filterPlazaModels(items, { vendor: 'gpt', category: 'text', keyword: 'deepseek' })).toEqual([])
  })

  it('分类判定与 getModelCategory 同源（mode 别名 audio_speech 归音频）', () => {
    const audio = plazaModel({ model: 'tts-1', mode: 'audio_speech' })
    expect(getModelCategory(audio)).toBe('audio')
    expect(filterPlazaModels([audio], { vendor: 'all', category: 'audio', keyword: '' })).toHaveLength(1)
  })
})

describe('clampPlazaPage（分页越界收敛）', () => {
  it('越界收敛到尾页、下界收敛到 1，24/48/96 均按 ceil 收敛', () => {
    expect(clampPlazaPage(9, 24, 100)).toBe(5)
    expect(clampPlazaPage(0, 24, 100)).toBe(1)
    expect(clampPlazaPage(2, 48, 30)).toBe(1)
    expect(clampPlazaPage(3, 96, 97)).toBe(2)
    expect(clampPlazaPage(1, 96, 97)).toBe(1)
    expect(clampPlazaPage(3, 24, 0)).toBe(1)
  })
})
