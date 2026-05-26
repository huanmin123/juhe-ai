import {
  formatCompactUsageAmount,
  formatDateTime,
  formatNumber,
  formatServerDateTimeInput,
  formatUsd,
  parseDatePickerValue
} from '@/shared/formatters'
import type { AccountStatus, AccountSummary, AccountTestResult, AccountType, AccountUsageSummary } from '@/types/domain'

export interface OAuthUsageBar {
  key: string
  label: string
  percent: number
  displayPercent: string
  resetText: string
  color: string
  tone: string
}

export function statusColor(status: AccountStatus) {
  if (status === 'active') return 'green'
  if (status === 'error') return 'red'
  if (status === 'rate_limited') return 'orange'
  if (status === 'temporary_unavailable') return 'gold'
  return 'default'
}

export function statusText(status: AccountStatus) {
  if (status === 'active') return '正常'
  if (status === 'error') return '异常'
  if (status === 'rate_limited') return '限流中'
  if (status === 'temporary_unavailable') return '临时不可调用'
  return '停用'
}

export function formatErrorPolicyAction(action: NonNullable<AccountTestResult['errorPolicyAction']>): string {
  if (action === 'retry_next') return '切换下一个账号'
  if (action === 'cooldown') return '账号冷却'
  if (action === 'disable') return '标记异常'
  return '无'
}

export function accountErrorCodeText(code?: string): string {
  if (code === 'oauth_token_refresh_failed') return 'OAuth Token 刷新失败'
  if (code === 'upstream_failure') return '上游调用失败'
  if (code === 'account_expired') return '账户过期'
  if (code === 'cooldown_retest_failed') return '后台复测失败'
  return code || '未分类异常'
}

export function accountStatusColor(account: AccountSummary) {
  if (isAuthorizationPaused(account)) return 'orange'
  if (isAuthorizationExpired(account) || isAuthorizationBindingUnavailable(account)) return 'red'
  if (isAccountPackageExpiredStatus(account)) return 'red'
  if (isAuthorizedAccount(account) && account.authorizationQuotaExceeded) return 'red'
  const sourceStatus = authorizedSourceUnavailableStatus(account)
  if (sourceStatus === 'error') return 'red'
  if (sourceStatus === 'cooling') return 'gold'
  if (sourceStatus === 'disabled') return 'default'
  if (isOwnerDisabledAuthorizedAccount(account)) return 'default'
  const runtimeStatus = activeRuntimeAvailabilityStatus(account)
  if (runtimeStatus === 'precheck_pending') return 'blue'
  if (runtimeStatus === 'local_suppressed') return 'gold'
  if (runtimeStatus === 'precheck_failed') return 'gold'
  return statusColor(account.status)
}

export function accountStatusText(account: AccountSummary) {
  if (isAuthorizationPaused(account)) return '授权暂停'
  if (isAuthorizationExpired(account)) return '授权到期'
  if (isAuthorizationBindingUnavailable(account)) return '授权已失效'
  if (isAccountPackageExpiredStatus(account)) return '账户到期'
  if (isAuthorizedAccount(account) && account.authorizationQuotaExceeded) return '授权额度已用完'
  const sourceStatusText = authorizedSourceUnavailableStatusText(account)
  if (sourceStatusText) return sourceStatusText
  if (isOwnerDisabledAuthorizedAccount(account)) return '停用'
  const runtimeStatus = activeRuntimeAvailabilityStatus(account)
  if (runtimeStatus === 'precheck_pending') return '待探针确认'
  if (runtimeStatus === 'local_suppressed') return '短暂避让'
  if (runtimeStatus === 'precheck_failed') return '探针确认失败'
  return statusText(account.status)
}

export function accountCooldownText(account: AccountSummary) {
  if (!isCoolingDown(account)) return ''
  return `暂停至 ${formatDateTime(account.cooldownUntil)}`
}

export function sourceAccountStatusText(account: AccountSummary): string {
  const sourceStatus = account.sourceStatus ? statusText(account.sourceStatus) : '正常'
  if (account.sourceCooldownUntil && isFutureTime(account.sourceCooldownUntil)) {
    return `${sourceStatus}，暂停至 ${formatDateTime(account.sourceCooldownUntil)}`
  }
  return sourceStatus
}

