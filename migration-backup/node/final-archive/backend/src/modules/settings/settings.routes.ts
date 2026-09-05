import { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import { getSettingsAsync, listGlobalSettingsAsync, updateGlobalSettingsAsync, updateSettingsAsync } from '../../storage/repositories.js'
import {
  getManagementSettingsSectionAsync,
  managementSettingsSectionCatalog,
  updateManagementSettingsSectionAsync,
  type ManagementSettingsSectionKey
} from '../../storage/settings.repository.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { diffSafeFields, runLoggedOperationAsync } from '../operation-logs/operation-log.service.js'

export const settingsRouter = Router()

settingsRouter.get('/global', requireAdmin, async (_req, res, next) => {
  try {
    res.json(ok(await listGlobalSettingsAsync()))
  } catch (error) {
    next(error)
  }
})

settingsRouter.patch('/global', requireAdmin, async (req, res) => {
  try {
    const before = await listGlobalSettingsAsync()
    const settings = await runLoggedOperationAsync(async () => {
      const settings = await updateGlobalSettingsAsync(req.body as Record<string, unknown>)
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

settingsRouter.get('/sections/:sectionKey', requireAdmin, async (req, res, next) => {
  try {
    const sectionKey = parseSectionKey(req.params.sectionKey)
    res.json(ok({ sectionKey, values: await getManagementSettingsSectionAsync(sectionKey) }))
  } catch (error) {
    if (error instanceof InvalidSettingsSectionError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    next(error)
  }
})

settingsRouter.patch('/sections/:sectionKey', requireAdmin, async (req, res) => {
  try {
    const sectionKey = parseSectionKey(req.params.sectionKey)
    const body = parseSectionPatch(req.body)
    const before = await getManagementSettingsSectionAsync(sectionKey)
    const values = await runLoggedOperationAsync(async () => {
      const values = await updateManagementSettingsSectionAsync(sectionKey, body)
      return {
        result: values,
        log: {
          mode: 'admin',
          module: 'settings',
          action: sectionKey === 'brand' ? 'update_global' : 'update_settings',
          operationKey: sectionKey === 'brand' ? 'settings.update_global' : 'settings.update',
          resourceType: sectionKey === 'brand' ? 'global_settings' : 'system_settings',
          resourceId: sectionKey,
          resourceName: `设置分区 ${sectionKey}`,
          summary: `更新设置分区 ${sectionKey}`,
          visibilityScope: 'all_users',
          detailLevel: 'summary',
          changes: diffSafeFields(before, values, Object.fromEntries(Object.keys(body).map((key) => [key, key])))
        }
      }
    }, req)
    res.json(ok({ sectionKey, values }))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '设置分区参数无效'))
  }
})

settingsRouter.get('/', requireAdmin, async (_req, res, next) => {
  try {
    res.json(ok(await getSettingsAsync()))
  } catch (error) {
    next(error)
  }
})

settingsRouter.patch('/', requireAdmin, async (req, res) => {
  const body = req.body as Record<string, unknown>
  try {
    const before = await getSettingsAsync()
    const settings = await runLoggedOperationAsync(async () => {
      const settings = await updateSettingsAsync(body)
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
          changes: diffSafeFields(before, settings, Object.fromEntries(Object.keys(body).map((key) => [key, key])))
        }
      }
    }, req)
    res.json(ok(settings))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '系统设置参数无效'))
  }
})

class InvalidSettingsSectionError extends Error {}

function parseSectionKey(value: string): ManagementSettingsSectionKey {
  if (!Object.prototype.hasOwnProperty.call(managementSettingsSectionCatalog, value)) {
    throw new InvalidSettingsSectionError(`未知设置分区：${value}`)
  }
  return value as ManagementSettingsSectionKey
}

function parseSectionPatch(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value) || Object.getPrototypeOf(value) !== Object.prototype) {
    throw new InvalidSettingsSectionError('设置分区更新必须是普通 JSON 对象')
  }
  return value as Record<string, unknown>
}
