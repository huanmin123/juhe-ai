import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { getAccountStatusSnapshot, parseAccountStatusSnapshotAccountIds } from './account-status-snapshot.service.js'

export function registerAccountStatusSnapshotRoutes(router: Router): void {
  router.get('/status-snapshot', async (req, res, next) => {
    try {
      const accountIds = parseAccountStatusSnapshotAccountIds(req.query.accountIds)
      const result = await getAccountStatusSnapshot(getRequestAccessScope(req.query.systemAccountId), accountIds)
      res.json(ok(result))
    } catch (error) {
      if (error instanceof Error && error.message.startsWith('账户状态快照')) {
        res.status(400).json(badRequest(error.message))
        return
      }
      next(error)
    }
  })
}
