import type { Router } from 'express'

import { ok } from '../../shared/http.js'
import { listAccountItemsPageAsync, listAccountOptionsAsync } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import {
  accountListNeedsRuntimeStatusFilter,
  applyAccountListRuntimeStatusFilter,
  listAccountsPageWithRuntimeStatusFilter
} from './account-list-runtime-status-filter.js'
import { parseAccountListOptions, parseAccountOptionsQuery } from './account-list-query.js'
import { sanitizeAccountListResponse } from './account-response-sanitizer.js'

export function registerAccountListRoutes(router: Router): void {
  router.get('/', async (req, res, next) => {
    try {
      const requestAccess = getRequestAccessScope(req.query.systemAccountId)
      const listOptions = parseAccountListOptions(req.query)
      let listDurationMs = 0
      let statusFilterDurationMs = 0
      const needsRuntimeStatusFilter = accountListNeedsRuntimeStatusFilter(listOptions)

      const statusFilterStartedAt = performance.now()
      let filteredResult: Awaited<ReturnType<typeof applyAccountListRuntimeStatusFilter>> | undefined = needsRuntimeStatusFilter
        ? await listAccountsPageWithRuntimeStatusFilter(requestAccess, listOptions)
        : undefined
      statusFilterDurationMs = performance.now() - statusFilterStartedAt

      if (!filteredResult) {
        const listStartedAt = performance.now()
        const result = await listAccountItemsPageAsync(requestAccess, listOptions)
        listDurationMs = performance.now() - listStartedAt
        if (needsRuntimeStatusFilter) {
          const fallbackStatusFilterStartedAt = performance.now()
          filteredResult = await applyAccountListRuntimeStatusFilter(requestAccess, listOptions, result)
          statusFilterDurationMs += performance.now() - fallbackStatusFilterStartedAt
        } else {
          filteredResult = result
        }
      }

      res.setHeader('Server-Timing', [
        serverTimingMetric('account-list', listDurationMs),
        serverTimingMetric('account-status-filter', statusFilterDurationMs)
      ].join(', '))
      res.json(ok(sanitizeAccountListResponse(filteredResult)))
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
