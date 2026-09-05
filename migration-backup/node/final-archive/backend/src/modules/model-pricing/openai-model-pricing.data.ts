// Curated from official OpenAI model, pricing, and deprecation docs on 2026-07-09.
// Values follow LiteLLM/model-price-repo field names: token prices are USD per token.

import { openAIGPT4ModelPricingData } from './openai-model-pricing.gpt4.data.js'
import { openAIGPT5ModelPricingData } from './openai-model-pricing.gpt5.data.js'
import { openAIImageModelPricingData } from './openai-model-pricing.image.data.js'
import { openAIReasoningModelPricingData } from './openai-model-pricing.reasoning.data.js'

export const openAIModelPricingData = [
  ...openAIGPT4ModelPricingData,
  ...openAIGPT5ModelPricingData,
  ...openAIImageModelPricingData,
  ...openAIReasoningModelPricingData
] as const
