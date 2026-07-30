import assert from 'node:assert/strict'

import {
  codexResponsesContractRegistry,
  codexResponsesContractRevision
} from '../../modules/gateway/codex-responses/contract-registry.js'

// Source of truth: openai-codex commit 1bbdb32789e1f79932df44941236ea3658f6e965,
// codex-rs/protocol/src/models.rs ResponseItem::id_prefix().
const expectedItemPrefixes = new Map<string, string>([
  ['additional_tools', 'at'],
  ['message', 'msg'],
  ['agent_message', 'amsg'],
  ['reasoning', 'rs'],
  ['local_shell_call', 'lsh'],
  ['function_call', 'fc'],
  ['tool_search_call', 'tsc'],
  ['function_call_output', 'fco'],
  ['custom_tool_call', 'ctc'],
  ['custom_tool_call_output', 'ctco'],
  ['tool_search_output', 'tso'],
  ['web_search_call', 'ws'],
  ['image_generation_call', 'ig'],
  ['compaction', 'cmp'],
  ['compaction_summary', 'cmp'],
  ['context_compaction', 'cmp']
])

assert.equal(codexResponsesContractRevision, 'codex-responses-2026-07-11-r1')
assert.equal(codexResponsesContractRegistry.revision, codexResponsesContractRevision)

for (const [type, prefix] of expectedItemPrefixes) {
  const contract = codexResponsesContractRegistry.item(type)
  assert.ok(contract, `registry 必须包含 ${type}`)
  assert.equal(contract.prefix, prefix, `${type} 必须使用 Codex 源码声明的 ${prefix}_ 前缀`)
}

assert.equal(codexResponsesContractRegistry.item('other'), undefined)
assert.equal(codexResponsesContractRegistry.item('future_response_item'), undefined)
assert.equal(codexResponsesContractRegistry.itemByPrefix('ctc')?.type, 'custom_tool_call')
assert.equal(codexResponsesContractRegistry.itemByPrefix('cmp')?.type, 'compaction')
assert.equal(codexResponsesContractRegistry.itemByPrefix('unknown'), undefined)
const compactionTrigger = codexResponsesContractRegistry.item('compaction_trigger')
assert.ok(compactionTrigger, 'Codex 源码中的 compaction_trigger 必须是已知无 ID 类型')
assert.equal(compactionTrigger.prefix, undefined)
assert.equal(codexResponsesContractRegistry.items.length, expectedItemPrefixes.size + 1)

assert.throws(
  () => (codexResponsesContractRegistry.items as unknown as Array<unknown>).push({}),
  TypeError,
  '公开 items 表必须冻结，调用方不能运行时修改 contract'
)

console.log('Codex Responses 上下文 ID 前缀表回归通过：revision、源码前缀和未知类型边界已固定')
