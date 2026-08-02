import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { Request, Response } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import {
  gatewayClientAllowsUpstreamSemanticInterpretation,
  type OpenAIGatewayClientProfile
} from '../../modules/gateway/client-profiles/strategy.js'
import {
  accountErrorPolicyCouldMatchStatus,
  decideAccountErrorPolicy,
  type AccountErrorPolicyAccount,
  type GatewaySettings
} from '../../modules/gateway/policy/account-error-policy.service.js'
import {
  accountErrorPolicyAllowsUpstreamReplayAfterDispatch,
  isOpaqueUpstreamFailoverAllowed
} from '../../modules/gateway/response/failure-dispatch.js'
import {
  automaticUpstreamReplayAllowedAfterDispatch,
  isOpenAIGatewayImageGenerationModel,
  resolveOpenAIGatewayRequestLane
} from '../../modules/gateway/protocols/openai-v1/request-lane.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import {
  finalizeHandledUpstreamResponse,
  handleNonStreamUpstreamResponse,
  isUnexpectedEmptyUpstreamProtocolResponse,
  nonStreamJsonProtocolValidationAllowed
} from '../../modules/gateway/response/finalization.js'
import { GatewayDownstreamCommitState } from '../../modules/gateway/response/downstream-commit-state.js'
import {
  ResponsesRootStatusTracker,
  responsesFailureStatusFromCapturedJson
} from '../../modules/gateway/response/responses-failure-status.js'
import type { GatewayUpstreamResponse } from '../../modules/gateway/upstream/request.js'

const oversizedFailurePrefix = `{"id":"resp_test","status":"failed","error":{"message":"failed"},"output":"${'x'.repeat(1024 * 1024)}`
assert.equal(responsesFailureStatusFromCapturedJson(oversizedFailurePrefix), true, '超大 Responses 根级 failed 终态仍必须判定为失败')
const oversizedNestedFailurePrefix = `{"id":"resp_test","status":"completed","output":[{"status":"failed","value":"${'x'.repeat(1024 * 1024)}`
assert.equal(responsesFailureStatusFromCapturedJson(oversizedNestedFailurePrefix), false, '嵌套失败状态不得误判为根级失败')
const deferredStatusTracker = new ResponsesRootStatusTracker()
deferredStatusTracker.push(Buffer.from(`{"id":"resp_test","output":"${'x'.repeat(2 * 1024 * 1024)}","sta`, 'utf8'))
deferredStatusTracker.push(Buffer.from('tus":"failed"}', 'utf8'))
assert.equal(deferredStatusTracker.hasFailedStatus(), true, '跨 chunk 的根级 failed 状态必须在大字段之后仍可识别')

const genericProfiles: OpenAIGatewayClientProfile[] = ['generic_openai', 'generic_anthropic', 'generic_gemini']
const explicitProfiles: OpenAIGatewayClientProfile[] = ['codex', 'claude_code', 'gemini_cli']
for (const clientProfile of genericProfiles) {
  assert.equal(gatewayClientAllowsUpstreamSemanticInterpretation({ clientProfile }), false, `${clientProfile} 不得解释上游状态或错误类型`)
}
for (const clientProfile of explicitProfiles) {
  assert.equal(gatewayClientAllowsUpstreamSemanticInterpretation({ clientProfile }), true, `${clientProfile} 应保留专用协议语义`)
}

const replayableInferenceRequest = {
  method: 'POST',
  originalUrl: '/v1/chat/completions',
  path: '/v1/chat/completions',
  body: { model: 'gpt-5.5', messages: [{ role: 'user', content: 'hello' }] }
} as Request
const geminiImageRequest = {
  method: 'POST',
  originalUrl: '/v1beta/models/gemini-2.5-flash:generateContent',
  path: '/v1beta/models/gemini-2.5-flash:generateContent',
  body: { contents: [], generationConfig: { responseModalities: ['IMAGE'] } }
} as Request
assert.equal(resolveOpenAIGatewayRequestLane(geminiImageRequest), 'image', 'Gemini 原生图片输出必须使用图片长时限 lane')
const sideEffectRequest = {
  method: 'POST',
  originalUrl: '/v1/audio/speech',
  path: '/v1/audio/speech'
} as Request
const emptyChatResponseRequest = {
  method: 'POST',
  originalUrl: '/v1/chat/completions',
  path: '/v1/chat/completions',
  body: { model: 'gpt-5.5', messages: [{ role: 'user', content: 'hello' }] }
} as Request
const geminiInteractionDeleteRequest = {
  method: 'DELETE',
  originalUrl: '/v1beta/interactions/interaction-1',
  path: '/v1beta/interactions/interaction-1'
} as Request
const emptyResponseAccount = {
  id: 'acct_empty_response',
  providerCode: GPT_VENDOR_CODE,
  providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
  protocolCode: 'openai',
  protocolVersion: 'v1'
} as UpstreamAccount
assert.equal(
  isUnexpectedEmptyUpstreamProtocolResponse({ req: emptyChatResponseRequest, account: emptyResponseAccount, statusCode: 204 }),
  true,
  'JSON 推理请求收到 204 空 body 必须判为缺少协议终态'
)
assert.equal(
  isUnexpectedEmptyUpstreamProtocolResponse({ req: emptyChatResponseRequest, account: emptyResponseAccount, statusCode: 205 }),
  true,
  'JSON 推理请求收到 205 空 body 必须判为缺少协议终态'
)
assert.equal(
  isUnexpectedEmptyUpstreamProtocolResponse({ req: geminiInteractionDeleteRequest, account: emptyResponseAccount, statusCode: 204 }),
  false,
  'Gemini interaction DELETE 的合法 204 空 body 不得被误判为协议失败'
)
const embeddingsProtocolRequest = {
  method: 'POST',
  originalUrl: '/v1/embeddings',
  path: '/v1/embeddings',
  body: { model: 'text-embedding-3-small', input: 'protocol validation regression' }
} as Request
assert.equal(
  nonStreamJsonProtocolValidationAllowed({
    req: embeddingsProtocolRequest,
    account: emptyResponseAccount,
    upstreamResponse: { ok: true, headers: new Headers({ 'content-type': 'text/plain' }) }
  }),
  true,
  '已知 Embeddings JSON 端点即使上游伪装 content-type 也必须先验证正文'
)
assert.equal(
  nonStreamJsonProtocolValidationAllowed({
    req: { method: 'GET', originalUrl: '/v1/files/file-1/content', path: '/v1/files/file-1/content' } as Request,
    account: emptyResponseAccount,
    upstreamResponse: { ok: true, headers: new Headers({ 'content-type': 'application/octet-stream' }) }
  }),
  false,
  '已知二进制下载不得被错误纳入 JSON 协议验证'
)

