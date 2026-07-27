import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  accountTestDefaultPrompt,
  accountImageTestDefaultPrompt,
  accountTestGeminiModelsPath,
  accountTestModelsPath,
  accountTestModelsPathForProtocol,
  createAnthropicTestRequest,
  createGeminiTestRequest,
  createOpenAIChatCompletionsTestPayload,
  createOpenAIImageGenerationTestRequest,
  createOpenAIResponsesTestPayload,
  createOpenAITestRequest,
  testPathFromEndpointMode
} from '../../modules/accounts/account-test-request.js'
import { accountTestProbeKind } from '../../modules/accounts/account-test-probe-policy.js'
import {
  accountDiagnosticAttemptProgress,
  accountDiagnosticRetryTimeouts
} from '../../modules/accounts/account-diagnostic-retry-policy.js'
import {
  extractAccountTestResponseOutputText,
  parseAccountTestUpstreamErrorCode,
  parseAccountTestStreamFailureMessage,
  parseAccountTestUpstreamMessage,
  redactAccountTestImageResponse,
  resolveAccountTestResponseDiagnostics
} from '../../modules/accounts/account-test-response-diagnostics.js'
import {
  accountModelCatalogIds,
  hasAccountModelCatalogResponseEvidence,
  hasAccountModelCatalogSuccessEvidence,
  hasAccountTestProtocolSuccessEvidence
} from '../../modules/accounts/account-test-success-evidence.js'
import { parseDiagnosticResponseContext } from '../../modules/gateway/diagnostics/diagnostic-response-context.js'
import { accountBatchEditSchema, accountCreateSchema, accountModelCatalogRefreshSchema, accountTestSchema, accountUpdateSchema } from '../../modules/accounts/account-request.schemas.js'

