import assert from 'node:assert/strict'

import {
  sanitizeCodexResponseHistoryItems
} from '../../modules/gateway/codex-responses/request-history-sanitizer.js'
const persistentSameScope = {
  store: true,
  sourceScopeKey: 'account:a',
  targetScopeKey: 'account:a',
  targetPersistenceScope: 'account'
} as const

const cleanItems = [
  {
    type: 'custom_tool_call',
    id: 'ctc_good',
    call_id: 'call_custom',
    name: 'apply_patch',
    input: '*** Begin Patch\n*** End Patch\n'
  },
  {
    type: 'message',
    id: 'msg_good',
    role: 'assistant',
    content: [{ type: 'output_text', text: 'done' }]
  }
]

const cleanResult = sanitizeCodexResponseHistoryItems(cleanItems, persistentSameScope)
assert.equal(cleanResult.items, cleanItems, 'clean 历史必须零拷贝复用输入数组')
assert.equal(cleanResult.changed, false)
assert.equal(cleanResult.removedIdCount, 0)
assert.equal(cleanResult.droppedItemCount, 0)
assert.deepEqual(cleanResult.issueCodes, [])

const prefixMismatchInput = [{
  type: 'custom_tool_call',
  id: 'fc_bad',
  call_id: 'call_custom',
  name: 'apply_patch',
  input: 'patch-body'
}]
const prefixMismatchResult = sanitizeCodexResponseHistoryItems(prefixMismatchInput, persistentSameScope)
assert.equal(prefixMismatchResult.changed, true)
assert.equal(prefixMismatchResult.removedIdCount, 1)
assert.equal(prefixMismatchResult.droppedItemCount, 0)
assert.deepEqual(prefixMismatchResult.issueCodes, ['item_id_prefix_mismatch'])
assert.equal(Object.hasOwn(prefixMismatchResult.items[0] as object, 'id'), false)
assert.equal((prefixMismatchResult.items[0] as Record<string, unknown>).call_id, 'call_custom')
assert.equal((prefixMismatchResult.items[0] as Record<string, unknown>).input, 'patch-body')
assert.equal(prefixMismatchInput[0]?.id, 'fc_bad', 'sanitizer 不得原地修改输入 item')

const storeFalseInput = [{
  type: 'reasoning',
  id: 'rs_unstored',
  summary: [{ type: 'summary_text', text: 'reasoning summary' }],
  encrypted_content: 'encrypted-payload'
}]
const storeFalseResult = sanitizeCodexResponseHistoryItems(storeFalseInput, {
  ...persistentSameScope,
  store: false,
  targetPersistenceScope: 'none'
})
assert.equal(Object.hasOwn(storeFalseResult.items[0] as object, 'id'), false)
assert.deepEqual(storeFalseResult.issueCodes, ['unpersisted_item_reference'])
assert.equal(storeFalseResult.droppedItemCount, 0)
assert.deepEqual((storeFalseResult.items[0] as Record<string, unknown>).summary, storeFalseInput[0]?.summary)
assert.equal((storeFalseResult.items[0] as Record<string, unknown>).encrypted_content, 'encrypted-payload')

const crossScopeInput = [{
  type: 'function_call_output',
  id: 'fco_saved_elsewhere',
  call_id: 'call_scope',
  output: 'tool-result'
}]
const crossScopeResult = sanitizeCodexResponseHistoryItems(crossScopeInput, {
  ...persistentSameScope,
  targetScopeKey: 'account:b'
})
assert.equal(Object.hasOwn(crossScopeResult.items[0] as object, 'id'), false)
assert.deepEqual(crossScopeResult.issueCodes, ['cross_scope_item_reference'])
assert.equal(crossScopeResult.droppedItemCount, 0)
assert.deepEqual(crossScopeResult.items, [{
  type: 'function_call_output',
  call_id: 'call_scope',
  output: 'tool-result'
}])

const legacyInput = [{
  type: 'message',
  id: 'legacy',
  role: 'assistant',
  content: [{ type: 'output_text', text: 'legacy content' }]
}]
const legacyResult = sanitizeCodexResponseHistoryItems(legacyInput, persistentSameScope)
assert.equal(Object.hasOwn(legacyResult.items[0] as object, 'id'), false)
assert.equal(legacyResult.droppedItemCount, 0)
assert.deepEqual(legacyResult.issueCodes, ['legacy_item_id'])

