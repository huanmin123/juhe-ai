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
  isOpenAIGatewayImageGenerationModel,
  resolveOpenAIGatewayRequestLane
} from '../../modules/gateway/protocols/openai-v1/request-lane.js'

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
  assert.equal(isOpaqueUpstreamFailoverAllowed(request), true, `${label} 未交付结果时必须允许请求内切换候选`)
  assert.equal(automaticUpstreamReplayAllowedAfterDispatch(request, lane), true, `${label} 必须与普通文本共用统一切号规则`)
}

for (const action of ['cooldown', 'disable', 'retry_next'] as const) {
  assert.equal(
    accountErrorPolicyAllowsUpstreamReplayAfterDispatch(sideEffectRequest, 'text', { action }),
    true,
    `${action} 的账户状态动作不得阻断当前请求继续切换候选`
  )
}
assert.equal(
  accountErrorPolicyAllowsUpstreamReplayAfterDispatch(replayableInferenceRequest, 'text', { action: 'cooldown' }),
  true,
  '普通推理请求仍可在执行 cooldown 后选择下一账户'
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
const auxiliarySource = readFileSync(new URL('../../modules/gateway/hybrid/auxiliary-dispatch.service.ts', import.meta.url), 'utf8')
const hybridScoringSource = readFileSync(new URL('../../modules/gateway/hybrid/scoring.service.ts', import.meta.url), 'utf8')
const hybridQualitySource = readFileSync(new URL('../../modules/gateway/hybrid/quality-inspection.service.ts', import.meta.url), 'utf8')
assert.match(
  failureDispatchSource,
  /isOpaqueUpstreamFailoverAllowed/,
  '所有端点的未知完整 HTTP 失败必须进入统一 opaque 接管边界'
)
assert.match(
  requestLaneSource,
  /automaticUpstreamReplayAllowedAfterDispatch\([\s\S]*?\): boolean \{\s*return true\s*\}/,
  '所有请求类型和 lane 必须共用无条件候选切换许可'
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
assert.doesNotMatch(routesSource, /userAuthorizedReplay|&& !userAuthorizedReplay/, '用户策略不得形成另一套候选切换旁路')
assert.doesNotMatch(`${routesSource}\n${dispatchSource}`, /image_request_automatic_replay_blocked|图片上游|图片请求/, '候选切换不得存在图片专属阻断文案或标签')
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
  /if \(!explicitPolicyDecision\) \{\s*return handleOpaqueFailedUpstreamResponse\(input, responseBodyRead, failureBodyFacts\)\s*\}/,
  '显式策略正文未命中时必须复用已读取正文及其单次解析事实进入统一 opaque 切号'
)
assert.match(
  failureDispatchSource,
  /const policyCouldMatch = input\.accountStateMutationEnabled !== false[\s\S]*accountErrorPolicyCouldMatchStatus\(account, response\.status\)[\s\S]*if \(!policyCouldMatch\) \{\s*return handleOpaqueFailedUpstreamResponse\(input\)\s*\}/,
  '无策略状态预筛选路径必须直接进入统一 opaque 切号，且不得解析 generic 错误正文'
)
assert.match(
  failureDispatchSource,
  /const responseBodyRead = inspectedBody \?\?[\s\S]*if \(inspectedBody\) \{\s*await inspectedBody\.close\(\)/,
  'opaque 接管必须复用 policy 已读取结果并关闭剩余正文，不得二次消费原始 body'
)
assert.match(
  failureDispatchSource,
  /applyAccountErrorHandlingWithCacheInvalidation\(account,[\s\S]*action: 'skip_account'[\s\S]*failureKind: 'explicit_policy'/,
  'cooldown/disable 必须先执行显式状态动作，再无条件沿统一规则切换候选'
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
