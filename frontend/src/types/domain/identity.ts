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

export interface AuthSessionSummary {
  id: string
  current: boolean
  createdAt: string
  lastSeenAt: string
  expiresAt: string
}

export interface AuthSessionListResult {
  items: AuthSessionSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface AuthSessionRevokeResult {
  id: string
  revoked: boolean
  current: boolean
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

export interface SystemAccountListResult {
  items: SystemAccountSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export type SystemAccountPrincipalSummary = Pick<SystemAccountSummary, 'id' | 'username' | 'displayName' | 'status'>
