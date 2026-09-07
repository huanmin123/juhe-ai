export type CodexContractRevision = 'codex-responses-2026-07-11-r1'

export interface CodexItemContract {
  type: string
  prefix?: string
}

export interface CodexResponsesContractRegistry {
  revision: CodexContractRevision
  items: readonly CodexItemContract[]
  item(type: string): CodexItemContract | undefined
}
