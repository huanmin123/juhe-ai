import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { getSettings, updateSettings } from '../../storage/repositories.js'

export const settingsRouter = Router()

settingsRouter.get('/', (_req, res) => {
  res.json(ok(getSettings()))
})

settingsRouter.patch('/', (req, res) => {
  res.json(ok(updateSettings(req.body as Record<string, unknown>)))
})