assert.equal(accountTestDefaultPrompt, '只输出 OK', '账号测试默认 prompt 应保持中文默认值')
assert.equal(accountTestModelsPath, '/v1/models', '模型列表探测路径应保持 /v1/models')
assert.equal(accountTestGeminiModelsPath, '/v1beta/models', 'Gemini 模型目录探测必须使用原生 /v1beta/models 路径')
assert.equal(accountTestModelsPathForProtocol('gemini'), '/v1beta/models', 'Gemini 协议必须选择原生模型目录路径')
assert.equal(accountTestModelsPathForProtocol('anthropic'), '/v1/models', 'Anthropic 协议必须保留 /v1/models 目录路径')
assert.deepEqual(accountDiagnosticRetryTimeouts('generation'), [10_000, 20_000, 30_000], '文本测试必须保留三档重试窗口')
assert.deepEqual(accountDiagnosticRetryTimeouts('image_generation'), [120_000], '图片测试必须只生成一次，并保留 120 秒完成窗口')
assert.deepEqual(
  accountDiagnosticAttemptProgress(0, 120_000, Date.now() + 1_000, accountDiagnosticRetryTimeouts('image_generation')),
  {
    attemptIndex: 0,
    attemptNumber: 1,
    totalAttempts: 1,
    timeoutMs: 120_000,
    maxTotalTimeoutMs: 120_000,
    elapsedMs: 0
  },
  '图片测试进度必须显示单次 120 秒窗口'
)
const openAIProfile = { providerCode: 'gpt', protocolCode: 'openai', protocolVersion: 'v1', type: 'api_key' }
assert.equal(accountTestProbeKind(openAIProfile, { supportedApiProtocols: ['images'] }), 'image_generation', '只声明 Images 的模型必须使用真实图片生成探针')
assert.equal(accountTestProbeKind(openAIProfile, { supportedApiProtocols: ['responses'] }), 'generation', '文本模型探针必须继续使用保存的生成请求形态')
assert.equal(
  accountTestProbeKind(openAIProfile, { supportedApiProtocols: ['images'] }),
  'image_generation',
  '自定义图片模型必须按图片能力识别，并使用真实图片生成探针'
)
assert.equal(
  accountTestProbeKind(openAIProfile, { testEndpointMode: 'responses_sse', supportedApiProtocols: ['responses', 'images'] }),
  'generation',
  '同时支持 Responses 与 Images 的模型选择 Responses 时必须执行文本生成探针'
)
assert.equal(
  accountTestProbeKind(openAIProfile, { testEndpointMode: 'images_json', supportedApiProtocols: ['responses', 'images'] }),
  'image_generation',
  '同时支持 Responses 与 Images 的模型选择 Images 时必须执行真实图片生成探针'
)
assert.equal(
  accountTestProbeKind(openAIProfile, { testEndpointMode: 'images_json', supportedApiProtocols: ['responses'] }),
  'generation',
  '模型目录未声明 Images 时不得仅凭请求形态启用图片探针'
)
assert.equal(
  accountTestProbeKind({ ...openAIProfile, type: 'oauth' }, { supportedApiProtocols: ['images'] }),
  'generation',
  'OAuth 账户没有 Images API 探针能力，不得误判为可测试的 gpt-image-2 账户'
)
assert.equal(
  accountTestProbeKind({ ...openAIProfile, protocolCode: 'gemini', protocolVersion: 'v1beta' }, { supportedApiProtocols: ['images'] }),
  'generation',
  '非 OpenAI 协议不得猜测复用 OpenAI 图片生成探针'
)
assert.equal(
  hasAccountModelCatalogSuccessEvidence('gpt-image-2', JSON.stringify({ object: 'list', data: [{ id: 'gpt-image-2' }] })),
  true,
  '模型目录探针必须确认目标图像模型存在'
)
assert.equal(
  hasAccountModelCatalogSuccessEvidence('gpt-image-2', JSON.stringify({ object: 'list', data: [{ id: 'gpt-5.5' }] })),
  false,
  '模型目录缺少目标模型时不得判定探针成功'
)
assert.deepEqual(
  accountModelCatalogIds(JSON.stringify({ models: [{ name: 'models/gemini-2.5-pro' }, { name: 'models/gemini-2.5-pro' }] })),
  ['gemini-2.5-pro'],
  'Gemini 模型目录必须去除 models/ 前缀并去重'
)
assert.equal(
  hasAccountModelCatalogResponseEvidence(JSON.stringify({ models: [] })),
  true,
  '空 Gemini 模型目录仍是有效目录响应，不能误判为协议错误'
)
assert.equal(
  hasAccountModelCatalogResponseEvidence(JSON.stringify({ data: [] })),
  true,
  'Anthropic 风格的 data 模型目录即使未携带 OpenAI object=list 也必须视为有效响应'
)
assert.equal(accountTestSchema.safeParse({ testEndpointMode: 'images_json' }).success, true, '账户测试契约必须接受 Images API 请求形态')
assert.equal(accountModelCatalogRefreshSchema.safeParse({
  account: {
    providerCode: 'openai-compatible',
    providerProtocolProfileId: 'openai-v1',
    type: 'api_key',
    credentials: { base_url: 'https://upstream.example/v1', api_key: 'sk-test' }
  }
}).success, true, '模型目录发现不得要求预先选择支持模型、检查模型或分组')
assert.deepEqual(redactAccountTestImageResponse(JSON.stringify({
  created: 1,
  data: [{ b64_json: 'base64-must-not-leak', url: 'https://image.example/private.png', revised_prompt: 'a white square' }],
  usage: { total_tokens: 1 }
})), {
  created: 1,
  data: [{ b64_json: '已省略', url: '已省略', revised_prompt: 'a white square' }],
  usage: { total_tokens: 1 }
}, '图片测试完整 JSON 必须保留非图片字段，并省略图片内容')
assert.deepEqual(redactAccountTestImageResponse('not-json'), { response: '已省略' }, '非 JSON 图片响应不得原样返回前端')

for (const mode of ['interactions_json', 'interactions_sse'] as const) {
  assert.equal(accountCreateSchema.safeParse({
    providerCode: 'gemini',
    providerProtocolProfileId: 'profile_gemini_native_v1beta',
    name: `Gemini ${mode}`,
    type: 'api_key',
    healthCheckEndpointMode: mode
  }).success, true, `账户创建契约必须接受 ${mode}`)
  assert.equal(accountUpdateSchema.safeParse({ healthCheckEndpointMode: mode }).success, true, `账户更新契约必须接受 ${mode}`)
  assert.equal(accountTestSchema.safeParse({ testEndpointMode: mode }).success, true, `账户测试契约必须接受 ${mode}`)
}
assert.equal(accountBatchEditSchema.safeParse({
  targets: [{ accountId: 'account-a', configRevision: 1 }, { accountId: 'account-b', configRevision: 1 }],
  updates: {
    supportedEndpointModes: {
      enabled: true,
      value: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'messages_json', 'messages_sse', 'message_token_counting', 'generate_content_json', 'generate_content_sse', 'interactions_json', 'interactions_sse', 'count_tokens', 'embed_content']
    }
  }
}).success, true, '批量编辑契约必须接受完整 13 种 endpoint mode')

