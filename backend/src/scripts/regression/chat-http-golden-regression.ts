import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

type JsonObject = Record<string, unknown>

interface DtoContract {
  required?: Record<string, string>
  optional?: Record<string, string>
  discriminator?: string
  variants?: Record<string, { required: Record<string, string>; optional: Record<string, string> }>
  additionalProperties: boolean
}

interface SuccessContract {
  status: number
  envelope: string
  dto: string | null
  fixture?: unknown
  fixtureRef?: string
}

interface RouteContract {
  id: string
  method: string
  path: string
  frontend: string
  auth: string
  owner: JsonObject
  request: {
    path: Record<string, string>
    query: Record<string, string>
    body: { kind: string; fields: string[]; fixture?: unknown }
  }
  cursor: unknown
  success: SuccessContract[]
  errors: string[]
}

interface ErrorContract {
  status: number
  code: string | null
  envelope: string
  fixture: JsonObject
}

interface GoldenContract {
  contract: string
  version: number
  basePath: string
  frontendBasePath: string
  authority: JsonObject
  normalization: { tokens: Record<string, JsonObject> }
  envelopes: Record<string, JsonObject>
  enums: Record<string, string[]>
  dtos: Record<string, DtoContract>
  commonErrors: string[]
  errorCatalog: Record<string, ErrorContract>
  fixtures: Record<string, unknown>
  routes: RouteContract[]
  concerns: string[]
}

interface ExpectedRoute {
  id: string
  method: RouteContract['method']
  path: string
  frontend: string
  pathFields: string[]
  queryFields: string[]
  bodyKind: string
  bodyFields: string[]
  successes: string[]
  errors: string[]
}

const goldenPath = '../testdata/ai-chat-contract/v1/http.json'
const goldenText = readFileSync(goldenPath, 'utf8')
const golden = JSON.parse(goldenText) as GoldenContract
const routesSource = readFileSync('src/modules/chat/chat.routes.ts', 'utf8')
const repositorySource = readFileSync('src/storage/chat.repository.ts', 'utf8')
const assetRepositorySource = readFileSync('src/storage/chat-assets.repository.ts', 'utf8')
const assetUploadSource = readFileSync('src/modules/chat/chat-asset-upload.ts', 'utf8')
const contextBudgetSource = readFileSync('src/modules/chat/chat-context-budget.ts', 'utf8')
const assetInputSource = readFileSync('src/modules/chat/chat-asset-input.ts', 'utf8')
const modelContextSource = readFileSync('src/modules/chat/chat-model-context.ts', 'utf8')
const modelOptionsSource = readFileSync('src/modules/chat/chat-model-options.ts', 'utf8')
const imagePolicySource = readFileSync('src/modules/chat/chat-image-policy.ts', 'utf8')
const systemApiSource = readFileSync('src/modules/system-api/system-api-app.ts', 'utf8')
const dbAccessSource = readFileSync('src/modules/system-api/system-api-db-access.ts', 'utf8')
const rateLimitSource = readFileSync('src/modules/system-api/system-api-rate-limit.middleware.ts', 'utf8')
const authMiddlewareSource = readFileSync('src/modules/auth/auth.middleware.ts', 'utf8')
const frontendApiSource = readFileSync('../frontend/src/api/domains/chat.ts', 'utf8')
const frontendTypesSource = readFileSync('../frontend/src/types/domain/chat.ts', 'utf8')
const sourceCorpus = [routesSource, repositorySource, assetRepositorySource, assetUploadSource, contextBudgetSource, assetInputSource, modelContextSource, modelOptionsSource, imagePolicySource, systemApiSource, dbAccessSource, rateLimitSource, authMiddlewareSource].join('\n')

const expectedCommonErrors = [
  'auth_required',
  'session_expired',
  'must_change_password',
  'rate_limited',
  'system_api_busy',
  'internal_error'
]

