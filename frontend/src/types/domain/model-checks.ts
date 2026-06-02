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

export interface ModelCheckTrustedComparisonOptions {
  enabledByDefault?: boolean
  available: boolean
  unavailableReason?: string
  message?: string
}

export interface ModelCheckOptions {
  supportedModels: ModelCheckOption[]
  supportedProfiles: ModelCheckOption[]
  trustedComparison: ModelCheckTrustedComparisonOptions
  defaultModel: ModelCheckModel
  defaultProfile: ModelCheckProfile
}

export interface ModelCheckRunPayload {
  targetType: ModelCheckTargetType
  targetId: string
  model: ModelCheckModel
  profile?: ModelCheckProfile
  trustedComparison?: boolean
  trustedComparisonAccountId?: string
}

export interface ModelCheckRunListParams {
  systemAccountId?: string
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
  trustedComparison: boolean
  trustedComparisonAvailable: boolean
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

export type ModelCheckProgressEvent = {
  type: 'run_started'
  message: string
  targetId: string
  targetName?: string
  model: string
  trustedComparison: boolean
  trustedComparisonAccountId?: string
  trustedComparisonAccountName?: string
} | {
  type: 'run_created'
  message: string
  runId: string
  traceId: string
  startedAt: string
} | {
  type: 'probe_started'
  message: string
  itemKey: string
  method: 'GET' | 'POST'
  path: string
} | {
  type: 'probe_completed'
  message: string
  itemKey: string
  traceId: string
  statusCode: number
  success: boolean
  durationMs: number
  responseModel?: string
  outputPreview?: string
} | {
  type: 'item_completed'
  message: string
  itemKey: string
  itemType: string
  status: ModelCheckItemStatus
  score: number
  maxScore: number
  traceId?: string
  durationMs?: number
} | {
  type: 'run_completed'
  message: string
  runId: string
  status: ModelCheckStatus
  level: ModelCheckLevel
  score: number
  maxScore: number
  durationMs?: number
}
