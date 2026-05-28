import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

import {
  parseOpenAIUsageFromJsonBuffer
} from '../../modules/gateway/openai-gateway-usage.js'
import {
  inspectOpenAIStreamText,
  OpenAIStreamInspector
} from '../../modules/gateway/openai-gateway-stream-inspection.js'
import { buildProviderCostBreakdown, estimateProviderCostUsd, getProviderModelPricing, listProviderModelPricing } from '../../modules/model-pricing/model-pricing.service.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { retryDelayMs, retryAttemptCount, sequenceRetryPolicy } from '../../shared/retry-policy.js'
import { usageSummaryFromAggregate } from '../../storage/usage-stats-helpers.js'

const scriptDirectory = dirname(fileURLToPath(import.meta.url))
const backendSrcDirectory = resolve(scriptDirectory, '../..')

function jsonBuffer(value: unknown): Buffer {
  return Buffer.from(JSON.stringify(value), 'utf8')
}

function defined(value: object): Record<string, unknown> {
  return Object.fromEntries(Object.entries(value).filter(([, item]) => item !== undefined))
}

const responsesUsage = parseOpenAIUsageFromJsonBuffer(jsonBuffer({
  usage: {
    input_tokens: 1000,
    output_tokens: 200,
    input_tokens_details: {
      cached_tokens: 300
    }
  }
}))
assert.deepEqual(defined(responsesUsage), {
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300
})

const chatUsage = parseOpenAIUsageFromJsonBuffer(jsonBuffer({
  usage: {
    prompt_tokens: '1200',
    completion_tokens: 150,
    prompt_tokens_details: {
      cached_tokens: 400
    }
  }
}))
assert.deepEqual(defined(chatUsage), {
  inputTokens: 1200,
  outputTokens: 150,
  cacheReadTokens: 400
})

const usageAfterLargePayload = parseOpenAIUsageFromJsonBuffer(jsonBuffer({
  output: [{ content: 'x'.repeat(1024 * 1024) }],
  usage: {
    input_tokens: 321,
    output_tokens: 45
  }
}))
assert.deepEqual(defined(usageAfterLargePayload), {
  inputTokens: 321,
  outputTokens: 45
})

const responsesStreamInspection = inspectOpenAIStreamText([
  'event: response.output_text.delta',
  'data: {"type":"response.output_text.delta","delta":"hi"}',
  '',
  'event: response.completed',
  'data: {"type":"response.completed","response":{"usage":{"input_tokens":1000,"output_tokens":200,"input_tokens_details":{"cached_tokens":300}}}}',
  ''
].join('\n'))
assert.equal(responsesStreamInspection.terminalReceived, true)
assert.equal(responsesStreamInspection.outputReceived, true)
assert.equal(responsesStreamInspection.estimatedOutputTokens, 1)
assert.deepEqual(defined(responsesStreamInspection.usage), defined(responsesUsage))

const chatStreamInspection = inspectOpenAIStreamText([
  'data: {"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"}}]}',
  '',
  'data: {"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":1200,"completion_tokens":150,"prompt_tokens_details":{"cached_tokens":400}}}',
  '',
  'data: [DONE]',
  ''
].join('\n'))
assert.equal(chatStreamInspection.terminalReceived, true)
assert.equal(chatStreamInspection.outputReceived, true)
assert.equal(chatStreamInspection.estimatedOutputTokens, 1)
assert.deepEqual(defined(chatStreamInspection.usage), defined(chatUsage))

const failedAfterOutputStreamInspection = inspectOpenAIStreamText([
  'event: response.output_text.delta',
  'data: {"type":"response.output_text.delta","delta":"hello"}',
  '',
  'event: response.failed',
  'data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_is_overloaded","message":"busy"}}}',
  ''
].join('\n'))
assert.equal(failedAfterOutputStreamInspection.terminalReceived, true)
assert.equal(failedAfterOutputStreamInspection.failedReceived, true)
assert.equal(failedAfterOutputStreamInspection.outputReceived, true)
assert.equal(failedAfterOutputStreamInspection.estimatedOutputTokens, 2)
assert.deepEqual(defined(failedAfterOutputStreamInspection.usage), {})

