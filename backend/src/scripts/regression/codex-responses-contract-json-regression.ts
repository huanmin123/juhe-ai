import assert from 'node:assert/strict'

import { codexResponsesContractRevision } from '../../modules/gateway/codex-responses/contract-registry.js'
import { validateCodexResponsesJson } from '../../modules/gateway/codex-responses/contract-validator.js'
import { planCodexResponsesJsonRepair } from '../../modules/gateway/codex-responses/repair-planner.js'
import { executeCodexResponsesRepair } from '../../modules/gateway/codex-responses/repair-executor.js'
import type { CodexRepairPlan } from '../../modules/gateway/codex-responses/contract-types.js'

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
assert.equal(duplicateValidation.outcome, 'blocked', '一个原始 ID 对应多个 item 时不得自动消歧')
assert.equal(duplicatePlan.level, 'R2')
assert.throws(() => executeCodexResponsesRepair(duplicateResponse, duplicatePlan), /codex_repair_forbidden/)

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

const invalidToolOutputPayloadValidation = validateCodexResponsesJson({
  response: {
    previous_response_id: 'resp_payload_source',
    output: [{ id: 'fco_invalid_output', type: 'function_call_output', call_id: 'call_payload', output: null }]
  },
  provenance: 'raw_upstream',
  revision: codexResponsesContractRevision
})
assert.equal(invalidToolOutputPayloadValidation.outcome, 'blocked')
assert.deepEqual(invalidToolOutputPayloadValidation.issues.map((issue) => issue.code), ['item_required_field_invalid'])

const structuredToolOutputValidation = validateCodexResponsesJson({
  response: {
    previous_response_id: 'resp_payload_source',
    output: [{
      id: 'fco_structured_output',
      type: 'function_call_output',
      call_id: 'call_payload',
      output: [
        { type: 'input_text', text: '' },
        { type: 'input_image', image_url: 'data:image/png;base64,AA==', detail: null },
        { type: 'encrypted_content', encrypted_content: 'opaque' }
      ]
    }]
  },
  provenance: 'raw_upstream',
  revision: codexResponsesContractRevision
})
assert.equal(structuredToolOutputValidation.outcome, 'clean')

const malformedStructuredToolOutputValidation = validateCodexResponsesJson({
  response: {
    previous_response_id: 'resp_payload_source',
    output: [{
      id: 'ctco_structured_output',
      type: 'custom_tool_call_output',
      call_id: 'call_payload',
      output: [{ type: 'input_image', image_url: 42 }]
    }]
  },
  provenance: 'raw_upstream',
  revision: codexResponsesContractRevision
})
assert.equal(malformedStructuredToolOutputValidation.outcome, 'blocked')
assert.deepEqual(malformedStructuredToolOutputValidation.issues.map((issue) => issue.code), ['item_required_field_invalid'])

const nullableResponseIdValidation = validateCodexResponsesJson({
  response: { output: [{ id: null, type: 'message', role: 'assistant', content: [] }] },
  provenance: 'raw_upstream',
  revision: codexResponsesContractRevision
})
assert.equal(nullableResponseIdValidation.outcome, 'clean', 'ResponseItem id: Option 必须接受 null')

const missingHistoryFieldsValidation = validateCodexResponsesJson({
  response: { input: [{ id: 'msg_valid_id', type: 'message' }] },
  provenance: 'request_history',
  revision: codexResponsesContractRevision
})
assert.equal(missingHistoryFieldsValidation.outcome, 'blocked')
assert.deepEqual(
  missingHistoryFieldsValidation.issues.map((issue) => issue.code),
  ['item_required_field_invalid', 'item_required_field_invalid']
)

const nullableOptionValidation = validateCodexResponsesJson({
  response: { output: [{ id: 'cmp_nullable', type: 'context_compaction', encrypted_content: null }] },
  provenance: 'raw_upstream',
  revision: codexResponsesContractRevision
})
assert.equal(nullableOptionValidation.outcome, 'clean')
const invalidOptionValidation = validateCodexResponsesJson({
  response: { output: [{ id: 'cmp_invalid_optional', type: 'context_compaction', encrypted_content: 7 }] },
  provenance: 'raw_upstream',
  revision: codexResponsesContractRevision
})
assert.equal(invalidOptionValidation.outcome, 'blocked')
assert.deepEqual(invalidOptionValidation.issues.map((issue) => issue.code), ['item_optional_field_invalid'])

const invalidPhaseValidation = validateCodexResponsesJson({
  response: { output: [{ id: 'msg_phase', type: 'message', role: '', content: [], phase: 'unexpected' }] },
  provenance: 'raw_upstream',
  revision: codexResponsesContractRevision
})
assert.equal(invalidPhaseValidation.outcome, 'blocked', 'String 必需字段允许空，但已知 enum 仍需校验')
assert.deepEqual(invalidPhaseValidation.issues.map((issue) => issue.code), ['item_optional_field_invalid'])

