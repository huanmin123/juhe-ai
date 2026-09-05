import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import {
  AccountManagementPatchRevisionConflictError,
  patchAccountManagementAsync
} from '../../storage/account-management-patch.repository.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { operationMode, runLoggedOperationAsync, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import { accountGroupSchema } from './account-request.schemas.js'

export function registerAccountGroupBindingRoutes(router: Router): void {
  router.post('/:id/group', async (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const parsed = accountGroupSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest('绑定分组参数无效'))
      return
    }

    try {
      const patched = await runLoggedOperationAsync(async () => {
        const patched = await patchAccountManagementAsync(req.params.id, parsed.data, requestAccess)
        if (!patched) {
          throw new Error('账户不存在、授权已失效或分组不可用')
        }
        const groupChange = patched.changes.find((change) => change.field === 'groupId')
        return {
          result: patched,
          log: groupChange ? {
            operationScopeSystemAccountId: patched.ownerSystemAccountId,
            mode: operationMode(requestAccess),
            module: 'accounts',
            action: 'bind_group',
            operationKey: 'accounts.bind_group',
            resourceType: 'account',
            resourceId: patched.id,
            resourceName: patched.name,
            summary: `绑定账户分组：${patched.name}`,
            changes: [
              safeChange('groupId', '绑定分组', groupChange.before, groupChange.after)
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
      res.status(400).json(badRequest(error instanceof Error ? error.message : '绑定账户分组失败'))
    }
  })
}
