import assert from 'node:assert/strict'

import {
  codexStreamContractDiagnosticLimit,
  createCodexResponsesStreamContractState
} from '../../modules/gateway/codex-responses/stream-contract-state.js'

type JsonRecord = Record<string, unknown>

const responseResourceId = 'resp_contract_sse'
const added = outputItemEvent('added', 0, customToolItem())
const delta: JsonRecord = {
  type: 'response.custom_tool_call_input.delta',
  output_index: 0,
  item_id: 'ctc_stream_0',
  call_id: 'call_stream_0',
  delta: '{"path":'
}
const done = outputItemEvent('done', 0, customToolItem({ input: '{"path":"README.md"}' }))
const completed = completedEvent([customToolItem({ input: '{"path":"README.md"}' })])

const lifecycle = createCodexResponsesStreamContractState({ provenance: 'raw_upstream' })
for (const [stage, event] of [
  ['added', added],
  ['delta', delta],
  ['done', done],
  ['completed', completed]
] as const) {
  const result = lifecycle.consume({ responseResourceId, event })
  assert.equal(result.outcome, 'clean', `${stage} 应保持同一 output identity`)
  assert.equal(result.eventCategory, 'protocol_event')
}
assert.deepEqual(lifecycle.identityFor(responseResourceId, 0), {
  itemId: 'ctc_stream_0',
  upstreamItemId: 'ctc_stream_0',
  clientItemId: undefined,
  itemType: 'custom_tool_call',
  callId: 'call_stream_0',
  outputIndex: 0,
  stage: 'done'
})

const changedIdGuard = guardWithAdded()
const changedIdDelta = changedIdGuard.consume({
  responseResourceId,
  event: { ...delta, item_id: 'ctc_stream_changed' }
})
assert.equal(changedIdDelta.outcome, 'blocked')
assert.equal(changedIdDelta.issue?.code, 'event_item_id_inconsistent')
assert.equal(changedIdGuard.identityFor(responseResourceId, 0)?.itemId, 'ctc_stream_0')

const changedTypeGuard = guardWithAdded()
const changedTypeDone = changedTypeGuard.consume({
  responseResourceId,
  event: outputItemEvent('done', 0, { ...customToolItem(), type: 'function_call' })
})
assert.equal(changedTypeDone.outcome, 'blocked')
assert.ok(changedTypeDone.issues.some((issue) => issue.code === 'event_item_type_inconsistent'))

const changedCallGuard = guardWithAdded()
const changedCallDone = changedCallGuard.consume({
  responseResourceId,
  event: outputItemEvent('done', 0, customToolItem({ call_id: 'call_stream_changed' }))
})
assert.equal(changedCallDone.outcome, 'blocked')
assert.ok(changedCallDone.issues.some((issue) => issue.code === 'event_call_id_inconsistent'))

const duplicateGuard = guardWithAdded()
const duplicateOtherIndex = duplicateGuard.consume({
  responseResourceId,
  event: outputItemEvent('added', 1, customToolItem())
})
assert.equal(duplicateOtherIndex.outcome, 'blocked', '一个原始 ID 对应多个流式 item 时不得自动消歧')
assert.equal(duplicateOtherIndex.issue?.code, 'duplicate_item_identity')

const separateResponseGuard = guardWithAdded()
const sameIdInSeparateResponse = separateResponseGuard.consume({
  responseResourceId: 'resp_contract_sse_other',
  event: outputItemEvent('added', 0, customToolItem())
})
assert.equal(sameIdInSeparateResponse.outcome, 'clean', 'item ID 去重作用域必须包含 response resource ID')

const wrongPrefixGuard = createCodexResponsesStreamContractState({ provenance: 'gateway_bridge' })
const wrongPrefix = wrongPrefixGuard.consume({
  responseResourceId,
  event: outputItemEvent('added', 0, customToolItem({ id: 'fc_wrong_custom' }))
})
assert.equal(wrongPrefix.outcome, 'repairable')
assert.equal(wrongPrefix.issue?.code, 'item_id_prefix_mismatch')
assert.equal(wrongPrefix.issue?.provenance, 'gateway_bridge')

