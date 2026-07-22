import assert from 'node:assert/strict'

import { codexResponsesContractRevision } from '../../modules/gateway/codex-responses/contract-registry.js'
import { validateCodexResponsesJson } from '../../modules/gateway/codex-responses/contract-validator.js'
import { planCodexResponsesJsonRepair } from '../../modules/gateway/codex-responses/repair-planner.js'
import { executeCodexResponsesRepair } from '../../modules/gateway/codex-responses/repair-executor.js'

type JsonRecord = Record<string, unknown>

const mismatchedResponse: JsonRecord = {
  id: 'resp_contract_json',
  object: 'response',
  output: [{
    id: 'fc_wrong_custom',
    type: 'custom_tool_call',
    status: 'completed',
    call_id: 'call_custom',
    name: 'apply_patch',
    input: '*** Begin Patch\n*** End Patch\n',
    output_index: 7
  }]
}
const mismatchedBefore = structuredClone(mismatchedResponse)
const mismatchValidation = validateCodexResponsesJson({
  response: mismatchedResponse,
  provenance: 'raw_upstream',
  revision: codexResponsesContractRevision
})
assert.deepEqual(mismatchValidation.issues.map((issue) => issue.code), ['item_id_prefix_mismatch'])
assert.equal(mismatchValidation.outcome, 'repairable')
const mismatchPlan = planCodexResponsesJsonRepair({
  document: mismatchedResponse,
  validation: mismatchValidation,
  downstreamExposed: false,
  createItemId: ({ prefix, sequence }) => `${prefix}_generated_${sequence}`
})
assert.equal(mismatchPlan.level, 'R0')
const repairedMismatch = executeCodexResponsesRepair(mismatchedResponse, mismatchPlan)
const repairedMismatchItem = (repairedMismatch.output as JsonRecord[])[0] as JsonRecord
assert.equal(repairedMismatchItem.id, 'ctc_generated_1')
assert.equal(repairedMismatchItem.call_id, 'call_custom')
assert.equal(repairedMismatchItem.output_index, 7)
assert.deepEqual(semanticProjection(repairedMismatch), semanticProjection(mismatchedBefore))
assert.deepEqual(mismatchedResponse, mismatchedBefore, 'executor 不得原地修改原始响应')

const duplicateResponse: JsonRecord = {
  output: [
    { id: 'msg_duplicate', type: 'message', role: 'assistant', content: [], output_index: 0 },
    { id: 'msg_duplicate', type: 'message', role: 'assistant', content: [], output_index: 1 }
  ]
}
const duplicateValidation = validateCodexResponsesJson({
  response: duplicateResponse,
  provenance: 'gateway_bridge',
  revision: codexResponsesContractRevision
})
assert.deepEqual(duplicateValidation.issues.map((issue) => issue.code), ['duplicate_item_identity'])
const duplicatePlan = planCodexResponsesJsonRepair({
  document: duplicateResponse,
  validation: duplicateValidation,
  downstreamExposed: false,
  createItemId: ({ prefix, sequence }) => `${prefix}_deduplicated_${sequence}`
})
const repairedDuplicate = executeCodexResponsesRepair(duplicateResponse, duplicatePlan)
assert.deepEqual(
  (repairedDuplicate.output as JsonRecord[]).map((item) => item.id),
  ['msg_duplicate', 'msg_deduplicated_1']
)
assert.deepEqual(
  (repairedDuplicate.output as JsonRecord[]).map((item) => item.output_index),
  [0, 1],
  '去重不得改变 output_index 或 item 顺序'
)

const unknownValidation = validateCodexResponsesJson({
  response: { output: [{ id: 'future_1', type: 'future_response_item', payload: 'opaque' }] },
  provenance: 'raw_upstream',
  revision: codexResponsesContractRevision
})
assert.equal(unknownValidation.outcome, 'observed_unknown')
assert.deepEqual(unknownValidation.issues.map((issue) => issue.code), ['unknown_item_type'])

const malformedKnownValidation = validateCodexResponsesJson({
  response: {
    output: [{ id: 'ctc_missing_payload', type: 'custom_tool_call', status: 'completed' }]
  },
  provenance: 'raw_upstream',
  revision: codexResponsesContractRevision
})
assert.equal(malformedKnownValidation.outcome, 'blocked')
assert.deepEqual(
  malformedKnownValidation.issues.map((issue) => issue.code),
  ['item_required_field_invalid', 'item_required_field_invalid', 'item_required_field_invalid']
)
assert.deepEqual(
  malformedKnownValidation.issues.map((issue) => issue.path.at(-1)),
  ['call_id', 'name', 'input']
)