const outputItemAddedOnlyInspection = inspectOpenAIStreamText([
  'event: response.output_item.added',
  'data: {"type":"response.output_item.added","output_index":0,"item":{"id":"item_1","type":"message","status":"in_progress"}}',
  ''
].join('\n'))
assert.equal(outputItemAddedOnlyInspection.outputReceived, false)
assert.equal(outputItemAddedOnlyInspection.estimatedOutputTokens, undefined)

const outputItemDoneOnlyInspection = inspectOpenAIStreamText([
  'event: response.output_item.done',
  'data: {"type":"response.output_item.done","output_index":0,"item":{"id":"item_1","type":"message","status":"completed","content":[{"type":"output_text","text":"hello world"}]}}',
  ''
].join('\n'))
assert.equal(outputItemDoneOnlyInspection.outputReceived, true)
assert.equal(outputItemDoneOnlyInspection.estimatedOutputTokens, 3)

const audioBase64DeltaInspection = inspectOpenAIStreamText([
  'event: response.audio.delta',
  `data: {"type":"response.audio.delta","delta":{"data":"${'QUFB'.repeat(160)}","transcript":"hi"}}`,
  ''
].join('\n'))
assert.equal(audioBase64DeltaInspection.outputReceived, true)
assert.equal(audioBase64DeltaInspection.estimatedOutputTokens, 1)

const textBase64LikeDeltaInspection = inspectOpenAIStreamText([
  'event: response.output_text.delta',
  `data: {"type":"response.output_text.delta","delta":"${'QUFB'.repeat(160)}"}`,
  ''
].join('\n'))
assert.equal(textBase64LikeDeltaInspection.outputReceived, true)
assert.equal(textBase64LikeDeltaInspection.estimatedOutputTokens, 160)

const oversizedStreamInspection = inspectOpenAIStreamText(`data: ${'x'.repeat(300 * 1024)}`)
assert.equal(oversizedStreamInspection.skipped, true)
assert.equal(oversizedStreamInspection.failedReceived, false)
assert.equal(oversizedStreamInspection.terminalReceived, false)

const largeImageLightweightInspector = new OpenAIStreamInspector()
const largeImageLightweightInspection = largeImageLightweightInspector.pushChunk(Buffer.from([
  'event: response.image_generation_call.partial_image',
  `data: {"type":"response.image_generation_call.partial_image","item_id":"ig_light","partial_image_b64":"${'a'.repeat(300 * 1024)}"}`,
  '',
  'event: response.completed',
  'data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":7,"output_tokens":11}}}',
  ''
].join('\n'), 'utf8'))
assert.equal(largeImageLightweightInspection.skipped, false)
assert.equal(largeImageLightweightInspection.imageOutputReceived, true)
assert.equal(largeImageLightweightInspection.terminalReceived, true)
assert.deepEqual(defined(largeImageLightweightInspection.usage), {
  inputTokens: 7,
  outputTokens: 11
})

const imageContinuationInspector = new OpenAIStreamInspector()
imageContinuationInspector.pushText([
  'event: response.image_generation_call.partial_image',
  'data: {"type":"response.image_generation_call.partial_image","partial_image_b64":"aaaa"}',
  ''
].join('\n'))
const imageContinuationInspection = imageContinuationInspector.pushChunk(
  Buffer.from(`data: ${'x'.repeat(300 * 1024)}`, 'utf8'),
  { lightweightImageStream: true }
)
assert.equal(imageContinuationInspection.skipped, false)
assert.equal(imageContinuationInspection.imageOutputReceived, true)

const splitImageTerminalInspector = new OpenAIStreamInspector()
splitImageTerminalInspector.pushText([
  'event: response.image_generation_call.partial_image',
  'data: {"type":"response.image_generation_call.partial_image","partial_image_b64":"aaaa"}',
  ''
].join('\n'))
const splitImageTerminalPrefix = splitImageTerminalInspector.pushChunk(
  Buffer.from('event: image_generation.completed\ndata: {"type":"image_generation.completed","b64_json":"'),
  { lightweightImageStream: true }
)
assert.equal(splitImageTerminalPrefix.imageOutputReceived, true)
assert.equal(splitImageTerminalPrefix.terminalReceived, false, '携带图片正文的终止事件拆包时不应在事件边界前提前结束')
const splitImageTerminalComplete = splitImageTerminalInspector.pushChunk(
  Buffer.from(`${'a'.repeat(300 * 1024)}","usage":{"input_tokens":9,"output_tokens":13}}\n\n`),
  { lightweightImageStream: true }
)
assert.equal(splitImageTerminalComplete.terminalReceived, true)
assert.deepEqual(defined(splitImageTerminalComplete.usage), {
  inputTokens: 9,
  outputTokens: 13
})

