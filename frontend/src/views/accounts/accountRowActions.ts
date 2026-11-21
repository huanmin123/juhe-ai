import type { RowActionItem } from '@/components/rowActions'
import type { AccountListItem } from '@/types/domain'
import type { AccountMenuItem } from './accountActionTypes'
import { accountDisplayName } from './accountBasicFormatters'
import { isAuthorizedAccount } from './accountFormatters'
import { accountMenuItemsWithClone } from './accountRules'

export type AccountRowActionOptions = {
  account: AccountListItem
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
      confirmTitle: `确认删除账户 ${accountDisplayName(account)}？`,
      confirmOkText: '删除'
    })
  }
  return list
}

export function buildAccountMoreActions(options: AccountRowActionOptions): RowActionItem[] {
  return accountMenuItemsWithClone(options.menuItems, !isAuthorizedAccount(options.account) && options.canClone)
}
