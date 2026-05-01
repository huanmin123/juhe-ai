import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { listProviders } from '../../storage/repositories.js'
import { listProviderModelPricing } from '../model-pricing/model-pricing.service.js'

export const providersRouter = Router()

providersRouter.get('/', (_req, res) => {
  res.json(ok(listProviders()))
})

providersRouter.get('/:code/models', (req, res) => {
  const provider = listProviders().find((item) => item.code === req.params.code)
  if (!provider) {
    res.status(404).json({ message: 'Provider not found' })
    return
  }

  res.json(ok(listProviderModelPricing(provider.code)))
})