const validLocalShellValidation = validateCodexResponsesJson({
  response: {
    output: [{
      id: 'lsh_action',
      type: 'local_shell_call',
      call_id: null,
      status: 'in_progress',
      action: { type: 'exec', command: [], timeout_ms: null, env: null }
    }]
  },
  provenance: 'raw_upstream',
  revision: codexResponsesContractRevision
})
assert.equal(validLocalShellValidation.outcome, 'clean')
const invalidLocalShellValidation = validateCodexResponsesJson({
  response: {
    output: [{
      id: 'lsh_bad_action',
      type: 'local_shell_call',
      status: 'running',
      action: { type: 'exec', command: 'not-an-array' }
    }]
  },
  provenance: 'raw_upstream',
  revision: codexResponsesContractRevision
})
assert.equal(invalidLocalShellValidation.outcome, 'blocked')
assert.deepEqual(
  invalidLocalShellValidation.issues.map((issue) => issue.code),
  ['item_required_field_invalid', 'item_required_field_invalid']
)
assert.deepEqual(
  malformedKnownValidation.issues.map((issue) => issue.path.at(-1)),
  ['call_id', 'name', 'input']
)

const compactionTriggerValidation = validateCodexResponsesJson({
  response: { output: [{ type: 'compaction_trigger' }] },
  provenance: 'raw_upstream',
  revision: codexResponsesContractRevision
})
assert.equal(compactionTriggerValidation.outcome, 'clean')
assert.deepEqual(compactionTriggerValidation.issues, [])
const invalidCompactionTriggerValidation = validateCodexResponsesJson({
  response: { output: [{ type: 'compaction_trigger', id: 'cmp_not_allowed' }] },
  provenance: 'raw_upstream',
  revision: codexResponsesContractRevision
})
assert.equal(invalidCompactionTriggerValidation.outcome, 'blocked')
assert.deepEqual(invalidCompactionTriggerValidation.issues.map((issue) => issue.code), ['item_id_forbidden'])

for (const invalidOutput of [{ type: 'message' }, 'not-an-array', null]) {
  const invalidCollection = validateCodexResponsesJson({
    response: { output: invalidOutput },
    provenance: 'raw_upstream',
    revision: codexResponsesContractRevision
  })
  assert.equal(invalidCollection.outcome, 'blocked')
  assert.deepEqual(invalidCollection.issues.map((issue) => issue.code), ['response_item_collection_invalid'])
}
const ambiguousCollection = validateCodexResponsesJson({
  response: { input: [], output: [] },
  provenance: 'raw_upstream',
  revision: codexResponsesContractRevision
})
assert.equal(ambiguousCollection.outcome, 'blocked')
assert.deepEqual(ambiguousCollection.issues.map((issue) => issue.code), ['response_item_collections_ambiguous'])

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

const duplicateToolCallValidation = validateCodexResponsesJson({
  response: {
    input: [
      { type: 'function_call', id: 'fc_duplicate_a', call_id: 'call_reused', name: 'a', arguments: '{}' },
      { type: 'function_call', id: 'fc_duplicate_b', call_id: 'call_reused', name: 'b', arguments: '{}' }
    ]
  },
  provenance: 'request_history',
  revision: codexResponsesContractRevision
})
assert.deepEqual(duplicateToolCallValidation.issues.map((issue) => issue.code), ['duplicate_tool_call_identity'])
assert.equal(duplicateToolCallValidation.outcome, 'blocked')

const crossTypeToolCallValidation = validateCodexResponsesJson({
  response: {
    input: [
      { type: 'function_call', id: 'fc_cross', call_id: 'call_cross', name: 'a', arguments: '{}' },
      { type: 'custom_tool_call', id: 'ctc_cross', call_id: 'call_cross', name: 'apply_patch', input: 'patch' }
    ]
  },
  provenance: 'request_history',
  revision: codexResponsesContractRevision
})
assert.deepEqual(crossTypeToolCallValidation.issues.map((issue) => issue.code), ['tool_call_type_mismatch'])
assert.equal(crossTypeToolCallValidation.outcome, 'blocked')

const localShellGraphValidation = validateCodexResponsesJson({
  response: {
    input: [
      {
        type: 'local_shell_call',
        id: 'lsh_graph',
        call_id: 'call_shell',
        status: 'completed',
        action: { type: 'exec', command: ['echo', 'ok'], timeout_ms: null, working_directory: null, env: null, user: null }
      },
      { type: 'function_call_output', id: 'fco_shell', call_id: 'call_shell', output: 'done' }
    ]
  },
  provenance: 'request_history',
  revision: codexResponsesContractRevision
})
assert.equal(localShellGraphValidation.outcome, 'clean', 'local_shell_call 必须允许 function_call_output 配对')