const protocolSuccessFixtures = [
  ['chat_json', JSON.stringify({ object: 'chat.completion', choices: [{ finish_reason: 'stop', message: { content: 'OK' } }] })],
  ['chat_sse', `data: ${JSON.stringify({ object: 'chat.completion.chunk', choices: [{ finish_reason: 'stop', delta: {} }] })}\n\ndata: [DONE]\n\n`],
  ['responses_json', JSON.stringify({ object: 'response', status: 'completed', output: [] })],
  ['responses_sse', `event: response.completed\ndata: ${JSON.stringify({ type: 'response.completed', response: { object: 'response', status: 'completed', output: [] } })}\n\n`],
  ['messages_json', JSON.stringify({ type: 'message', stop_reason: 'end_turn', content: [] })],
  ['messages_sse', `event: message_stop\ndata: ${JSON.stringify({ type: 'message_stop' })}\n\n`],
  ['generate_content_json', JSON.stringify({ candidates: [{ finishReason: 'STOP', content: { parts: [{ text: 'OK' }] } }] })],
  ['generate_content_sse', `data: ${JSON.stringify({ candidates: [{ finishReason: 'STOP', content: { parts: [{ text: 'OK' }] } }] })}\n\n`],
  ['interactions_json', JSON.stringify({ object: 'interaction', status: 'completed', steps: [{ type: 'model_output', content: [{ type: 'text', text: 'OK' }] }] })],
  ['interactions_sse', `data: ${JSON.stringify({ event_type: 'interaction.completed', interaction: { status: 'completed' } })}\n\n`]
] as const
for (const [mode, body] of protocolSuccessFixtures) {
  assert.equal(hasAccountTestProtocolSuccessEvidence(mode, body), true, `${mode} 应识别协议完成证据`)
  for (const invalid of ['', 'data: [DONE]\n\n', '<html>ok</html>', '{invalid json']) {
    assert.equal(hasAccountTestProtocolSuccessEvidence(mode, invalid), false, `${mode} 不得把空白、仅 DONE、HTML 或畸形 JSON 当作成功`)
  }
}
assert.equal(hasAccountTestProtocolSuccessEvidence(
  'chat_sse',
  `data: ${JSON.stringify({ choices: [{ delta: { content: 'OK' }, finish_reason: null }] })}\n\ndata: [DONE]\n\n`
), true, '兼容 Chat SSE 有非空 content chunk 且以 [DONE] 结束时应视为成功')
assert.equal(hasAccountTestProtocolSuccessEvidence(
  'chat_sse',
  `data: ${JSON.stringify({ choices: [{ delta: { role: 'assistant' }, finish_reason: null }] })}\n\ndata: [DONE]\n\n`
), false, '只有角色 chunk 和 [DONE] 不构成模型输出成功证据')
assert.equal(hasAccountTestProtocolSuccessEvidence(
  'chat_sse',
  `data: [DONE]\n\ndata: ${JSON.stringify({ choices: [{ delta: { content: 'late' }, finish_reason: null }] })}\n\n`
), false, '[DONE] 之后追加内容属于非法事件顺序，不得构成成功证据')

const upstreamFailureResponse = 'event: response.failed\ndata: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_is_overloaded","message":"upstream original failure"}}}\n\n'
assert.equal(parseAccountTestUpstreamErrorCode(upstreamFailureResponse), 'server_is_overloaded', '人工账号测试必须从上游原始 SSE 提取错误码')
let accountDiagnosticJsonParseCount = 0
const sharedFailureContext = parseDiagnosticResponseContext(upstreamFailureResponse, {
  onJsonParseAttempt: () => { accountDiagnosticJsonParseCount += 1 }
})
assert.equal(parseAccountTestUpstreamErrorCode(sharedFailureContext), 'server_is_overloaded')
assert.equal(parseAccountTestUpstreamMessage(sharedFailureContext, 'openai'), 'upstream original failure')
assert.equal(parseAccountTestStreamFailureMessage(sharedFailureContext, 'openai'), 'upstream original failure')
assert.equal(extractAccountTestResponseOutputText(sharedFailureContext, 'openai'), undefined)
assert.equal(hasAccountTestProtocolSuccessEvidence('responses_sse', sharedFailureContext), false)
assert.equal(accountDiagnosticJsonParseCount, 1, '同一账户诊断 SSE payload 的错误码、消息、输出和完成证据必须共享一次 JSON 解析')

