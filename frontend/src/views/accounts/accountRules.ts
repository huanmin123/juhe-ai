import type { AccountSummary, ResourcePermissions } from '@/types/domain'
import { serverDateTimeTimestamp } from '@/shared/formatters'
import { quotaLimitSummaryText } from '../shared/requestQuotaFormatters'
import { hasQuotaLimits } from '../shared/requestQuotaForm'
import type { AccountMenuItem } from './accountActionTypes'
import {
  canCreateOAuthAccount,
  isGatewayTestableAccountProfile
} from './accountProviderCapabilities'
import {
  formatDateTime,
  hasAccountRuntimeRecoveryState,
  isAccountPackageExpiredStatus,
  isAuthorizationBindingUnavailable,
  isAuthorizationExpired,
  isAuthorizationPaused,
  isAuthorizedAccount,
  isPendingHealthCheckFailed,
  isTemporaryAccountStatus
} from './accountFormatters'

export type AccountGroupIdResolver = (accountId: string) => string | undefined
export type AuthorizedAccountSourceTone = 'normal' | 'warning' | 'danger'

export function authorizedAccountOwnerBadgeText(account: AccountSummary): string {
  return `${account.ownerSystemAccountName || '其他用户'}授权`
}

export function authorizedAccountTooltip(account: AccountSummary): string {
  const ownerName = account.ownerSystemAccountName || '其他用户'
  const expiresText = account.authorizationExpiresAt ? formatDateTime(account.authorizationExpiresAt) : '长期有效'
  const limitsText = quotaLimitSummaryText(account.authorizationLimits)
  const lines = [
    `授权自 ${ownerName}。`,
    `授权来源：${authorizedAccountSourceText(account)}`,
    `授权到期：${expiresText}`,
    `授权限额：${limitsText}`
  ]
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
  if (account.effectiveAvailability?.available === false) return true
  return Boolean(
    account.permissions?.canUse === false
    || !account.boundGroupId
    || account.authorizationQuotaExceeded
    || isAuthorizationExpired(account)
    || isAuthorizationPaused(account)
    || isAuthorizationBindingUnavailable(account)
    || account.status === 'pending_test'
    || account.status === 'disabled'
    || account.status === 'error'
    || isTemporaryAccountStatus(account)
    || !account.schedulable
  )
}

function isAuthorizationExpiringSoon(account: AccountSummary): boolean {
  if (!account.authorizationExpiresAt) return false
  const timestamp = serverDateTimeTimestamp(account.authorizationExpiresAt)
  if (timestamp === undefined) return false
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
  if (isAuthorizedAccount(account)) return false
  return account.permissions?.canDelete !== false
}

export function canBatchDeleteAccount(account: AccountSummary): boolean {
  return canDeleteAccount(account)
}

