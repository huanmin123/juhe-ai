import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { getSettings, listGlobalSettings, updateGlobalSettings, updateSettings } from '../../storage/repositories.js'
import { requireAdmin } from '../auth/auth.middleware.js'

export const settingsRouter = Router()

settingsRouter.get('/public', (_req, res) => {
  res.json(ok(listGlobalSettings()))
})

settingsRouter.get('/global', requireAdmin, (_req, res) => {
  res.json(ok(listGlobalSettings()))
})

settingsRouter.patch('/global', requireAdmin, (req, res) => {
  res.json(ok(updateGlobalSettings(req.body as Record<string, unknown>)))
})

settingsRouter.get('/', (_req, res) => {
  res.json(ok(getSettings()))
})

settingsRouter.patch('/', (req, res) => {
  res.json(ok(updateSettings(req.body as Record<string, unknown>)))
})
