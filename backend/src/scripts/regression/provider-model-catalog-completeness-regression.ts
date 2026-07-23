import assert from 'node:assert/strict'

import { listProviderModelPricing } from '../../modules/model-pricing/model-pricing.service.js'

const providerCodes = ['gpt', 'anthropic', 'gemini', 'deepseek', 'glm', 'xai']
const failures: string[] = []

for (const providerCode of providerCodes) {
  for (const model of listProviderModelPricing(providerCode).filter((item) => item.catalogVisible !== false)) {
    const label = `${providerCode}/${model.model}`
    if (!model.releaseDate) failures.push(`${label}: releaseDate`)
    if (!model.supportedApiProtocols.length) failures.push(`${label}: supportedApiProtocols`)
    if (!model.inputModalities.length) failures.push(`${label}: inputModalities`)
    if (!model.outputModalities.length) failures.push(`${label}: outputModalities`)
    if (
      model.inputUsdPer1M === undefined
      && model.outputUsdPer1M === undefined
      && model.imageInputUsdPer1M === undefined
      && model.imageOutputUsdPer1M === undefined
      && model.audioInputUsdPer1M === undefined
      && model.audioOutputUsdPer1M === undefined
      && model.outputUsdPerImage === undefined
      && Object.keys(model.serviceTierPrices ?? {}).length === 0
    ) {
      failures.push(`${label}: pricing`)
    }
  }
}

assert.deepEqual(failures, [], `内置可见模型元数据不完整：${failures.join(', ')}`)
console.log('provider model catalog completeness regression passed')
