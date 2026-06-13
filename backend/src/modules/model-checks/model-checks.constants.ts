export const supportedModels = ['gpt-5.5', 'gpt-5.4'] as const
export const supportedModelSet = new Set<string>(supportedModels)
export type SupportedModel = typeof supportedModels[number]

export const defaultModel = 'gpt-5.5'
export const defaultProfile = 'full'
export const probeSetVersion = 'openai-model-check-v2-strong-retry'
export const responsesPath = '/v1/responses'
export const modelsPath = '/v1/models'
export const distributionSampleCount = 5
