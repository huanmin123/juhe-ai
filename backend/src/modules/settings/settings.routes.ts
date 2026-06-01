import { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import { getSettings, listGlobalSettings, updateGlobalSettings, updateSettings } from '../../storage/repositories.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { diffSafeFields, runLoggedOperation } from '../operation-logs/operation-log.service.js'

export const settingsRouter = Router()

settingsRouter.get('/global', requireAdmin, (_req, res) => {
  res.json(ok(listGlobalSettings()))
})

settingsRouter.patch('/global', requireAdmin, (req, res) => {
  const before = listGlobalSettings()
  try {
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
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '全局设置参数无效'))
  }
})

settingsRouter.get('/', requireAdmin, (_req, res) => {
  res.json(ok(getSettings()))
})

settingsRouter.patch('/', requireAdmin, (req, res) => {
  const body = req.body as Record<string, unknown>
  const before = getSettings()
  try {
    const settings = runLoggedOperation(() => {
      const settings = updateSettings(body)
      return {
        result: settings,
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
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '系统设置参数无效'))
  }
})
