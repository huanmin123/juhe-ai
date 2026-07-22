import { GatewayRequestValidationError } from '../request/validation-error.js'
import { validateCodexResponsesJson } from './contract-validator.js'
import type { CodexRepairOperation, CodexRepairPlan } from './contract-types.js'
import { isReplayableCodexHistoryItem } from './request-history-sanitizer.js'
import { codexResponsesContractRegistry } from './contract-registry.js'

type JsonRecord = Record<string, unknown>

export function executeCodexResponsesRepair(document: JsonRecord, plan: CodexRepairPlan): JsonRecord {
  if (plan.revision !== codexResponsesContractRegistry.revision) {
    throw new Error('codex_repair_forbidden: unsupported contract revision')
  }
  if (plan.level !== 'R0') {
    throw new Error(`codex_repair_forbidden: ${plan.forbiddenReason ?? 'R2 plan'}`)
  }
  if (plan.operations.length === 0) return document

  const output: JsonRecord = { ...document }
  const copiedArrays = new Map<'input' | 'output', unknown[]>()
  const copiedItems = new Map<string, JsonRecord>()

  for (const operation of plan.operations) {
    const target = operationTarget(document, operation)
    assertOperationAllowed(plan, operation, target)
    if (target.field === 'input' && operation.action === 'remove' && !isReplayableCodexHistoryItem(target.item)) {
      throw new GatewayRequestValidationError(
        'codex_history_item_unrecoverable: Codex Responses 历史 item 只有 ID，没有可重放内容',
        'codex_history_item_unrecoverable'
      )
    }
    let items = copiedArrays.get(target.field)
    if (!items) {
      items = [...target.items]
      copiedArrays.set(target.field, items)
      output[target.field] = items
    }
    const itemKey = `${target.field}:${target.index}`
    let item = copiedItems.get(itemKey)
    if (!item) {
      item = { ...target.item }
      copiedItems.set(itemKey, item)
      items[target.index] = item
    }
    applyOperation(item, operation)
  }

  assertSemanticInvariants(document, output)
  const postValidation = validateCodexResponsesJson({
    response: output,
    provenance: plan.provenance,
    revision: plan.revision
  })
  const remainingViolation = postValidation.issues.find((issue) => issue.repairLevel !== undefined)
  if (remainingViolation) {
    throw new Error(`codex_repair_post_validation_failed: ${remainingViolation.code}`)
  }
  return output
}

function assertOperationAllowed(
  plan: CodexRepairPlan,
  operation: CodexRepairOperation,
  target: { field: 'input' | 'output'; item: JsonRecord }
): void {
  const contract = codexResponsesContractRegistry.item(operation.expectedItemType)
  if (!contract || !contract.repairableIdPaths.includes('id') || target.item.type !== operation.expectedItemType) {
    throw new Error('codex_repair_forbidden: target item type is not repairable')
  }
  const idPresent = Object.hasOwn(target.item, 'id')
  if (idPresent !== operation.expectedItemIdPresent || !Object.is(target.item.id, operation.expectedItemId)) {
    throw new Error('codex_repair_forbidden: stale item ID precondition')
  }
  const allowedIssue = operation.issueCode === 'item_id_invalid' || operation.issueCode === 'item_id_prefix_mismatch'
  if (!allowedIssue) throw new Error('codex_repair_forbidden: issue is not R0 allowlisted')

  if (target.field === 'input') {
    if (
      plan.provenance !== 'request_history'
      || operation.action !== 'remove'
      || operation.ruleId !== 'codex.r0.request_history.remove_item_id'
    ) {
      throw new Error('codex_repair_forbidden: request history only permits ID removal')
    }
    return
  }
  if (
    (plan.provenance !== 'raw_upstream' && plan.provenance !== 'gateway_bridge')
    || operation.action !== 'replace'
    || operation.ruleId !== 'codex.r0.response.replace_item_id'
    || typeof operation.value !== 'string'
    || !contract.prefix
    || !operation.value.startsWith(`${contract.prefix}_`)
  ) {
    throw new Error('codex_repair_forbidden: response only permits typed ID replacement')
  }
}

function operationTarget(document: JsonRecord, operation: CodexRepairOperation): {
  field: 'input' | 'output'
  index: number
  items: unknown[]
  item: JsonRecord
} {
  const [fieldValue, indexValue, property] = operation.path
  if (
    operation.path.length !== 3
    || (fieldValue !== 'input' && fieldValue !== 'output')
    || !Number.isInteger(indexValue)
    || property !== 'id'
  ) {
    throw new Error('codex_repair_forbidden: operation path is not an allowed item ID path')
  }
  const field = fieldValue
  const index = Number(indexValue)
  const items = Array.isArray(document[field]) ? document[field] as unknown[] : undefined
  const item = items && plainObject(items[index])
  if (!items || !item) throw new Error('codex_repair_forbidden: operation target is missing')
  return { field, index, items, item }
}

function applyOperation(item: JsonRecord, operation: CodexRepairOperation): void {
  if (operation.action === 'remove') {
    delete item.id
    return
  }
  if (operation.action === 'replace' && typeof operation.value === 'string' && operation.value.length > 0) {
    item.id = operation.value
    return
  }
  throw new Error('codex_repair_forbidden: invalid item ID operation')
}

function assertSemanticInvariants(before: JsonRecord, after: JsonRecord): void {
  for (const field of ['input', 'output'] as const) {
    const beforeItems = Array.isArray(before[field]) ? before[field] as unknown[] : undefined
    const afterItems = Array.isArray(after[field]) ? after[field] as unknown[] : undefined
    if (!beforeItems && !afterItems) continue
    if (!beforeItems || !afterItems || beforeItems.length !== afterItems.length) {
      throw new Error('codex_repair_semantic_invariant_failed: item count changed')
    }
    for (let index = 0; index < beforeItems.length; index += 1) {
      const beforeItem = plainObject(beforeItems[index])
      const afterItem = plainObject(afterItems[index])
      if (!beforeItem || !afterItem || !sameExceptItemId(beforeItem, afterItem)) {
        throw new Error(`codex_repair_semantic_invariant_failed: ${field}[${index}] changed outside id`)
      }
    }
  }
}

function sameExceptItemId(before: JsonRecord, after: JsonRecord): boolean {
  const beforeKeys = Object.keys(before).filter((key) => key !== 'id')
  const afterKeys = Object.keys(after).filter((key) => key !== 'id')
  if (beforeKeys.length !== afterKeys.length) return false
  return beforeKeys.every((key) => Object.hasOwn(after, key) && Object.is(before[key], after[key]))
}

function plainObject(value: unknown): JsonRecord | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as JsonRecord : undefined
}
