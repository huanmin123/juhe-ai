import type { RawModelPricing } from './provider-driver.types.js'

const textGenerationProtocols = ['chat_completions', 'generate_content', 'stream_generate_content', 'count_tokens'] as const

export const geminiModelPricingData: RawModelPricing[] = [
  {
    model: 'gemini-3.5-flash',
    mode: 'chat',
    catalog_order: 10,
    input_cost_per_token: 0,
    output_cost_per_token: 0,
    max_tokens: 1_048_576,
    supported_api_protocols: textGenerationProtocols,
    supports_prompt_caching: true,
    catalog_visible: true
  },
  {
    model: 'gemini-3.1-flash-lite',
    mode: 'chat',
    catalog_order: 20,
    input_cost_per_token: 0,
    output_cost_per_token: 0,
    max_tokens: 1_048_576,
    supported_api_protocols: textGenerationProtocols,
    supports_prompt_caching: true,
    catalog_visible: true
  },
  {
    model: 'gemini-2.5-flash',
    mode: 'chat',
    catalog_order: 30,
    input_cost_per_token: 0,
    output_cost_per_token: 0,
    max_tokens: 1_048_576,
    supported_api_protocols: textGenerationProtocols,
    supports_prompt_caching: true,
    catalog_visible: true
  },
  {
    model: 'gemini-2.5-pro',
    mode: 'chat',
    catalog_order: 40,
    input_cost_per_token: 0,
    output_cost_per_token: 0,
    max_tokens: 1_048_576,
    supported_api_protocols: textGenerationProtocols,
    supports_prompt_caching: true,
    catalog_visible: true
  },
  {
    model: 'gemini-embedding-001',
    mode: 'embedding',
    catalog_order: 100,
    input_cost_per_token: 0,
    output_cost_per_token: 0,
    supported_api_protocols: ['embed_content'],
    supports_prompt_caching: false,
    catalog_visible: true
  }
]
