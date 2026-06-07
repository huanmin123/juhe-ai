import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

import { GPT_VENDOR_CODE, OPENAI_COMPATIBLE_PROVIDER_CODE } from '../../domain/provider-protocol.js'
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
const projectRoot = resolve(backendSrcDirectory, '../..')

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
  providerCode: GPT_VENDOR_CODE,
  model: 'gpt-4.1',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300
})
assert.equal(gpt41Cost, 0.00315)
assert.equal(estimateProviderCostUsd({
  providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
  model: 'gpt-4.1',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300
}), gpt41Cost)

const openAIModelPricingList = listProviderModelPricing(GPT_VENDOR_CODE)
const genericOpenAIModelPricingList = listProviderModelPricing(OPENAI_COMPATIBLE_PROVIDER_CODE)
assert.equal(genericOpenAIModelPricingList.length, openAIModelPricingList.length, 'openai 通用供应商应继承 OpenAI-compatible 内置模型目录')
assert.equal(genericOpenAIModelPricingList[0]?.providerCode, OPENAI_COMPATIBLE_PROVIDER_CODE, '通用供应商模型目录应保留 openai providerCode')
assert.equal(openAIModelPricingList[0]?.providerCode, GPT_VENDOR_CODE, 'GPT 子供应商模型目录应保留 gpt providerCode')
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
  assert.ok(getProviderModelPricing(GPT_VENDOR_CODE, id), `${id} should resolve pricing`)
}
for (const id of [
  'gpt-4.5-preview',
  'gpt-5.3-codex-spark',
  'codex-mini-latest',
  'o1-mini'
]) {
  assert.equal(availableOpenAIModels.has(id), false, `${id} should not be exposed`)
  assert.equal(getProviderModelPricing(GPT_VENDOR_CODE, id), undefined, `${id} should not resolve pricing`)
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
  providerCode: GPT_VENDOR_CODE,
  model: 'gpt-5.2-codex',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300
}), 0.0040775)
assert.equal(estimateProviderCostUsd({
  providerCode: GPT_VENDOR_CODE,
  model: 'gpt-5.1-codex-mini',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300
}), 0.0005825)
assert.equal(estimateProviderCostUsd({
  providerCode: GPT_VENDOR_CODE,
  model: 'gpt-3.5-turbo',
  inputTokens: 1000,
  outputTokens: 200
}), 0.0008)

const gpt55ImageInputAsNormalTokensCost = estimateProviderCostUsd({
  providerCode: GPT_VENDOR_CODE,
  model: 'gpt-5.5',
  inputTokens: 1000,
  outputTokens: 200,
  inputImageTokens: 25
})
assert.equal(gpt55ImageInputAsNormalTokensCost, 0.011)