let accountMultilineParseCount = 0
const multilineChatContext = parseDiagnosticResponseContext(
  'event: chunk\r\ndata: {"model":"gpt-test",\r\ndata: "choices":[{"delta":{"content":"OK"},"finish_reason":null}]}\r\n\r\ndata: [DONE]',
  { onJsonParseAttempt: () => { accountMultilineParseCount += 1 } }
)
assert.equal(extractAccountTestResponseOutputText(multilineChatContext, 'openai'), 'OK', 'SSE framing 必须合并 multi-line data')
assert.equal(hasAccountTestProtocolSuccessEvidence('chat_sse', multilineChatContext), true, 'CRLF 且末尾无空行的 SSE 必须 flush 最后事件')
assert.equal(accountMultilineParseCount, 1, '[DONE] 不得触发 JSON.parse，multi-line payload 只解析一次')
assert.deepEqual(resolveAccountTestResponseDiagnostics({
  downstreamResponseText: 'event: response.failed\ndata: {"type":"response.failed","response":{"error":{"code":"upstream_retryable_error","message":"上游流式响应在输出前失败，请重试"}}}\n\n',
  downstreamResponseHeaders: { 'content-type': 'text/event-stream; charset=utf-8' },
  downstreamResponseTruncated: false,
  upstreamAttempt: {
    accountId: 'account-upstream-failure',
    accountName: '上游失败账户',
    upstreamUrl: 'https://upstream.example/v1/responses',
    status: 503,
    responseHeaders: { 'content-type': 'application/json', 'x-upstream-diagnostic': 'original' },
    responseBodyText: upstreamFailureResponse
  }
}), {
  responseText: upstreamFailureResponse,
  responseHeaders: { 'content-type': 'application/json', 'x-upstream-diagnostic': 'original' },
  responseTruncated: false
}, '人工账号测试失败时必须展示上游原始响应正文和 header，不能展示网关生成的客户端重试错误')
assert.deepEqual(resolveAccountTestResponseDiagnostics({
  downstreamResponseText: 'downstream fallback',
  downstreamResponseHeaders: { 'content-type': 'application/json' },
  downstreamResponseTruncated: true,
  upstreamAttempt: {
    accountId: 'account-empty-upstream-body',
    accountName: '空上游正文账户',
    upstreamUrl: 'https://upstream.example/v1/responses',
    status: 503,
    responseBodyText: '   '
  }
}), {
  responseText: 'downstream fallback',
  responseHeaders: { 'content-type': 'application/json' },
  responseTruncated: true
}, '上游诊断未捕获正文时必须保留下游兜底响应，避免返回空控制台')
assert.equal(resolveAccountTestResponseDiagnostics({
  downstreamResponseText: 'downstream',
  downstreamResponseHeaders: {},
  downstreamResponseTruncated: false,
  upstreamAttempt: {
    accountId: 'account-truncated-upstream-body',
    accountName: '截断上游正文账户',
    upstreamUrl: 'https://upstream.example/v1/responses',
    responseBodyText: `${upstreamFailureResponse}\n[truncated]`
  }
}).responseTruncated, true, '展示上游有界诊断时必须同步返回正文截断标记')

assert.equal(testPathFromEndpointMode('chat_json'), '/v1/chat/completions', 'Chat JSON 测试应使用 Chat Completions 路径')
assert.equal(testPathFromEndpointMode('images_json'), '/v1/images/generations', '图片生成测试应使用 Images generations 路径')
assert.equal(testPathFromEndpointMode('chat_sse'), '/v1/chat/completions', 'Chat SSE 测试应使用 Chat Completions 路径')
assert.equal(testPathFromEndpointMode('responses_json'), '/v1/responses', 'Responses JSON 测试应使用 Responses 路径')
assert.equal(testPathFromEndpointMode('responses_sse'), '/v1/responses', 'Responses SSE 测试应使用 Responses 路径')
assert.equal(testPathFromEndpointMode('messages_sse'), '/v1/messages', 'Anthropic Messages 测试应使用 Messages 路径')
assert.equal(testPathFromEndpointMode('generate_content_json', 'gemini-2.5-pro'), '/v1beta/models/gemini-2.5-pro:generateContent', 'Gemini JSON 测试应使用 generateContent 路径')
assert.equal(testPathFromEndpointMode('generate_content_sse', 'gemini-2.5-pro'), '/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse', 'Gemini SSE 测试应使用 streamGenerateContent 路径')
assert.equal(testPathFromEndpointMode('interactions_json', 'gemini-3.5-flash'), '/v1beta/interactions', 'Gemini Interactions JSON 测试应使用统一资源路径')
assert.equal(testPathFromEndpointMode('interactions_sse', 'gemini-3.5-flash'), '/v1beta/interactions', 'Gemini Interactions SSE 测试应使用统一资源路径，不追加 alt=sse')