const repairGuard = createCodexResponsesStreamContractState({
  provenance: 'gateway_bridge',
  repairItemIds: true,
  createItemId: ({ prefix }) => `${prefix}_client_stable`
})
const repairAdded = repairGuard.consume({
  responseResourceId,
  event: outputItemEvent('added', 0, customToolItem({ id: 'fc_wrong_custom' }))
})
assert.equal(repairAdded.outcome, 'repairable')
assert.equal(repairAdded.repairs[0]?.clientItemId, 'ctc_client_stable')
assert.equal(repairAdded.repairs[0]?.field, 'item.id')
assert.equal(repairGuard.identityFor(responseResourceId, 0)?.upstreamItemId, 'fc_wrong_custom')
assert.equal(repairGuard.identityFor(responseResourceId, 0)?.clientItemId, 'ctc_client_stable')
const repairDelta = repairGuard.consume({
  responseResourceId,
  event: { ...delta, item_id: 'fc_wrong_custom' }
})
assert.equal(repairDelta.repairs[0]?.clientItemId, 'ctc_client_stable')
assert.equal(repairDelta.repairs[0]?.field, 'item_id')
const repairDone = repairGuard.consume({
  responseResourceId,
  event: outputItemEvent('done', 0, customToolItem({ id: 'fc_wrong_custom' }))
})
assert.equal(repairDone.repairs[0]?.clientItemId, 'ctc_client_stable')
const repairCompleted = repairGuard.consume({
  responseResourceId,
  event: completedEvent([customToolItem({ id: 'fc_wrong_custom' })])
})
assert.equal(repairCompleted.repairs[0]?.clientItemId, 'ctc_client_stable')
assert.equal(repairCompleted.repairs[0]?.field, 'response.output.id')

const unknownGuard = createCodexResponsesStreamContractState({ provenance: 'raw_upstream' })
const unknown = unknownGuard.consume({
  responseResourceId,
  event: outputItemEvent('added', 4, { id: 'future_stream_4', type: 'future_response_item', payload: 'opaque' })
})
assert.equal(unknown.outcome, 'observed_unknown')
assert.equal(unknown.issue?.code, 'unknown_item_type')

const malformedKnown = createCodexResponsesStreamContractState({ provenance: 'raw_upstream' }).consume({
  responseResourceId,
  event: outputItemEvent('added', 0, { id: 'ctc_missing_name', type: 'custom_tool_call', call_id: 'call_missing_name', input: '' })
})
assert.equal(malformedKnown.outcome, 'blocked')
assert.ok(malformedKnown.issues.some((issue) => issue.code === 'item_required_field_invalid' && issue.path.at(-1) === 'name'))

const triggerAdded = createCodexResponsesStreamContractState({ provenance: 'raw_upstream' }).consume({
  responseResourceId,
  event: outputItemEvent('added', 0, { type: 'compaction_trigger' })
})
assert.equal(triggerAdded.outcome, 'blocked')
assert.ok(triggerAdded.issues.some((issue) => issue.code === 'event_stage_invalid'))

const invalidDeltaStageGuard = createCodexResponsesStreamContractState({ provenance: 'raw_upstream' })
assert.equal(invalidDeltaStageGuard.consume({
  responseResourceId,
  event: outputItemEvent('added', 0, { id: 'at_stream', type: 'additional_tools', role: 'assistant', tools: [] })
}).outcome, 'clean')
const invalidAdditionalToolsDelta = invalidDeltaStageGuard.consume({
  responseResourceId,
  event: { type: 'response.additional_tools.delta', output_index: 0, item_id: 'at_stream', delta: 'ignored' }
})
assert.equal(invalidAdditionalToolsDelta.outcome, 'blocked')
assert.ok(invalidAdditionalToolsDelta.issues.some((issue) => issue.code === 'event_stage_invalid'))

const duplicateAddedGuard = guardWithAdded()
const duplicateAdded = duplicateAddedGuard.consume({ responseResourceId, event: added })
assert.equal(duplicateAdded.outcome, 'blocked')
assert.ok(duplicateAdded.issues.some((issue) => issue.code === 'event_stage_inconsistent'))

