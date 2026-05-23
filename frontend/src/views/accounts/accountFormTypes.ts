import type { Dayjs } from 'dayjs'

import type { AccountStatus, AccountType } from '@/types/domain'
import type { GroupSelection } from '@/shared/groupLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'

export interface AccountFormModel {
  providerCode: string
  name: string
  type: AccountType
  groupId?: string
  group?: GroupSelection
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
  providerCode: string
  type: string
  groupId: string
  group?: GroupSelection
  status: AccountStatus[]
  systemAccountId: string
  systemAccount?: PrincipalSelection
}
