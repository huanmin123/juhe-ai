import type { SystemAccountRole, SystemAccountStatus } from './base'

export interface CurrentUserSummary {
  id: string
  username: string
  displayName: string
  role: SystemAccountRole
  mustChangePassword: boolean
}

export interface CaptchaChallengeSummary {
  required: boolean
  captchaId?: string
  image?: string
  expiresAt?: string
}

export interface SystemAccountSummary {
  id: string
  username: string
  displayName: string
  description?: string
  role: SystemAccountRole
  status: SystemAccountStatus
  mustChangePassword: boolean
  imageGenerationEnabled: boolean
  aiAccountLimit?: number
  /** 特供账户上限覆盖；null/缺省表示未覆盖（默认 3），0 = 禁止新增/重新启用。 */
  expeditedAccountLimit?: number | null
  requestLimits?: UserRequestLimits
  lastLoginAt?: string
  createdAt: string
  updatedAt: string
}

export interface UserRequestLimits {
  perMinute?: number
  perDay?: number
  perWeek?: number
  perMonth?: number
  expiresOn?: string
}

export interface EffectiveUserRequestLimitValue {
  limit: number
  source: 'global' | 'user'
}

export interface EffectiveUserRequestLimits {
  perMinute: EffectiveUserRequestLimitValue
  perDay: EffectiveUserRequestLimitValue
  perWeek: EffectiveUserRequestLimitValue
  perMonth: EffectiveUserRequestLimitValue
  timezone: string
  overrideExpiresOn?: string
  overrideActive: boolean
}

export interface CurrentUserProfile extends SystemAccountSummary {
  effectiveRequestLimits: EffectiveUserRequestLimits
}

export type SystemAccountListItem = Omit<SystemAccountSummary, 'createdAt' | 'updatedAt'> & {
  editVersion: string
}

export type SystemAccountPatchPayload = {
  expectedUpdatedAt: string
} & Partial<Pick<SystemAccountSummary,
  'displayName'
  | 'role'
  | 'status'
  | 'mustChangePassword'
  | 'imageGenerationEnabled'
  | 'aiAccountLimit'
>> & {
  password?: string
  description?: string | null
  aiAccountLimit?: number | null
  /** 三态：键缺省=不修改；显式 null=清除覆盖；整数=设置 0–100。 */
  expeditedAccountLimit?: number | null
  requestLimits?: UserRequestLimits | null
}

export type SystemAccountMutationResult = {
  id: string
  updatedAt: string
  apiKeyValidationCacheInvalidationFailed?: boolean
} & Partial<Pick<SystemAccountSummary,
  'displayName'
  | 'role'
  | 'status'
  | 'mustChangePassword'
  | 'imageGenerationEnabled'
  | 'aiAccountLimit'
>> & {
  description?: string | null
  aiAccountLimit?: number | null
  expeditedAccountLimit?: number | null
  requestLimits?: UserRequestLimits | null
}

export interface SystemAccountOptionSummary {
  id: string
  name: string
  disabledReason?: 'account_disabled'
}

export interface SystemAccountListResult {
  items: SystemAccountListItem[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export type SystemAccountPrincipalSummary = Pick<SystemAccountSummary, 'id' | 'username' | 'displayName' | 'status'>