const doneThenDeltaGuard = createCodexResponsesStreamContractState({ provenance: 'raw_upstream' })
assert.equal(doneThenDeltaGuard.consume({ responseResourceId, event: done }).outcome, 'clean', 'Codex 允许没有 added 的 standalone done')
const afterDoneDelta = doneThenDeltaGuard.consume({ responseResourceId, event: delta })
assert.equal(afterDoneDelta.outcome, 'blocked')
assert.ok(afterDoneDelta.issues.some((issue) => issue.code === 'event_stage_inconsistent'))

const deltaBeforeAdded = createCodexResponsesStreamContractState({ provenance: 'raw_upstream' }).consume({
  responseResourceId,
  event: delta
})
assert.equal(deltaBeforeAdded.outcome, 'clean', 'Codex parser accepts tool delta without a preceding added event')

const standaloneDoneWithoutResourceOrIndex = createCodexResponsesStreamContractState({ provenance: 'raw_upstream' }).consume({
  responseResourceId: '',
  event: { type: 'response.output_item.done', item: customToolItem() }
})
assert.equal(standaloneDoneWithoutResourceOrIndex.outcome, 'clean', 'Codex wire parser does not require response.created or output_index')

const completedIdentityMismatchGuard = guardWithAdded()
const completedIdentityMismatch = completedIdentityMismatchGuard.consume({
  responseResourceId,
  event: completedEvent([customToolItem({ id: 'ctc_completed_changed' })])
})
assert.equal(completedIdentityMismatch.outcome, 'blocked')
assert.ok(completedIdentityMismatch.issues.some((issue) => issue.code === 'event_item_id_inconsistent'))

const completedResourceMismatch = createCodexResponsesStreamContractState({ provenance: 'raw_upstream' }).consume({
  responseResourceId,
  event: { type: 'response.completed', response: { id: 'resp_changed_at_completion' } }
})
assert.equal(completedResourceMismatch.outcome, 'blocked')
assert.equal(completedResourceMismatch.issue?.code, 'response_resource_id_inconsistent')

const completedPrefixMismatch = createCodexResponsesStreamContractState({ provenance: 'raw_upstream' }).consume({
  responseResourceId,
  event: completedEvent([customToolItem({ id: 'fc_wrong_completed' })])
})
assert.equal(completedPrefixMismatch.outcome, 'repairable')
assert.ok(completedPrefixMismatch.issues.some((issue) => issue.code === 'item_id_prefix_mismatch'))

const completedDuplicateId = createCodexResponsesStreamContractState({ provenance: 'raw_upstream' }).consume({
  responseResourceId,
  event: completedEvent([
    { id: 'msg_duplicate', type: 'message', role: 'assistant', content: [] },
    { id: 'msg_duplicate', type: 'message', role: 'assistant', content: [] }
  ])
})
assert.equal(completedDuplicateId.outcome, 'blocked')
assert.ok(completedDuplicateId.issues.some((issue) => issue.code === 'duplicate_item_identity'))

const completedTrigger = createCodexResponsesStreamContractState({ provenance: 'raw_upstream' }).consume({
  responseResourceId,
  event: completedEvent([{ type: 'compaction_trigger' }])
})
assert.equal(completedTrigger.outcome, 'blocked')
assert.ok(completedTrigger.issues.some((issue) => issue.code === 'event_stage_invalid'))

const completedUnknownMismatchGuard = guardWithAdded()
const completedUnknownMismatch = completedUnknownMismatchGuard.consume({
  responseResourceId,
  event: completedEvent([{ id: 'ctc_stream_0', type: 'future_item' }])
})
assert.equal(completedUnknownMismatch.outcome, 'blocked')
assert.ok(completedUnknownMismatch.issues.some((issue) => issue.code === 'event_item_type_inconsistent'))

const terminalGuard = createCodexResponsesStreamContractState({ provenance: 'raw_upstream' })
assert.equal(terminalGuard.consume({ responseResourceId, event: completedEvent([]) }).outcome, 'clean')
assert.equal(terminalGuard.consume({ responseResourceId, event: done }).outcome, 'blocked')
assert.equal(terminalGuard.consume({ responseResourceId, event: completedEvent([]) }).outcome, 'blocked')

