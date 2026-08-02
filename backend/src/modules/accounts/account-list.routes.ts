import type { Router } from 'express'

import { ok } from '../../shared/http.js'
import { listAccountManagementItemsPageAsync, type AccountManagementListResult } from '../../storage/account-management-list.repository.js'
import { listAccountOptionsAsync } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import {
  AccountRuntimeStatusFilterScanLimitError,
  accountListNeedsRuntimeStatusFilter,
  listAccountsPageWithRuntimeStatusFilter
} from './account-list-runtime-status-filter.js'
import { parseAccountListOptions, parseAccountOptionsQuery } from './account-list-query.js'
import {
  hydrateAccountListPage,
  type AccountListTimingMetric
} from './account-status-snapshot.service.js'

export function registerAccountListRoutes(router: Router): void {
  router.get('/', async (req, res, next) => {
    try {
      const requestAccess = getRequestAccessScope(req.query.systemAccountId)
      const listOptions = parseAccountListOptions(req.query)
      let listDurationMs = 0
      let statusFilterDurationMs = 0
      let hydrateDurationMs = 0
      const hydrateTimingDurations = new Map<AccountListTimingMetric, number>()
      const needsRuntimeStatusFilter = accountListNeedsRuntimeStatusFilter(listOptions)

      let completePage: AccountManagementListResult | undefined
      if (needsRuntimeStatusFilter) {
        const statusFilterStartedAt = performance.now()
        completePage = await listAccountsPageWithRuntimeStatusFilter(requestAccess, listOptions)
        statusFilterDurationMs = performance.now() - statusFilterStartedAt
      }

      if (!completePage) {
        const listStartedAt = performance.now()
        const result = await listAccountManagementItemsPageAsync(requestAccess, listOptions)
        listDurationMs = performance.now() - listStartedAt
        const hydrateStartedAt = performance.now()
        completePage = await hydrateAccountListPage(requestAccess, result, (metric, durationMs) => {
          hydrateTimingDurations.set(metric, (hydrateTimingDurations.get(metric) ?? 0) + durationMs)
        })
        hydrateDurationMs = performance.now() - hydrateStartedAt
      }

      const serverTiming = [serverTimingMetric('account-list', listDurationMs)]
      if (needsRuntimeStatusFilter) {
        serverTiming.push(serverTimingMetric('account-status-filter', statusFilterDurationMs))
      } else {
        serverTiming.push(serverTimingMetric('account-hydrate', hydrateDurationMs))
        for (const metric of accountListTimingMetrics) {
          const durationMs = hydrateTimingDurations.get(metric)
          if (durationMs !== undefined) serverTiming.push(serverTimingMetric(metric, durationMs))
        }
      }
      res.setHeader('Server-Timing', serverTiming.join(', '))
      res.json(ok(completePage))
    } catch (error) {
      if (error instanceof AccountRuntimeStatusFilterScanLimitError) {
        res.status(422).json({ message: error.message })
        return
      }
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

export function serverTimingMetric(name: string, durationMs: number): string {
  const normalizedDurationMs = Number.isFinite(durationMs) && durationMs >= 0 ? durationMs : 0
  return `${name};dur=${normalizedDurationMs.toFixed(1)}`
}

const accountListTimingMetrics: readonly AccountListTimingMetric[] = [
  'account-usage',
  'account-runtime',
  'account-concurrency',
  'account-circuit',
  'account-balance'
]