const universalFailoverCases: Array<{
  label: string
  request: Request
  lane: 'text' | 'image'
}> = [
  { label: '普通文本推理', request: replayableInferenceRequest, lane: 'text' },
  { label: '图片 lane', request: replayableInferenceRequest, lane: 'image' },
  { label: '语音生成', request: sideEffectRequest, lane: 'text' },
  { label: '图片生成', request: { method: 'POST', originalUrl: '/v1/images/generations', path: '/v1/images/generations' } as Request, lane: 'image' },
  { label: '图片编辑', request: { method: 'POST', originalUrl: '/v1/images/edits', path: '/v1/images/edits' } as Request, lane: 'image' },
  { label: 'Responses 图片生成', request: { method: 'POST', originalUrl: '/v1/responses', path: '/v1/responses', body: { model: 'gpt-image-2', input: 'create an image' } } as Request, lane: 'image' },
  { label: 'Chat 托管工具', request: { method: 'POST', originalUrl: '/v1/chat/completions', path: '/v1/chat/completions', body: { tools: [{ type: 'web_search' }], store: true } } as Request, lane: 'text' },
  { label: 'Chat 不可检查正文', request: { method: 'POST', originalUrl: '/v1/chat/completions', path: '/v1/chat/completions' } as Request, lane: 'text' },
  { label: 'Responses 后台任务', request: { method: 'POST', originalUrl: '/v1/responses', path: '/v1/responses', body: { background: true, store: true } } as Request, lane: 'text' },
  { label: 'Responses 解析正文缓存', request: { method: 'POST', originalUrl: '/v1/responses', path: '/v1/responses', gatewayParsedJsonBodyAvailable: true, gatewayParsedJsonBody: { background: true } } as unknown as Request, lane: 'text' },
  { label: 'Gemini 托管工具', request: { method: 'POST', originalUrl: '/v1beta/models/gemini-2.5-flash:generateContent', path: '/v1beta/models/gemini-2.5-flash:generateContent', body: { tools: [{ googleSearch: {} }] } } as Request, lane: 'text' },
  { label: 'Gemini 图片输出', request: geminiImageRequest, lane: 'image' },
  { label: 'Gemini Interactions 状态推进', request: { method: 'POST', originalUrl: '/v1beta/interactions/interaction-1', path: '/v1beta/interactions/interaction-1', body: { input: 'continue' } } as Request, lane: 'text' },
  { label: 'Anthropic 服务端工具', request: { method: 'POST', originalUrl: '/v1/messages', path: '/v1/messages', body: { tools: [{ type: 'web_search_20250305', name: 'web_search' }] } } as Request, lane: 'text' },
  { label: 'Anthropic MCP', request: { method: 'POST', originalUrl: '/v1/messages', path: '/v1/messages', body: { mcp_servers: [{ type: 'url', url: 'https://mcp.invalid' }] } } as Request, lane: 'text' },
  { label: '未知资源端点', request: { method: 'POST', originalUrl: '/v1/vector_stores', path: '/v1/vector_stores' } as Request, lane: 'text' }
]

for (const { label, request, lane } of universalFailoverCases) {
  assert.equal(isOpaqueUpstreamFailoverAllowed(request), false, `${label} 的未知失败不得在同账户内自动轮换兄弟 Key`)
  assert.equal(automaticUpstreamReplayAllowedAfterDispatch(request, lane), false, `${label} 不得获得无条件重放许可`)
}

for (const action of ['cooldown', 'disable', 'retry_next'] as const) {
  assert.equal(
    accountErrorPolicyAllowsUpstreamReplayAfterDispatch(sideEffectRequest, 'text', { action }),
    action === 'retry_next',
    `${action} 只有 retry_next 可以授权当前请求继续轮换同账户兄弟 Key`
  )
}
assert.equal(
  accountErrorPolicyAllowsUpstreamReplayAfterDispatch(replayableInferenceRequest, 'text', { action: 'cooldown' }),
  false,
  '普通推理执行 cooldown 不授权同账户兄弟 Key，但当前账户仍必须进入统一账户级切号'
)
assert.equal(isOpenAIGatewayImageGenerationModel('gpt-image-1'), true)
assert.equal(isOpenAIGatewayImageGenerationModel('imagen-3.0-generate-002'), true)
assert.equal(isOpenAIGatewayImageGenerationModel('gemini-2.5-flash-image-preview'), true)
assert.equal(isOpenAIGatewayImageGenerationModel('gpt-5.5'), false)
assert.equal(isOpenAIGatewayImageGenerationModel('flux-image-regression'), false, '非标准图片模型必须由目录能力识别，不能继续扩张名称猜测')

