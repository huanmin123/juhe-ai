import { createHash, randomUUID } from 'node:crypto'

import {
  codexResponsesContractRegistry,
  codexResponsesContractRevision
} from './contract-registry.js'
import { validateCodexItemContractFields } from './contract-validator.js'
import type {
  CodexContractOutcome,
  CodexContractRevision,
  CodexItemContract,
  CodexProtocolIssue,
  CodexProtocolIssueProvenance,
  CodexResponseItemEventStage
} from './contract-types.js'

type JsonRecord = Record<string, unknown>

export const codexStreamContractDiagnosticLimit = 32

export interface CodexStreamItemIdentity {
  itemId?: string
  upstreamItemId?: string
  clientItemId?: string
  itemType?: string
  callId?: string
  outputIndex: number
  stage: CodexResponseItemEventStage
}

export interface CodexStreamIdRepair {
  outputIndex?: number
  itemType: string
  field: 'item.id' | 'item_id' | 'response.output.id'
  clientItemId: string
}

export interface CodexStreamContractEventResult {
  revision: CodexContractRevision
  outcome: CodexContractOutcome
  issue?: CodexProtocolIssue
  issues: readonly CodexProtocolIssue[]
  repairs: readonly CodexStreamIdRepair[]
  eventCategory: 'sse_comment' | 'protocol_event'
}

export interface CodexStreamContractSnapshot {
  revision: CodexContractRevision
  identityCount: number
  itemIdOwnerCount: number
  diagnostics: readonly CodexProtocolIssue[]
  omittedDiagnosticCount: number
}

export type CodexStreamContractInput = {
  responseResourceId: string
  event: unknown
} | {
  kind: 'comment'
  comment?: string
}

export class CodexResponsesStreamContractState {
  readonly #provenance: CodexProtocolIssueProvenance
  readonly #identities = new Map<string, CodexStreamItemIdentity>()
  readonly #itemIdOwners = new Map<string, string>()
  readonly #diagnostics: CodexProtocolIssue[] = []
  readonly #clientItemIds = new Set<string>()
  readonly #observedItemIds = new Set<string>()
  readonly #eventRepairs: CodexStreamIdRepair[] = []
  readonly #repairItemIds: boolean
  readonly #createItemId?: (input: { prefix: string; type: string; sequence: number; outputIndex: number }) => string
  #omittedDiagnosticCount = 0
  #completed = false
  #standaloneSequence = 0
  #repairSequence = 1
  #allowNewRepair = true

  constructor(input: {
    provenance: CodexProtocolIssueProvenance
    repairItemIds?: boolean
    createItemId?: (input: { prefix: string; type: string; sequence: number; outputIndex: number }) => string
  }) {
    this.#provenance = input.provenance
    this.#repairItemIds = input.repairItemIds === true
    this.#createItemId = input.createItemId
  }

  consume(input: CodexStreamContractInput, options: { allowNewRepair?: boolean } = {}): CodexStreamContractEventResult {
    this.#allowNewRepair = options.allowNewRepair !== false
    try {
      return this.#consume(input)
    } finally {
      this.#allowNewRepair = true
    }
  }

