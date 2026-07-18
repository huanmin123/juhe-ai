export type ModelCheckTargetType = 'account'
export type ModelCheckModel = string
export type ModelCheckProfile = 'full'
export type ModelCheckLevel = 'high_confidence' | 'likely' | 'uncertain' | 'suspicious' | 'unavailable'
export type ModelCheckStatus = 'running' | 'completed' | 'failed' | 'canceled'
export type ModelCheckItemStatus = 'passed' | 'warning' | 'failed' | 'skipped'
export type ModelIdentityStatus = 'consistent' | 'suspected_downgrade' | 'suspected_same_source' | 'population_outlier' | 'insufficient_evidence'
export type ModelMappingStatus = 'direct' | 'configured_mapping' | 'undeclared_mismatch' | 'unknown'
export type UsageIntegrityStatus = 'consistent' | 'warning' | 'suspected_padding' | 'unsupported' | 'insufficient_evidence'
export type ModelProtocolStatus = 'consistent' | 'warning' | 'failed' | 'insufficient_evidence'
export type ModelEvidenceStatus = 'stable' | 'candidate' | 'bootstrap' | 'insufficient'

export interface ModelCheckTrustReport {
  identityStatus: ModelIdentityStatus
  mappingStatus: ModelMappingStatus
  usageIntegrityStatus: UsageIntegrityStatus
  protocolStatus: ModelProtocolStatus
  evidenceStatus: ModelEvidenceStatus
  requestedModel?: string
  mappedUpstreamModel?: string
  observedModel?: string
  mappingApplied: boolean
  probeSetVersion: string
  evidenceCoverage: number
  reasonCodes: string[]
  observationCount?: number
  roundCount?: number
  independentSourceCount?: number
  identityObservationCount?: number
  pairedProbeCount?: number
  slope?: number
  intercept?: number
  interceptBaselineMedian?: number
  interceptBaselineMad?: number
  interceptBaselineVersion?: number
  interceptBaselineStatus?: 'unavailable' | 'calibration_pending' | 'active'
  interceptStrongGateEnabled?: boolean
  identityDistance?: number
  pairedDistance?: number
  pairedBaselineMedian?: number
  pairedBaselineMad?: number
  baselineVersion?: number
  baselineVersionStatus?: 'active' | 'drift_protected' | 'retired'
  featureVersion?: string
  tokenizerVersion?: string
  lastObservedAt?: string
}

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
  startAt?: string
  endAt?: string
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
  requestSummary?: Record<string, unknown>
  resultSummary?: Record<string, unknown>
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
  requestSummary: Record<string, unknown>
  resultSummary: Record<string, unknown>
  checks: ModelCheckCheckResult[]
}

export interface ModelCheckRunListResult {
  items: ModelCheckRunSummary[]
  page: number
  pageSize: number
  total: number
  hasMore: boolean
}

export interface ActiveModelCheckRunSummary {
  runId?: string
  traceId?: string
  targetId?: string
  targetName?: string
  model?: string
  startedAt: string
  stopRequested: boolean
}

export interface ModelCheckStopResult {
  stopped: boolean
  active: ActiveModelCheckRunSummary | null
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
  requestModel?: string
  expectedModel?: string
  upstreamModel?: string
  modelMappingApplied?: boolean
  modelMappingSource?: string
  sourceEndpointFamily?: string
  upstreamEndpointFamily?: string
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