for (const invalidId of ['', null, 42]) {
  const invalidIdInput = [{
    type: 'message',
    id: invalidId,
    role: 'assistant',
    content: [{ type: 'output_text', text: 'invalid ID remains replayable' }]
  }]
  const invalidIdResult = sanitizeCodexResponseHistoryItems(invalidIdInput, persistentSameScope)
  assert.equal(Object.hasOwn(invalidIdResult.items[0] as object, 'id'), false, `无效 ID ${String(invalidId)} 必须被剥离`)
  assert.equal(invalidIdResult.droppedItemCount, 0)
  assert.deepEqual(invalidIdResult.issueCodes, ['invalid_item_id'])
  assert.equal((invalidIdResult.items[0] as Record<string, unknown>).role, 'assistant')
  assert.deepEqual((invalidIdResult.items[0] as Record<string, unknown>).content, invalidIdInput[0]?.content)
}

const noIdItem = {
  type: 'message',
  role: 'assistant',
  content: [{ type: 'output_text', text: 'already inline' }]
}
const noIdResult = sanitizeCodexResponseHistoryItems([noIdItem], persistentSameScope)
assert.equal(noIdResult.items[0], noIdItem, '原本没有 ID 字段的 item 必须保持零拷贝')
assert.equal(noIdResult.changed, false)

const idempotentResult = sanitizeCodexResponseHistoryItems(prefixMismatchResult.items, persistentSameScope)
assert.equal(idempotentResult.items, prefixMismatchResult.items, '已清洗结果再次执行必须零拷贝')
assert.equal(idempotentResult.changed, false)
assert.equal(idempotentResult.removedIdCount, 0)
assert.equal(idempotentResult.droppedItemCount, 0)

const unrecoverableContext = {
  ...persistentSameScope,
  store: false,
  targetPersistenceScope: 'none'
} as const
const unrecoverableOnlyInput = [{
  type: 'reasoning',
  id: 'rs_only_reference'
}]
const unrecoverableOnlyResult = sanitizeCodexResponseHistoryItems(unrecoverableOnlyInput, unrecoverableContext)
assert.deepEqual(unrecoverableOnlyResult.items, [])
assert.equal(unrecoverableOnlyResult.changed, true)
assert.equal(unrecoverableOnlyResult.removedIdCount, 0)
assert.equal(unrecoverableOnlyResult.droppedItemCount, 1)
assert.deepEqual(unrecoverableOnlyResult.issueCodes, [
  'unpersisted_item_reference',
  'unrecoverable_item_dropped'
])
assert.equal(unrecoverableOnlyInput.length, 1, '整项清理不得原地修改输入数组')

const customToolOnlyResult = sanitizeCodexResponseHistoryItems([{
  type: 'custom_tool_call',
  id: 'ctc_only_reference',
  call_id: 'call_custom_only',
  name: 'apply_patch'
}], unrecoverableContext)
assert.deepEqual(customToolOnlyResult.items, [])
assert.equal(customToolOnlyResult.droppedItemCount, 1)
assert.deepEqual(customToolOnlyResult.issueCodes, [
  'unpersisted_item_reference',
  'unrecoverable_item_dropped'
])

const mixedUnrecoverableInput = [
  { type: 'reasoning', id: 'rs_empty_summary', summary: [] },
  { type: 'message', id: 'msg_replayable', role: 'assistant', content: [{ type: 'output_text', text: 'keep' }] },
  { type: 'message', id: null }
]
const mixedUnrecoverableResult = sanitizeCodexResponseHistoryItems(mixedUnrecoverableInput, unrecoverableContext)
assert.deepEqual(mixedUnrecoverableResult.items, [{
  type: 'message',
  role: 'assistant',
  content: [{ type: 'output_text', text: 'keep' }]
}])
assert.equal(mixedUnrecoverableResult.removedIdCount, 1)
assert.equal(mixedUnrecoverableResult.droppedItemCount, 2)
assert.deepEqual(mixedUnrecoverableResult.issueCodes, [
  'unpersisted_item_reference',
  'unrecoverable_item_dropped',
  'invalid_item_id'
])

const unknownItem = { type: 'future_response_item', id: 'future_1', payload: 'opaque' }
const unknownResult = sanitizeCodexResponseHistoryItems([unknownItem], {
  ...persistentSameScope,
  store: false,
  targetPersistenceScope: 'none'
})
assert.equal(unknownResult.items[0], unknownItem, '未知新类型必须原样保留，P0 不猜测其持久化语义')
assert.equal(unknownResult.changed, false)
assert.equal(unknownResult.droppedItemCount, 0)

console.log('Codex Responses 历史 sanitizer 回归通过：前缀、store、作用域、整项清理、幂等与 clean 零拷贝边界均已固定')
