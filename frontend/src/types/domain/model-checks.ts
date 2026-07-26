export type ModelCheckTargetType = 'account'
export type ModelCheckModel = string
export type ModelCheckProfile = 'quick' | 'full'
export type ModelCheckTriggerKind = 'manual' | 'scheduled' | 'quality_recovery'
export type ModelQualityPenaltyAction = 'disable' | 'fallback' | 'quality_isolate'
export type ModelQualityEnforcementResult = 'not_triggered' | 'applied' | 'already_effective' | 'skipped' | 'stale' | 'pending_retry' | 'failed'
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

export interface ModelCheckAccountOption {
  id: string
  name: string
  providerCode: string
  providerProtocolProfileId: string
  protocolCode?: string
  protocolVersion?: string
}

export interface ModelCheckRunPayload {
  targetType: ModelCheckTargetType
  targetId: string
  model: ModelCheckModel
  profile?: ModelCheckProfile
  trustedComparison?: boolean
  trustedComparisonAccountId?: string
}

export interface ModelQualityPolicy {
  systemAccountId: string
  revision: number
  profile: ModelCheckProfile
  manualEnforcementEnabled: boolean
  penaltyThreshold: number
  penaltyAction: ModelQualityPenaltyAction
  recoveryIntervalMinutes: number
  createdAt?: string
  updatedAt?: string
}

export interface ModelQualityPolicyUpdateInput {
  expectedRevision: number
  profile: ModelCheckProfile
  manualEnforcementEnabled: boolean
  penaltyThreshold: number
  penaltyAction: ModelQualityPenaltyAction
  recoveryIntervalMinutes: number
}

export interface ModelQualitySchedule {
  id: string
  systemAccountId: string
  accountId: string
  accountName?: string
  providerCode?: string
  model: string
  intervalMinutes: number
  enabled: boolean
  revision: number
  nextRunAt: string
  lastRunId?: string
  lastRunAt?: string
  lastRunStatus?: Exclude<ModelCheckStatus, 'running'>
  currentEnforcementAction?: ModelQualityPenaltyAction
  currentEnforcementRecoveryDueAt?: string
  createdAt: string
  updatedAt: string
}

export interface ModelQualityScheduleListResult {
  items: ModelQualitySchedule[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface ModelQualityPolicySnapshot {
  policyRevision: number
  profile: ModelCheckProfile
  manualEnforcementEnabled: boolean
  threshold: number
  action: ModelQualityPenaltyAction
  recoveryIntervalMinutes: number
  scheduleId?: string
  accountConfigRevision: number
}

export interface ModelQualityDecision {
  triggerKind: ModelCheckTriggerKind
  triggered: boolean
  hardFailure: boolean
  threshold: number
  score: number
  configuredAction: ModelQualityPenaltyAction
  result: ModelQualityEnforcementResult
  reasonCodes: string[]
  beforeStatus?: import('./base').AccountStatus
  afterStatus?: import('./base').AccountStatus
  recoveryDueAt?: string
  enforcementId?: string
  generation?: number
  healthSyncResult?: 'applied' | 'pending_retry' | 'failed'
  healthStatHour?: string
  message: string
  decidedAt: string
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
  triggerKind?: ModelCheckTriggerKind
  startAt?: string
  endAt?: string
}

export interface ModelQualityScheduleMutationInput {
  accountId: string
  model: string
  intervalMinutes: number
  enabled?: boolean
  expectedRevision?: number
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
  triggerKind: ModelCheckTriggerKind
  scheduleId?: string
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
  policySnapshot?: ModelQualityPolicySnapshot
  qualityDecision?: ModelQualityDecision
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
  profile?: ModelCheckProfile
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
  profile: ModelCheckProfile
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
  type: 'quality_decision'
  triggered: boolean
  score: number
  threshold: number
  hardFailure: boolean
  configuredAction: ModelQualityPenaltyAction
  message: string
} | {
  type: 'quality_enforcement_started'
  action: ModelQualityPenaltyAction
  message: string
} | {
  type: 'quality_enforcement_completed'
  action: ModelQualityPenaltyAction
  result: ModelQualityEnforcementResult
  beforeStatus?: import('./base').AccountStatus
  afterStatus?: import('./base').AccountStatus
  recoveryDueAt?: string
  message: string
} | {
  type: 'quality_health_sync'
  result: 'applied' | 'pending_retry' | 'failed'
  statHour: string
  message: string
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