const bodyConstrainedPolicyAccount: AccountErrorPolicyAccount = {
  id: 'body-constrained-policy-account',
  providerCode: 'gpt',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  credentials: {
    error_handling_rules: [{
      enabled: true,
      name: '仅匹配指定正文',
      priority: 1,
      status_codes: [418],
      keywords: ['configured-body-marker'],
      action: 'retry_next'
    }]
  }
}
assert.equal(accountErrorPolicyCouldMatchStatus(bodyConstrainedPolicyAccount, 418), true, '状态预筛选应要求读取正文')
assert.equal(decideAccountErrorPolicy(
  bodyConstrainedPolicyAccount,
  418,
  new Headers({ 'content-type': 'application/json' }),
  Buffer.from('{"error":{"message":"untrusted unrelated body"}}'),
  {} as GatewaySettings
), undefined, '仅状态码命中但正文不命中时不得伪造显式用户策略')

const dispatchSource = readFileSync(new URL('../../modules/gateway/dispatch/upstream-dispatch.ts', import.meta.url), 'utf8')
const failureDispatchSource = readFileSync(new URL('../../modules/gateway/response/failure-dispatch.ts', import.meta.url), 'utf8')
const requestLaneSource = readFileSync(new URL('../../modules/gateway/protocols/openai-v1/request-lane.ts', import.meta.url), 'utf8')
const failureClassifierSource = readFileSync(new URL('../../modules/gateway/response/upstream-failure-classifier.ts', import.meta.url), 'utf8')
const routesSource = readFileSync(new URL('../../modules/gateway/routes.ts', import.meta.url), 'utf8')
const responseInspectionSource = readFileSync(new URL('../../modules/gateway/response/inspection.ts', import.meta.url), 'utf8')
const requestPreflightSource = readFileSync(new URL('../../modules/gateway/request/preflight.ts', import.meta.url), 'utf8')
const streamSource = readFileSync(new URL('../../modules/gateway/response/stream.ts', import.meta.url), 'utf8')
const compactPreflightSource = readFileSync(new URL('../../modules/gateway/codex-responses/compact-preflight.ts', import.meta.url), 'utf8')
const candidateSelectionSource = readFileSync(new URL('../../modules/gateway/routing/hot-quality-candidate-selection.ts', import.meta.url), 'utf8')
const finalizationSource = readFileSync(new URL('../../modules/gateway/response/finalization.ts', import.meta.url), 'utf8')
const nonStreamInspectionSource = readFileSync(new URL('../../modules/gateway/response/non-stream-json-inspection.ts', import.meta.url), 'utf8')
const auxiliarySource = readFileSync(new URL('../../modules/gateway/hybrid/auxiliary-dispatch.service.ts', import.meta.url), 'utf8')
const hybridScoringSource = readFileSync(new URL('../../modules/gateway/hybrid/scoring.service.ts', import.meta.url), 'utf8')
const hybridQualitySource = readFileSync(new URL('../../modules/gateway/hybrid/quality-inspection.service.ts', import.meta.url), 'utf8')
assert.match(
  failureDispatchSource,
  /isOpaqueUpstreamFailoverAllowed/,
  '所有端点的未知完整 HTTP 失败必须经过统一终止边界'
)
assert.match(
  requestLaneSource,
  /automaticUpstreamReplayAllowedAfterDispatch\([\s\S]*?\): boolean \{\s*return false\s*\}/,
  '所有请求类型和 lane 都不得获得无条件候选切换许可'
)
assert.doesNotMatch(
  requestLaneSource,
  /lane === 'image'|RequiresAtMostOnce|toolDefinitionsRequireAtMostOnce|outputModalitiesRequireAtMostOnce/,
  '统一切号规则不得重新引入图片、工具或副作用请求分类门禁'
)
assert.match(
  dispatchSource,
  /const failedResponseResult = await handleFailedUpstreamResponse/,
  'HTTP 失败必须经过统一失败处理边界'
)
assert.match(
  dispatchSource,
  /failureKind === 'explicit_policy'/,
  '只有显式策略分支才能产生显式策略质量终态'
)
assert.match(
  dispatchSource,
  /outcomeClass: explicitPolicyFailure \? 'explicit_policy_failure' : 'upstream_response_failure'/,
  '未知完整 HTTP 失败必须使用独立质量终态'
)
assert.match(
  dispatchSource,
  /failureScope: explicitPolicyFailure \? 'account' : 'none'/,
  'opaque HTTP 切号只允许写诊断中性终态'
)
assert.doesNotMatch(
  `${dispatchSource}\n${routesSource}\n${failureDispatchSource}`,
  /UpstreamReplayBlockedError|upstream_outcome_unknown|upstream_automatic_replay_blocked|replay_blocked/,
  '统一候选切换不得保留第二套自动重放阻断终态或旧结果未知错误'
)
assert.match(
  requestPreflightSource,
  /hybridRoute\.outcome === 'selected'[\s\S]*requestLane = resolveOpenAIGatewayRequestLane\(req\)/,
  '混合路由初次改写模型后必须重新判定图片 lane'
)
assert.match(
  requestPreflightSource,
  /requestLane !== 'image'[\s\S]*await accountModelsTargetImage\(req, candidateFilter\.accounts, systemAccountId\)[\s\S]*requestLane = 'image'[\s\S]*applyOpenAIGatewayImagePermissionPreflight/,
  '候选账户把客户端模型映射为图片模型时，必须在权限检查和上游派发前提升为图片 lane'
)
assert.match(
  requestPreflightSource,
  /listCachedProviderModelCatalogAsync\([\s\S]*supportedApiProtocols\.includes\('images'\)[\s\S]*outputModalities\?\.includes\('image'\)/,
  '非标准映射模型必须复用模型目录的显式图片能力，不能只依赖模型名称前缀'
)
assert.match(
  requestPreflightSource,
  /resolveOpenAIAccountModelMapping\(account, requestedModel, sourceEndpointFamily\)\?\.upstreamModel\s*\?\? requestedModel/,
  '没有显式映射的直接自定义模型也必须查询目录能力，不能漏过图片 lane'
)
assert.match(
  routesSource,
  /resolveNextHybridGatewayRoute\([\s\S]*requestLane: resolveOpenAIGatewayRequestLane\(req\)/,
  '混合质量升级改写模型后必须把新 lane 传入重入预检'
)
assert.match(failureDispatchSource, /_decision\?\.action === 'retry_next'/, '只有显式 retry_next 可以授权同账户兄弟 Key 轮换')
assert.match(
  failureDispatchSource,
  /failureKind: explicitPolicyDecision \? 'explicit_policy' : 'opaque_http',[\s\S]*keyScopedFailure: explicitPolicyDecision\?\.action === 'retry_next'[\s\S]*hasAlternativeAccountApiKeys\(account\)/,
  '只有 retry_next 才允许在同一账户内轮换尚未尝试的兄弟 Key；状态动作必须直接进入账户级切号'
)
assert.match(
  responseInspectionSource,
  /policy\.source !== 'system_default' && policy\.action === 'retry_next_account'[\s\S]*replayAuthority: 'explicit_user_policy'/,
  '显式响应策略的归因标签必须可审计，但不得形成另一套切号规则'
)
const codexSyntheticDecisionSource = streamSource.slice(
  streamSource.indexOf('function codexResponsesProtocolDecision'),
  streamSource.indexOf('function withTransportFailure')
)
assert.doesNotMatch(codexSyntheticDecisionSource, /replayAuthority/, 'Codex 内部协议 guard 不得伪装成用户重放授权')
assert.match(
  routesSource,
  /const protocolValidatedSuccess = !responseRetryUpstream[\s\S]*handledResponse\.protocolValidatedSuccess === true/,
  '只有协议验证成功的完整响应才能记为 completed_response，未知 2xx 必须保持中性诊断'
)
assert.match(routesSource, /const diagnosticUpstreamResponse = !transportFailure[\s\S]*!protocolValidatedSuccess/, '未验证响应必须进入中性诊断终态')
assert.match(routesSource, /diagnosticUpstreamResponse[\s\S]*\? 'upstream_response_failure'[\s\S]*: 'completed_response'/, '中性诊断终态不得增加 completedResponses')
assert.match(
  routesSource,
  /exposeKnownUpstreamHttpFailure = gatewayUsageContext\.trafficSource !== 'gateway'[\s\S]*knownUpstreamHttpFailure && exposeKnownUpstreamHttpFailure/,
  '客户网关候选耗尽不得把最后一次真实 HTTP 错误透传给客户端，诊断流仍可保留'
)
for (const [label, source] of [
  ['混合辅助', auxiliarySource],
  ['Codex 压缩预检', compactPreflightSource]
] as const) {
  assert.match(
    source,
    /opaqueUpstreamResponse[\s\S]*\? 'upstream_response_failure'[\s\S]*: 'completed_response'/,
    `${label}消费 return_response 非 2xx 时也不得记为 completed_response`
  )
}
assert.match(
  failureDispatchSource,
  /const gatewayFailoverEnabled = usageContext\.trafficSource === 'gateway'[\s\S]*if \(!gatewayFailoverEnabled\)[\s\S]*return \{ action: 'return_response', response \}/,
  '只有非客户网关流和显式关闭状态写入的内部流可以保留真实上游响应'
)
assert.match(
  failureDispatchSource,
  /if \(explicitPolicyDecision && input\.accountStateMutationEnabled !== false\)/,
  '网关客户流命中用户策略时必须保留显式状态动作'
)
assert.match(failureDispatchSource, /applyAccountErrorHandlingWithCacheInvalidation[\s\S]*policyDecision: explicitPolicyDecision/)
assert.match(failureDispatchSource, /action: 'skip_account'/, 'cooldown/disable 必须执行显式状态动作后继续统一账户级切号')
assert.match(
  failureDispatchSource,
  /keyScopedFailure: explicitPolicyDecision\?\.action === 'retry_next'[\s\S]*hasAlternativeAccountApiKeys\(account\)/,
  '所有显式动作必须共用账户级切号，只有 retry_next 才能继续同账户兄弟 Key'
)
assert.match(
  finalizationSource,
  /pipeNonStreamUpstreamResponseForInspection[\s\S]*maxLifetimeMs: input\.timeoutProfile\.timeoutsDisabled === true\s*\? undefined\s*: input\.timeoutProfile\.uncommittedAttemptMaxLifetimeMs[\s\S]*pipeNonStreamUpstreamResponse[\s\S]*maxLifetimeMs: input\.timeoutProfile\.timeoutsDisabled === true\s*\? undefined\s*: input\.timeoutProfile\.uncommittedAttemptMaxLifetimeMs/,
  '非流式普通请求必须保留 lane 独立绝对上限，显式无限时压缩必须同时豁免检查缓冲与普通转发'
)
assert.match(
  finalizationSource,
  /firstByteDeadlineMs: responseTimeoutsDisabled \? undefined : input\.firstByteDeadlineMs[\s\S]*responsePrecommitDeadlineAtMs: responseTimeoutsDisabled \? undefined : input\.responsePrecommitDeadlineAtMs/,
  '流式返回边界必须忽略无限时压缩请求遗留的有限 deadline'
)
assert.equal(
  (finalizationSource.match(/firstByteDeadlineMs: input\.timeoutProfile\.timeoutsDisabled === true\s*\? undefined\s*: input\.firstByteDeadlineMs/g) ?? []).length,
  2,
  '非流式检查与普通转发边界都必须忽略无限时压缩请求遗留的首字 deadline'
)
assert.doesNotMatch(
  failureDispatchSource,
  /recordGatewayAccountApiKeyLocalFailure/,
  '未知完整 HTTP 失败不得写跨请求共享 Key 避让'
)
assert.doesNotMatch(
  failureClassifierSource,
  /\b(?:401|403|429|500|502|503|504)\b|invalid_api_key|rate_limit|insufficient_quota/,
  '内部失败分类器不得硬编码供应商状态码或错误类型语义'
)
assert.doesNotMatch(
  candidateSelectionSource,
  /upstreamResponseFailures/,
  'opaque HTTP 诊断计数不得被候选排序直接消费'
)
assert.doesNotMatch(
  failureDispatchSource,
  /builtInImagePermission|isBuiltInImageAccountPermissionFailure|Image generation is not enabled/,
  '通用失败派发不得写死图片状态码、错误类型或供应商文案'
)
assert.doesNotMatch(
  finalizationSource,
  /!interpretUpstreamResponseSemantics \|\| upstreamResponse\.ok/,
  '通用响应不得绕过 HTTP 成功条件写入成功审计或用量'
)
assert.match(
  finalizationSource,
  /upstreamProtocolFailure = result\.errorPayload\.code === 'upstream_protocol_failure'[\s\S]*responsesFailedTerminal = upstreamResponse\.ok[\s\S]*upstreamProtocolFailure[\s\S]*hasResponsesFailedTerminal\(result\.responseBodyText\)[\s\S]*forwardedResponseSuccessful = upstreamResponse\.ok && !responsesFailedTerminal && !upstreamProtocolFailure/,
  'Responses 非流失败终态不得因 HTTP 200 被记为成功'
)
assert.match(
  finalizationSource,
  /isUnexpectedEmptyUpstreamProtocolResponse[\s\S]*statusCode !== 204 && input\.statusCode !== 205[\s\S]*isSuccessfulEmptyUpstreamResponseAllowed\(input\)[\s\S]*emptyUpstreamProtocolFailure[\s\S]*sendGatewayErrorResponse\(res, 502/,
  '协议请求收到 204/205 空 body 时必须保留 DELETE 例外，并返回兼容 502 协议错误而非语义成功'
)
assert.match(
  finalizationSource,
  /const responsesFailureStatusTracker = responseEndpointFamily === 'responses'[\s\S]*onChunkRead: \(chunk\) => responsesFailureStatusTracker\?\.push\(chunk\)[\s\S]*markTrackedResponsesFailure\(\)/,
  '超大 Responses 失败正文必须逐 chunk 检查根级失败终态，不能只依赖截断正文前缀'
)
assert.match(
  nonStreamInspectionSource,
  /endpointFamily === 'responses'[\s\S]*root\.status === 'failed'[\s\S]*upstream_protocol_failure/,
  'Responses JSON 失败终态必须形成 upstream_protocol_failure'
)
assert.match(
  nonStreamInspectionSource,
  /parsedJsonBody\.status !== 'valid'[\s\S]*上游成功响应不是有效 JSON[\s\S]*upstream_protocol_error/,
  '完整 2xx 的畸形 JSON 必须返回协议错误，不能继续向下游透传'
)
assert.match(
  finalizationSource,
  /const protocolValidationEnabled = nonStreamJsonProtocolValidationAllowed\(input\)[\s\S]*const inspectJsonResponse = !upstreamResponse\.ok[\s\S]*\|\| protocolValidationEnabled[\s\S]*requireFullyBuffered: protocolValidationEnabled[\s\S]*pipeResult\.fullyBuffered \|\| pipeResult\.inspectionLimitExceeded[\s\S]*protocolValidationLimitExceeded: pipeResult\.inspectionLimitExceeded[\s\S]*res\.send\(downstreamBody\)/,
  '协议端点即使上游伪装 content-type 或超过验证窗口，也必须在写入下游前完成或终止 2xx JSON 检查'
)
assert.match(
  finalizationSource,
  /captureBody: !upstreamResponse\.ok[\s\S]*responseBodyText = pipeResult\.captureTruncated[\s\S]*pipeResult\.diagnosticBodyText/,
  '所有非 2xx 响应必须保留有界错误正文，供审计、用量和管理页展示实际错误'
)
assert.match(
  finalizationSource,
  /protocolValidatedNonStreamResponse[\s\S]*case 'chat_completions':[\s\S]*Array\.isArray\(root\.choices\)[\s\S]*root\.choices\.some/,
  '非流式 Chat 成功必须先通过 choices 协议结构验证，未知 2xx 正文不得伪装成功'
)
assert.match(
  finalizationSource,
  /protocolValidatedSuccess: upstreamResponse\.ok && streamResult\.protocolValidated/,
  '流式成功必须来自协议检查器确认的终止帧，解析器跳过或未知 SSE 不得恢复 Key'
)
assert.match(
  streamSource,
  /protocolTerminalReceived = protocolTerminalReceived \|\| inspection\.terminalReceived[\s\S]*completed && protocolTerminalReceived && !streamParserSkipped/,
  '流式验证来源必须保留终止帧证据，并在解析器跳过时降级为中性 framing'
)
assert.match(auxiliarySource, /confirmHalfOpenSuccess[\s\S]*releaseHalfOpenLease/, '混合辅助必须沿用网关派发器的半开租约确认与释放')
assert.match(auxiliarySource, /await input\.confirmHalfOpenSuccess\(\)/, '混合辅助成功完成后必须确认半开租约')
assert.match(auxiliarySource, /await input\.releaseHalfOpenLease\(\)/, '混合辅助失败完成后必须释放半开租约')
assert.match(
  auxiliarySource,
  /let leaseSettled = false[\s\S]*finally \{[\s\S]*if \(!leaseSettled\)[\s\S]*releaseHalfOpenLease[\s\S]*auditCapture\.finalize/,
  '混合辅助 finish 内部异常也必须释放未结租约并最终化审计'
)
assert.doesNotMatch(auxiliarySource, /throw finishError/, '混合辅助收尾副作用异常不得推翻已完成的有效模型结果')
for (const [label, source] of [['scoring', hybridScoringSource], ['quality', hybridQualitySource]] as const) {
  assert.match(
    source,
    /let dispatchFinished = false[\s\S]*finally \{[\s\S]*if \(!dispatchFinished\)[\s\S]*dispatch\.finish\(/,
    `混合 ${label} 调用方异常时必须在 finally 结束辅助 dispatch`
  )
  assert.match(
    source,
    /await finishDispatch\(\{ success: true \}\)[\s\S]*recordHybridScoringAttempt/,
    `混合 ${label} 必须先完成 dispatch，再写成功 usage/cache`
  )
}

async function assertOversizedResponsesFailureMarksAuditAndUsageFailed(): Promise<void> {
  const tempRoot = resolve(tmpdir(), `juhe-ai-responses-failed-audit-${Date.now()}-${Math.random().toString(16).slice(2)}`)
  mkdirSync(tempRoot, { recursive: true })
  runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
  runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
  runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
  runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
  runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
  // This isolated process creates both business and dataset fixtures. There
  // is no second writer process to protect inside this regression.
  process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = '0'
  runtimeConfig.runtimeMode = 'standalone'
  runtimeConfig.cacheDriver = 'memory'
  runtimeConfig.runtimeStateDriver = 'memory'
  runtimeConfig.auditLog.enabled = true
  // This regression creates and seeds its isolated business database locally.
  // It therefore owns the SQLite writer boundary for the test process.
  runtimeConfig.processRole = 'db-service'
  runtimeConfig.workerRole = 'ingest-worker'
  runtimeConfig.log.consoleEnabled = false
  runtimeConfig.log.fileEnabled = false
  logger.level = 'silent'

  const [database, repositories, auditQueue, usageRecordQueue, auditCaptureModule] = await Promise.all([
    import('../../storage/database.js'),
    import('../../storage/repositories.js'),
    import('../../modules/audit-logs/audit-log-queue.service.js'),
    import('../../modules/gateway/usage/record-queue.service.js'),
    import('../../modules/gateway/audit/capture.service.js')
  ])
  auditQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  const response = new OversizedResponsesMockResponse()
  const request = oversizedResponsesRequest()
  const account = oversizedResponsesAccount()
  const traceId = 'trace-oversized-responses-failed-terminal'
  const auditCapture = auditCaptureModule.createAuditCapture({
    req: request,
    res: response as unknown as Response,
    traceId,
    startedAtMs: Date.now() - 50,
    captureMode: 'metadata_only'
  })
  const auditAttemptId = auditCapture.startAttempt({
    account,
    attemptIndex: 0,
    upstreamUrl: 'https://upstream.invalid/v1/responses',
    method: 'POST',
    headers: new Headers({ 'content-type': 'application/json' }),
    body: JSON.stringify(request.body)
  })
  try {
    const result = await handleNonStreamUpstreamResponse({
      req: request,
      res: response as unknown as Response,
      account,
      upstreamResponse: oversizedFailedResponsesUpstreamResponse(),
      upstreamUrl: 'https://upstream.invalid/v1/responses',
      auditAttemptId,
      auditCapture,
      settings: {} as GatewaySettings,
      usageContext: {
        traceId,
        trafficSource: 'gateway',
        systemAccountId: 'sys_semantic_regression',
        apiKeyId: 'key_semantic_regression',
        groupId: 'group_semantic_regression',
        endpoint: 'POST /v1/responses',
        requestSnapshot: {
          method: 'POST',
          path: '/v1/responses',
          originalUrl: '/v1/responses',
          traceId,
          headers: {},
          body: request.body
        }
      },
      startedAt: Date.now() - 50,
      timeoutProfile: { timeoutsDisabled: true } as never,
      signal: new AbortController().signal,
      downstreamCommitState: new GatewayDownstreamCommitState(),
      accountStateMutationEnabled: false,
      automaticAccountStateMutationEnabled: false
    })

    assert.equal(result.alreadyFinalized, true, '超大协议正文不得绕过验证窗口向下游透传')
    assert.equal(response.statusCode, 502, '超大协议正文必须返回明确的 502 协议错误')

    await auditQueue.flushAllAuditLogQueueAsync()
    const auditLog = repositories.listAuditLogs({ traceId }).items[0]
    assert(auditLog, '超大失败终态必须保留失败审计')
    assert.equal(auditLog.success, false, '超大失败终态审计总结果不得记为成功')
    const auditDetail = repositories.getAuditLogDetail(auditLog.id)
    assert.equal(auditDetail?.attempts.length, 1, '超大失败终态必须保留唯一上游 attempt')
    assert.equal(auditDetail?.attempts[0]?.success, false, '超大失败终态的 audit attempt 不得记为成功')
    assert.equal(auditDetail?.attempts[0]?.errorCode, 'upstream_protocol_error', 'audit attempt 必须记录协议验证窗口错误码')
    const usageRecord = usageRecordQueue.peekPendingUsageRecordForTest()
    assert.equal(usageRecord?.success, false, '超大失败终态使用记录不得记为成功')
    assert.equal(usageRecord?.errorCode, 'upstream_protocol_error', '使用记录必须记录协议验证窗口错误码')

    auditQueue.clearAuditLogQueueForTest()
    auditQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
    usageRecordQueue.clearUsageRecordQueueForTest()
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
    const emptyResponse = new OversizedResponsesMockResponse()
    const emptyRequest = emptyChatJsonRequest()
    const emptyTraceId = 'trace-empty-204-protocol-failure'
    const emptyAuditCapture = auditCaptureModule.createAuditCapture({
      req: emptyRequest,
      res: emptyResponse as unknown as Response,
      traceId: emptyTraceId,
      startedAtMs: Date.now() - 50,
      captureMode: 'metadata_only'
    })
    const emptyAuditAttemptId = emptyAuditCapture.startAttempt({
      account,
      attemptIndex: 0,
      upstreamUrl: 'https://upstream.invalid/v1/chat/completions',
      method: 'POST',
      headers: new Headers({ 'content-type': 'application/json' }),
      body: JSON.stringify(emptyRequest.body)
    })
    const emptyInput = {
      req: emptyRequest,
      res: emptyResponse as unknown as Response,
      account,
      upstreamResponse: empty204UpstreamResponse(),
      upstreamUrl: 'https://upstream.invalid/v1/chat/completions',
      auditAttemptId: emptyAuditAttemptId,
      auditCapture: emptyAuditCapture,
      settings: {} as GatewaySettings,
      usageContext: {
        traceId: emptyTraceId,
        trafficSource: 'gateway' as const,
        systemAccountId: 'sys_semantic_regression',
        apiKeyId: 'key_semantic_regression',
        groupId: 'group_semantic_regression',
        endpoint: 'POST /v1/chat/completions',
        requestSnapshot: {
          method: 'POST',
          path: '/v1/chat/completions',
          originalUrl: '/v1/chat/completions',
          traceId: emptyTraceId,
          headers: {},
          body: emptyRequest.body
        }
      },
      startedAt: Date.now() - 50,
      timeoutProfile: { timeoutsDisabled: true } as never,
      signal: new AbortController().signal,
      downstreamCommitState: new GatewayDownstreamCommitState(),
      accountStateMutationEnabled: false,
      automaticAccountStateMutationEnabled: false
    }
    const emptyResult = await handleNonStreamUpstreamResponse(emptyInput)
    if (emptyResult.alreadyFinalized || emptyResult.retryUpstream === true) {
      throw new Error('204 空响应必须进入统一最终化，不能重试或提前结束')
    }
    const completedEmptyResult = emptyResult
    assert.equal(completedEmptyResult.errorPayload.code, 'upstream_protocol_failure', '204 空响应必须进入协议失败终态')
    await finalizeHandledUpstreamResponse({
      ...emptyInput,
      upstreamResponse: empty204UpstreamResponse(),
      result: completedEmptyResult,
      routingEffectsApplied: true
    })
    assert.equal(emptyResponse.statusCode, 502, '204 空响应不得向客户端提交空成功')
    await auditQueue.flushAllAuditLogQueueAsync()
    const emptyAuditLog = repositories.listAuditLogs({ traceId: emptyTraceId }).items[0]
    assert(emptyAuditLog, '204 空响应必须保留失败审计')
    assert.equal(emptyAuditLog.success, false, '204 空响应审计总结果不得记为成功')
    const emptyAuditDetail = repositories.getAuditLogDetail(emptyAuditLog.id)
    assert.equal(emptyAuditDetail?.attempts[0]?.success, false, '204 空响应的 audit attempt 不得记为成功')
    assert.equal(emptyAuditDetail?.attempts[0]?.errorCode, 'upstream_protocol_failure', '204 空响应 attempt 必须记录协议失败码')
    const emptyUsageRecord = usageRecordQueue.peekPendingUsageRecordForTest()
    assert.equal(emptyUsageRecord?.success, false, '204 空响应使用记录不得记为成功')
    assert.equal(emptyUsageRecord?.errorCode, 'upstream_protocol_failure', '204 空响应使用记录必须记录协议失败码')

    auditQueue.clearAuditLogQueueForTest()
    auditQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
    usageRecordQueue.clearUsageRecordQueueForTest()
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
    const quotaResponse = new OversizedResponsesMockResponse()
    const quotaRequest = emptyChatJsonRequest()
    const quotaTraceId = 'trace-402-insufficient-user-quota'
    const quotaAuditCapture = auditCaptureModule.createAuditCapture({
      req: quotaRequest,
      res: quotaResponse as unknown as Response,
      traceId: quotaTraceId,
      startedAtMs: Date.now() - 50,
      captureMode: 'metadata_only'
    })
    const quotaAuditAttemptId = quotaAuditCapture.startAttempt({
      account,
      attemptIndex: 0,
      upstreamUrl: 'https://upstream.invalid/v1/chat/completions',
      method: 'POST',
      headers: new Headers({ 'content-type': 'application/json' }),
      body: JSON.stringify(quotaRequest.body)
    })
    const quotaInput = {
      ...emptyInput,
      req: quotaRequest,
      res: quotaResponse as unknown as Response,
      upstreamResponse: insufficientUserQuotaUpstreamResponse(),
      auditAttemptId: quotaAuditAttemptId,
      auditCapture: quotaAuditCapture,
      usageContext: {
        ...emptyInput.usageContext,
        traceId: quotaTraceId,
        requestSnapshot: {
          ...emptyInput.usageContext.requestSnapshot,
          traceId: quotaTraceId,
          body: quotaRequest.body
        }
      },
      downstreamCommitState: new GatewayDownstreamCommitState()
    }
    const quotaResult = await handleNonStreamUpstreamResponse(quotaInput)
    if (quotaResult.alreadyFinalized || quotaResult.retryUpstream === true) {
      throw new Error('完整 402 必须原样进入统一最终化，不能切换账户或改写为泛化错误')
    }
    assert.equal(quotaResult.errorPayload.code, 'insufficient_user_quota', '402 必须解析并保留上游错误码')
    assert.match(String(quotaResult.errorPayload.message), /当前账户暂无生效套餐/, '402 必须解析并保留上游错误消息')
    await finalizeHandledUpstreamResponse({
      ...quotaInput,
      upstreamResponse: insufficientUserQuotaUpstreamResponse(),
      result: quotaResult,
      routingEffectsApplied: true
    })
    assert.equal(quotaResponse.statusCode, 402, '完整 402 必须以原始 HTTP 状态返回客户端')
    await auditQueue.flushAllAuditLogQueueAsync()
    const quotaAuditLog = repositories.listAuditLogs({ traceId: quotaTraceId }).items[0]
    assert(quotaAuditLog, '402 必须保留审计记录')
    assert.equal(quotaAuditLog.success, false, '402 审计总结果不得记为成功')
    const quotaAuditDetail = repositories.getAuditLogDetail(quotaAuditLog.id)
    assert.equal(quotaAuditDetail?.attempts[0]?.errorCode, 'insufficient_user_quota', '402 审计 attempt 必须记录上游错误码')
    assert.match(String(quotaAuditDetail?.attempts[0]?.errorMessage), /当前账户暂无生效套餐/, '402 审计 attempt 必须记录上游错误消息')
    const quotaUsageRecord = usageRecordQueue.peekPendingUsageRecordForTest()
    assert.equal(quotaUsageRecord?.success, false, '402 使用记录不得记为成功')
    assert.equal(quotaUsageRecord?.errorCode, 'insufficient_user_quota', '402 使用记录必须记录上游错误码')
    assert.match(String(quotaUsageRecord?.errorMessage), /当前账户暂无生效套餐/, '402 使用记录必须记录上游错误消息')
  } finally {
    auditQueue.clearAuditLogQueueForTest()
    auditQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
    usageRecordQueue.clearUsageRecordQueueForTest()
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
    database.closeStorageDatabases()
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

class OversizedResponsesMockResponse extends EventEmitter {
  locals: Record<string, unknown> = {}
  headersSent = false
  destroyed = false
  writableEnded = false
  writableFinished = false
  statusCode = 200
  private readonly headers = new Map<string, string | number | readonly string[]>()

  status(statusCode: number): this {
    this.statusCode = statusCode
    return this
  }

  setHeader(name: string, value: string | number | readonly string[]): this {
    this.headers.set(name.toLowerCase(), value)
    return this
  }

  getHeader(name: string): string | number | readonly string[] | undefined {
    return this.headers.get(name.toLowerCase())
  }

  getHeaders(): Record<string, string | number | readonly string[]> {
    return Object.fromEntries(this.headers)
  }

  hasHeader(name: string): boolean {
    return this.headers.has(name.toLowerCase())
  }

  write(_chunk: Uint8Array): boolean {
    this.headersSent = true
    return true
  }

  send(_body: Uint8Array): this {
    this.headersSent = true
    this.writableEnded = true
    this.writableFinished = true
    this.emit('finish')
    return this
  }

  json(body: unknown): this {
    return this.send(Buffer.from(JSON.stringify(body), 'utf8'))
  }

  end(): this {
    this.writableEnded = true
    this.writableFinished = true
    this.emit('finish')
    return this
  }

  destroy(): this {
    this.destroyed = true
    this.emit('close')
    return this
  }
}

function oversizedResponsesRequest(): Request {
  const headers: Record<string, string> = { 'content-type': 'application/json' }
  return {
    method: 'POST',
    originalUrl: '/v1/responses',
    path: '/v1/responses',
    headers,
    body: { model: 'gpt-5.6-sol', input: 'semantic failure regression' },
    header(name: string): string | undefined {
      return headers[name.toLowerCase()]
    }
  } as unknown as Request
}

function emptyChatJsonRequest(): Request {
  const headers: Record<string, string> = { 'content-type': 'application/json' }
  return {
    method: 'POST',
    originalUrl: '/v1/chat/completions',
    path: '/v1/chat/completions',
    headers,
    body: { model: 'gpt-5.6-sol', messages: [{ role: 'user', content: 'empty response regression' }] },
    header(name: string): string | undefined {
      return headers[name.toLowerCase()]
    }
  } as unknown as Request
}

function oversizedResponsesAccount(): UpstreamAccount {
  return {
    id: 'account_semantic_regression',
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: 'openai',
    protocolVersion: 'v1',
    systemAccountId: 'sys_semantic_regression',
    accountOwnerSystemAccountId: 'sys_semantic_regression',
    groupOwnerSystemAccountId: 'sys_semantic_regression',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: 'oversized responses semantic regression account',
    type: 'api_key',
    status: 'active',
    concurrencyLimit: 1,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    healthCheckEndpointMode: 'responses_json',
    baseUrl: 'https://upstream.invalid/v1',
    apiKey: 'sk-semantic-regression',
    streamFailureCount: 0,
    credentials: {}
  }
}

function oversizedFailedResponsesUpstreamResponse(): GatewayUpstreamResponse {
  const prefix = Buffer.from(`{"id":"resp_oversized","object":"response","output":[{"type":"message","content":"${'x'.repeat(2 * 1024 * 1024 + 256)}"}],"sta`, 'utf8')
  const suffix = Buffer.from('tus":"failed","error":{"message":"upstream failed"}}', 'utf8')
  return {
    status: 200,
    ok: true,
    headers: new Headers({ 'content-type': 'application/json; charset=utf-8' }),
    body: (async function* (): AsyncGenerator<Uint8Array> {
      yield prefix
      yield suffix
    })()
  }
}

function empty204UpstreamResponse(): GatewayUpstreamResponse {
  return {
    status: 204,
    ok: true,
    headers: new Headers(),
    body: null
  }
}

function insufficientUserQuotaUpstreamResponse(): GatewayUpstreamResponse {
  const body = Buffer.from(JSON.stringify({
    error: {
      message: '当前账户暂无生效套餐，请前往钱包页面或相关选项订阅',
      code: 'insufficient_user_quota'
    }
  }), 'utf8')
  return {
    status: 402,
    ok: false,
    headers: new Headers({ 'content-type': 'application/json' }),
    body: (async function * (): AsyncGenerator<Uint8Array> {
      yield body
    })()
  }
}

await assertOversizedResponsesFailureMarksAuditAndUsageFailed()
console.log('gateway upstream semantic policy regression passed')
