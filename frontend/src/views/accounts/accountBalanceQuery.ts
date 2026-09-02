import type { AccountBalanceSnapshot, AccountListItem } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'

export function canManuallyRefreshAccountBalance(
  account: Pick<AccountListItem, 'balanceQueryEnabled' | 'accessType'>
): boolean {
  return account.balanceQueryEnabled === true && account.accessType !== 'authorized'
}

export function formatAccountBalance(
  snapshot?: AccountBalanceSnapshot,
  account?: Pick<AccountListItem, 'status' | 'schedulable'>
): {
  text: string
  tone: 'pending' | 'refreshing' | 'fresh' | 'unlimited' | 'unsupported' | 'failed'
  tooltip?: string
  refreshing: boolean
  visible: boolean
} {
  if (!snapshot) return pendingBalanceDisplay('pending', false, pendingBalanceTooltip(account))
  if (snapshot.status === 'failed') {
    const partial = snapshot.keyCount && snapshot.queriedKeyCount !== undefined && snapshot.queriedKeyCount < snapshot.keyCount
    return {
      text: partial ? `部分失败（${snapshot.queriedKeyCount}/${snapshot.keyCount}）` : '余额查询失败',
      tone: 'failed',
      tooltip: snapshot.errorMessage,
      refreshing: false,
      visible: true
    }
  }
  if (snapshot.status === 'unsupported') {
    return {
      text: snapshot.keyCount && snapshot.keyCount > 1 ? '无法安全合计' : '余额查询失败',
      tone: 'failed',
      tooltip: snapshot.errorMessage ?? '当前配置未找到可用余额接口',
      refreshing: false,
      visible: true
    }
  }
  if (snapshot.status === 'refreshing') {
    return snapshot.remainingUsd !== undefined
      ? { text: formatUsdAmount(snapshot.remainingUsd), tone: 'fresh', tooltip: undefined, refreshing: true, visible: true }
      : pendingBalanceDisplay('refreshing', true)
  }
  const retryTooltip = transientFailureTooltip(snapshot, true)
  if (snapshot.status === 'unlimited') return { text: '不限额', tone: 'unlimited', tooltip: retryTooltip, refreshing: false, visible: true }
  if (snapshot.status === 'fresh' && snapshot.remainingUsd !== undefined) {
    return {
      text: formatUsdAmount(snapshot.remainingUsd),
      tone: 'fresh',
      tooltip: retryTooltip,
      refreshing: false,
      visible: true
    }
  }
  if (snapshot.status === 'pending' && snapshot.consecutiveTransientFailures) {
    return {
      text: '暂时无法查询',
      tone: 'pending',
      tooltip: snapshot.lastTransientErrorMessage,
      refreshing: false,
      visible: true
    }
  }
  return pendingBalanceDisplay('pending', false, pendingBalanceTooltip(account))
}

function formatUsdAmount(value: string | number): string {
  const text = String(value).trim()
  const match = /^(-?)(\d+)(?:\.(\d+))?$/.exec(text)
  if (!match) return '余额查询失败'
  const negative = match[1] === '-'
  const fraction = match[3] ?? ''
  const padded = `${fraction}00`.slice(0, 3)
  let cents = BigInt(padded.slice(0, 2))
  if (padded.length > 2 && Number(padded[2]) >= 5) cents += 1n
  let integer = BigInt(match[2]!)
  if (cents >= 100n) {
    integer += 1n
    cents -= 100n
  }
  return `${negative ? '-' : ''}$${integer.toString()}.${cents.toString().padStart(2, '0')}`
}

function pendingBalanceDisplay(
  tone: 'pending' | 'refreshing',
  refreshing = false,
  tooltip?: string
): ReturnType<typeof formatAccountBalance> {
  return {
    text: tone === 'refreshing' ? '查询中' : '待查询',
    tone,
    tooltip,
    refreshing,
    visible: true
  }
}

function pendingBalanceTooltip(account?: Pick<AccountListItem, 'status' | 'schedulable'>): string | undefined {
  if (!account || (account.status === 'active' && account.schedulable)) return undefined
  return '账户当前不可自动调度；恢复可用后会自动查询，也可点击刷新图标手动查询'
}

function transientFailureTooltip(snapshot: AccountBalanceSnapshot, preservesResult: boolean): string | undefined {
  const count = snapshot.consecutiveTransientFailures
  const message = snapshot.lastTransientErrorMessage
  if (!count || !message) return undefined
  return `刷新暂时失败（${count}/3）：${message}${preservesResult ? '；当前显示上次成功余额' : ''}`
}

