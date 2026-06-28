import type { Router } from 'express'

import type { AccountSummary } from '../../domain/types.js'
import { badRequest, ok } from '../../shared/http.js'
import { findAccountForTestAsync, setAccountGroupAsync } from '../../storage/repositories.js'
import { getRequestAccessScope, type RequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { operationMode, resolveOperationOwner, runLoggedOperationAsync, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import { accountGroupSchema } from './account-request.schemas.js'
import { sanitizeAccountResponse } from './account-response-sanitizer.js'

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

    const before = await findAccountForTestAsync(req.params.id, requestAccess)
    try {
      const account = await runLoggedOperationAsync(async () => {
        const account = await setAccountGroupAsync(req.params.id, parsed.data.groupId, requestAccess)
        if (!account) {
          throw new Error('账户不存在、授权已失效或分组不可用')
        }
        const ownerSystemAccountId = authorizedLocalOperationOwner(account, requestAccess)
          ?? resolveOperationOwner(account as unknown as Record<string, unknown>, requestAccess)
        return {
          result: account,
          log: {
            operationScopeSystemAccountId: ownerSystemAccountId,
            mode: operationMode(requestAccess),
            module: 'accounts',
            action: 'bind_group',
            operationKey: 'accounts.bind_group',
            resourceType: 'account',
            resourceId: account.id,
            resourceName: account.name,
            summary: `绑定账户分组：${account.name}`,
            changes: [
              safeChange('groupId', '绑定分组', before?.boundGroupId, account.boundGroupId)
            ],
            viewers: viewer(ownerSystemAccountId, 'resource_owner')
          }
        }
      }, req)
      res.json(ok(sanitizeAccountResponse(account)))
    } catch (error) {
      res.status(400).json(badRequest(error instanceof Error ? error.message : '绑定账户分组失败'))
    }
  })
}

function authorizedLocalOperationOwner(account: AccountSummary, access?: RequestAccessScope): string | undefined {
  return account.accessType === 'authorized' ? effectiveRequestSystemAccountId(access) : undefined
}

function effectiveRequestSystemAccountId(access?: RequestAccessScope): string | undefined {
  return access?.systemAccountFilterId?.trim() || access?.systemAccountId
}
