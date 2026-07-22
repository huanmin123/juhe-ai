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
    item('additional_tools', 'at', itemLifecycle),
    item('message', 'msg', fullLifecycle),
    item('agent_message', 'amsg', fullLifecycle),
    item('reasoning', 'rs', fullLifecycle),
    item('local_shell_call', 'lsh', itemLifecycle),
    item('function_call', 'fc', fullLifecycle),
    item('tool_search_call', 'tsc', itemLifecycle),
    item('function_call_output', 'fco', itemLifecycle),
    item('custom_tool_call', 'ctc', fullLifecycle),
    item('custom_tool_call_output', 'ctco', itemLifecycle),
    item('tool_search_output', 'tso', itemLifecycle),
    item('web_search_call', 'ws', itemLifecycle),
    item('image_generation_call', 'ig', itemLifecycle),
    item('compaction', 'cmp', itemLifecycle),
    item('compaction_summary', 'cmp', itemLifecycle),
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
  eventStages: readonly ('added' | 'delta' | 'done')[]
): CodexItemContract {
  return { type, prefix, eventStages, repairableIdPaths: itemIdPath }
}

function freezeItemContract(definition: CodexItemContract): CodexItemContract {
  return Object.freeze({
    ...definition,
    eventStages: Object.freeze([...definition.eventStages]),
    repairableIdPaths: Object.freeze([...definition.repairableIdPaths])
  })
}
