import {
  formatCompactUsageAmount,
  formatDateTime,
  formatNumber,
  formatServerDateTimeInput,
  formatUsd,
  parseDatePickerValue
} from '@/shared/formatters'
import type { AccountStatus, AccountSummary, AccountTestResult, AccountType, AccountUsageSummary } from '@/types/domain'

export type SchedulableFilter = 'all' | 'enabled' | 'disabled' | 'cooling'

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
  if (status === 'error') return '错误'
  if (status === 'rate_limited') return '限流中'
  if (status === 'temporary_unavailable') return '临时不可调用'
  return '停用'
}

export function formatErrorPolicyAction(action: NonNullable<AccountTestResult['errorPolicyAction']>): string {
  if (action === 'retry_next') return '切换下一个账号'
  if (action === 'cooldown') return '账号冷却'
  if (action === 'disable') return '标记错误'
  if (action === 'default_cooldown') return '默认临时不可调用'
  return '无'
}

export function accountStatusColor(account: AccountSummary) {
  if (isOwnerDisabledAuthorizedAccount(account)) return 'default'
  return statusColor(account.status)
}

export function accountStatusText(account: AccountSummary) {
  if (isOwnerDisabledAuthorizedAccount(account)) return '停用'
  return statusText(account.status)
}

export function accountCooldownText(account: AccountSummary) {
  if (!isCoolingDown(account)) return ''
  return `暂停至 ${formatDateTime(account.cooldownUntil)}`
}

export function accountStatusTooltipLines(account: AccountSummary): string[] {
  const lines: string[] = []
  if (account.accountExpiresAt) {
    lines.push(`账户到期时间：${formatDateTime(account.accountExpiresAt)}`)
  }
  const cooldownText = accountCooldownText(account)
  if (cooldownText) {
    lines.push(cooldownText)
  } else if (isTemporaryAccountStatus(account) && account.cooldownUntil) {
    lines.push(`已到期：${formatDateTime(account.cooldownUntil)}`)
    lines.push('等待后台复测；也可手动测试，成功后恢复正常')
  }
  if (account.lastErrorMessage) {
    lines.push(`原因：${account.lastErrorMessage}`)
  }
  return lines
}

export function isTemporaryAccountStatus(account: AccountSummary) {
  return account.status === 'rate_limited' || account.status === 'temporary_unavailable'
}

export function isCoolingDown(account: AccountSummary) {
  if (!account.cooldownUntil) return false
  const time = new Date(account.cooldownUntil).getTime()
  return Number.isFinite(time) && time > Date.now()
}

export function isAccountPackageExpired(account: AccountSummary) {
  if (!account.accountExpiresAt) return false
  const time = new Date(account.accountExpiresAt).getTime()
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
  if (providerCode === 'openai' && type === 'oauth') return '适合 Codex / ChatGPT OAuth 授权账户，支持手动授权或 Refresh Token。'
  if (providerCode === 'openai' && type === 'api_key') return '适合直接粘贴 OpenAI API Key，可配置 Base URL。'
  return '该账户类型会使用供应商定义的创建流程。'
}

export function isAuthorizedAccount(account: AccountSummary): boolean {
  return account.accessType === 'authorized'
}

export function isOwnerDisabledAuthorizedAccount(account: AccountSummary): boolean {
  return isAuthorizedAccount(account) && account.status === 'disabled'
}

export function asString(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

export function normalizeKeyword(value: unknown): string {
  return String(value ?? '').trim().toLowerCase()
}

export function matchesSchedulableFilter(account: AccountSummary, filter: SchedulableFilter): boolean {
  if (filter === 'all') return true
  if (filter === 'cooling') return isTemporaryAccountStatus(account) || isCoolingDown(account)
  if (filter === 'enabled') return account.status === 'active' && account.schedulable && !isTemporaryAccountStatus(account) && !isCoolingDown(account)
  return account.status === 'disabled' || !account.schedulable
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
  return timestampOf(left.accountExpiresAt) - timestampOf(right.accountExpiresAt)
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