const expectedRoutes: ExpectedRoute[] = [
  route('get-image-policy', 'GET', '/image-policy', 'chatApi.getImagePolicy', [], [], 'none', [], ['200:json-data:ChatImagePolicy'], []),
  route('list-conversations', 'GET', '/conversations', 'chatApi.listConversations', [], ['beforeIsPinned', 'beforeLastMessageAt', 'beforeId', 'limit'], 'none', [], ['200:json-data:ChatConversation[]'], []),
  route('create-conversation', 'POST', '/conversations', 'chatApi.createConversation', [], [], 'json-strict', ['apiKeyId'], ['201:json-data:ChatConversation'], ['request_body_invalid', 'request_body_too_large', 'chat_invalid_request', 'chat_conversation_limit_exceeded']),
  route('list-messages', 'GET', '/conversations/:conversationId/messages', 'chatApi.listMessages', ['conversationId'], ['beforeSequenceNo', 'afterSequenceNo', 'fromSequenceNo', 'limit'], 'none', [], ['200:json-data:ChatMessage[]'], ['chat_invalid_request']),
  route('get-conversation-sync', 'GET', '/conversations/:conversationId/sync', 'chatApi.getConversationSync', ['conversationId'], ['knownRevision'], 'none', [], ['200:json-data:ChatConversationSyncHead'], ['chat_invalid_request', 'chat_conversation_not_found']),
  route('get-submission-status', 'GET', '/conversations/:conversationId/submissions/:clientMessageId', 'chatApi.getSubmissionStatus', ['conversationId', 'clientMessageId'], [], 'none', [], ['200:json-data:ChatSubmissionPreparing', '200:json-data:ChatSubmissionNotFound', '200:json-data:ChatSubmissionAccepted'], ['chat_invalid_request', 'chat_conversation_not_found']),
  route('compact-context', 'POST', '/conversations/:conversationId/context/compactions', 'chatApi.compactContext', ['conversationId'], [], 'json-strict', ['model'], ['202:json-data:ChatCompactionAccepted', '202:json-data:ChatCompactionAccepted'], ['request_body_invalid', 'request_body_too_large', 'chat_invalid_request', 'chat_conversation_not_found', 'chat_message_in_progress', 'chat_context_compacting', 'chat_conversation_clearing', 'no_compactable_turn', 'chat_context_compaction_skipped', 'chat_context_compaction_failed', 'chat_model_capability_unavailable']),
  route('get-context-status', 'GET', '/conversations/:conversationId/context-status', 'chatApi.getContextStatus', ['conversationId'], [], 'none', [], ['200:json-data:ChatContextStatus'], ['conversation_not_found_uncoded']),
  route('clear-conversation', 'POST', '/conversations/:conversationId/clear', 'chatApi.clearConversation', ['conversationId'], [], 'json-strict', [], ['200:json-data:ChatConversation'], ['request_body_invalid', 'request_body_too_large', 'chat_invalid_request', 'chat_conversation_not_found', 'chat_message_in_progress', 'chat_context_compacting', 'chat_conversation_clearing']),
  route('get-conversation', 'GET', '/conversations/:conversationId', 'chatApi.getConversation', ['conversationId'], [], 'none', [], ['200:json-data:ChatConversation'], ['conversation_not_found_uncoded']),
  route('update-conversation', 'PATCH', '/conversations/:conversationId', 'chatApi.updateConversation', ['conversationId'], [], 'json-strict-at-least-one', ['title', 'isPinned', 'defaultImageModel'], ['200:json-data:ChatConversation'], ['request_body_invalid', 'request_body_too_large', 'chat_invalid_request', 'conversation_not_found_uncoded']),
  route('upload-asset', 'POST', '/conversations/:conversationId/assets', 'chatApi.uploadAsset', ['conversationId'], [], 'multipart-strict', ['file'], ['201:json-data:ChatAsset'], ['chat_conversation_not_found', 'chat_asset_invalid_request', 'chat_asset_count_exceeded', 'chat_asset_too_large', 'chat_asset_quota_exceeded', 'chat_asset_unsupported_type']),
  route('get-asset-content', 'GET', '/conversations/:conversationId/assets/:assetId/content', 'chatAssetContentUrl', ['conversationId', 'assetId'], ['variant', 'download'], 'none', [], ['200:binary:-', '304:empty:-'], ['chat_invalid_request', 'chat_conversation_not_found', 'asset_not_found_uncoded']),
  route('delete-asset', 'DELETE', '/conversations/:conversationId/assets/:assetId', 'chatApi.deleteAsset', ['conversationId', 'assetId'], [], 'none', [], ['204:empty:-'], ['chat_conversation_not_found', 'chat_asset_not_deletable']),
  route('list-models', 'GET', '/conversations/:conversationId/models', 'chatApi.listModels', ['conversationId'], [], 'none', [], ['200:json-data:ChatModelListOption[]'], ['chat_conversation_not_found']),
  route('get-model-capabilities', 'GET', '/conversations/:conversationId/models/:modelId', 'chatApi.getModelCapabilities', ['conversationId', 'modelId'], [], 'none', [], ['200:json-data:ChatModelCapabilities'], ['chat_conversation_not_found', 'chat_model_not_found']),
  route('stream-message', 'POST', '/conversations/:conversationId/stream', 'streamChatMessage', ['conversationId'], [], 'json-strict', ['clientMessageId', 'replaceTurnId', 'content', 'contentBlocks', 'model', 'reasoningEffort', 'serviceTier', 'generationParameters'], ['200:sse:-'], ['request_body_invalid', 'request_body_too_large', 'chat_invalid_request', 'chat_message_too_large', 'chat_conversation_not_found', 'chat_message_already_exists', 'chat_turn_limit_exceeded', 'chat_message_in_progress', 'chat_replace_conflict', 'chat_context_compacting', 'chat_conversation_clearing', 'chat_storage_quota_exceeded', 'chat_image_not_supported', 'chat_request_body_too_large', 'chat_input_exceeds_context', 'chat_asset_unavailable', 'chat_model_capability_mismatch', 'chat_context_unavailable', 'chat_preparation_canceled']),
  route('stop-message', 'POST', '/conversations/:conversationId/stop', 'chatApi.stop', ['conversationId'], [], 'json-strict-at-least-one', ['turnId', 'clientMessageId'], ['202:json-data:ChatStopResult', '202:json-data:ChatStopResult', '202:json-data:ChatStopResult'], ['request_body_invalid', 'request_body_too_large', 'chat_invalid_request', 'chat_conversation_not_found', 'chat_generation_not_found', 'chat_turn_mismatch']),
  route('attach-stream', 'GET', '/conversations/:conversationId/streams/:turnId', 'attachChatStream', ['conversationId', 'turnId'], [], 'none', [], ['200:sse:-'], ['chat_conversation_not_found', 'chat_stream_terminal', 'chat_stream_runner_missing']),
  route('delete-conversation', 'DELETE', '/conversations/:conversationId', 'chatApi.deleteConversation', ['conversationId'], [], 'none', [], ['204:empty:-'], ['conversation_not_found_uncoded'])
]

const expectedDtoFields: Record<string, { required: string[]; optional: string[] }> = {
  ChatImageOptimizationPolicy: fields(['mimeType', 'maxEdge', 'quality', 'maxBytes']),
  ChatImagePolicy: fields(['input']),
  ChatModelListOption: fields(['id', 'name']),
  ChatModelCapabilities: fields(['id', 'name', 'supportsPromptCaching', 'supportedReasoningEfforts', 'supportedServiceTiers', 'supportedApiProtocols', 'inputModalities', 'outputModalities', 'supportedTools', 'generationParameters'], ['defaultReasoningEffort', 'contextWindowTokens', 'maxInputTokens', 'maxOutputTokens']),
  ChatGenerationParameters: fields([], ['temperature', 'topP', 'frequencyPenalty', 'presencePenalty', 'maxOutputTokens', 'seed']),
  ChatConversation: fields(['id', 'systemAccountId', 'apiKeyNameSnapshot', 'title', 'isPinned', 'defaultImageModel', 'userTurnCount', 'messageRevision', 'userTurnLimit', 'lastMessageAt', 'createdAt', 'updatedAt'], ['apiKeyId', 'defaultModel', 'lastModel', 'toolCapabilities', 'activeTurnId']),
  ChatToolEvent: fields(['id', 'type', 'status'], ['item']),
  ChatMessage: fields(['id', 'conversationId', 'turnId', 'sequenceNo', 'role', 'status', 'contentText', 'contentBlocks', 'model', 'createdAt', 'expiresAt'], ['clientMessageId', 'traceId', 'finishReason', 'errorCode', 'errorMessage', 'completedAt', 'reasoningText', 'toolEvents', 'eventVersion', 'renderRevision']),
  ChatAsset: fields(['id', 'fileName', 'mimeType', 'width', 'height', 'byteSize']),
  ChatContextStatus: fields(['usedTokens', 'ratio', 'state', 'usageEstimated', 'compactedThroughSequence', 'revision', 'attemptCount'], ['limitTokens', 'errorCode', 'retryAt']),
  ChatMessageTail: fields(['id', 'turnId', 'sequenceNo', 'role', 'status', 'expiresAt'], ['completedAt']),
  ChatConversationActiveTurn: fields(['turnId', 'assistantMessageId', 'startedAt']),
  ChatConversationSyncHead: fields(['serverTime', 'unchanged', 'conversationId', 'messageRevision', 'lastSequenceNo', 'tail'], ['activeTurn']),
  ChatSubmissionPreparing: fields(['state', 'phase', 'serverTime']),
  ChatSubmissionNotFound: fields(['state', 'serverTime']),
  ChatSubmissionAccepted: fields(['state', 'turnId', 'assistantMessageId', 'assistantStatus', 'runnerState', 'serverTime'], ['eventVersion', 'lastSemanticActivityAt', 'errorCode', 'errorMessage', 'completedAt']),
  ChatCompactionAccepted: fields(['state', 'serverTime']),
  ChatStopResult: fields(['stopped'], ['preparationPhase', 'turnId', 'state', 'assistantStatus'])
}

