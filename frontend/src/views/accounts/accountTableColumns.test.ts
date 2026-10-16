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
    expect(accountTableScrollX(true)).toBe(2650 + 32)
    expect(accountTableScrollX(false)).toBe(2470 + 32)
  })
})
