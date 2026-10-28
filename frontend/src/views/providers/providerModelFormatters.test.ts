import { describe, expect, it } from 'vitest'

import {
  formatModelServiceTier,
  formatSpec,
  modelInfoItems,
  modelPriceSlots,
  modelPriceTable,
  trimNumber,
  type ModelPriceSlot
} from './providerModelFormatters'
import type { ProviderModelCatalogDisplayItem, ProviderModelCatalogDisplaySection, ProviderModelPricing } from '@/types/domain'

function providerModel(overrides: Partial<ProviderModelPricing> = {}): ProviderModelPricing {
  return {
    providerCode: 'gpt',
    model: 'gpt-5.5',
    source: 'built-in',
    scope: 'built_in',
    status: 'active',
    supportsPromptCaching: false,
    supportsServiceTier: false,
    defaultReasoningEffort: null,
    ...overrides
  }
}

function pricingEntry(key: string, label: string, value: number): ProviderModelCatalogDisplayItem {
  return { key, label, format: 'usd_per_1m_tokens', value }
}

describe('modelPriceSlots', () => {
  it('text 分类按候选序列输出输入/输出/缓存读三槽', () => {
    expect(modelPriceSlots(providerModel({
      inputUsdPer1M: 1.25,
      outputUsdPer1M: 2.5,
      cachedInputUsdPer1M: 0.1
    }))).toEqual<ModelPriceSlot[]>([
      { label: '输入', value: '1.25', unit: '/1M tokens' },
      { label: '输出', value: '2.5', unit: '/1M tokens' },
      { label: '缓存读', value: '0.1', unit: '/1M tokens' }
    ])
  })

  it('audio 分类命中字符价槽位（语音合成）', () => {
    expect(modelPriceSlots(providerModel({
      mode: 'audio',
      ttsInputUsdPer1MChars: 20
    }))).toEqual<ModelPriceSlot[]>([
      { label: '语音合成', value: '20', unit: '/1M 字符' }
    ])
  })

  it('audio 分类命中秒价槽位（语音识别）', () => {
    expect(modelPriceSlots(providerModel({
      mode: 'audio',
      audioInputUsdPerSecond: 0.06
    }))).toEqual<ModelPriceSlot[]>([
      { label: '语音识别', value: '0.06', unit: '/秒' }
    ])
  })

  it('video 分类秒价与次价互斥，各取存在者', () => {
    expect(modelPriceSlots(providerModel({
      mode: 'video',
      videoOutputUsdPerSecond: 0.5
    }))).toEqual<ModelPriceSlot[]>([
      { label: '视频（每秒）', value: '0.5', unit: '/秒' }
    ])
    expect(modelPriceSlots(providerModel({
      mode: 'video',
      videoOutputUsdPerCall: 1.2
    }))).toEqual<ModelPriceSlot[]>([
      { label: '视频（每次）', value: '1.2', unit: '/次' }
    ])
  })

  it('image 分类优先每张输出价', () => {
    expect(modelPriceSlots(providerModel({
      mode: 'image',
      outputUsdPerImage: 0.04
    }))).toEqual<ModelPriceSlot[]>([
      { label: '每张输出', value: '0.04', unit: '/张' }
    ])
  })

  it('存在字段超过 3 个时截断为前 3 个', () => {
    const slots = modelPriceSlots(providerModel({
      mode: 'audio',
      audioInputUsdPer1M: 1,
      audioOutputUsdPer1M: 2,
      ttsInputUsdPer1MChars: 3,
      audioInputUsdPerSecond: 4,
      inputUsdPer1M: 5
    }))
    expect(slots.map((slot) => slot.label)).toEqual(['音频输入', '音频输出', '语音合成'])
  })

  it('分类候选 0 槽时按全字段回退：text 误分类行回退显示真实媒体价（C1）', () => {
    expect(modelPriceSlots(providerModel({
      ttsInputUsdPer1MChars: 20
    }))).toEqual<ModelPriceSlot[]>([
      { label: '语音合成', value: '20', unit: '/1M 字符' }
    ])
  })

  it('分类候选 0 槽时回退多字段按回退序列取两槽', () => {
    expect(modelPriceSlots(providerModel({
      audioInputUsdPer1M: 1,
      audioOutputUsdPer1M: 2
    })).map((slot) => [slot.label, slot.value, slot.unit])).toEqual([
      ['音频输入', '1', '/1M tokens'],
      ['音频输出', '2', '/1M tokens']
    ])
  })

  it('全部直价字段为空才返回空数组', () => {
    expect(modelPriceSlots(providerModel())).toEqual([])
  })

  it('显式 0 是真实价，必须保留', () => {
    expect(modelPriceSlots(providerModel({ inputUsdPer1M: 0 }))).toEqual<ModelPriceSlot[]>([
      { label: '输入', value: '0', unit: '/1M tokens' }
    ])
  })

  it('极大值（1e15 级）按 trimNumber 口径格式化', () => {
    expect(modelPriceSlots(providerModel({ inputUsdPer1M: 1e15 }))).toEqual<ModelPriceSlot[]>([
      { label: '输入', value: '1000000000000000', unit: '/1M tokens' }
    ])
  })

  it('后端投影呈单 Token 条目时合并为单槽 Token', () => {
    expect(modelPriceSlots(providerModel({
      inputUsdPer1M: 2,
      outputUsdPer1M: 2,
      catalogDisplay: [
        { key: 'token_pricing', label: 'Token 计费', items: [{ key: 'token', label: 'Token', format: 'usd_per_1m_tokens', value: 2 }] }
      ]
    }))).toEqual<ModelPriceSlot[]>([
      { label: 'Token', value: '2', unit: '/1M tokens' }
    ])
  })

  it('无单 Token 投影的同值行保持输入/输出两槽', () => {
    expect(modelPriceSlots(providerModel({ inputUsdPer1M: 2, outputUsdPer1M: 2 })).map((slot) => slot.label))
      .toEqual(['输入', '输出'])
  })

  it('token_pricing 分组含多条目或键不符时不合并', () => {
    const multiItem = providerModel({
      inputUsdPer1M: 2,
      catalogDisplay: [
        {
          key: 'token_pricing',
          label: 'Token 计费',
          items: [
            { key: 'input', label: '输入', format: 'usd_per_1m_tokens', value: 2 },
            { key: 'output', label: '输出', format: 'usd_per_1m_tokens', value: 2 }
          ]
        }
      ]
    })
    expect(modelPriceSlots(multiItem).map((slot) => slot.label)).toEqual(['输入'])
  })
})