const gpt55Breakdown = buildProviderCostBreakdown({
  providerCode: GPT_VENDOR_CODE,
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

const audioUsage = parseOpenAIUsageFromJsonBuffer(jsonBuffer({
  usage: {
    input_tokens: 100,
    output_tokens: 60,
    input_tokens_details: {
      audio_tokens: 30,
      cached_tokens: 10
    },
    output_tokens_details: {
      audio_tokens: 40
    },
    output_image_count: 2
  }
}))
assert.deepEqual(defined(audioUsage), {
  inputTokens: 100,
  outputTokens: 60,
  cacheReadTokens: 10,
  inputAudioTokens: 30,
  outputAudioTokens: 40,
  outputImageCount: 2
})

const imageCost = estimateProviderCostUsd({
  providerCode: GPT_VENDOR_CODE,
  model: 'gpt-image-2',
  inputTokens: imageUsage.inputTokens,
  outputTokens: imageUsage.outputTokens,
  cacheReadTokens: imageUsage.cacheReadTokens,
  inputImageTokens: imageUsage.inputImageTokens,
  outputImageTokens: imageUsage.outputImageTokens
})
assert.equal(imageCost, 0.0305375)

const imageBreakdown = buildProviderCostBreakdown({
  providerCode: GPT_VENDOR_CODE,
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
  cache_read_cost_usd: 0,
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
assert.match(gatewayUpstreamDispatchSource, /gateway_temporary_unschedulable_same_account_retry/)
assert.doesNotMatch(gatewayUpstreamDispatchSource, /temporaryUnschedulableRetryPolicy/)
assert.match(gatewayUpstreamDispatchSource, /retryAttemptCount\(sameAccountRetryPolicy\)/)
assert.match(gatewayUpstreamDispatchSource, /shouldRetryPolicyAttempt\(attemptIndex, sameAccountRetryPolicy\)/)
assert.match(gatewayUpstreamDispatchSource, /waitForSameAccountRetry/)

const oauthAccessTokenRefreshSource = readSource('modules/openai-oauth/openai-oauth-access-token-refresh.service.ts')
assert.match(oauthAccessTokenRefreshSource, /openAIOAuthRefreshRaceRetryPolicy/)
assert.match(oauthAccessTokenRefreshSource, /shouldRetryPolicyAttempt\(attempt, openAIOAuthRefreshRaceRetryPolicy\)/)
assert.doesNotMatch(oauthAccessTokenRefreshSource, /normalizeOpenAIOAuthStoppedRefreshExceptionMessages/)
assert.doesNotMatch(oauthAccessTokenRefreshSource, /历史后台刷新失败/)

const openAIOAuthRoutesSource = readSource('modules/openai-oauth/openai-oauth.routes.ts')
assert.match(openAIOAuthRoutesSource, /callbackUrl:\s*z\.string\(\)\.min\(1\)/)
assert.doesNotMatch(openAIOAuthRoutesSource, /code:\s*z\.string\(\)\.optional\(\)/)
assert.doesNotMatch(openAIOAuthRoutesSource, /state:\s*z\.string\(\)\.optional\(\)/)

const openAIOAuthServiceSource = readSource('modules/openai-oauth/openai-oauth.service.ts')
assert.match(openAIOAuthServiceSource, /export function extractCodeAndState\(input:\s*\{\s*callbackUrl:\s*string\s*\}/)
assert.doesNotMatch(openAIOAuthServiceSource, /input:\s*\{\s*callbackUrl\?:\s*string;\s*code\?:\s*string;\s*state\?:\s*string\s*\}/)
assert.doesNotMatch(openAIOAuthServiceSource, /directCode|directState/)

const idempotencyDesignSource = readProjectFile('docs/functions/幂等与唯一约束设计.md')
assert.match(idempotencyDesignSource, /OAuth 授权回调创建账户 \| `openai_oauth\.create_from_code` \| `callbackUrl fingerprint`/)
assert.doesNotMatch(idempotencyDesignSource, /state \+ code hash/)

const frontendRouterSource = readProjectFile('frontend/src/router/index.ts')
assert.match(frontendRouterSource, /管理自己的 API Key，绑定自己的分组。/)
assert.doesNotMatch(frontendRouterSource, /绑定自有或授权给自己的分组/)

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

const requestErrorPolicySource = readSource('modules/gateway/request-error-policy.service.ts')
assert.match(requestErrorPolicySource, /requestErrorPolicyUpstreamSummary/)
assert.match(requestErrorPolicySource, /requestErrorPolicyReason\(statusCode,\s*decision,\s*upstreamSummary\)/)

const backgroundJobsSource = readSource('modules/background/background-jobs.ts')
const backgroundSettingsNumberSource = sourceBetween(backgroundJobsSource, 'function settingsNumber', 'async function databaseFileBytes')
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
assert.match(backgroundJobsSource, /settingsNumber\('cooldownAccountRetestIntervalSeconds', 1, 3600\)/)
assert.match(backgroundJobsSource, /settingsNumber\('cooldownAccountRetestBatchSize', 1, 100\)/)
assert.match(backgroundJobsSource, /settingsNumber\('cooldownAccountRetestMaxBackoffHours', 1, 24 \* 30\)/)
assert.doesNotMatch(backgroundSettingsNumberSource, /typeof value === 'string' \? Number\(value\)/)

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
assert.match(usageRecordsRepositorySource, /traffic_source = 'gateway'/)
assert.doesNotMatch(usageRecordsRepositorySource, /COALESCE\(traffic_source, 'gateway'\)/)

const usageRecordListQuerySource = readSource('storage/usage-record-list-query.ts')
assert.match(usageRecordListQuerySource, /\$\{columns\.trafficSource\} = \?/)
assert.doesNotMatch(usageRecordListQuerySource, /COALESCE\(\$\{columns\.trafficSource\}, 'gateway'\)/)

const auditLogsRepositorySource = readSource('storage/audit-logs.repository.ts')
assert.match(auditLogsRepositorySource, /al\.traffic_source = \?/)
assert.doesNotMatch(auditLogsRepositorySource, /COALESCE\(al\.traffic_source, 'gateway'\)/)

const clientIpStatsRepositorySource = readSource('storage/client-ip-stats.repository.ts')
assert.match(clientIpStatsRepositorySource, /traffic_source <> 'cooldown_retest'/)
assert.match(clientIpStatsRepositorySource, /traffic_source = 'cooldown_retest'/)
assert.doesNotMatch(clientIpStatsRepositorySource, /COALESCE\(traffic_source, 'gateway'\)/)

const usageStatsRepositorySource = readSource('storage/usage-stats.repository.ts')
assert.match(usageStatsRepositorySource, /traffic_source <> 'cooldown_retest'/)
assert.doesNotMatch(usageStatsRepositorySource, /COALESCE\(traffic_source, 'gateway'\)/)

const usageStatsAggregationSource = readSource('storage/usage-stats-aggregation.ts')
assert.match(usageStatsAggregationSource, /row\.traffic_source !== 'cooldown_retest'/)
assert.doesNotMatch(usageStatsAggregationSource, /traffic_source \?\? 'gateway'/)

const usageStatsTypesSource = readSource('storage/usage-stats-types.ts')
assert.match(usageStatsTypesSource, /traffic_source: string\r?\n/)
assert.doesNotMatch(usageStatsTypesSource, /traffic_source: string \| null/)

const openAIAccountSelectorSource = readSource('storage/openai-account-selector.repository.ts')
assert.doesNotMatch(openAIAccountSelectorSource, /COALESCE\(source_accounts\.type, accounts\.type\)/)
assert.match(openAIAccountSelectorSource, /accountAccess\.accountAccessType === 'account_authorized' && !row\.resource_account_id/)

const accountsRoutesSource = readSource('modules/accounts/accounts.routes.ts')
assert.match(accountsRoutesSource, /const accountUpdateSchema = z\.object/)
assert.match(accountsRoutesSource, /concurrencyLimit:\s*z\.number\(\)\.int\(\)\.min\(1\)\.optional\(\)/)
assert.match(accountsRoutesSource, /status:\s*z\.enum\(\['active', 'pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable'\]\)\.optional\(\)/)
assert.match(accountsRoutesSource, /accountUpdateSchema\.safeParse\(req\.body\)/)
assert.match(accountsRoutesSource, /\}\)\.strict\(\)/)

const repositoriesSource = readSource('storage/repositories.ts')
assert.doesNotMatch(repositoriesSource, /SET type = \?,\s*credentials_encrypted = \?/s)
assert.match(repositoriesSource, /function logicallyDeleteSourceAccountWithInstances/)
assert.match(repositoriesSource, /WHERE authorization_instance_source_account_id = \?/)
assert.doesNotMatch(repositoriesSource, /authorization_instance_source_account_id = NULL/)
assert.match(repositoriesSource, /function normalizedAccountType\(value: unknown\): string/)
assert.match(repositoriesSource, /function normalizedAccountStatusInput\(value: unknown, fallback: AccountStatus\): AccountStatus/)
assert.match(repositoriesSource, /function normalizedPositiveIntegerInput\(value: unknown, fallback: number, label: string\): number/)
assert.doesNotMatch(repositoriesSource, /String\(input\.type \?\? 'api_key'\)/)
assert.doesNotMatch(repositoriesSource, /Number\(input\.concurrencyLimit \?\? current\.concurrencyLimit\)/)
assert.doesNotMatch(repositoriesSource, /Number\(input\.priority \?\? current\.priority\)/)
assert.doesNotMatch(repositoriesSource, /value === 1 \|\| value === '1'/)
assert.doesNotMatch(repositoriesSource, /typeof value === 'string' \? Number\(value\)/)

const frontendSettingsFormSource = readProjectFile('frontend/src/views/settings/settingsForm.ts')
assert.doesNotMatch(frontendSettingsFormSource, /typeof value === 'string' \? Number\(value\)/)

const proxyRepositorySource = readSource('storage/proxy.repository.ts')
assert.match(proxyRepositorySource, /function normalizedProxyType\(value: unknown\): string/)
assert.match(proxyRepositorySource, /function normalizedProxyPort\(value: unknown\): number/)
assert.doesNotMatch(proxyRepositorySource, /input\.type \?\? 'socks5h'/)
assert.doesNotMatch(proxyRepositorySource, /Number\(input\.port \?\? 0\)/)
assert.doesNotMatch(proxyRepositorySource, /Number\(input\.port \?\? current\.port\)/)

const proxiesRoutesSource = readSource('modules/proxies/proxies.routes.ts')
assert.match(proxiesRoutesSource, /const proxyUpdateSchema = proxySchema\.partial\(\)\.strict\(\)/)
assert.match(proxiesRoutesSource, /proxyUpdateSchema\.safeParse\(req\.body\)/)

const externalPublicApiCatalogSource = readSource('modules/external-integrations/external-public-api-catalog.ts')
assert.match(externalPublicApiCatalogSource, /id: 'api-key-delete'[\s\S]+groupRouteStrategy:\s*'priority_failover'[\s\S]+groupBindings: \[\{[^}]*groupId: 'grp_xxx'/)
assert.doesNotMatch(externalPublicApiCatalogSource, /apiKey:\s*\{[^\n]*groupId:\s*'grp_xxx'/)
assert.doesNotMatch(externalPublicApiCatalogSource, /新的主绑定分组 ID/)
assert.doesNotMatch(externalPublicApiCatalogSource, /绑定分组 ID；与 groupName/)
const externalPublicAccountUpdateCatalogSource = sourceBetween(externalPublicApiCatalogSource, "id: 'account-update'", "id: 'account-delete'")
assert.match(externalPublicAccountUpdateCatalogSource, /name: 'providerCode'[\s\S]+required: true/)
assert.match(externalPublicAccountUpdateCatalogSource, /name: 'accountId'[\s\S]+required: true/)
assert.match(externalPublicAccountUpdateCatalogSource, /name: 'type'[\s\S]+required: true/)
assert.match(externalPublicAccountUpdateCatalogSource, /providerCode:\s*GPT_VENDOR_CODE/)
assert.match(externalPublicAccountUpdateCatalogSource, /accountId:\s*'acc_xxx'/)
assert.match(externalPublicAccountUpdateCatalogSource, /type:\s*'api_key'/)
assert.doesNotMatch(externalPublicApiCatalogSource, /externalId/)

const externalIntegrationsRoutesSource = readSource('modules/external-integrations/external-integrations.routes.ts')
const accountPushSchemaSource = sourceBetween(externalIntegrationsRoutesSource, 'const accountPushSchema', 'const accountDeleteSchema')
assert.match(accountPushSchemaSource, /providerCode:\s*providerCodeSchema/)
assert.match(accountPushSchemaSource, /type:\s*publicAccountTypeSchema/)
assert.match(accountPushSchemaSource, /const accountUpdateSchema = accountPushSchema\.extend\(\{[\s\S]+accountId:\s*z\.string\(\)\.trim\(\)\.min\(1\)\.max\(120\)/)
assert.match(accountPushSchemaSource, /concurrencyLimit:\s*z\.number\(\)\.int\(\)\.min\(1\)/)
assert.match(accountPushSchemaSource, /priority:\s*z\.number\(\)\.int\(\)\.min\(0\)/)
assert.doesNotMatch(accountPushSchemaSource, /z\.coerce\.number/)
assert.doesNotMatch(accountPushSchemaSource, /externalId/)
const apiKeyGroupBindingSchemaSource = sourceBetween(externalIntegrationsRoutesSource, 'const apiKeyGroupBindingSchema', 'const apiKeyAddSchema')
assert.match(apiKeyGroupBindingSchemaSource, /priority:\s*z\.number\(\)\.int\(\)\.positive\(\)\.optional\(\)/)
assert.match(apiKeyGroupBindingSchemaSource, /weight:\s*z\.number\(\)\.int\(\)\.min\(1\)\.max\(100\)\.optional\(\)/)
assert.match(apiKeyGroupBindingSchemaSource, /\}\)\.strict\(\)/)
assert.doesNotMatch(apiKeyGroupBindingSchemaSource, /z\.coerce\.number/)

const externalPublicAccountPushSource = readSource('modules/external-integrations/external-public-account-push.service.ts')
const externalPublicBoundedIntegerSource = sourceFunctionBlock(externalPublicAccountPushSource, 'function boundedInteger')
assert.match(externalPublicBoundedIntegerSource, /typeof value !== 'number'/)
assert.doesNotMatch(externalPublicBoundedIntegerSource, /Number\(value\)/)
assert.doesNotMatch(externalPublicAccountPushSource, /normalizedText\(input\.providerCode\)\s*\|\|\s*'openai'/)

const apiKeyRepositorySource = readSource('storage/api-key.repository.ts')
assert.doesNotMatch(apiKeyRepositorySource, /'scopes_json'/)
assert.doesNotMatch(apiKeyRepositorySource, /JSON\.stringify\(\[\]\)/)

const businessSchemaSource = readSource('storage/schema/business-schema.ts')
const apiKeysSchemaSource = sourceBetween(businessSchemaSource, 'CREATE TABLE IF NOT EXISTS api_keys', 'CREATE TABLE IF NOT EXISTS api_key_group_bindings')
assert.doesNotMatch(apiKeysSchemaSource, /scopes_json/)

const coreFunctionDocSource = readProjectFile('docs/functions/核心功能设计.md')
assert.doesNotMatch(coreFunctionDocSource, /`scopes_json`：保留字段/)

const modelChecksRepositorySource = readSource('storage/model-checks.repository.ts')
assert.match(modelChecksRepositorySource, /providerCode: string/)
assert.doesNotMatch(modelChecksRepositorySource, /providerCode\s*\?\?\s*'openai'/)

const streamInterceptPoliciesRoutesSource = readSource('modules/stream-intercept-policies/stream-intercept-policies.routes.ts')
const streamInterceptPolicyBodySchemaSource = sourceBetween(streamInterceptPoliciesRoutesSource, 'const policyBodySchema', 'streamInterceptPoliciesRouter.get')
assert.match(streamInterceptPolicyBodySchemaSource, /priority:\s*z\.number\(\)\.int\(\)\.min\(1\)\.max\(9999\)\.optional\(\)/)
assert.doesNotMatch(streamInterceptPolicyBodySchemaSource, /avoidanceTtlSeconds/)
assert.doesNotMatch(streamInterceptPolicyBodySchemaSource, /z\.coerce\.number/)

const accountStreamInterceptPolicyValidationSource = readSource('modules/accounts/account-stream-intercept-policy-validation.ts')
assert.match(accountStreamInterceptPolicyValidationSource, /priority:\s*z\.number\(\)\.int\(\)\.min\(1\)\.max\(9999\)/)
assert.doesNotMatch(accountStreamInterceptPolicyValidationSource, /avoidanceTtlSeconds/)
assert.doesNotMatch(accountStreamInterceptPolicyValidationSource, /z\.coerce\.number/)

const externalIntegrationSourcesRoutesSource = readSource('modules/external-integrations/external-integration-sources.routes.ts')
const rateLimitRuleSchemaSource = sourceBetween(externalIntegrationSourcesRoutesSource, 'const rateLimitRuleSchema', 'const sourceBodySchema')
assert.match(rateLimitRuleSchemaSource, /windowSeconds:\s*z\.number\(\)\.int\(\)\.min\(1/)
assert.match(rateLimitRuleSchemaSource, /maxRequests:\s*z\.number\(\)\.int\(\)\.min\(1/)
assert.match(rateLimitRuleSchemaSource, /\}\)\.strict\(\)/)
assert.doesNotMatch(rateLimitRuleSchemaSource, /z\.coerce\.number/)

const ipStatsRoutesSource = readSource('modules/ip-stats/ip-stats.routes.ts')
const ipPolicyBodySchemaSource = sourceBetween(ipStatsRoutesSource, 'const policyBodySchema', 'ipStatsRouter.get')
assert.match(ipPolicyBodySchemaSource, /durationMinutes:\s*z\.number\(\)\.int\(\)\.min\(1/)
assert.match(ipPolicyBodySchemaSource, /durationDays:\s*z\.number\(\)\.int\(\)\.min\(1/)
assert.doesNotMatch(ipPolicyBodySchemaSource, /z\.coerce\.number/)

const tableMonitorRoutesSource = readSource('modules/table-monitor/table-monitor.routes.ts')
const nonBusinessDataCleanupSchemaSource = sourceBetween(tableMonitorRoutesSource, 'const nonBusinessDataCleanupSchema', 'interface NonBusinessDataCleanupResult')
assert.match(nonBusinessDataCleanupSchemaSource, /batchSize:\s*z\.number\(\)\.int\(\)\.min\(100\)\.max\(10000\)\.optional\(\)/)
assert.match(nonBusinessDataCleanupSchemaSource, /maxBatches:\s*z\.number\(\)\.int\(\)\.min\(1\)\.max\(100\)\.optional\(\)/)
assert.match(nonBusinessDataCleanupSchemaSource, /\}\)\.strict\(\)/)
assert.doesNotMatch(nonBusinessDataCleanupSchemaSource, /z\.coerce\.number/)

const gatewayBodySource = readSource('modules/gateway/openai-gateway-body.ts')
assert.match(gatewayBodySource, /responseBackpressureWarnThresholdMs\s*=\s*50/)
assert.match(gatewayBodySource, /gateway_response_backpressure_slow/)
assert.match(gatewayBodySource, /gateway_response_backpressure_drained/)
assert.match(gatewayBodySource, /logLevel\s*===\s*'warn'\s*\?\s*'gateway_response_backpressure_slow'\s*:\s*'gateway_response_backpressure_drained'/)
assert.doesNotMatch(gatewayBodySource, /gateway_response_backpressure_started/)
const nonStreamPipeSource = sourceFunctionBlock(gatewayBodySource, 'export async function pipeNonStreamUpstreamResponse')
assert.match(nonStreamPipeSource, /destroyResponseForUpstreamBodyError\(res\)/, '非流式正文已输出后上游中断必须打断下游连接，让客户端按网络失败重试')
assert.match(nonStreamPipeSource, /NonStreamUpstreamBodyPipeError/, '非流式正文已输出后上游中断必须携带部分捕获结果进入审计和使用记录')
const gatewayForcedCloseSource = sourceFunctionBlock(gatewayBodySource, 'export function isGatewayForcedDownstreamClose')
assert.match(gatewayForcedCloseSource, /gatewayForcedDownstreamCloseReasonKey/, '网关主动打断下游连接必须可被路由 close 监听识别，避免误记为客户端取消')

const gatewayStreamSource = readSource('modules/gateway/openai-gateway-stream.ts')
assert.match(gatewayStreamSource, /writeResult\.logLevel\s*===\s*'warn'/)
assert.match(gatewayStreamSource, /responseBackpressureWarnThresholdMs/)

const gatewayUpstreamSource = readSource('modules/gateway/openai-gateway-upstream.ts')
const upstreamRequestTimeoutSource = sourceFunctionBlock(gatewayUpstreamSource, 'export function upstreamRequestTimeoutMs')
assert.match(upstreamRequestTimeoutSource, /settings\.streamRequestTimeoutSeconds/, '首包等待上限应统一用于上游首个响应等待')
assert.doesNotMatch(upstreamRequestTimeoutSource, /isEffectiveOpenAIStreamRequest|streamCircuitBreakerEnabled/, '非流式请求也必须应用首包等待上限，不能只在流式熔断开启时生效')

const releaseStartScriptSource = readFileSync(resolve(backendSrcDirectory, '../../deploy/start.sh'), 'utf8')
assert.match(releaseStartScriptSource, /JUHE_AI_LOG_CONSOLE_ENABLED="\$\{JUHE_AI_LOG_CONSOLE_ENABLED:-false\}"/)

const oauthRoutesSource = readSource('modules/openai-oauth/openai-oauth.routes.ts')
assert.doesNotMatch(oauthRoutesSource, /refreshOpenAIOAuthUsageSnapshot/)

const gatewayRoutesSource = readSource('modules/gateway/openai-gateway.routes.ts')
assert.match(gatewayRoutesSource, /persistOpenAICodexHeadersIfNeeded\(account,\s*upstreamResponse\.headers,\s*gatewayUsageContext\.trafficSource\)/)
assert.match(gatewayRoutesSource, /!isGatewayForcedDownstreamClose\(res\)/, '网关主动关闭非流式半截响应时不应被 close 监听误判为客户端取消')
assert.match(gatewayFailureDispatchSource, /usageContext\.trafficSource === 'gateway' \? 'gateway_error' : usageContext\.trafficSource/)

const cooldownRetestRepositorySource = sourceFunctionBlock(repositoriesSource, 'export function listAccountsDueForCooldownRetest')
assert.match(cooldownRetestRepositorySource, /status IN \('temporary_unavailable', 'rate_limited'\)/)
assert.match(cooldownRetestRepositorySource, /rate_limited/)
assert.match(repositoriesSource, /recordCooldownAccountRetestFailure/)
assert.match(repositoriesSource, /cooldownRetestObservationElapsedSeconds/)
assert.doesNotMatch(repositoriesSource, /SET status = 'error'[\s\S]+account_cooldown_retest_exhausted/)

const accountQualityRepositorySource = readSource('storage/account-quality.repository.ts')
assert.match(accountQualityRepositorySource, /refreshAccountQualityFromUsage/)
assert.match(accountQualityRepositorySource, /account_quality_minute_stats/)
assert.match(accountQualityRepositorySource, /ewma_first_token_ms/)

const schemaSource = [
  readSource('storage/schema.ts'),
  readSource('storage/schema/business-schema.ts'),
  readSource('storage/schema/dataset-schema.ts'),
  readSource('storage/schema/stats-schema.ts'),
  readSource('storage/schema/seed-defaults.ts')
].join('\n')
assert.match(schemaSource, /CREATE TABLE IF NOT EXISTS account_quality_scores/)
assert.match(schemaSource, /recent_request_count/)
assert.match(schemaSource, /ewma_first_token_ms/)
assert.match(schemaSource, /last_sample_at/)
assert.match(schemaSource, /cooldown_retest_observation_started_at/)

console.log('usage-pricing-regression passed')

function readSource(path: string): string {
  return readFileSync(resolve(backendSrcDirectory, path), 'utf8')
}

function readProjectFile(path: string): string {
  return readFileSync(resolve(projectRoot, path), 'utf8')
}

function sourceFunctionBlock(source: string, marker: string): string {
  const start = source.indexOf(marker)
  assert.notEqual(start, -1, `${marker} should exist`)
  const nextExport = source.indexOf('\nexport function ', start + marker.length)
  return source.slice(start, nextExport === -1 ? undefined : nextExport)
}

function sourceBetween(source: string, startMarker: string, endMarker: string): string {
  const start = source.indexOf(startMarker)
  assert.notEqual(start, -1, `${startMarker} should exist`)
  const end = source.indexOf(endMarker, start + startMarker.length)
  assert.notEqual(end, -1, `${endMarker} should exist after ${startMarker}`)
  return source.slice(start, end)
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