const chatRequest = createOpenAITestRequest({
  explicitModel: '  gpt-5.5-chat  ',
  fallbackModel: 'fallback-model',
  prompt: 'ping',
  isOAuth: false,
  clientCompatibility: 'openai_standard',
  testEndpointMode: 'chat_json'
})
assert.equal(chatRequest.path, '/v1/chat/completions', 'Chat JSON 本次测试形态应生成 chat 测试路径')
assert.equal(chatRequest.model, 'gpt-5.5-chat', '显式模型应 trim 后优先使用')
assert.deepEqual(chatRequest.body, {
  model: 'gpt-5.5-chat',
  messages: [
    {
      role: 'user',
      content: 'ping'
    }
  ],
  max_tokens: 1,
  stream: false
}, 'Chat JSON 测试 payload 应保持非流式字段')

const imageRequest = createOpenAIImageGenerationTestRequest({
  explicitModel: ' gpt-image-2 ',
  fallbackModel: 'fallback-image-model'
})
assert.equal(accountImageTestDefaultPrompt, 'Solid black.', '图片测试提示词应保持轻量且稳定')
assert.equal(imageRequest.path, '/v1/images/generations', '图片模型测试必须调用 Images generations')
assert.deepEqual(imageRequest.body, {
  model: 'gpt-image-2',
  prompt: accountImageTestDefaultPrompt,
  n: 1,
  size: '1024x1024',
  quality: 'low',
  output_format: 'webp',
  output_compression: 100
}, '图片测试必须使用单张、低质量的最小成本请求')

const chatSseRequest = createOpenAITestRequest({
  fallbackModel: 'chat-only-model',
  prompt: 'ok',
  isOAuth: false,
  clientCompatibility: 'openai_standard',
  testEndpointMode: 'chat_sse'
})
assert.equal(chatSseRequest.path, '/v1/chat/completions', 'Chat SSE 测试应构造 chat completions 路径')
assert.equal(chatSseRequest.body.stream, true, 'Chat SSE 测试应使用 stream=true')

const oauthRequest = createOpenAITestRequest({
  fallbackModel: 'gpt-5.5-oauth',
  prompt: 'pong',
  isOAuth: true,
  clientCompatibility: 'codex_responses',
  testEndpointMode: 'responses_sse'
})
assert.equal(oauthRequest.path, '/v1/responses', 'OAuth Responses SSE 测试应走 Responses')
assert.equal(oauthRequest.model, 'gpt-5.5-oauth', '无显式模型时应使用 fallback model')
assert.equal(oauthRequest.body.model, 'gpt-5.5-oauth', 'Responses payload 应写入模型')
assert.equal(oauthRequest.body.stream, true, 'Responses SSE 测试应保持流式形态')
assert.equal(oauthRequest.body.store, false, 'OAuth Responses 测试不应存储')
assert.equal(oauthRequest.body.max_output_tokens, 1, 'OAuth Responses 测试应限制输出 token')
assert.deepEqual(oauthRequest.body.include, ['reasoning.encrypted_content'], 'Codex Responses SSE 测试应保留 encrypted reasoning include')

const responsesJsonPayload = createOpenAIResponsesTestPayload('gpt-5.5-json', 'ok', false, 'codex_responses', false)
assert.equal(responsesJsonPayload.stream, false, 'Responses JSON 测试不能被 Codex 画像强行改成 stream')
assert.equal(Object.prototype.hasOwnProperty.call(responsesJsonPayload, 'include'), false, 'Responses JSON 测试不应写入 SSE 专用 include')

const codexSsePayload = createOpenAIResponsesTestPayload('gpt-5.5-codex', 'ok', false, 'codex_responses', true)
assert.equal(codexSsePayload.stream, true, 'Codex Responses SSE 测试必须保持 stream')
assert.equal(codexSsePayload.store, false, 'Codex Responses SSE 测试不应存储')
assert.deepEqual(codexSsePayload.include, ['reasoning.encrypted_content'], 'Codex Responses SSE 测试应保留 encrypted reasoning include')
assert.equal(codexSsePayload.reasoning, undefined, '非 Lite Codex Responses 测试不能注入 reasoning context')
assert.equal(codexSsePayload.parallel_tool_calls, undefined, '非 Lite Codex Responses 测试保持现有并行工具语义')

