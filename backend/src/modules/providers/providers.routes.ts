import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { listProviders } from '../../storage/repositories.js'

export const providersRouter = Router()

providersRouter.get('/', (_req, res) => {
  res.json(ok(listProviders()))
})
