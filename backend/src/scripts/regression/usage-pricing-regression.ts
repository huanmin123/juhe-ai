import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

import { ANTHROPIC_PROVIDER_CODE, DEEPSEEK_PROVIDER_CODE, GEMINI_PROVIDER_CODE, GLM_PROVIDER_CODE, GPT_VENDOR_CODE, OPENAI_COMPATIBLE_PROVIDER_CODE, XAI_PROVIDER_CODE } from '../../domain/provider-protocol.js'
import { getExternalPublicApiCatalog } from '../../modules/external-integrations/external-public-api-catalog.js'
import {
  parseOpenAIUsageFromJsonBuffer
} from '../../modules/gateway/protocols/openai-v1/usage.js'
import {
  parseAnthropicUsageFromJsonBuffer
} from '../../modules/gateway/protocols/anthropic-v1/usage.js'
import {
  inspectOpenAIStreamText,
  OpenAIStreamInspector
} from '../../modules/gateway/protocols/openai-v1/stream-inspection.js'
import {
  inspectAnthropicStreamText
} from '../../modules/gateway/protocols/anthropic-v1/stream-inspection.js'
import { buildProviderCostBreakdown, estimateProviderCostUsd, getProviderModelPricing, listProviderModelPricing } from '../../modules/model-pricing/model-pricing.service.js'
import { anthropicModelPricingData } from '../../modules/model-pricing/anthropic-model-pricing.data.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { retryDelayMs, retryAttemptCount, sequenceRetryPolicy } from '../../shared/retry-policy.js'
import { externalIntegrationScopeOptions } from '../../storage/external-integration-source-constants.js'
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
      cached_tokens: 300,
      cache_write_tokens: 40
    },
    output_tokens_details: {
      reasoning_tokens: 12
    }
  }
}))
assert.deepEqual(defined(responsesUsage), {
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300,
  cacheWriteTokens: 40,
  thinkingTokens: 12
})

const chatUsage = parseOpenAIUsageFromJsonBuffer(jsonBuffer({
  usage: {
    prompt_tokens: '1200',
    completion_tokens: 150,
    prompt_tokens_details: {
      cached_tokens: 400,
      cache_creation_input_tokens: '50'
    },
    completion_tokens_details: {
      reasoning_tokens: 10
    }
  }
}))
assert.deepEqual(defined(chatUsage), {
  inputTokens: 1200,
  outputTokens: 150,
  cacheReadTokens: 400,
  cacheWriteTokens: 50,
  thinkingTokens: 10
})

const openAICompatibleCacheCreationUsage = parseOpenAIUsageFromJsonBuffer(jsonBuffer({
  usage: {
    input_tokens: 800,
    output_tokens: 40,
    cache_creation: {
      ephemeral_5m_input_tokens: 30,
      ephemeral_1h_input_tokens: 10
    }
  }
}))
assert.deepEqual(defined(openAICompatibleCacheCreationUsage), {
  inputTokens: 800,
  outputTokens: 40,
  cacheWriteTokens: 40,
  cacheWrite1hTokens: 10
})

const openAICompatibleCacheWriteInputUsage = parseOpenAIUsageFromJsonBuffer(jsonBuffer({
  usage: {
    input_tokens: 600,
    output_tokens: 30,
    input_tokens_details: {
      cache_write_input_tokens: 12
    }
  }
}))
assert.deepEqual(defined(openAICompatibleCacheWriteInputUsage), {
  inputTokens: 600,
  outputTokens: 30,
  cacheWriteTokens: 12
})

const openAICompatibleClaudeCacheCreationUsage = parseOpenAIUsageFromJsonBuffer(jsonBuffer({
  usage: {
    input_tokens: 700,
    output_tokens: 20,
    claude_cache_creation_5_m_tokens: '6',
    claude_cache_creation_1_h_tokens: 4
  }
}))
assert.deepEqual(defined(openAICompatibleClaudeCacheCreationUsage), {
  inputTokens: 700,
  outputTokens: 20,
  cacheWriteTokens: 10,
  cacheWrite1hTokens: 4
})

