export type CodexContractRevision = 'codex-responses-2026-07-11-r1'

export type CodexResponseItemEventStage = 'added' | 'delta' | 'done'

export type CodexRequiredFieldKind =
  | 'present'
  | 'string'
  | 'array'
  | 'object'
  | 'enum'
  | 'function_output'
  | 'local_shell_action'

export interface CodexRequiredItemField {
  name: string
  kind: CodexRequiredFieldKind
  nullable?: boolean
  values?: readonly string[]
}

export interface CodexItemContract {
  type: string
  prefix?: string
  eventStages: readonly CodexResponseItemEventStage[]
  repairableIdPaths: readonly string[]
  requiredFields: readonly CodexRequiredItemField[]
  optionalFields: readonly CodexRequiredItemField[]
}

export interface CodexResponsesContractRegistry {
  revision: CodexContractRevision
  items: readonly CodexItemContract[]
  item(type: string): CodexItemContract | undefined
  itemByPrefix(prefix: string): CodexItemContract | undefined
}