export function buildAccountBalancePayload(form: Pick<AccountFormModel,
  | 'type' | 'apiKeys' | 'balanceQueryEnabled' | 'balanceQueryAdapter' | 'balanceQueryIntervalMinutes'
  | 'balanceQueryPreferredBuiltinAdapter'
  | 'balanceQueryCustomPath' | 'balanceQueryRemainingPointer' | 'balanceQueryTotalPointer'
  | 'balanceQueryUsedPointer' | 'balanceQueryDivisor'
>): { balanceQueryEnabled: boolean; balanceQueryConfig?: Record<string, unknown> } | undefined {
  const apiKeys = effectiveFormApiKeys(form.apiKeys)
  if (form.type !== 'api_key' || apiKeys.length === 0) return undefined
  if (!form.balanceQueryEnabled) return { balanceQueryEnabled: false }
  return { balanceQueryEnabled: true, balanceQueryConfig: buildBalanceQueryConfig(form) }
}

export function accountBalanceWillAutoDisable(form: Pick<AccountFormModel, 'type' | 'apiKeys' | 'balanceQueryEnabled'>): boolean {
  return false
}

function buildBalanceQueryConfig(form: Pick<AccountFormModel,
  | 'balanceQueryAdapter' | 'balanceQueryIntervalMinutes' | 'balanceQueryPreferredBuiltinAdapter'
  | 'balanceQueryCustomPath' | 'balanceQueryRemainingPointer' | 'balanceQueryTotalPointer'
  | 'balanceQueryUsedPointer' | 'balanceQueryDivisor'
>): Record<string, unknown> {
  const config: Record<string, unknown> = {
    adapter: form.balanceQueryAdapter,
    intervalMinutes: form.balanceQueryIntervalMinutes
  }
  if (form.balanceQueryAdapter === 'builtin' && form.balanceQueryPreferredBuiltinAdapter) {
    config.preferredBuiltinAdapter = form.balanceQueryPreferredBuiltinAdapter
  }
  if (form.balanceQueryAdapter === 'custom') {
    config.custom = compact({
      path: form.balanceQueryCustomPath.trim(),
      remainingPointer: form.balanceQueryRemainingPointer.trim(),
      totalPointer: form.balanceQueryTotalPointer.trim(),
      usedPointer: form.balanceQueryUsedPointer.trim(),
      divisor: form.balanceQueryDivisor.trim()
    })
  }
  return config
}

export function validateAccountBalanceForm(form: Pick<AccountFormModel,
  | 'type' | 'apiKeys' | 'balanceQueryEnabled' | 'balanceQueryAdapter' | 'balanceQueryIntervalMinutes'
  | 'balanceQueryPreferredBuiltinAdapter'
  | 'balanceQueryCustomPath' | 'balanceQueryRemainingPointer' | 'balanceQueryTotalPointer'
  | 'balanceQueryUsedPointer' | 'balanceQueryDivisor'
>): string | undefined {
  if (!form.balanceQueryEnabled) return undefined
  const apiKeys = effectiveFormApiKeys(form.apiKeys)
  if (form.type !== 'api_key') return '上游余额查询仅支持 API Key 账户'
  if (apiKeys.length < 1) return '上游余额查询需要至少一个有效的 API Key'
  return validateBalanceQueryConfigForm(form)
}

function validateBalanceQueryConfigForm(form: Pick<AccountFormModel,
  | 'balanceQueryAdapter' | 'balanceQueryIntervalMinutes'
  | 'balanceQueryCustomPath' | 'balanceQueryRemainingPointer' | 'balanceQueryTotalPointer'
  | 'balanceQueryUsedPointer' | 'balanceQueryDivisor'
>): string | undefined {
  if (!Number.isInteger(form.balanceQueryIntervalMinutes) || form.balanceQueryIntervalMinutes < 1 || form.balanceQueryIntervalMinutes > 10) {
    return '余额刷新周期必须是 1 到 10 分钟的整数'
  }
  if (form.balanceQueryAdapter !== 'custom') return undefined
  if (!form.balanceQueryCustomPath.startsWith('/') || form.balanceQueryCustomPath.startsWith('//')) return '自定义余额接口必须填写同源相对路径'
  const hasRemaining = Boolean(form.balanceQueryRemainingPointer.trim())
  const hasTotalAndUsed = Boolean(form.balanceQueryTotalPointer.trim()) && Boolean(form.balanceQueryUsedPointer.trim())
  if (hasRemaining === hasTotalAndUsed) return '请填写余额字段，或同时填写总额字段和已用字段'
  for (const pointer of [form.balanceQueryRemainingPointer, form.balanceQueryTotalPointer, form.balanceQueryUsedPointer].filter(Boolean)) {
    if (!/^(?:\/(?:[^~/]|~[01])*)*$/.test(pointer.trim())) return '余额字段必须使用合法 JSON Pointer'
  }
  if (form.balanceQueryDivisor.trim() && !/^(?:0*[1-9]\d*)(?:\.\d+)?$|^0\.0*[1-9]\d*$/.test(form.balanceQueryDivisor.trim())) {
    return '金额除数必须是正数'
  }
  return undefined
}

function compact(value: Record<string, string>): Record<string, string> {
  return Object.fromEntries(Object.entries(value).filter(([, item]) => item !== ''))
}

function effectiveFormApiKeys(values: readonly string[]): string[] {
  return [...new Set(values.map((item) => item.trim()).filter(Boolean))]
}
