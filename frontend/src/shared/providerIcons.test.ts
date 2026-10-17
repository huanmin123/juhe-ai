import { describe, expect, it } from 'vitest'

import {
  GLM_PROVIDER_CODE,
  GPT_VENDOR_CODE,
  ANTHROPIC_PROVIDER_CODE,
  DEEPSEEK_PROVIDER_CODE,
  GEMINI_PROVIDER_CODE,
  HYBRID_PROVIDER_CODE,
  MINIMAX_PROVIDER_CODE,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  QWEN_PROVIDER_CODE,
  VOLCENGINE_PROVIDER_CODE,
  XAI_PROVIDER_CODE
} from '@/shared/providerProtocol'
import { providerIconFallbackChar, providerIconPaths } from '@/shared/providerIcons'

// 设计契约（供应商卡片选择与图标设计 §2.2）的 12 个 providerCode。
// moonshot 为预留 code，providerProtocol.ts 尚无对应常量，此处按设计契约字面量列出。
const DESIGN_PROVIDER_ICON_CODES = [
  GPT_VENDOR_CODE,
  ANTHROPIC_PROVIDER_CODE,
  GEMINI_PROVIDER_CODE,
  DEEPSEEK_PROVIDER_CODE,
  GLM_PROVIDER_CODE,
  QWEN_PROVIDER_CODE,
  VOLCENGINE_PROVIDER_CODE,
  'moonshot',
  MINIMAX_PROVIDER_CODE,
  XAI_PROVIDER_CODE,
  HYBRID_PROVIDER_CODE,
  OPENAI_COMPATIBLE_PROVIDER_CODE
]

describe('providerIconPaths', () => {
  it('映射 key 集合与设计契约的 12 个 providerCode 完全一致', () => {
    expect(Object.keys(providerIconPaths).sort()).toEqual([...DESIGN_PROVIDER_ICON_CODES].sort())
  })

  it('每个条目 viewBox 为 4 个合法数字且 paths 非空、d 非空', () => {
    for (const [code, item] of Object.entries(providerIconPaths)) {
      const parts = item.viewBox.split(/\s+/).map(Number)
      expect(parts, `${code} viewBox 应为 4 个数字`).toHaveLength(4)
      for (const value of parts) {
        expect(Number.isFinite(value), `${code} viewBox 应为合法数字`).toBe(true)
      }
      expect(item.paths.length, `${code} paths 应非空`).toBeGreaterThan(0)
      for (const path of item.paths) {
        expect(path.d.trim().length, `${code} path d 应非空`).toBeGreaterThan(0)
      }
    }
  })

  it('关键条目保持来源特征（Qwen evenodd、OpenAI 品牌黑、豆包 4 path、xAI 方形标）', () => {
    expect(providerIconPaths[QWEN_PROVIDER_CODE].paths[0].fillRule).toBe('evenodd')
    expect(providerIconPaths[GPT_VENDOR_CODE].paths[0].fill).toBe('#000000')
    expect(providerIconPaths[VOLCENGINE_PROVIDER_CODE].paths).toHaveLength(4)
    expect(providerIconPaths[XAI_PROVIDER_CODE].viewBox).toBe('0 0 24 24')
    expect(providerIconPaths[GLM_PROVIDER_CODE].paths[0].fill).toBe('#3859FF')
    expect(providerIconPaths[GLM_PROVIDER_CODE].paths[0].fillRule).toBeUndefined()
  })
})

describe('providerIconFallbackChar', () => {
  it('name 优先', () => {
    expect(providerIconFallbackChar('自定义供应商', 'gpt')).toBe('自')
  })

  it('name 缺省或空白时回退 providerCode', () => {
    expect(providerIconFallbackChar(undefined, 'glm')).toBe('G')
    expect(providerIconFallbackChar('', 'glm')).toBe('G')
    expect(providerIconFallbackChar('   ', 'glm')).toBe('G')
  })

  it('首字符 trim 后大写显示', () => {
    expect(providerIconFallbackChar('  xai  ', undefined)).toBe('X')
    expect(providerIconFallbackChar(undefined, ' deepseek ')).toBe('D')
  })

  it('两者皆空回「?」', () => {
    expect(providerIconFallbackChar(undefined, undefined)).toBe('?')
    expect(providerIconFallbackChar('', '')).toBe('?')
    expect(providerIconFallbackChar('   ', '   ')).toBe('?')
  })
})