const responsesLiteSsePayload = createOpenAIResponsesTestPayload('gpt-5.6-sol', 'ok', false, 'codex_responses', true)
assert.deepEqual(responsesLiteSsePayload.reasoning, { context: 'all_turns' }, 'Lite 账户测试必须声明全部轮次 reasoning context')
assert.equal(responsesLiteSsePayload.parallel_tool_calls, false, 'Lite 账户测试必须关闭并行工具调用')
const latestCodexProbe = createOpenAITestRequest({
  fallbackModel: 'gpt-5.6-sol',
  prompt: 'ok',
  isOAuth: false,
  clientCompatibility: 'codex_responses',
  testEndpointMode: 'responses_sse'
})
assert.equal(latestCodexProbe.headers?.originator, 'Codex Desktop')
assert.equal(latestCodexProbe.headers?.['x-codex-beta-features'], 'remote_compaction_v2')
assert.equal(latestCodexProbe.headers?.['x-openai-internal-codex-responses-lite'], 'true')
assert.equal(typeof latestCodexProbe.body.client_metadata, 'object', '手动测试与后台探针必须携带当前 Codex client_metadata')
assert.equal(typeof latestCodexProbe.body.prompt_cache_key, 'string', '手动测试与后台探针必须携带当前 Codex prompt cache key')

assert.deepEqual(
  createOpenAIChatCompletionsTestPayload('gpt-5.5-chat', 'ok', true),
  {
    model: 'gpt-5.5-chat',
    messages: [
      {
        role: 'user',
        content: 'ok'
      }
    ],
    max_tokens: 1,
    stream: true
  },
  'chat completions payload helper 应保持原字段'
)

const glmResponsesRequest = createOpenAITestRequest({
  fallbackModel: 'glm-5.2',
  prompt: 'ok',
  isOAuth: false,
  clientCompatibility: 'codex_responses',
  testEndpointMode: 'responses_sse'
})
assert.equal(glmResponsesRequest.path, '/v1/responses', '显式 Responses SSE 测试请求必须走 Responses 下游路径')
assert.equal(glmResponsesRequest.body.stream, true, '显式 Responses SSE 测试请求必须使用 SSE')

const anthropicRequest = createAnthropicTestRequest({
  fallbackModel: 'claude-opus-4-8',
  prompt: 'ok',
  supportedEndpointModes: ['messages_json', 'messages_sse'],
  testEndpointMode: 'messages_json'
})
assert.equal(anthropicRequest.path, '/v1/messages', 'Anthropic Messages 测试应使用 Messages 路径')
assert.equal(anthropicRequest.body.stream, false, 'Messages JSON 测试应保持非流式')
assert.equal(anthropicRequest.headers?.['x-juhe-client-profile'], 'claude_code', 'Anthropic 账户测试应显式声明 Claude Code 画像')
assert.equal(typeof anthropicRequest.headers?.['x-claude-code-session-id'], 'string', 'Anthropic 账户测试应携带 Claude Code session header')
assert.equal(anthropicRequest.body.max_tokens, 32000, 'Anthropic 账户测试应使用 Claude Code 形态的 max_tokens')
assert.equal(Array.isArray(anthropicRequest.body.tools), true, 'Anthropic 账户测试应保留无工具 tools=[] 形态')
assert.deepEqual(anthropicRequest.body.thinking, { type: 'adaptive' }, 'Anthropic 账户测试应使用 Claude Code thinking body')
assert.deepEqual(anthropicRequest.body.output_config, { effort: 'high' }, 'Anthropic 账户测试应使用 Claude Code output_config')
assert.equal(Array.isArray(anthropicRequest.body.system), true, 'Anthropic 账户测试应使用 Claude Code system block 数组')
assert.match(JSON.stringify(anthropicRequest.body), /Claude Agent SDK/, 'Anthropic 账户测试应使用真实 Claude Code SDK system 文案')
const anthropicSseRequest = createAnthropicTestRequest({
  fallbackModel: 'claude-opus-4-8',
  prompt: 'ok',
  supportedEndpointModes: ['messages_sse'],
  testEndpointMode: 'messages_sse'
})
assert.equal(anthropicSseRequest.body.stream, true, 'Messages SSE 测试必须使用 stream=true')

