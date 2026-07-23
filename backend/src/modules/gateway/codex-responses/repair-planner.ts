import { randomUUID } from 'node:crypto'

import { codexResponsesContractRegistry } from './contract-registry.js'
import type {
  CodexContractValidationResult,
  CodexRepairOperation,
  CodexRepairPlan
} from './contract-types.js'

type JsonRecord = Record<string, unknown>

export interface CodexItemIdFactoryInput {
  prefix: string
  type: string
  sequence: number
  outputIndex: number
}

export function planCodexResponsesJsonRepair(input: {
  document: JsonRecord
  validation: CodexContractValidationResult
  downstreamExposed: boolean
  createItemId?: (input: CodexItemIdFactoryInput) => string
}): CodexRepairPlan {
  const provenance = input.validation.issues[0]?.provenance ?? 'unknown'
  const forbidden = input.validation.issues.find((issue) => issue.repairLevel === 'R2')
  if (forbidden) {
    return repairPlan(input.validation, provenance, 'R2', [], forbidden.code)
  }

  const operations: CodexRepairOperation[] = []
  const plannedPaths = new Set<string>()
  const existingIds = responseItemIds(input.document)
  let sequence = 1

  for (const issue of input.validation.issues) {
    if (issue.repairLevel !== 'R0') continue
    const target = repairTarget(input.document, issue.path)
    if (!target) return repairPlan(input.validation, provenance, 'R2', [], 'invalid_repair_path')
    const pathKey = issue.path.join('/')
    if (plannedPaths.has(pathKey)) continue

    if (target.field === 'input') {
      operations.push({
        action: 'remove',
        path: issue.path,
        expectedItemType: target.type,
        expectedItemIdPresent: Object.hasOwn(target.item, 'id'),
        expectedItemId: target.item.id,
        issueCode: issue.code,
        ruleId: 'codex.r0.request_history.remove_item_id'
      })
      plannedPaths.add(pathKey)
      continue
    }
    if (input.downstreamExposed) {
      return repairPlan(input.validation, provenance, 'R2', [], 'downstream_item_identity_already_exposed')
    }
    const contract = codexResponsesContractRegistry.item(target.type)
    if (!contract?.prefix) {
      return repairPlan(input.validation, provenance, 'R2', [], 'unknown_item_type')
    }
    let generated: string | undefined
    for (let attempt = 0; attempt < 100 && !generated; attempt += 1) {
      const candidate = (input.createItemId ?? defaultItemIdFactory)({
        prefix: contract.prefix,
        type: target.type,
        sequence,
        outputIndex: target.index
      })
      sequence += 1
      if (candidate.startsWith(`${contract.prefix}_`) && candidate.length > contract.prefix.length + 1 && !existingIds.has(candidate)) {
        generated = candidate
      }
    }
    if (!generated) return repairPlan(input.validation, provenance, 'R2', [], 'item_id_generation_failed')
    existingIds.add(generated)
    operations.push({
      action: 'replace',
      path: issue.path,
      value: generated,
      expectedItemType: target.type,
      expectedItemIdPresent: Object.hasOwn(target.item, 'id'),
      expectedItemId: target.item.id,
      issueCode: issue.code,
      ruleId: 'codex.r0.response.replace_item_id'
    })
    plannedPaths.add(pathKey)
  }

  if (operations.length === 0 && input.validation.outcome !== 'clean') {
    return repairPlan(input.validation, provenance, 'R2', [], 'no_safe_repair_available')
  }
  return repairPlan(input.validation, provenance, 'R0', operations)
}

function repairPlan(
  validation: CodexContractValidationResult,
  provenance: CodexRepairPlan['provenance'],
  level: CodexRepairPlan['level'],
  operations: readonly CodexRepairOperation[],
  forbiddenReason?: string
): CodexRepairPlan {
  return {
    revision: validation.revision,
    level,
    provenance,
    sourceOutcome: validation.outcome,
    operations,
    forbiddenReason
  }
}

function repairTarget(
  document: JsonRecord,
  path: readonly (string | number)[]
): { field: 'input' | 'output'; index: number; type: string; item: JsonRecord } | undefined {
  if (path.length !== 3 || (path[0] !== 'input' && path[0] !== 'output') || !Number.isInteger(path[1]) || path[2] !== 'id') {
    return undefined
  }
  const field = path[0]
  const index = Number(path[1])
  const items = Array.isArray(document[field]) ? document[field] as unknown[] : undefined
  const item = items && plainObject(items[index])
  const type = item && typeof item.type === 'string' ? item.type : undefined
  return type && item ? { field, index, type, item } : undefined
}

function responseItemIds(document: JsonRecord): Set<string> {
  const ids = new Set<string>()
  for (const field of ['input', 'output'] as const) {
    const items = Array.isArray(document[field]) ? document[field] as unknown[] : []
    for (const value of items) {
      const item = plainObject(value)
      if (typeof item?.id === 'string' && item.id) ids.add(item.id)
    }
  }
  return ids
}

function defaultItemIdFactory(input: CodexItemIdFactoryInput): string {
  return `${input.prefix}_gateway_${randomUUID().replaceAll('-', '')}`
}

function plainObject(value: unknown): JsonRecord | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as JsonRecord : undefined
}
