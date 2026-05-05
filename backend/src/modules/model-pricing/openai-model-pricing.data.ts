// Generated from https://raw.githubusercontent.com/Wei-Shaw/model-price-repo/main/model_prices_and_context_window.json at 2026-05-01T17:12:56Z.
// Values follow LiteLLM/model-price-repo field names: token prices are USD per token.

import { openAIGPT4ModelPricingData } from './openai-model-pricing.gpt4.data.js'
import { openAIGPT5ModelPricingData } from './openai-model-pricing.gpt5.data.js'
import { openAIMediaRealtimeModelPricingData } from './openai-model-pricing.media-realtime.data.js'
import { openAIReasoningModelPricingData } from './openai-model-pricing.reasoning.data.js'

export const openAIModelPricingData = [
  ...openAIGPT4ModelPricingData,
  ...openAIGPT5ModelPricingData,
  ...openAIMediaRealtimeModelPricingData,
  ...openAIReasoningModelPricingData
] as const
