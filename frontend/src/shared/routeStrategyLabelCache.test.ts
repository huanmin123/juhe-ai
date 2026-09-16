import { describe, expect, it } from 'vitest'

import {
  mergeSelectedRouteStrategyOptions,
  rememberRouteStrategyLabel,
  rememberRouteStrategyLabels,
  rememberRouteStrategySelection,
  rememberRouteStrategySelections,
  routeStrategyLabelForId,
  routeStrategyModeText,
  routeStrategySelectionForId,
  routeStrategySelectionFromOption,
  routeStrategySelectOptionLabel,
  type RouteStrategyOptionLike
} from './routeStrategyLabelCache'
import type { SelectOption } from './selectLabelCache'

function strategy(partial: Partial<RouteStrategyOptionLike> & { id: string; name: string }): RouteStrategyOptionLike {
  return {
    mode: 'normal',
    status: 'active',
    isDefault: false,
    ...partial
  }
}

describe('routeStrategyModeText', () => {
  it('映射路由模式中文文案', () => {
    expect(routeStrategyModeText('hybrid_smart')).toBe('混合智能路由')
    expect(routeStrategyModeText('weighted')).toBe('权重调度路由')
    expect(routeStrategyModeText('round_robin')).toBe('轮询路由')
    expect(routeStrategyModeText('failover')).toBe('故障回退路由')
    expect(routeStrategyModeText('normal')).toBe('普通路由')
    expect(routeStrategyModeText(undefined)).toBe('普通路由')
  })
})

describe('routeStrategySelectOptionLabel', () => {
  it('默认拼接模式后缀', () => {
    expect(routeStrategySelectOptionLabel(strategy({ id: 's1', name: '策略一', mode: 'weighted' }))).toBe('策略一（权重调度路由）')
  })

  it('isDefault 时后缀固定为默认', () => {
    expect(routeStrategySelectOptionLabel(strategy({ id: 's2', name: '策略二', isDefault: true }))).toBe('策略二（默认）')
  })

  it('showSystemAccountLabel 时追加属主前缀', () => {
    expect(routeStrategySelectOptionLabel(
      strategy({ id: 's3', name: '策略三', systemAccountName: '张三' }),
      { showSystemAccountLabel: true }
    )).toBe('张三 / 策略三（普通路由）')
  })
})

describe('rememberRouteStrategyLabels / routeStrategyLabelForId', () => {
  it('批量记录策略标签（含后缀）并支持自定义缓存键', () => {
    rememberRouteStrategyLabels(
      [strategy({ id: 'rs-1', name: '策略一', mode: 'round_robin' })],
      'rs-custom-key'
    )
    expect(routeStrategyLabelForId('rs-1', 'rs-custom-key')).toBe('策略一（轮询路由）')
    expect(routeStrategyLabelForId('rs-1')).toBeUndefined()
  })

  it('rememberRouteStrategySelection / rememberRouteStrategySelections 记录选中项', () => {
    rememberRouteStrategySelection({ id: 'rs-2', name: '选中策略', mode: 'normal', status: 'active', isDefault: false })
    rememberRouteStrategySelections([undefined, { id: 'rs-3', name: '选中策略二', mode: 'normal', status: 'active', isDefault: false }])
    expect(routeStrategyLabelForId('rs-2')).toBe('选中策略')
    expect(routeStrategyLabelForId('rs-3')).toBe('选中策略二')
  })
})

describe('routeStrategySelectionFromOption', () => {
  it('提取选择所需的字段', () => {
    const source = strategy({ id: 'rs-4', name: '策略四', mode: 'failover', status: 'disabled', isDefault: true, systemAccountName: '李四' })
    expect(routeStrategySelectionFromOption(source)).toEqual({
      id: 'rs-4',
      name: '策略四',
      mode: 'failover',
      status: 'disabled',
      isDefault: true,
      systemAccountName: '李四'
    })
  })
})

describe('routeStrategySelectionForId', () => {
  it('空白 id 返回 undefined', () => {
    expect(routeStrategySelectionForId(undefined)).toBeUndefined()
    expect(routeStrategySelectionForId('  ')).toBeUndefined()
  })

  it('优先从策略列表解析完整字段', () => {
    const source = strategy({ id: 'rs-10', name: '列表策略', mode: 'weighted' })
    expect(routeStrategySelectionForId('rs-10', [source])).toEqual({
      id: 'rs-10', name: '列表策略', mode: 'weighted', status: 'active', isDefault: false, systemAccountName: undefined
    })
  })

  it('策略列表未命中时回退选项列表', () => {
    const options: SelectOption[] = [{ label: '选项策略', value: 'rs-11' }]
    expect(routeStrategySelectionForId('rs-11', [], options)).toEqual({ id: 'rs-11', name: '选项策略' })
  })

  it('再未命中时回退缓存，最终未命中返回 undefined', () => {
    rememberRouteStrategyLabel('rs-12', '缓存策略')
    expect(routeStrategySelectionForId('rs-12')).toEqual({ id: 'rs-12', name: '缓存策略' })
    expect(routeStrategySelectionForId('rs-ghost')).toBeUndefined()
  })
})

describe('mergeSelectedRouteStrategyOptions', () => {
  it('合并选项列表与选中策略（标签按选项规则生成）', () => {
    const options: SelectOption[] = [{ label: '窗口策略（普通路由）', value: 'rs-m-win' }]
    const merged = mergeSelectedRouteStrategyOptions(
      'rs-merge-key',
      options,
      ['rs-m-win', 'ghost'],
      [undefined, strategy({ id: 'rs-m-sel', name: '选中策略', mode: 'failover' })]
    )
    expect(merged).toEqual([
      { label: '窗口策略（普通路由）', value: 'rs-m-win' },
      { label: '选中策略（故障回退路由）', value: 'rs-m-sel' }
    ])
  })

  it('labelOptions 透传给选中策略标签生成', () => {
    const merged = mergeSelectedRouteStrategyOptions(
      'rs-merge-key-2',
      [],
      ['rs-m-2'],
      [strategy({ id: 'rs-m-2', name: '策略', systemAccountName: '王五' })],
      { showSystemAccountLabel: true }
    )
    expect(merged).toEqual([{ label: '王五 / 策略（普通路由）', value: 'rs-m-2' }])
  })
})
