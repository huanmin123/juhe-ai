import type {
  CodexContractRevision,
  CodexItemContract,
  CodexResponsesContractRegistry
} from './contract-types.js'

export const codexResponsesContractRevision: CodexContractRevision = 'codex-responses-2026-07-11-r1'

// Request-side history normalization only needs the upstream item's ID prefix.
// Response shape and lifecycle validation deliberately do not live here.
export const codexResponsesContractRegistry = createCodexResponsesContractRegistry(
  codexResponsesContractRevision,
  [
    item('additional_tools', 'at'),
    item('message', 'msg'),
    item('agent_message', 'amsg'),
    item('reasoning', 'rs'),
    item('local_shell_call', 'lsh'),
    item('function_call', 'fc'),
    item('tool_search_call', 'tsc'),
    item('function_call_output', 'fco'),
    item('custom_tool_call', 'ctc'),
    item('custom_tool_call_output', 'ctco'),
    item('tool_search_output', 'tso'),
    item('web_search_call', 'ws'),
    item('image_generation_call', 'ig'),
    item('compaction', 'cmp'),
    item('compaction_summary', 'cmp'),
    item('context_compaction', 'cmp'),
    itemWithoutId('compaction_trigger')
  ]
)

export function createCodexResponsesContractRegistry(
  revision: CodexContractRevision,
  definitions: readonly CodexItemContract[]
): CodexResponsesContractRegistry {
  const items = Object.freeze(definitions.map((definition) => Object.freeze({ ...definition })))
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

function item(type: string, prefix: string): CodexItemContract {
  return { type, prefix }
}

function itemWithoutId(type: string): CodexItemContract {
  return { type }
}
