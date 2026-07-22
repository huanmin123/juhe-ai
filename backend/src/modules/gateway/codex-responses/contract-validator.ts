import { codexResponsesContractRegistry } from './contract-registry.js'
import type {
  CodexContractOutcome,
  CodexContractRevision,
  CodexContractValidationResult,
  CodexItemContract,
  CodexProtocolIssue,
  CodexProtocolIssueProvenance,
  CodexRequiredItemField
} from './contract-types.js'

type JsonRecord = Record<string, unknown>

const toolOutputTypeByCallType = new Map<string, string>([
  ['function_call', 'function_call_output'],
  ['local_shell_call', 'function_call_output'],
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
  if (collection.outcome === 'absent') return result(input.revision, [])
  if (collection.outcome === 'invalid') {
    return result(input.revision, [issue(
      input,
      collection.code,
      collection.message,
      collection.path,
      -1,
      undefined,
      'R2'
    )])
  }

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

    let hasRepairableIdIssue = false
    if (Object.hasOwn(item, 'id')) {
      if (!contract.prefix || !contract.repairableIdPaths.includes('id')) {
        issues.push(issue(input, 'item_id_forbidden', `${type} 不允许携带 item ID`, [collection.field, index, 'id'], index, type, 'R2'))
        continue
      }
      if (item.id === null && input.provenance !== 'request_history') {
        // ResponseItem.id is Option<ResponseItemId>; null is equivalent to no persisted ID.
      } else {
      const id = nonEmptyString(item.id)
      if (!id) {
        issues.push(issue(input, 'item_id_invalid', `${type} 的 item ID 必须是非空字符串`, [collection.field, index, 'id'], index, type, 'R0'))
        hasRepairableIdIssue = true
      } else {
        if (!isExpectedItemId(id, contract.prefix)) {
          issues.push(issue(input, 'item_id_prefix_mismatch', `${type} 的 item ID 前缀与 contract 不一致`, [collection.field, index, 'id'], index, type, 'R0'))
          hasRepairableIdIssue = true
        }
        if (itemIds.has(id)) {
          issues.push(issue(input, 'duplicate_item_identity', '同一 Responses 文档中出现重复 item ID', [collection.field, index, 'id'], index, type, 'R2'))
        } else {
          itemIds.set(id, index)
        }
      }
      }
    }

    const deferHistoryPayloadCheck = input.provenance === 'request_history' && hasRepairableIdIssue
    if (!deferHistoryPayloadCheck) {
      for (const fieldIssue of validateCodexItemContractFields(item, contract)) {
        issues.push(issue(
          input,
          fieldIssue.code,
          `${type}.${fieldIssue.field} 不满足 Codex Responses contract`,
          [collection.field, index, fieldIssue.field],
          index,
          type,
          'R2'
        ))
      }
    }

    const callId = stringValue(item.call_id)
    if (callId !== undefined && toolOutputTypeByCallType.has(type)) {
      const previousType = calls.get(callId)
      if (previousType) {
        issues.push(issue(
          input,
          previousType === type ? 'duplicate_tool_call_identity' : 'tool_call_type_mismatch',
          previousType === type
            ? `${callId} 在同一 Responses 文档中出现重复工具调用`
            : `${callId} 同时被声明为 ${previousType} 和 ${type}`,
          [collection.field, index, 'call_id'],
          index,
          type,
          'R2'
        ))
      } else {
        calls.set(callId, type)
      }
    }
    if (
      callId !== undefined
      && toolOutputTypes.has(type)
      && !(type === 'tool_search_output' && item.execution === 'server')
    ) {
      outputs.push({ index, type, callId })
    }
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

function responseItemCollection(response: JsonRecord):
  | { outcome: 'present'; field: 'input' | 'output'; items: unknown[] }
  | { outcome: 'absent' }
  | { outcome: 'invalid'; code: string; message: string; path: readonly string[] } {
  const hasInput = Object.hasOwn(response, 'input')
  const hasOutput = Object.hasOwn(response, 'output')
  if (hasInput && hasOutput) {
    return {
      outcome: 'invalid',
      code: 'response_item_collections_ambiguous',
      message: '同一 Codex Responses 文档不能同时包含 input 与 output item 集合',
      path: []
    }
  }
  if (!hasInput && !hasOutput) return { outcome: 'absent' }
  const field = hasOutput ? 'output' : 'input'
  const value = response[field]
  if (!Array.isArray(value)) {
    return {
      outcome: 'invalid',
      code: 'response_item_collection_invalid',
      message: `${field} 必须是 Responses item 数组`,
      path: [field]
    }
  }
  return { outcome: 'present', field, items: value }
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

export function validateCodexItemContractFields(
  item: JsonRecord,
  contract: CodexItemContract
): Array<{ code: 'item_required_field_invalid' | 'item_optional_field_invalid'; field: string }> {
  const issues: Array<{ code: 'item_required_field_invalid' | 'item_optional_field_invalid'; field: string }> = []
  for (const field of contract.requiredFields) {
    if (!validContractField(item, field, true)) issues.push({ code: 'item_required_field_invalid', field: field.name })
  }
  for (const field of contract.optionalFields) {
    if (!validContractField(item, field, false)) issues.push({ code: 'item_optional_field_invalid', field: field.name })
  }
  return issues
}

function validContractField(item: JsonRecord, field: CodexRequiredItemField, required: boolean): boolean {
  if (!Object.hasOwn(item, field.name)) return !required
  const value = item[field.name]
  if (value === null) return field.nullable === true
  switch (field.kind) {
    case 'present': return true
    case 'string': return typeof value === 'string'
    case 'array': return Array.isArray(value)
    case 'object': return plainObject(value) !== undefined
    case 'enum': return typeof value === 'string' && Boolean(field.values?.includes(value))
    case 'function_output': return validFunctionOutput(value)
    case 'local_shell_action': return validLocalShellAction(value)
  }
}

function validFunctionOutput(value: unknown): boolean {
  if (typeof value === 'string') return true
  if (!Array.isArray(value)) return false
  return value.every((entry) => {
    const item = plainObject(entry)
    if (!item) return false
    if (item.type === 'input_text') return typeof item.text === 'string'
    if (item.type === 'encrypted_content') return typeof item.encrypted_content === 'string'
    if (item.type !== 'input_image' || typeof item.image_url !== 'string') return false
    return !Object.hasOwn(item, 'detail')
      || item.detail === null
      || (typeof item.detail === 'string' && ['auto', 'low', 'high', 'original'].includes(item.detail))
  })
}

function validLocalShellAction(value: unknown): boolean {
  const action = plainObject(value)
  if (!action || action.type !== 'exec' || !Array.isArray(action.command) || !action.command.every((part) => typeof part === 'string')) {
    return false
  }
  if (!nullableOptional(action, 'timeout_ms', (candidate) => Number.isSafeInteger(candidate) && Number(candidate) >= 0)) return false
  if (!nullableOptional(action, 'working_directory', (candidate) => typeof candidate === 'string')) return false
  if (!nullableOptional(action, 'user', (candidate) => typeof candidate === 'string')) return false
  return nullableOptional(action, 'env', (candidate) => {
    const env = plainObject(candidate)
    return Boolean(env) && Object.values(env ?? {}).every((entry) => typeof entry === 'string')
  })
}

function nullableOptional(item: JsonRecord, name: string, validate: (value: unknown) => boolean): boolean {
  return !Object.hasOwn(item, name) || item[name] === null || validate(item[name])
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' ? value : undefined
}

function plainObject(value: unknown): JsonRecord | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as JsonRecord : undefined
}
