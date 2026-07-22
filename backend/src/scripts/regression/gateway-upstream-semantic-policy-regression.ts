import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import {
  gatewayClientAllowsUpstreamSemanticInterpretation,
  type OpenAIGatewayClientProfile
} from '../../modules/gateway/client-profiles/strategy.js'

const genericProfiles: OpenAIGatewayClientProfile[] = ['generic_openai', 'generic_anthropic', 'generic_gemini']
const explicitProfiles: OpenAIGatewayClientProfile[] = ['codex', 'claude_code', 'gemini_cli']
for (const clientProfile of genericProfiles) {
  assert.equal(gatewayClientAllowsUpstreamSemanticInterpretation({ clientProfile }), false, `${clientProfile} 不得解释上游状态或错误类型`)
}
for (const clientProfile of explicitProfiles) {
  assert.equal(gatewayClientAllowsUpstreamSemanticInterpretation({ clientProfile }), true, `${clientProfile} 应保留专用协议语义`)
}

const dispatchSource = readFileSync(new URL('../../modules/gateway/dispatch/upstream-dispatch.ts', import.meta.url), 'utf8')
const finalizationSource = readFileSync(new URL('../../modules/gateway/response/finalization.ts', import.meta.url), 'utf8')
const auxiliarySource = readFileSync(new URL('../../modules/gateway/hybrid/auxiliary-dispatch.service.ts', import.meta.url), 'utf8')
const hybridScoringSource = readFileSync(new URL('../../modules/gateway/hybrid/scoring.service.ts', import.meta.url), 'utf8')
const hybridQualitySource = readFileSync(new URL('../../modules/gateway/hybrid/quality-inspection.service.ts', import.meta.url), 'utf8')
assert.match(
  dispatchSource,
  /failedResponseResult\.action === 'return_response'[\s\S]*response: failedResponseResult\.response/,
  '未命中用户显式规则的完整 HTTP 响应必须直接提交给下游管道'
)
assert.match(
  dispatchSource,
  /const failedResponseResult = await handleFailedUpstreamResponse/,
  'HTTP 失败只能由用户显式账户错误策略决定是否切号，不得按客户端画像分流默认动作'
)
assert.doesNotMatch(
  dispatchSource,
  /isOpaqueUpstreamFailoverAllowed/,
  '完整 HTTP 非成功响应不得按端点建立默认接管白名单'
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
