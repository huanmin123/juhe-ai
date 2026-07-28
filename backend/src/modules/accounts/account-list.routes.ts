import type { Router } from 'express'

import { ok } from '../../shared/http.js'
import { listAccountManagementItemsPageAsync, type AccountManagementListResult } from '../../storage/account-management-list.repository.js'
import { listAccountOptionsAsync } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import {
  accountListNeedsRuntimeStatusFilter,
  listAccountsPageWithRuntimeStatusFilter
} from './account-list-runtime-status-filter.js'
import { parseAccountListOptions, parseAccountOptionsQuery } from './account-list-query.js'
import { hydrateAccountListPage } from './account-status-snapshot.service.js'

export function registerAccountListRoutes(router: Router): void {
  router.get('/', async (req, res, next) => {
    try {
      const requestAccess = getRequestAccessScope(req.query.systemAccountId)
      const listOptions = parseAccountListOptions(req.query)
      let listDurationMs = 0
      let statusFilterDurationMs = 0
      const needsRuntimeStatusFilter = accountListNeedsRuntimeStatusFilter(listOptions)

      const statusFilterStartedAt = performance.now()
      let completePage: AccountManagementListResult | undefined = needsRuntimeStatusFilter
        ? await listAccountsPageWithRuntimeStatusFilter(requestAccess, listOptions)
        : undefined
      statusFilterDurationMs = performance.now() - statusFilterStartedAt

      if (!completePage) {
        const listStartedAt = performance.now()
        const result = await listAccountManagementItemsPageAsync(requestAccess, listOptions)
        listDurationMs = performance.now() - listStartedAt
        const hydrateStartedAt = performance.now()
        completePage = await hydrateAccountListPage(requestAccess, result)
        statusFilterDurationMs += performance.now() - hydrateStartedAt
      }

      res.setHeader('Server-Timing', [
        serverTimingMetric('account-list', listDurationMs),
        serverTimingMetric('account-status-filter', statusFilterDurationMs)
      ].join(', '))
      res.json(ok(completePage))
    } catch (error) {
      next(error)
    }
  })

  router.get('/options', async (req, res, next) => {
    try {
      const access = getRequestAccessScope(req.query.systemAccountId)
      const query = parseAccountOptionsQuery(req.query)
      const options = await listAccountOptionsAsync(access, query)
      res.json(ok(options))
    } catch (error) {
      next(error)
    }
  })
}

function serverTimingMetric(name: string, durationMs: number): string {
  return `${name};dur=${Math.max(0, durationMs).toFixed(1)}`
}
