import { describe, expect, it } from 'vitest'

import {
  mergeSelectedProxyOptions,
  proxyLabelForId,
  proxySelectOptionLabel,
  proxySelectionForId,
  rememberProxyLabel,
  rememberProxyLabels,
  rememberProxySelection,
  rememberProxySelections,
  type ProxyOptionLike
} from './proxyLabelCache'
import type { ProxyProfileOptionSummary } from '@/types/domain'
import type { SelectOption } from './selectLabelCache'

function proxy(partial: Partial<ProxyOptionLike> & { id: string; name: string }): ProxyOptionLike {
  return { ...partial }
}

describe('proxySelectOptionLabel', () => {
  it('无类型时仅显示名称', () => {
    expect(proxySelectOptionLabel(proxy({ id: 'p1', name: '代理一' }))).toBe('代理一')
    expect(proxySelectOptionLabel(proxy({ id: 'p2', name: '代理二', type: '  ' }))).toBe('代理二')
  })

  it('有类型时以全角括号附加，停用时追加文案', () => {
    expect(proxySelectOptionLabel(proxy({ id: 'p3', name: '代理三', type: 'http' }))).toBe('代理三（http）')
    expect(proxySelectOptionLabel(proxy({ id: 'p4', name: '代理四', type: 'socks5', enabled: false }))).toBe('代理四（socks5，已停用）')
    expect(proxySelectOptionLabel(proxy({ id: 'p5', name: '代理五', type: 'http', enabled: true }))).toBe('代理五（http）')
  })

  it('兼容 ProxyProfileOptionSummary 形状', () => {
    const summary = { id: 'p6', name: '代理六', type: 'http', enabled: false } as ProxyProfileOptionSummary
    expect(proxySelectOptionLabel(summary)).toBe('代理六（http，已停用）')
  })
})

describe('rememberProxyLabels / proxyLabelForId', () => {
  it('批量记录代理标签（含类型后缀）', () => {
    rememberProxyLabels([
      proxy({ id: 'px-1', name: '代理一' }),
      proxy({ id: 'px-2', name: '代理二', type: 'http' })
    ])
    expect(proxyLabelForId('px-1')).toBe('代理一')
    expect(proxyLabelForId('px-2')).toBe('代理二（http）')
    expect(proxyLabelForId('missing')).toBeUndefined()
  })

  it('rememberProxySelection / rememberProxySelections 记录选中项', () => {
    rememberProxySelection({ id: 'px-3', name: '选中代理' })
    rememberProxySelections([undefined, { id: 'px-4', name: '选中代理二' }])
    expect(proxyLabelForId('px-3')).toBe('选中代理')
    expect(proxyLabelForId('px-4')).toBe('选中代理二')
  })
})

describe('proxySelectionForId', () => {
  it('空白 id 返回 undefined', () => {
    expect(proxySelectionForId(undefined)).toBeUndefined()
    expect(proxySelectionForId('   ')).toBeUndefined()
  })

  it('优先从代理列表解析（带类型后缀）', () => {
    expect(proxySelectionForId('px-10', [proxy({ id: 'px-10', name: '列表代理', type: 'http' })]))
      .toEqual({ id: 'px-10', name: '列表代理（http）' })
  })

  it('代理列表未命中时回退选项列表', () => {
    const options: SelectOption[] = [{ label: '选项代理', value: 'px-11' }]
    expect(proxySelectionForId('px-11', [], options)).toEqual({ id: 'px-11', name: '选项代理' })
  })

  it('再未命中时回退缓存，最终未命中返回 undefined', () => {
    rememberProxyLabel('px-12', '缓存代理')
    expect(proxySelectionForId('px-12')).toEqual({ id: 'px-12', name: '缓存代理' })
    expect(proxySelectionForId('px-ghost')).toBeUndefined()
  })
})

describe('mergeSelectedProxyOptions', () => {
  it('合并选项列表与选中代理，未知项被忽略', () => {
    rememberProxyLabel('px-m-cached', '缓存代理')
    const options: SelectOption[] = [{ label: '窗口代理', value: 'px-m-win' }]
    const merged = mergeSelectedProxyOptions(
      options,
      ['px-m-win', 'px-m-cached', 'ghost', undefined],
      [undefined, { id: 'px-m-sel', name: '选中代理' }]
    )
    expect(merged).toEqual([
      { label: '窗口代理', value: 'px-m-win' },
      { label: '选中代理', value: 'px-m-sel' },
      { label: '缓存代理', value: 'px-m-cached' }
    ])
  })
})
