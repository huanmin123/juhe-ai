import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import {
  AccountManagementPatchRevisionConflictError,
  patchAccountManagementAsync
} from '../../storage/account-management-patch.repository.js'
import { AccountTagInUseError, deleteAccountTagAsync, listAccountTagsAsync } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { operationMode, runLoggedOperationAsync, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import { accountTagsUpdateSchema } from './account-request.schemas.js'

export function registerAccountTagsRoutes(router: Router): void {
  router.get('/tags', async (req, res, next) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    try {
      res.json(ok(await listAccountTagsAsync(getRequestAccessScope(scopeQuery.data.systemAccountId))))
    } catch (error) {
      next(error)
    }
  })

  router.delete('/tags/:tagId', async (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    try {
      if (!(await deleteAccountTagAsync(req.params.tagId, getRequestAccessScope(scopeQuery.data.systemAccountId)))) {
        res.status(404).json({ message: '标签不存在' })
        return
      }
      res.status(204).send()
    } catch (error) {
      if (error instanceof AccountTagInUseError) {
        res.status(400).json(badRequest(error.message))
        return
      }
      res.status(400).json(badRequest(error instanceof Error ? error.message : '删除账户标签失败'))
    }
  })

  router.patch('/:id/tags', async (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const parsed = accountTagsUpdateSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '账户标签参数无效'))
      return
    }
    try {
      const patched = await runLoggedOperationAsync(async () => {
        const patched = await patchAccountManagementAsync(req.params.id, parsed.data, requestAccess)
        if (!patched) {
          throw new Error('账户不存在')
        }
        const tagsChange = patched.changes.find((change) => change.field === 'tags')
        return {
          result: patched,
          log: tagsChange ? {
            operationScopeSystemAccountId: patched.ownerSystemAccountId,
            mode: operationMode(requestAccess),
            module: 'accounts',
            action: 'update_tags',
            operationKey: 'accounts.update_tags',
            resourceType: 'account',
            resourceId: patched.id,
            resourceName: patched.name,
            summary: `更新账户标签：${patched.name}`,
            changes: [
              safeChange('tags', '标签', tagsChange.before, tagsChange.after)
            ],
            viewers: viewer(patched.ownerSystemAccountId, 'resource_owner')
          } : undefined
        }
      }, req)
      res.json(ok({
        id: patched.id,
        configRevision: patched.configRevision,
        changedFields: patched.changedFields
      }))
    } catch (error) {
      if (error instanceof AccountManagementPatchRevisionConflictError) {
        res.status(409).json(badRequest(error.message))
        return
      }
      if (error instanceof Error && error.message === '账户不存在') {
        res.status(404).json({ message: '账户不存在' })
        return
      }
      res.status(400).json(badRequest(error instanceof Error ? error.message : '更新账户标签失败'))
    }
  })
}
