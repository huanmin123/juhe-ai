// Curated from official OpenAI model, pricing, and deprecation docs on 2026-05-08.
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
