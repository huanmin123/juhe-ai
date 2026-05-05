import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { getSettings, listGlobalSettings, updateGlobalSettings, updateSettings } from '../../storage/repositories.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { clearAuditLogSettingsCache } from '../audit-logs/audit-log-settings.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'

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
  const settings = updateSettings(req.body as Record<string, unknown>)
  clearGatewayRuntimeCache()
  clearAuditLogSettingsCache()
  res.json(ok(settings))
})
