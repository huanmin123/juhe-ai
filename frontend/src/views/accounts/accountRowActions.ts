import type { RowActionItem } from '@/components/rowActions'
import type { AccountSummary } from '@/types/domain'
import type { AccountMenuItem } from './accountActionTypes'
import { isAuthorizedAccount } from './accountFormatters'
import { accountMenuItemsWithClone } from './accountRules'

export type AccountRowActionOptions = {
  account: AccountSummary
  canClone: boolean
  canDelete: boolean
  canEdit: boolean
  groupName?: string
  menuItems: AccountMenuItem[]
}

export function buildAccountRowActions(options: AccountRowActionOptions): RowActionItem[] {
  const { account } = options
  if (isAuthorizedAccount(account)) {
    const authorizedList: RowActionItem[] = []
    if (options.canEdit) {
      authorizedList.push({ key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' })
    }
    if (options.canDelete) {
      authorizedList.push({
        key: 'delete',
        label: '删除',
        icon: 'delete',
        tone: 'danger',
        confirmTitle: '确认删除这个授权账户？',
        confirmOkText: '删除'
      })
    }
    return authorizedList
  }

  const list: RowActionItem[] = []
  if (options.canEdit) {
    list.push({ key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' })
  }
  if (options.canDelete) {
    list.push({
      key: 'delete',
      label: '删除',
      icon: 'delete',
      tone: 'danger',
      confirmTitle: '确认删除这个账户？',
      confirmOkText: '删除'
    })
  }
  return list
}

export function buildAccountMoreActions(options: AccountRowActionOptions): RowActionItem[] {
  return accountMenuItemsWithClone(options.menuItems, !isAuthorizedAccount(options.account) && options.canClone)
}