export function accountStatusTooltipLines(account: AccountSummary): string[] {
  const lines: string[] = []
  if (isAuthorizedAccount(account) && account.authorizationExpiresAt) {
    lines.push(`授权到期时间：${formatDateTime(account.authorizationExpiresAt)}`)
  }
  if (account.accountExpiresAt) {
    lines.push(`账户到期时间：${formatDateTime(account.accountExpiresAt)}`)
  }
  const accountExpired = isAccountPackageExpiredStatus(account)
  if (isAuthorizationExpired(account)) {
    lines.push('授权已到期，当前不可用')
  }
  if (isAuthorizationPaused(account)) {
    lines.push('授权已暂停，当前不可用')
  }
  if (accountExpired) {
    lines.push('账户已到期，当前不可用')
  }
  if (isAuthorizedAccount(account) && account.authorizationQuotaExceeded && !isAuthorizationExpired(account)) {
    lines.push('授权额度已用完，当前调用会被拦截')
  }
  if (isAuthorizedAccount(account) && account.sourceStatus) {
    lines.push(`授权来源状态：${sourceAccountStatusText(account)}`)
  }
  if (isAuthorizedAccount(account) && account.sourceSchedulable === false) {
    lines.push('授权来源已暂停调度')
  }
  const sourceUnavailableText = authorizedSourceUnavailableTooltipText(account)
  if (sourceUnavailableText) {
    lines.push(sourceUnavailableText)
  }
  lines.push(...accountRuntimeAvailabilityTooltipLines(account))
  if (isAuthorizationBindingUnavailable(account)) {
    lines.push('当前分组绑定的授权已失效，请重新绑定分组或联系授权人')
  }
  const cooldownText = accountCooldownText(account)
  if (cooldownText) {
    lines.push(cooldownText)
    if (isTemporaryAccountStatus(account) && account.cooldownRetestFailureCount) {
      lines.push(`后台复测连续失败：${formatNumber(account.cooldownRetestFailureCount)} 次`)
    }
  } else if (isTemporaryAccountStatus(account) && account.cooldownUntil) {
    lines.push(`已到期：${formatDateTime(account.cooldownUntil)}`)
    lines.push('等待后台复测；也可手动测试或在更多菜单恢复正常')
    if (account.cooldownRetestFailureCount) {
      lines.push(`后台复测连续失败：${formatNumber(account.cooldownRetestFailureCount)} 次`)
    }
  } else if (account.status === 'disabled' && !accountExpired) {
    lines.push('停用账户可手动测试诊断，但不会被测试结果或后台任务自动恢复')
  } else if (account.status === 'error') {
    lines.push(`异常类型：${accountErrorCodeText(account.lastErrorCode)}`)
    if (account.cooldownRetestFailureCount) {
      lines.push(`后台复测连续失败：${formatNumber(account.cooldownRetestFailureCount)} 次`)
    }
    lines.push(account.lastErrorCode === 'oauth_token_refresh_failed'
      ? 'OAuth 刷新失败异常会在后台刷新成功后自动恢复，也可手动恢复异常'
      : '异常账户不会参与调度，仅保留测试和恢复异常')
  }
  if (account.cooldownRetestLastAt) {
    const suffix = account.cooldownRetestLastStatusCode ? `，HTTP ${account.cooldownRetestLastStatusCode}` : ''
    lines.push(`最近后台复测：${formatDateTime(account.cooldownRetestLastAt)}${suffix}`)
  }
  if (account.cooldownRetestObservationStartedAt && isTemporaryAccountStatus(account)) {
    lines.push(`自动恢复观察开始：${formatDateTime(account.cooldownRetestObservationStartedAt)}`)
  }
  if (account.lastErrorMessage) {
    lines.push(`原因：${account.lastErrorMessage}`)
  }
  return lines
}

function activeRuntimeAvailabilityStatus(account: AccountSummary) {
  const status = account.runtimeAvailability?.status
  if (!status || status === 'normal') return undefined
  if (account.status !== 'active') return undefined
  return status
}

function accountRuntimeAvailabilityTooltipLines(account: AccountSummary): string[] {
  const runtime = account.runtimeAvailability
  if (!runtime || runtime.status === 'normal') return []
  const lines = [
    `运行态状态：${runtimeAvailabilityText(runtime.status)}`,
    account.status === 'active'
      ? `数据库状态仍为${statusText(account.status)}；此状态只保存在当前网关进程缓存，不写入数据库`
      : `数据库状态：${statusText(account.status)}；运行态仅说明当前网关进程最近的确认过程`
  ]
  if (runtime.since) {
    lines.push(`进入时间：${formatDateTime(runtime.since)}`)
  }
  if (runtime.until) {
    lines.push(`预计释放：${formatDateTime(runtime.until)}`)
  }
  if (runtime.failureCount) {
    lines.push(`短窗口失败：${formatNumber(runtime.failureCount)} 次`)
  }
  if (runtime.distinctClientIpCount) {
    lines.push(`来源 IP：${formatNumber(runtime.distinctClientIpCount)} 个`)
  }
  if (runtime.distinctApiKeyCount) {
    lines.push(`API Key：${formatNumber(runtime.distinctApiKeyCount)} 个`)
  }
  if (runtime.precheckAttemptCount) {
    lines.push(`事前探针：${formatNumber(runtime.precheckAttemptCount)} 次`)
  }
  if (runtime.reason) {
    lines.push(`原因：${runtime.reason}`)
  }
  return lines
}

