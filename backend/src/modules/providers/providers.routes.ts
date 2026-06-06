import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { listProviders } from '../../storage/repositories.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { listProviderModelPricing } from '../model-pricing/model-pricing.service.js'

export const providersRouter = Router()

interface ProviderModelOption {
  providerCode: string
  model: string
}

providersRouter.get('/', requireAdmin, (_req, res) => {
  res.json(ok(listProviders()))
})

providersRouter.get('/options', (_req, res) => {
  res.json(ok(listProviders().filter((provider) => provider.enabled)))
})

providersRouter.get('/models/options', (_req, res) => {
  const options = dedupeProviderModelOptions(
    listProviders()
      .filter((provider) => provider.enabled)
      .flatMap((provider) => listProviderModelPricing(provider.code).map((item) => ({
        providerCode: item.providerCode,
        model: item.model
      })))
  )
  res.json(ok(options))
})

providersRouter.get('/:code/models', (req, res) => {
  const provider = listProviders().find((item) => item.code === req.params.code)
  if (!provider) {
    res.status(404).json({ message: '供应商不存在' })
    return
  }

  res.json(ok(listProviderModelPricing(provider.code)))
})

function dedupeProviderModelOptions(options: ProviderModelOption[]): ProviderModelOption[] {
  const seenModels = new Set<string>()
  const result: ProviderModelOption[] = []
  for (const option of options) {
    const model = option.model.trim()
    if (!model) continue
    const normalizedModel = model.toLowerCase()
    if (seenModels.has(normalizedModel)) continue
    seenModels.add(normalizedModel)
    result.push({ providerCode: option.providerCode, model })
  }
  return result
}
