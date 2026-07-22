import { codexResponsesContractRegistry } from './contract-registry.js'
import type {
  CodexContractOutcome,
  CodexContractRevision,
  CodexContractValidationResult,
  CodexProtocolIssue,
  CodexProtocolIssueProvenance
} from './contract-types.js'

type JsonRecord = Record<string, unknown>

const toolOutputTypeByCallType = new Map<string, string>([
  ['function_call', 'function_call_output'],
  ['custom_tool_call', 'custom_tool_call_output'],
  ['tool_search_call', 'tool_search_output']
])
const toolOutputTypes = new Set(['function_call_output', 'custom_tool_call_output', 'tool_search_output'])

export function validateCodexResponsesJson(input: {
  response: Record<string, unknown>
  provenance: CodexProtocolIssueProvenance
  revision: CodexContractRevision
}): CodexContractValidationResult {
  const collection = responseItemCollection(input.response)
  if (!collection) return result(input.revision, [])

  const issues: CodexProtocolIssue[] = []
  const itemIds = new Map<string, number>()
  const calls = new Map<string, string>()
  const outputs: Array<{ index: number; type: string; callId: string }> = []

  for (let index = 0; index < collection.items.length; index += 1) {
    const item = plainObject(collection.items[index])
    if (!item) {
      issues.push(issue(input, 'item_not_object', 'Responses item 必须是对象', [collection.field, index], index, undefined, 'R2'))
      continue
    }
    const type = nonEmptyString(item.type)
    if (!type) {
      issues.push(issue(input, 'item_type_missing', 'Responses item 缺少非空 type', [collection.field, index, 'type'], index, undefined, 'R2'))
      continue
    }
    const contract = codexResponsesContractRegistry.item(type)
    if (!contract) {
      issues.push(issue(input, 'unknown_item_type', `未知 Codex Responses item type: ${type}`, [collection.field, index, 'type'], index, type))
      continue
    }

    if (input.provenance !== 'request_history') {
      for (const requiredField of contract.requiredFields) {
        if (!validRequiredField(item, requiredField.name, requiredField.kind)) {
          issues.push(issue(
            input,
            'item_required_field_invalid',
            `${type}.${requiredField.name} 不满足 Codex Responses contract`,
            [collection.field, index, requiredField.name],
            index,
            type,
            'R2'
          ))
        }
      }
    }

    if (Object.hasOwn(item, 'id')) {
      const id = nonEmptyString(item.id)
      if (!id) {
        issues.push(issue(input, 'item_id_invalid', `${type} 的 item ID 必须是非空字符串`, [collection.field, index, 'id'], index, type, 'R0'))
      } else {
        if (!isExpectedItemId(id, contract.prefix)) {
          issues.push(issue(input, 'item_id_prefix_mismatch', `${type} 的 item ID 前缀与 contract 不一致`, [collection.field, index, 'id'], index, type, 'R0'))
        }
        if (itemIds.has(id)) {
          issues.push(issue(input, 'duplicate_item_identity', '同一 Responses 文档中出现重复 item ID', [collection.field, index, 'id'], index, type, 'R0'))
        } else {
          itemIds.set(id, index)
        }
      }
    }

    const callId = nonEmptyString(item.call_id)
    if (callId && toolOutputTypeByCallType.has(type)) calls.set(callId, type)
    if (callId && toolOutputTypes.has(type)) outputs.push({ index, type, callId })
  }

  const externalHistoryAvailable = Boolean(nonEmptyString(input.response.previous_response_id))
  const completedCallIds = new Set<string>()
  for (const output of outputs) {
    if (completedCallIds.has(output.callId)) {
      issues.push(issue(
        input,
        'duplicate_tool_output',
        `${output.callId} 在同一 Responses 文档中出现重复工具输出`,
        [collection.field, output.index, 'call_id'],
        output.index,
        output.type,
        'R2'
      ))
      continue
    }
    completedCallIds.add(output.callId)
    const callType = calls.get(output.callId)
    if (callType) {
      if (toolOutputTypeByCallType.get(callType) !== output.type) {
        issues.push(issue(
          input,
          'tool_call_type_mismatch',
          `${callType} 的 call_id 不能由 ${output.type} 完成`,
          [collection.field, output.index, 'call_id'],
          output.index,
          output.type,
          'R2'
        ))
      }
    } else if (!externalHistoryAvailable) {
        issues.push(issue(
          input,
          'orphan_tool_output',
          `${output.type} 引用了当前完整历史中不存在的 call_id`,
          [collection.field, output.index, 'call_id'],
          output.index,
          output.type,
          'R2'
        ))
    }
  }

  return result(input.revision, issues)
}

function responseItemCollection(response: JsonRecord): { field: 'input' | 'output'; items: unknown[] } | undefined {
  if (Array.isArray(response.output)) return { field: 'output', items: response.output }
  if (Array.isArray(response.input)) return { field: 'input', items: response.input }
  return undefined
}

function result(revision: CodexContractRevision, issues: CodexProtocolIssue[]): CodexContractValidationResult {
  return {
    revision,
    outcome: validationOutcome(issues),
    issues
  }
}

function validationOutcome(issues: readonly CodexProtocolIssue[]): CodexContractOutcome {
  if (issues.length === 0) return 'clean'
  if (issues.some((value) => value.repairLevel === 'R2')) return 'blocked'
  if (issues.some((value) => value.repairLevel === 'R0')) return 'repairable'
  return 'observed_unknown'
}

function issue(
  input: { provenance: CodexProtocolIssueProvenance },
  code: string,
  message: string,
  path: readonly (string | number)[],
  outputIndex: number,
  itemType?: string,
  repairLevel?: 'R0' | 'R2'
): CodexProtocolIssue {
  return {
    code,
    message,
    path,
    provenance: input.provenance,
    itemType,
    outputIndex,
    repairLevel
  }
}

function isExpectedItemId(id: string, prefix: string | undefined): boolean {
  return Boolean(prefix) && id.startsWith(`${prefix}_`) && id.length > (prefix?.length ?? 0) + 1
}

function nonEmptyString(value: unknown): string | undefined {
  return typeof value === 'string' && value.length > 0 ? value : undefined
}

function validRequiredField(
  item: JsonRecord,
  name: string,
  kind: 'present' | 'string' | 'non_empty_string' | 'array' | 'object'
): boolean {
  if (!Object.hasOwn(item, name)) return false
  const value = item[name]
  switch (kind) {
    case 'present': return true
    case 'string': return typeof value === 'string'
    case 'non_empty_string': return typeof value === 'string' && value.length > 0
    case 'array': return Array.isArray(value)
    case 'object': return plainObject(value) !== undefined
  }
}

function plainObject(value: unknown): JsonRecord | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as JsonRecord : undefined
}
