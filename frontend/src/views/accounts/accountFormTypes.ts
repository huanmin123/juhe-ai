import type { Dayjs } from 'dayjs'

import type {
  AccountClientCompatibility,
  AccountBalanceBuiltinAdapter,
  AccountHealthCheckEndpointMode,
  AccountGptReasoningEffortOverride,
  AccountGptServiceTierOverride,
  AccountModelMapping,
  AccountStatus,
  AccountSupportedEndpointMode,
  AccountType
} from '@/types/domain'
import type { AccountCapabilityDomain } from './accountCapabilityDomains'
import type { GroupSelection } from '@/shared/groupLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type { AccountAvailabilityScheduleForm } from './accountAvailabilitySchedule'
import type { AccountQuotaRecoveryPolicyForm } from './accountQuotaRecoveryPolicyTypes'
import type { AccountErrorPolicyRuleOverrideForm } from './accountErrorPolicyTypes'

export interface AccountFormModel {
  providerCode: string
  providerProtocolProfileId: string
  name: string
  type: AccountType
  groupId?: string
  group?: GroupSelection
  apiKey: string
  apiKeys: string[]
  apiKeyStrategy: 'round_robin' | 'weighted_round_robin' | 'failover'
  apiKeyWeights: number[]
  baseUrl: string
  accessToken: string
  refreshToken: string
  googleClientId: string
  googleClientSecret: string
  googleQuotaProjectId: string
  oauthType: 'code_assist' | 'google_one' | 'ai_studio'
  tierId: string
  projectId: string
  oauthMode: 'manual' | 'refresh_token' | 'access_token' | 'sso_cookie'
  ssoTokens: string
  callbackUrl: string
  accountExpiresAt?: Dayjs | null
  concurrencyLimit: number
  priority: number
  privilege: 'normal' | 'super_priority' | 'fallback'
  status: 'active' | 'pending_test' | 'disabled'
  statusSelectionExplicit: boolean
  clientCompatibility: AccountClientCompatibility
  supportedEndpointModes: AccountSupportedEndpointMode[]
  supportedModels: string[]
  healthCheckModel: string
  healthCheckEndpointMode: AccountHealthCheckEndpointMode
  temporaryUnavailableContinuousProbeEnabled: boolean
  lockDeathTimeoutSeconds: number
  lockRetryIntervalSeconds: number
  serviceTierOverride: AccountGptServiceTierOverride
  reasoningEffortOverride: AccountGptReasoningEffortOverride
  modelMappings: AccountModelMapping[]
  tags: string[]
  proxyProfileId?: string
  availabilitySchedule: AccountAvailabilityScheduleForm
  notes: string
  balanceQueryEnabled: boolean
  balanceQueryAdapter: 'builtin' | 'custom'
  balanceQueryPreferredBuiltinAdapter?: AccountBalanceBuiltinAdapter
  balanceQueryIntervalMinutes: number
  balanceQueryCustomPath: string
  balanceQueryRemainingPointer: string
  balanceQueryTotalPointer: string
  balanceQueryUsedPointer: string
  balanceQueryDivisor: string
  quotaRecoveryPolicy?: AccountQuotaRecoveryPolicyForm
  errorHandlingRuleOverrides?: AccountErrorPolicyRuleOverrideForm[]
  /**
   * 当前能力域上下文（M2 域拆分，D3）：由 AccountEditModal 按路由域注入，
   * 不是可编辑字段；创建 payload 携带该值，编辑 payload 不携带（域不可变）。
   */
  capabilityDomain: AccountCapabilityDomain
  /** 火山方舟语音应用双值凭据（M6 契约 §9.2）：仅 volcengine + audio 域表单渲染并提交。 */
  speechAppId: string
  speechToken: string
}

export type AccountOAuthAuthorizeForm = Pick<
  AccountFormModel,
  | 'oauthMode'
  | 'callbackUrl'
  | 'refreshToken'
  | 'accessToken'
  | 'ssoTokens'
  | 'googleClientId'
  | 'googleClientSecret'
  | 'googleQuotaProjectId'
  | 'oauthType'
  | 'tierId'
  | 'projectId'
  | 'baseUrl'
>

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
