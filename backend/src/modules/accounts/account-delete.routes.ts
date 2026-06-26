import { type Router } from 'express'

import { badRequest } from '../../shared/http.js'
import { deleteAccountWithRelatedCleanupAsync, findAccountSummaryAsync } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { operationMode, resolveOperationOwner, runLoggedOperationAsync, safeChange, viewer } from '../operation-logs/operation-log.service.js'

export function registerAccountDeleteRoutes(router: Router): void {
  router.delete('/:id', async (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const before = await findAccountSummaryAsync(req.params.id, requestAccess)
    const ownerSystemAccountId = resolveOperationOwner(before as unknown as Record<string, unknown> | undefined, requestAccess)
    try {
      await runLoggedOperationAsync(async () => {
        const deleteResult = await deleteAccountWithRelatedCleanupAsync(req.params.id, requestAccess)
        if (!deleteResult.deleted) {
          throw new Error('账户不存在')
        }
        return {
          result: true,
          log: {
            operationScopeSystemAccountId: ownerSystemAccountId,
            mode: operationMode(requestAccess),
            module: 'accounts',
            action: 'delete',
            operationKey: 'accounts.delete',
            resourceType: 'account',
            resourceId: req.params.id,
            resourceName: before?.name ?? req.params.id,
            summary: `删除 AI 账户：${before?.name ?? req.params.id}`,
            changes: [safeChange('deleted', '删除状态', false, true)],
            viewers: viewer(ownerSystemAccountId, 'resource_owner')
          }
        }
      }, req)
    } catch (error) {
      if (error instanceof Error && error.message === '账户不存在') {
        res.status(404).json({ message: '账户不存在' })
        return
      }
      if (error instanceof Error && error.message === '授权账户请使用归还操作') {
        res.status(400).json(badRequest('授权账户请使用归还操作'))
        return
      }
      throw error
    }
    res.status(204).send()
  })
}