const orphanDocument: JsonRecord = {
  input: [{ type: 'function_call_output', id: 'fco_orphan', call_id: 'call_missing', output: 'result' }]
}
const orphanValidation = validateCodexResponsesJson({
  response: orphanDocument,
  provenance: 'request_history',
  revision: codexResponsesContractRevision
})
assert.deepEqual(orphanValidation.issues.map((issue) => issue.code), ['orphan_tool_output'])
assert.equal(orphanValidation.outcome, 'blocked')
const orphanPlan = planCodexResponsesJsonRepair({
  document: orphanDocument,
  validation: orphanValidation,
  downstreamExposed: false
})
assert.equal(orphanPlan.level, 'R2')
assert.throws(() => executeCodexResponsesRepair(orphanDocument, orphanPlan), /codex_repair_forbidden/)

const externalOutputDocument: JsonRecord = {
  previous_response_id: 'resp_server_state',
  input: [{ type: 'function_call_output', id: 'fco_external', call_id: 'call_external', output: 'result' }]
}
assert.equal(validateCodexResponsesJson({
  response: externalOutputDocument,
  provenance: 'request_history',
  revision: codexResponsesContractRevision
}).outcome, 'clean', 'previous_response_id 存在时 output 可以引用服务端历史 call')

const mismatchedToolGraphValidation = validateCodexResponsesJson({
  response: {
    input: [
      { type: 'custom_tool_call', id: 'ctc_graph', call_id: 'call_graph', name: 'apply_patch', input: 'patch' },
      { type: 'function_call_output', id: 'fco_graph', call_id: 'call_graph', output: 'done' }
    ]
  },
  provenance: 'request_history',
  revision: codexResponsesContractRevision
})
assert.deepEqual(mismatchedToolGraphValidation.issues.map((issue) => issue.code), ['tool_call_type_mismatch'])
assert.equal(mismatchedToolGraphValidation.outcome, 'blocked')

const duplicateToolOutputValidation = validateCodexResponsesJson({
  response: {
    input: [
      { type: 'function_call', id: 'fc_graph', call_id: 'call_duplicate_output', name: 'run', arguments: '{}' },
      { type: 'function_call_output', id: 'fco_graph_a', call_id: 'call_duplicate_output', output: 'first' },
      { type: 'function_call_output', id: 'fco_graph_b', call_id: 'call_duplicate_output', output: 'second' }
    ]
  },
  provenance: 'request_history',
  revision: codexResponsesContractRevision
})
assert.deepEqual(duplicateToolOutputValidation.issues.map((issue) => issue.code), ['duplicate_tool_output'])
assert.equal(duplicateToolOutputValidation.outcome, 'blocked')

const replayableHistory: JsonRecord = {
  input: [{
    type: 'custom_tool_call',
    id: 'fc_legacy_custom',
    call_id: 'call_history',
    name: 'apply_patch',
    input: 'patch body'
  }]
}
const historyBefore = structuredClone(replayableHistory)
const historyValidation = validateCodexResponsesJson({
  response: replayableHistory,
  provenance: 'request_history',
  revision: codexResponsesContractRevision
})
const historyPlan = planCodexResponsesJsonRepair({
  document: replayableHistory,
  validation: historyValidation,
  downstreamExposed: false
})
const repairedHistory = executeCodexResponsesRepair(replayableHistory, historyPlan)
assert.equal(Object.hasOwn((repairedHistory.input as JsonRecord[])[0] as object, 'id'), false)
assert.equal(((repairedHistory.input as JsonRecord[])[0] as JsonRecord).call_id, 'call_history')
assert.deepEqual(semanticProjection(repairedHistory), semanticProjection(historyBefore))

const unrecoverableHistory: JsonRecord = {
  input: [{ type: 'reasoning', id: 'fc_wrong_reasoning' }]
}
const unrecoverableValidation = validateCodexResponsesJson({
  response: unrecoverableHistory,
  provenance: 'request_history',
  revision: codexResponsesContractRevision
})
const unrecoverablePlan = planCodexResponsesJsonRepair({
  document: unrecoverableHistory,
  validation: unrecoverableValidation,
  downstreamExposed: false
})
assert.throws(
  () => executeCodexResponsesRepair(unrecoverableHistory, unrecoverablePlan),
  /codex_history_item_unrecoverable/
)

const exposedPlan = planCodexResponsesJsonRepair({
  document: mismatchedResponse,
  validation: mismatchValidation,
  downstreamExposed: true
})
assert.equal(exposedPlan.level, 'R2', '已经暴露给下游的 item ID 禁止重写')

console.log('Codex Responses JSON contract 回归通过：诊断、R0、未知类型、orphan、历史自愈和语义不变量已固定')

function semanticProjection(document: JsonRecord): unknown {
  const copy = structuredClone(document)
  for (const field of ['input', 'output'] as const) {
    const items = Array.isArray(copy[field]) ? copy[field] as JsonRecord[] : []
    for (const item of items) delete item.id
  }
  return copy
}
