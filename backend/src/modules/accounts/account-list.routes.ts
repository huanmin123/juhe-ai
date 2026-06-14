import type { Router } from 'express'

import { ok } from '../../shared/http.js'
import { listAccountOptions, listAccountsPage } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { applyServerAccountConcurrencyToAccountList } from '../gateway/runtime/runtime-snapshot.service.js'
import { parseAccountListOptions, parseAccountOptionsQuery } from './account-list-query.js'
import { sanitizeAccountListResponse } from './account-response-sanitizer.js'

export function registerAccountListRoutes(router: Router): void {
  router.get('/', async (req, res, next) => {
    try {
      const listStartedAt = performance.now()
      const result = listAccountsPage(getRequestAccessScope(req.query.systemAccountId), parseAccountListOptions(req.query))
      const listDurationMs = performance.now() - listStartedAt
      const concurrencyStartedAt = performance.now()
      const hydratedResult = await applyServerAccountConcurrencyToAccountList(result)
      const concurrencyDurationMs = performance.now() - concurrencyStartedAt
      res.setHeader('Server-Timing', [
        serverTimingMetric('account-list', listDurationMs),
        serverTimingMetric('account-concurrency', concurrencyDurationMs)
      ].join(', '))
      res.json(ok(sanitizeAccountListResponse(hydratedResult)))
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
