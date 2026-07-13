import type { ModelCheckItemSummary } from '../../domain/types.js'

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
}

export function buildModelCheckTrustReport(
  checks: ModelCheckItemSummary[],
  input: { requestedModel: string; probeSetVersion: string; evidenceCoverage: number }
): ModelCheckTrustReport {
  const targetChecks = checks.filter((item) => item.itemKey.startsWith('target.'))
  const evidence = targetChecks.map((item) => item.evidenceSummary)
  const mappingApplied = evidence.some((item) => item.modelMappingApplied === true)
  const modelMismatch = evidence.some((item) => item.modelMismatch === true)
  const representative = evidence.find((item) => item.requestModel || item.upstreamModel || item.responseModel)
  const mappedUpstreamModel = text(representative?.upstreamModel) ?? input.requestedModel
  const observedModel = text(representative?.responseModel)
  const mappingStatus: ModelMappingStatus = mappingApplied
    ? 'configured_mapping'
    : modelMismatch
      ? 'undeclared_mismatch'
      : targetChecks.length
        ? 'direct'
        : 'unknown'
  const protocolChecks = targetChecks.filter((item) => protocolItemTypes.has(item.itemType))
  const protocolStatus: ModelProtocolStatus = protocolChecks.length === 0
    ? 'insufficient_evidence'
    : protocolChecks.some((item) => item.status === 'failed')
      ? 'failed'
      : protocolChecks.some((item) => item.status === 'warning' || item.status === 'skipped')
        ? 'warning'
        : 'consistent'
  const identityStatus: ModelIdentityStatus = modelMismatch
    ? 'suspected_downgrade'
    : targetChecks.length
      ? 'consistent'
      : 'insufficient_evidence'
  const reasonCodes = [
    ...(mappingStatus === 'configured_mapping' ? ['configured_model_mapping'] : []),
    ...(mappingStatus === 'undeclared_mismatch' ? ['undeclared_response_model_mismatch'] : []),
    ...(protocolStatus === 'failed' ? ['protocol_check_failed'] : []),
    'tokenizer_calibration_unavailable',
    'population_baseline_unavailable'
  ]
  return {
    identityStatus,
    mappingStatus,
    usageIntegrityStatus: 'insufficient_evidence',
    protocolStatus,
    evidenceStatus: 'insufficient',
    requestedModel: text(representative?.requestModel) ?? input.requestedModel,
    mappedUpstreamModel,
    observedModel,
    mappingApplied,
    probeSetVersion: input.probeSetVersion,
    evidenceCoverage: boundedPercentage(input.evidenceCoverage),
    reasonCodes
  }
}

const protocolItemTypes = new Set([
  'responses_basic',
  'protocol_basic',
  'responses_stream',
  'protocol_stream',
  'structured_output',
  'tool_calling',
  'usage_shape'
])

function text(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function boundedPercentage(value: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.max(0, Math.min(100, Math.round(value)))
}
