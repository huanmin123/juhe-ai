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

export type SystemAccountListItem = Omit<SystemAccountSummary, 'createdAt' | 'updatedAt'>

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
