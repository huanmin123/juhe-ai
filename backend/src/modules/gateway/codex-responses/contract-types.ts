export type CodexContractRevision = 'codex-responses-2026-07-11-r1'

export type CodexResponseItemEventStage = 'added' | 'delta' | 'done'

export type CodexRequiredFieldKind = 'present' | 'string' | 'non_empty_string' | 'array' | 'object'

export interface CodexRequiredItemField {
  name: string
  kind: CodexRequiredFieldKind
}

export type CodexProtocolIssueProvenance =
  | 'request_history'
  | 'raw_upstream'
  | 'gateway_bridge'
  | 'unknown'

export type CodexContractOutcome =
  | 'clean'
  | 'repairable'
  | 'blocked'
  | 'observed_unknown'
  | 'late_violation'

export type CodexRepairLevel = 'R0' | 'R1' | 'R2'

export interface CodexItemContract {
  type: string
  prefix?: string
  eventStages: readonly CodexResponseItemEventStage[]
  repairableIdPaths: readonly string[]
  requiredFields: readonly CodexRequiredItemField[]
}

export interface CodexProtocolIssue {
  code: string
  message: string
  path: readonly (string | number)[]
  provenance: CodexProtocolIssueProvenance
  itemType?: string
  outputIndex?: number
  repairLevel?: CodexRepairLevel
}

export interface CodexContractValidationResult {
  revision: CodexContractRevision
  outcome: CodexContractOutcome
  issues: readonly CodexProtocolIssue[]
}

export interface CodexRepairOperation {
  action: 'remove' | 'replace'
  path: readonly (string | number)[]
  value?: unknown
  issueCode: string
  ruleId: string
}

export interface CodexRepairPlan {
  revision: CodexContractRevision
  level: CodexRepairLevel
  provenance: CodexProtocolIssueProvenance
  sourceOutcome: CodexContractOutcome
  operations: readonly CodexRepairOperation[]
  forbiddenReason?: string
}

export interface CodexResponsesContractRegistry {
  revision: CodexContractRevision
  items: readonly CodexItemContract[]
  item(type: string): CodexItemContract | undefined
  itemByPrefix(prefix: string): CodexItemContract | undefined
}
