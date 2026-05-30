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
    if (account.status !== 'error') {
      authorizedList.push({ key: 'bind-group', label: options.groupName ? '调整分组' : '绑定分组', icon: 'bind', tone: 'purple' })
    }
    if (account.accountAuthorizationId) {
      authorizedList.push({
        key: 'return',
        label: '归还',
        icon: 'revoke',
        tone: 'danger',
        confirmTitle: `确认归还授权账户「${account.name}」？归还后你将不再看到或使用它，不影响授权方原账户。`,
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
      confirmTitle: '确认删除这个账户？',
      confirmOkText: '删除'
    })
  }
  return list
}

export function buildAccountMoreActions(options: AccountRowActionOptions): RowActionItem[] {
  return accountMenuItemsWithClone(options.menuItems, !isAuthorizedAccount(options.account) && options.canClone)
}
