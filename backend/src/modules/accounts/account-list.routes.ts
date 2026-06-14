import type { Router } from 'express'

import { ok } from '../../shared/http.js'
import { listAccountOptions, listAccountsPage } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { applyServerAccountConcurrencyToAccountList } from '../gateway/runtime/runtime-snapshot.service.js'
import { applyAccountListRuntimeStatusFilter } from './account-list-runtime-status-filter.js'
import { parseAccountListOptions, parseAccountOptionsQuery } from './account-list-query.js'
import { sanitizeAccountListResponse } from './account-response-sanitizer.js'

export function registerAccountListRoutes(router: Router): void {
  router.get('/', async (req, res, next) => {
    try {
      const listStartedAt = performance.now()
      const requestAccess = getRequestAccessScope(req.query.systemAccountId)
      const listOptions = parseAccountListOptions(req.query)
      const result = listAccountsPage(requestAccess, listOptions)
      const listDurationMs = performance.now() - listStartedAt
      const concurrencyStartedAt = performance.now()
      const hydratedResult = await applyServerAccountConcurrencyToAccountList(result)
      const concurrencyDurationMs = performance.now() - concurrencyStartedAt
      const statusFilterStartedAt = performance.now()
      const filteredResult = await applyAccountListRuntimeStatusFilter(requestAccess, listOptions, hydratedResult)
      const statusFilterDurationMs = performance.now() - statusFilterStartedAt
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

  router.get('/options', (req, res, next) => {
    try {
      const options = listAccountOptions(getRequestAccessScope(req.query.systemAccountId), parseAccountOptionsQuery(req.query))
      res.json(ok(options))
    } catch (error) {
      next(error)
    }
  })
}

function serverTimingMetric(name: string, durationMs: number): string {
  return `${name};dur=${Math.max(0, durationMs).toFixed(1)}`
}
