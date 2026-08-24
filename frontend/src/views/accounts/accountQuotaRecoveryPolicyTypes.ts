export type AccountQuotaRecoveryStrategy = 'duration' | 'daily' | 'weekly'

export interface AccountQuotaRecoveryScheduleForm {
  reset_strategy: AccountQuotaRecoveryStrategy
  duration_minutes: number
  daily_reset_hour: number
  weekly_reset_day: number
  weekly_reset_hour: number
  timezone: string
  jitter_minutes: number
}

export interface AccountQuotaRecoveryPolicyForm {
  api_key?: AccountQuotaRecoveryScheduleForm
  oauth?: AccountQuotaRecoveryScheduleForm
  google_oauth?: AccountQuotaRecoveryScheduleForm
}

export function defaultAccountQuotaRecoverySchedule(type: string): AccountQuotaRecoveryScheduleForm {
  return type === 'api_key'
    ? {
        reset_strategy: 'duration',
        duration_minutes: 60,
        daily_reset_hour: 0,
        weekly_reset_day: 0,
        weekly_reset_hour: 0,
        timezone: 'UTC',
        jitter_minutes: 15
      }
    : {
        reset_strategy: 'daily',
        duration_minutes: 60,
        daily_reset_hour: 0,
        weekly_reset_day: 0,
        weekly_reset_hour: 0,
        timezone: 'UTC',
        jitter_minutes: 15
      }
}

export function loadAccountQuotaRecoveryPolicy(value: unknown): AccountQuotaRecoveryPolicyForm {
  const input = value && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {}
  const output: AccountQuotaRecoveryPolicyForm = {}
  for (const key of ['api_key', 'oauth', 'google_oauth'] as const) {
    const source = input[key]
    if (!source || typeof source !== 'object' || Array.isArray(source)) continue
    const item = source as Record<string, unknown>
    const base = defaultAccountQuotaRecoverySchedule(key)
    const strategy = item.reset_strategy === 'duration' || item.reset_strategy === 'daily' || item.reset_strategy === 'weekly'
      ? item.reset_strategy
      : base.reset_strategy
    output[key] = {
      ...base,
      reset_strategy: strategy,
      duration_minutes: integerOr(item.duration_minutes, base.duration_minutes),
      daily_reset_hour: integerOr(item.daily_reset_hour, base.daily_reset_hour),
      weekly_reset_day: integerOr(item.weekly_reset_day, base.weekly_reset_day),
      weekly_reset_hour: integerOr(item.weekly_reset_hour, base.weekly_reset_hour),
      timezone: typeof item.timezone === 'string' && item.timezone.trim() ? item.timezone.trim() : base.timezone,
      jitter_minutes: 15
    }
  }
  return output
}

export function ensureAccountQuotaRecoverySchedule(
  policy: AccountQuotaRecoveryPolicyForm | undefined,
  type: string
): AccountQuotaRecoveryScheduleForm {
  const key = type === 'api_key' ? 'api_key' : type === 'google_oauth' ? 'google_oauth' : 'oauth'
  const next = policy ?? {}
  const current = next[key]
  if (current) return current
  const created = defaultAccountQuotaRecoverySchedule(type)
  next[key] = created
  return created
}

function integerOr(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isInteger(value) ? value : fallback
}
