import type { Dayjs } from 'dayjs'

import type {
  AccountClientCompatibility,
  AccountHealthCheckEndpointMode,
  AccountGptReasoningEffortOverride,
  AccountGptServiceTierOverride,
  AccountModelMapping,
  AccountStatus,
  AccountSupportedEndpointMode,
  AccountType
} from '@/types/domain'
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
  accessToken: string
  refreshToken: string
  googleClientId: string
  googleClientSecret: string
  googleQuotaProjectId: string
  oauthMode: 'manual' | 'refresh_token'
  callbackUrl: string
  accountExpiresAt?: Dayjs | null
  concurrencyLimit: number
  priority: number
  privilege: 'normal' | 'super_priority' | 'fallback'
  status: 'active' | 'pending_test' | 'disabled'
  clientCompatibility: AccountClientCompatibility
  codexResponsesSafeRepairEnabled: boolean
  codexResponsesStrictInterceptEnabled: boolean
  supportedEndpointModes: AccountSupportedEndpointMode[]
  supportedModels: string[]
  healthCheckModel: string
  healthCheckEndpointMode: AccountHealthCheckEndpointMode
  temporaryUnavailableContinuousProbeEnabled: boolean
  serviceTierOverride: AccountGptServiceTierOverride
  reasoningEffortOverride: AccountGptReasoningEffortOverride
  modelMappings: AccountModelMapping[]
  tags: string[]
  proxyProfileId?: string
  availabilitySchedule: AccountAvailabilityScheduleForm
  notes: string
  balanceQueryEnabled: boolean
  balanceQueryAdapter: 'builtin' | 'custom'
  balanceQueryPreferredBuiltinAdapter?: 'sub2api' | 'newapi' | 'litellm' | 'user_balance'
  balanceQueryIntervalMinutes: number
  balanceQueryCustomPath: string
  balanceQueryRemainingPointer: string
  balanceQueryTotalPointer: string
  balanceQueryUsedPointer: string
  balanceQueryDivisor: string
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
