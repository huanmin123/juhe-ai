import type {
  CodexHistorySanitizerContext,
  CodexHistorySanitizerResult
} from './request-history-types.js'
import { codexResponsesContractRegistry } from './contract-registry.js'

type JsonRecord = Record<string, unknown>

export function sanitizeCodexResponseHistoryItems(
  items: unknown[],
  context: CodexHistorySanitizerContext
): CodexHistorySanitizerResult {
  let output: unknown[] | undefined
  let removedIdCount = 0
  let droppedItemCount = 0
  const issueCodes: string[] = []

  for (let index = 0; index < items.length; index += 1) {
    const item = items[index]
    const decision = itemIdRemovalDecision(item, context)
    if (!decision) continue
    const outputIndex = index - droppedItemCount
    if (!output) output = items.slice()
    if (!isReplayableCodexHistoryItem(item)) {
      output.splice(outputIndex, 1)
      droppedItemCount += 1
      pushIssueCode(issueCodes, decision.issueCode)
      pushIssueCode(issueCodes, 'unrecoverable_item_dropped')
      continue
    }
    const { id: _removedId, ...copy } = decision.item
    output[outputIndex] = copy
    removedIdCount += 1
    pushIssueCode(issueCodes, decision.issueCode)
  }

  return {
    items: output ?? items,
    changed: output !== undefined,
    removedIdCount,
    droppedItemCount,
    issueCodes
  }
}

function pushIssueCode(issueCodes: string[], issueCode: string): void {
  if (!issueCodes.includes(issueCode)) issueCodes.push(issueCode)
}

function itemIdRemovalDecision(
  value: unknown,
  context: CodexHistorySanitizerContext
): { item: JsonRecord; type: string; issueCode: string } | undefined {
  if (!isPlainObject(value)) return undefined
  const type = stringValue(value.type)
  const expectedPrefix = type ? codexResponsesContractRegistry.item(type)?.prefix : undefined
  if (!type || !expectedPrefix || !Object.hasOwn(value, 'id')) return undefined

  const id = stringValue(value.id)
  if (!id) {
    return { item: value, type, issueCode: 'invalid_item_id' }
  }

  if (!hasNonEmptyPrefixAndSuffix(id)) {
    return { item: value, type, issueCode: 'legacy_item_id' }
  }
  if (!id.startsWith(`${expectedPrefix}_`)) {
    return { item: value, type, issueCode: 'item_id_prefix_mismatch' }
  }
  if (context.targetPersistenceScope === 'none' && context.store === false) {
    return { item: value, type, issueCode: 'unpersisted_item_reference' }
  }
  if (context.sourceScopeKey && context.targetScopeKey && context.sourceScopeKey !== context.targetScopeKey) {
    return { item: value, type, issueCode: 'cross_scope_item_reference' }
  }
  return undefined
}

export function isReplayableCodexHistoryItem(value: unknown): boolean {
  if (!isPlainObject(value)) return false
  switch (stringValue(value.type)) {
    case 'additional_tools':
      return typeof value.role === 'string' && Array.isArray(value.tools)
    case 'message':
      return typeof value.role === 'string' && Array.isArray(value.content)
    case 'agent_message':
      return typeof value.author === 'string' && typeof value.recipient === 'string' && Array.isArray(value.content)
    case 'reasoning':
      return hasArrayContent(value.summary)
        || hasArrayContent(value.content)
        || (typeof value.encrypted_content === 'string' && value.encrypted_content.length > 0)
    case 'local_shell_call':
      return isPlainObject(value.action)
    case 'function_call':
      return typeof value.name === 'string' && typeof value.arguments === 'string' && typeof value.call_id === 'string'
    case 'tool_search_call':
      return Object.hasOwn(value, 'arguments') && typeof value.execution === 'string'
    case 'function_call_output':
    case 'custom_tool_call_output':
      return typeof value.call_id === 'string' && Object.hasOwn(value, 'output')
    case 'custom_tool_call':
      return typeof value.name === 'string' && typeof value.input === 'string' && typeof value.call_id === 'string'
    case 'tool_search_output':
      return typeof value.execution === 'string' && Array.isArray(value.tools)
    case 'web_search_call':
      return Object.hasOwn(value, 'action') || typeof value.status === 'string'
    case 'image_generation_call':
      return typeof value.status === 'string' && typeof value.result === 'string'
    case 'compaction':
    case 'compaction_summary':
      return typeof value.encrypted_content === 'string'
    case 'context_compaction':
      return typeof value.encrypted_content === 'string'
    default:
      return false
  }
}

function hasNonEmptyPrefixAndSuffix(value: string): boolean {
  const separator = value.indexOf('_')
  return separator > 0 && separator < value.length - 1
}

function hasArrayContent(value: unknown): boolean {
  return Array.isArray(value) && value.length > 0
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.length > 0 ? value : undefined
}

function isPlainObject(value: unknown): value is JsonRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