const serverToolSearchOutputValidation = validateCodexResponsesJson({
  response: {
    input: [{
      type: 'tool_search_output',
      id: 'tso_server',
      call_id: 'call_server_managed',
      status: 'completed',
      execution: 'server',
      tools: []
    }]
  },
  provenance: 'request_history',
  revision: codexResponsesContractRevision
})
assert.equal(serverToolSearchOutputValidation.outcome, 'clean', 'server tool search output 不依赖客户端历史 call')

const noCallIdToolSearchOutputValidation = validateCodexResponsesJson({
  response: {
    input: [{
      type: 'tool_search_output',
      id: 'tso_no_call',
      call_id: null,
      status: '',
      execution: 'client',
      tools: []
    }]
  },
  provenance: 'request_history',
  revision: codexResponsesContractRevision
})
assert.equal(noCallIdToolSearchOutputValidation.outcome, 'clean')

const localToolSearchOrphanValidation = validateCodexResponsesJson({
  response: {
    input: [{
      type: 'tool_search_output',
      id: 'tso_client',
      call_id: 'call_client_missing',
      status: 'completed',
      execution: 'client',
      tools: []
    }]
  },
  provenance: 'request_history',
  revision: codexResponsesContractRevision
})
assert.deepEqual(localToolSearchOrphanValidation.issues.map((issue) => issue.code), ['orphan_tool_output'])

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

const outputRemovePlan: CodexRepairPlan = {
  ...mismatchPlan,
  operations: mismatchPlan.operations.map((operation) => ({ ...operation, action: 'remove' as const }))
}
assert.throws(() => executeCodexResponsesRepair(mismatchedResponse, outputRemovePlan), /codex_repair_forbidden/)

const inputReplacePlan: CodexRepairPlan = {
  ...historyPlan,
  operations: historyPlan.operations.map((operation) => ({ ...operation, action: 'replace' as const, value: 'ctc_forged' }))
}
assert.throws(() => executeCodexResponsesRepair(replayableHistory, inputReplacePlan), /codex_repair_forbidden/)

const unknownTypeDocument: JsonRecord = { output: [{ type: 'future_response_item', id: 'future_bad' }] }
const forgedUnknownPlan: CodexRepairPlan = {
  revision: codexResponsesContractRevision,
  level: 'R0',
  provenance: 'raw_upstream',
  sourceOutcome: 'repairable',
  operations: [{
    action: 'replace',
    path: ['output', 0, 'id'],
    value: 'future_forged',
    expectedItemType: 'future_response_item',
    expectedItemIdPresent: true,
    expectedItemId: 'future_bad',
    issueCode: 'item_id_prefix_mismatch',
    ruleId: 'codex.r0.response.replace_item_id'
  }]
}
assert.throws(() => executeCodexResponsesRepair(unknownTypeDocument, forgedUnknownPlan), /codex_repair_forbidden/)

const staleDocument = structuredClone(mismatchedResponse)
;(staleDocument.output as JsonRecord[])[0]!.id = 'ctc_changed_after_planning'
assert.throws(() => executeCodexResponsesRepair(staleDocument, mismatchPlan), /codex_repair_forbidden/)

const arbitraryIssuePlan: CodexRepairPlan = {
  ...mismatchPlan,
  operations: mismatchPlan.operations.map((operation) => ({ ...operation, issueCode: 'arbitrary_r0_issue' }))
}
assert.throws(() => executeCodexResponsesRepair(mismatchedResponse, arbitraryIssuePlan), /codex_repair_forbidden/)

const validIdDocument: JsonRecord = {
  output: [{ id: 'msg_already_valid', type: 'message', role: 'assistant', content: [] }]
}
const forgedAllowlistedIssuePlan: CodexRepairPlan = {
  revision: codexResponsesContractRevision,
  level: 'R0',
  provenance: 'raw_upstream',
  sourceOutcome: 'repairable',
  operations: [{
    action: 'replace',
    path: ['output', 0, 'id'],
    value: 'msg_rewritten',
    expectedItemType: 'message',
    expectedItemIdPresent: true,
    expectedItemId: 'msg_already_valid',
    issueCode: 'item_id_prefix_mismatch',
    ruleId: 'codex.r0.response.replace_item_id'
  }]
}
assert.throws(
  () => executeCodexResponsesRepair(validIdDocument, forgedAllowlistedIssuePlan),
  /codex_repair_forbidden/,
  'executor 必须重算 allowlisted issue predicate，不能只信任 issue code 字符串'
)

console.log('Codex Responses JSON contract 回归通过：诊断、R0、未知类型、orphan、历史自愈和语义不变量已固定')

function semanticProjection(document: JsonRecord): unknown {
  const copy = structuredClone(document)
  for (const field of ['input', 'output'] as const) {
    const items = Array.isArray(copy[field]) ? copy[field] as JsonRecord[] : []
    for (const item of items) delete item.id
  }
  return copy
}
