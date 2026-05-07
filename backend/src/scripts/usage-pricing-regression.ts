import assert from 'node:assert/strict'

import {
  inspectOpenAIStreamText,
  parseOpenAIUsageFromJsonBuffer
} from '../modules/gateway/openai-gateway-usage.js'
import { estimateProviderCostUsd } from '../modules/model-pricing/model-pricing.service.js'
import { usageSummaryFromAggregate } from '../storage/usage-stats-helpers.js'

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
assert.deepEqual(defined(chatStreamInspection.usage), defined(chatUsage))

const gpt41Cost = estimateProviderCostUsd({
  providerCode: 'openai',
  model: 'gpt-4.1',
  inputTokens: 1000,
  outputTokens: 200,
  cacheReadTokens: 300
})
assert.equal(gpt41Cost, 0.00315)

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

const cachedUsageSummary = usageSummaryFromAggregate({
  request_count: 1,
  input_tokens: 1000,
  output_tokens: 200,
  cache_read_tokens: 300,
  total_cost: 0.00315,
  last_used_at: null
})
assert.equal(cachedUsageSummary.totalTokens, 1200)

console.log('usage-pricing-regression passed')
