import type { AccountBalanceCustomConfig, AccountBalanceSnapshot } from './account-balance.types.js'

interface DecimalValue {
  coefficient: bigint
  scale: number
}

export type OpenAiCompatibleBillingOptions =
  | { rawUnit: 'usd' | 'cny' | 'quota', divisor?: string }
  | { snapshot: AccountBalanceSnapshot }

function decimalValue(value: unknown, field: string): DecimalValue {
  const text = typeof value === 'number' && Number.isFinite(value) ? String(value) : typeof value === 'string' ? value.trim() : ''
  const match = /^(-?)(\d+)(?:\.(\d+))?$/.exec(text)
  if (!match) throw new Error(`${field} 不是有效数字`)
  const fraction = match[3] ?? ''
  return {
    coefficient: BigInt(`${match[1] ?? ''}${match[2]}${fraction}`),
    scale: fraction.length
  }
}

function powerOfTen(scale: number): bigint {
  return 10n ** BigInt(scale)
}

function decimalSubtract(left: DecimalValue, right: DecimalValue): DecimalValue {
  const scale = Math.max(left.scale, right.scale)
  return {
    coefficient: left.coefficient * powerOfTen(scale - left.scale) - right.coefficient * powerOfTen(scale - right.scale),
    scale
  }
}

function decimalDivideByHundred(value: DecimalValue): DecimalValue {
  return { coefficient: value.coefficient, scale: value.scale + 2 }
}

function decimalText(value: DecimalValue): string {
  const negative = value.coefficient < 0n
  const digits = (negative ? -value.coefficient : value.coefficient).toString().padStart(value.scale + 1, '0')
  const integer = value.scale ? digits.slice(0, -value.scale) : digits
  const fraction = value.scale ? digits.slice(-value.scale).replace(/0+$/, '') : ''
  return `${negative ? '-' : ''}${integer}${fraction ? `.${fraction}` : ''}`
}

function decimalDivideToSix(value: DecimalValue, divisor: DecimalValue): string {
  if (divisor.coefficient <= 0n) throw new Error('金额除数必须是正数')
  const negative = value.coefficient < 0n
  const numerator = (negative ? -value.coefficient : value.coefficient) * powerOfTen(divisor.scale + 6)
  const denominator = divisor.coefficient * powerOfTen(value.scale)
  const quotient = numerator / denominator
  const remainder = numerator % denominator
  const rounded = remainder * 2n >= denominator ? quotient + 1n : quotient
  const digits = rounded.toString().padStart(7, '0')
  const text = `${digits.slice(0, -6)}.${digits.slice(-6)}`
  return negative && rounded > 0n ? `-${text}` : text
}

function freshResult(value: DecimalValue, options: {
  divisor?: DecimalValue
  rawUnit: 'usd' | 'cny' | 'quota'
  basis: 'api_key_quota' | 'budget' | 'subscription' | 'wallet' | 'custom'
  rawRemaining?: string
}): AccountBalanceSnapshot {
  return {
    status: 'fresh',
    remainingUsd: decimalDivideToSix(value, options.divisor ?? { coefficient: 1n, scale: 0 }),
    rawRemaining: options.rawRemaining ?? decimalText(value),
    rawUnit: options.rawUnit,
    basis: options.basis
  }
}

function objectValue(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('余额接口响应必须是 JSON 对象')
  return value as Record<string, unknown>
}

export function parseSub2ApiBalance(payload: unknown): AccountBalanceSnapshot {
  const response = objectValue(payload)
  if (response.unit !== 'USD') throw new Error('Sub2API 余额单位必须是 USD')
  const rawRemaining = response.remaining ?? response.balance
  const remaining = decimalValue(rawRemaining, response.remaining === undefined ? 'balance' : 'remaining')
  const isWalletBalance = response.planName === '钱包余额' || response.balance !== undefined
  const basis = response.mode === 'quota_limited'
    ? 'api_key_quota'
    : isWalletBalance
      ? 'wallet'
      : 'subscription'
  if (basis === 'subscription' && remaining.coefficient === -powerOfTen(remaining.scale)) {
    return { status: 'unlimited', basis: 'subscription' }
  }
  return freshResult(remaining, {
    rawUnit: 'usd',
    basis,
    rawRemaining: decimalText(remaining)
  })
}

export function parseNewApiBalance(payload: unknown, options: { quotaPerUnit: unknown }): AccountBalanceSnapshot {
  const data = objectValue(objectValue(payload).data)
  if (data.unlimited_quota === true) return { status: 'unsupported', basis: 'api_key_quota' }
  const remaining = decimalValue(data.total_available, 'total_available')
  const divisor = decimalValue(options.quotaPerUnit, 'quota_per_unit')
  return freshResult(remaining, {
    divisor,
    rawUnit: 'quota',
    basis: 'api_key_quota',
    rawRemaining: decimalText(remaining)
  })
}