const deepSeekUsage = parseOpenAIUsageFromJsonBuffer(jsonBuffer({
  choices: [
    {
      message: {
        role: 'assistant',
        reasoning_content: 'reasoning',
        content: 'answer'
      },
      finish_reason: 'stop'
    }
  ],
  usage: {
    prompt_tokens: 1000,
    completion_tokens: 80,
    prompt_cache_hit_tokens: 640,
    prompt_cache_miss_tokens: 360
  }
}))
assert.deepEqual(defined(deepSeekUsage), {
  inputTokens: 1000,
  outputTokens: 80,
  cacheReadTokens: 640
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

const anthropicUsage = parseAnthropicUsageFromJsonBuffer(jsonBuffer({
  type: 'message',
  usage: {
    input_tokens: 900,
    output_tokens: 120,
    cache_read_input_tokens: 300,
    cache_creation_input_tokens: 40,
    cache_creation: {
      ephemeral_5m_input_tokens: 30,
      ephemeral_1h_input_tokens: 10
    },
    output_tokens_details: {
      thinking_tokens: 12
    }
  }
}))
assert.deepEqual(defined(anthropicUsage), {
  inputTokens: 900,
  outputTokens: 120,
  cacheReadTokens: 300,
  cacheWriteTokens: 40,
  cacheWrite1hTokens: 10,
  thinkingTokens: 12
})

const responsesStreamInspection = inspectOpenAIStreamText([
  'event: response.output_text.delta',
  'data: {"type":"response.output_text.delta","delta":"hi"}',
  '',
  'event: response.completed',
  'data: {"type":"response.completed","response":{"usage":{"input_tokens":1000,"output_tokens":200,"input_tokens_details":{"cached_tokens":300,"cache_write_tokens":40},"output_tokens_details":{"reasoning_tokens":12}}}}',
  ''
].join('\n'))
assert.equal(responsesStreamInspection.terminalReceived, true)
assert.equal(responsesStreamInspection.outputReceived, true)
assert.equal(responsesStreamInspection.estimatedOutputTokens, 1)
assert.deepEqual(defined(responsesStreamInspection.usage), defined(responsesUsage))

const chatStreamInspection = inspectOpenAIStreamText([
  'data: {"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"}}]}',
  '',
  'data: {"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":1200,"completion_tokens":150,"prompt_tokens_details":{"cached_tokens":400,"cache_creation_input_tokens":"50"},"completion_tokens_details":{"reasoning_tokens":10}}}',
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

const customToolCallAddedOnlyInspection = inspectOpenAIStreamText([
  'event: response.output_item.added',
  'data: {"type":"response.output_item.added","output_index":0,"item":{"id":"call_1","type":"custom_tool_call","status":"in_progress","name":"apply_patch","call_id":"call_1"}}',
  ''
].join('\n'))
assert.equal(customToolCallAddedOnlyInspection.outputReceived, true)
assert.equal(customToolCallAddedOnlyInspection.estimatedOutputTokens, undefined)

const functionCallDoneMetadataOnlyInspection = inspectOpenAIStreamText([
  'event: response.output_item.done',
  'data: {"type":"response.output_item.done","output_index":0,"item":{"id":"call_2","type":"function_call","status":"completed"}}',
  ''
].join('\n'))
assert.equal(functionCallDoneMetadataOnlyInspection.outputReceived, true)
assert.equal(functionCallDoneMetadataOnlyInspection.estimatedOutputTokens, undefined)

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

const malformedTextDeltaInspection = inspectOpenAIStreamText([
  'event: response.output_text.delta',
  'data: {"type":"response.output_text.delta","delta":"bad"',
  ''
].join('\n'))
assert.equal(malformedTextDeltaInspection.outputReceived, false, '畸形 Responses delta JSON 不应被快速路径记为输出')
assert.equal(malformedTextDeltaInspection.estimatedOutputTokens, undefined)

const semanticTextDeltaInspection = inspectOpenAIStreamText([
  'event: response.output_text.delta',
  'data: {"type":"response.output_text.delta","delta":"hi","usage":{"input_tokens":3,"output_tokens":2}}',
  ''
].join('\n'))
assert.equal(semanticTextDeltaInspection.outputReceived, true)
assert.deepEqual(defined(semanticTextDeltaInspection.usage), {
  inputTokens: 3,
  outputTokens: 2
})

const anthropicStreamInspection = inspectAnthropicStreamText([
  'event: message_start',
  'data: {"type":"message_start","message":{"usage":{"input_tokens":900,"output_tokens":0,"cache_read_input_tokens":300,"cache_creation_input_tokens":40,"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":10},"output_tokens_details":{"thinking_tokens":12}}}}',
  '',
  'event: content_block_delta',
  'data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}',
  '',
  'event: message_delta',
  'data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":120}}',
  '',
  'event: message_stop',
  'data: {"type":"message_stop"}',
  ''
].join('\n'))
assert.equal(anthropicStreamInspection.terminalReceived, true)
assert.equal(anthropicStreamInspection.outputReceived, true)
assert.equal(anthropicStreamInspection.estimatedOutputTokens, 1)
assert.deepEqual(defined(anthropicStreamInspection.usage), {
  inputTokens: 900,
  outputTokens: 120,
  cacheReadTokens: 300,
  cacheWriteTokens: 40,
  cacheWrite1hTokens: 10,
  thinkingTokens: 12
})

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

const deepSeekV4ProCost = estimateProviderCostUsd({
  providerCode: DEEPSEEK_PROVIDER_CODE,
  model: 'deepseek-v4-pro',
  inputTokens: 1000,
  outputTokens: 100,
  cacheReadTokens: 400
})
assert.equal(deepSeekV4ProCost, 0.00034945, 'DeepSeek V4 Pro 成本应按 cache hit 与 cache miss 拆分')
const deepSeekModelPricingList = listProviderModelPricing(DEEPSEEK_PROVIDER_CODE)
assert.deepEqual(deepSeekModelPricingList.map((item) => item.model), [
  'deepseek-v4-flash',
  'deepseek-v4-pro',
  ...(new Date().toISOString().slice(0, 10) < '2026-07-24' ? ['deepseek-chat', 'deepseek-reasoner'] : [])
], 'DeepSeek 价格目录应按当前官方优先模型到历史兼容名排序')
const deepSeekPricingById = new Map(deepSeekModelPricingList.map((item) => [item.model, item]))
for (const id of ['deepseek-v4-flash', 'deepseek-v4-pro']) {
  assert(deepSeekPricingById.has(id), `DeepSeek 模型价格目录应包含 ${id}`)
}
assert.deepEqual(deepSeekPricingById.get('deepseek-v4-flash')?.supportedApiProtocols, ['chat_completions', 'messages'])
assert.deepEqual(deepSeekPricingById.get('deepseek-v4-pro')?.supportedApiProtocols, ['chat_completions', 'messages', 'completions'])
assert.equal(deepSeekPricingById.get('deepseek-v4-flash')?.inputUsdPer1M, 0.14)
assert.equal(deepSeekPricingById.get('deepseek-v4-flash')?.cachedInputUsdPer1M, 0.0028)
assert.equal(deepSeekPricingById.get('deepseek-v4-flash')?.outputUsdPer1M, 0.28)
assert.equal(deepSeekPricingById.get('deepseek-v4-flash')?.contextWindowTokens, 1_000_000)
assert.equal(deepSeekPricingById.get('deepseek-v4-flash')?.maxInputTokens, undefined)
assert.equal(deepSeekPricingById.get('deepseek-v4-flash')?.maxOutputTokens, 384_000)
assert.equal(deepSeekPricingById.get('deepseek-v4-pro')?.inputUsdPer1M, 0.435)
assert.equal(deepSeekPricingById.get('deepseek-v4-pro')?.cachedInputUsdPer1M, 0.003625)
assert.equal(deepSeekPricingById.get('deepseek-v4-pro')?.outputUsdPer1M, 0.87)
assert.equal(getProviderModelPricing(DEEPSEEK_PROVIDER_CODE, 'deepseek-v4-pro-2026-06-20')?.model, 'deepseek-v4-pro')
assert.equal(getProviderModelPricing(DEEPSEEK_PROVIDER_CODE, 'deepseek-ai-v4-pro'), undefined)

const glm52Cost = estimateProviderCostUsd({
  providerCode: GLM_PROVIDER_CODE,
  model: 'glm-5.2',
  inputTokens: 1000,
  outputTokens: 100,
  cacheReadTokens: 400
})
assert.equal(glm52Cost, 0.001384, 'GLM-5.2 成本应按 cache hit 与 cache miss 拆分')
const glmModelPricingList = listProviderModelPricing(GLM_PROVIDER_CODE)
assert.deepEqual(glmModelPricingList.map((item) => item.model), [
  'glm-5.2',
  'glm-5.1',
  'glm-5',
  'glm-5-turbo',
  'glm-4.7',
  'glm-4.7-flashx',
  'glm-4.7-flash',
  'glm-4.6',
  'glm-4.5',
  'glm-4.5-x',
  'glm-4.5-air',
  'glm-4.5-airx',
  'glm-4.5-flash',
  'glm-4-32b-0414-128k',
  'glm-4-long',
  'glm-4-flashx-250414',
  'glm-4-flash-250414',
  'glm-5.2-free'
], 'GLM 价格目录应按官方当前模型从新到旧排序，隐藏历史估算项排最后')
const glmPricingById = new Map(glmModelPricingList.map((item) => [item.model, item]))
for (const id of [
  'glm-5.2',
  'glm-5.1',
  'glm-5',
  'glm-5-turbo',
  'glm-4.7',
  'glm-4.7-flashx',
  'glm-4.7-flash',
  'glm-4.6',
  'glm-4.5',
  'glm-4.5-x',
  'glm-4.5-air',
  'glm-4.5-airx',
  'glm-4.5-flash',
  'glm-4-32b-0414-128k',
  'glm-4-long',
  'glm-4-flashx-250414',
  'glm-4-flash-250414',
  'glm-5.2-free'
]) {
  assert(glmPricingById.has(id), `GLM 模型价格目录应包含 ${id}`)
  assert.deepEqual(glmPricingById.get(id)?.supportedApiProtocols, ['chat_completions'])
}
assert.equal(glmPricingById.get('glm-5.2-free')?.catalogVisible, false, '非官方 glm-5.2-free 不应进入可见模型目录')
assert.equal(glmPricingById.get('glm-5.2')?.inputUsdPer1M, 1.4)
assert.equal(glmPricingById.get('glm-5.2')?.cachedInputUsdPer1M, 0.26)
assert.equal(glmPricingById.get('glm-5.2')?.outputUsdPer1M, 4.4)
assert.equal(glmPricingById.get('glm-5.1')?.inputUsdPer1M, 1.4)
assert.equal(glmPricingById.get('glm-5.1')?.cachedInputUsdPer1M, 0.26)
assert.equal(glmPricingById.get('glm-5.1')?.outputUsdPer1M, 4.4)
assert.equal(glmPricingById.get('glm-5')?.inputUsdPer1M, 1.0)
assert.equal(glmPricingById.get('glm-5')?.cachedInputUsdPer1M, 0.2)
assert.equal(glmPricingById.get('glm-5')?.outputUsdPer1M, 3.2)
assert.equal(glmPricingById.get('glm-5-turbo')?.inputUsdPer1M, 1.2)
assert.equal(glmPricingById.get('glm-5-turbo')?.cachedInputUsdPer1M, 0.24)
assert.equal(glmPricingById.get('glm-5-turbo')?.outputUsdPer1M, 4)
assert.equal(glmPricingById.get('glm-4.7')?.inputUsdPer1M, 0.6)
assert.equal(glmPricingById.get('glm-4.7')?.cachedInputUsdPer1M, 0.11)
assert.equal(glmPricingById.get('glm-4.7')?.outputUsdPer1M, 2.2)
assert.equal(glmPricingById.get('glm-4.7-flashx')?.inputUsdPer1M, 0.07)
assert.equal(glmPricingById.get('glm-4.7-flashx')?.cachedInputUsdPer1M, 0.01)
assert.equal(glmPricingById.get('glm-4.7-flashx')?.outputUsdPer1M, 0.4)
assert.equal(glmPricingById.get('glm-4.7-flash')?.inputUsdPer1M, 0)
assert.equal(glmPricingById.get('glm-4.7-flash')?.outputUsdPer1M, 0)
assert.equal(glmPricingById.get('glm-4.5-x')?.inputUsdPer1M, 2.2)
assert.equal(glmPricingById.get('glm-4.5-x')?.cachedInputUsdPer1M, 0.45)
assert.equal(glmPricingById.get('glm-4.5-x')?.outputUsdPer1M, 8.9)
assert.equal(glmPricingById.get('glm-4.5-air')?.inputUsdPer1M, 0.2)
assert.equal(glmPricingById.get('glm-4.5-air')?.cachedInputUsdPer1M, 0.03)
assert.equal(glmPricingById.get('glm-4.5-air')?.outputUsdPer1M, 1.1)
assert.equal(glmPricingById.get('glm-4.5-airx')?.inputUsdPer1M, 1.1)
assert.equal(glmPricingById.get('glm-4.5-airx')?.cachedInputUsdPer1M, 0.22)
assert.equal(glmPricingById.get('glm-4.5-airx')?.outputUsdPer1M, 4.5)
assert.equal(glmPricingById.get('glm-4.5-flash')?.inputUsdPer1M, 0)
assert.equal(glmPricingById.get('glm-4.5-flash')?.outputUsdPer1M, 0)
assert.equal(glmPricingById.get('glm-4-32b-0414-128k')?.inputUsdPer1M, 0.1)
assert.equal(glmPricingById.get('glm-4-32b-0414-128k')?.outputUsdPer1M, 0.1)
assert.equal(glmPricingById.get('glm-4-long')?.inputUsdPer1M, 0.14)
assert.equal(glmPricingById.get('glm-4-long')?.outputUsdPer1M, 0.14)
assert.equal(getProviderModelPricing(GLM_PROVIDER_CODE, 'glm-5.2-20260620')?.model, 'glm-5.2')
assert.equal(getProviderModelPricing(GLM_PROVIDER_CODE, 'glm-5-turbo-20260620')?.model, 'glm-5-turbo')
assert.equal(getProviderModelPricing(GLM_PROVIDER_CODE, 'glm-4.7-flashx-20260620')?.model, 'glm-4.7-flashx')
assert.equal(getProviderModelPricing(GLM_PROVIDER_CODE, 'glm-4-flashx-250414-20260620')?.model, 'glm-4-flashx-250414')

const gemini35FlashCost = estimateProviderCostUsd({
  providerCode: GEMINI_PROVIDER_CODE,
  model: 'gemini-3.5-flash',
  inputTokens: 1000,
  outputTokens: 100,
  cacheReadTokens: 400
})
assert.equal(gemini35FlashCost, 0.00186, 'Gemini 3.5 Flash 成本应按官方 Standard 价格和 cache read 拆分')
assert.equal(estimateProviderCostUsd({
  providerCode: GEMINI_PROVIDER_CODE,
  model: 'gemini-3.1-pro-preview',
  inputTokens: 200_000,
  outputTokens: 0
}), 0.4, 'Gemini 长上下文价格必须在输入超过 200k 后才启用')
assert.equal(estimateProviderCostUsd({
  providerCode: GEMINI_PROVIDER_CODE,
  model: 'gemini-3.1-pro-preview',
  inputTokens: 200_001,
  outputTokens: 0
}), 0.800004, 'Gemini 输入超过 200k 后应按长上下文价格计费')
for (const serviceTier of ['priority', 'flex'] as const) {
  const gemini31ProTierCost = estimateProviderCostUsd({
    providerCode: GEMINI_PROVIDER_CODE,
    model: 'gemini-3.1-pro-preview',
    serviceTier,
    inputTokens: 1_000,
    outputTokens: 100
  })
  assert.equal(gemini31ProTierCost, serviceTier === 'priority' ? 0.00576 : 0.0016, `Gemini ${serviceTier} 必须使用官方专用 token 价格`)
  const gemini25FlashTierCost = estimateProviderCostUsd({
    providerCode: GEMINI_PROVIDER_CODE,
    model: 'gemini-2.5-flash',
    serviceTier,
    inputTokens: 1_000,
    outputTokens: 100
  })
  assert.equal(gemini25FlashTierCost, serviceTier === 'priority' ? 0.00099 : 0.000275, `Gemini 2.5 Flash ${serviceTier} 必须使用官方 token 价格，而不是把音频价混入 token 价`)
}
assert.equal(estimateProviderCostUsd({
  providerCode: XAI_PROVIDER_CODE,
  model: 'grok-4.3',
  inputTokens: 200_000,
  outputTokens: 0
}), 0.5, 'xAI 输入达到 200k 阈值时必须对全量输入启用长上下文价格')
const geminiModelPricingList = listProviderModelPricing(GEMINI_PROVIDER_CODE)
assert.deepEqual(geminiModelPricingList.map((item) => item.model), [
  'gemini-3.5-flash',
  'gemini-3.1-pro-preview',
  'gemini-3.1-pro-preview-customtools',
  'gemini-3-flash-preview',
  'gemini-3.1-flash-lite',
  'gemini-2.5-pro',
  'gemini-2.5-flash',
  'gemini-2.5-flash-lite',
  'gemini-embedding-2'
], 'Gemini 价格目录应只包含当前收录的 Google 官方模型')
const geminiPricingById = new Map(geminiModelPricingList.map((item) => [item.model, item]))
for (const id of [
  'gemini-3.5-flash',
  'gemini-3.1-pro-preview',
  'gemini-3.1-pro-preview-customtools',
  'gemini-3-flash-preview',
  'gemini-3.1-flash-lite',
  'gemini-2.5-pro',
  'gemini-2.5-flash',
  'gemini-2.5-flash-lite'
]) {
  assert(geminiPricingById.has(id), `Gemini 模型价格目录应包含 ${id}`)
  assert.deepEqual(
    geminiPricingById.get(id)?.supportedApiProtocols,
    [
      'chat_completions',
      'generate_content',
      'stream_generate_content',
      'count_tokens',
      ...(id === 'gemini-3.1-pro-preview-customtools' ? [] : ['interactions'] as const)
    ]
  )
  assert.equal(geminiPricingById.get(id)?.maxInputTokens, 1_048_576)
  assert.equal(geminiPricingById.get(id)?.maxOutputTokens, 65_536)
  assert.deepEqual(geminiPricingById.get(id)?.supportedServiceTiers, ['priority', 'flex'])
  assert.deepEqual(geminiPricingById.get(id)?.outputModalities, ['text'])
}
assert.equal(geminiPricingById.get('gemini-3.5-flash')?.defaultReasoningEffort, 'medium')
assert.equal(geminiPricingById.get('gemini-3.1-pro-preview')?.defaultReasoningEffort, 'high')
assert.equal(geminiPricingById.get('gemini-3-flash-preview')?.defaultReasoningEffort, 'high')
for (const id of ['gemini-3.1-flash-lite', 'gemini-2.5-pro', 'gemini-2.5-flash', 'gemini-2.5-flash-lite']) {
  assert.equal(geminiPricingById.get(id)?.defaultReasoningEffort, undefined, `${id} 未公开离散默认级别时必须交给上游决定`)
}
assert.deepEqual(geminiPricingById.get('gemini-3.1-pro-preview')?.serviceTierPrices?.flex, {
  inputUsdPer1M: 1,
  outputUsdPer1M: 6,
  cachedInputUsdPer1M: 0.2
})
assert.equal(geminiPricingById.get('gemini-2.5-flash')?.serviceTierPrices?.priority?.audioInputUsdPer1M, 1.8)
assert.deepEqual(geminiPricingById.get('gemini-embedding-2')?.supportedApiProtocols, ['embed_content'])
assert.equal(geminiPricingById.get('gemini-3.5-flash')?.inputUsdPer1M, 1.5)
assert.equal(geminiPricingById.get('gemini-3.5-flash')?.cachedInputUsdPer1M, 0.15)
assert.equal(geminiPricingById.get('gemini-3.5-flash')?.outputUsdPer1M, 9)
assert.equal(geminiPricingById.get('gemini-3.1-flash-lite')?.inputUsdPer1M, 0.25)
assert.equal(geminiPricingById.get('gemini-2.5-flash-lite')?.outputUsdPer1M, 0.4)
assert.equal(geminiPricingById.get('gemini-embedding-2')?.inputUsdPer1M, 0.2)
assert.deepEqual(geminiPricingById.get('gemini-embedding-2')?.inputModalities, ['text', 'image', 'video', 'audio', 'file'])
assert.equal(getProviderModelPricing(GEMINI_PROVIDER_CODE, 'models/gemini-3.5-flash')?.model, 'gemini-3.5-flash')
assert.equal(getProviderModelPricing(GEMINI_PROVIDER_CODE, 'gemini-3.5-flash-antigravity'), undefined, '中转自定义 Gemini 型号不应回落到官方模型价格')
assert.equal(geminiPricingById.has('gemini-3.5-flash-antigravity'), false, '中转自定义 Gemini 型号不应进入官方 Gemini 价格目录')

const openAIModelPricingList = listProviderModelPricing(GPT_VENDOR_CODE)
const genericOpenAIModelPricingList = listProviderModelPricing(OPENAI_COMPATIBLE_PROVIDER_CODE)
assert.equal(genericOpenAIModelPricingList.length, openAIModelPricingList.length, 'openai 通用供应商应继承 OpenAI-compatible 内置模型目录')
assert.equal(genericOpenAIModelPricingList[0]?.providerCode, OPENAI_COMPATIBLE_PROVIDER_CODE, '通用供应商模型目录应保留 openai providerCode')
assert.equal(openAIModelPricingList[0]?.providerCode, GPT_VENDOR_CODE, 'GPT 子供应商模型目录应保留 gpt providerCode')
assert.deepEqual(openAIModelPricingList.slice(0, 8).map((item) => item.model), [
  'gpt-5.6-sol',
  'gpt-5.6-terra',
  'gpt-5.6-luna',
  'gpt-5.5',
  'gpt-5.5-2026-04-23',
  'gpt-5.5-pro',
  'gpt-5.5-pro-2026-04-23',
  'gpt-image-2'
], 'GPT/OpenAI 价格目录首屏应按官方当前模型从新到旧排序')
const anthropicModelPricingList = listProviderModelPricing(ANTHROPIC_PROVIDER_CODE)
assert(anthropicModelPricingList.length > 0, 'Anthropic 供应商应暴露 Anthropic 内置模型价格目录')
assert.equal(anthropicModelPricingList[0]?.providerCode, ANTHROPIC_PROVIDER_CODE, 'Anthropic 模型目录应保留 anthropic providerCode')
assert.deepEqual(anthropicModelPricingList.slice(0, 25).map((item) => item.model), [
  'claude-fable-5',
  'claude-mythos-5',
  'claude-sonnet-5',
  ...(new Date().toISOString().slice(0, 10) < '2026-06-30' ? ['claude-mythos-preview'] : []),
  'claude-opus-4-8',
  'claude-opus-4-7',
  'claude-opus-4-6',
  'claude-opus-4-6-thinking',
  'claude-opus-4-5',
  'claude-opus-4-5-20251101',
  ...(new Date().toISOString().slice(0, 10) < '2026-08-05' ? ['claude-opus-4-1', 'claude-opus-4-1-20250805'] : []),
  'claude-sonnet-4-6',
  'claude-sonnet-4-6-thinking',
  'claude-sonnet-4-5',
  'claude-sonnet-4-5-20250929',
  'claude-haiku-4-5',
  'claude-haiku-4-5-20251001',
  'best',
  'fable',
  'opus',
  'opus[1m]',
  'opusplan',
  'sonnet',
  'sonnet[1m]',
  'haiku'
].slice(0, 25), 'Anthropic 价格目录应按官方当前模型从新到旧排序，隐藏兼容模型排在可见模型后')
const anthropicPricingById = new Map(anthropicModelPricingList.map((item) => [item.model, item]))
for (const id of [
  'best',
  'fable',
  'opus',
  'opus[1m]',
  'opusplan',
  'sonnet',
  'sonnet[1m]',
  'haiku'
]) {
  assert(anthropicPricingById.has(id), `Anthropic 模型目录应包含 Claude Code 模型别名 ${id}`)
  assert.equal(anthropicPricingById.get(id)?.catalogVisible, false, `Claude Code 兼容别名 ${id} 不应进入官方模型发现目录`)
  assert.equal(getProviderModelPricing(ANTHROPIC_PROVIDER_CODE, id)?.model, id, `Claude Code 模型别名 ${id} 应直接命中价格目录`)
}
for (const id of [
  'claude-fable-5',
  'claude-mythos-5',
  'claude-sonnet-5',
  'claude-opus-4-8',
  'claude-opus-4-7',
  'claude-opus-4-6',
  'claude-opus-4-6-thinking',
  'claude-sonnet-4-6',
  'claude-sonnet-4-6-thinking',
  'claude-opus-4-5',
  'claude-sonnet-4-5',
  'claude-haiku-4-5',
  'claude-sonnet-4-5-20250929',
  'claude-haiku-4-5-20251001',
  'claude-opus-4-5-20251101'
]) {
  assert(anthropicPricingById.has(id), `Anthropic 模型价格目录应包含 Claude 可见模型 ${id}`)
  assert.equal(anthropicPricingById.get(id)?.catalogVisible, true, `${id} 应进入模型发现目录`)
  assert.equal(getProviderModelPricing(ANTHROPIC_PROVIDER_CODE, id)?.model, id, `${id} 应直接命中价格目录`)
}
assert.equal(anthropicPricingById.has('claude-mythos-preview'), false, 'Mythos preview 已退休，不应进入 Anthropic 目录')
assert.equal(getProviderModelPricing(ANTHROPIC_PROVIDER_CODE, 'claude-mythos-preview'), undefined, 'Mythos preview 已退休，不应命中计价')
for (const id of [
  'antigravity-claude-opus-4-6-thinking',
  'antigravity/claude-opus-4-6-thinking',
  'google/antigravity-claude-opus-4-6-thinking',
  'google-antigravity/claude-opus-4-6-thinking',
  'google-antigravity:claude-opus-4-6-thinking',
  'claude-opus-4-6-antigravity',
  'claude-sonnet-4-6-antigravity',
  'antigravity-claude-sonnet-4-6',
  'antigravity/claude-sonnet-4-6',
  'google/antigravity-claude-sonnet-4-6',
  'google-antigravity/claude-sonnet-4-6',
  'google-antigravity:claude-sonnet-4-6',
  'antigravity-claude-sonnet-4-6-thinking',
  'antigravity/claude-sonnet-4-6-thinking',
  'google/antigravity-claude-sonnet-4-6-thinking',
  'google-antigravity/claude-sonnet-4-6-thinking',
  'google-antigravity:claude-sonnet-4-6-thinking',
  'claude-fake-5'
]) {
  assert(anthropicPricingById.has(id), `Anthropic 模型价格目录应包含兼容代理隐藏计价模型 ${id}`)
  assert.equal(anthropicPricingById.get(id)?.catalogVisible, false, `${id} 不应进入模型发现目录`)
  assert.equal(getProviderModelPricing(ANTHROPIC_PROVIDER_CODE, id)?.model, id, `${id} 应直接命中价格目录`)
}
assert.deepEqual(anthropicPricingById.get('claude-haiku-4-5')?.supportedApiProtocols, ['messages', 'message_token_counting'])
assert.equal(anthropicPricingById.get('claude-haiku-4-5')?.inputUsdPer1M, 1)
assert.equal(anthropicPricingById.get('claude-haiku-4-5')?.outputUsdPer1M, 5)
assert.equal(anthropicPricingById.get('claude-haiku-4-5')?.cachedInputUsdPer1M, 0.1)
assert.equal(anthropicPricingById.get('claude-haiku-4-5')?.contextWindowTokens, undefined)
assert.equal(anthropicPricingById.get('claude-haiku-4-5')?.maxInputTokens, undefined)
assert.equal(anthropicPricingById.get('claude-haiku-4-5')?.maxOutputTokens, undefined)
assert.equal(anthropicPricingById.get('best')?.inputUsdPer1M, 10)
assert.equal(anthropicPricingById.get('best')?.outputUsdPer1M, 50)
assert.equal(anthropicPricingById.get('fable')?.inputUsdPer1M, 10)
assert.equal(anthropicPricingById.get('fable')?.outputUsdPer1M, 50)
assert.equal(anthropicPricingById.get('opus')?.inputUsdPer1M, 5)
assert.equal(anthropicPricingById.get('opus')?.outputUsdPer1M, 25)
assert.equal(anthropicPricingById.get('opus[1m]')?.contextWindowTokens, undefined)
assert.equal(anthropicPricingById.get('opus[1m]')?.maxInputTokens, 1_000_000)
assert.equal(anthropicPricingById.get('sonnet')?.inputUsdPer1M, 3)
assert.equal(anthropicPricingById.get('sonnet')?.outputUsdPer1M, 15)
assert.equal(anthropicPricingById.get('sonnet[1m]')?.contextWindowTokens, undefined)
assert.equal(anthropicPricingById.get('sonnet[1m]')?.maxInputTokens, 1_000_000)
assert.equal(anthropicPricingById.get('haiku')?.inputUsdPer1M, 1)
assert.equal(anthropicPricingById.get('haiku')?.outputUsdPer1M, 5)
assert.equal(anthropicPricingById.get('claude-fable-5')?.inputUsdPer1M, 10)
assert.equal(anthropicPricingById.get('claude-fable-5')?.outputUsdPer1M, 50)
assert.equal(anthropicPricingById.get('claude-fable-5')?.contextWindowTokens, undefined)
assert.equal(anthropicPricingById.get('claude-fable-5')?.maxInputTokens, 1_000_000)
assert.equal(anthropicPricingById.get('claude-fable-5')?.maxOutputTokens, 128_000)
assert.deepEqual(anthropicPricingById.get('claude-fable-5')?.supportedReasoningEfforts, ['low', 'medium', 'high', 'xhigh', 'max'])
assert.equal(anthropicPricingById.get('claude-fable-5')?.defaultReasoningEffort, 'high')
assert.deepEqual(anthropicPricingById.get('claude-fable-5')?.supportedServiceTiers, [])
assert.deepEqual(anthropicPricingById.get('claude-fable-5')?.inputModalities, ['text', 'image'])
assert.deepEqual(anthropicPricingById.get('claude-fable-5')?.outputModalities, ['text'])
assert.deepEqual(anthropicPricingById.get('claude-fable-5')?.supportedTools, ['function_calling', 'code_execution'])
assert.equal(anthropicPricingById.get('claude-sonnet-5')?.inputUsdPer1M, 2)
assert.equal(anthropicPricingById.get('claude-sonnet-5')?.outputUsdPer1M, 10)
assert.equal(anthropicPricingById.get('claude-sonnet-5')?.contextWindowTokens, 1_000_000)
assert.equal(anthropicPricingById.get('claude-sonnet-5')?.maxInputTokens, undefined)
assert.equal(anthropicPricingById.get('claude-sonnet-5')?.maxOutputTokens, 128_000)
assert.deepEqual(anthropicPricingById.get('claude-sonnet-5')?.supportedReasoningEfforts, ['low', 'medium', 'high', 'xhigh', 'max'])
assert.equal(anthropicPricingById.get('claude-sonnet-5')?.defaultReasoningEffort, 'high')
assert.deepEqual(anthropicPricingById.get('claude-opus-4-5')?.supportedReasoningEfforts, ['low', 'medium', 'high'])
assert.equal(anthropicPricingById.get('claude-mythos-5')?.inputUsdPer1M, 10)
assert.equal(anthropicPricingById.get('claude-mythos-5')?.outputUsdPer1M, 50)
assert.equal(anthropicPricingById.get('claude-mythos-5')?.catalogVisible, true)
assert.equal(anthropicPricingById.get('claude-opus-4-8')?.inputUsdPer1M, 5)
assert.equal(anthropicPricingById.get('claude-opus-4-8')?.outputUsdPer1M, 25)
assert.equal(anthropicPricingById.get('claude-opus-4-7')?.inputUsdPer1M, 5)
assert.equal(anthropicPricingById.get('claude-opus-4-7')?.outputUsdPer1M, 25)
assert.equal(anthropicPricingById.get('claude-opus-4-6')?.inputUsdPer1M, 5)
assert.equal(anthropicPricingById.get('claude-opus-4-6')?.outputUsdPer1M, 25)
assert.equal(anthropicPricingById.get('claude-opus-4-6-antigravity')?.inputUsdPer1M, 5)
assert.equal(anthropicPricingById.get('claude-opus-4-6-antigravity')?.outputUsdPer1M, 25)
assert.equal(anthropicPricingById.get('google/antigravity-claude-opus-4-6-thinking')?.inputUsdPer1M, 5)
assert.equal(anthropicPricingById.get('google-antigravity/claude-opus-4-6-thinking')?.contextWindowTokens, undefined)
assert.equal(anthropicPricingById.get('google-antigravity/claude-opus-4-6-thinking')?.maxInputTokens, 1_000_000)
assert.equal(anthropicPricingById.get('google/antigravity-claude-sonnet-4-6')?.inputUsdPer1M, 3)
assert.equal(anthropicPricingById.get('google-antigravity/claude-sonnet-4-6-thinking')?.contextWindowTokens, undefined)
assert.equal(anthropicPricingById.get('google-antigravity/claude-sonnet-4-6-thinking')?.maxInputTokens, 1_000_000)
assert.deepEqual(anthropicPricingById.get('claude-haiku-4-5-20251001')?.supportedApiProtocols, ['messages', 'message_token_counting'])
assert.equal(anthropicPricingById.get('claude-haiku-4-5-20251001')?.inputUsdPer1M, 1)
assert.equal(anthropicPricingById.get('claude-haiku-4-5-20251001')?.outputUsdPer1M, 5)
assert.equal(anthropicPricingById.get('claude-haiku-4-5-20251001')?.releaseDate, '2025-10-01')
assert.equal(anthropicPricingById.get('claude-sonnet-4-5-20250929')?.inputUsdPer1M, 3)
assert.equal(anthropicPricingById.get('claude-sonnet-4-5-20250929')?.outputUsdPer1M, 15)
assert.equal(anthropicPricingById.get('claude-sonnet-4-5-20250929')?.releaseDate, '2025-09-29')
assert.equal(anthropicPricingById.get('claude-opus-4-5-20251101')?.inputUsdPer1M, 5)
assert.equal(anthropicPricingById.get('claude-opus-4-5-20251101')?.outputUsdPer1M, 25)
assert.equal(anthropicPricingById.get('claude-opus-4-5-20251101')?.releaseDate, '2025-11-01')
assert.deepEqual(anthropicPricingById.get('claude-fake-5')?.supportedApiProtocols, ['messages', 'message_token_counting'])
assert.equal(anthropicPricingById.get('claude-fake-5')?.inputUsdPer1M, 1)
assert.equal(anthropicPricingById.get('claude-fake-5')?.outputUsdPer1M, 5)
assert.equal(getProviderModelPricing(ANTHROPIC_PROVIDER_CODE, 'claude-haiku-4-5-20251001')?.model, 'claude-haiku-4-5-20251001', 'Anthropic 官方 dated ID 应直接进入模型目录')
assert.equal(getProviderModelPricing(ANTHROPIC_PROVIDER_CODE, 'claude-opus-4-5-20251101')?.model, 'claude-opus-4-5-20251101', 'Anthropic Opus dated ID 应直接进入模型目录')
assert.equal(getProviderModelPricing(ANTHROPIC_PROVIDER_CODE, 'claude-fake-5')?.model, 'claude-fake-5', 'Anthropic-compatible 联调模型应保留隐藏计价能力')
assert.equal(getProviderModelPricing(ANTHROPIC_PROVIDER_CODE, 'claude-haiku-4-5-2026-01-01')?.model, 'claude-haiku-4-5', 'Anthropic dated alias 应回落到基础模型价格')
assert.equal(getProviderModelPricing(ANTHROPIC_PROVIDER_CODE, 'antigravity-claude-opus-4-6-thinking-low')?.model, 'antigravity-claude-opus-4-6-thinking', 'Antigravity effort 后缀应回落到基础 Opus thinking 模型')
assert.equal(getProviderModelPricing(ANTHROPIC_PROVIDER_CODE, 'google/antigravity-claude-opus-4-6-thinking-high')?.model, 'google/antigravity-claude-opus-4-6-thinking', 'google/antigravity effort 后缀应回落到基础 Opus thinking 模型')
assert.equal(getProviderModelPricing(ANTHROPIC_PROVIDER_CODE, 'google-antigravity/claude-sonnet-4-6-thinking-max')?.model, 'google-antigravity/claude-sonnet-4-6-thinking', 'google-antigravity effort 后缀应回落到基础 Sonnet thinking 模型')
assert.equal(getProviderModelPricing(ANTHROPIC_PROVIDER_CODE, 'google-antigravity:claude-sonnet-4-6-thinking-medium')?.model, 'google-antigravity:claude-sonnet-4-6-thinking', 'google-antigravity 冒号写法 effort 后缀应回落到基础 Sonnet thinking 模型')
if (new Date().toISOString().slice(0, 10) < '2026-08-05') {
  assert.equal(anthropicPricingById.get('claude-opus-4-1')?.inputUsdPer1M, 15)
  assert.equal(anthropicPricingById.get('claude-opus-4-1')?.outputUsdPer1M, 75)
  assert.equal(anthropicPricingById.get('claude-opus-4-1')?.shutdownDate, '2026-08-05')
  assert.equal(anthropicPricingById.get('claude-opus-4-1-20250805')?.releaseDate, '2025-08-05')
  assert.equal(anthropicPricingById.get('claude-opus-4-1-20250805')?.shutdownDate, '2026-08-05')
  assert.equal(getProviderModelPricing(ANTHROPIC_PROVIDER_CODE, 'claude-opus-4-1')?.model, 'claude-opus-4-1', 'Anthropic Opus 4.1 shutdown date 未到期前应保留')
  assert.equal(getProviderModelPricing(ANTHROPIC_PROVIDER_CODE, 'claude-opus-4-1-20250805')?.model, 'claude-opus-4-1-20250805', 'Anthropic Opus 4.1 dated ID shutdown date 未到期前应保留')
}
for (const retiredAnthropicModel of [
  'claude-opus-4-20250514',
  'claude-sonnet-4-20250514',
  'claude-3-7-sonnet-20250219',
  'claude-3-5-haiku-20241022',
  'google/antigravity-claude-opus-4-5-thinking'
]) {
  assert.equal(anthropicPricingById.has(retiredAnthropicModel), false, `${retiredAnthropicModel} 已退休，不应进入 Anthropic 目录`)
  assert.equal(getProviderModelPricing(ANTHROPIC_PROVIDER_CODE, retiredAnthropicModel), undefined, `${retiredAnthropicModel} 已退休，不应命中计价`)
}
assert.equal(estimateProviderCostUsd({
  providerCode: ANTHROPIC_PROVIDER_CODE,
  model: 'best',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300
}), 0.0203)
assert.equal(estimateProviderCostUsd({
  providerCode: ANTHROPIC_PROVIDER_CODE,
  model: 'opus',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300
}), 0.01015)
assert.equal(estimateProviderCostUsd({
  providerCode: ANTHROPIC_PROVIDER_CODE,
  model: 'sonnet',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300
}), 0.00609)
assert.equal(estimateProviderCostUsd({
  providerCode: ANTHROPIC_PROVIDER_CODE,
  model: 'claude-opus-4-6-antigravity',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300
}), 0.01015)
assert.equal(estimateProviderCostUsd({
  providerCode: ANTHROPIC_PROVIDER_CODE,
  model: 'claude-haiku-4-5',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300
}), 0.00203)
assert.equal(estimateProviderCostUsd({
  providerCode: ANTHROPIC_PROVIDER_CODE,
  model: 'claude-fake-5',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300
}), 0.00203)
assert.equal(estimateProviderCostUsd({
  providerCode: ANTHROPIC_PROVIDER_CODE,
  model: 'claude-haiku-4-5',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300,
  cacheWriteTokens: 40,
  cacheWrite1hTokens: 10
}), 0.0020875)
assert.equal(buildProviderCostBreakdown({
  providerCode: ANTHROPIC_PROVIDER_CODE,
  model: 'claude-haiku-4-5',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300,
  cacheWriteTokens: 40,
  cacheWrite1hTokens: 10,
  thinkingTokens: 12
})?.cacheReadUsdPer1M, 0.1)
assert.equal(buildProviderCostBreakdown({
  providerCode: ANTHROPIC_PROVIDER_CODE,
  model: 'claude-haiku-4-5',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300,
  cacheWriteTokens: 40,
  cacheWrite1hTokens: 10,
  thinkingTokens: 12
})?.cacheWriteUsdPer1M, 1.25)
assert.equal(buildProviderCostBreakdown({
  providerCode: ANTHROPIC_PROVIDER_CODE,
  model: 'claude-haiku-4-5',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300,
  cacheWriteTokens: 40,
  cacheWrite1hTokens: 10,
  thinkingTokens: 12
})?.cacheWrite1hUsdPer1M, 2)
const tieredCacheWriteModel = anthropicModelPricingData.find((item) => item.model === 'claude-haiku-4-5') as unknown as Record<string, unknown>
const originalTieredCacheWriteFields = {
  supported_service_tiers: tieredCacheWriteModel.supported_service_tiers,
  cache_creation_input_token_cost_above_1hr_priority: tieredCacheWriteModel.cache_creation_input_token_cost_above_1hr_priority,
  cache_creation_input_token_cost_above_1hr_flex: tieredCacheWriteModel.cache_creation_input_token_cost_above_1hr_flex
}
Object.assign(tieredCacheWriteModel, {
  supported_service_tiers: ['priority', 'flex'],
  cache_creation_input_token_cost_above_1hr_priority: 4 / 1_000_000,
  cache_creation_input_token_cost_above_1hr_flex: 1 / 1_000_000
})
assert.equal(buildProviderCostBreakdown({
  providerCode: ANTHROPIC_PROVIDER_CODE,
  model: 'claude-haiku-4-5',
  cacheWriteTokens: 10,
  cacheWrite1hTokens: 10,
  serviceTier: 'priority'
})?.cacheWrite1hUsdPer1M, 4)
assert.equal(buildProviderCostBreakdown({
  providerCode: ANTHROPIC_PROVIDER_CODE,
  model: 'claude-haiku-4-5',
  cacheWriteTokens: 10,
  cacheWrite1hTokens: 10,
  serviceTier: 'flex'
})?.cacheWrite1hUsdPer1M, 1)
Object.assign(tieredCacheWriteModel, originalTieredCacheWriteFields)
assert.equal(buildProviderCostBreakdown({
  providerCode: ANTHROPIC_PROVIDER_CODE,
  model: 'claude-haiku-4-5',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300,
  cacheWriteTokens: 40,
  cacheWrite1hTokens: 10,
  thinkingTokens: 12
})?.thinkingTokens, 12)
const availableOpenAIModels = new Set(openAIModelPricingList.map((item) => item.model))
const openAIModelPricingById = new Map(openAIModelPricingList.map((item) => [item.model, item]))
assert.equal(openAIModelPricingById.get('codex-auto-review')?.releaseDate, undefined, '未确认发布时间的 Codex 自动审查模型必须保持未知')
const datedOpenAIModelPricingList = openAIModelPricingList.filter((item) => item.releaseDate)
for (let index = 1; index < datedOpenAIModelPricingList.length; index += 1) {
  const previous = datedOpenAIModelPricingList[index - 1]
  const current = datedOpenAIModelPricingList[index]
  assert.ok(
    (previous.releaseDate ?? '') >= (current.releaseDate ?? ''),
    `${previous.model} (${previous.releaseDate}) should sort before ${current.model} (${current.releaseDate})`
  )
}
for (const id of [
  'gpt-5.6-sol',
  'gpt-5.6-terra',
  'gpt-5.6-luna',
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
assert.deepEqual(openAIModelPricingById.get('gpt-image-1')?.supportedApiProtocols, ['images', 'responses'])
assert.deepEqual(openAIModelPricingById.get('gpt-4o-mini-tts')?.supportedApiProtocols, ['audio'])
assert.equal(openAIModelPricingById.get('gpt-5.6-sol')?.releaseDate, '2026-06-26')
assert.equal(openAIModelPricingById.get('gpt-5.6-terra')?.releaseDate, '2026-06-26')
assert.equal(openAIModelPricingById.get('gpt-5.6-luna')?.releaseDate, '2026-06-26')
assert.equal(openAIModelPricingById.get('gpt-5.6-sol')?.contextWindowTokens, 1_050_000)
assert.equal(openAIModelPricingById.get('gpt-5.6-sol')?.maxInputTokens, 922_000)
assert.equal(openAIModelPricingById.get('gpt-5.6-sol')?.maxOutputTokens, 128_000)
assert.equal(openAIModelPricingById.get('gpt-5.6-terra')?.contextWindowTokens, 1_050_000)
assert.equal(openAIModelPricingById.get('gpt-5.6-terra')?.maxInputTokens, 922_000)
assert.equal(openAIModelPricingById.get('gpt-5.6-terra')?.maxOutputTokens, 128_000)
assert.equal(openAIModelPricingById.get('gpt-5.6-luna')?.contextWindowTokens, 1_050_000)
assert.equal(openAIModelPricingById.get('gpt-5.6-luna')?.maxInputTokens, 922_000)
assert.equal(openAIModelPricingById.get('gpt-5.6-luna')?.maxOutputTokens, 128_000)
assert.equal(openAIModelPricingById.get('gpt-5.5')?.maxInputTokens, undefined)
assert.equal(openAIModelPricingById.get('gpt-5.5')?.contextWindowTokens, 1_050_000)
assert.equal(openAIModelPricingById.get('gpt-4.1')?.maxInputTokens, undefined)
assert.equal(openAIModelPricingById.get('gpt-4.1')?.contextWindowTokens, 1_047_576)
assert.equal(openAIModelPricingById.get('o3')?.maxInputTokens, undefined)
assert.equal(openAIModelPricingById.get('o3')?.contextWindowTokens, 200_000)
assert.deepEqual(openAIModelPricingById.get('gpt-5.6-sol')?.supportedReasoningEfforts, ['none', 'low', 'medium', 'high', 'xhigh', 'max'])
assert.equal(openAIModelPricingById.get('gpt-5.6-sol')?.defaultReasoningEffort, undefined)
assert.deepEqual(openAIModelPricingById.get('gpt-5.5-pro')?.supportedServiceTiers, ['priority', 'flex'])
assert.equal(openAIModelPricingById.get('gpt-5.5-pro')?.defaultReasoningEffort, undefined)
assert.deepEqual(openAIModelPricingById.get('gpt-5.4-nano')?.supportedServiceTiers, ['priority', 'flex'])
assert.deepEqual(openAIModelPricingById.get('gpt-5.4-pro')?.supportedServiceTiers, ['priority', 'flex'])
assert.deepEqual(openAIModelPricingById.get('gpt-5.2')?.supportedServiceTiers, ['priority'])
assert.deepEqual(openAIModelPricingById.get('gpt-5.2-pro')?.supportedServiceTiers, ['priority'])
assert.deepEqual(openAIModelPricingById.get('gpt-5-pro')?.supportedReasoningEfforts, ['high'])
assert.equal(openAIModelPricingById.get('gpt-5-pro')?.defaultReasoningEffort, undefined)
assert.equal(openAIModelPricingById.get('gpt-5.5')?.releaseDate, '2026-04-23')
assert.equal(openAIModelPricingById.get('gpt-5.4-mini')?.releaseDate, '2026-03-17')
assert.equal(openAIModelPricingById.get('gpt-5.3-codex')?.releaseDate, '2026-02-01')
assert.equal(openAIModelPricingById.get('gpt-5.2')?.releaseDate, '2025-12-11')
assert.equal(openAIModelPricingById.get('gpt-5-search-api')?.releaseDate, '2025-08-07')
assert.equal(openAIModelPricingById.get('gpt-4.1')?.releaseDate, '2025-04-14')
assert.equal(openAIModelPricingById.get('babbage-002')?.releaseDate, '2024-01-04')
assert.equal(openAIModelPricingById.get('gpt-5.6-sol')?.inputUsdPer1M, 5)
assert.equal(openAIModelPricingById.get('gpt-5.6-sol')?.outputUsdPer1M, 30)
assert.equal(openAIModelPricingById.get('gpt-5.6-sol')?.cachedInputUsdPer1M, 0.5)
assert.equal(openAIModelPricingById.get('gpt-5.6-sol')?.cacheWriteUsdPer1M, 6.25)
assert.equal(openAIModelPricingById.get('gpt-5.6-terra')?.inputUsdPer1M, 2.5)
assert.equal(openAIModelPricingById.get('gpt-5.6-terra')?.outputUsdPer1M, 15)
assert.equal(openAIModelPricingById.get('gpt-5.6-terra')?.cachedInputUsdPer1M, 0.25)
assert.equal(openAIModelPricingById.get('gpt-5.6-terra')?.cacheWriteUsdPer1M, 3.125)
assert.equal(openAIModelPricingById.get('gpt-5.6-luna')?.inputUsdPer1M, 1)
assert.equal(openAIModelPricingById.get('gpt-5.6-luna')?.outputUsdPer1M, 6)
assert.equal(openAIModelPricingById.get('gpt-5.6-luna')?.cachedInputUsdPer1M, 0.1)
assert.equal(openAIModelPricingById.get('gpt-5.6-luna')?.cacheWriteUsdPer1M, 1.25)

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
assert.equal(estimateProviderCostUsd({
  providerCode: GPT_VENDOR_CODE,
  model: 'gpt-5.6-luna',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300,
  cacheWriteTokens: 40
}), 0.00198)
assert.equal(buildProviderCostBreakdown({
  providerCode: GPT_VENDOR_CODE,
  model: 'gpt-5.6-terra',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300,
  cacheWriteTokens: 40
})?.cacheWriteUsdPer1M, 3.125)

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

const gatewayUsageRecordsSource = readSource('modules/gateway/usage/records.ts')
assert.match(gatewayUsageRecordsSource, /function recordCompletedUpstreamAttempt/)
assert.match(gatewayUsageRecordsSource, /recordClientAbortedUpstreamAttempt/)
assert.match(gatewayUsageRecordsSource, /resolveGatewayUsageModel/)
assert.match(gatewayUsageRecordsSource, /usageSemanticForProfile/)
assert.match(gatewayUsageRecordsSource, /defaultGatewayUsageProviderCode/)
assert.match(gatewayUsageRecordsSource, /parseGatewayProtocolErrorPayload/)
assert.doesNotMatch(gatewayUsageRecordsSource, /parseErrorPayload/)
assert.doesNotMatch(gatewayUsageRecordsSource, /resolveOpenAIAccountModelMapping/)
assert.doesNotMatch(gatewayUsageRecordsSource, /ANTHROPIC_PROVIDER_CODE|GPT_VENDOR_CODE/)
assert.doesNotMatch(gatewayUsageRecordsSource, /function usageSemanticForProvider/)

const gatewayFixedResponsesSource = readSource('modules/gateway/response/fixed-responses.ts')
assert.match(gatewayFixedResponsesSource, /usageSemanticForProfile/)
assert.match(gatewayFixedResponsesSource, /defaultGatewayUsageProviderCode/)
assert.doesNotMatch(gatewayFixedResponsesSource, /GPT_VENDOR_CODE/)

const gatewayResponseFinalizationSource = readSource('modules/gateway/response/finalization.ts')
assert.match(gatewayResponseFinalizationSource, /applyGatewayProtocolStreamUsageFallback/)
assert.match(gatewayResponseFinalizationSource, /completed:\s*streamResult\.completed/, '流式 usage fallback 必须知道成功完成状态，避免成功空输出请求 input token 记为 0')
assert.match(gatewayResponseFinalizationSource, /parseGatewayProtocolErrorPayload/)
assert.doesNotMatch(gatewayResponseFinalizationSource, /parseErrorPayload/)
assert.doesNotMatch(gatewayResponseFinalizationSource, /applyOpenAIStreamUsageFallback/)
assert.doesNotMatch(gatewayResponseFinalizationSource, /applyAnthropicStreamUsageFallback/)
assert.match(gatewayResponseFinalizationSource, /gateway_stream_usage_estimated/)
assert.match(gatewayResponseFinalizationSource, /responseSemanticText\s*=\s*completeBodyText/, '非流式完整 JSON 响应语义文本不能依赖成功审计正文捕获开关')
assert.match(gatewayResponseFinalizationSource, /responseBodyText:\s*responseBodyText\s*\?\?\s*responseSemanticText/, '非流式 usage fallback 应读取完整检查窗口文本')
assert.match(
  gatewayResponseFinalizationSource,
  /if \(!upstreamResponse\.body\) \{[\s\S]*prepareUpstreamResponseForDownstream[\s\S]*markTransportCommitted[\s\S]*markSemanticCommitted/,
  '完整空 body HTTP 响应必须透明提交，不得按客户端画像切号'
)
assert.doesNotMatch(gatewayResponseFinalizationSource, /upstream_empty_body/, '空 body 完整响应不得写账户故障或服务端重试语义')

const gatewayResponseStreamSource = readSource('modules/gateway/response/stream.ts')
assert.match(gatewayResponseStreamSource, /requireGatewayProtocolDriverForResponseProtocol/)
assert.doesNotMatch(gatewayResponseStreamSource, /OpenAIStreamInspector/)
assert.doesNotMatch(gatewayResponseStreamSource, /AnthropicStreamInspector/)
assert.doesNotMatch(gatewayResponseStreamSource, /extractAnthropicSseSemanticFrames/)

const openAIProtocolDriverSource = readSource('modules/gateway/protocols/openai-v1/driver.ts')
assert.match(openAIProtocolDriverSource, /applyStreamUsageFallback:\s*applyOpenAIStreamUsageFallback/)
assert.match(openAIProtocolDriverSource, /createStreamInspector:\s*\(\)\s*=>\s*new OpenAIStreamInspector/)
assert.match(openAIProtocolDriverSource, /parseErrorPayload:\s*parseOpenAIErrorPayload/)
const openAIStreamInspectionSource = readSource('modules/gateway/protocols/openai-v1/stream-inspection.ts')
assert.match(openAIStreamInspectionSource, /input\.completed\s*!==\s*true/, 'OpenAI 流式成功完成但没有上游 usage 时，仍应估算 input token')
assert.match(openAIStreamInspectionSource, /input\.outputReceived\s*&&\s*!positiveTokenCount\(nextUsage\.outputTokens\)/, '没有可见输出时不能凭空估算 output token')
const anthropicProtocolDriverSource = readSource('modules/gateway/protocols/anthropic-v1/driver.ts')
assert.match(anthropicProtocolDriverSource, /applyStreamUsageFallback:\s*applyAnthropicStreamUsageFallback/)
assert.match(anthropicProtocolDriverSource, /createStreamInspector:\s*\(\)\s*=>\s*new AnthropicStreamInspector/)
assert.match(anthropicProtocolDriverSource, /extractSseSemanticFrames/)
assert.match(anthropicProtocolDriverSource, /parseErrorPayload:\s*parseAnthropicErrorPayload/)

const modelPricingServiceSource = readSource('modules/model-pricing/model-pricing.service.ts')
const modelCatalogServiceSource = readSource('modules/model-pricing/model-catalog.service.ts')
const modelPricingProviderDriverRegistrySource = readSource('modules/model-pricing/provider-driver.registry.ts')
assert.match(modelPricingServiceSource, /modelPricingProviderDriverForProvider/)
assert.doesNotMatch(modelPricingServiceSource, /anthropic-model-pricing\.data|openai-model-pricing\.data/)
assert.doesNotMatch(modelPricingServiceSource, /ANTHROPIC_PROVIDER_CODE|isOpenAICompatibleProviderCode/)
assert.match(modelCatalogServiceSource, /modelPricingProviderDriverForProvider/)
assert.doesNotMatch(modelCatalogServiceSource, /ANTHROPIC_PROVIDER_CODE/)
assert.match(modelPricingProviderDriverRegistrySource, /const openAIModelPricingDriver/)
assert.match(modelPricingProviderDriverRegistrySource, /const anthropicModelPricingDriver/)
assert.match(modelPricingProviderDriverRegistrySource, /buildAnthropicModelCandidates/)

const gatewayFailureDispatchSource = readSource('modules/gateway/response/failure-dispatch.ts')
assert.match(gatewayFailureDispatchSource, /shouldRecordAbortedUpstreamAttempt/)
assert.match(gatewayFailureDispatchSource, /suppressGatewayAccountLocally/)
assert.match(gatewayFailureDispatchSource, /parseGatewayProtocolErrorPayload/)
assert.match(gatewayFailureDispatchSource, /recordFailedDispatchAttempt/, '账号准备等未创建 upstream attempt 的失败分支必须补审计 attempt')
assert.doesNotMatch(gatewayFailureDispatchSource, /parseErrorPayload/)
assert.doesNotMatch(gatewayFailureDispatchSource, /shouldRetryPolicyAttempt/)
assert.doesNotMatch(gatewayFailureDispatchSource, /shouldRetryAttempt\(/)

const retryPolicySource = readSource('shared/retry-policy.ts')
assert.match(retryPolicySource, /export function retryAttemptCount/)
assert.match(retryPolicySource, /export function shouldRetryPolicyAttempt/)

const gatewayDispatchHelpersSource = readSource('modules/gateway/dispatch/helpers.ts')
assert.doesNotMatch(gatewayDispatchHelpersSource, /temporaryUnschedulableRetryPolicy/)
assert.doesNotMatch(gatewayDispatchHelpersSource, /gateway_temporary_unschedulable_same_account_retry/)

const gatewayUpstreamDispatchSource = readSource('modules/gateway/dispatch/upstream-dispatch.ts')
assert.match(gatewayUpstreamDispatchSource, /gateway_temporary_unschedulable_same_account_retry/)
assert.doesNotMatch(gatewayUpstreamDispatchSource, /temporaryUnschedulableRetryPolicy/)
assert.match(gatewayUpstreamDispatchSource, /retryAttemptCount\(sameAccountRetryPolicy\)/)
assert.match(gatewayUpstreamDispatchSource, /shouldRetryPolicyAttempt\(attemptIndex, sameAccountRetryPolicy\)/)
assert.match(gatewayUpstreamDispatchSource, /waitForSameAccountRetry/)
assert.match(gatewayUpstreamDispatchSource, /recordAccountCapacityLimitFailure\([\s\S]*auditCapture[\s\S]*auditAttemptIndex/, '账号容量失败写使用记录时也必须补审计 attempt')

const oauthAccessTokenRefreshSource = readSource('modules/openai-oauth/openai-oauth-access-token-refresh.service.ts')
assert.match(oauthAccessTokenRefreshSource, /openAIOAuthRefreshRaceRetryPolicy/)
assert.match(oauthAccessTokenRefreshSource, /shouldRetryPolicyAttempt\(attempt, openAIOAuthRefreshRaceRetryPolicy\)/)
assert.match(oauthAccessTokenRefreshSource, /runtimeConfig\.processRole === 'server' \|\| runtimeConfig\.processRole === 'worker'\s*\?\s*'db-service'\s*:\s*'sync'/)
assert.doesNotMatch(oauthAccessTokenRefreshSource, /isSingleProcessWorkerRole\(\)[\s\S]{0,200}runLocalOpenAIOAuthDbServiceOperation/)
assert.doesNotMatch(oauthAccessTokenRefreshSource, /normalizeOpenAIOAuthStoppedRefreshExceptionMessages/)
assert.doesNotMatch(oauthAccessTokenRefreshSource, /历史后台刷新失败/)

const gatewayAccountPreparationSource = readSource('modules/gateway/dispatch/account-preparation.ts')
assert.match(gatewayAccountPreparationSource, /prepareGatewayUpstreamAccount/)
assert.match(gatewayAccountPreparationSource, /recordFailedDispatchAttempt/, '代理配置不可用写使用记录时也必须补审计 attempt')
assert.doesNotMatch(gatewayAccountPreparationSource, /refreshOpenAIOAuthAccountAccessToken/)
assert.doesNotMatch(gatewayAccountPreparationSource, /shouldRefreshOpenAIOAuthCredentials/)

const gptProviderDriverSource = readSource('modules/providers/drivers/gpt/driver.ts')
const gptOAuthDispatchPreparationSource = readSource('modules/providers/drivers/gpt/oauth-dispatch-preparation.ts')
assert.match(gptProviderDriverSource, /prepareAccountBeforeDispatch/)
assert.match(gptProviderDriverSource, /prepareGptAccountBeforeDispatch/)
assert.match(gptOAuthDispatchPreparationSource, /refreshOpenAIOAuthAccountAccessToken/)
assert.match(gptOAuthDispatchPreparationSource, /gateway_openai_oauth_access_token_preheated/)

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
assert.match(frontendRouterSource, /管理自己的 API Key，绑定自己的策略路由。/)
assert.match(frontendRouterSource, /维护自己的账户分组，策略路由再绑定分组统一调度。/)
assert.match(frontendRouterSource, /按系统账户管理 API Key，并为 API Key 选择策略路由。/)
assert.doesNotMatch(frontendRouterSource, /API Key 再绑定分组统一调度/)
assert.doesNotMatch(frontendRouterSource, /API Key 和分组绑定/)
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
const memoryGatewayHttpSource = readSource('modules/gateway/testing/memory-gateway-http.ts')
assert.match(accountTestSource, /from '\.\.\/gateway\/testing\/memory-gateway-http\.js'/)
assert.match(accountTestSource, /handleOpenAIGatewayRequest/)
assert.match(accountTestSource, /candidateAccounts:\s*\[diagnosticCandidate\]/)
assert.match(accountTestSource, /disableSessionAffinity:\s*true/)
assert.match(accountTestSource, /trafficSource:\s*input\.trafficSource\s*\?\?\s*'manual_account_test'/)
assert.match(accountTestSource, /testOpenAIAccountWithDiagnosticRetries/)
assert.match(accountTestSource, /diagnosticAccountTestGatewaySettingsOverride/)
assert.doesNotMatch(accountTestSource, /class MemoryGatewayRequest/)
assert.doesNotMatch(accountTestSource, /class MemoryGatewayResponse/)
assert.doesNotMatch(accountTestSource, /accountTestResponsePreviewBytes\s*=\s*256\s*\*\s*1024/)
assert.doesNotMatch(accountTestSource, /BoundedBufferCollector\(accountTestResponsePreviewBytes\)/)
assert.doesNotMatch(accountTestSource, /OpenAIStreamInspector/)
assert.match(memoryGatewayHttpSource, /export const accountTestResponsePreviewBytes\s*=\s*256\s*\*\s*1024/)
assert.match(memoryGatewayHttpSource, /createGatewayTestRequest/)
assert.match(memoryGatewayHttpSource, /createMemoryGatewayRequest/)
assert.match(memoryGatewayHttpSource, /class MemoryGatewayRequest/)
assert.match(memoryGatewayHttpSource, /class MemoryGatewayResponse/)
assert.match(memoryGatewayHttpSource, /BoundedBufferCollector\(accountTestResponsePreviewBytes\)/)
assert.match(memoryGatewayHttpSource, /new OpenAIStreamInspector\(\)/)
const accountDiagnosticRetrySource = readSource('modules/accounts/account-diagnostic-retry-policy.ts')
assert.match(accountDiagnosticRetrySource, /accountDiagnosticRetryTimeoutMs\s*=\s*\[10_000,\s*20_000,\s*30_000\]/)
assert.match(accountDiagnosticRetrySource, /temporaryUnschedulableRetryAttempts:\s*0/)
assert.match(accountDiagnosticRetrySource, /temporaryUnschedulableRetryIntervalSeconds:\s*0/)
assert.match(accountDiagnosticRetrySource, /textFirstResponseTimeoutSeconds:\s*timeoutSeconds/)
const diagnosticRetrySource = sourceBetween(accountTestSource, 'export async function testOpenAIAccountWithDiagnosticRetries', 'export async function testOpenAIAccount')
assert.doesNotMatch(diagnosticRetrySource, /statusCode|errorCode/, '账号诊断重试不能按上游状态码或错误码分支')
const accountTestTaskQueueSource = readSource('modules/accounts/account-test-task-queue.service.ts')
assert.match(accountTestTaskQueueSource, /testOpenAIDraftAccountWithDiagnosticRetries/)
assert.match(accountTestTaskQueueSource, /openAIDraftAccountSecret\(draft,\s*attemptSignal\)/, '草稿账号 OAuth 刷新必须纳入单次诊断 attempt 的超时 signal')
assert.match(accountTestTaskQueueSource, /diagnosticAccountTestGatewaySettingsOverride\(undefined,\s*timeoutMs\)/)

const accountErrorPolicySource = readSource('modules/gateway/policy/account-error-policy.service.ts')
assert.match(accountErrorPolicySource, /parseGatewayProtocolErrorPayload/)
assert.doesNotMatch(accountErrorPolicySource, /JSON\.parse/)
assert.match(accountErrorPolicySource, /accountErrorPolicyUpstreamSummary/)
assert.match(accountErrorPolicySource, /genericUpstreamResponseFailureReason\(statusCode,\s*upstreamSummary\)/)

const backgroundJobsSource = readSource('modules/background/background-jobs.ts')
const backgroundSettingsNumberSource = sourceBetween(backgroundJobsSource, 'function settingsNumber', 'async function databaseFileBytes')
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
assert.match(backgroundJobsSource, /const safety = await usageStatsAggregationSafety\(\)/)
assert.match(backgroundJobsSource, /safeCreatedBefore: safety\.safeCreatedBefore/)
assert.match(backgroundJobsSource, /requestIngestWorkerDrainStatus\(6000\)/)
assert.match(backgroundJobsSource, /defaultUsageStatsSafeCreatedBeforeIso\(\)/)
assert.match(backgroundJobsSource, /使用记录 ingest 队列已有/)
assert.match(backgroundJobsSource, /usageStatsSafeCreatedBeforeForPendingBacklog/)
assert.match(backgroundJobsSource, /oldestRedisStreamUsageRecordCreatedAtForStatsAggregation/)
assert.match(backgroundJobsSource, /settingsNumber\('cooldownAccountRetestIntervalSeconds', 1, 3600\)/)
assert.doesNotMatch(backgroundSettingsNumberSource, /typeof value === 'string' \? Number\(value\)/)

const accountProbeJobsSource = readSource('modules/background/account-probe-jobs.ts')
assert.match(accountProbeJobsSource, /enqueueCooldownAccountRetest/)
assert.match(accountProbeJobsSource, /getCooldownAccountRetestQueueSnapshot/)
assert.match(accountProbeJobsSource, /settingsNumber\('cooldownAccountRetestBatchSize', 1, 100\)/)
assert.match(accountProbeJobsSource, /settingsNumber\('defaultTemporaryUnschedulableMinutes', 1, 1440\)/)
assert.match(accountProbeJobsSource, /settingsNumber\('cooldownAccountRetestMaxBackoffHours', 1, 24 \* 30\)/)
assert.doesNotMatch(accountProbeJobsSource, /cooldownAccountRetestLongTermIntervalHours/)

const cooldownAccountRetestSource = readSource('modules/background/cooldown-account-retest.service.ts')
assert.match(cooldownAccountRetestSource, /sequenceRetryPolicy\('cooldown_account_retest_revival', \[\], 0\)/)
assert.match(cooldownAccountRetestSource, /createRetryQueue/)
assert.doesNotMatch(cooldownAccountRetestSource, /background_cooldown_account_retest_retry_scheduled/)
assert.match(cooldownAccountRetestSource, /diagnostics:\s*'full'/)
assert.doesNotMatch(cooldownAccountRetestSource, /\bmodel\s*:/)
assert.match(cooldownAccountRetestSource, /trafficSource:\s*'cooldown_retest'/)
assert.match(cooldownAccountRetestSource, /temporaryUnschedulableRetryAttempts:\s*0/)
assert.match(cooldownAccountRetestSource, /testOpenAIAccountWithDiagnosticRetries/)
assert.doesNotMatch(cooldownAccountRetestSource, /requestShape/, '冷却复测只能使用账户测试健康探针，不能复用失败请求形态')
assert.match(cooldownAccountRetestSource, /account\.boundGroupId/)
assert.doesNotMatch(cooldownAccountRetestSource, /waitForRetryDelay/)

const accountHealthCheckSource = readSource('modules/background/account-health-check.service.ts')
assert.match(accountHealthCheckSource, /diagnostics:\s*'limited'/)
assert.doesNotMatch(accountHealthCheckSource, /\bmodel\s*:/)
assert.match(accountHealthCheckSource, /trafficSource:\s*'account_health_check'/)
assert.doesNotMatch(accountHealthCheckSource, /requestShape/, '账号健康检查只能使用账户测试健康探针，不能复用失败请求形态')

const gatewayAccountSideEffectsSource = readSource('modules/gateway/runtime/account-side-effects.service.ts')
assert.match(gatewayAccountSideEffectsSource, /runSingleGatewayAccountPrecheck/)
assert.match(gatewayAccountSideEffectsSource, /diagnostics:\s*'full'/)
assert.doesNotMatch(gatewayAccountSideEffectsSource, /diagnostics:\s*'limited'/)
assert.match(gatewayAccountSideEffectsSource, /stateAfterResult\.reason\s*=\s*accountPrecheckFailureReason\(result\)/)
assert.match(gatewayAccountSideEffectsSource, /function accountPrecheckFailureReason/)
assert.match(gatewayAccountSideEffectsSource, /errorCode\?:\s*string/)
assert.match(gatewayAccountSideEffectsSource, /precheckMaxAttempts\s*=\s*accountDiagnosticRetryTimeoutMs\.length/)
assert.match(gatewayAccountSideEffectsSource, /diagnosticAccountTestGatewaySettingsOverride\(state\.settings,\s*timeoutMs\)/)
assert.doesNotMatch(gatewayAccountSideEffectsSource, /model:\s*await preferredSystemAccountTestModelAsync/)
assert.match(gatewayAccountSideEffectsSource, /systemAccountId:\s*state\.systemAccountId/)
assert.match(gatewayAccountSideEffectsSource, /trafficSource:\s*'runtime_recovery_probe'/)
assert.doesNotMatch(gatewayAccountSideEffectsSource, /requestShape:/, '运行态恢复探针不能复用失败请求形态')
assert.doesNotMatch(gatewayAccountSideEffectsSource, /precheckAttemptTimeoutMs|precheckRetryDelayMs|45_000/)
const gatewayClientStrategySource = readSource('modules/gateway/client-profiles/strategy.ts')
assert.equal(
  existsSync(resolve(dirname(fileURLToPath(import.meta.url)), '../../modules/gateway/client-profiles/codex-switch-probe.ts')),
  false,
  'usage/pricing 回归不得继续依赖旧 Codex 直接切号 probe 文件'
)
assert.match(gatewayClientStrategySource, /export function resolveOpenAIGatewayClientStrategy/)
assert.match(gatewayClientStrategySource, /clientProfile === 'codex' && downstreamProtocol === 'responses_sse'/, 'Codex Responses SSE 重试协调必须由正式 client strategy owner 判定')
assert.match(gatewayClientStrategySource, /allowCodexTurnAccountAvoidance:\s*Boolean\(codexTurn\)/, '正式 Codex client strategy 必须显式管理 turn 级账号避让能力')
assert.doesNotMatch(gatewayClientStrategySource, /probeCodexSwitchCandidateAccount|accountDiagnosticRetryTimeoutMs/, '正式 client strategy 不得回引旧直接切号 probe 或账号诊断重试实现')

const usageRecordsRepositorySource = readSource('storage/usage-records.repository.ts')
assert.match(usageRecordsRepositorySource, /traffic_source/)
assert.doesNotMatch(usageRecordsRepositorySource, /requestShape|RequestShape/, '使用记录仓储不应保留请求形态查询入口')
assert.doesNotMatch(usageRecordsRepositorySource, /COALESCE\(traffic_source, 'gateway'\)/)

const usageRecordListQuerySource = readSource('storage/usage-record-list-query.ts')
assert.match(usageRecordListQuerySource, /\$\{columns\.trafficSource\} = \?/)
assert.doesNotMatch(usageRecordListQuerySource, /COALESCE\(\$\{columns\.trafficSource\}, 'gateway'\)/)

const auditLogListQuerySource = readSource('storage/audit-log-list-query.ts')
assert.match(auditLogListQuerySource, /al\.traffic_source = \?/)
assert.doesNotMatch(auditLogListQuerySource, /COALESCE\(al\.traffic_source, 'gateway'\)/)

const clientIpStatsAggregationRepositorySource = readSource('storage/client-ip-stats-aggregation.repository.ts')
assert.match(clientIpStatsAggregationRepositorySource, /traffic_source NOT IN \('runtime_recovery_probe', 'cooldown_retest'\)/)
assert.match(clientIpStatsAggregationRepositorySource, /traffic_source IN \('runtime_recovery_probe', 'cooldown_retest'\)/)
assert.doesNotMatch(clientIpStatsAggregationRepositorySource, /COALESCE\(traffic_source, 'gateway'\)/)

const usageStatsRepositorySource = readSource('storage/usage-stats.repository.ts')
assert.doesNotMatch(usageStatsRepositorySource, /traffic_source <> 'cooldown_retest'/)
assert.doesNotMatch(usageStatsRepositorySource, /latestIgnoredUsageRecordCursor/)
assert.doesNotMatch(usageStatsRepositorySource, /COALESCE\(traffic_source, 'gateway'\)/)

const usageStatsWritersSource = readSource('storage/usage-stats-writers.ts')
const usageStatsAuthorizationDailyWriterSource = readSource('storage/usage-stats-authorization-daily-writer.ts')
const usageStatsModelWriterSource = readSource('storage/usage-stats-model-writer.ts')
const usageStatsErrorWriterSource = readSource('storage/usage-stats-error-writer.ts')
const usageStatsLatencyWriterSource = readSource('storage/usage-stats-latency-writer.ts')
const usageStatsTimeBucketsSource = readSource('storage/usage-stats-time-buckets.ts')
const usageStatsWriterParamsSource = readSource('storage/usage-stats-writer-params.ts')
assert.match(usageStatsWritersSource, /from '\.\/usage-stats-authorization-daily-writer\.js'/)
assert.match(usageStatsWritersSource, /from '\.\/usage-stats-model-writer\.js'/)
assert.match(usageStatsWritersSource, /from '\.\/usage-stats-error-writer\.js'/)
assert.match(usageStatsWritersSource, /from '\.\/usage-stats-latency-writer\.js'/)
assert.match(usageStatsWritersSource, /from '\.\/usage-stats-time-buckets\.js'/)
assert.match(usageStatsWritersSource, /function shouldRecordAccountQualityStats\(row: UsageStatsRecordRow\): boolean \{[\s\S]+row\.traffic_source === 'runtime_recovery_probe'[\s\S]+row\.traffic_source === 'cooldown_retest'[\s\S]+row\.traffic_source === 'hybrid_scoring'[\s\S]+row\.traffic_source === 'hybrid_quality_scoring'[\s\S]+return false[\s\S]+\}/, '运行态恢复探针、恢复探活、混合评分和混合质量评分应计入用量统计但不写入账号质量分钟样本')
assert.match(usageStatsWritersSource, /if \(shouldRecordAccountQualityStats\(row\)\) \{[\s\S]+upsertAccountQualityMinuteStats/, '恢复探活应计入用量统计但不写入账号质量分钟样本')
assert.match(usageStatsWritersSource, /if \(shouldRecordAccountQualityStats\(row\)\) \{[\s\S]+subtractAccountQualityMinuteStats/, '恢复探活反向扣减时也不应触碰账号质量分钟样本')
assert.doesNotMatch(usageStatsWritersSource, /function authorizationReportRows|authorization_team_usage_summary_daily|authorization_user_usage_summary_daily/)
assert.doesNotMatch(usageStatsWritersSource, /function upsertUsageModelBuckets|usage_model_/)
assert.doesNotMatch(usageStatsWritersSource, /function upsertUsageErrorBuckets|usage_error_/)
assert.doesNotMatch(usageStatsWritersSource, /function upsertUsageLatencyEntry|usage_latency_|latencyBucketUpperBoundsMs/)
assert.match(usageStatsAuthorizationDailyWriterSource, /export function upsertAuthorizationUsageReportRows/)
assert.match(usageStatsAuthorizationDailyWriterSource, /export function subtractAuthorizationUsageReportRows/)
assert.match(usageStatsAuthorizationDailyWriterSource, /authorization_team_usage_summary_daily/)
assert.match(usageStatsAuthorizationDailyWriterSource, /authorization_user_usage_summary_daily/)
assert.match(usageStatsModelWriterSource, /export function upsertUsageModelBuckets/)
assert.match(usageStatsModelWriterSource, /export function subtractUsageModelBuckets/)
assert.match(usageStatsModelWriterSource, /usageModelTimeBuckets/)
assert.match(usageStatsErrorWriterSource, /export function upsertUsageErrorBuckets/)
assert.match(usageStatsErrorWriterSource, /export function subtractUsageErrorBuckets/)
assert.match(usageStatsErrorWriterSource, /usageErrorTimeBuckets/)
assert.match(usageStatsLatencyWriterSource, /export function upsertUsageLatencyEntry/)
assert.match(usageStatsLatencyWriterSource, /export function subtractUsageLatencyEntry/)
assert.match(usageStatsLatencyWriterSource, /usageLatencyTimeBuckets/)
assert.match(usageStatsLatencyWriterSource, /latencyBucketUpperBoundsMs/)
assert.match(usageStatsTimeBucketsSource, /export function usageStatsTimeKeys/)
assert.match(usageStatsTimeBucketsSource, /usageStatsTimezone/)
assert.match(usageStatsTimeBucketsSource, /usage_model_/)
assert.match(usageStatsTimeBucketsSource, /usage_error_/)
assert.match(usageStatsTimeBucketsSource, /usage_latency_/)
assert.match(usageStatsWriterParamsSource, /export function statsParamsTail/)
assert.match(usageStatsWriterParamsSource, /export function statsSubtractParams/)

const usageStatsAggregationSource = readSource('storage/usage-stats-aggregation.ts')
assert.match(usageStatsAggregationSource, /shouldAggregateUsageStatsRecord\(row: UsageStatsRecordRow\): boolean/)
assert.match(usageStatsAggregationSource, /return true/)
assert.doesNotMatch(usageStatsAggregationSource, /traffic_source !== 'cooldown_retest'/)
assert.doesNotMatch(usageStatsAggregationSource, /traffic_source \?\? 'gateway'/)

const usageStatsTypesSource = readSource('storage/usage-stats-types.ts')
assert.match(usageStatsTypesSource, /traffic_source: string\r?\n/)
assert.doesNotMatch(usageStatsTypesSource, /traffic_source: string \| null/)

const openAIAccountSelectorSource = readSource('storage/openai-account-selector.repository.ts')
const gatewayDispatchCandidateWindowSource = readSource('storage/gateway-dispatch-candidate-window.repository.ts')
assert.doesNotMatch(gatewayDispatchCandidateWindowSource, /COALESCE\(source_accounts\.type, accounts\.type\)/)
assert.match(openAIAccountSelectorSource, /accountAccess\.accountAccessType === 'account_authorized' && !row\.resource_account_id/)

const accountRequestSchemasSource = readSource('modules/accounts/account-request.schemas.ts')
assert.match(accountRequestSchemasSource, /const accountUpdateSchema = z\.object/)
assert.match(accountRequestSchemasSource, /concurrencyLimit:\s*z\.number\(\)\.int\(\)\.min\(1\)\.optional\(\)/)
assert.match(accountRequestSchemasSource, /status:\s*z\.enum\(\['active', 'pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable'\]\)\.optional\(\)/)
assert.match(accountRequestSchemasSource, /\}\)\.strict\(\)/)

const accountsRoutesSource = readSource('modules/accounts/accounts.routes.ts')
assert.match(accountsRoutesSource, /accountUpdateSchema\.safeParse\(req\.body\)/)

const repositoriesSource = readSource('storage/repositories.ts')
const accountWriteInputSource = readSource('storage/account-write-input.ts')
const accountStatusSource = readSource('storage/account-status.ts')
assert.doesNotMatch(repositoriesSource, /SET type = \?,\s*credentials_encrypted = \?/s)
assert.match(accountWriteInputSource, /function normalizedAccountType\(value: unknown\): string/)
assert.match(accountStatusSource, /function normalizedAccountStatusInput\(value: unknown, fallback: AccountStatus\): AccountStatus/)
assert.match(accountWriteInputSource, /function normalizedPositiveIntegerInput\(value: unknown, fallback: number, label: string\): number/)
assert.doesNotMatch(repositoriesSource, /String\(input\.type \?\? 'api_key'\)/)
assert.doesNotMatch(repositoriesSource, /Number\(input\.concurrencyLimit \?\? current\.concurrencyLimit\)/)
assert.doesNotMatch(repositoriesSource, /Number\(input\.priority \?\? current\.priority\)/)
assert.doesNotMatch(`${repositoriesSource}\n${accountWriteInputSource}`, /value === 1 \|\| value === '1'/)
assert.doesNotMatch(`${repositoriesSource}\n${accountWriteInputSource}`, /typeof value === 'string' \? Number\(value\)/)

const accountDeleteCleanupRepositorySource = readSource('storage/account-delete-cleanup.repository.ts')
assert.match(accountDeleteCleanupRepositorySource, /function logicallyDeleteSourceAccountWithInstances/)
assert.match(accountDeleteCleanupRepositorySource, /WHERE authorization_instance_source_account_id = \?/)
assert.doesNotMatch(accountDeleteCleanupRepositorySource, /authorization_instance_source_account_id = NULL/)

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

const externalPublicApiCatalogSource = [
  readSource('modules/external-integrations/external-public-api-catalog.items.ts'),
  readSource('modules/external-integrations/external-public-api-response-fields.ts'),
  readSource('modules/external-integrations/external-public-api-scopes.ts'),
  readSource('modules/external-integrations/external-public-api-catalog.ts')
].join('\n')
assert.match(externalPublicApiCatalogSource, /id: 'api-key-delete'[\s\S]+routeStrategyId:\s*'rts_xxx'/)
assert.doesNotMatch(externalPublicApiCatalogSource, /groupRouteStrategy:\s*'priority_failover'/)
assert.doesNotMatch(externalPublicApiCatalogSource, /apiKey:\s*\{[^\n]*groupId:\s*'grp_xxx'/)
assert.doesNotMatch(externalPublicApiCatalogSource, /新的主绑定分组 ID/)
assert.doesNotMatch(externalPublicApiCatalogSource, /绑定分组 ID；与 groupName/)
assert.doesNotMatch(externalPublicApiCatalogSource, /status:\s*'mock'/, '公开接口接入文档不应把已落地接口标记为 Mock 数据')
const externalPublicAccountUpdateCatalogSource = sourceBetween(externalPublicApiCatalogSource, "id: 'account-update'", "id: 'account-delete'")
assert.match(externalPublicAccountUpdateCatalogSource, /name: 'accountId'[\s\S]+required: true/)
assert.match(externalPublicAccountUpdateCatalogSource, /name: 'providerCode'[\s\S]+required: false/)
assert.match(externalPublicAccountUpdateCatalogSource, /name: 'providerProtocolProfileId'[\s\S]+required: false/)
assert.match(externalPublicAccountUpdateCatalogSource, /name: 'type'[\s\S]+required: false/)
assert.doesNotMatch(externalPublicAccountUpdateCatalogSource, /targetDisplayName/, '账号修改公开文档不应再保留无实际语义的兼容字段')
assert.match(externalPublicAccountUpdateCatalogSource, /accountId:\s*'acc_xxx'/)
assert.match(externalPublicAccountUpdateCatalogSource, /apiKey:\s*'sk-\.\.\.'/)
assert.match(externalPublicAccountUpdateCatalogSource, /status:\s*'disabled'/)
const externalPublicApiKeyListCatalogSource = sourceBetween(externalPublicApiCatalogSource, "id: 'api-key-list'", "id: 'api-key-add'")
assert.doesNotMatch(externalPublicApiKeyListCatalogSource, /name: 'groupId'/, 'API Key 列表公开文档不应再保留按分组筛选的旧契约')
assert.doesNotMatch(externalPublicApiKeyListCatalogSource, /groupBindings: \[\{[^}]*groupId: 'grp_xxx'/, 'API Key 列表公开文档不应返回策略内分组绑定')
const externalPublicRouteStrategyListCatalogSource = sourceBetween(externalPublicApiCatalogSource, "id: 'route-strategy-list'", "id: 'route-strategy-add'")
assert.match(externalPublicRouteStrategyListCatalogSource, /path: '\/__aipublic__\/route-strategy\/list'/)
assert.match(externalPublicApiCatalogSource, /const routeStrategy[\s\S]+groupBindings/, '路由策略公开文档示例应包含分组绑定摘要')
const externalPublicRouteStrategyUpdateCatalogSource = sourceBetween(externalPublicApiCatalogSource, "id: 'route-strategy-update'", "id: 'route-strategy-delete'")
assert.match(externalPublicRouteStrategyUpdateCatalogSource, /routeStrategyId:\s*'rts_xxx'/)
assert.match(externalPublicRouteStrategyUpdateCatalogSource, /mode:\s*'round_robin'/)
const externalPublicAccountListCatalogSource = sourceBetween(externalPublicApiCatalogSource, "id: 'account-list'", "id: 'account-add'")
assert.match(externalPublicAccountListCatalogSource, /name: 'providerProtocolProfileId'[\s\S]+required: false/, '账号列表公开文档必须暴露实际支持的协议档案筛选字段')
assert.match(externalPublicApiCatalogSource, /const account[\s\S]+providerProtocolProfileId:\s*'profile_gpt_openai_v1'/, '账号列表公开文档示例必须返回协议档案字段')
const externalPublicGroupUpdateCatalogSource = sourceBetween(externalPublicApiCatalogSource, "id: 'group-update'", "id: 'group-delete'")
assert.match(externalPublicGroupUpdateCatalogSource, /name: 'targetUsername'[\s\S]+required: false/)
assert.match(externalPublicGroupUpdateCatalogSource, /groupId:\s*'grp_xxx'[\s\S]+name:\s*'福利-主池'/)
const externalPublicApiKeyUpdateCatalogSource = sourceBetween(externalPublicApiCatalogSource, "id: 'api-key-update'", "id: 'api-key-delete'")
assert.match(externalPublicApiKeyUpdateCatalogSource, /name: 'targetUsername'[\s\S]+required: false/)
assert.match(externalPublicApiKeyUpdateCatalogSource, /apiKeyId:\s*'key_xxx'[\s\S]+status:\s*'disabled'/)
const externalPublicAccountDeleteCatalogSource = sourceBetween(externalPublicApiCatalogSource, "id: 'account-delete'", "] satisfies ExternalPublicApiDocItemSeed[]")
assert.match(externalPublicAccountDeleteCatalogSource, /name: 'accountId'[\s\S]+required: true/)
assert.match(externalPublicAccountDeleteCatalogSource, /name: 'targetUsername'[\s\S]+required: false/)
assert.match(externalPublicAccountDeleteCatalogSource, /name: 'providerCode'[\s\S]+required: false/)
assert.doesNotMatch(externalPublicApiCatalogSource, /externalId/)

const externalPublicApiCatalog = getExternalPublicApiCatalog()
assert.deepEqual(externalPublicApiCatalog.items.map((item) => item.id), [
  'api-key-list',
  'api-key-add',
  'api-key-update',
  'api-key-delete',
  'route-strategy-list',
  'route-strategy-add',
  'route-strategy-update',
  'route-strategy-delete',
  'group-list',
  'group-add',
  'group-update',
  'group-delete',
  'account-list',
  'account-add',
  'account-update',
  'account-delete'
])
const externalIntegrationScopeValues = new Set<string>(externalIntegrationScopeOptions.map((item) => item.value))
for (const item of externalPublicApiCatalog.items) {
  const scope = item.scope
  assert.equal(item.status, 'available', `public API catalog item ${item.id} should be marked available`)
  if (typeof scope !== 'string') {
    throw new Error(`public API catalog item ${item.id} should expose a scope`)
  }
  assert.ok(scope.length > 0, `public API catalog item ${item.id} should expose a scope`)
  assert.ok(externalIntegrationScopeValues.has(scope), `public API catalog item ${item.id} should use a registered scope`)
  assert.ok(Array.isArray(item.responseFields), `public API catalog item ${item.id} should expose responseFields`)
  assert.ok(item.responseFields.length > 0, `public API catalog item ${item.id} should document response fields`)
  assert.notEqual(item.responseExample, undefined, `public API catalog item ${item.id} should expose a response example`)
}

const externalIntegrationsRoutesSource = readSource('modules/external-integrations/external-integrations.routes.ts')
assert.match(externalIntegrationsRoutesSource, /from '\.\/external-public-account-push\.mock\.js'/)
const accountPushSchemaSource = sourceBetween(externalIntegrationsRoutesSource, 'const accountPushSchema', 'const accountDeleteSchema')
assert.match(accountPushSchemaSource, /providerCode:\s*providerCodeSchema/)
assert.match(accountPushSchemaSource, /type:\s*publicAccountTypeSchema/)
assert.match(accountPushSchemaSource, /const accountUpdateSchema = z\.object\(\{[\s\S]+accountId:\s*z\.string\(\)\.trim\(\)\.min\(1\)\.max\(120\)/)
assert.match(accountPushSchemaSource, /apiKey:\s*z\.string\(\)\.trim\(\)\.min\(1\)\.max\(1000\)\.optional\(\)/)
assert.match(accountPushSchemaSource, /accountUpdateMutableFields\.some/)
assert.match(accountPushSchemaSource, /concurrencyLimit:\s*z\.number\(\)\.int\(\)\.min\(1\)/)
assert.match(accountPushSchemaSource, /priority:\s*z\.number\(\)\.int\(\)\.min\(0\)/)
assert.doesNotMatch(accountPushSchemaSource, /z\.coerce\.number/)
assert.doesNotMatch(accountPushSchemaSource, /externalId/)
const accountDeleteSchemaSource = sourceBetween(externalIntegrationsRoutesSource, 'const accountDeleteSchema', 'const accountListQuerySchema')
assert.match(accountDeleteSchemaSource, /accountId:\s*z\.string\(\)\.trim\(\)\.min\(1\)\.max\(120\)/)
assert.match(accountDeleteSchemaSource, /targetUsername:[\s\S]+optional\(\)/)
assert.match(accountDeleteSchemaSource, /providerCode:\s*providerCodeSchema\.optional\(\)/)
const groupUpdateSchemaSource = sourceBetween(externalIntegrationsRoutesSource, 'const groupUpdateMutableFields', 'const groupDeleteSchema')
assert.match(groupUpdateSchemaSource, /targetUsername:[\s\S]+optional\(\)/)
assert.match(groupUpdateSchemaSource, /groupUpdateMutableFields\.some/)
const groupDeleteSchemaSource = sourceBetween(externalIntegrationsRoutesSource, 'const groupDeleteSchema', 'const groupListQuerySchema')
assert.match(groupDeleteSchemaSource, /targetUsername:[\s\S]+optional\(\)/)
const apiKeyUpdateSchemaSource = sourceBetween(externalIntegrationsRoutesSource, 'const apiKeyUpdateMutableFields', 'const apiKeyDeleteSchema')
assert.match(apiKeyUpdateSchemaSource, /targetUsername:[\s\S]+optional\(\)/)
assert.match(apiKeyUpdateSchemaSource, /apiKeyUpdateMutableFields\.some/)
assert.match(apiKeyUpdateSchemaSource, /'routeStrategyId'/)
assert.match(apiKeyUpdateSchemaSource, /routeStrategyId:\s*z\.string\(\)\.trim\(\)\.min\(1\)\.max\(120\)\.optional\(\)/)
assert.doesNotMatch(apiKeyUpdateSchemaSource, /groupBindings|clientProfile|explicitHybridRouteRules/)
const apiKeyDeleteSchemaSource = sourceBetween(externalIntegrationsRoutesSource, 'const apiKeyDeleteSchema', 'const apiKeyListQuerySchema')
assert.match(apiKeyDeleteSchemaSource, /targetUsername:[\s\S]+optional\(\)/)
const apiKeyAddSchemaSource = sourceBetween(externalIntegrationsRoutesSource, 'const apiKeyAddSchema', 'const apiKeyUpdateMutableFields')
assert.match(apiKeyAddSchemaSource, /routeStrategyId:\s*z\.string\(\)\.trim\(\)\.min\(1\)\.max\(120\)/)
assert.match(apiKeyAddSchemaSource, /\}\)\.strict\(\)/)
assert.doesNotMatch(apiKeyAddSchemaSource, /groupBindings|clientProfile|explicitHybridRouteRules/)
assert.doesNotMatch(apiKeyAddSchemaSource, /z\.coerce\.number/)

const externalPublicAccountPushSource = readSource('modules/external-integrations/external-public-account-push.service.ts')
const externalPublicAccountPushTargetSource = readSource('modules/external-integrations/external-public-account-push.target.ts')
const externalPublicAccountPushPayloadSource = readSource('modules/external-integrations/external-public-account-push.payload.ts')
const externalPublicAccountPushSanitizeSource = readSource('modules/external-integrations/external-public-account-push.sanitize.ts')
const externalPublicAccountPushMockSource = readSource('modules/external-integrations/external-public-account-push.mock.ts')
const externalPublicBoundedIntegerSource = sourceFunctionBlock(externalPublicAccountPushPayloadSource, 'function boundedInteger')
assert.match(externalPublicBoundedIntegerSource, /typeof value !== 'number'/)
assert.doesNotMatch(externalPublicBoundedIntegerSource, /Number\(value\)/)
assert.doesNotMatch(externalPublicAccountPushSource, /normalizedText\(input\.providerCode\)\s*\|\|\s*'openai'/)
assert.doesNotMatch(externalPublicAccountPushSource, /function sanitizeAccount/)
assert.match(externalPublicAccountPushSanitizeSource, /export function sanitizeAccount/)
assert.match(externalPublicAccountPushSanitizeSource, /export function publicAccountListResponse/)
assert.match(externalPublicAccountPushSanitizeSource, /export function publicApiKeyResponse/)
assert.doesNotMatch(externalPublicAccountPushSanitizeSource, /runInDatabaseTransaction|getBusinessDatabase|createAccount|updateAccount|deleteAccount/)
assert.match(externalPublicAccountPushSource, /from '\.\/external-public-account-push\.target\.js'/)
assert.doesNotMatch(externalPublicAccountPushSource, /function ensureTargetSystemAccount|function ensureTargetGroup|function findPublicTarget|function requirePublicTarget/)
assert.match(externalPublicAccountPushTargetSource, /export function ensureTargetSystemAccount/)
assert.match(externalPublicAccountPushTargetSource, /export function ensureTargetGroup/)
assert.match(externalPublicAccountPushTargetSource, /export function targetAccess/)
assert.doesNotMatch(externalPublicAccountPushTargetSource, /runInDatabaseTransaction|getBusinessDatabase|createAccount|updateAccount|deleteAccountWithRelatedCleanup|deleteApiKeyWithRelatedCleanup|createApiKeyRecord/)
assert.doesNotMatch(externalPublicAccountPushSource, /export function mockPublic/)
assert.doesNotMatch(externalPublicAccountPushSource, /mock_system_account_huanmin/)
assert.match(externalPublicAccountPushMockSource, /export function mockPublicWelfareAccountPush/)
assert.match(externalPublicAccountPushMockSource, /mock_system_account_huanmin/)

const apiKeyRepositorySource = readSource('storage/api-key.repository.ts')
assert.doesNotMatch(apiKeyRepositorySource, /'scopes_json'/)
assert.doesNotMatch(apiKeyRepositorySource, /JSON\.stringify\(\[\]\)/)

const businessSchemaSource = readSource('storage/schema/business-schema.ts')
const apiKeysSchemaSource = sourceBetween(businessSchemaSource, 'CREATE TABLE IF NOT EXISTS api_keys', 'CREATE INDEX IF NOT EXISTS idx_api_keys_route_strategy')
assert.doesNotMatch(apiKeysSchemaSource, /scopes_json/)

const coreFunctionDocSource = readProjectFile('docs/functions/核心功能设计.md')
assert.doesNotMatch(coreFunctionDocSource, /`scopes_json`：保留字段/)

const modelChecksRepositorySource = readSource('storage/model-checks.repository.ts')
assert.match(modelChecksRepositorySource, /providerCode: string/)
assert.doesNotMatch(modelChecksRepositorySource, /providerCode\s*\?\?\s*'openai'/)
const modelChecksServiceSource = readSource('modules/model-checks/model-checks.service.ts')
const modelChecksGatewayProbeSource = readSource('modules/model-checks/model-checks-gateway-probe.ts')
assert.match(modelChecksServiceSource, /from '\.\/model-checks-gateway-probe\.js'/)
assert.doesNotMatch(modelChecksServiceSource, /probeMaxAttempts|diagnosticAttemptSignal\(signal,\s*timeoutMs\)|diagnosticAccountTestGatewaySettingsOverride\(undefined,\s*timeoutMs\)/)
assert.match(modelChecksGatewayProbeSource, /from '\.\.\/gateway\/testing\/memory-gateway-http\.js'/)
assert.match(modelChecksGatewayProbeSource, /probeMaxAttempts\s*=\s*accountDiagnosticRetryTimeoutMs\.length/)
assert.match(modelChecksGatewayProbeSource, /diagnosticAccountTestGatewaySettingsOverride\(undefined,\s*timeoutMs\)/)
assert.match(modelChecksGatewayProbeSource, /diagnosticAttemptSignal\(signal,\s*timeoutMs\)/)
assert.match(modelChecksGatewayProbeSource, /probeRetryDelayMs/)
assert.match(modelChecksGatewayProbeSource, /attemptUpstreamStatusCodes/)
assert.match(modelChecksGatewayProbeSource, /upstreamStatusCode/)
assert.match(modelChecksGatewayProbeSource, /requests-per-minute/)
assert.match(modelChecksGatewayProbeSource, /createMemoryGatewayRequest\(/)
assert.doesNotMatch(modelChecksGatewayProbeSource, /from '\.\.\/accounts\/account-test\.service\.js'/)
assert.doesNotMatch(modelChecksGatewayProbeSource, /class MemoryGatewayRequest/)
assert.doesNotMatch(modelChecksGatewayProbeSource, /acquireModelCheckProbeSlot|model-checks-probe-scheduler|probeMaxInFlight|probeMinStartIntervalMs/, '模型检测正常探针不应再做本机并发或启动间隔限制')
assert.doesNotMatch(modelChecksGatewayProbeSource, /probeRateLimitRetryDelayMs/, '模型检测重试延迟不能依赖上游状态码或限流类型判断')
assert.doesNotMatch(modelChecksGatewayProbeSource, /45_000/, '模型检测探针不能回退旧 45s 超时')

const responseInspectionPoliciesRoutesSource = readSource('modules/response-inspection-policies/response-inspection-policies.routes.ts')
const responseInspectionPolicyBodySchemaSource = sourceBetween(responseInspectionPoliciesRoutesSource, 'const policyBodySchema', 'responseInspectionPoliciesRouter.get')
assert.match(responseInspectionPolicyBodySchemaSource, /priority:\s*z\.number\(\)\.int\(\)\.min\(1\)\.max\(9999\)\.optional\(\)/)
assert.doesNotMatch(responseInspectionPolicyBodySchemaSource, /avoidanceTtlSeconds/)
assert.doesNotMatch(responseInspectionPolicyBodySchemaSource, /z\.coerce\.number/)

const responseInspectionPolicyRepositorySource = readSource('storage/response-inspection-policy.repository.ts')
assert.match(responseInspectionPolicyRepositorySource, /maxManagementResponseInspectionPolicies/)
assert.doesNotMatch(responseInspectionPolicyRepositorySource, /avoidanceTtlSeconds/)

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

const gatewayBodySource = readSource('modules/gateway/upstream/body.ts')
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

const gatewayStreamSource = readSource('modules/gateway/response/stream.ts')
assert.match(gatewayStreamSource, /writeResult\.logLevel\s*===\s*'warn'/)
assert.match(gatewayStreamSource, /responseBackpressureWarnThresholdMs/)

const gatewayUpstreamSource = readSource('modules/gateway/upstream/request.ts')
const upstreamRequestTimeoutSource = sourceFunctionBlock(gatewayUpstreamSource, 'export function upstreamRequestTimeoutMs')
const gatewayTimeoutProfileSource = readSource('modules/gateway/policy/timeout-profile.ts')
const buildGatewayTimeoutProfileSource = sourceFunctionBlock(gatewayTimeoutProfileSource, 'export function gatewayTimeoutProfileForLane')
assert.match(buildGatewayTimeoutProfileSource, /settings\.textFirstResponseTimeoutSeconds/, '文本首包等待上限必须进入统一 timeout profile')
assert.match(upstreamRequestTimeoutSource, /return profile\.firstResponseTimeoutMs/, '上游首个响应等待必须消费统一 timeout profile')
assert.doesNotMatch(upstreamRequestTimeoutSource, /isEffectiveOpenAIStreamRequest|streamCircuitBreakerEnabled/, '非流式请求也必须应用首包等待上限，不能只在流式熔断开启时生效')

const releaseStartScriptSource = readFileSync(resolve(backendSrcDirectory, '../../deploy/start.sh'), 'utf8')
assert.match(releaseStartScriptSource, /JUHE_AI_LOG_CONSOLE_ENABLED="\$\{JUHE_AI_LOG_CONSOLE_ENABLED:-false\}"/)

const oauthRoutesSource = readSource('modules/openai-oauth/openai-oauth.routes.ts')
assert.doesNotMatch(oauthRoutesSource, /refreshOpenAIOAuthUsageSnapshot/)

const gatewayRoutesSource = readSource('modules/gateway/routes.ts')
assert.match(gatewayRoutesSource, /persistOpenAICodexHeadersIfNeeded\(account,\s*upstreamResponse\.headers,\s*gatewayUsageContext\.trafficSource\)/)
assert.match(gatewayRoutesSource, /!isGatewayForcedDownstreamClose\(res\)/, '网关主动关闭非流式半截响应时不应被 close 监听误判为客户端取消')
assert.match(gatewayFailureDispatchSource, /usageContext\.trafficSource === 'gateway' \? 'gateway_error' : usageContext\.trafficSource/)

const cooldownRetestRepositorySource = readSource('storage/account-cooldown-retest.repository.ts')
assert.match(repositoriesSource, /from '\.\/account-cooldown-retest\.repository\.js'/)
assert.match(cooldownRetestRepositorySource, /status IN \('temporary_unavailable', 'rate_limited'\)/)
assert.match(cooldownRetestRepositorySource, /rate_limited/)
assert.match(cooldownRetestRepositorySource, /recordCooldownAccountRetestFailure/)
assert.match(cooldownRetestRepositorySource, /cooldownRetestObservationElapsedSeconds/)
assert.match(cooldownRetestRepositorySource, /cooldown_retest_long_term_unavailable/)
assert.doesNotMatch(cooldownRetestRepositorySource, /SET status = 'error'[\s\S]+cooldown_retest_max_recovery_exceeded/)

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