const gpt41Cost = estimateProviderCostUsd({
  providerCode: 'openai',
  model: 'gpt-4.1',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300
})
assert.equal(gpt41Cost, 0.00315)

const openAIModelPricingList = listProviderModelPricing('openai')
const availableOpenAIModels = new Set(openAIModelPricingList.map((item) => item.model))
const openAIModelPricingById = new Map(openAIModelPricingList.map((item) => [item.model, item]))
for (const item of openAIModelPricingList) {
  assert.ok(item.releaseDate, `${item.model} should expose release date`)
}
for (let index = 1; index < openAIModelPricingList.length; index += 1) {
  const previous = openAIModelPricingList[index - 1]
  const current = openAIModelPricingList[index]
  assert.ok(
    (previous.releaseDate ?? '') >= (current.releaseDate ?? ''),
    `${previous.model} (${previous.releaseDate}) should sort before ${current.model} (${current.releaseDate})`
  )
}
for (const id of [
  'gpt-5.3-codex',
  'gpt-5.2',
  'gpt-5.2-2025-12-11',
  'gpt-5.2-chat-latest',
  'gpt-5.2-codex',
  'gpt-5.1',
  'gpt-5.1-chat-latest',
  'gpt-5.1-codex',
  'gpt-5.1-codex-max',
  'gpt-5.1-codex-mini',
  'gpt-5',
  'gpt-5-chat-latest',
  'gpt-5-codex',
  'gpt-5-mini',
  'gpt-5-nano',
  'gpt-5-pro',
  'o1',
  'o1-pro',
  'o3-mini',
  'gpt-4',
  'gpt-4-turbo',
  'gpt-3.5-turbo',
  'gpt-image-1',
  'babbage-002',
  'davinci-002'
]) {
  assert.equal(availableOpenAIModels.has(id), true, `${id} should be exposed while official shutdown date has not passed`)
  assert.ok(getProviderModelPricing('openai', id), `${id} should resolve pricing`)
}
for (const id of [
  'gpt-4.5-preview',
  'gpt-5.3-codex-spark',
  'codex-mini-latest',
  'o1-mini'
]) {
  assert.equal(availableOpenAIModels.has(id), false, `${id} should not be exposed`)
  assert.equal(getProviderModelPricing('openai', id), undefined, `${id} should not resolve pricing`)
}

assert.deepEqual(openAIModelPricingById.get('gpt-5.2')?.supportedApiProtocols, ['chat_completions', 'responses'])
assert.deepEqual(openAIModelPricingById.get('gpt-5.2-2025-12-11')?.supportedApiProtocols, ['chat_completions', 'responses'])
assert.deepEqual(openAIModelPricingById.get('gpt-5.3-codex')?.supportedApiProtocols, ['responses'])
assert.deepEqual(openAIModelPricingById.get('gpt-5.2-codex')?.supportedApiProtocols, ['responses'])
assert.deepEqual(openAIModelPricingById.get('gpt-image-1')?.supportedApiProtocols, ['images'])
assert.deepEqual(openAIModelPricingById.get('gpt-4o-mini-tts')?.supportedApiProtocols, ['audio'])
assert.equal(openAIModelPricingById.get('gpt-5.5')?.releaseDate, '2026-04-23')
assert.equal(openAIModelPricingById.get('gpt-5.4-mini')?.releaseDate, '2026-03-17')
assert.equal(openAIModelPricingById.get('gpt-5.3-codex')?.releaseDate, '2026-02-01')
assert.equal(openAIModelPricingById.get('gpt-5.2')?.releaseDate, '2025-12-11')
assert.equal(openAIModelPricingById.get('gpt-4.1')?.releaseDate, '2025-04-14')
assert.equal(openAIModelPricingById.get('babbage-002')?.releaseDate, '2024-01-04')

