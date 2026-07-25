import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import type { Request } from 'express'

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
  isOpenAIGatewayImageGenerationModel
} from '../../modules/gateway/protocols/openai-v1/request-lane.js'

const genericProfiles: OpenAIGatewayClientProfile[] = ['generic_openai', 'generic_anthropic', 'generic_gemini']
const explicitProfiles: OpenAIGatewayClientProfile[] = ['codex', 'claude_code', 'gemini_cli']
for (const clientProfile of genericProfiles) {
  assert.equal(gatewayClientAllowsUpstreamSemanticInterpretation({ clientProfile }), false, `${clientProfile} 不得解释上游状态或错误类型`)
}
for (const clientProfile of explicitProfiles) {
  assert.equal(gatewayClientAllowsUpstreamSemanticInterpretation({ clientProfile }), true, `${clientProfile} 应保留专用协议语义`)
}

assert.equal(isOpaqueUpstreamFailoverAllowed({
  method: 'POST',
  originalUrl: '/v1/audio/speech',
  path: '/v1/audio/speech'
} as Request), false, '白名单外端点不得由网关自动接管或重放')
assert.equal(isOpaqueUpstreamFailoverAllowed({
  method: 'POST',
  originalUrl: '/v1/chat/completions',
  path: '/v1/chat/completions'
} as Request), true, '安全推理端点应保留请求内 opaque 接管能力')
for (const imagePath of ['/v1/images/generations', '/v1/images/edits']) {
  assert.equal(isOpaqueUpstreamFailoverAllowed({
    method: 'POST',
    originalUrl: imagePath,
    path: imagePath
  } as Request), false, `${imagePath} 可能已创建计费资源，不得自动重放`)
}
assert.equal(isOpaqueUpstreamFailoverAllowed({
  method: 'POST',
  originalUrl: '/v1/responses',
  path: '/v1/responses',
  body: { model: 'gpt-image-2', input: 'create an image' }
} as Request), false, 'Responses 路径只要进入图片 lane 也不得 opaque 重放')
const replayableInferenceRequest = {
  method: 'POST',
  originalUrl: '/v1/chat/completions',
  path: '/v1/chat/completions'
} as Request
assert.equal(automaticUpstreamReplayAllowedAfterDispatch(replayableInferenceRequest, 'text'), true)
assert.equal(automaticUpstreamReplayAllowedAfterDispatch(replayableInferenceRequest, 'image'), false, '图片 transport 一旦被调用就不得自动跨 Key/账户重放')
for (const sideEffectPath of ['/v1/audio/speech', '/v1/files', '/v1/vector_stores']) {
  assert.equal(automaticUpstreamReplayAllowedAfterDispatch({
    method: 'POST',
    originalUrl: sideEffectPath,
    path: sideEffectPath
  }, 'text'), false, `${sideEffectPath} 发出请求体后不得自动重放`)
}
const replayableResponsesRequest = {
  method: 'POST',
  originalUrl: '/v1/responses',
  path: '/v1/responses',
  body: {
    model: 'gpt-5.5',
    input: 'hello',
    background: false,
    tools: [{ type: 'function', name: 'lookup' }, { type: 'custom', name: 'grammar' }]
  }
} as Request
assert.equal(
  automaticUpstreamReplayAllowedAfterDispatch(replayableResponsesRequest, 'text'),
  true,
  '前台 Responses + 客户端执行工具仍可沿用推理重放能力'
)
assert.equal(automaticUpstreamReplayAllowedAfterDispatch({
  method: 'POST',
  originalUrl: '/v1/responses',
  path: '/v1/responses'
}, 'text'), false, 'Responses 请求体和解析元数据完全不可用时必须 fail-closed')
assert.equal(automaticUpstreamReplayAllowedAfterDispatch({
  method: 'POST',
  originalUrl: '/v1/embeddings',
  path: '/v1/embeddings'
}, 'text'), true, 'Responses fail-closed 不得改变 embeddings 推理重放边界')
assert.equal(automaticUpstreamReplayAllowedAfterDispatch({
  method: 'POST',
  originalUrl: '/v1/messages',
  path: '/v1/messages',
  body: {
    model: 'claude-haiku-4-5',
    messages: [{ role: 'user', content: 'hello' }],
    tools: [{ name: 'lookup', input_schema: { type: 'object' } }]
  }
}, 'text'), true, '普通 Anthropic Messages 与客户端执行工具属于可安全切号的文本推理')
assert.equal(automaticUpstreamReplayAllowedAfterDispatch({
  method: 'POST',
  originalUrl: '/v1/messages?beta=1',
  path: '/messages',
  gatewayParsedJsonBodyAvailable: true,
  gatewayParsedJsonBody: {
    model: 'claude-haiku-4-5',
    messages: [{ role: 'user', content: 'parsed metadata' }]
  }
}, 'text'), true, '挂载在 /v1 且只保留解析正文缓存的 Messages 仍应精确识别')
assert.equal(automaticUpstreamReplayAllowedAfterDispatch({
  method: 'POST',
  originalUrl: '/v1/messages/count_tokens',
  path: '/v1/messages/count_tokens'
}, 'text'), true, 'Anthropic Count Tokens 是只读计算，即使正文未解析也允许切号')
for (const [label, body] of [
  ['server tool', { model: 'claude-sonnet-4-5', messages: [], tools: [{ type: 'web_search_20250305', name: 'web_search' }] }],
  ['mcp_servers', { model: 'claude-sonnet-4-5', messages: [], mcp_servers: [{ type: 'url', url: 'https://mcp.invalid' }] }],
  ['container', { model: 'claude-sonnet-4-5', messages: [], container: 'container_123' }],
  ['malformed tools object', { model: 'claude-sonnet-4-5', messages: [], tools: {} }],
  ['malformed tools item', { model: 'claude-sonnet-4-5', messages: [], tools: [null] }],
  ['malformed custom type', { model: 'claude-sonnet-4-5', messages: [], tools: [{ type: '' }] }],
  ['malformed mcp_servers object', { model: 'claude-sonnet-4-5', messages: [], mcp_servers: {} }],
  ['malformed empty container', { model: 'claude-sonnet-4-5', messages: [], container: {} }]
] as const) {
  assert.equal(automaticUpstreamReplayAllowedAfterDispatch({
    method: 'POST',
    originalUrl: '/v1/messages',
    path: '/v1/messages',
    body
  }, 'text'), false, `Anthropic ${label} 可能执行供应商托管工作，必须保持 at-most-once`)
}
assert.equal(automaticUpstreamReplayAllowedAfterDispatch({
  method: 'POST',
  originalUrl: '/v1/messages',
  path: '/v1/messages'
}, 'text'), false, 'Anthropic Messages 请求体不可检查时必须 fail-closed')
assert.equal(automaticUpstreamReplayAllowedAfterDispatch({
  method: 'POST',
  originalUrl: '/foo/messages',
  path: '/foo/messages',
  body: { model: 'claude-haiku-4-5', messages: [] }
}, 'text'), false, '未知后缀路径不得仅因以 /messages 结尾就被纳入重放白名单')
for (const [label, body] of [
  ['background', { model: 'gpt-5.5', input: 'hello', background: true }],
  ['web_search', { model: 'gpt-5.5', input: 'hello', tools: [{ type: 'web_search' }] }],
  ['code_interpreter', { model: 'gpt-5.5', input: 'hello', tools: [{ type: 'code_interpreter' }] }],
  ['computer', { model: 'gpt-5.5', input: 'hello', tools: [{ type: 'computer' }] }],
  ['file_search tool_choice', { model: 'gpt-5.5', input: 'hello', tool_choice: { type: 'file_search' } }],
  ['future hosted extension', { model: 'gpt-5.5', input: 'hello', tools: [{ type: 'provider_hosted_future_tool' }] }]
] as const) {
  assert.equal(automaticUpstreamReplayAllowedAfterDispatch({
    method: 'POST',
    originalUrl: '/v1/responses',
    path: '/v1/responses',
    body
  }, 'text'), false, `${label} 可能已创建后台任务或执行服务端工具，不得自动重放`)
}
assert.equal(automaticUpstreamReplayAllowedAfterDispatch({
  method: 'POST',
  originalUrl: '/v1/responses',
  path: '/v1/responses',
  gatewayParsedJsonBodyAvailable: true,
  gatewayParsedJsonBody: { model: 'gpt-5.5', input: 'hello', background: true }
}, 'text'), false, '请求体只存在解析缓存时也必须识别 background')
assert.equal(automaticUpstreamReplayAllowedAfterDispatch({
  method: 'POST',
  originalUrl: '/v1/responses',
  path: '/v1/responses',
  gatewayRequestBody: {
    rawBodyBytes: 300_000,
    contentType: 'application/json',
    isJson: true,
    jsonParseStatus: 'deferred_large_json',
    jsonParseWarningBytes: 2 * 1024 * 1024
  }
}, 'text'), false, '大 JSON 无完整工具元数据时必须保守按最多一次处理')