function runtimeAvailabilityText(status: NonNullable<AccountSummary['runtimeAvailability']>['status']): string {
  if (status === 'precheck_pending') return '待探针确认'
  if (status === 'local_suppressed') return '短暂避让'
  if (status === 'precheck_failed') return '探针确认失败'
  return '正常'
}

export function isTemporaryAccountStatus(account: AccountSummary) {
  return account.status === 'rate_limited' || account.status === 'temporary_unavailable'
}

export function isCoolingDown(account: AccountSummary) {
  if (!account.cooldownUntil) return false
  return isFutureTime(account.cooldownUntil)
}

function isFutureTime(value?: string): boolean {
  if (!value) return false
  const time = new Date(value).getTime()
  return Number.isFinite(time) && time > Date.now()
}

export function isAccountPackageExpired(account: AccountSummary) {
  if (!account.accountExpiresAt) return false
  const time = new Date(account.accountExpiresAt).getTime()
  return Number.isFinite(time) && time <= Date.now()
}

export function isAccountPackageExpiredStatus(account: AccountSummary): boolean {
  if (isAuthorizedAccount(account)) {
    return isAccountPackageExpired(account)
      || account.sourceLastErrorCode === 'account_expired'
  }
  return isAccountPackageExpired(account)
    || account.lastErrorCode === 'account_expired'
}

export function isAuthorizationExpired(account: AccountSummary): boolean {
  if (!isAuthorizedAccount(account)) return false
  if (account.authorizationStatus === 'expired') return true
  if (!account.authorizationExpiresAt) return false
  const time = new Date(account.authorizationExpiresAt).getTime()
  return Number.isFinite(time) && time <= Date.now()
}

export function isAuthorizationPaused(account: AccountSummary): boolean {
  return isAuthorizedAccount(account) && account.authorizationStatus === 'paused'
}

export function isAuthorizationBindingUnavailable(account: AccountSummary): boolean {
  return isAuthorizedAccount(account)
    && account.groupBindStatus === 'authorization_unavailable'
    && !isAuthorizationExpired(account)
    && !isAuthorizationPaused(account)
}

export function accountDisplayExpiresAt(account: AccountSummary): string | undefined {
  if (isAuthorizedAccount(account) && account.authorizationExpiresAt) {
    return account.authorizationExpiresAt
  }
  return account.accountExpiresAt
}

export function isAccountDisplayExpired(account: AccountSummary): boolean {
  const expiresAt = accountDisplayExpiresAt(account)
  if (!expiresAt) return false
  const time = new Date(expiresAt).getTime()
  return Number.isFinite(time) && time <= Date.now()
}

export function accountTypeText(type: AccountType) {
  if (type === 'oauth') return 'OAuth'
  if (type === 'api_key') return 'API Key'
  return type || '-'
}

export function accountTypeTitle(providerName: string, type: AccountType) {
  if (type === 'oauth') return `${providerName} OAuth`
  if (type === 'api_key') return `${providerName} API Key`
  return `${providerName} ${type}`.trim()
}

export function accountTypeDescription(providerCode: string, type: AccountType) {
  if (providerCode === 'openai' && type === 'oauth') return '适合 Codex / ChatGPT OAuth 授权账户；网关只支持 Responses / compact 路径。'
  if (providerCode === 'openai' && type === 'api_key') return '适合公开 OpenAI-compatible 透传，可配置 Base URL。'
  return '该账户类型会使用供应商定义的创建流程。'
}

export function isAuthorizedAccount(account: AccountSummary): boolean {
  return account.accessType === 'authorized'
}

export function isOwnerDisabledAuthorizedAccount(account: AccountSummary): boolean {
  return isAuthorizedAccount(account)
    && account.status === 'disabled'
    && !authorizedSourceUnavailableStatus(account)
}

function authorizedSourceUnavailableStatus(account: AccountSummary): 'disabled' | 'error' | 'cooling' | undefined {
  if (!isAuthorizedAccount(account)) return undefined
  if (account.sourceStatus === 'disabled') return 'disabled'
  if (account.sourceStatus === 'error') return 'error'
  if (account.sourceSchedulable === false) return 'disabled'
  if (account.sourceStatus === 'rate_limited' || account.sourceStatus === 'temporary_unavailable') return 'cooling'
  if (account.sourceCooldownUntil && isFutureTime(account.sourceCooldownUntil)) return 'cooling'
  return undefined
}