assert.equal(estimateProviderCostUsd({
  providerCode: 'openai',
  model: 'gpt-5.2-codex',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300
}), 0.0040775)
assert.equal(estimateProviderCostUsd({
  providerCode: 'openai',
  model: 'gpt-5.1-codex-mini',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300
}), 0.0005825)
assert.equal(estimateProviderCostUsd({
  providerCode: 'openai',
  model: 'gpt-3.5-turbo',
  inputTokens: 1000,
  outputTokens: 200
}), 0.0008)

const gpt55ImageInputAsNormalTokensCost = estimateProviderCostUsd({
  providerCode: 'openai',
  model: 'gpt-5.5',
  inputTokens: 1000,
  outputTokens: 200,
  inputImageTokens: 25
})
assert.equal(gpt55ImageInputAsNormalTokensCost, 0.011)

const gpt55Breakdown = buildProviderCostBreakdown({
  providerCode: 'openai',
  model: 'gpt-5.5',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300,
  inputImageTokens: 25
})
assert.equal(gpt55Breakdown?.inputImageCostUsd, undefined)
assert.equal(gpt55Breakdown?.inputImageUsdPer1M, undefined)
assert.equal(gpt55Breakdown?.cacheReadUsdPer1M, 0.5)

const imageUsage = parseOpenAIUsageFromJsonBuffer(jsonBuffer({
  usage: {
    input_tokens: 100,
    output_tokens: 1000,
    input_tokens_details: {
      image_tokens: 25,
      cached_tokens: 10
    }
  }
}))
assert.deepEqual(defined(imageUsage), {
  inputTokens: 100,
  outputTokens: 1000,
  cacheReadTokens: 10,
  inputImageTokens: 25
})

const imageCost = estimateProviderCostUsd({
  providerCode: 'openai',
  model: 'gpt-image-2',
  inputTokens: imageUsage.inputTokens,
  outputTokens: imageUsage.outputTokens,
  cacheReadTokens: imageUsage.cacheReadTokens,
  inputImageTokens: imageUsage.inputImageTokens,
  outputImageTokens: imageUsage.outputImageTokens
})
assert.equal(imageCost, 0.0305375)

const imageBreakdown = buildProviderCostBreakdown({
  providerCode: 'openai',
  model: 'gpt-image-2',
  inputTokens: imageUsage.inputTokens,
  outputTokens: imageUsage.outputTokens,
  cacheReadTokens: imageUsage.cacheReadTokens,
  inputImageTokens: imageUsage.inputImageTokens,
  outputImageTokens: imageUsage.outputImageTokens
})
assert.equal(imageBreakdown?.inputImageUsdPer1M, 8)
assert.equal(imageBreakdown?.outputImageUsdPer1M, 30)

const cachedUsageSummary = usageSummaryFromAggregate({
  request_count: 1,
  input_tokens: 1000,
  output_tokens: 200,
  cache_read_tokens: 300,
  total_cost: 0.00315,
  last_used_at: null
})
assert.equal(cachedUsageSummary.totalTokens, 1200)

const cooldownRetestRetryPolicy = sequenceRetryPolicy('test_cooldown_account_retest_revival', [3000, 10000, 30000])
assert.equal(retryAttemptCount(cooldownRetestRetryPolicy), 4)
assert.deepEqual([
  retryDelayMs(cooldownRetestRetryPolicy, 1),
  retryDelayMs(cooldownRetestRetryPolicy, 2),
  retryDelayMs(cooldownRetestRetryPolicy, 3)
], [3000, 10000, 30000])
await assertRetryQueueRetainsFailedItems()

const gatewayUsageRecordsSource = readSource('modules/gateway/openai-gateway-usage-records.ts')
assert.match(gatewayUsageRecordsSource, /function recordCompletedUpstreamAttempt/)
assert.match(gatewayUsageRecordsSource, /recordClientAbortedUpstreamAttempt/)

const gatewayResponseFinalizationSource = readSource('modules/gateway/openai-gateway-response-finalization.ts')
assert.match(gatewayResponseFinalizationSource, /applyOpenAIStreamUsageFallback/)
assert.match(gatewayResponseFinalizationSource, /gateway_stream_usage_estimated/)
assert.match(gatewayResponseFinalizationSource, /errorMessage:\s*'上游响应体为空'/)

