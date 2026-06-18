import type { Dayjs } from 'dayjs'

import type { AccountClientCompatibility, AccountModelMapping, AccountStatus, AccountSupportedEndpointMode, AccountType } from '@/types/domain'
import type { GroupSelection } from '@/shared/groupLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type { AccountAvailabilityScheduleForm } from './accountAvailabilitySchedule'

export interface AccountFormModel {
  providerCode: string
  providerProtocolProfileId: string
  name: string
  type: AccountType
  groupId?: string
  group?: GroupSelection
  apiKey: string
  apiKeys: string[]
  apiKeyStrategy: 'round_robin' | 'weighted_round_robin'
  apiKeyWeights: number[]
  baseUrl: string
  anthropicVersion: string
  anthropicBeta: string
  accessToken: string
  refreshToken: string
  oauthMode: 'manual' | 'refresh_token'
  callbackUrl: string
  accountExpiresAt?: Dayjs | null
  concurrencyLimit: number
  priority: number
  clientCompatibility: AccountClientCompatibility
  supportedEndpointModes: AccountSupportedEndpointMode[]
  supportedModels: string[]
  modelMappings: AccountModelMapping[]
  tags: string[]
  proxyProfileId?: string
  availabilitySchedule: AccountAvailabilityScheduleForm
  notes: string
}

export type AccountOAuthAuthorizeForm = Pick<AccountFormModel, 'oauthMode' | 'callbackUrl' | 'refreshToken'>

export interface AccountFilters {
  keyword: string
  providerCode: string
  type: string
  groupId: string
  group?: GroupSelection
  tagIds: string[]
  status: AccountStatus[]
  systemAccountId: string
  systemAccount?: PrincipalSelection
}