function main(): void {
assert.equal(golden.contract, 'ai-chat-http')
assert.equal(golden.version, 1)
assert.equal(golden.basePath, '/__aisys__/api/my-chat')
assert.equal(golden.frontendBasePath, '/my-chat')
assert.deepEqual(golden.commonErrors, expectedCommonErrors)
assert.equal(golden.routes.length, 20, 'AI Chat HTTP golden 必须冻结全部 20 条路由')
assert(golden.concerns.some((item) => item.includes('000070')), '两条模型路由必须记录 master 000070 的 HTTP 影响判断')

const actualRouteKeys = [...routesSource.matchAll(/chatRouter\.(get|post|patch|delete)\('([^']+)'/g)]
  .map((match) => `${match[1]!.toUpperCase()} ${match[2]}`)
const expectedRouteKeys = expectedRoutes.map((item) => `${item.method} ${item.path}`)
assert.deepEqual(actualRouteKeys, expectedRouteKeys, 'chat.routes.ts 的路由集合或顺序已偏离 HTTP golden')
assert.deepEqual(golden.routes.map((item) => `${item.method} ${item.path}`), expectedRouteKeys, 'http.json 的路由集合或顺序不完整')

for (const expected of expectedRoutes) {
  const actual = golden.routes.find((item) => item.id === expected.id)
  assert(actual, `http.json 缺少路由 ${expected.id}`)
  assert.equal(actual.method, expected.method, `${expected.id} method 漂移`)
  assert.equal(actual.path, expected.path, `${expected.id} path 漂移`)
  assert.equal(actual.frontend, expected.frontend, `${expected.id} frontend binding 漂移`)
  assert.equal(actual.auth, 'session', `${expected.id} 必须要求 session`)
  assert.deepEqual(Object.keys(actual.request.path), expected.pathFields, `${expected.id} path fields 漂移`)
  assert.deepEqual(Object.keys(actual.request.query), expected.queryFields, `${expected.id} query fields 漂移`)
  assert.equal(actual.request.body.kind, expected.bodyKind, `${expected.id} body kind 漂移`)
  assert.deepEqual(actual.request.body.fields.map(baseFieldName), expected.bodyFields, `${expected.id} body fields 漂移`)
  assert.deepEqual(actual.success.map(successSignature), expected.successes, `${expected.id} success status/envelope/DTO 漂移`)
  assert.deepEqual(actual.errors, expected.errors, `${expected.id} error contract 漂移`)
  assert.equal(typeof actual.owner.scope, 'string', `${expected.id} 缺少 owner scope`)
  assert.equal(typeof actual.owner.source, 'string', `${expected.id} 缺少 owner source`)
}

assert.deepEqual(cursorSummary(golden, 'list-conversations'), {
  kind: 'composite-before', fields: ['beforeIsPinned', 'beforeLastMessageAt', 'beforeId'], limit: { default: 30, min: 1, max: 50 }
})
assert.deepEqual(cursorSummary(golden, 'list-messages'), {
  kind: 'exclusive-one-of', fields: ['beforeSequenceNo', 'afterSequenceNo', 'fromSequenceNo'], limit: { default: 100, min: 1, max: 100 }
})
assert.deepEqual(cursorSummary(golden, 'get-conversation-sync'), {
  kind: 'revision', fields: ['knownRevision'], unchanged: 'true only when knownRevision === messageRevision'
})
for (const item of golden.routes.filter((route) => !['list-conversations', 'list-messages', 'get-conversation-sync'].includes(route.id))) {
  assert.equal(item.cursor, null, `${item.id} 不应伪造分页/同步游标`)
}

for (const [name, expected] of Object.entries(expectedDtoFields)) {
  const dto = golden.dtos[name]
  assert(dto, `缺少 DTO ${name}`)
  assert.equal(dto.additionalProperties, false, `${name} 必须禁止额外字段`)
  assert.deepEqual(Object.keys(dto.required ?? {}), expected.required, `${name} required fields 漂移`)
  assert.deepEqual(Object.keys(dto.optional ?? {}), expected.optional, `${name} optional fields 漂移`)
}
assert.deepEqual(Object.keys(golden.dtos.ChatMessageContentBlock.variants ?? {}), ['output_text', 'reasoning', 'tool_call', 'output_image', 'input_text', 'input_image'])
for (const [variantName, variant] of Object.entries(golden.dtos.ChatMessageContentBlock.variants ?? {})) {
  assert.equal(variant.required.type, `literal:${variantName}`, `ChatMessageContentBlock.${variantName} discriminator 漂移`)
}

assert.deepEqual(golden.enums.ChatMessageRole, ['user', 'assistant'])
assert.deepEqual(golden.enums.ChatMessageStatus, ['completed', 'streaming', 'failed', 'canceled'])
assert.deepEqual(golden.enums.ChatImageModel, ['gpt-image-2'])
assert.deepEqual(golden.enums.ChatProcessStatus, ['started', 'completed', 'failed', 'canceled'])
assert.deepEqual(golden.enums.ChatToolStatus, ['started', 'updated', 'completed', 'failed', 'canceled'])
assert.deepEqual(golden.enums.ChatReasoningEffort, ['minimal', 'low', 'medium', 'high', 'xhigh', 'max'])
assert.deepEqual(golden.enums.ChatServiceTier, ['default', 'priority', 'flex'])
assert.deepEqual(golden.enums.ChatGenerationParameter, ['temperature', 'topP', 'frequencyPenalty', 'presencePenalty', 'maxOutputTokens', 'seed'])

for (const errorName of new Set([...golden.commonErrors, ...golden.routes.flatMap((item) => item.errors)])) {
  const error = golden.errorCatalog[errorName]
  assert(error, `errorCatalog 缺少 ${errorName}`)
  assert(golden.envelopes[error.envelope], `${errorName} 引用了未知 envelope ${error.envelope}`)
  validateEnvelope(error.fixture, error.envelope, `${errorName}.fixture`)
  if (errorName === 'chat_asset_count_exceeded') {
    assert.match(assetRepositorySource, /每条消息最多 \$\{maxChatAssetsPerMessage\} 张图片，请移除图片后重试/)
    assert.equal(error.fixture.message, '每条消息最多 5 张图片，请移除图片后重试')
  } else {
    assert(sourceCorpus.includes(String(error.fixture.message)), `${errorName} fixture message 不是当前 Node 实现中的真实样例`)
  }
  if (error.code === null) {
    assert.equal(Object.hasOwn(error.fixture, 'code'), false, `${errorName} 不得伪造 code`)
  } else {
    assert.equal(error.fixture.code, error.code, `${errorName} fixture code 漂移`)
    assert(sourceCorpus.includes(error.code), `${errorName} 的机器码未在当前 Node 权威实现中出现`)
  }
}

for (const routeContract of golden.routes) {
  for (const success of routeContract.success) {
    assert(golden.envelopes[success.envelope], `${routeContract.id} 引用了未知 success envelope`)
    if (success.envelope === 'json-data') {
      const fixture = resolveSuccessFixture(golden, success)
      assert(fixture !== undefined, `${routeContract.id} 的 JSON success 缺少真实样例`)
      validateEnvelope(fixture, success.envelope, `${routeContract.id}.success`)
      assert(success.dto, `${routeContract.id} 的 JSON success 缺少 DTO`)
      validateDtoReference((fixture as JsonObject).data, success.dto, `${routeContract.id}.success.data`)
    } else {
      assert.equal(success.dto, null, `${routeContract.id} 的 ${success.envelope} success 不应绑定 JSON DTO`)
    }
  }
}

const declaredTokens = golden.normalization.tokens
for (const [token, definition] of Object.entries(declaredTokens)) {
  assert.match(token, /^<[a-z][a-z0-9-]*>$/)
  assert(['id', 'time'].includes(String(definition.kind)), `${token} 只能规范化动态 ID 或时间`)
}
const usedTokens = [...new Set(goldenText.match(/<[a-z][a-z0-9-]*>/g) ?? [])]
assert.deepEqual(usedTokens.sort(), Object.keys(declaredTokens).sort(), '所有且仅有明确声明的 ID/时间 token 才能用于 golden')

assert.match(systemApiSource, /app\.use\(`\$\{systemApiPrefix\}\/my-chat`, requireAuth,[\s\S]{0,320}forceSelfAccessScope, chatRouter\)/, 'AI Chat 必须挂在 requireAuth 与 forceSelfAccessScope 后')
assert.match(authMiddlewareSource, /delete \(req\.query as Record<string, unknown>\)\.systemAccountId[\s\S]{0,120}role: 'user'/, 'self scope 必须删除 systemAccountId 并降为 user role')
assert.match(systemApiSource, /chatSystemApiJsonBodyLimit = '24mb'/, 'AI Chat JSON parser 上限漂移')
assert.match(systemApiSource, /statusCode === 413 \? '请求体过大' : '请求体无效'/, 'JSON parser error envelope 漂移')
assert.match(rateLimitSource, /status\(429\)\.json\(\{ message: '请求过于频繁，请稍后重试' \}\)/, 'rate limit envelope 漂移')
assert.match(dbAccessSource, /code: 'system_api_busy'/, 'DB admission code 漂移')
assert.match(systemApiSource, /status\(500\)\.json\(\{ message: '服务器内部错误' \}\)/, 'global error envelope 漂移')

for (const [id, pattern] of Object.entries(frontendRouteMarkers)) {
  assert.match(frontendApiSource, pattern, `${id} 的前端请求路径/方法漂移`)
}

for (const [name, expected] of Object.entries(frontendInterfaceFields)) {
  assert.deepEqual(extractInterfaceFields(frontendTypesSource, name), expected, `frontend ${name} 字段或 optional 性漂移`)
}
for (const [name, expected] of Object.entries(backendInterfaceFields)) {
  const source = name === 'ChatAssetApiMetadata' ? assetRepositorySource : repositorySource
  assert.deepEqual(extractInterfaceFields(source, name), expected, `backend ${name} 字段或 optional 性漂移`)
}
for (const [name, expected] of Object.entries(frontendInterfaceTypes)) {
  assert.deepEqual(extractInterfaceMemberTypes(frontendTypesSource, name), expected, `frontend ${name} 字段类型漂移`)
}
for (const [name, expected] of Object.entries(backendInterfaceTypes)) {
  const source = name === 'ChatAssetApiMetadata' ? assetRepositorySource : repositorySource
  assert.deepEqual(extractInterfaceMemberTypes(source, name), expected, `backend ${name} 字段类型漂移`)
}

assert.deepEqual(extractStringUnion(frontendTypesSource, 'ChatMessageRole'), golden.enums.ChatMessageRole)
assert.deepEqual(extractStringUnion(frontendTypesSource, 'ChatMessageStatus'), golden.enums.ChatMessageStatus)
assert.deepEqual(extractStringUnion(frontendTypesSource, 'ChatImageModel'), golden.enums.ChatImageModel)
assert.deepEqual(extractStringUnion(frontendTypesSource, 'ChatProcessStatus'), golden.enums.ChatProcessStatus)
assert.deepEqual(
  [...extractStringUnion(frontendTypesSource, 'ChatProcessStatus'), ...extractStringUnion(frontendTypesSource, 'ChatToolStatus')].sort(),
  [...golden.enums.ChatToolStatus].sort()
)
assert.deepEqual(extractStringUnion(frontendTypesSource, 'ChatReasoningEffort'), golden.enums.ChatReasoningEffort)
assert.deepEqual(extractStringUnion(frontendTypesSource, 'ChatServiceTier'), golden.enums.ChatServiceTier)
assert.deepEqual(extractStringUnion(frontendTypesSource, 'ChatGenerationParameter'), golden.enums.ChatGenerationParameter)

for (const [label, pattern] of sourceGuards) assert.match(routesSource, pattern, label)
assert.match(repositorySource, /export type ChatMessageRole = 'user' \| 'assistant'/)
assert.match(repositorySource, /export type ChatMessageStatus = 'completed' \| 'streaming' \| 'failed' \| 'canceled'/)
assert.match(repositorySource, /contentBlocks: parseContentBlocks\(row\.content_blocks_json\)/, '消息 HTTP DTO 的 contentBlocks 必须来自 repository 映射')
assert.match(routesSource, /chatRouter\.get\('\/conversations\/:conversationId\/messages'[\s\S]{0,700}listChatMessages\(/, '消息列表仍必须走 repository owner lookup')
assert.match(repositorySource, /async function requireConversation[\s\S]{0,360}if \(!row\) throw new Error\('会话不存在'\)/, '消息列表 owner miss 的当前 generic 500 concern 已变化，需显式更新 golden')
assert.match(assetRepositorySource, /return \{[\s\S]{0,220}id: asset\.id,[\s\S]{0,220}byteSize: asset\.processedBytes/, '资产上传 DTO 字段漂移')
assert.match(modelOptionsSource, /export const chatReasoningEfforts = \['minimal', 'low', 'medium', 'high', 'xhigh', 'max'\]/)
assert.match(modelOptionsSource, /export const chatServiceTiers = \['default', 'priority', 'flex'\]/)
assert.match(imagePolicySource, /mimeType: 'image\/webp'[\s\S]{0,120}maxEdge: 1024[\s\S]{0,120}quality: 82[\s\S]{0,120}maxBytes: 3 \* 1024 \* 1024/)
assert.match(routesSource, /const maxInternalChatRequestBytes = 21 \* 1024 \* 1024/, '五张各 3 MiB 图片的 Base64 模型请求必须保留传输余量')
assert.match(readFileSync(new URL('../../modules/chat/chat-asset-input.ts', import.meta.url), 'utf8'), /const maxChatImagesPerTurn = 5[\s\S]{0,80}const maxChatModelImageBytesPerTurn = 15 \* 1024 \* 1024/, '单轮最多五张图片时，处理后图片总字节必须允许 15 MiB')

console.log(`AI Chat HTTP golden contract regression passed (${golden.routes.length} routes, ${Object.keys(golden.dtos).length} DTOs)`)
}

function route(
  id: string,
  method: string,
  path: string,
  frontend: string,
  pathFields: string[],
  queryFields: string[],
  bodyKind: string,
  bodyFields: string[],
  successes: string[],
  errors: string[]
): ExpectedRoute {
  return { id, method, path, frontend, pathFields, queryFields, bodyKind, bodyFields, successes, errors }
}

function fields(required: string[], optional: string[] = []): { required: string[]; optional: string[] } {
  return { required, optional }
}

function baseFieldName(value: string): string {
  return value.split(/[? (]/, 1)[0] ?? value
}

function successSignature(success: SuccessContract): string {
  return `${success.status}:${success.envelope}:${success.dto ?? '-'}`
}

function cursorSummary(contract: GoldenContract, routeId: string): unknown {
  const cursor = contract.routes.find((item) => item.id === routeId)?.cursor
  assert(cursor && typeof cursor === 'object' && !Array.isArray(cursor), `${routeId} cursor 缺失`)
  const source = cursor as JsonObject
  if (routeId === 'get-conversation-sync') return { kind: source.kind, fields: source.fields, unchanged: source.unchanged }
  return { kind: source.kind, fields: source.fields, limit: source.limit }
}

function resolveSuccessFixture(contract: GoldenContract, success: SuccessContract): unknown {
  if (success.fixture !== undefined) return success.fixture
  if (!success.fixtureRef) return undefined
  if (success.fixtureRef === 'conversation-array') return { data: [contract.fixtures.conversation] }
  if (success.fixtureRef === 'message-array') return { data: [contract.fixtures.userMessage, contract.fixtures.assistantMessage] }
  return { data: contract.fixtures[success.fixtureRef] }
}

function validateEnvelope(value: unknown, envelopeName: string, path: string): void {
  const envelope = golden.envelopes[envelopeName]
  assert(envelope, `${path}: unknown envelope ${envelopeName}`)
  if (envelopeName === 'empty' || envelopeName === 'binary' || envelopeName === 'sse') return
  const object = asObject(value, path)
  const required = stringArray(envelope.required)
  const optional = stringArray(envelope.optional)
  assert.deepEqual(Object.keys(object).sort(), [...required, ...optional.filter((key) => Object.hasOwn(object, key))].sort(), `${path}: envelope fields 漂移`)
  for (const field of required) assert(Object.hasOwn(object, field), `${path}: missing envelope field ${field}`)
}

function validateDtoReference(value: unknown, reference: string, path: string): void {
  if (reference.endsWith('[]')) {
    assert(Array.isArray(value), `${path}: expected array`)
    const itemReference = reference.slice(0, -2)
    value.forEach((item, index) => validateDto(item, itemReference, `${path}[${index}]`))
    return
  }
  validateDto(value, reference, path)
}

function validateDto(value: unknown, dtoName: string, path: string): void {
  const dto = golden.dtos[dtoName]
  assert(dto, `${path}: unknown DTO ${dtoName}`)
  if (dto.variants) {
    const object = asObject(value, path)
    const discriminator = dto.discriminator ?? 'type'
    const variantName = String(object[discriminator] ?? '')
    const variant = dto.variants[variantName]
    assert(variant, `${path}: unknown ${dtoName} variant ${variantName}`)
    validateObjectContract(object, variant.required, variant.optional, path)
    return
  }
  validateObjectContract(asObject(value, path), dto.required ?? {}, dto.optional ?? {}, path)
}

function validateObjectContract(object: JsonObject, required: Record<string, string>, optional: Record<string, string>, path: string): void {
  const allowed = [...Object.keys(required), ...Object.keys(optional)]
  assert.deepEqual(Object.keys(object).sort(), Object.keys(object).filter((key) => allowed.includes(key)).sort(), `${path}: contains additional fields`)
  for (const [field, type] of Object.entries(required)) {
    assert(Object.hasOwn(object, field), `${path}: missing ${field}`)
    validateType(object[field], type, `${path}.${field}`)
  }
  for (const [field, type] of Object.entries(optional)) {
    if (Object.hasOwn(object, field)) validateType(object[field], type, `${path}.${field}`)
  }
}

function validateType(value: unknown, type: string, path: string): void {
  if (type === 'string') return void assert.equal(typeof value, 'string', `${path}: expected string`)
  if (type === 'id') return void assert(typeof value === 'string' && value.length > 0, `${path}: expected non-empty opaque ID`)
  if (type === 'timestamp') return void assert(typeof value === 'string' && (value === '<timestamp>' || Number.isFinite(Date.parse(value))), `${path}: expected timestamp`)
  if (type === 'integer') return void assert(Number.isSafeInteger(value), `${path}: expected safe integer`)
  if (type === 'number') return void assert(typeof value === 'number' && Number.isFinite(value), `${path}: expected finite number`)
  if (type === 'boolean') return void assert.equal(typeof value, 'boolean', `${path}: expected boolean`)
  if (type === 'object') return void asObject(value, path)
  if (type === 'literal:true') return void assert.equal(value, true, `${path}: expected true`)
  if (type.startsWith('literal:')) return void assert.equal(value, type.slice('literal:'.length), `${path}: literal mismatch`)
  if (type.startsWith('enum-inline:')) return void assert(type.slice('enum-inline:'.length).split('|').includes(String(value)), `${path}: enum mismatch`)
  if (type.startsWith('enum:')) return void assert(golden.enums[type.slice('enum:'.length)]?.includes(String(value)), `${path}: enum mismatch`)
  if (type.startsWith('dto:')) return validateDto(value, type.slice('dto:'.length), path)
  if (type === 'array:string') return void assert(Array.isArray(value) && value.every((item) => typeof item === 'string'), `${path}: expected string[]`)
  if (type.startsWith('array-enum:')) {
    const values = golden.enums[type.slice('array-enum:'.length)] ?? []
    return void assert(Array.isArray(value) && value.every((item) => values.includes(String(item))), `${path}: enum array mismatch`)
  }
  if (type.startsWith('array-dto:')) {
    assert(Array.isArray(value), `${path}: expected DTO array`)
    const itemDto = type.slice('array-dto:'.length)
    value.forEach((item, index) => validateDto(item, itemDto, `${path}[${index}]`))
    return
  }
  assert.fail(`${path}: unsupported type contract ${type}`)
}

function asObject(value: unknown, path: string): JsonObject {
  assert(value && typeof value === 'object' && !Array.isArray(value), `${path}: expected object`)
  return value as JsonObject
}

function stringArray(value: unknown): string[] {
  assert(Array.isArray(value) && value.every((item) => typeof item === 'string'))
  return value
}

function extractInterfaceFields(source: string, interfaceName: string): { required: string[]; optional: string[] } {
  const shape = extractInterfaceMemberTypes(source, interfaceName)
  return {
    required: Object.entries(shape).filter(([, type]) => !type.startsWith('?')).map(([field]) => field),
    optional: Object.entries(shape).filter(([, type]) => type.startsWith('?')).map(([field]) => field)
  }
}

function extractInterfaceMemberTypes(source: string, interfaceName: string): Record<string, string> {
  const marker = `export interface ${interfaceName}`
  const markerIndex = source.search(new RegExp(`${marker}(?=\\s*\\{)`, 'm'))
  assert(markerIndex >= 0, `interface ${interfaceName} not found`)
  const openIndex = source.indexOf('{', markerIndex + marker.length)
  assert(openIndex >= 0)
  const closeIndex = matchingBrace(source, openIndex)
  const body = source.slice(openIndex + 1, closeIndex)
  const result: Record<string, string> = {}
  for (const member of topLevelMembers(body)) {
    const match = member.match(/^\s*([A-Za-z_$][\w$]*)(\?)?\s*:\s*([\s\S]+)$/)
    if (!match) continue
    result[match[1]!] = `${match[2] ? '?' : ''}${normalizeTypeText(match[3]!)}`
  }
  return result
}

function normalizeTypeText(value: string): string {
  return value.replace(/[\s;]/g, '')
}

function matchingBrace(source: string, openIndex: number): number {
  let depth = 0
  for (let index = openIndex; index < source.length; index += 1) {
    if (source[index] === '{') depth += 1
    else if (source[index] === '}') {
      depth -= 1
      if (depth === 0) return index
    }
  }
  assert.fail('unclosed brace')
}

function topLevelMembers(body: string): string[] {
  const members: string[] = []
  let current = ''
  let braceDepth = 0
  let angleDepth = 0
  for (const char of body) {
    if (char === '{' || char === '(' || char === '[') braceDepth += 1
    else if (char === '}' || char === ')' || char === ']') braceDepth -= 1
    else if (char === '<') angleDepth += 1
    else if (char === '>') angleDepth = Math.max(0, angleDepth - 1)
    if ((char === '\n' || char === ';') && braceDepth === 0 && angleDepth === 0) {
      if (current.trim()) members.push(current.trim())
      current = ''
    } else current += char
  }
  if (current.trim()) members.push(current.trim())
  return members
}

function extractStringUnion(source: string, typeName: string): string[] {
  const match = source.match(new RegExp(`export type ${typeName}\\s*=\\s*([^\\n]+)`))
  assert(match, `type ${typeName} not found`)
  return [...match[1]!.matchAll(/'([^']+)'/g)].map((item) => item[1]!)
}

const frontendInterfaceFields: Record<string, { required: string[]; optional: string[] }> = {
  ChatConversation: fields(['id', 'systemAccountId', 'apiKeyNameSnapshot', 'title', 'isPinned', 'defaultImageModel', 'userTurnCount', 'messageRevision', 'userTurnLimit', 'lastMessageAt', 'createdAt', 'updatedAt'], ['apiKeyId', 'defaultModel', 'lastModel', 'toolCapabilities', 'activeTurnId']),
  ChatMessage: fields(['id', 'conversationId', 'turnId', 'sequenceNo', 'role', 'status', 'contentText', 'model', 'createdAt', 'expiresAt'], ['clientMessageId', 'contentBlocks', 'traceId', 'finishReason', 'errorCode', 'errorMessage', 'completedAt', 'reasoningText', 'toolEvents', 'eventVersion', 'renderRevision']),
  ChatToolEvent: fields(['id', 'type', 'status'], ['item']),
  ChatAsset: fields(['id', 'fileName', 'mimeType', 'width', 'height', 'byteSize']),
  ChatImageOptimizationPolicy: fields(['mimeType', 'maxEdge', 'quality', 'maxBytes']),
  ChatImagePolicy: fields(['input']),
  ChatContextStatus: fields(['usedTokens', 'ratio', 'state', 'usageEstimated', 'compactedThroughSequence', 'revision', 'attemptCount'], ['limitTokens', 'errorCode', 'retryAt']),
  ChatMessageTail: fields(['id', 'turnId', 'sequenceNo', 'role', 'status', 'expiresAt'], ['completedAt']),
  ChatConversationActiveTurn: fields(['turnId', 'assistantMessageId', 'startedAt']),
  ChatConversationSyncHead: fields(['serverTime', 'unchanged', 'conversationId', 'messageRevision', 'lastSequenceNo', 'tail'], ['activeTurn']),
  ChatModelListOption: fields(['id', 'name']),
  ChatModelCapabilities: fields(['id', 'name', 'supportsPromptCaching', 'supportedReasoningEfforts', 'supportedServiceTiers', 'supportedApiProtocols', 'inputModalities', 'outputModalities', 'supportedTools', 'generationParameters'], ['defaultReasoningEffort', 'contextWindowTokens', 'maxInputTokens', 'maxOutputTokens'])
}

const backendInterfaceFields: Record<string, { required: string[]; optional: string[] }> = {
  ChatConversation: fields(['id', 'systemAccountId', 'apiKeyNameSnapshot', 'title', 'isPinned', 'defaultImageModel', 'userTurnCount', 'messageRevision', 'lastMessageAt', 'createdAt', 'updatedAt'], ['apiKeyId', 'lastModel', 'activeTurnId']),
  ChatMessage: fields(['id', 'conversationId', 'turnId', 'sequenceNo', 'role', 'status', 'contentText', 'contentBlocks', 'model', 'createdAt', 'expiresAt'], ['clientMessageId', 'traceId', 'finishReason', 'errorCode', 'errorMessage', 'completedAt']),
  ChatConversationSyncMessage: fields(['id', 'turnId', 'sequenceNo', 'role', 'status', 'expiresAt'], ['completedAt']),
  ChatConversationSyncHead: fields(['conversationId', 'messageRevision', 'lastSequenceNo', 'tail'], ['activeTurn']),
  ChatTurnSubmissionFact: fields(['turnId', 'assistantMessageId', 'assistantStatus'], ['errorCode', 'errorMessage', 'completedAt', 'traceId']),
  ChatAssetApiMetadata: fields(['id', 'fileName', 'mimeType', 'width', 'height', 'byteSize'])
}

const frontendInterfaceTypes: Record<string, Record<string, string>> = {
  ChatConversation: {
    id: 'string', systemAccountId: 'string', apiKeyId: '?string', apiKeyNameSnapshot: 'string', defaultModel: '?ChatModelListOption',
    title: 'string', isPinned: 'boolean', lastModel: '?string', defaultImageModel: 'ChatImageModel', toolCapabilities: '?ChatConversationToolCapabilities', activeTurnId: '?string',
    userTurnCount: 'number', messageRevision: 'number', userTurnLimit: 'number', lastMessageAt: 'string', createdAt: 'string', updatedAt: 'string'
  },
  ChatMessage: {
    id: 'string', conversationId: 'string', turnId: 'string', sequenceNo: 'number', clientMessageId: '?string', role: 'ChatMessageRole',
    status: 'ChatMessageStatus', contentText: 'string', contentBlocks: '?ChatMessageContentBlock[]', model: 'string', traceId: '?string',
    finishReason: '?string', errorCode: '?string', errorMessage: '?string', createdAt: 'string', completedAt: '?string', expiresAt: 'string',
    reasoningText: '?string', toolEvents: '?ChatToolEvent[]', eventVersion: '?number', renderRevision: '?number'
  },
  ChatAsset: { id: 'string', fileName: 'string', mimeType: 'string', width: 'number', height: 'number', byteSize: 'number' },
  ChatImageOptimizationPolicy: { mimeType: "'image/webp'", maxEdge: 'number', quality: 'number', maxBytes: 'number' },
  ChatImagePolicy: { input: 'ChatImageOptimizationPolicy' },
  ChatContextStatus: {
    usedTokens: 'number', limitTokens: '?number', ratio: 'number', state: "'ready'|'compact_pending'|'compacting'|'compact_failed'",
    usageEstimated: 'boolean', compactedThroughSequence: 'number', revision: 'number', errorCode: '?string', retryAt: '?string', attemptCount: 'number'
  },
  ChatConversationSyncHead: {
    serverTime: 'string', unchanged: 'boolean', conversationId: 'string', messageRevision: 'number', lastSequenceNo: 'number',
    activeTurn: '?ChatConversationActiveTurn', tail: 'ChatMessageTail[]'
  },
  ChatModelCapabilities: {
    id: 'string', name: 'string', supportsPromptCaching: 'boolean', supportedReasoningEfforts: 'ChatReasoningEffort[]',
    defaultReasoningEffort: '?ChatReasoningEffort', supportedServiceTiers: 'ChatServiceTier[]', contextWindowTokens: '?number',
    maxInputTokens: '?number', maxOutputTokens: '?number', supportedApiProtocols: 'string[]', inputModalities: 'string[]',
    outputModalities: 'string[]', supportedTools: 'string[]', generationParameters: 'ChatGenerationParameterCapability[]'
  }
}

const backendInterfaceTypes: Record<string, Record<string, string>> = {
  ChatConversation: {
    id: 'string', systemAccountId: 'string', apiKeyId: '?string', apiKeyNameSnapshot: 'string', title: 'string', isPinned: 'boolean',
    lastModel: '?string', defaultImageModel: 'ChatImageModel', activeTurnId: '?string', userTurnCount: 'number', messageRevision: 'number',
    lastMessageAt: 'string', createdAt: 'string', updatedAt: 'string'
  },
  ChatMessage: {
    id: 'string', conversationId: 'string', turnId: 'string', sequenceNo: 'number', clientMessageId: '?string', role: 'ChatMessageRole',
    status: 'ChatMessageStatus', contentText: 'string', contentBlocks: 'ChatMessageContentBlock[]', model: 'string', traceId: '?string',
    finishReason: '?string', errorCode: '?string', errorMessage: '?string', createdAt: 'string', completedAt: '?string', expiresAt: 'string'
  },
  ChatConversationSyncHead: {
    conversationId: 'string', messageRevision: 'number', lastSequenceNo: 'number',
    activeTurn: '?{turnId:stringassistantMessageId:stringstartedAt:string}', tail: 'ChatConversationSyncMessage[]'
  },
  ChatTurnSubmissionFact: {
    turnId: 'string', assistantMessageId: 'string', assistantStatus: 'ChatMessageStatus', errorCode: '?string', errorMessage: '?string', completedAt: '?string', traceId: '?string'
  },
  ChatAssetApiMetadata: {
    id: 'string', fileName: 'string', mimeType: 'ChatAssetProcessedMimeType', width: 'number', height: 'number', byteSize: 'number'
  }
}

const frontendRouteMarkers: Record<string, RegExp> = {
  'get-image-policy': /getImagePolicy:[\s\S]{0,120}http\.get\('\/my-chat\/image-policy'\)/,
  'list-conversations': /listConversations:[\s\S]{0,220}http\.get\('\/my-chat\/conversations'/,
  'create-conversation': /createConversation:[\s\S]{0,220}http\.post\('\/my-chat\/conversations'/,
  'get-conversation': /getConversation:[\s\S]{0,180}http\.get\(`\/my-chat\/conversations\/\$\{encodeURIComponent\(conversationId\)\}`\)/,
  'list-messages': /listMessages:[\s\S]{0,220}\/messages`, \{ params \}/,
  'get-conversation-sync': /getConversationSync:[\s\S]{0,260}\/sync`, \{ params: \{ knownRevision:/,
  'get-submission-status': /getSubmissionStatus:[\s\S]{0,260}\/submissions\/\$\{encodeURIComponent\(clientMessageId\)\}`/,
  'list-models': /listModels:[\s\S]{0,220}\/models`, \{ signal:/,
  'get-model-capabilities': /getModelCapabilities:[\s\S]{0,280}\/models\/\$\{encodeURIComponent\(modelId\)\}`/,
  'get-context-status': /getContextStatus:[\s\S]{0,180}\/context-status`/,
  'compact-context': /compactContext:[\s\S]{0,240}\/context\/compactions`, payload/,
  'upload-asset': /uploadAsset:[\s\S]{0,620}\/assets`, body/,
  'get-asset-content': /chatAssetContentUrl[\s\S]{0,260}\/assets\/\$\{encodeURIComponent\(assetId\)\}\/content\?variant=/,
  'delete-asset': /deleteAsset:[\s\S]{0,240}http\.delete\(`[\s\S]*\/assets\/\$\{encodeURIComponent\(assetId\)\}`\)/,
  'update-conversation': /updateConversation:[\s\S]{0,260}http\.patch\(`/,
  'stop-message': /stop:[\s\S]{0,240}\/stop`, target/,
  'clear-conversation': /clearConversation:[\s\S]{0,220}\/clear`, \{\}\)/,
  'delete-conversation': /deleteConversation:[\s\S]{0,220}http\.delete\(`/,
  'stream-message': /streamChatMessage[\s\S]{0,900}\/stream`[\s\S]{0,300}method: 'POST'/,
  'attach-stream': /attachChatStream[\s\S]{0,420}\/streams\/\$\{encodeURIComponent\(input\.turnId\)\}`[\s\S]{0,180}method: 'GET'/
}

const sourceGuards: Array<[string, RegExp]> = [
  ['create body 必须严格只允许 apiKeyId', /createConversationSchema = z\.object\(\{ apiKeyId:[\s\S]{0,120}optional\(\) \}\)\.strict\(\)/],
  ['update body 字段和图像模型枚举必须冻结', /updateConversationSchema = z\.object\(\{[\s\S]{0,400}title:[\s\S]{0,240}isPinned:[\s\S]{0,240}defaultImageModel: z\.enum\(\['gpt-image-2'\]\)[\s\S]{0,260}strict\(\)\.refine/],
  ['stream body 字段必须严格冻结', /messageBodySchema = z\.object\(\{[\s\S]{0,1200}clientMessageId:[\s\S]{0,180}replaceTurnId:[\s\S]{0,180}content:[\s\S]{0,180}contentBlocks:[\s\S]{0,180}model:[\s\S]{0,180}reasoningEffort:[\s\S]{0,180}serviceTier:[\s\S]{0,180}generationParameters: z\.object\(\{[\s\S]{0,700}temperature:[\s\S]{0,180}topP:[\s\S]{0,180}frequencyPenalty:[\s\S]{0,180}presencePenalty:[\s\S]{0,180}maxOutputTokens:[\s\S]{0,180}seed:[\s\S]{0,220}\}\)\.strict\(\)\.optional\(\)[\s\S]{0,120}\}\)\.strict\(\)/],
  ['消息块只允许 input_text/input_image assetId', /messageContentBlocksSchema = z\.array\(z\.discriminatedUnion\('type',[\s\S]{0,500}input_text[\s\S]{0,300}input_image[\s\S]{0,200}assetId:[\s\S]{0,700}最多粘贴 5 张图片[\s\S]{0,400}同一张图片不能重复引用/],
  ['消息 cursor 必须互斥且 int4 有界', /messagesQuerySchema[\s\S]{0,900}beforeSequenceNo:[\s\S]{0,160}2_147_483_647[\s\S]{0,260}afterSequenceNo:[\s\S]{0,160}2_147_483_647[\s\S]{0,260}fromSequenceNo:[\s\S]{0,160}2_147_483_647[\s\S]{0,320}max\(100\)\.default\(100\)[\s\S]{0,320}消息游标只能指定一个/],
  ['sync cursor 必须是安全非负整数', /syncQuerySchema[\s\S]{0,260}min\(0\)\.max\(Number\.MAX_SAFE_INTEGER\)/],
  ['asset content query 枚举必须冻结', /assetContentQuerySchema[\s\S]{0,260}preview', 'original'[\s\S]{0,200}'0', '1'/],
  ['stop body 必须严格且至少有一个目标', /stopBodySchema[\s\S]{0,500}turnId:[\s\S]{0,180}clientMessageId:[\s\S]{0,260}strict\(\)\.refine/],
  ['compact/clear body 必须严格', /compactBodySchema = z\.object\(\{ model:[\s\S]{0,180}\}\)\.strict\(\)[\s\S]{0,180}clearBodySchema = z\.object\(\{\}\)\.strict\(\)/],
  ['会话列表 cursor 必须透传三个 before 字段并限制 30/50', /beforeIsPinned: optionalBooleanQuery[\s\S]{0,160}beforeLastMessageAt: textQuery[\s\S]{0,160}beforeId: textQuery[\s\S]{0,160}limit: integerQuery\(req\.query\.limit, 30, 1, 50\)/],
  ['sync success 必须返回 serverTime/unchanged/head', /unchanged: query\.knownRevision === head\.messageRevision,[\s\S]{0,120}\.\.\.head/],
  ['会话响应必须附加 userTurnLimit', /userTurnLimit: runtimeConfig\.chat\.maxTurnsPerConversation/],
  ['资产读取必须支持 200/304 与 ETag', /Cache-Control', 'private, max-age=86400, immutable'[\s\S]{0,400}requestEtagMatches[\s\S]{0,200}status\(304\)[\s\S]{0,700}status\(200\)/],
  ['模型列表必须按 API Key 实际账户快照收敛可用模型', /loadOwnedChatModelListAsync[\s\S]{0,1200}loadChatModelListsFromAccountSnapshot[\s\S]{0,2200}loadChatModelCatalogSnapshot[\s\S]{0,1000}resolveChatModelOptionsFromAccountSnapshot/],
  ['模型列表响应不得返回完整能力', /chatRouter\.get\('\/conversations\/:conversationId\/models'[\s\S]{0,360}res\.json\(ok\(modelOptions\)\)/],
  ['模型详情必须按实际账户快照收敛能力并保留 404 code', /chatRouter\.get\('\/conversations\/:conversationId\/models\/:modelId'[\s\S]{0,1200}loadChatModelCatalogSnapshot[\s\S]{0,700}constrainChatModelOptionForAccounts[\s\S]{0,700}chat_model_not_found/],
  ['stream success 必须是 SSE', /prepareSseResponse\(res\)[\s\S]{0,120}writeChatSseEvent\(res, 'message\.started'/],
  ['stream 413 必须无机器码', /status\(413\)\.json\(\{ message: '消息内容超过 192 KiB 上限' \}\)/],
  ['stream 422 错误必须保持各自 code', /error instanceof ChatContextBudgetError[\s\S]{0,180}status\(422\)[\s\S]{0,180}code: error\.code[\s\S]{0,900}error instanceof ChatModelContextError[\s\S]{0,180}status\(422\)[\s\S]{0,180}code: error\.code/],
  ['stop success 必须是 202 data envelope', /status\(202\)\.json\(ok\(\{ stopped: true,[\s\S]{0,800}status\(202\)\.json\(ok\(\{ stopped: true, turnId:/],
  ['asset delete 必须保持 204 empty', /delete\('\/conversations\/:conversationId\/assets\/:assetId'[\s\S]{0,3000}status\(204\)\.end\(\)/],
  ['conversation delete 必须保持 204 empty', /delete\('\/conversations\/:conversationId'[\s\S]{0,800}status\(204\)\.end\(\)/],
  ['统一 zod error 必须是 400 chat_invalid_request', /error instanceof z\.ZodError[\s\S]{0,180}status\(400\)[\s\S]{0,180}code: 'chat_invalid_request'/],
  ['所有权 helper 必须抛专用 not-found error', /async function requireOwnedConversation[\s\S]{0,360}if \(!conversation\) throw new ChatConversationNotFoundError\(\)/]
]

main()
