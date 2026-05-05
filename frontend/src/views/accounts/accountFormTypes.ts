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
  status: AccountStatus
  concurrencyLimit: number
  priority: number
  proxyProfileId?: string
  notes: string
}
