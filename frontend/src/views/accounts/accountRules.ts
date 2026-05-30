import type { AccountSummary, ResourcePermissions } from '@/types/domain'
import { quotaLimitSummaryText } from '../shared/requestQuotaFormatters'
import { hasQuotaLimits } from '../shared/requestQuotaForm'
import type { AccountMenuItem } from './accountActionTypes'
import {
  formatDateTime,
  hasAccountRuntimeRecoveryState,
  isAccountPackageExpiredStatus,
  isAuthorizationBindingUnavailable,
  isAuthorizationExpired,
  isAuthorizationPaused,
  isAuthorizedAccount,
  isTemporaryAccountStatus
} from './accountFormatters'

export type AccountGroupIdResolver = (accountId: string) => string | undefined
export type AuthorizedAccountSourceTone = 'normal' | 'warning' | 'danger'

export function authorizedAccountTooltip(account: AccountSummary): string {
  const ownerName = account.ownerSystemAccountName || '其他用户'
  const expiresText = account.authorizationExpiresAt ? formatDateTime(account.authorizationExpiresAt) : '长期有效'
  const limitsText = quotaLimitSummaryText(account.authorizationLimits)
  const hasBlocker = hasAuthorizedAccountSourceBlocker(account)
  const lines = [
    `授权自 ${ownerName}。`,
    `授权来源：${authorizedAccountSourceText(account)}`,
    `授权到期：${expiresText}`,
    `授权限额：${limitsText}`
  ]
  if (isAuthorizationExpired(account)) {
    lines.push('授权已到期，当前不可用。')
  }
  if (isAuthorizationPaused(account)) {
    lines.push('授权已暂停，当前不可用。')
  }
  if (account.authorizationQuotaExceeded && !isAuthorizationExpired(account)) {
    lines.push('授权额度已用完，当前调用会被拦截。')
  }
  if (isAccountPackageExpiredStatus(account)) {
    lines.push('账户已到期，当前不可用。')
  } else if (isAuthorizedAccount(account) && account.status === 'disabled') {
    lines.push('账户已停用，当前不可用。')
  } else if (isAuthorizedAccount(account) && account.status === 'error') {
    lines.push('授权账户状态异常，当前不可用。')
  } else if (isTemporaryAccountStatus(account) || (isAuthorizedAccount(account) && !account.schedulable)) {
    lines.push(isAuthorizedAccount(account) ? '授权账户实例暂时不可调用，恢复前不会参与调度。' : '账户暂时不可调用，恢复前不会参与调度。')
  }
  if (isAuthorizationBindingUnavailable(account)) {
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
    || isAuthorizationExpired(account)
    || isAuthorizationPaused(account)
    || isAuthorizationBindingUnavailable(account)
    || account.status === 'disabled'
    || account.status === 'error'
    || isTemporaryAccountStatus(account)
    || !account.schedulable
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
  if (isAuthorizedAccount(account)) return account.permissions?.canUse !== false
  return hasAccountEditPermission(account)
}

export function canDeleteAccount(account: AccountSummary): boolean {
  return account.permissions?.canDelete !== false
}

export function canCloneAccount(account: AccountSummary): boolean {
  return !isAuthorizedAccount(account)
    && canEditAccount(account)
    && account.permissions?.canViewCredentials !== false
}

export function canUseAccountActions(account: AccountSummary): boolean {
  return account.status !== 'error' && canEditAccount(account) && account.permissions?.canViewCredentials !== false
}

export function canBatchManageAccount(account: AccountSummary): boolean {
  if (isAuthorizedAccount(account)) {
    return Boolean(account.boundGroupId)
      && account.permissions?.canUse !== false
      && !isAuthorizationExpired(account)
      && !isAuthorizationBindingUnavailable(account)
      && !isAccountPackageExpiredStatus(account)
      && account.status !== 'error'
  }
  return canEditAccount(account) && account.status !== 'error'
}

export function canRestoreException(account: AccountSummary): boolean {
  return account.status === 'error' && hasAccountEditPermission(account)
}

export function authorizedAccountUnavailableText(account: AccountSummary): string | undefined {
  if (!isAuthorizedAccount(account)) return undefined
  if (account.permissions?.canUse === false) return '当前授权账户无可用权限'
  if (!account.boundGroupId) return '授权账户需要先绑定到你的分组'
  if (isAuthorizationExpired(account)) return '授权已到期，当前账户不能调用'
  if (isAuthorizationPaused(account)) return '授权已暂停，当前账户不能调用'
  if (isAuthorizationBindingUnavailable(account)) return '当前分组绑定的授权已失效，请重新绑定分组或联系授权人'
  if (account.authorizationQuotaExceeded) return '授权额度已用完，当前账户不能调用'
  if (account.status === 'disabled') return '账户已停用，当前不可用'
  if (account.status === 'error') return '授权账户状态异常，当前不可用'
  if (isTemporaryAccountStatus(account) || isFutureTime(account.cooldownUntil)) return '授权账户实例暂时不可调用，恢复前不会参与调度'
  if (!account.schedulable) return '授权账户实例暂时不可调用，恢复前不会参与调度'
  return undefined
}

function isFutureTime(value?: string): boolean {
  if (!value) return false
  const time = Date.parse(value)
  return Number.isFinite(time) && time > Date.now()
}

export function canUseAuthorizedAccount(account: AccountSummary): boolean {
  return isAuthorizedAccount(account) && !authorizedAccountUnavailableText(account)
}

export function canUseBoundAuthorizedAccount(account: AccountSummary): boolean {
  return canUseAuthorizedAccount(account) && Boolean(account.boundGroupId)
}

export function canTestAccount(account: AccountSummary): boolean {
  if (isAuthorizedAccount(account)) {
    if (!account.boundGroupId || account.permissions?.canUse === false) return false
    if (isAuthorizationExpired(account) || isAuthorizationPaused(account) || isAuthorizationBindingUnavailable(account) || isAccountPackageExpiredStatus(account)) return false
    if (account.authorizationQuotaExceeded || account.status === 'error') return false
    const instanceFailureState = hasAuthorizedInstanceFailureState(account)
    if (isTemporaryAccountStatus(account) && !instanceFailureState) return false
    return account.schedulable || account.status === 'disabled' || instanceFailureState
  }
  return account.permissions?.canUse !== false
}

export function hasAuthorizedInstanceFailureState(account: AccountSummary): boolean {
  return Boolean(
    (account.status !== 'active' && account.status !== 'disabled')
    || account.cooldownUntil
    || account.lastErrorMessage
  )
}

export function canManageGroupAccounts(group: { accessType?: string; permissions?: Pick<ResourcePermissions, 'canManageAccounts'> }): boolean {
  return group.permissions?.canManageAccounts !== false && group.accessType !== 'authorized'
}

export function canUseAsTrafficMigrationTarget(source: AccountSummary, target: AccountSummary, groupIdForAccount: AccountGroupIdResolver): boolean {
  if (target.id === source.id) return false
  if (target.providerCode !== source.providerCode) return false
  if (groupIdForAccount(target.id) !== groupIdForAccount(source.id)) return false
  if (isAuthorizedAccount(source)) {
    if (isAuthorizedAccount(target)) return canUseBoundAuthorizedAccount(target) && !hasAccountRuntimeRecoveryState(target)
    return target.permissions?.canUse !== false && target.status === 'active' && target.schedulable && !isTemporaryAccountStatus(target) && !hasAccountRuntimeRecoveryState(target)
  }
  if (isAuthorizedAccount(target)) return false
  if (!canEditAccount(target)) return false
  if (target.ownerSystemAccountId !== source.ownerSystemAccountId) return false
  return target.status === 'active' && target.schedulable && !isTemporaryAccountStatus(target) && !hasAccountRuntimeRecoveryState(target)
}

export function canManageOpenAIOAuth(account: AccountSummary): boolean {
  return canUseAccountActions(account) && account.providerCode === 'openai' && account.type === 'oauth'
}

export function accountMenuItems(account: AccountSummary): AccountMenuItem[] {
  const items: AccountMenuItem[] = []
  if (isAuthorizedAccount(account)) {
    if (canTestAccount(account)) {
      items.push({ key: 'test', label: '测试' })
    }
    if (account.status === 'error') {
      pushDispatchFlagItems(items, account)
      return items.map(normalizeAccountMenuItem)
    }
    if (hasAccountRuntimeRecoveryState(account) || (account.boundGroupId && hasAuthorizedInstanceFailureState(account))) {
      items.push({ key: 'restore-normal', label: '恢复正常' })
    }
    pushDispatchFlagItems(items, account)
    if (canUseBoundAuthorizedAccount(account)) {
      items.push({ key: 'migrate-traffic', label: '迁移流量' })
    }
    if (canBatchManageAccount(account)) {
      const instanceDisabled = account.status === 'disabled'
      items.push({
        key: 'toggle-status',
        label: instanceDisabled ? '启用账户' : '停用账户',
        danger: !instanceDisabled,
        icon: instanceDisabled ? 'enable' : 'pause',
        tone: instanceDisabled ? 'success' : 'warning',
        confirmTitle: instanceDisabled
          ? `确认启用账户「${account.name}」？启用后只恢复你这里的授权账户实例。`
          : `确认停用账户「${account.name}」？停用后只影响你这里的授权账户实例。`,
        confirmOkText: instanceDisabled ? '启用' : '停用'
      })
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
    if (hasAccountRuntimeRecoveryState(account) || isTemporaryAccountStatus(account)) {
      items.push({ key: 'restore-normal', label: '恢复正常' })
    }
    pushDispatchFlagItems(items, account)
    items.push({ key: 'migrate-traffic', label: '迁移流量' })
    items.push({
      key: 'toggle-status',
      label: account.status === 'disabled' ? '启用账户' : '停用账户',
      danger: account.status !== 'disabled',
      icon: account.status === 'disabled' ? 'enable' : 'pause',
      tone: account.status === 'disabled' ? 'success' : 'warning',
      confirmTitle: account.status === 'disabled'
        ? `确认启用账户「${account.name}」？`
        : `确认停用账户「${account.name}」？停用后该账户将不再参与调度。`,
      confirmOkText: account.status === 'disabled' ? '启用' : '停用'
    })
  }
  return items.map(normalizeAccountMenuItem)
}

export function accountMenuItemsWithClone(menuItems: AccountMenuItem[], canClone: boolean): AccountMenuItem[] {
  if (!canClone) return menuItems
  const cloneItem: AccountMenuItem = { key: 'clone', label: '克隆', icon: 'copy', tone: 'info' }
  const testIndex = menuItems.findIndex((item) => item.key === 'test')
  if (testIndex < 0) return [...menuItems, cloneItem]
  return [
    ...menuItems.slice(0, testIndex + 1),
    cloneItem,
    ...menuItems.slice(testIndex + 1)
  ]
}

function pushDispatchFlagItems(items: AccountMenuItem[], account: AccountSummary): void {
  const canEnableDispatchFlag = isAuthorizedAccount(account)
    ? canUseBoundAuthorizedAccount(account)
    : account.status === 'active'
  if (canEnableDispatchFlag || account.superPriorityEnabled) {
    items.push({
      key: account.superPriorityEnabled ? 'super-priority-off' : 'super-priority-on',
      label: account.superPriorityEnabled ? '取消超级优先' : '超级优先'
    })
  }
  if (canEnableDispatchFlag || account.fallbackEnabled) {
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
