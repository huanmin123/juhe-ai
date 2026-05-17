import type { AccountSummary, GroupSummary } from '@/types/domain'
import type { AccountMenuItem } from './accountActionTypes'
import { isAuthorizedAccount, isOwnerDisabledAuthorizedAccount, isTemporaryAccountStatus } from './accountFormatters'

export type AccountGroupIdResolver = (accountId: string) => string | undefined

export function authorizedAccountTooltip(account: AccountSummary): string {
  const ownerName = account.ownerSystemAccountName || '其他用户'
  if (isOwnerDisabledAuthorizedAccount(account)) {
    return `授权自 ${ownerName}。账户所有者已停用该账户，你暂时无法启用或调用；请联系对方启用后再使用。`
  }
  return `授权自 ${ownerName}，仅可使用`
}

export function hasAccountEditPermission(account: AccountSummary): boolean {
  return account.permissions?.canEdit !== false
}

export function canEditAccount(account: AccountSummary): boolean {
  return hasAccountEditPermission(account)
}

export function canDeleteAccount(account: AccountSummary): boolean {
  return account.permissions?.canDelete !== false
}

export function canUseAccountActions(account: AccountSummary): boolean {
  return account.status !== 'error' && canEditAccount(account) && account.permissions?.canViewCredentials !== false
}

export function canBatchManageAccount(account: AccountSummary): boolean {
  return canEditAccount(account) && account.status !== 'error'
}

export function canRestoreException(account: AccountSummary): boolean {
  return account.status === 'error' && hasAccountEditPermission(account)
}

export function canTestAccount(account: AccountSummary): boolean {
  return account.status !== 'disabled' && account.permissions?.canUse !== false
}

export function canManageGroupAccounts(group: GroupSummary): boolean {
  return group.permissions?.canManageAccounts !== false && group.accessType !== 'authorized'
}

export function canUseAsTrafficMigrationTarget(source: AccountSummary, target: AccountSummary, groupIdForAccount: AccountGroupIdResolver): boolean {
  if (target.id === source.id) return false
  if (target.providerCode !== source.providerCode) return false
  if (groupIdForAccount(target.id) !== groupIdForAccount(source.id)) return false
  if (isAuthorizedAccount(source)) {
    return target.permissions?.canUse !== false && target.status === 'active' && target.schedulable && !isTemporaryAccountStatus(target)
  }
  if (!canEditAccount(target)) return false
  if (target.ownerSystemAccountId !== source.ownerSystemAccountId) return false
  return target.status === 'active' && target.schedulable && !isTemporaryAccountStatus(target)
}

export function canManageOpenAIOAuth(account: AccountSummary): boolean {
  return canUseAccountActions(account) && account.providerCode === 'openai' && account.type === 'oauth'
}

export function accountMenuItems(account: AccountSummary): AccountMenuItem[] {
  const items: AccountMenuItem[] = []
  if (isAuthorizedAccount(account)) {
    if (account.status === 'error') {
      return items.map(normalizeAccountMenuItem)
    }
    if (account.boundGroupId && account.localStatus && account.localStatus !== 'active') {
      items.push({ key: 'restore-normal', label: '恢复正常' })
    }
    if (account.status === 'active') {
      items.push({
        key: account.superPriorityEnabled ? 'super-priority-off' : 'super-priority-on',
        label: account.superPriorityEnabled ? '取消超级优先' : '超级优先'
      })
      items.push({
        key: account.fallbackEnabled ? 'fallback-off' : 'fallback-on',
        label: account.fallbackEnabled ? '取消降级备用' : '降级备用'
      })
      items.push({ key: 'migrate-traffic', label: '迁移流量' })
    }
    return items.map(normalizeAccountMenuItem)
  }
  if (canTestAccount(account)) {
    items.push({ key: 'test', label: '测试' })
  }
  if (account.status === 'error') {
    if (canRestoreException(account)) {
      items.push({ key: 'restore-normal', label: '恢复异常' })
    }
    return items.map(normalizeAccountMenuItem)
  }
  if (canUseAccountActions(account)) {
    if (canManageOpenAIOAuth(account)) {
      items.push({ key: 'refresh-oauth-token', label: '刷新令牌' })
      items.push({ key: 'reauthorize-oauth', label: '重新授权' })
    }
    if (isTemporaryAccountStatus(account)) {
      items.push({ key: 'restore-normal', label: '恢复正常' })
    }
    if (account.status === 'active') {
      items.push({
        key: account.superPriorityEnabled ? 'super-priority-off' : 'super-priority-on',
        label: account.superPriorityEnabled ? '取消超级优先' : '超级优先'
      })
      items.push({
        key: account.fallbackEnabled ? 'fallback-off' : 'fallback-on',
        label: account.fallbackEnabled ? '取消降级备用' : '降级备用'
      })
    }
    items.push({ key: 'migrate-traffic', label: '迁移流量' })
    items.push({
      key: 'toggle-status',
      label: account.status === 'disabled' ? '启用账户' : '停用账户',
      danger: account.status !== 'disabled',
      icon: account.status === 'disabled' ? 'enable' : 'pause',
      tone: account.status === 'disabled' ? 'success' : 'warning'
    })
  }
  return items.map(normalizeAccountMenuItem)
}

function normalizeAccountMenuItem(item: AccountMenuItem): AccountMenuItem {
  if (item.icon || item.tone) return item
  if (item.key === 'test') return { ...item, icon: 'test', tone: 'info' }
  if (item.key === 'refresh-oauth-token') return { ...item, icon: 'refresh', tone: 'info' }
  if (item.key === 'reauthorize-oauth') return { ...item, icon: 'reset', tone: 'warning' }
  if (item.key === 'restore-normal') return { ...item, icon: 'restore', tone: 'success' }
  if (item.key === 'super-priority-on') return { ...item, icon: 'superPriority', tone: 'warning' }
  if (item.key === 'super-priority-off') return { ...item, icon: 'superPriority', tone: 'default' }
  if (item.key === 'fallback-on') return { ...item, icon: 'fallback', tone: 'purple' }
  if (item.key === 'fallback-off') return { ...item, icon: 'fallback', tone: 'default' }
  if (item.key === 'migrate-traffic') return { ...item, icon: 'migrate', tone: 'purple' }
  return item
}
