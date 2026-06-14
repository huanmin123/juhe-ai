import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import { findAccountForTest, findAccountSummary } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { applyServerAccountRuntimeToAccount } from '../gateway/runtime/runtime-snapshot.service.js'
import { sanitizeAccountResponse } from './account-response-sanitizer.js'

export function registerAccountDetailRoutes(router: Router): void {
  router.get('/:id', async (req, res, next) => {
    try {
      const scopeQuery = parseRequestScopeQuery(req.query)
      if (!scopeQuery.success) {
        res.status(400).json(badRequest(scopeQuery.message))
        return
      }
      const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
      const visibleAccount = findAccountSummary(req.params.id, requestAccess)
      if (!visibleAccount) {
        res.status(404).json({ message: '账户不存在' })
        return
      }
      if (visibleAccount.accessType === 'authorized') {
        const hydratedAccount = await applyServerAccountRuntimeToAccount(visibleAccount)
        res.json(ok(sanitizeAccountResponse(hydratedAccount)))
        return
      }
      if (visibleAccount.permissions?.canViewCredentials === false || visibleAccount.permissions?.canEdit === false) {
        res.status(403).json({ message: '无权查看账户凭据' })
        return
      }
      const account = findAccountForTest(req.params.id, requestAccess)
      if (!account) {
        res.status(404).json({ message: '账户不存在' })
        return
      }
      const hydratedAccount = await applyServerAccountRuntimeToAccount(account)
      res.json(ok(hydratedAccount))
    } catch (error) {
      next(error)
    }
  })
}