function authorizedSourceUnavailableStatusText(account: AccountSummary): string | undefined {
  if (!isAuthorizedAccount(account)) return undefined
  if (account.sourceStatus === 'disabled') return '授权来源停用'
  if (account.sourceStatus === 'error') return '授权来源异常'
  if (account.sourceSchedulable === false) return '授权来源停调'
  if (account.sourceStatus === 'rate_limited' || account.sourceStatus === 'temporary_unavailable') return '授权来源冷却'
  if (account.sourceCooldownUntil && isFutureTime(account.sourceCooldownUntil)) return '授权来源冷却'
  return undefined
}

function authorizedSourceUnavailableTooltipText(account: AccountSummary): string | undefined {
  const status = authorizedSourceUnavailableStatus(account)
  if (!status) return undefined
  if (status === 'error') return '授权来源账户异常，当前不会参与调度'
  if (status === 'cooling') return '授权来源账户暂时不可调用，当前不会参与调度'
  if (account.sourceSchedulable === false) return '授权来源账户已暂停调度，当前不会参与调度'
  return '授权来源账户已停用，当前不会参与调度'
}

export function asString(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

export function normalizeKeyword(value: unknown): string {
  return String(value ?? '').trim().toLowerCase()
}

export function formatAccountUsageSummary(usage: AccountUsageSummary): string {
  return `${formatNumber(usage.requestCount)}req / ${formatUsageAmount(usage.totalTokens)} / ${formatCost(usage.totalCost)}`
}

export function formatUsageAmount(value?: number): string {
  return formatCompactUsageAmount(value)
}

export function formatCost(value?: number): string {
  return formatUsd(value)
}

export function oauthUsageBars(account: AccountSummary): OAuthUsageBar[] {
  if (account.providerCode !== 'openai' || account.type !== 'oauth') return []
  const usage = account.oauthUsage
  if (!usage) return []
  return [
    oauthUsageBar('5h', '5h', usage.fiveHour),
    oauthUsageBar('7d', '7d', usage.sevenDay)
  ].filter((bar): bar is OAuthUsageBar => Boolean(bar))
}

export function formatRelativeReset(value: string): string {
  const time = Date.parse(value)
  if (!Number.isFinite(time)) return value
  const diffMs = time - Date.now()
  if (diffMs <= 0) return '现在'
  const totalMinutes = Math.ceil(diffMs / 60_000)
  const days = Math.floor(totalMinutes / 1440)
  const hours = Math.floor((totalMinutes % 1440) / 60)
  const minutes = totalMinutes % 60
  if (days > 0) return `${days}d ${hours}h`
  if (hours > 0) return `${hours}h ${minutes}m`
  return `${minutes}m`
}

export { formatDateTime, formatNumber, formatServerDateTimeInput, parseDatePickerValue }

export function accountLastUsedAt(account: AccountSummary): string | undefined {
  return account.lastUsedAt || account.usage.lastUsedAt
}

export function compareAccountLastUsedAt(left: AccountSummary, right: AccountSummary): number {
  return timestampOf(accountLastUsedAt(left)) - timestampOf(accountLastUsedAt(right))
}

export function compareAccountExpiresAt(left: AccountSummary, right: AccountSummary): number {
  return timestampOf(accountDisplayExpiresAt(left)) - timestampOf(accountDisplayExpiresAt(right))
}

export function compareAccountConcurrency(left: AccountSummary, right: AccountSummary): number {
  return left.concurrencyLimit - right.concurrencyLimit || left.currentConcurrency - right.currentConcurrency || left.name.localeCompare(right.name, 'zh-CN')
}

export function formatTestTerminalResult(result: AccountTestResult): string {
  if (result.outputText?.trim()) return result.outputText.trim()
  if (result.success) return ''
  const rawText = result.responseText?.trim()
  if (!rawText || rawText === result.message.trim()) return ''
  return rawText
}

export function formatAccountTestDuration(value?: number): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return '-'
  if (value < 1000) return `${Math.max(0, Math.round(value))} ms`
  return `${(value / 1000).toFixed(2)} s`
}

function oauthUsageBar(key: string, label: string, window?: { utilization: number; resetsAt?: string; remainingSeconds: number }): OAuthUsageBar | undefined {
  if (!window) return undefined
  const rawPercent = Math.max(0, window.utilization)
  const percent = Math.min(Math.round(rawPercent), 100)
  return {
    key,
    label,
    percent,
    displayPercent: rawPercent > 999 ? '>999%' : `${Math.round(rawPercent)}%`,
    resetText: window.resetsAt ? formatRelativeReset(window.resetsAt) : '现在',
    color: rawPercent >= 100 ? '#ef4444' : rawPercent >= 80 ? '#f59e0b' : '#22c55e',
    tone: rawPercent >= 100 ? 'danger' : rawPercent >= 80 ? 'warning' : 'normal'
  }
}

function timestampOf(value?: string): number {
  if (!value) return 0
  const timestamp = Date.parse(value)
  return Number.isFinite(timestamp) ? timestamp : 0
}