const sideEffectRequest = {
  method: 'POST',
  originalUrl: '/v1/audio/speech',
  path: '/v1/audio/speech'
} as Request
for (const action of ['cooldown', 'disable'] as const) {
  assert.equal(
    accountErrorPolicyAllowsUpstreamReplayAfterDispatch(sideEffectRequest, 'text', { action }),
    false,
    `${action} 只授权状态变更，不授权已派发副作用请求重放`
  )
}
assert.equal(
  accountErrorPolicyAllowsUpstreamReplayAfterDispatch(sideEffectRequest, 'text', { action: 'retry_next' }),
  false,
  '显式 retry_next 也不能把已派发副作用请求变成可安全重放'
)
assert.equal(
  accountErrorPolicyAllowsUpstreamReplayAfterDispatch(replayableInferenceRequest, 'text', { action: 'cooldown' }),
  true,
  '普通推理请求仍可在执行 cooldown 后选择下一账户'
)
assert.equal(isOpenAIGatewayImageGenerationModel('gpt-image-1'), true)
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
const failureClassifierSource = readFileSync(new URL('../../modules/gateway/response/upstream-failure-classifier.ts', import.meta.url), 'utf8')
const routesSource = readFileSync(new URL('../../modules/gateway/routes.ts', import.meta.url), 'utf8')
const replayBlockedSource = readFileSync(new URL('../../modules/gateway/response/upstream-replay-blocked.ts', import.meta.url), 'utf8')
const responseInspectionSource = readFileSync(new URL('../../modules/gateway/response/inspection.ts', import.meta.url), 'utf8')
const requestPreflightSource = readFileSync(new URL('../../modules/gateway/request/preflight.ts', import.meta.url), 'utf8')
const streamSource = readFileSync(new URL('../../modules/gateway/response/stream.ts', import.meta.url), 'utf8')
const compactPreflightSource = readFileSync(new URL('../../modules/gateway/codex-responses/compact-preflight.ts', import.meta.url), 'utf8')
const candidateSelectionSource = readFileSync(new URL('../../modules/gateway/routing/hot-quality-candidate-selection.ts', import.meta.url), 'utf8')
const finalizationSource = readFileSync(new URL('../../modules/gateway/response/finalization.ts', import.meta.url), 'utf8')
const auxiliarySource = readFileSync(new URL('../../modules/gateway/hybrid/auxiliary-dispatch.service.ts', import.meta.url), 'utf8')
const hybridScoringSource = readFileSync(new URL('../../modules/gateway/hybrid/scoring.service.ts', import.meta.url), 'utf8')
const hybridQualitySource = readFileSync(new URL('../../modules/gateway/hybrid/quality-inspection.service.ts', import.meta.url), 'utf8')
assert.match(
  failureDispatchSource,
  /isOpaqueUpstreamFailoverAllowed/,
  '安全推理端点的未知完整 HTTP 失败必须进入统一 opaque 接管边界'
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
assert.match(
  dispatchSource,
  /automaticUpstreamReplayAllowedAfterDispatch\(req, requestLane\)[\s\S]*phase: 'upstream_transport'[\s\S]*throw new UpstreamReplayBlockedError/,
  '请求体可能已被上游接受的图片或副作用 transport 失败必须终止服务端重放'
)
assert.match(
  routesSource,
  /automaticUpstreamReplayAllowedAfterDispatch\(req, currentPreflight\.requestLane\)[\s\S]*phase: 'upstream_response_body'[\s\S]*throw new UpstreamReplayBlockedError/,
  '图片或副作用请求的 2xx 正文读取失败必须终止服务端重放'
)
assert.match(
  routesSource,
  /if \(error instanceof UpstreamReplayBlockedError\) \{\s*throw error\s*\}[\s\S]*switchToFallbackGroup/,
  '图片重放阻断错误必须在跨组 fallback 之前终止调度'
)
assert.match(
  requestPreflightSource,
  /hybridRoute\.outcome === 'selected'[\s\S]*requestLane = resolveOpenAIGatewayRequestLane\(req\)/,
  '混合路由初次改写模型后必须重新判定图片 lane'
)
assert.match(
  requestPreflightSource,
  /requestLane !== 'image'[\s\S]*await accountModelMappingsTargetImage\(req, candidateFilter\.accounts, systemAccountId\)[\s\S]*requestLane = 'image'[\s\S]*applyOpenAIGatewayImagePermissionPreflight/,
  '候选账户把客户端模型映射为图片模型时，必须在权限检查和上游派发前提升为图片 lane'
)
assert.match(
  requestPreflightSource,
  /listCachedProviderModelCatalogAsync\([\s\S]*supportedApiProtocols\.includes\('images'\)[\s\S]*outputModalities\?\.includes\('image'\)/,
  '非标准映射模型必须复用模型目录的显式图片能力，不能只依赖模型名称前缀'
)
assert.match(
  routesSource,
  /resolveNextHybridGatewayRoute\([\s\S]*requestLane: resolveOpenAIGatewayRequestLane\(req\)/,
  '混合质量升级改写模型后必须把新 lane 传入重入预检'
)
assert.match(routesSource, /if \(!automaticUpstreamReplayAllowedAfterDispatch\(req, currentPreflight\.requestLane\)\) \{/, '任意流式/正文重试都必须服从统一请求语义门禁')
assert.doesNotMatch(routesSource, /userAuthorizedReplay|&& !userAuthorizedReplay/, '用户策略不得形成绕过副作用 at-most-once 的旁路')
assert.match(
  replayBlockedSource,
  /gatewayUpstreamOutcomeUnknownErrorCode = 'upstream_outcome_unknown'[\s\S]*gatewayUpstreamOutcomeUnknownMessage = '上游可能已接收请求，但结果未知；网关未自动重放'[\s\S]*upstreamAutomaticReplayBlockedAuditLabel = 'upstream_automatic_replay_blocked'/,
  '副作用结果未知必须使用通用稳定网关错误与审计标签'
)
assert.match(
  routesSource,
  /gatewayUpstreamOutcomeUnknownErrorCode[\s\S]*!automaticReplayBlocked && shouldSendDispatchExhaustedProtocolRetry[\s\S]*!automaticReplayBlocked && options\.exposeUpstreamDiagnostics/,
  '副作用结果未知必须返回非重试型稳定网关错误，并禁止协议重试提示或诊断透传'
)
assert.doesNotMatch(`${routesSource}\n${dispatchSource}`, /image_request_automatic_replay_blocked|图片上游|图片请求/, '副作用请求不得复用图片专属自动重放阻止文案或标签')
assert.match(
  responseInspectionSource,
  /policy\.source !== 'system_default' && policy\.action === 'retry_next_account'[\s\S]*replayAuthority: 'explicit_user_policy'/,
  '显式响应策略的归因标签必须可审计，但不得被消费为副作用重放旁路'
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
  /if \(!explicitPolicyDecision\) \{[\s\S]*action: 'return_response'/,
  '显式规则正文不命中且端点不可重放时必须保持透明返回'
)
assert.match(
  failureDispatchSource,
  /if \(!explicitPolicyDecision\) \{[\s\S]*isOpaqueUpstreamFailoverAllowed\(req\)[\s\S]*handleOpaqueFailedUpstreamResponse\(input, responseBodyRead\)/,
  '显式规则只命中状态预筛选时，安全推理端点必须复用已检查正文进入 opaque 接管'
)
assert.match(
  failureDispatchSource,
  /const responseBodyRead = inspectedBody \?\?[\s\S]*if \(inspectedBody\) \{\s*await inspectedBody\.close\(\)/,
  'opaque 接管必须复用 policy 已读取结果并关闭剩余正文，不得二次消费原始 body'
)
assert.match(
  failureDispatchSource,
  /!automaticUpstreamReplayAllowedAfterDispatch\(req, input\.requestLane\)[\s\S]*handleOpaqueFailedUpstreamResponse\(input, undefined, true\)/,
  '无策略预筛选时，所有不可重放请求的未知 HTTP 失败都必须消费并审计后进入稳定终态'
)
assert.match(
  failureDispatchSource,
  /!explicitPolicyDecision[\s\S]*!automaticUpstreamReplayAllowedAfterDispatch\(req, input\.requestLane\)[\s\S]*handleOpaqueFailedUpstreamResponse\(input, responseBodyRead, true\)/,
  '状态预筛选命中但用户规则正文不命中时，不可重放请求仍不得透传供应商原错'
)
assert.match(
  failureDispatchSource,
  /replayBlocked[\s\S]*accountErrorPolicyAllowsUpstreamReplayAfterDispatch\(req, input\.requestLane, explicitPolicyDecision\)/,
  '账户错误策略必须经过统一请求语义门禁，不能越过副作用 at-most-once'
)
assert.match(
  failureDispatchSource,
  /applyAccountErrorHandlingWithCacheInvalidation\(account,[\s\S]*if \(!accountErrorPolicyAllowsUpstreamReplayAfterDispatch\(req, input\.requestLane, explicitPolicyDecision\)\)[\s\S]*failureKind: 'explicit_policy'/,
  'cooldown/disable 必须先执行显式状态动作，再阻止副作用请求自动跳到下一账户'
)
assert.match(
  finalizationSource,
  /pipeNonStreamUpstreamResponseForInspection[\s\S]*maxLifetimeMs: input\.timeoutProfile\.uncommittedAttemptMaxLifetimeMs[\s\S]*pipeNonStreamUpstreamResponse[\s\S]*maxLifetimeMs: input\.timeoutProfile\.uncommittedAttemptMaxLifetimeMs/,
  '非流式响应的普通转发与检查缓冲都必须受 lane 独立绝对上限约束'
)
assert.match(
  dispatchSource,
  /failedResponseResult\.action === 'replay_blocked'[\s\S]*phase: 'opaque_upstream_response'[\s\S]*throw new UpstreamReplayBlockedError/,
  '图片未知 HTTP 失败必须返回稳定网关错误，不能透传供应商原错或切号'
)
assert.match(
  dispatchSource,
  /catch \(error\) \{\s*if \(error instanceof UpstreamReplayBlockedError\) \{\s*throw error\s*\}[\s\S]*handleUpstreamRequestError/,
  '图片 HTTP 重放阻断异常不得被通用 catch 再分类为 transport 故障或重复写状态'
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
  finalizationSource,
  /!interpretUpstreamResponseSemantics \|\| upstreamResponse\.ok/,
  '通用响应不得绕过 HTTP 成功条件写入成功审计或用量'
)
assert.match(
  finalizationSource,
  /forwardedResponseSuccessful = upstreamResponse\.ok/,
  '响应最终成功必须统一取决于 HTTP 成功条件'
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

console.log('gateway upstream semantic policy regression passed')