  #consume(input: CodexStreamContractInput): CodexStreamContractEventResult {
    if ('kind' in input) return cleanResult('sse_comment')
    const event = plainObject(input.event)
    if (!event) {
      return this.#result([this.#issue(
        'stream_event_not_object',
        'Responses SSE event 必须是对象',
        ['event'],
        undefined,
        undefined,
        'R2'
      )])
    }
    const eventType = nonEmptyString(event.type)
    if (!eventType) {
      return this.#result([this.#issue(
        'stream_event_type_missing',
        'Responses SSE event 缺少非空 type',
        ['event', 'type'],
        undefined,
        undefined,
        'R2'
      )])
    }
    if (this.#completed) {
      return this.#result([this.#issue(
        'event_after_response_completed',
        'response.completed 后不得继续发送协议事件',
        ['event', 'type'],
        undefined,
        undefined,
        'R2'
      )])
    }
    if (eventType === 'response.completed') {
      this.#completed = true
      return this.#consumeCompleted(input.responseResourceId, event)
    }
    const stage = eventStage(eventType)
    if (!stage) return cleanResult('protocol_event')
    return this.#consumeItemEvent(input.responseResourceId, eventType, event, stage)
  }

  canTransparentRetry(input: { semanticCommitted: boolean }): boolean {
    return !input.semanticCommitted
  }

  identityFor(responseResourceId: string, outputIndex: number): CodexStreamItemIdentity | undefined {
    const identity = this.#identities.get(itemKey(responseScope(responseResourceId), outputIndex))
    return identity ? { ...identity } : undefined
  }

  snapshot(): CodexStreamContractSnapshot {
    return {
      revision: codexResponsesContractRevision,
      identityCount: this.#identities.size,
      itemIdOwnerCount: this.#itemIdOwners.size,
      diagnostics: this.#diagnostics.map(cloneIssue),
      omittedDiagnosticCount: this.#omittedDiagnosticCount
    }
  }

  dispose(): void {
    this.#identities.clear()
    this.#itemIdOwners.clear()
    this.#clientItemIds.clear()
    this.#observedItemIds.clear()
    this.#eventRepairs.length = 0
    this.#diagnostics.length = 0
    this.#omittedDiagnosticCount = 0
    this.#completed = false
    this.#standaloneSequence = 0
    this.#repairSequence = 1
    this.#allowNewRepair = true
  }

  #consumeCompleted(responseResourceId: string, event: JsonRecord): CodexStreamContractEventResult {
    const response = plainObject(event.response)
    const completedResponseId = response && nonEmptyString(response.id)
    const issues: CodexProtocolIssue[] = []
    if (!completedResponseId) {
      issues.push(this.#issue('response_resource_id_missing', 'response.completed 缺少 response.id', ['event', 'response', 'id'], undefined, undefined, 'R2'))
    } else if (nonEmptyString(responseResourceId) && completedResponseId !== responseResourceId) {
      issues.push(this.#issue(
        'response_resource_id_inconsistent',
        'response.completed 的 response.id 与当前流资源不一致',
        ['event', 'response', 'id'],
        undefined,
        undefined,
        'R2'
      ))
    }
    if (response && Object.hasOwn(response, 'output')) {
      if (!Array.isArray(response.output)) {
        issues.push(this.#issue('response_item_collection_invalid', 'response.completed.response.output 必须是数组', ['event', 'response', 'output'], undefined, undefined, 'R2'))
      } else {
        const completedItemIdOwners = new Map<string, number>()
        for (let index = 0; index < response.output.length; index += 1) {
          issues.push(...this.#inspectCompletedItem(
            responseResourceId,
            index,
            response.output[index],
            completedItemIdOwners
          ))
        }
      }
    }
    return this.#result(issues)
  }

  #inspectCompletedItem(
    responseResourceId: string,
    outputIndex: number,
    value: unknown,
    completedItemIdOwners: Map<string, number>
  ): CodexProtocolIssue[] {
    const item = plainObject(value)
    if (!item) return [this.#issue('item_not_object', 'completed output item 必须是对象', ['event', 'response', 'output', outputIndex], outputIndex, undefined, 'R2')]
    const itemType = nonEmptyString(item.type)
    if (!itemType) return [this.#issue('item_type_missing', 'completed output item 缺少 type', ['event', 'response', 'output', outputIndex, 'type'], outputIndex, undefined, 'R2')]
    const issues: CodexProtocolIssue[] = []
    const previous = this.#identities.get(itemKey(responseScope(responseResourceId), outputIndex))
    const itemId = stringValue(item.id)
    const callId = stringValue(item.call_id)
    if (previous?.itemId !== undefined && itemId !== undefined && previous.itemId !== identityToken(itemId)) {
      issues.push(this.#issue('event_item_id_inconsistent', 'completed output 的 item ID 与流式 identity 不一致', ['event', 'response', 'output', outputIndex, 'id'], outputIndex, itemType, 'R2'))
    }
    if (previous?.itemType && previous.itemType !== identityToken(itemType)) {
      issues.push(this.#issue('event_item_type_inconsistent', 'completed output 的 item type 与流式 identity 不一致', ['event', 'response', 'output', outputIndex, 'type'], outputIndex, itemType, 'R2'))
    }
    if (previous?.callId !== undefined && callId !== undefined && previous.callId !== identityToken(callId)) {
      issues.push(this.#issue('event_call_id_inconsistent', 'completed output 的 call_id 与流式 identity 不一致', ['event', 'response', 'output', outputIndex, 'call_id'], outputIndex, itemType, 'R2'))
    }
    const contract = codexResponsesContractRegistry.item(itemType)
    if (!contract) {
      issues.push(this.#issue('unknown_item_type', 'completed output 出现 registry 未知 item type', ['event', 'response', 'output', outputIndex, 'type'], outputIndex, itemType))
      return issues
    }
    if (!contract.eventStages.includes('done')) {
      issues.push(this.#issue('event_stage_invalid', `${itemType} 不允许 done 阶段`, ['event', 'response', 'output', outputIndex, 'type'], outputIndex, itemType, 'R2'))
    }
    issues.push(...this.#itemContractIssues(item, contract, 'done', outputIndex, ['event', 'response', 'output', outputIndex]))
    if (Object.hasOwn(item, 'id')) {
      const rawId = item.id
      if (!contract.prefix || !contract.repairableIdPaths.includes('id')) {
        issues.push(this.#issue('item_id_forbidden', `${itemType} 不允许携带 item ID`, ['event', 'response', 'output', outputIndex, 'id'], outputIndex, itemType, 'R2'))
      } else if (rawId !== null && (typeof rawId !== 'string' || rawId.length === 0)) {
        issues.push(this.#issue('item_id_invalid', `${itemType} 的 item ID 必须是字符串或 null`, ['event', 'response', 'output', outputIndex, 'id'], outputIndex, itemType, 'R0'))
      } else if (itemId && !expectedItemId(itemId, contract.prefix)) {
        issues.push(this.#issue('item_id_prefix_mismatch', `${itemType} 的 item ID 前缀与 contract 不一致`, ['event', 'response', 'output', outputIndex, 'id'], outputIndex, itemType, 'R0'))
      }
    }
    if (itemId) {
      const itemIdToken = identityToken(itemId)
      this.#observedItemIds.add(itemIdToken)
      const priorIndex = completedItemIdOwners.get(itemIdToken)
      if (priorIndex !== undefined && priorIndex !== outputIndex) {
        issues.push(this.#issue('duplicate_item_identity', 'completed output 的多个 item 使用了重复 ID', ['event', 'response', 'output', outputIndex, 'id'], outputIndex, itemType, 'R2'))
      } else {
        completedItemIdOwners.set(itemIdToken, outputIndex)
      }
    }
    if (
      !issues.some((value) => value.repairLevel === 'R2')
      && issues.some((value) => value.repairLevel === 'R0')
    ) {
      const clientItemId = previous?.clientItemId
        ?? (this.#allowNewRepair ? this.#newClientItemId(contract, outputIndex) : undefined)
      if (clientItemId) {
        this.#eventRepairs.push({
          outputIndex,
          itemType: contract.type,
          field: 'response.output.id',
          clientItemId
        })
      }
    }
    return issues
  }

  #consumeItemEvent(
    responseResourceId: string,
    eventType: string,
    event: JsonRecord,
    stage: CodexResponseItemEventStage
  ): CodexStreamContractEventResult {
    const outputIndex = nonNegativeInteger(event.output_index)
    if (Object.hasOwn(event, 'output_index') && outputIndex === undefined) {
      return this.#result([this.#issue('event_output_index_invalid', 'Responses item event 的 output_index 必须是非负整数', ['event', 'output_index'], undefined, undefined, 'R2')])
    }
    const item = plainObject(event.item)
    if (stage !== 'delta' && !item) {
      return this.#result([this.#issue('event_item_missing', 'output_item added/done 必须包含 item 对象', ['event', 'item'], outputIndex, undefined, 'R2')])
    }

    const expectedDeltaType = stage === 'delta' ? deltaItemType(eventType) : undefined
    const itemId = stringValue(item?.id) ?? stringValue(event.item_id)
    const itemType = nonEmptyString(item?.type) ?? nonEmptyString(event.item_type) ?? expectedDeltaType
    const callId = stringValue(item?.call_id) ?? stringValue(event.call_id)
    const scope = responseScope(responseResourceId)
    const existingOwnerKey = itemId ? this.#itemIdOwners.get(itemIdKey(scope, itemId)) : undefined
    const internalKey = outputIndex !== undefined
      ? itemKey(scope, outputIndex)
      : stage === 'delta' && existingOwnerKey
        ? existingOwnerKey
        : standaloneItemKey(scope, ++this.#standaloneSequence)
    const previous = this.#identities.get(internalKey)
    const issues: CodexProtocolIssue[] = []

    issues.push(...this.#stageIssues(previous, stage, outputIndex, itemType))
    if (stage === 'delta' && !expectedDeltaType) {
      issues.push(this.#issue(
        'unknown_delta_event_type',
        'Responses stream 出现 registry 未识别的 delta event',
        ['event', 'type'],
        outputIndex,
        previous?.itemType
      ))
    }
    if (expectedDeltaType && previous?.itemType && previous.itemType !== identityToken(expectedDeltaType)) {
      issues.push(this.#issue('event_delta_type_mismatch', `${eventType} 不能用于 ${previous.itemType}`, ['event', 'type'], outputIndex, previous.itemType, 'R2'))
    }
    if (previous?.itemId !== undefined && itemId !== undefined && previous.itemId !== identityToken(itemId)) {
      issues.push(this.#issue('event_item_id_inconsistent', '同一 Responses output identity 的 item ID 在事件间发生变化', itemFieldPath(stage, 'id'), outputIndex, itemType ?? previous.itemType, 'R2'))
    }
    if (previous?.itemType && itemType && previous.itemType !== identityToken(itemType)) {
      issues.push(this.#issue('event_item_type_inconsistent', '同一 Responses output identity 的 item type 在事件间发生变化', itemFieldPath(stage, 'type'), outputIndex, itemType, 'R2'))
    }
    if (previous?.callId !== undefined && callId !== undefined && previous.callId !== identityToken(callId)) {
      issues.push(this.#issue('event_call_id_inconsistent', '同一 Responses output identity 的 call_id 在事件间发生变化', itemFieldPath(stage, 'call_id'), outputIndex, itemType ?? previous.itemType, 'R2'))
    }

    const effectiveType = itemType ?? previous?.itemType
    const effectiveId = itemId ?? previous?.itemId
    const contract = effectiveType ? codexResponsesContractRegistry.item(effectiveType) : undefined
    if (effectiveType && !contract) {
      issues.push(this.#issue('unknown_item_type', 'Responses stream 出现 registry 未知的 item type', itemFieldPath(stage, 'type'), outputIndex, effectiveType))
    } else if (contract) {
      if (!contract.eventStages.includes(stage)) {
        issues.push(this.#issue('event_stage_invalid', `${effectiveType} 不允许 ${stage} 阶段`, ['event', 'type'], outputIndex, effectiveType, 'R2'))
      }
      if (item && stage !== 'delta') {
        issues.push(...this.#itemContractIssues(item, contract, stage, outputIndex, ['event', 'item']))
      }
      if (Object.hasOwn(item ?? event, stage === 'delta' ? 'item_id' : 'id')) {
        const rawId = stage === 'delta' ? event.item_id : item?.id
        if (!contract.prefix || !contract.repairableIdPaths.includes('id')) {
          issues.push(this.#issue('item_id_forbidden', `${effectiveType} 不允许携带 item ID`, itemFieldPath(stage, 'id'), outputIndex, effectiveType, 'R2'))
        } else if (rawId !== null && (typeof rawId !== 'string' || rawId.length === 0)) {
          issues.push(this.#issue('item_id_invalid', `${effectiveType} 的 item ID 必须是字符串或 null`, itemFieldPath(stage, 'id'), outputIndex, effectiveType, 'R0'))
        } else if (effectiveId && !expectedItemId(effectiveId, contract.prefix)) {
          issues.push(this.#issue('item_id_prefix_mismatch', `${effectiveType} 的 item ID 前缀与 contract 不一致`, itemFieldPath(stage, 'id'), outputIndex, effectiveType, 'R0'))
        }
      }
    }

    if (effectiveId) {
      this.#observedItemIds.add(identityToken(effectiveId))
      const ownerKey = this.#itemIdOwners.get(itemIdKey(scope, effectiveId))
      if (ownerKey && ownerKey !== internalKey) {
        issues.push(this.#issue('duplicate_item_identity', '同一 Responses 流的多个 output index 使用了重复 item ID', itemFieldPath(stage, 'id'), outputIndex, effectiveType, 'R2'))
      }
    }

    let clientItemId = previous?.clientItemId
    if (
      contract
      && !issues.some((value) => value.repairLevel === 'R2')
      && issues.some((value) => value.repairLevel === 'R0')
    ) {
      clientItemId = clientItemId
        ?? (this.#allowNewRepair ? this.#newClientItemId(contract, outputIndex) : undefined)
      if (clientItemId) {
        this.#eventRepairs.push({
          outputIndex,
          itemType: contract.type,
          field: stage === 'delta' ? 'item_id' : 'item.id',
          clientItemId
        })
      }
    }

    if (!issues.some((value) => value.repairLevel === 'R2')) {
      const identity: CodexStreamItemIdentity = {
        itemId: effectiveId ? identityToken(effectiveId) : undefined,
        upstreamItemId: effectiveId ? identityToken(effectiveId) : undefined,
        clientItemId,
        itemType: effectiveType ? identityToken(effectiveType) : undefined,
        callId: callId ? identityToken(callId) : previous?.callId,
        outputIndex: outputIndex ?? -1,
        stage
      }
      this.#identities.set(internalKey, identity)
      if (identity.itemId) {
        const ownerIdKey = itemIdKey(scope, identity.itemId)
        if (!this.#itemIdOwners.has(ownerIdKey)) this.#itemIdOwners.set(ownerIdKey, internalKey)
      }
    }
    return this.#result(issues)
  }

  #newClientItemId(contract: CodexItemContract, outputIndex: number | undefined): string | undefined {
    if (!this.#repairItemIds || !contract.prefix) return undefined
    for (let attempt = 0; attempt < 100; attempt += 1) {
      const sequence = this.#repairSequence++
      const candidate = (this.#createItemId ?? defaultStreamItemIdFactory)({
        prefix: contract.prefix,
        type: contract.type,
        sequence,
        outputIndex: outputIndex ?? Math.max(0, this.#standaloneSequence - 1)
      })
      if (
        expectedItemId(candidate, contract.prefix)
        && !this.#clientItemIds.has(candidate)
        && !this.#observedItemIds.has(identityToken(candidate))
      ) {
        this.#clientItemIds.add(candidate)
        return candidate
      }
    }
    return undefined
  }

  #stageIssues(
    previous: CodexStreamItemIdentity | undefined,
    stage: CodexResponseItemEventStage,
    outputIndex: number | undefined,
    itemType: string | undefined
  ): CodexProtocolIssue[] {
    if (!previous) return []
    if (previous.stage === 'done') {
      return [this.#issue('event_stage_inconsistent', `output identity 已 done，不能再次进入 ${stage}`, ['event', 'type'], outputIndex, itemType ?? previous.itemType, 'R2')]
    }
    if (stage === 'added') {
      return [this.#issue('event_stage_inconsistent', '同一 output identity 重复 added', ['event', 'type'], outputIndex, itemType ?? previous.itemType, 'R2')]
    }
    return []
  }

  #itemContractIssues(
    item: JsonRecord,
    contract: CodexItemContract,
    stage: CodexResponseItemEventStage,
    outputIndex: number | undefined,
    basePath: readonly (string | number)[]
  ): CodexProtocolIssue[] {
    return validateCodexItemContractFields(item, contract).map((fieldIssue) => this.#issue(
      fieldIssue.code,
      `${contract.type}.${fieldIssue.field} 不满足 Codex Responses contract`,
      [...basePath, fieldIssue.field],
      outputIndex,
      contract.type,
      'R2'
    ))
  }

  #result(issues: readonly CodexProtocolIssue[]): CodexStreamContractEventResult {
    this.#recordDiagnostics(issues)
    const boundedIssues = issues.slice(0, codexStreamContractDiagnosticLimit)
    return {
      revision: codexResponsesContractRevision,
      outcome: outcomeFor(issues),
      issue: boundedIssues[0],
      issues: boundedIssues,
      repairs: this.#takeEventRepairs(),
      eventCategory: 'protocol_event'
    }
  }

  #takeEventRepairs(): CodexStreamIdRepair[] {
    const repairs = this.#eventRepairs.map((repair) => ({ ...repair }))
    this.#eventRepairs.length = 0
    return repairs
  }

  #recordDiagnostics(issues: readonly CodexProtocolIssue[]): void {
    for (const issue of issues) {
      if (this.#diagnostics.length < codexStreamContractDiagnosticLimit) this.#diagnostics.push(cloneIssue(issue))
      else this.#omittedDiagnosticCount += 1
    }
  }

  #issue(
    code: string,
    message: string,
    path: readonly (string | number)[],
    outputIndex?: number,
    itemType?: string,
    repairLevel?: 'R0' | 'R2'
  ): CodexProtocolIssue {
    return {
      code: boundedDiagnosticString(code, 96),
      message: boundedDiagnosticString(message, 256),
      path: path.slice(0, 12).map((part) => typeof part === 'string' ? boundedDiagnosticString(part, 64) : part),
      provenance: this.#provenance,
      outputIndex,
      itemType: itemType === undefined ? undefined : boundedDiagnosticString(itemType, 128),
      repairLevel
    }
  }
}

export function createCodexResponsesStreamContractState(input: {
  provenance: CodexProtocolIssueProvenance
  repairItemIds?: boolean
  createItemId?: (input: { prefix: string; type: string; sequence: number; outputIndex: number }) => string
}): CodexResponsesStreamContractState {
  return new CodexResponsesStreamContractState(input)
}

function cleanResult(eventCategory: 'sse_comment' | 'protocol_event'): CodexStreamContractEventResult {
  return { revision: codexResponsesContractRevision, outcome: 'clean', issues: [], repairs: [], eventCategory }
}

function outcomeFor(issues: readonly CodexProtocolIssue[]): CodexContractOutcome {
  if (issues.length === 0) return 'clean'
  if (issues.some((value) => value.repairLevel === 'R2')) return 'blocked'
  if (issues.some((value) => value.repairLevel === 'R0')) return 'repairable'
  return 'observed_unknown'
}

function eventStage(eventType: string): CodexResponseItemEventStage | undefined {
  if (eventType === 'response.output_item.added') return 'added'
  if (eventType === 'response.output_item.done') return 'done'
  if (eventType.startsWith('response.') && eventType.endsWith('.delta')) return 'delta'
  return undefined
}

function deltaItemType(eventType: string): string | undefined {
  if (eventType === 'response.output_text.delta') return 'message'
  if (eventType === 'response.function_call_arguments.delta') return 'function_call'
  if (eventType === 'response.custom_tool_call_input.delta') return 'custom_tool_call'
  if (eventType === 'response.reasoning_summary_text.delta' || eventType === 'response.reasoning_text.delta') return 'reasoning'
  return undefined
}

function itemFieldPath(stage: CodexResponseItemEventStage, field: string): readonly (string | number)[] {
  if (stage !== 'delta') return ['event', 'item', field]
  if (field === 'id') return ['event', 'item_id']
  if (field === 'type') return ['event', 'item_type']
  return ['event', field]
}

function itemKey(responseResourceId: string, outputIndex: number): string {
  return `${responseResourceId.length}:${responseResourceId}:${outputIndex}`
}

function standaloneItemKey(responseResourceId: string, sequence: number): string {
  return `${responseResourceId.length}:${responseResourceId}:standalone:${sequence}`
}

function responseScope(responseResourceId: string): string {
  const value = nonEmptyString(responseResourceId)
  return value ? identityToken(value) : '<unscoped>'
}

function itemIdKey(responseResourceId: string, itemId: string): string {
  const token = identityToken(itemId)
  return `${responseResourceId.length}:${responseResourceId}:${token}`
}

function identityToken(value: string): string {
  if (value.length <= 256) return value
  return `sha256:${createHash('sha256').update(value).digest('hex')}`
}

function boundedDiagnosticString(value: string, maximumLength: number): string {
  return value.length <= maximumLength ? value : value.slice(0, maximumLength)
}

function defaultStreamItemIdFactory(input: { prefix: string }): string {
  return `${input.prefix}_${randomUUID()}`
}

function expectedItemId(itemId: string, prefix: string): boolean {
  return itemId.startsWith(`${prefix}_`) && itemId.length > prefix.length + 1
}

function plainObject(value: unknown): JsonRecord | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as JsonRecord : undefined
}

function nonEmptyString(value: unknown): string | undefined {
  return typeof value === 'string' && value.length > 0 ? value : undefined
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' ? value : undefined
}

function nonNegativeInteger(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : undefined
}

function cloneIssue(issue: CodexProtocolIssue): CodexProtocolIssue {
  return { ...issue, path: [...issue.path] }
}