const unknownDeltaGuard = guardWithAdded()
const unknownDelta = unknownDeltaGuard.consume({
  responseResourceId,
  event: { type: 'response.typo.delta', output_index: 0, item_id: 'ctc_stream_0', delta: 'x' }
})
assert.equal(unknownDelta.outcome, 'observed_unknown')
assert.ok(unknownDelta.issues.some((issue) => issue.code === 'unknown_delta_event_type'))

const longIdentityGuard = createCodexResponsesStreamContractState({ provenance: 'raw_upstream' })
const longIdentity = `ctc_${'x'.repeat(16 * 1024)}`
assert.equal(longIdentityGuard.consume({
  responseResourceId: `resp_${'r'.repeat(16 * 1024)}`,
  event: outputItemEvent('added', 0, customToolItem({ id: longIdentity, call_id: `call_${'c'.repeat(16 * 1024)}` }))
}).outcome, 'clean')
const retainedLongIdentity = longIdentityGuard.identityFor(`resp_${'r'.repeat(16 * 1024)}`, 0)
assert.match(retainedLongIdentity?.itemId ?? '', /^sha256:/)
assert.equal(JSON.stringify(longIdentityGuard.snapshot()).includes(longIdentity), false)

const heartbeatGuard = createCodexResponsesStreamContractState({ provenance: 'raw_upstream' })
const heartbeat = heartbeatGuard.consume({ kind: 'comment', comment: 'juhe-ai waiting for upstream capacity' })
assert.equal(heartbeat.outcome, 'clean')
assert.equal(heartbeat.eventCategory, 'sse_comment')
assert.equal(heartbeatGuard.snapshot().identityCount, 0)
assert.equal(heartbeatGuard.canTransparentRetry({ semanticCommitted: false }), true)
assert.equal(heartbeatGuard.canTransparentRetry({ semanticCommitted: true }), false)

const boundedGuard = createCodexResponsesStreamContractState({ provenance: 'raw_upstream' })
const largeBodyMarker = 'body-must-not-be-retained-'.repeat(4096)
boundedGuard.consume({
  responseResourceId,
  event: outputItemEvent('added', 0, customToolItem({ input: largeBodyMarker }))
})
for (let sequence = 0; sequence < codexStreamContractDiagnosticLimit + 10; sequence += 1) {
  boundedGuard.consume({
    responseResourceId,
    event: { ...delta, item_id: `ctc_inconsistent_${sequence}` }
  })
}
const boundedSnapshot = boundedGuard.snapshot()
assert.equal(boundedSnapshot.identityCount, 1, '事件数量增长不得导致同一 identity 重复存储')
assert.equal(boundedSnapshot.diagnostics.length, codexStreamContractDiagnosticLimit)
assert.equal(boundedSnapshot.omittedDiagnosticCount, 10)
assert.equal(JSON.stringify(boundedSnapshot).includes(largeBodyMarker), false, '状态机不得保留正文或完整事件')
boundedGuard.dispose()
assert.equal(boundedGuard.snapshot().identityCount, 0)
assert.equal(boundedGuard.snapshot().itemIdOwnerCount, 0)
assert.equal(boundedGuard.snapshot().diagnostics.length, 0)
assert.equal(boundedGuard.snapshot().omittedDiagnosticCount, 0)

console.log('Codex Responses SSE contract 回归通过：增量身份、生命周期、字段契约、未知类型、heartbeat 与有界状态已固定')

function guardWithAdded() {
  const guard = createCodexResponsesStreamContractState({ provenance: 'raw_upstream' })
  assert.equal(guard.consume({ responseResourceId, event: added }).outcome, 'clean')
  return guard
}

function outputItemEvent(stage: 'added' | 'done', outputIndex: number, item: JsonRecord): JsonRecord {
  return { type: `response.output_item.${stage}`, output_index: outputIndex, item }
}

function completedEvent(output: JsonRecord[]): JsonRecord {
  return { type: 'response.completed', response: { id: responseResourceId, output } }
}

function customToolItem(overrides: JsonRecord = {}): JsonRecord {
  return {
    id: 'ctc_stream_0',
    type: 'custom_tool_call',
    call_id: 'call_stream_0',
    name: 'apply_patch',
    input: '',
    ...overrides
  }
}
