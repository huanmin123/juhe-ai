import { describe, expect, it } from 'vitest'

import {
  accountTableScrollX,
  buildAccountTableColumns,
  tableColumnKey
} from './accountTableColumns'

// 域外遗留能力机制已废除（账户声明集必须 ⊆ 所在域允许集），「域外能力」
// 提示列不再存在；横向滚动宽度只随基础列变化。

function columnKeys(isManagementView: boolean): string[] {
  return buildAccountTableColumns(isManagementView, () => null).map((column) => tableColumnKey(column))
}

describe('buildAccountTableColumns 基础列', () => {
  it('管理面与用户面都不渲染 capabilityLegacy 列', () => {
    expect(columnKeys(true)).not.toContain('capabilityLegacy')
    expect(columnKeys(false)).not.toContain('capabilityLegacy')
  })

  it('横向滚动宽度按无提示列的基础列基数', () => {
    // 「限频」列宽 100→150（RPM 设计 §6.2 单行双数对 + 一体化双输入），总宽同步 +50。
    // 单菜单新增「场景」列 +100。
    expect(accountTableScrollX(true)).toBe(2800 + 32)
    expect(accountTableScrollX(false)).toBe(2620 + 32)
  })

  it('单菜单渲染场景列', () => {
    expect(columnKeys(true)).toContain('scenario')
    expect(columnKeys(false)).toContain('scenario')
  })

  it('限频列放宽到 150（RPM 单行双数对与一体化双输入）', () => {
    const column = buildAccountTableColumns(false, () => null).find((item) => tableColumnKey(item) === 'concurrency')
    expect(column).toBeDefined()
    expect(column?.title).toBe('限频')
    expect(column?.width).toBe(150)
    expect(Object.hasOwn(column ?? {}, 'sorter')).toBe(false)
  })
})
