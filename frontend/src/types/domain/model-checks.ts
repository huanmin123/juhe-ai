export type ModelCheckTargetType = 'account'
export type ModelCheckModel = 'gpt-5.5' | 'gpt-5.4'
export type ModelCheckProfile = 'full'
export type ModelCheckLevel = 'high_confidence' | 'likely' | 'uncertain' | 'suspicious' | 'unavailable'
export type ModelCheckStatus = 'running' | 'completed' | 'failed' | 'canceled'
export type ModelCheckItemStatus = 'passed' | 'warning' | 'failed' | 'skipped'

export interface ModelCheckOption {
  value: string
  label: string
  description?: string
}

export interface ModelCheckOfficialBaselineOptions {
  enabledByDefault?: boolean
  available: boolean
  unavailableReason?: string
  message?: string
}

export interface ModelCheckOptions {
  supportedModels: ModelCheckOption[]
  supportedProfiles: ModelCheckOption[]
  officialBaseline: ModelCheckOfficialBaselineOptions
  defaultModel: ModelCheckModel
  defaultProfile: ModelCheckProfile
}

export interface ModelCheckRunPayload {
  targetType: ModelCheckTargetType
  targetId: string
  model: ModelCheckModel
  profile?: ModelCheckProfile
  officialBaseline?: boolean
}

export interface ModelCheckRunListParams {
  page?: number
  pageSize?: number
  targetType?: ModelCheckTargetType
  targetId?: string
  model?: ModelCheckModel
  level?: ModelCheckLevel
  status?: ModelCheckStatus
}

export interface ModelCheckRunSummary {
  id: string
  systemAccountId?: string
  actorSystemAccountId?: string
  providerCode: string
  targetType: ModelCheckTargetType
  targetId: string
  targetName?: string
  targetOwnerSystemAccountId?: string
  accountId?: string
  groupId?: string
  apiKeyId?: string
  model: ModelCheckModel
  profile: ModelCheckProfile
  officialBaseline: boolean
  officialBaselineAvailable: boolean
  level: ModelCheckLevel
  score: number
  maxScore: number
  status: ModelCheckStatus
  message: string
  traceId?: string
  probeSetVersion: string
  startedAt: string
  finishedAt?: string
  durationMs?: number
  requestSummary: Record<string, unknown>
  resultSummary: Record<string, unknown>
  errorCode?: string
  errorMessage?: string
  createdAt: string
  updatedAt: string
}

export interface ModelCheckCheckResult {
  id: string
  runId: string
  itemKey: string
  itemType: string
  status: ModelCheckItemStatus
  score: number
  maxScore: number
  durationMs?: number
  traceId?: string
  evidenceSummary: Record<string, unknown>
  errorCode?: string
  errorMessage?: string
  createdAt: string
  updatedAt: string
}

export interface ModelCheckRunDetail extends ModelCheckRunSummary {
  checks: ModelCheckCheckResult[]
}

export interface ModelCheckRunListResult {
  items: ModelCheckRunSummary[]
  page: number
  pageSize: number
  total: number
  hasMore: boolean
}
