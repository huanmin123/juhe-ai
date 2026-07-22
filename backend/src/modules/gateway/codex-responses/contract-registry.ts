import type {
  CodexContractRevision,
  CodexItemContract,
  CodexResponsesContractRegistry
} from './contract-types.js'

export const codexResponsesContractRevision: CodexContractRevision = 'codex-responses-2026-07-11-r1'

const fullLifecycle = ['added', 'delta', 'done'] as const
const itemLifecycle = ['added', 'done'] as const
const itemIdPath = ['id'] as const

export const codexResponsesContractRegistry = createCodexResponsesContractRegistry(
  codexResponsesContractRevision,
  [
    item('additional_tools', 'at', itemLifecycle, [field('role', 'non_empty_string'), field('tools', 'array')]),
    item('message', 'msg', fullLifecycle, [field('role', 'non_empty_string'), field('content', 'array')]),
    item('agent_message', 'amsg', fullLifecycle, [field('author', 'non_empty_string'), field('recipient', 'non_empty_string'), field('content', 'array')]),
    item('reasoning', 'rs', fullLifecycle, [field('summary', 'array')]),
    item('local_shell_call', 'lsh', itemLifecycle, [field('status', 'non_empty_string'), field('action', 'object')]),
    item('function_call', 'fc', fullLifecycle, [field('name', 'non_empty_string'), field('arguments', 'string'), field('call_id', 'non_empty_string')]),
    item('tool_search_call', 'tsc', itemLifecycle, [field('execution', 'string'), field('arguments', 'present')]),
    item('function_call_output', 'fco', itemLifecycle, [field('call_id', 'non_empty_string'), field('output', 'present')]),
    item('custom_tool_call', 'ctc', fullLifecycle, [field('call_id', 'non_empty_string'), field('name', 'non_empty_string'), field('input', 'string')]),
    item('custom_tool_call_output', 'ctco', itemLifecycle, [field('call_id', 'non_empty_string'), field('output', 'present')]),
    item('tool_search_output', 'tso', itemLifecycle, [field('status', 'non_empty_string'), field('execution', 'string'), field('tools', 'array')]),
    item('web_search_call', 'ws', itemLifecycle),
    item('image_generation_call', 'ig', itemLifecycle, [field('status', 'non_empty_string'), field('result', 'string')]),
    item('compaction', 'cmp', itemLifecycle, [field('encrypted_content', 'string')]),
    item('compaction_summary', 'cmp', itemLifecycle, [field('encrypted_content', 'string')]),
    item('context_compaction', 'cmp', itemLifecycle)
  ]
)

export function createCodexResponsesContractRegistry(
  revision: CodexContractRevision,
  definitions: readonly CodexItemContract[]
): CodexResponsesContractRegistry {
  const items = Object.freeze(definitions.map(freezeItemContract))
  const byType = new Map(items.map((definition) => [definition.type, definition]))
  const byPrefix = new Map<string, CodexItemContract>()
  for (const definition of items) {
    if (definition.prefix && !byPrefix.has(definition.prefix)) {
      byPrefix.set(definition.prefix, definition)
    }
  }
  return Object.freeze({
    revision,
    items,
    item: (type: string) => byType.get(type),
    itemByPrefix: (prefix: string) => byPrefix.get(prefix)
  })
}

function item(
  type: string,
  prefix: string,
  eventStages: readonly ('added' | 'delta' | 'done')[],
  requiredFields: CodexItemContract['requiredFields'] = []
): CodexItemContract {
  return { type, prefix, eventStages, repairableIdPaths: itemIdPath, requiredFields }
}

function field(name: string, kind: CodexItemContract['requiredFields'][number]['kind']): CodexItemContract['requiredFields'][number] {
  return { name, kind }
}

function freezeItemContract(definition: CodexItemContract): CodexItemContract {
  return Object.freeze({
    ...definition,
    eventStages: Object.freeze([...definition.eventStages]),
    repairableIdPaths: Object.freeze([...definition.repairableIdPaths]),
    requiredFields: Object.freeze(definition.requiredFields.map((value) => Object.freeze({ ...value })))
  })
}