const gatewayFailureDispatchSource = readSource('modules/gateway/openai-gateway-failure-dispatch.ts')
assert.match(gatewayFailureDispatchSource, /shouldRecordAbortedUpstreamAttempt/)
assert.match(gatewayFailureDispatchSource, /suppressGatewayAccountLocally/)
assert.doesNotMatch(gatewayFailureDispatchSource, /shouldRetryPolicyAttempt/)
assert.doesNotMatch(gatewayFailureDispatchSource, /shouldRetryAttempt\(/)

const retryPolicySource = readSource('shared/retry-policy.ts')
assert.match(retryPolicySource, /export function retryAttemptCount/)
assert.match(retryPolicySource, /export function shouldRetryPolicyAttempt/)

const gatewayDispatchHelpersSource = readSource('modules/gateway/openai-gateway-dispatch-helpers.ts')
assert.doesNotMatch(gatewayDispatchHelpersSource, /temporaryUnschedulableRetryPolicy/)
assert.doesNotMatch(gatewayDispatchHelpersSource, /gateway_temporary_unschedulable_same_account_retry/)

const gatewayUpstreamDispatchSource = readSource('modules/gateway/openai-gateway-upstream-dispatch.ts')
assert.match(gatewayUpstreamDispatchSource, /const maxAttemptCount = 1/)
assert.doesNotMatch(gatewayUpstreamDispatchSource, /temporaryUnschedulableRetryPolicy/)
assert.doesNotMatch(gatewayUpstreamDispatchSource, /retryAttemptCount\(retryPolicy\)/)
assert.doesNotMatch(gatewayUpstreamDispatchSource, /normalizeRetryCount\(settings\.temporaryUnschedulableRetryAttempts\)/)

const oauthAccessTokenRefreshSource = readSource('modules/openai-oauth/openai-oauth-access-token-refresh.service.ts')
assert.match(oauthAccessTokenRefreshSource, /openAIOAuthRefreshRaceRetryPolicy/)
assert.match(oauthAccessTokenRefreshSource, /shouldRetryPolicyAttempt\(attempt, openAIOAuthRefreshRaceRetryPolicy\)/)

const settingsRepositorySource = readSource('storage/settings.repository.ts')
assert.match(settingsRepositorySource, /createAppCache/)
assert.match(settingsRepositorySource, /const systemSettingsCache = createAppCache/)
assert.match(settingsRepositorySource, /const globalSettingsCache = createAppCache/)
assert.match(settingsRepositorySource, /export function clearSettingsRepositoryCache/)
assert.match(settingsRepositorySource, /clearSystemSettingsCache\(\)\s*\n\s*notifyGatewayRuntimeCacheInvalidation\('settings_updated'\)/)

const usageStatsHelpersSource = readSource('storage/usage-stats-helpers.ts')
assert.match(usageStatsHelpersSource, /cachedUsageStatsTimezone/)
assert.match(usageStatsHelpersSource, /usageStatsTimezoneCacheTtlMs/)
assert.match(usageStatsHelpersSource, /export function clearUsageStatsTimezoneCache/)

const accountTestSource = readSource('modules/accounts/account-test.service.ts')
assert.match(accountTestSource, /handleOpenAIGatewayRequest/)
assert.match(accountTestSource, /candidateAccounts:\s*\[resolved\.account\]/)
assert.match(accountTestSource, /disableSessionAffinity:\s*true/)
assert.match(accountTestSource, /trafficSource:\s*input\.trafficSource\s*\?\?\s*'manual_account_test'/)

const accountErrorPolicySource = readSource('modules/gateway/account-error-policy.service.ts')
assert.match(accountErrorPolicySource, /accountErrorPolicyUpstreamSummary/)
assert.match(accountErrorPolicySource, /accountErrorPolicyReason\(statusCode,\s*decision,\s*upstreamSummary\)/)

const backgroundJobsSource = readSource('modules/background/background-jobs.ts')
assert.match(backgroundJobsSource, /enqueueCooldownAccountRetest/)
assert.match(backgroundJobsSource, /getCooldownAccountRetestQueueSnapshot/)
assert.doesNotMatch(backgroundJobsSource, /prompt:\s*'hi'/)
assert.doesNotMatch(backgroundJobsSource, /openai-oauth-usage-refresh/)
assert.doesNotMatch(backgroundJobsSource, /refreshOpenAIOAuthUsageSnapshot/)
assert.doesNotMatch(backgroundJobsSource, /accountQualityActiveProbeEnabled/)
assert.doesNotMatch(backgroundJobsSource, /listAccountQualityProbeCandidates/)
assert.doesNotMatch(backgroundJobsSource, /recordAccountQualityProbe/)
assert.doesNotMatch(backgroundJobsSource, /cooldownAccountRetestEnabled/)
assert.doesNotMatch(backgroundJobsSource, /gatewaySettingsOverride/)
assert.doesNotMatch(backgroundJobsSource, /temporaryUnschedulableRetryAttempts:\s*0/)
assert.doesNotMatch(backgroundJobsSource, /temporaryUnschedulableRetryIntervalSeconds:\s*0/)
assert.doesNotMatch(backgroundJobsSource, /cooldownAccountRetestAttemptTimeoutMs/)
assert.doesNotMatch(backgroundJobsSource, /cooldownAccountRetestRunBudgetMs/)
assert.match(backgroundJobsSource, /flushUsageRecordQueue\(\{\s*drain:\s*true/)
assert.match(backgroundJobsSource, /settingsNumber\('cooldownAccountRetestIntervalSeconds', 3, 1, 3600\)/)
assert.match(backgroundJobsSource, /settingsNumber\('cooldownAccountRetestBatchSize', 10, 1, 100\)/)
assert.match(backgroundJobsSource, /settingsNumber\('cooldownAccountRetestMaxBackoffHours', 24, 1, 24 \* 30\)/)

const cooldownAccountRetestSource = readSource('modules/background/cooldown-account-retest.service.ts')
assert.match(cooldownAccountRetestSource, /sequenceRetryPolicy\('cooldown_account_retest_revival', \[\], 0\)/)
assert.match(cooldownAccountRetestSource, /createRetryQueue/)
assert.doesNotMatch(cooldownAccountRetestSource, /background_cooldown_account_retest_retry_scheduled/)
assert.match(cooldownAccountRetestSource, /diagnostics:\s*'full'/)
assert.match(cooldownAccountRetestSource, /trafficSource:\s*'cooldown_retest'/)
assert.match(cooldownAccountRetestSource, /temporaryUnschedulableRetryAttempts:\s*0/)
assert.match(cooldownAccountRetestSource, /testOpenAIAccount/)
assert.match(cooldownAccountRetestSource, /findRecentOpenAIRequestShapeForAccount/)
assert.match(cooldownAccountRetestSource, /account\.boundGroupId/)
assert.doesNotMatch(cooldownAccountRetestSource, /waitForRetryDelay/)

const gatewayAccountSideEffectsSource = readSource('modules/gateway/gateway-account-side-effects.service.ts')
assert.match(gatewayAccountSideEffectsSource, /runSingleGatewayAccountPrecheck/)
assert.match(gatewayAccountSideEffectsSource, /diagnostics:\s*'full'/)
assert.doesNotMatch(gatewayAccountSideEffectsSource, /diagnostics:\s*'limited'/)
assert.match(gatewayAccountSideEffectsSource, /latestState\.reason\s*=\s*accountPrecheckFailureReason\(result\)/)
assert.match(gatewayAccountSideEffectsSource, /function accountPrecheckFailureReason/)
assert.match(gatewayAccountSideEffectsSource, /errorCode\?:\s*string/)

const usageRecordsRepositorySource = readSource('storage/usage-records.repository.ts')
assert.match(usageRecordsRepositorySource, /traffic_source/)
assert.match(usageRecordsRepositorySource, /COALESCE\(traffic_source, 'gateway'\) = 'gateway'/)

const usageStatsRepositorySource = readSource('storage/usage-stats.repository.ts')
assert.match(usageStatsRepositorySource, /COALESCE\(traffic_source, 'gateway'\) <> 'cooldown_retest'/)

const gatewayBodySource = readSource('modules/gateway/openai-gateway-body.ts')
assert.match(gatewayBodySource, /responseBackpressureWarnThresholdMs\s*=\s*50/)
assert.match(gatewayBodySource, /gateway_response_backpressure_slow/)
assert.match(gatewayBodySource, /gateway_response_backpressure_drained/)
assert.match(gatewayBodySource, /logLevel\s*===\s*'warn'\s*\?\s*'gateway_response_backpressure_slow'\s*:\s*'gateway_response_backpressure_drained'/)
assert.doesNotMatch(gatewayBodySource, /gateway_response_backpressure_started/)

const gatewayStreamSource = readSource('modules/gateway/openai-gateway-stream.ts')
assert.match(gatewayStreamSource, /writeResult\.logLevel\s*===\s*'warn'/)
assert.match(gatewayStreamSource, /responseBackpressureWarnThresholdMs/)

const releaseStartScriptSource = readFileSync(resolve(backendSrcDirectory, '../../deploy/start.sh'), 'utf8')
assert.match(releaseStartScriptSource, /JUHE_AI_LOG_CONSOLE_ENABLED="\$\{JUHE_AI_LOG_CONSOLE_ENABLED:-false\}"/)

const oauthRoutesSource = readSource('modules/openai-oauth/openai-oauth.routes.ts')
assert.doesNotMatch(oauthRoutesSource, /refreshOpenAIOAuthUsageSnapshot/)

const gatewayRoutesSource = readSource('modules/gateway/openai-gateway.routes.ts')
assert.match(gatewayRoutesSource, /persistOpenAICodexHeadersIfNeeded\(account,\s*upstreamResponse\.headers,\s*gatewayUsageContext\.trafficSource\)/)
assert.match(gatewayFailureDispatchSource, /usageContext\.trafficSource === 'gateway' \? 'gateway_error' : usageContext\.trafficSource/)

const repositoriesSource = readSource('storage/repositories.ts')
const cooldownRetestRepositorySource = sourceFunctionBlock(repositoriesSource, 'export function listAccountsDueForCooldownRetest')
assert.match(cooldownRetestRepositorySource, /status IN \('temporary_unavailable', 'rate_limited'\)/)
assert.match(cooldownRetestRepositorySource, /rate_limited/)
assert.match(repositoriesSource, /recordCooldownAccountRetestFailure/)
assert.match(repositoriesSource, /cooldownRetestObservationElapsedSeconds/)
assert.doesNotMatch(repositoriesSource, /SET status = 'error'[\s\S]+account_cooldown_retest_exhausted/)

const accountQualityRepositorySource = readSource('storage/account-quality.repository.ts')
assert.doesNotMatch(accountQualityRepositorySource, /recordAccountQualityProbe/)
assert.doesNotMatch(accountQualityRepositorySource, /AccountQualityScoreInput/)
assert.doesNotMatch(accountQualityRepositorySource, /last_probe_at/)

const schemaSource = [
  readSource('storage/schema.ts'),
  readSource('storage/schema/business-schema.ts'),
  readSource('storage/schema/dataset-schema.ts'),
  readSource('storage/schema/stats-schema.ts'),
  readSource('storage/schema/seed-defaults.ts')
].join('\n')
assert.doesNotMatch(schemaSource, /last_probe_at/)
assert.match(schemaSource, /cooldown_retest_observation_started_at/)

console.log('usage-pricing-regression passed')

function readSource(path: string): string {
  return readFileSync(resolve(backendSrcDirectory, path), 'utf8')
}

function sourceFunctionBlock(source: string, marker: string): string {
  const start = source.indexOf(marker)
  assert.notEqual(start, -1, `${marker} should exist`)
  const nextExport = source.indexOf('\nexport function ', start + marker.length)
  return source.slice(start, nextExport === -1 ? undefined : nextExport)
}

async function assertRetryQueueRetainsFailedItems(): Promise<void> {
  const attempts: number[] = []
  const scheduledDelays: number[] = []
  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('retry queue did not finish')), 1000)
    createRetryQueue<{ id: string }>({
      name: 'regression-retry-queue',
      policy: sequenceRetryPolicy('regression_retry_queue', [1, 2]),
      run: (_item, context) => {
        attempts.push(context.attemptIndex)
        return attempts.length >= 3
      },
      onRetryScheduled: (event) => {
        scheduledDelays.push(event.delayMs)
      },
      onSuccess: () => {
        clearTimeout(timeout)
        resolve()
      },
      onExhausted: () => {
        clearTimeout(timeout)
        reject(new Error('retry queue exhausted before success'))
      }
    }).enqueue('account-a', { id: 'account-a' })
  })
  assert.deepEqual(attempts, [0, 1, 2])
  assert.deepEqual(scheduledDelays, [1, 2])
}
