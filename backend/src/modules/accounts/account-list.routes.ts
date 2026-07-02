import type { Router } from 'express'

import { ok } from '../../shared/http.js'
import { listAccountOptionsAsync, listAccountsPageAsync } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { applyServerAccountConcurrencyToAccountList } from '../gateway/runtime/runtime-snapshot.service.js'
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
      let concurrencyDurationMs = 0
      let statusFilterDurationMs = 0

      const statusFilterStartedAt = performance.now()
      let filteredResult: Awaited<ReturnType<typeof applyAccountListRuntimeStatusFilter>> | undefined = accountListNeedsRuntimeStatusFilter(listOptions)
        ? await listAccountsPageWithRuntimeStatusFilter(requestAccess, listOptions)
        : undefined
      statusFilterDurationMs = performance.now() - statusFilterStartedAt

      if (!filteredResult) {
        const listStartedAt = performance.now()
        const result = await listAccountsPageAsync(requestAccess, listOptions)
        listDurationMs = performance.now() - listStartedAt
        const concurrencyStartedAt = performance.now()
        const hydratedResult = await applyServerAccountConcurrencyToAccountList(result)
        concurrencyDurationMs = performance.now() - concurrencyStartedAt
        const fallbackStatusFilterStartedAt = performance.now()
        filteredResult = await applyAccountListRuntimeStatusFilter(requestAccess, listOptions, hydratedResult)
        statusFilterDurationMs += performance.now() - fallbackStatusFilterStartedAt
      }

      res.setHeader('Server-Timing', [
        serverTimingMetric('account-list', listDurationMs),
        serverTimingMetric('account-concurrency', concurrencyDurationMs),
        serverTimingMetric('account-status-filter', statusFilterDurationMs)
      ].join(', '))
      res.json(ok(sanitizeAccountListResponse(filteredResult)))
    } catch (error) {
      next(error)
    }
  })

  router.get('/options', async (req, res, next) => {
    try {
      const options = await listAccountOptionsAsync(getRequestAccessScope(req.query.systemAccountId), parseAccountOptionsQuery(req.query))
      res.json(ok(options))
    } catch (error) {
      next(error)
    }
  })
}

function serverTimingMetric(name: string, durationMs: number): string {
  return `${name};dur=${Math.max(0, durationMs).toFixed(1)}`
}
