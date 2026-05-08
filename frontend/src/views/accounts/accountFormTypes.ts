import type { Dayjs } from 'dayjs'

import type { AccountStatus, AccountType } from '@/types/domain'
import type { SchedulableFilter } from './accountFormatters'

export interface AccountFormModel {
  providerCode: string
  name: string
  type: AccountType
  groupId?: string
  apiKey: string
  baseUrl: string
  openaiOrganization: string
  openaiProject: string
  openaiBeta: string
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

export type AccountOAuthAuthorizeForm = Pick<AccountFormModel, 'oauthMode' | 'callbackUrl' | 'refreshToken'>

export interface AccountFilters {
  keyword: string
  type: 'all' | AccountType
  status: 'all' | AccountStatus
  schedulable: SchedulableFilter
  systemAccountId: string
}
