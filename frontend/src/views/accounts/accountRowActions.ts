import type { RowActionItem } from '@/components/rowActions'
import type { AccountSummary } from '@/types/domain'
import type { AccountMenuItem } from './accountActionTypes'
import { accountDisplayName } from './accountBasicFormatters'
import { isAuthorizedAccount } from './accountFormatters'
import { accountMenuItemsWithClone, canReturnAuthorizedAccount } from './accountRules'

export type AccountRowActionOptions = {
  account: AccountSummary
  canClone: boolean
  canDelete: boolean
  canEdit: boolean
  menuItems: AccountMenuItem[]
}

export function buildAccountRowActions(options: AccountRowActionOptions): RowActionItem[] {
  const { account } = options
  if (isAuthorizedAccount(account)) {
    const authorizedList: RowActionItem[] = []
    if (options.canEdit) {
      authorizedList.push({ key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' })
    }
    if (canReturnAuthorizedAccount(account)) {
      authorizedList.push({
        key: 'return-authorization',
        label: '归还',
        icon: 'revoke',
        tone: 'danger',
        confirmTitle: '确认归还这个授权账户？归还后你将不再看到或使用它，不影响授权方原账户。',
        confirmOkText: '归还'
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
      confirmTitle: `确认删除账户 ${accountDisplayName(account)}？`,
      confirmOkText: '删除'
    })
  }
  return list
}

export function buildAccountMoreActions(options: AccountRowActionOptions): RowActionItem[] {
  return accountMenuItemsWithClone(options.menuItems, !isAuthorizedAccount(options.account) && options.canClone)
}
