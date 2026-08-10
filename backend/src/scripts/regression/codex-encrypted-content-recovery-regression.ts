import assert from 'node:assert/strict'
import type { Request } from 'express'

import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import {
  classifyCodexEncryptedContentRecoverySignal,
  recoverCodexEncryptedContentRequest
} from '../../modules/gateway/request/codex-encrypted-content-recovery.js'

const account = {
  id: 'account_codex_recovery',
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_openai_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1'
} as UpstreamAccount

const originalBody = {
  model: 'gpt-5.6-codex',
  stream: true,
  store: false,
  previous_response_id: 'resp_preserve_for_tool_output',
  input: [
    {
      type: 'reasoning',
      id: 'rs_rejected',
      summary: [],
      encrypted_content: 'rejected-reasoning-content'
    },
    {
      type: 'function_call',
      call_id: 'call_1',
      name: 'read_file',
      arguments: '{"path":"package.json"}'
    },
    {
      type: 'function_call_output',
      call_id: 'call_1',
      output: [
        { type: 'input_text', text: 'package content remains available' },
        { type: 'encrypted_content', encrypted_content: 'rejected-function-output-content' }
      ]
    },
    {
      type: 'agent_message',
      author: '/root/subtask',
      content: [
        { type: 'input_text', text: '子任务可读结果仍保留' },
        { type: 'encrypted_content', encrypted_content: 'rejected-agent-message-content' }
      ]
    },
    {
      type: 'compaction',
      id: 'cmp_rejected',
      encrypted_content: 'rejected-compaction-content'
    },
    {
      type: 'compaction_summary',
      id: 'cmp_summary_rejected',
      encrypted_content: 'rejected-compaction-summary-content'
    },
    {
      type: 'context_compaction',
      id: 'cmp_context_rejected',
      encrypted_content: 'rejected-context-compaction-content'
    },
    {
      type: 'message',
      role: 'user',
      content: [{ type: 'input_text', text: 'continue' }]
    }
  ]
}

const recovery = await recoverCodexEncryptedContentRequest({
  req: request(originalBody),
  account,
  requestClientCompatibility: 'codex_responses',
  body: Buffer.from(JSON.stringify(originalBody), 'utf8'),
  upstreamErrorText: 'event: error\ndata: {"type":"error","code":"thinking_signature_invalid","message":"Encrypted function output content could not be decrypted or decoded."}\n\n'
})

assert.equal(recovery.action, 'retry_with_body_variant', '精确签名错误必须生成一次清洗重试变体')
if (recovery.action !== 'retry_with_body_variant') throw new Error('expected recovery body variant')
assert.equal(recovery.metadata.removedReasoningEncryptedContentCount, 1)
assert.equal(recovery.metadata.removedReasoningItemCount, 1)
assert.equal(recovery.metadata.removedFunctionOutputEncryptedContentCount, 1)
assert.equal(recovery.metadata.removedAgentMessageEncryptedContentCount, 1)
assert.equal(recovery.metadata.removedAgentMessageItemCount, 0)
assert.equal(recovery.metadata.removedCompactionEncryptedContentCount, 3)
assert.equal(recovery.metadata.removedCompactionItemCount, 3)
assert.equal(recovery.metadata.preservedPreviousResponseId, true)
assert.equal(recovery.semanticRetryId, 'codex_encrypted_content_cleanup:thinking_signature_invalid')

