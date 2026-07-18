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
assert.match(
  dispatchSource,
  /if \(!interpretUpstreamResponseSemantics \|\| response\.ok\)/,
  '通用派发必须把任意已建立的 HTTP 响应交给下游管道'
)
assert.match(
  dispatchSource,
  /if \(!interpretUpstreamResponseSemantics \|\| response\.ok\)[\s\S]*handleFailedUpstreamResponse/,
  '只有明确允许语义解释时才能进入上游失败分类器'
)

console.log('gateway upstream semantic policy regression passed')
