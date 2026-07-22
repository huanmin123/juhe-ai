import type { Router } from 'express'

import { badRequest } from '../../shared/http.js'
import { findAccountSummaryAsync, returnAccountAuthorizationInstanceForGranteeAsync } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { mutationGuard, normalizedText, queryField } from '../deduplication/mutation-guard.middleware.js'
import { operationMode, ownerTarget, runLoggedOperationAsync, safeChange, viewer, viewers } from '../operation-logs/operation-log.service.js'

export function registerAccountAuthorizationReturnRoutes(router: Router): void {
  router.post('/:id/return-authorization', mutationGuard({
    operationKey: 'accounts.return_authorization',
    scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
    fingerprint: (req) => ({
      accountId: normalizedText(req.params.id),
      grantee: normalizedText(queryField(req, 'systemAccountId'))
    })
  }), async (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const before = await findAccountSummaryAsync(req.params.id, requestAccess)
    try {
      await runLoggedOperationAsync(async () => {
        const authorization = await returnAccountAuthorizationInstanceForGranteeAsync(req.params.id, requestAccess)
        if (!authorization) {
          throw new Error('授权账户不存在或不可归还')
        }
        const resourceName = before?.name ?? authorization.resource_id
        return {
          result: authorization,
          log: {
            operationScopeSystemAccountId: authorization.grantee_system_account_id,
            mode: operationMode(requestAccess),
            module: 'authorizations',
            action: 'return',
            operationKey: 'accounts.return_authorization',
            resourceType: 'authorization',
            resourceId: authorization.id,
            resourceName,
            summary: `归还授权账户：${resourceName}`,
            changes: [safeChange('returned', '归还授权账户', false, true)],
            targets: [
              ownerTarget({
                targetType: authorization.resource_type,
                targetId: authorization.resource_id,
                ownerSystemAccountId: authorization.resource_owner_system_account_id,
                relation: 'owner'
              }),
              ownerTarget({
                targetType: 'system_account',
                targetId: authorization.grantee_system_account_id,
                ownerSystemAccountId: authorization.grantee_system_account_id,
                relation: 'grantee'
              })
            ],
            viewers: viewers(
              viewer(authorization.resource_owner_system_account_id, 'authorization_owner'),
              viewer(authorization.grantee_system_account_id, 'authorization_grantee')
            )
          }
        }
      }, req)
      res.status(204).send()
    } catch (error) {
      if (error instanceof Error && error.message === '授权账户不存在或不可归还') {
        res.status(404).json({ message: '授权账户不存在或不可归还' })
        return
      }
      res.status(400).json(badRequest(error instanceof Error ? error.message : '归还授权账户失败'))
    }
  })
}