export function parseOpenAiCompatibleBillingBalance(
  subscriptionPayload: unknown,
  usagePayload: unknown,
  options: { divisor?: unknown, rawUnit: 'usd' | 'cny' | 'quota' }
): AccountBalanceSnapshot {
  const subscription = objectValue(subscriptionPayload)
  const usage = objectValue(usagePayload)
  if (subscription.object !== 'billing_subscription') throw new Error('账单订阅响应类型不匹配')
  if (usage.object !== 'list') throw new Error('账单用量响应类型不匹配')
  const hardLimit = decimalValue(subscription.hard_limit_usd, 'hard_limit_usd')
  if (decimalText(hardLimit) === '100000000') {
    return {
      status: 'unsupported',
      basis: 'api_key_quota',
      errorMessage: '上游 API Key 为无限额度，无法确认实际可用余额'
    }
  }
  const totalUsage = decimalValue(usage.total_usage, 'total_usage')
  if (totalUsage.coefficient < 0n) throw new Error('total_usage 不能为负数')
  const remaining = decimalSubtract(hardLimit, decimalDivideByHundred(totalUsage))
  return freshResult(remaining, {
    ...(options.divisor === undefined ? {} : { divisor: decimalValue(options.divisor, '金额换算系数') }),
    rawUnit: options.rawUnit,
    basis: 'api_key_quota'
  })
}

export function parseOpenAiCompatibleBillingStatus(payload: unknown): OpenAiCompatibleBillingOptions {
  const response = objectValue(payload)
  if (response.success !== true) throw new Error('上游状态接口未返回成功响应')
  const data = objectValue(response.data)
  const displayType = typeof data.quota_display_type === 'string'
    ? data.quota_display_type.trim().toUpperCase()
    : undefined
  if (displayType === 'USD') return { rawUnit: 'usd' }
  if (displayType === 'CNY') {
    return { rawUnit: 'cny', divisor: decimalText(decimalValue(data.usd_exchange_rate, 'usd_exchange_rate')) }
  }
  if (displayType === 'TOKENS' || displayType === 'CUSTOM') {
    return {
      snapshot: {
        status: 'unsupported',
        basis: 'api_key_quota',
        errorMessage: `上游余额展示单位为 ${displayType}，无法安全换算为美元`
      }
    }
  }
  if (data.display_in_currency === true) return { rawUnit: 'usd' }
  if (data.display_in_currency === false) {
    return { rawUnit: 'quota', divisor: decimalText(decimalValue(data.quota_per_unit, 'quota_per_unit')) }
  }
  throw new Error('上游状态接口未提供可识别的余额单位')
}

export function parseLiteLlmBalance(payload: unknown): AccountBalanceSnapshot {
  const info = objectValue(objectValue(payload).info)
  if (info.max_budget === undefined || info.max_budget === null) return { status: 'unsupported', basis: 'budget' }
  const remaining = decimalSubtract(decimalValue(info.max_budget, 'max_budget'), decimalValue(info.spend ?? 0, 'spend'))
  return freshResult(remaining, { rawUnit: 'usd', basis: 'budget' })
}

export function parseUserBalance(payload: unknown): AccountBalanceSnapshot {
  const response = objectValue(payload)
  return freshResult(decimalValue(response.balance, 'balance'), {
    rawUnit: 'usd',
    basis: 'wallet'
  })
}

function jsonPointerValue(payload: unknown, pointer: string): unknown {
  if (pointer === '') return payload
  return pointer.slice(1).split('/').reduce<unknown>((current, token) => {
    const key = token.replace(/~1/g, '/').replace(/~0/g, '~')
    const record = objectValue(current)
    if (!Object.prototype.hasOwnProperty.call(record, key)) throw new Error(`JSON Pointer 字段不存在：${pointer}`)
    return record[key]
  }, payload)
}

export function parseCustomBalance(payload: unknown, config: AccountBalanceCustomConfig): AccountBalanceSnapshot {
  const remaining = config.remainingPointer
    ? decimalValue(jsonPointerValue(payload, config.remainingPointer), '余额字段')
    : decimalSubtract(
        decimalValue(jsonPointerValue(payload, config.totalPointer ?? ''), '总额字段'),
        decimalValue(jsonPointerValue(payload, config.usedPointer ?? ''), '已用字段')
      )
  return freshResult(remaining, {
    divisor: decimalValue(config.divisor ?? '1', 'divisor'),
    rawUnit: 'usd',
    basis: 'custom'
  })
}
