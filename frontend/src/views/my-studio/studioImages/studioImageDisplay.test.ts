import { describe, expect, it } from 'vitest'

import type { StudioAsset, StudioModelOption } from '@/types/domain'
import {
  formatImageBytes,
  formatImageDimensions,
  groupImageGalleryByDate,
  pickStudioImageParams,
  studioImageCostEstimate
} from './studioImageDisplay'

function imageAsset(overrides: Partial<StudioAsset> = {}): StudioAsset {
  return {
    id: 'stasset_1',
    kind: 'image',
    mime: 'image/png',
    bytes: 2048,
    width: 1024,
    height: 768,
    durationMs: null,
    sha256: 'abc',
    sourceTaskId: null,
    params: {},
    createdAt: '2026-10-12T02:00:00.000Z',
    ...overrides
  }
}

describe('studioImageDisplay 画廊日期分组', () => {
  const now = new Date('2026-10-12T12:00:00.000+08:00').getTime()

  it('同日资产合入同一分组，标签为今天', () => {
    const groups = groupImageGalleryByDate([
      imageAsset({ id: 'a', createdAt: '2026-10-12T03:00:00.000+08:00' }),
      imageAsset({ id: 'b', createdAt: '2026-10-12T09:00:00.000+08:00' })
    ], now)
    expect(groups).toHaveLength(1)
    expect(groups[0].dateLabel).toBe('今天')
    expect(groups[0].assets.map((asset) => asset.id)).toEqual(['a', 'b'])
  })

  it('跨日资产按先后形成多个分组，历史日期用 YYYY-MM-DD', () => {
    const groups = groupImageGalleryByDate([
      imageAsset({ id: 'today', createdAt: '2026-10-12T09:00:00.000+08:00' }),
      imageAsset({ id: 'yesterday', createdAt: '2026-10-11T09:00:00.000+08:00' }),
      imageAsset({ id: 'older', createdAt: '2026-09-30T09:00:00.000+08:00' }),
      imageAsset({ id: 'older2', createdAt: '2026-09-30T10:00:00.000+08:00' })
    ], now)
    expect(groups.map((group) => group.dateLabel)).toEqual(['今天', '昨天', '2026-09-30'])
    expect(groups[2].assets.map((asset) => asset.id)).toEqual(['older', 'older2'])
  })

  it('空列表返回空分组', () => {
    expect(groupImageGalleryByDate([], now)).toEqual([])
  })
})

describe('studioImageDisplay 元信息', () => {
  it('尺寸与字节展示，来源缺失回落 -', () => {
    expect(formatImageDimensions(imageAsset())).toBe('1024×768')
    expect(formatImageDimensions(imageAsset({ width: null, height: null }))).toBe('-')
    expect(formatImageBytes(imageAsset())).toBe('2.0 KB')
    expect(formatImageBytes(imageAsset({ bytes: -1 }))).toBe('-')
  })
})

describe('studioImageDisplay 成本预估', () => {
  const model = (capabilities: Record<string, unknown>): StudioModelOption => ({
    model: 'gpt-image-1',
    providerCode: 'openai',
    domain: 'image',
    endpointModes: ['images_json'],
    capabilities
  })

  it('目录未声明单价时显式"无法估算"', () => {
    expect(studioImageCostEstimate(undefined)).toBe('无法估算')
    expect(studioImageCostEstimate(model({}))).toBe('无法估算')
    expect(studioImageCostEstimate(model({ unitPrice: '0.04' }))).toBe('无法估算')
  })

  it('声明单价时按单张计算', () => {
    expect(studioImageCostEstimate(model({ unitPrice: 0.04 }))).toBe('$0.040000')
  })
})

describe('studioImageDisplay 参数快照回填', () => {
  it('提取字符串参数并忽略非字符串与空值', () => {
    expect(pickStudioImageParams({
      accountId: 'acc-1',
      model: 'gpt-image-1',
      prompt: '一只猫',
      negativePrompt: '',
      size: 1024,
      referenceAssetId: 'stasset_9',
      extra: true
    })).toEqual({
      accountId: 'acc-1',
      model: 'gpt-image-1',
      prompt: '一只猫',
      negativePrompt: undefined,
      size: undefined,
      referenceAssetId: 'stasset_9'
    })
  })

  it('缺省 params 回落全空快照', () => {
    expect(pickStudioImageParams(undefined)).toEqual({})
    expect(pickStudioImageParams(null)).toEqual({})
  })
})
