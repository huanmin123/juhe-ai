import assert from 'node:assert/strict'

import { GatewayRequestValidationError } from '../../modules/gateway/request/validation-error.js'
import {
  sanitizeCodexResponseHistoryItems
} from '../../modules/gateway/codex-responses/request-history-sanitizer.js'
import type {
  CodexHistorySanitizerContext
} from '../../modules/gateway/codex-responses/request-history-types.js'

const persistentSameScope: CodexHistorySanitizerContext = {
  store: true,
  sourceScopeKey: 'account:a',
  targetScopeKey: 'account:a',
  targetPersistenceScope: 'account',
  contractRevision: 'codex-responses-2026-07-11-r1'
}

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
assert.deepEqual(legacyResult.issueCodes, ['legacy_item_id'])

const idempotentResult = sanitizeCodexResponseHistoryItems(prefixMismatchResult.items, persistentSameScope)
assert.equal(idempotentResult.items, prefixMismatchResult.items, '已清洗结果再次执行必须零拷贝')
assert.equal(idempotentResult.changed, false)
assert.equal(idempotentResult.removedIdCount, 0)

assert.throws(
  () => sanitizeCodexResponseHistoryItems([{
    type: 'reasoning',
    id: 'rs_only_reference'
  }], {
    ...persistentSameScope,
    store: false,
    targetPersistenceScope: 'none'
  }),
  (error) => error instanceof GatewayRequestValidationError
    && error.code === 'codex_history_item_unrecoverable'
    && error.statusCode === 400,
  '只有 ID、没有可重放 payload 的 item 必须显式失败'
)

assert.throws(
  () => sanitizeCodexResponseHistoryItems([{
    type: 'reasoning',
    id: 'rs_empty_summary',
    summary: []
  }], {
    ...persistentSameScope,
    store: false,
    targetPersistenceScope: 'none'
  }),
  (error) => error instanceof GatewayRequestValidationError
    && error.code === 'codex_history_item_unrecoverable',
  '空 reasoning 容器不能伪装成可重放 payload'
)

const unknownItem = { type: 'future_response_item', id: 'future_1', payload: 'opaque' }
const unknownResult = sanitizeCodexResponseHistoryItems([unknownItem], {
  ...persistentSameScope,
  store: false,
  targetPersistenceScope: 'none'
})
assert.equal(unknownResult.items[0], unknownItem, '未知新类型必须原样保留，P0 不猜测其持久化语义')
assert.equal(unknownResult.changed, false)

console.log('Codex Responses 历史 sanitizer 回归通过：前缀、store、作用域、不可恢复、幂等与 clean 零拷贝边界均已固定')
