import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { getSettings, listGlobalSettings, listPublicGlobalSettings, updateGlobalSettings, updateSettings } from '../../storage/repositories.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'
import { diffSafeFields, runLoggedOperation } from '../operation-logs/operation-log.service.js'

export const settingsRouter = Router()

settingsRouter.get('/public', (_req, res) => {
  res.json(ok(listPublicGlobalSettings()))
})

settingsRouter.get('/global', requireAdmin, (_req, res) => {
  res.json(ok(listGlobalSettings()))
})

settingsRouter.patch('/global', requireAdmin, (req, res) => {
  const before = listGlobalSettings()
  const settings = runLoggedOperation(() => {
    const settings = updateGlobalSettings(req.body as Record<string, unknown>)
    return {
      result: settings,
      log: {
        mode: 'admin',
        module: 'settings',
        action: 'update_global',
        operationKey: 'settings.update_global',
        resourceType: 'global_settings',
        resourceId: 'global',
        resourceName: '全局品牌设置',
        summary: '更新全局品牌设置',
        visibilityScope: 'all_users',
        detailLevel: 'summary',
        changes: diffSafeFields(before, settings, {
          appName: '系统名称',
          appIcon: '系统图标'
        })
      }
    }
  }, req)
  res.json(ok(settings))
})

settingsRouter.get('/', requireAdmin, (_req, res) => {
  res.json(ok(getSettings()))
})

settingsRouter.patch('/', requireAdmin, (req, res) => {
  const body = req.body as Record<string, unknown>
  const before = getSettings()
  const settings = runLoggedOperation(() => {
    const settings = updateSettings(body)
    return {
      result: settings,
      afterCommit: clearGatewayRuntimeCache,
      log: {
        mode: 'admin',
        module: 'settings',
        action: 'update_settings',
        operationKey: 'settings.update',
        resourceType: 'system_settings',
        resourceId: 'system',
        resourceName: '系统运行设置',
        summary: '更新系统运行设置',
        visibilityScope: 'all_users',
        detailLevel: 'summary',
        changes: diffSafeFields(before, settings, Object.fromEntries(Object.keys(body).map((key) => [key, key]))),
        force: Object.prototype.hasOwnProperty.call(body, 'operationLogEnabled')
      }
    }
  }, req)
  res.json(ok(settings))
})