const recoveredBody = JSON.parse(recovery.body.toString('utf8')) as Record<string, unknown>
assert.equal(recoveredBody.previous_response_id, originalBody.previous_response_id, '工具输出续链必须保留 previous_response_id')
const recoveredInput = recoveredBody.input as Array<Record<string, unknown>>
assert.equal(recoveredInput.some((item) => item.type === 'reasoning'), false, '只有被拒绝密文的空 reasoning 项必须移除')
const functionOutput = recoveredInput.find((item) => item.type === 'function_call_output')
assert.deepEqual(functionOutput?.output, [{ type: 'input_text', text: 'package content remains available' }], '只移除失败的 encrypted_content，保留可读工具输出')
const agentMessage = recoveredInput.find((item) => item.type === 'agent_message')
assert.deepEqual(agentMessage?.content, [{ type: 'input_text', text: '子任务可读结果仍保留' }], 'Codex agent_message 中的失败密文也必须移除，普通子任务结果保持可读')
assert.equal(
  recoveredInput.some((item) => (
    item.type === 'compaction'
      || item.type === 'compaction_summary'
      || item.type === 'context_compaction'
  )),
  false,
  '被拒绝密文所在的 compaction 输入项必须整体移除，不能留下无 encrypted_content 的非法项'
)
assert.equal(countEncryptedContent(recoveredInput), 0, '清洗重试变体不得遗留任意 fixture encrypted_content')
assert.equal((originalBody.input[0] as Record<string, unknown>).encrypted_content, 'rejected-reasoning-content', '清洗不得原地改写客户端请求对象')
assert.equal(((originalBody.input[2] as Record<string, unknown>).output as Array<Record<string, unknown>>)[1]?.encrypted_content, 'rejected-function-output-content', '清洗不得原地改写客户端工具输出')
assert.equal(((originalBody.input[3] as Record<string, unknown>).content as Array<Record<string, unknown>>)[1]?.encrypted_content, 'rejected-agent-message-content', '清洗不得原地改写 Codex 子任务消息')
assert.equal((originalBody.input[4] as Record<string, unknown>).encrypted_content, 'rejected-compaction-content', '清洗不得原地改写 compaction')
assert.equal((originalBody.input[5] as Record<string, unknown>).encrypted_content, 'rejected-compaction-summary-content', '清洗不得原地改写 compaction_summary')
assert.equal((originalBody.input[6] as Record<string, unknown>).encrypted_content, 'rejected-context-compaction-content', '清洗不得原地改写 context_compaction')

const onlyEncryptedFunctionOutput = {
  model: 'gpt-5.6-codex',
  previous_response_id: 'resp_tool_link',
  input: [{
    type: 'function_call_output',
    call_id: 'call_only_encrypted',
    output: [{ type: 'encrypted_content', encrypted_content: 'broken' }]
  }]
}
const functionOutputRecovery = await recoverCodexEncryptedContentRequest({
  req: request(onlyEncryptedFunctionOutput),
  account,
  requestClientCompatibility: 'codex_responses',
  body: JSON.stringify(onlyEncryptedFunctionOutput),
  upstreamErrorText: 'event: error\ndata: {"type":"error","message":"Encrypted function output content could not be decrypted or decoded."}\n\n'
})
assert.equal(functionOutputRecovery.action, 'retry_with_body_variant', '结构化 error.message 的精确解密失败也必须恢复')
if (functionOutputRecovery.action !== 'retry_with_body_variant') throw new Error('expected function output recovery body variant')
const functionOutputOnlyBody = JSON.parse(functionOutputRecovery.body.toString('utf8')) as Record<string, unknown>
assert.deepEqual(((functionOutputOnlyBody.input as Array<Record<string, unknown>>)[0]?.output), [], '仅含失败密文的工具输出必须保留空 output 结构而不是保留密文')
assert.equal(functionOutputOnlyBody.previous_response_id, 'resp_tool_link')

const singleAgentMessage = {
  model: 'gpt-5.6-codex',
  input: {
    type: 'agent_message',
    author: '/root/subtask',
    content: [{ type: 'encrypted_content', encrypted_content: 'broken-agent-message' }]
  }
}
const singleAgentMessageRecovery = await recoverCodexEncryptedContentRequest({
  req: request(singleAgentMessage),
  account,
  requestClientCompatibility: 'codex_responses',
  body: JSON.stringify(singleAgentMessage),
  upstreamErrorText: 'thinking_signature_invalid'
})
assert.equal(singleAgentMessageRecovery.action, 'retry_with_body_variant', '单项 input 的 agent_message 必须能够恢复')
if (singleAgentMessageRecovery.action !== 'retry_with_body_variant') throw new Error('expected single agent message recovery body variant')
const singleAgentMessageBody = JSON.parse(singleAgentMessageRecovery.body.toString('utf8')) as Record<string, unknown>
assert.deepEqual(singleAgentMessageBody.input, [], '仅含失败密文的 agent_message 必须移除，避免发送空 content')
assert.equal(singleAgentMessageRecovery.metadata.removedAgentMessageItemCount, 1)

