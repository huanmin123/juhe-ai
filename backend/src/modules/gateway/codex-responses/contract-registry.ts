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
    item('additional_tools', 'at', itemLifecycle, [field('role', 'string'), field('tools', 'array')]),
    item('message', 'msg', fullLifecycle, [field('role', 'string'), field('content', 'array')], [field('phase', 'enum', { nullable: true, values: ['commentary', 'final_answer'] })]),
    item('agent_message', 'amsg', fullLifecycle, [field('author', 'string'), field('recipient', 'string'), field('content', 'array')]),
    item('reasoning', 'rs', fullLifecycle, [field('summary', 'array')], [field('content', 'array', { nullable: true }), field('encrypted_content', 'string', { nullable: true })]),
    item('local_shell_call', 'lsh', itemLifecycle, [field('status', 'enum', { values: ['completed', 'in_progress', 'incomplete'] }), field('action', 'local_shell_action')], [field('call_id', 'string', { nullable: true })]),
    item('function_call', 'fc', fullLifecycle, [field('name', 'string'), field('arguments', 'string'), field('call_id', 'string')], [field('namespace', 'string', { nullable: true })]),
    item('tool_search_call', 'tsc', itemLifecycle, [field('execution', 'string'), field('arguments', 'present')], [field('call_id', 'string', { nullable: true }), field('status', 'string', { nullable: true })]),
    item('function_call_output', 'fco', itemLifecycle, [field('call_id', 'string'), field('output', 'function_output')]),
    item('custom_tool_call', 'ctc', fullLifecycle, [field('call_id', 'string'), field('name', 'string'), field('input', 'string')], [field('status', 'string', { nullable: true }), field('namespace', 'string', { nullable: true })]),
    item('custom_tool_call_output', 'ctco', itemLifecycle, [field('call_id', 'string'), field('output', 'function_output')], [field('name', 'string', { nullable: true })]),
    item('tool_search_output', 'tso', itemLifecycle, [field('status', 'string'), field('execution', 'string'), field('tools', 'array')], [field('call_id', 'string', { nullable: true })]),
    item('web_search_call', 'ws', itemLifecycle, [], [field('status', 'string', { nullable: true }), field('action', 'object', { nullable: true })]),
    item('image_generation_call', 'ig', itemLifecycle, [field('status', 'string'), field('result', 'string')], [field('revised_prompt', 'string', { nullable: true })]),
    item('compaction', 'cmp', itemLifecycle, [field('encrypted_content', 'string')]),
    item('compaction_summary', 'cmp', itemLifecycle, [field('encrypted_content', 'string')]),
    item('context_compaction', 'cmp', itemLifecycle, [], [field('encrypted_content', 'string', { nullable: true })]),
    itemWithoutId('compaction_trigger')
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
  requiredFields: CodexItemContract['requiredFields'] = [],
  optionalFields: CodexItemContract['optionalFields'] = []
): CodexItemContract {
  return { type, prefix, eventStages, repairableIdPaths: itemIdPath, requiredFields, optionalFields: withCommonOptionalFields(optionalFields) }
}

function itemWithoutId(type: string): CodexItemContract {
  return { type, eventStages: [], repairableIdPaths: [], requiredFields: [], optionalFields: [] }
}

function field(
  name: string,
  kind: CodexItemContract['requiredFields'][number]['kind'],
  options: Pick<CodexItemContract['requiredFields'][number], 'nullable' | 'values'> = {}
): CodexItemContract['requiredFields'][number] {
  return { name, kind, ...options }
}

function withCommonOptionalFields(fields: CodexItemContract['optionalFields']): CodexItemContract['optionalFields'] {
  return [...fields, field('internal_chat_message_metadata_passthrough', 'object', { nullable: true })]
}

function freezeItemContract(definition: CodexItemContract): CodexItemContract {
  return Object.freeze({
    ...definition,
    eventStages: Object.freeze([...definition.eventStages]),
    repairableIdPaths: Object.freeze([...definition.repairableIdPaths]),
    requiredFields: freezeFields(definition.requiredFields),
    optionalFields: freezeFields(definition.optionalFields)
  })
}

function freezeFields(fields: CodexItemContract['requiredFields']): CodexItemContract['requiredFields'] {
  return Object.freeze(fields.map((value) => Object.freeze({
    ...value,
    values: value.values ? Object.freeze([...value.values]) : undefined
  })))
}