const geminiRequest = createGeminiTestRequest({
  fallbackModel: 'gemini-2.5-pro',
  prompt: 'ok',
  testEndpointMode: 'generate_content_sse'
})
assert.equal(geminiRequest.path, '/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse', 'Gemini SSE 测试应使用 streamGenerateContent')
assert.deepEqual(geminiRequest.body, {
  contents: [
    {
      role: 'user',
      parts: [
        {
          text: 'ok'
        }
      ]
    }
  ],
  generationConfig: {
    maxOutputTokens: 1
  }
}, 'Gemini 测试 payload 应使用 generateContent 原生结构')

const geminiInteractionsJsonRequest = createGeminiTestRequest({
  fallbackModel: 'gemini-3.5-flash',
  prompt: 'ok',
  testEndpointMode: 'interactions_json'
})
assert.equal(geminiInteractionsJsonRequest.path, '/v1beta/interactions', 'Gemini Interactions JSON 测试应使用统一资源路径')
assert.deepEqual(geminiInteractionsJsonRequest.body, {
  model: 'gemini-3.5-flash',
  input: 'ok',
  stream: false
}, 'Gemini Interactions JSON 测试应使用官方 model/input/stream 请求结构')

const geminiInteractionsSseRequest = createGeminiTestRequest({
  explicitModel: ' gemini-3.5-flash ',
  fallbackModel: 'fallback-model',
  prompt: 'stream ok',
  testEndpointMode: 'interactions_sse'
})
assert.equal(geminiInteractionsSseRequest.path, '/v1beta/interactions', 'Gemini Interactions SSE 测试应使用统一资源路径，不追加 alt=sse')
assert.equal(geminiInteractionsSseRequest.headers?.accept, 'text/event-stream', 'Gemini Interactions SSE 测试应使用 Accept 协商流式响应')
assert.deepEqual(geminiInteractionsSseRequest.body, {
  model: 'gemini-3.5-flash',
  input: 'stream ok',
  stream: true
}, 'Gemini Interactions SSE 测试应启用官方 stream 字段')

