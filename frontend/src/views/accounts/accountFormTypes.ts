import type { Dayjs } from 'dayjs'

import type { AccountStatus, AccountType } from '@/types/domain'

export interface AccountFormModel {
  providerCode: string
  name: string
  type: AccountType
  groupId?: string
  apiKey: string
  baseUrl: string
  accessToken: string
  refreshToken: string
  oauthMode: 'manual' | 'refresh_token'
  callbackUrl: string
  accountExpiresAt?: Dayjs | null
  concurrencyLimit: number
  priority: number
  supportedModels: string[]
  proxyProfileId?: string
  notes: string
}

export type AccountOAuthAuthorizeForm = Pick<AccountFormModel, 'oauthMode' | 'callbackUrl' | 'refreshToken'>

export interface AccountFilters {
  keyword: string
  groupId: string
  type: 'all' | AccountType
  status: AccountStatus[]
  systemAccountId: string
}