describe('trimNumber（数值格式化最多 2 位小数，模型目录卡片化与详情抽屉设计 §5.1）', () => {
  it('|v| ≥ 0.01 走 toFixed(2) 去尾零', () => {
    expect(trimNumber(0.28571429)).toBe('0.29')
    expect(trimNumber(1.14285714)).toBe('1.14')
    expect(trimNumber(0.01)).toBe('0.01')
    expect(trimNumber(2.5)).toBe('2.5')
    expect(trimNumber(1e15)).toBe('1000000000000000')
  })

  it('0 < |v| < 0.01 保留 2 位有效数字，小单价不被舍成 0.01 或 0', () => {
    expect(trimNumber(0.00571429)).toBe('0.0057')
    expect(trimNumber(0.00083333)).toBe('0.00083')
    expect(trimNumber(0.0099)).toBe('0.0099')
    expect(trimNumber(0.004)).toBe('0.004')
  })

  it('0 显示 0（真实免费价，不得 falsy 过滤）', () => {
    expect(trimNumber(0)).toBe('0')
  })
})

describe('modelPriceTable / modelInfoItems（抽屉「计费价格」一张表 + 「基础信息」，§6.2-6.4）', () => {
  it('① DeepSeek 形态（token+offpeak+容量+思考）：两列「正常价 | 高峰价」，正常价=offpeak 值、高峰价=token 值', () => {
    const display: ProviderModelCatalogDisplaySection[] = [
      {
        key: 'token_pricing',
        label: 'Token 计费',
        items: [pricingEntry('cache_miss', '输入', 0.28571429), pricingEntry('cache_hit', '缓存读', 0.02857143), pricingEntry('output', '输出', 1.14285714)]
      },
      {
        key: 'offpeak_pricing',
        label: '正常价',
        items: [pricingEntry('cache_miss', '输入', 0.142857145), pricingEntry('cache_hit', '缓存读', 0.014285715), pricingEntry('output', '输出', 0.57142857)]
      },
      { key: 'reasoning', label: '思考能力', items: [{ key: 'levels', label: '级别', format: 'text', value: 'high' }] },
      { key: 'capacity', label: '容量', items: [{ key: 'context', label: '上下文', format: 'tokens', value: 131_072 }] }
    ]
    const item = providerModel({ catalogDisplay: display })
    const table = modelPriceTable(item)
    expect(table.hasOffpeak).toBe(true)
    expect(table.columns.map((column) => [column.key, column.label])).toEqual([
      ['normal', '正常价'],
      ['peak', '高峰价']
    ])
    expect(table.rows.map((row) => [row.key, row.label, row.cells.normal?.value, row.cells.peak?.value])).toEqual([
      ['cache_miss', '输入', 0.142857145, 0.28571429],
      ['cache_hit', '缓存读', 0.014285715, 0.02857143],
      ['output', '输出', 0.57142857, 1.14285714]
    ])
    // 非价格分组进基础信息且保持后端顺序；价格档不进。
    expect(modelInfoItems(item).map((group) => group.key)).toEqual(['reasoning', 'capacity'])
  })

  it('② 带 tier_priority 的 gpt 形态（无高峰档）：单列「正常价」+ Priority 列，cells 按 key 配对', () => {
    const table = modelPriceTable(providerModel({
      catalogDisplay: [
        {
          key: 'token_pricing',
          label: 'Token 计费',
          items: [pricingEntry('input', '输入', 1.25), pricingEntry('output', '输出', 10)]
        },
        {
          key: 'tier_priority',
          label: 'Priority',
          items: [pricingEntry('input', '输入', 2.5), pricingEntry('output', '输出', 20)]
        }
      ]
    }))
    expect(table.columns.map((column) => [column.key, column.label])).toEqual([['normal', '正常价'], ['priority', 'Priority']])
    expect(table.rows.map((row) => [row.key, row.cells.normal?.value, row.cells.priority?.value])).toEqual([
      ['input', 1.25, 2.5],
      ['output', 10, 20]
    ])
    expect(table.hasOffpeak).toBe(false)
  })

  it('③ 只有 token_pricing：单列「正常价」；tier 行缺配对落 undefined（UI 显示 -）', () => {
    const single = modelPriceTable(providerModel({
      catalogDisplay: [{ key: 'token_pricing', label: 'Token 计费', items: [pricingEntry('input', '输入', 1)] }]
    }))
    expect(single.columns.map((column) => [column.key, column.label])).toEqual([['normal', '正常价']])
    expect(single.rows).toHaveLength(1)
    expect(single.hasOffpeak).toBe(false)

    const mismatched = modelPriceTable(providerModel({
      catalogDisplay: [
        { key: 'token_pricing', label: 'Token 计费', items: [pricingEntry('input', '输入', 1), pricingEntry('cache_write', '缓存写入', 2)] },
        { key: 'tier_flex', label: 'Flex', items: [pricingEntry('input', '输入', 0.5)] }
      ]
    }))
    expect(mismatched.columns.map((column) => column.key)).toEqual(['normal', 'flex'])
    expect(mismatched.rows.find((row) => row.key === 'cache_write')?.cells.flex).toBeUndefined()
  })

  it('④ image_generation/media_pricing 条目追加为额外行（key 冲突时 section 前缀去重），无高峰档时 normal=token 值', () => {
    const table = modelPriceTable(providerModel({
      catalogDisplay: [
        { key: 'token_pricing', label: 'Token 计费', items: [pricingEntry('input', '输入', 1)] },
        { key: 'image_generation', label: '图片生成', items: [{ key: 'output_image', label: '每张', format: 'usd_per_image', value: 0.04 }] },
        { key: 'media_pricing', label: '媒体计费', items: [{ key: 'tts_chars', label: '语音合成（每百万字符）', format: 'text', value: '$4.2857' }] }
      ]
    }))
    expect(table.rows.map((row) => row.key)).toEqual(['input', 'output_image', 'tts_chars'])
    expect(table.rows[0]?.cells.normal?.value).toBe(1)
    expect(table.rows[1]?.cells.normal?.format).toBe('usd_per_image')
  })

  it('⑤ banned 分组（旧后端响应兜底）两函数都不输出；空 catalogDisplay 双空', () => {
    const item = providerModel({
      catalogDisplay: [
        { key: 'token_pricing', label: 'Token 计费', items: [pricingEntry('input', '输入', 1)] },
        { key: 'currency_conversion', label: '美元换算', items: [{ key: 'source_currency', label: '官方币种', format: 'text', value: 'CNY' }] },
        { key: 'source_disclosure', label: '来源披露', items: [{ key: 'source_note', label: '官方源价', format: 'text', value: '官方价' }] }
      ]
    })
    expect(modelPriceTable(item).rows.map((row) => row.key)).toEqual(['input'])
    expect(modelInfoItems(item)).toEqual([])
    const empty = providerModel()
    expect(modelPriceTable(empty).rows).toEqual([])
    expect(modelInfoItems(empty)).toEqual([])
  })
})

describe('formatSpec', () => {
  it('0 或缺失显示 -', () => {
    expect(formatSpec(0)).toBe('-')
    expect(formatSpec(undefined)).toBe('-')
    expect(formatSpec(null)).toBe('-')
  })

  it('千位以下原样显示', () => {
    expect(formatSpec(999)).toBe('999')
    expect(formatSpec(1)).toBe('1')
  })

  it('千进制量级单位去尾零', () => {
    expect(formatSpec(1_000)).toBe('1k')
    expect(formatSpec(128_000)).toBe('128k')
    expect(formatSpec(1_500)).toBe('1.5k')
    expect(formatSpec(1_000_000)).toBe('1m')
    expect(formatSpec(1_048_576)).toBe('1m')
  })
})

describe('formatModelServiceTier', () => {
  it('priority/flex 映射展示名，未知 key 原样透传', () => {
    expect(formatModelServiceTier('priority')).toBe('Priority')
    expect(formatModelServiceTier('flex')).toBe('Flex')
    expect(formatModelServiceTier('scale')).toBe('scale')
  })
})
