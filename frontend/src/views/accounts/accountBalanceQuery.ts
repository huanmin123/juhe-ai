import type { AccountBalanceSnapshot } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'

export function formatAccountBalance(snapshot?: AccountBalanceSnapshot): {
  text: string
  tone: 'pending' | 'refreshing' | 'fresh' | 'unlimited' | 'unsupported' | 'failed'
  tooltip?: string
  refreshing: boolean
} {
  if (!snapshot) return { text: '待查询', tone: 'pending', tooltip: undefined, refreshing: false }
  if (snapshot.status === 'failed') {
    return { text: '查询失败', tone: 'failed', tooltip: snapshot.errorMessage, refreshing: false }
  }
  if (snapshot.status === 'refreshing') return { text: '查询中', tone: 'refreshing', tooltip: undefined, refreshing: true }
  if (snapshot.status === 'unlimited') return { text: '无限', tone: 'unlimited', tooltip: undefined, refreshing: false }
  if (snapshot.status === 'unsupported') return { text: '未提供', tone: 'unsupported', tooltip: undefined, refreshing: false }
  if (snapshot.status === 'fresh' && snapshot.remainingUsd !== undefined) {
    const amount = Number(snapshot.remainingUsd)
    return {
      text: Number.isFinite(amount) ? `$${amount.toFixed(2)}` : '查询失败',
      tone: Number.isFinite(amount) ? 'fresh' : 'failed',
      tooltip: Number.isFinite(amount) ? undefined : '上游返回的余额金额无效',
      refreshing: false
    }
  }
  return { text: '待查询', tone: 'pending', tooltip: undefined, refreshing: false }
}

export function buildAccountBalancePayload(form: Pick<AccountFormModel,
  | 'type' | 'apiKeys' | 'balanceQueryEnabled' | 'balanceQueryAdapter' | 'balanceQueryIntervalMinutes'
  | 'balanceQueryCustomPath' | 'balanceQueryRemainingPointer' | 'balanceQueryTotalPointer'
  | 'balanceQueryUsedPointer' | 'balanceQueryDivisor'
>): { balanceQueryEnabled: boolean; balanceQueryConfig?: Record<string, unknown> } | undefined {
  const apiKeys = form.apiKeys.map((item) => item.trim()).filter(Boolean)
  if (form.type !== 'api_key' || apiKeys.length !== 1) return undefined
  if (!form.balanceQueryEnabled) return { balanceQueryEnabled: false }
  const config: Record<string, unknown> = {
    adapter: form.balanceQueryAdapter,
    intervalMinutes: form.balanceQueryIntervalMinutes
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
  return { balanceQueryEnabled: true, balanceQueryConfig: config }
}

export function validateAccountBalanceForm(form: Pick<AccountFormModel,
  | 'type' | 'apiKeys' | 'balanceQueryEnabled' | 'balanceQueryAdapter' | 'balanceQueryIntervalMinutes'
  | 'balanceQueryCustomPath' | 'balanceQueryRemainingPointer' | 'balanceQueryTotalPointer'
  | 'balanceQueryUsedPointer' | 'balanceQueryDivisor'
>): string | undefined {
  if (!form.balanceQueryEnabled) return undefined
  const apiKeys = form.apiKeys.map((item) => item.trim()).filter(Boolean)
  if (form.type !== 'api_key' || apiKeys.length !== 1) return '上游余额查询仅支持单 API Key 账户'
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