const noEncryptedContent = await recoverCodexEncryptedContentRequest({
  req: request({ model: 'gpt-5.6-codex', input: 'continue' }),
  account,
  requestClientCompatibility: 'codex_responses',
  body: JSON.stringify({ model: 'gpt-5.6-codex', input: 'continue' }),
  upstreamErrorText: 'thinking_signature_invalid'
})
assert.deepEqual(noEncryptedContent, {
  action: 'not_recoverable',
  signal: 'thinking_signature_invalid',
  reason: 'no_removable_encrypted_content'
}, '没有可移除内容时不得伪造重试变体')

const genericClient = await recoverCodexEncryptedContentRequest({
  req: request(originalBody),
  account,
  requestClientCompatibility: 'openai_standard',
  body: Buffer.from(JSON.stringify(originalBody), 'utf8'),
  upstreamErrorText: 'thinking_signature_invalid'
})
assert.deepEqual(genericClient, { action: 'not_applicable' }, '普通 OpenAI 客户端不得继承 Codex 加密恢复语义')

const exactSignals = [
  'thinking_signature_invalid',
  'invalid_encrypted_content',
  'encrypted_content_decryption_failed'
] as const
for (const signal of exactSignals) {
  assert.equal(
    classifyCodexEncryptedContentRecoverySignal(signal),
    signal,
    `精确 allowlist 信号 ${signal} 必须被分类器保留`
  )
  const exactSignalRecovery = await recoverCodexEncryptedContentRequest({
    req: request(originalBody),
    account,
    requestClientCompatibility: 'codex_responses',
    body: Buffer.from(JSON.stringify(originalBody), 'utf8'),
    upstreamErrorText: signal
  })
  assert.equal(exactSignalRecovery.action, 'retry_with_body_variant', `${signal} 必须产生清洗重试`)
  if (exactSignalRecovery.action !== 'retry_with_body_variant') throw new Error(`expected ${signal} recovery body variant`)
  assert.equal(
    exactSignalRecovery.semanticRetryId,
    `codex_encrypted_content_cleanup:${signal}`,
    `${signal} 必须使用匹配的 semantic retry ID`
  )
}

assert.equal(
  classifyCodexEncryptedContentRecoverySignal('上游调试内容提到了 thinking_signature_invalid，但这不是错误码'),
  undefined,
  '普通文本中的错误码片段不得触发清洗'
)
assert.equal(
  classifyCodexEncryptedContentRecoverySignal('event: response.created\ndata: {"type":"response.created","response":{"metadata":{"note":"thinking_signature_invalid"}}}\n\n'),
  undefined,
  '非 error SSE 事件中的错误码片段不得触发清洗'
)
assert.equal(
  classifyCodexEncryptedContentRecoverySignal('{"error":{"message":"Encrypted content could not be decoded."}}'),
  'encrypted_content_decryption_failed',
  'HTTP JSON error.message 仍可触发受限恢复'
)

console.log('Codex 加密内容恢复回归通过：精确失败后清理 reasoning、工具输出、agent_message 与 compaction 密文，保留关联且不修改原始请求')

function countEncryptedContent(value: unknown): number {
  if (Array.isArray(value)) return value.reduce((count, item) => count + countEncryptedContent(item), 0)
  if (typeof value !== 'object' || value === null) return 0
  const item = value as Record<string, unknown>
  return (typeof item.encrypted_content === 'string' ? 1 : 0)
    + Object.values(item).reduce<number>((count, child) => count + countEncryptedContent(child), 0)
}

function request(body: Record<string, unknown>): Request {
  return {
    method: 'POST',
    originalUrl: '/v1/responses',
    path: '/responses',
    body,
    rawBody: Buffer.from(JSON.stringify(body), 'utf8')
  } as unknown as Request
}
