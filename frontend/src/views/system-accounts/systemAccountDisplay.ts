import type { SystemAccountListItem, UserRequestLimits } from '@/types/domain'

// 与编辑弹窗「用户限制」区同源的三态语义：请求限额留空继承全局、0=不限、
// 到期日次日起整组失效；AI 账户上限留空继承全局、0=不限；特供上限留空默认
// 3、0=禁止新增/重新启用。
const requestLimitWindows: Array<{ key: keyof Omit<UserRequestLimits, 'expiresOn'>; unit: string }> = [
  { key: 'perMinute', unit: '/分' },
  { key: 'perDay', unit: '/日' },
  { key: 'perWeek', unit: '/周' },
  { key: 'perMonth', unit: '/月' }
]

export function systemAccountRequestLimitsText(limits?: UserRequestLimits): string {
  const segments = requestLimitWindows
    .filter(({ key }) => limits?.[key] !== undefined)
    .map(({ key, unit }) => `${limitValueText(limits?.[key])}${unit}`)
  if (!segments.length) return '继承全局'
  const expiresOn = limits?.expiresOn
  return expiresOn ? `${segments.join(' ')}（至 ${expiresOn}）` : segments.join(' ')
}

export function systemAccountAccountLimitsText(item: Pick<SystemAccountListItem, 'aiAccountLimit' | 'expeditedAccountLimit'>): string {
  const ai = item.aiAccountLimit === undefined ? '继承' : limitValueText(item.aiAccountLimit)
  const expedited = item.expeditedAccountLimit === null || item.expeditedAccountLimit === undefined
    ? '默认'
    : item.expeditedAccountLimit === 0 ? '禁止' : String(item.expeditedAccountLimit)
  return `AI ${ai} · 特供 ${expedited}`
}

function limitValueText(value: number | undefined): string {
  return value === 0 ? '不限' : String(value)
}