const requestSource = readFileSync(resolve('src/modules/accounts/account-test-request.ts'), 'utf8')
assert.doesNotMatch(requestSource, /handleOpenAIGatewayRequest|findAccountForTest|flushGatewayAccountSideEffects/, '测试请求 payload 模块不能依赖真实网关编排或账号解析')
const openAITestRequestInputSource = requestSource.match(/export type AccountTestRequestInput\s*=\s*\{[\s\S]*?\r?\n}\r?\n/)?.[0] ?? ''
assert.doesNotMatch(openAITestRequestInputSource, /requestShape|supportedEndpointModes|providerProtocolProfileId/, 'OpenAI 测试请求模块只按本次 testEndpointMode 构造请求，不从真实请求或供应商档案改写')
assert.match(openAITestRequestInputSource, /testEndpointMode:\s*AccountSupportedEndpointMode/, 'OpenAI 测试请求输入必须显式接收本次测试 endpoint mode')
const serviceSource = readFileSync(resolve('src/modules/accounts/account-test.service.ts'), 'utf8')
const optionsServiceSource = readFileSync(resolve('src/modules/accounts/account-test-options.service.ts'), 'utf8')
const endpointModesSource = readFileSync(resolve('src/modules/accounts/account-test-endpoint-modes.ts'), 'utf8')
assert.match(serviceSource, /const responseContext = parseDiagnosticResponseContext\(responseText\)/, '账户测试必须为最终诊断正文创建唯一解析上下文')
assert.match(serviceSource, /const imageResponseBody = probeKind === 'image_generation'\s*\? redactAccountTestImageResponse\(responseContext\)/, '图片测试必须复用解析上下文后脱敏图片响应正文')
assert.doesNotMatch(serviceSource, /parseOpenAIJsonBody|parseOpenAIUpstreamMessage|parseAnthropicUpstreamMessage|parseGeminiPayloads/, '账户测试不得绕过统一诊断上下文重复解析响应正文')
assert.match(serviceSource, /responseBody: imageResponseBody,\s*responseText: JSON\.stringify\(imageResponseBody\)/, '图片测试必须仅返回已脱敏的响应 JSON')
assert.match(serviceSource, /probeKind === 'image_generation'\s*\? proxyFailureMessage \|\| protocolEvidenceError \|\| accountTestHttpFailureMessage/, '图片测试失败信息不得回显上游响应正文')
assert.match(serviceSource, /const suppressDiagnostics = probeKind === 'image_generation'/, '图片测试异常路径不得保留原始错误正文')
assert.match(serviceSource, /createOpenAIImageGenerationTestRequest/, '人工图像测试必须构造真实图片生成请求')
assert.match(serviceSource, /const protocolSuccessEvidence = probeKind === 'image_generation'\s*\? true/, '图片测试 HTTP 2xx 必须直接判定为成功，不解析图片响应内容')
assert.match(serviceSource, /accountManualTestEndpointModes/, '测试服务必须复用共享解析器读取账号可测试形态')
assert.match(optionsServiceSource, /accountManualTestEndpointModes/, '测试选项必须复用共享解析器读取账号可测试形态')
assert.match(endpointModesSource, /supported_endpoint_modes/, '共享解析器必须从账号上游接口能力读取可测试形态')
assert.match(serviceSource, /resolveAccountTestEndpointMode/, '测试服务必须校验本次 testEndpointMode 是否被账号上游接口能力允许')
assert.match(serviceSource, /isMessagesTestEndpointMode\(testEndpointMode\)/, '混合供应商测试请求必须按本次 mode 分派 Messages 请求构造与解析')
assert.match(serviceSource, /isGeminiTestEndpointMode\(testEndpointMode\)/, '混合供应商测试请求必须按本次 mode 分派 Gemini 请求构造与解析')
assert.match(serviceSource, /测试请求形态不在账户上游接口能力中/, '账户测试请求形态错误必须使用上游接口能力文案')
assert.match(serviceSource, /账户上游接口能力中没有可用于连接测试的请求形态/, '账户测试空能力错误必须使用上游接口能力文案')
assert.match(optionsServiceSource, /账户上游接口能力中没有可用于连接测试的请求形态/, '测试选项空能力错误必须使用上游接口能力文案')
assert.match(serviceSource, /handleOpenAIGatewayRequest/, '真实网关测试编排仍应留在 account-test.service.ts')
assert.match(serviceSource, /preflightAccountModelCatalog/, '人工测试和恢复探针必须先执行可缓存的模型目录预检')
assert.match(serviceSource, /accountModelCatalogPreflightTtlMs/, '目录预检必须使用有界成功缓存，避免每次探针都额外请求')
const retryTestSource = serviceSource.slice(
  serviceSource.indexOf('export async function testOpenAIAccountWithDiagnosticRetries'),
  serviceSource.indexOf('export async function discoverAccountUpstreamModels')
)
assert.match(retryTestSource, /const preflightFailure = await preflightAccountModelCatalog/, '真实模型测试必须先获得目录预检结果')
assert.match(retryTestSource, /if \(preflightFailure\)\s*\{\s*return accountTestResultWithTotalDuration\(preflightFailure, startedAt\)/, '目录预检不成功时不得继续真实模型测试')
assert.doesNotMatch(retryTestSource, /继续执行真实模型测试/, '目录预检失败不得存在生成测试降级路径')
assert.match(serviceSource, /candidateAccounts:\s*\[diagnosticCandidate\]/, '测试服务仍应固定当前诊断候选账号')
assert.match(serviceSource, /disableSessionAffinity:\s*true/, '测试服务仍应禁用 session affinity')
assert.match(serviceSource, /trafficSource:\s*input\.trafficSource\s*\?\?\s*'manual_account_test'/, '测试服务仍应保留 manual_account_test 默认来源')
assert.match(serviceSource, /resolveOpenAIRequestModelMapping/, '账户测试必须解析本次真实请求命中的模型映射')
assert.match(serviceSource, /modelMappingApplied/, '账户测试结果必须返回是否命中模型映射，便于前端终端诊断')
assert.match(serviceSource, /upstreamModel/, '账户测试结果必须返回实际上游模型，避免把请求模型误认为上游模型')
assert.match(serviceSource, /sourceEndpointFamily/, '账户测试结果必须返回映射下游协议入口')
assert.match(serviceSource, /upstreamEndpointFamily/, '账户测试结果必须返回映射上游协议入口')
const taskQueueSource = readFileSync(resolve('src/modules/accounts/account-test-task-queue.service.ts'), 'utf8')
assert.doesNotMatch(taskQueueSource, /requestShape:/, '管理端手动账号测试不得透传真实请求形态')
assert.doesNotMatch(taskQueueSource, /task\.clientCompatibility/, '管理端手动账号测试任务不得使用客户端画像作为测试请求形态')
assert.match(taskQueueSource, /testEndpointMode:\s*task\.testEndpointMode/, '管理端手动账号测试任务必须透传本次 testEndpointMode')

console.log('账号测试请求构造回归通过：endpoint mode、payload 字段、上游接口能力校验和真实网关编排边界均符合预期')
