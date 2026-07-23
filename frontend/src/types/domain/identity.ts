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
  lastLoginAt?: string
  createdAt: string
  updatedAt: string
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
