import { strict as assert } from 'node:assert'

import { applyProviderAccountRequestOverridesToBody } from '../../modules/providers/drivers/_shared/provider-request-overrides.js'
import { GatewayRequestValidationError } from '../../modules/gateway/request/validation-error.js'
import type { DispatchAccountSecret } from '../../storage/openai-account-selector.types.js'

function account(providerCode: string, credentials: Record<string, unknown>): DispatchAccountSecret {
  return { providerCode, credentials } as DispatchAccountSecret
}

const anthropicBody = await applyProviderAccountRequestOverridesToBody(Buffer.from(JSON.stringify({
  model: 'claude-test',
  speed: 'fast',
  thinking: { type: 'enabled', budget_tokens: 4096 },
  output_config: { existing: true }
})), {
  account: account('anthropic', { service_tier_override: 'priority', reasoning_effort_override: 'high' }),
  upstreamModel: 'claude-test',
  wireFormat: 'anthropic_messages',
  modelCapabilities: { supportedServiceTiers: ['auto', 'priority'], supportedReasoningEfforts: ['low', 'high'] }
})
const anthropic = JSON.parse(Buffer.from(anthropicBody!).toString('utf8'))
assert.equal(anthropic.service_tier, 'priority')
assert.equal(anthropic.output_config.effort, 'high')
assert.equal(anthropic.output_config.existing, true)
assert.equal(anthropic.speed, 'fast', 'Anthropic speed 必须原样保护，不能被通用覆盖控件接管')
assert.equal(anthropic.thinking.budget_tokens, 4096, 'Anthropic thinking budget 必须原样保护')

const geminiBody = await applyProviderAccountRequestOverridesToBody(JSON.stringify({
  generationConfig: { thinkingConfig: { thinkingBudget: 2048 }, temperature: 0.2 }
}), {
  account: account('gemini', { reasoning_effort_override: 'low' }),
  upstreamModel: 'gemini-test',
  wireFormat: 'gemini_generate_content',
  modelCapabilities: { supportedServiceTiers: [], supportedReasoningEfforts: ['low', 'high'] }
})
const gemini = JSON.parse(geminiBody as string)
assert.equal(gemini.generationConfig.thinkingConfig.thinkingLevel, 'low')
assert.equal(gemini.generationConfig.thinkingConfig.thinkingBudget, 2048, 'Gemini thinking budget 必须原样保护')
assert.equal(gemini.generationConfig.temperature, 0.2)

await assert.rejects(
  () => applyProviderAccountRequestOverridesToBody('{}', {
    account: account('gemini', { service_tier_override: 'priority' }),
    upstreamModel: 'gemini-test',
    wireFormat: 'gemini_generate_content',
    modelCapabilities: { supportedServiceTiers: ['priority'], supportedReasoningEfforts: [] }
  }),
  (error: unknown) => error instanceof GatewayRequestValidationError
    && error.code === 'account_request_override_unsupported'
    && /没有可确认的服务等级 wire 字段/.test(error.message)
)

console.log('跨供应商账户请求覆盖回归通过：Anthropic/Gemini 明确映射，原生 speed/thinking budget 保留，无法映射返回 400')
