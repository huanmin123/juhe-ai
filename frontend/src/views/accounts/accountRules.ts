import type { AccountSummary, ResourcePermissions } from '@/types/domain'
import { quotaLimitSummaryText } from '../shared/requestQuotaFormatters'
import { hasQuotaLimits } from '../shared/requestQuotaForm'
import type { AccountMenuItem } from './accountActionTypes'
import { formatDateTime, isAccountPackageExpired, isAuthorizedAccount, isTemporaryAccountStatus } from './accountFormatters'

export type AccountGroupIdResolver = (accountId: string) => string | undefined
export type AuthorizedAccountSourceTone = 'normal' | 'warning' | 'danger'

export function authorizedAccountTooltip(account: AccountSummary): string {
  const ownerName = account.ownerSystemAccountName || '其他用户'
  const expiresText = account.authorizationExpiresAt ? formatDateTime(account.authorizationExpiresAt) : '长期有效'
  const limitsText = quotaLimitSummaryText(account.authorizationLimits)
  const hasBlocker = hasAuthorizedAccountSourceBlocker(account)
  const lines = [
    hasBlocker ? `授权自 ${ownerName}。` : `授权自 ${ownerName}，仅可使用。`,
    `授权来源：${authorizedAccountSourceText(account)}`,
    `授权到期：${expiresText}`,
    `授权限额：${limitsText}`
  ]
  if (account.authorizationQuotaExceeded) {
    lines.push('授权额度已用完，当前调用会被拦截。')
  }
  if (isAccountPackageExpired(account) || account.lastErrorCode === 'account_expired' || account.lastErrorMessage?.includes('账户套餐已过期')) {
    lines.push('账户已到期，当前不可用。')
  } else if (isAuthorizedAccount(account) && account.status === 'disabled') {
    lines.push(account.localStatus === 'disabled' ? '当前分组已停用该账户，当前不可用。' : '账户所有者已停用该账户，当前不可用。')
  } else if (isAuthorizedAccount(account) && account.status === 'error') {
    lines.push('账户处于异常状态，当前不可用。')
  } else if (isTemporaryAccountStatus(account)) {
    lines.push('账户暂时不可调用，恢复前不会参与调度。')
  }
  if (account.groupBindStatus === 'authorization_unavailable') {
    lines.push('当前分组绑定的授权已失效，请重新绑定分组或联系授权人。')
  }
  if (authorizedAccountSourceTone(account) === 'warning' && !hasBlocker) {
    if (isAuthorizationExpiringSoon(account)) {
      lines.push('授权即将到期，请提前续期。')
    } else {
      lines.push('该授权配置了额度限制，达到限额后会停止调度。')
    }
  }
  return lines.join('\n')
}

export function authorizedAccountSourceTone(account: AccountSummary): AuthorizedAccountSourceTone {
  if (hasAuthorizedAccountSourceBlocker(account)) return 'danger'
  if (isAuthorizationExpiringSoon(account) || hasQuotaLimits(account.authorizationLimits)) return 'warning'
  return 'normal'
}

export function authorizedAccountSourceToneClass(account: AccountSummary): string {
  return `source-${authorizedAccountSourceTone(account)}`
}

function authorizedAccountSourceText(account: AccountSummary): string {
  const activeSources = account.authorizationSources?.filter((source) => source.status === 'active') ?? []
  if (!activeSources.length && account.authorizationSources?.some((source) => source.sourceType === 'team')) {
    return '团队授权'
  }
  const manual = activeSources.some((source) => source.sourceType === 'manual')
  const teamSources = activeSources.filter((source) => source.sourceType === 'team')
  const teamNames = teamSources.map((source) => source.sourceTeamName).filter((name): name is string => Boolean(name))
  if (manual && teamSources.length) {
    return teamNames.length ? `个人授权 + 团队授权（${teamNames.join('、')}）` : '个人授权 + 团队授权'
  }
  if (teamSources.length) {
    return teamNames.length ? `团队授权（${teamNames.join('、')}）` : '团队授权'
  }
  return '个人授权'
}

function hasAuthorizedAccountSourceBlocker(account: AccountSummary): boolean {
  return Boolean(
    account.authorizationQuotaExceeded
    || account.groupBindStatus === 'authorization_unavailable'
    || isAccountPackageExpired(account)
    || account.lastErrorCode === 'account_expired'
    || account.status === 'disabled'
    || account.status === 'error'
  )
}

function isAuthorizationExpiringSoon(account: AccountSummary): boolean {
  if (!account.authorizationExpiresAt) return false
  const timestamp = Date.parse(account.authorizationExpiresAt)
  if (!Number.isFinite(timestamp)) return false
  const remainingMs = timestamp - Date.now()
  return remainingMs > 0 && remainingMs <= 3 * 24 * 60 * 60 * 1000
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

export function canManageGroupAccounts(group: { accessType?: string; permissions?: Pick<ResourcePermissions, 'canManageAccounts'> }): boolean {
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
      pushDispatchFlagItems(items, account)
      return items.map(normalizeAccountMenuItem)
    }
    if (account.boundGroupId && account.localStatus && account.localStatus !== 'active') {
      items.push({ key: 'restore-normal', label: '恢复正常' })
    }
    pushDispatchFlagItems(items, account)
    if (account.status === 'active') {
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
    pushDispatchFlagItems(items, account)
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
    pushDispatchFlagItems(items, account)
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

function pushDispatchFlagItems(items: AccountMenuItem[], account: AccountSummary): void {
  if (account.status === 'active' || account.superPriorityEnabled) {
    items.push({
      key: account.superPriorityEnabled ? 'super-priority-off' : 'super-priority-on',
      label: account.superPriorityEnabled ? '取消超级优先' : '超级优先'
    })
  }
  if (account.status === 'active' || account.fallbackEnabled) {
    items.push({
      key: account.fallbackEnabled ? 'fallback-off' : 'fallback-on',
      label: account.fallbackEnabled ? '取消降级备用' : '降级备用'
    })
  }
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