export function canReturnAuthorizedAccount(account: AccountSummary): boolean {
  if (!isAuthorizedAccount(account)) return false
  if (!account.accountAuthorizationId) return false
  return account.permissions?.canReturnAuthorization === true
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
  if (account.status === 'pending_test') return false
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

export function canToggleAccountStatus(account: AccountSummary): boolean {
  if (isAuthorizedAccount(account)) return canBatchManageAccount(account)
  return canEditAccount(account) && account.permissions?.canViewCredentials !== false
}

export function canBatchEditAccount(account: AccountSummary): boolean {
  return !isAuthorizedAccount(account)
    && hasAccountEditPermission(account)
    && account.permissions?.canViewCredentials !== false
}

export function canSelectAccountForBatch(account: AccountSummary): boolean {
  return canBatchEditAccount(account)
    || canBatchManageAccount(account)
    || canBatchRestoreAccount(account)
    || canBatchDeleteAccount(account)
    || canTestAccount(account)
}

export function canBatchRestoreAccount(account: AccountSummary): boolean {
  if (isAccountPackageExpiredStatus(account)) return false
  if (account.status === 'pending_test') return false
  if (isAuthorizedAccount(account)) {
    if (!account.boundGroupId || account.permissions?.canUse === false) return false
    if (account.status === 'disabled') return false
    if (isAuthorizationExpired(account) || isAuthorizationPaused(account) || isAuthorizationBindingUnavailable(account)) return false
    return hasAccountRuntimeRecoveryState(account) || hasAuthorizedInstanceFailureState(account)
  }
  if (account.status === 'disabled') return false
  if (account.status === 'error') return canRestoreException(account)
  if (!canUseAccountActions(account)) return false
  return hasAccountRuntimeRecoveryState(account) || isTemporaryAccountStatus(account) || hasPersistentFailureState(account)
}

export function canRestoreException(account: AccountSummary): boolean {
  return account.status === 'error' && hasAccountEditPermission(account)
}

function hasPersistentFailureState(account: AccountSummary): boolean {
  return Boolean(
    account.cooldownUntil
    || account.lastErrorCode
    || account.lastErrorMessage
    || account.cooldownRetestFailureCount
    || account.cooldownRetestObservationStartedAt
    || account.cooldownRetestLastAt
    || account.cooldownRetestLastStatusCode
    || account.streamFailureCount
    || account.streamFailureWindowStartedAt
  )
}

export function authorizedAccountUnavailableText(account: AccountSummary): string | undefined {
  if (!isAuthorizedAccount(account)) return undefined
  if (account.effectiveAvailability?.available === false) {
    return account.effectiveAvailability.reason ?? account.effectiveAvailability.label
  }
  if (account.permissions?.canUse === false) return '当前授权账户无可用权限'
  if (!account.boundGroupId) return '授权账户需要先绑定到你的分组'
  if (isAuthorizationExpired(account)) return '授权已到期，当前账户不能调用'
  if (isAuthorizationPaused(account)) return '授权已暂停，当前账户不能调用'
  if (isAuthorizationBindingUnavailable(account)) return '当前分组绑定的授权已失效，请重新绑定分组或联系授权人'
  if (account.authorizationQuotaExceeded) return '授权额度已用完，当前账户不能调用'
  if (account.status === 'disabled') return '账户已停用，当前不可用'
  if (account.status === 'pending_test') return '账户正在等待后台健康检查，检查通过前不会参与调度；人工测试不改变账户状态'
  if (account.status === 'error') return '授权账户状态异常，当前不可用'
  if (isTemporaryAccountStatus(account) || isFutureTime(account.cooldownUntil)) return '授权账户实例暂时不可调用，恢复前不会参与调度'
  if (!account.schedulable) return '授权账户实例暂时不可调用，恢复前不会参与调度'
  return undefined
}

function isFutureTime(value?: string): boolean {
  if (!value) return false
  const time = serverDateTimeTimestamp(value)
  return time !== undefined && time > Date.now()
}

export function canUseAuthorizedAccount(account: AccountSummary): boolean {
  return isAuthorizedAccount(account) && !authorizedAccountUnavailableText(account)
}

export function canUseBoundAuthorizedAccount(account: AccountSummary): boolean {
  return canUseAuthorizedAccount(account) && Boolean(account.boundGroupId)
}

export function canTestAccount(account: AccountSummary): boolean {
  if (!isGatewayTestableAccountProfile(account)) return false
  if (isAuthorizedAccount(account)) {
    if (!account.boundGroupId || account.permissions?.canUse === false) return false
    if (account.effectiveAvailability?.available === false) {
      if (account.effectiveAvailability.blockerScope === 'runtime') return true
      if (account.effectiveAvailability.blockerScope !== 'authorized_instance') return false
      if (account.effectiveAvailability.status === 'instance_disabled') return true
      if (account.effectiveAvailability.status === 'instance_pending_test') return true
      const instanceFailureState = hasAuthorizedInstanceFailureState(account)
      return instanceFailureState && (
        account.effectiveAvailability.status === 'instance_error'
        || account.effectiveAvailability.status === 'instance_rate_limited'
        || account.effectiveAvailability.status === 'instance_temporary_unavailable'
        || account.effectiveAvailability.status === 'instance_cooldown'
      )
    }
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
  if (target.providerProtocolProfileId !== source.providerProtocolProfileId) return false
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

export function canManageOAuthAccount(account: AccountSummary): boolean {
  return canUseAccountActions(account) && account.type === 'oauth' && canCreateOAuthAccount({ profile: account })
}

export function accountMenuItems(account: AccountSummary): AccountMenuItem[] {
  const items: AccountMenuItem[] = []
  if (isAuthorizedAccount(account)) {
    if (canTestAccount(account)) {
      items.push({ key: 'test', label: '测试' })
    }
    if (account.status === 'error') {
      if (canBatchRestoreAccount(account)) {
        items.push({ key: 'restore-normal', label: '异常恢复' })
      }
      pushDispatchFlagItems(items, account)
      return items.map(normalizeAccountMenuItem)
    }
    if (account.status !== 'pending_test' && (hasAccountRuntimeRecoveryState(account) || (account.boundGroupId && hasAuthorizedInstanceFailureState(account)))) {
      items.push({ key: 'restore-normal', label: '恢复可调度' })
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
        icon: instanceDisabled ? 'enable' : 'stop',
        tone: instanceDisabled ? 'success' : 'danger',
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
      items.push({ key: 'restore-normal', label: '异常恢复' })
    }
    pushDispatchFlagItems(items, account)
    if (canToggleAccountStatus(account)) {
      pushAccountStatusToggleItem(items, account)
    }
    return items.map(normalizeAccountMenuItem)
  }
  if (isPendingHealthCheckFailed(account) && canToggleAccountStatus(account)) {
    items.push({ key: 'recheck-health', label: '重新检查' })
  }
  if (canUseAccountActions(account)) {
    if (canManageOAuthAccount(account)) {
      items.push({ key: 'refresh-oauth-token', label: '刷新令牌' })
      items.push({ key: 'reauthorize-oauth', label: '重新授权' })
    }
    if (account.status === 'pending_test' || hasAccountRuntimeRecoveryState(account) || isTemporaryAccountStatus(account)) {
      items.push({ key: 'restore-normal', label: '恢复可调度' })
    }
    pushDispatchFlagItems(items, account)
    if (account.status !== 'pending_test') {
      items.push({ key: 'migrate-traffic', label: '迁移流量' })
    }
    if (account.status === 'active') {
      items.push({
        key: 'manual-isolate',
        label: '人工隔离',
        icon: 'pause',
        tone: 'warning'
      })
    }
    if (canToggleAccountStatus(account)) {
      pushAccountStatusToggleItem(items, account)
    }
  }
  return items.map(normalizeAccountMenuItem)
}

function pushAccountStatusToggleItem(items: AccountMenuItem[], account: AccountSummary): void {
  items.push({
    key: 'toggle-status',
    label: account.status === 'disabled' ? '启用账户' : '停用账户',
    danger: account.status !== 'disabled',
    icon: account.status === 'disabled' ? 'enable' : 'stop',
    tone: account.status === 'disabled' ? 'success' : 'danger',
    confirmTitle: account.status === 'disabled'
      ? `确认启用账户「${account.name}」？`
      : `确认停用账户「${account.name}」？停用后该账户将不再参与调度。`,
    confirmOkText: account.status === 'disabled' ? '启用' : '停用'
  })
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
    : account.status === 'active' || account.status === 'pending_test'
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
  if (item.key === 'recheck-health') return { ...item, icon: 'refresh', tone: 'info' }
  if (item.key === 'super-priority-on') return { ...item, icon: 'superPriority', tone: 'warning' }
  if (item.key === 'super-priority-off') return { ...item, icon: 'superPriority', tone: 'default' }
  if (item.key === 'fallback-on') return { ...item, icon: 'fallback', tone: 'purple' }
  if (item.key === 'fallback-off') return { ...item, icon: 'fallback', tone: 'default' }
  if (item.key === 'migrate-traffic') return { ...item, icon: 'migrate', tone: 'purple' }
  return item
}
