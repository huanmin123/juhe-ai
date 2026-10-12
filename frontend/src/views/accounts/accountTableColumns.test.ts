import { describe, expect, it } from 'vitest'

import {
  accountTableScrollX,
  buildAccountTableColumns,
  tableColumnKey
} from './accountTableColumns'

// M3「域外能力」提示列（计划 §M3 配套适配）：管理面与用户面均渲染该列，
// 仅存在域外遗留声明的行显示徽标；横向滚动宽度随列同步扩展。

function columnKeys(isManagementView: boolean): string[] {
  return buildAccountTableColumns(isManagementView, () => null).map((column) => tableColumnKey(column))
}

describe('buildAccountTableColumns 域外能力提示列', () => {
  it('管理面在供应商列之后渲染 capabilityLegacy 列', () => {
    const keys = columnKeys(true)
    expect(keys).toContain('capabilityLegacy')
    expect(keys.indexOf('capabilityLegacy')).toBeGreaterThan(keys.indexOf('type'))
    expect(keys.indexOf('capabilityLegacy')).toBeLessThan(keys.indexOf('concurrency'))
  })

  it('用户面同样渲染 capabilityLegacy 列', () => {
    expect(columnKeys(false)).toContain('capabilityLegacy')
  })

  it('横向滚动宽度包含提示列（+110）', () => {
    expect(accountTableScrollX(true)).toBe(2760 + 32)
    expect(accountTableScrollX(false)).toBe(2580 + 32)
  })
})
