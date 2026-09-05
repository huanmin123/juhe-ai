import type { Router } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { ok } from '../../shared/http.js'
import {
  AccountListAvailabilityProjectionUnavailableError,
  listAccountListAvailabilityProjectionPageInClient
} from '../../storage/account-list-availability-projection.repository.js'
import { listAccountManagementItemsPageAsync, type AccountManagementListResult } from '../../storage/account-management-list.repository.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { getPostgresPool } from '../../storage/postgres-client.js'
import { listAccountOptionsAsync } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import {
  AccountRuntimeStatusFilterScanLimitError,
  accountListNeedsRuntimeStatusFilter,
  listAccountsPageWithRuntimeStatusFilter,
  takeAccountRuntimeStatusFilterTiming
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
      let listHydrationDurationMs = 0
      let projectionDurationMs = 0
      let runtimeStatusTiming: ReturnType<typeof takeAccountRuntimeStatusFilterTiming>
      const projectionAccess = requestAccess?.role === 'user' ? requestAccess : undefined
      const useAvailabilityProjection = Boolean(projectionAccess)
        && runtimeConfig.databaseDriver === 'postgres'
        && runtimeConfig.background.accountListAvailabilityProjectionReadEnabled
      const needsRuntimeStatusFilter = !useAvailabilityProjection && accountListNeedsRuntimeStatusFilter(listOptions)

      let completePage: AccountManagementListResult | undefined
      if (useAvailabilityProjection && projectionAccess) {
        const projectionStartedAt = performance.now()
        const client = createPostgresDatabaseClient(await getPostgresPool())
        completePage = await listAccountListAvailabilityProjectionPageInClient(client, {
          viewerSystemAccountId: projectionAccess.systemAccountId,
          options: listOptions,
        })
        projectionDurationMs = performance.now() - projectionStartedAt
      } else {
        const statusFilterStartedAt = performance.now()
        completePage = needsRuntimeStatusFilter
          ? await listAccountsPageWithRuntimeStatusFilter(requestAccess, listOptions)
          : undefined
        statusFilterDurationMs = performance.now() - statusFilterStartedAt
        if (completePage) runtimeStatusTiming = takeAccountRuntimeStatusFilterTiming(completePage)
      }

      if (!completePage) {
        const listStartedAt = performance.now()
        const result = await listAccountManagementItemsPageAsync(requestAccess, listOptions)
        listDurationMs = performance.now() - listStartedAt
        const hydrateStartedAt = performance.now()
        completePage = await hydrateAccountListPage(requestAccess, result)
        listHydrationDurationMs = performance.now() - hydrateStartedAt
      }

      const serverTiming = [
        serverTimingMetric('account-list', listDurationMs),
        serverTimingMetric('account-list-hydrate', listHydrationDurationMs),
        serverTimingMetric('account-status-filter', statusFilterDurationMs),
        serverTimingMetric('account-list-projection', projectionDurationMs)
      ]
      if (runtimeStatusTiming) {
        serverTiming.push(
          serverTimingMetric('account-status-candidate-list', runtimeStatusTiming.candidateListDurationMs),
          serverTimingMetric('account-status-candidate-hydrate', runtimeStatusTiming.candidateHydrationDurationMs),
          serverTimingMetric('account-status-candidate-predicate', runtimeStatusTiming.candidatePredicateDurationMs),
          serverTimingMetric('account-status-final-hydrate', runtimeStatusTiming.finalHydrationDurationMs),
          serverTimingMetric('account-status-final-tags', runtimeStatusTiming.finalTagDurationMs)
        )
      }
      res.setHeader('Server-Timing', serverTiming.join(', '))
      res.json(ok(completePage))
    } catch (error) {
      if (error instanceof AccountRuntimeStatusFilterScanLimitError) {
        res.status(422).json({ message: error.message })
        return
      }
      if (error instanceof AccountListAvailabilityProjectionUnavailableError) {
        res.setHeader('Retry-After', '1')
        res.status(503).json({
          code: 'account_list_projection_unavailable',
          message: '账户列表正在更新，请稍后重试'
        })
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
      if (error instanceof AccountListAvailabilityProjectionUnavailableError) {
        res.setHeader('Retry-After', '1')
        res.status(503).json({
          code: 'account_list_projection_unavailable',
          message: '账户列表正在更新，请稍后重试'
        })
        return
      }
      next(error)
    }
  })
}

function serverTimingMetric(name: string, durationMs: number): string {
  return `${name};dur=${Math.max(0, durationMs).toFixed(1)}`
}
