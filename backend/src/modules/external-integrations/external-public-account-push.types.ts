import type { AccountClientCompatibility, AccountStatus, AccountSummary, ApiKeySummary } from '../../domain/types.js'

export interface PublicAccountPushInput {
  targetUsername: string
  targetDisplayName?: string
  targetGroupName: string
  providerCode: string
  providerProtocolProfileId?: string
  connectionType?: string
  name: string
  type: 'api_key'
  baseUrl: string
  apiKey: string
  supportedModels?: string[]
  status?: 'active' | 'disabled'
  concurrencyLimit?: number
  priority?: number
  availabilitySchedule?: Record<string, unknown> | null
  notes?: string
}

export interface PublicAccountUpdateInput {
  accountId: string
  targetUsername?: string
  targetDisplayName?: string
  targetGroupName?: string
  providerCode?: string
  providerProtocolProfileId?: string
  connectionType?: string
  name?: string
  type?: 'api_key'
  baseUrl?: string
  apiKey?: string
  supportedModels?: string[]
  status?: 'active' | 'disabled'
  concurrencyLimit?: number
  priority?: number
  availabilitySchedule?: Record<string, unknown> | null
  notes?: string
}

export interface PublicAccountPushResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  action: 'created' | 'updated' | 'mock'
  target: {
    username: string
    displayName: string
    systemAccountId: string
    created: boolean
    groupId: string
    groupName: string
    groupCreated: boolean
  }
  account: {
    id: string
    name: string
    providerCode: string
    providerProtocolProfileId?: string
    connectionType?: string
    protocolCode?: string
    protocolVersion?: string
    type: string
    clientCompatibility: AccountClientCompatibility
    status: AccountStatus
    supportedModels?: string[]
    boundGroupId?: string
    boundGroupName?: string
    schedulable: boolean
    availabilitySchedule?: AccountSummary['availabilitySchedule']
  }
}

export interface PublicAccountDeleteInput {
  accountId: string
  targetUsername?: string
  targetGroupName?: string
  providerCode?: string
  providerProtocolProfileId?: string
  connectionType?: string
}

export interface PublicAccountDeleteResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  action: 'deleted' | 'not_found' | 'mock'
  target: PublicAccountPushResponse['target']
  account: PublicAccountPushResponse['account'] | null
}

export interface PublicAccountListInput {
  targetUsername: string
  targetGroupName?: string
  providerCode?: string
  providerProtocolProfileId?: string
  connectionType?: string
  groupId?: string
  keyword?: string
  type?: string
  status?: string
  schedulable?: 'all' | 'enabled' | 'disabled' | 'cooling'
  page?: number
  pageSize?: number
}

export type PublicAccountListItem = PublicAccountPushResponse['account'] & {
  concurrencyLimit: number
  priority: number
}

export interface PublicAccountListResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  target: Omit<PublicAccountPushResponse['target'], 'groupId' | 'groupName' | 'groupCreated'>
  page: number
  pageSize: number
  pageUpperBound: number
  hasMore: boolean
  items: PublicAccountListItem[]
}

export interface PublicGroupAddInput {
  targetUsername: string
  targetDisplayName?: string
  name: string
  providerCode: string
  providerProtocolProfileId?: string
  connectionType?: string
  description?: string
  enabled?: boolean
  groupType?: 'personal' | 'high_concurrency'
}

export interface PublicGroupUpdateInput {
  targetUsername?: string
  groupId: string
  name?: string
  providerCode?: string
  providerProtocolProfileId?: string
  connectionType?: string
  description?: string | null
  enabled?: boolean
  groupType?: 'personal' | 'high_concurrency'
}

export interface PublicGroupDeleteInput {
  targetUsername?: string
  groupId: string
}

export interface PublicGroupResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  action: 'created' | 'existing' | 'updated' | 'deleted' | 'not_found' | 'mock'
  target: Omit<PublicAccountPushResponse['target'], 'groupId' | 'groupName' | 'groupCreated'>
  group: PublicGroupSummary | null
}

export interface PublicGroupListInput {
  targetUsername: string
  keyword?: string
  providerCode?: string
  providerProtocolProfileId?: string
  connectionType?: string
  page?: number
  pageSize?: number
}

export interface PublicGroupListResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  target: PublicGroupResponse['target']
  page: number
  pageSize: number
  pageUpperBound: number
  hasMore: boolean
  items: PublicGroupSummary[]
}

export interface PublicApiKeyAddInput {
  targetUsername: string
  name: string
  description?: string | null
  groupBindings?: Array<{ groupId: string; priority?: number; weight?: number; status?: 'active' | 'disabled' }>
  groupRouteStrategy?: string
  status?: 'active' | 'disabled'
  expiresAt?: string
  quotaLimits?: Record<string, unknown> | null
  availabilitySchedule?: Record<string, unknown> | null
  availabilityScheduleActive?: boolean
}

export interface PublicApiKeyUpdateInput {
  targetUsername?: string
  apiKeyId: string
  name?: string
  description?: string | null
  groupBindings?: Array<{ groupId: string; priority?: number; weight?: number; status?: 'active' | 'disabled' }>
  groupRouteStrategy?: string
  status?: 'active' | 'disabled'
  expiresAt?: string | null
  quotaLimits?: Record<string, unknown> | null
  availabilitySchedule?: Record<string, unknown> | null
  availabilityScheduleActive?: boolean
}

export interface PublicApiKeyDeleteInput {
  targetUsername?: string
  apiKeyId: string
}

export interface PublicApiKeyResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  action: 'created' | 'updated' | 'deleted' | 'not_found' | 'mock'
  target: Omit<PublicAccountPushResponse['target'], 'groupId' | 'groupName' | 'groupCreated'>
  apiKey: PublicApiKeySummary | null
}

export interface PublicApiKeyListInput {
  targetUsername: string
  keyword?: string
  status?: 'active' | 'disabled' | 'all'
  groupId?: string
  page?: number
  pageSize?: number
}

export interface PublicApiKeyListResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  target: PublicApiKeyResponse['target']
  page: number
  pageSize: number
  pageUpperBound: number
  hasMore: boolean
  items: PublicApiKeySummary[]
}

export interface PublicGroupSummary {
  id: string
  name: string
  providerCode: string
  providerProtocolProfileId?: string
  connectionType?: string
  protocolCode?: string
  protocolVersion?: string
  description?: string
  enabled: boolean
  groupType: string
  isDefault: boolean
}

export interface PublicApiKeySummary {
  id: string
  name: string
  keyPrefix: string
  key?: string
  status: 'active' | 'disabled'
  groupRouteStrategy: string
  groupBindings: ApiKeySummary['groupBindings']
  expiresAt?: string
  availabilitySchedule?: ApiKeySummary['availabilitySchedule']
  availabilityScheduleActive?: ApiKeySummary['availabilityScheduleActive']
}
