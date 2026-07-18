import type { Router } from 'express'

import { ok } from '../../shared/http.js'
import { listAccountOptionsAsync, listAccountsPageAsync } from '../../storage/repositories.js'
import {
  accountBalanceSnapshotMatchesConfiguration,
  loadAccountBalanceConfigurationsByAccountIdsAsync,
  loadAccountBalanceSnapshotRecordsByAccountIdsAsync
} from '../../storage/account-balance.repository.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { applyServerAccountConcurrencyToAccountList } from '../gateway/runtime/runtime-snapshot.service.js'
import {
  accountListNeedsRuntimeStatusFilter,
  applyAccountListRuntimeStatusFilter,
  listAccountsPageWithRuntimeStatusFilter
} from './account-list-runtime-status-filter.js'
import { parseAccountListOptions, parseAccountOptionsQuery } from './account-list-query.js'
import { sanitizeAccountListResponse } from './account-response-sanitizer.js'
import { isAccountBalanceSnapshotSuppressed } from './account-balance-snapshot-cleanup.service.js'
import { createPageDataDomainReadCache, pageDataReadCacheKey } from '../page-data/page-data-read-cache.service.js'

const accountOptionsReadCache = createPageDataDomainReadCache<Awaited<ReturnType<typeof listAccountOptionsAsync>>>('accounts.options', {
  max: 512,
  ttlMs: 6 * 60 * 60 * 1000
})

export function registerAccountListRoutes(router: Router): void {
  router.get('/', async (req, res, next) => {
    try {
      const requestAccess = getRequestAccessScope(req.query.systemAccountId)
      const listOptions = parseAccountListOptions(req.query)
      let listDurationMs = 0
      let concurrencyDurationMs = 0
      let statusFilterDurationMs = 0
      let balanceDurationMs = 0

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

      const balanceStartedAt = performance.now()
      filteredResult = await hydrateAccountBalances(filteredResult)
      balanceDurationMs = performance.now() - balanceStartedAt

      res.setHeader('Server-Timing', [
        serverTimingMetric('account-list', listDurationMs),
        serverTimingMetric('account-concurrency', concurrencyDurationMs),
        serverTimingMetric('account-status-filter', statusFilterDurationMs),
        serverTimingMetric('account-balance', balanceDurationMs)
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
      const options = await accountOptionsReadCache.load(pageDataReadCacheKey({
        scope: access,
        route: '/accounts/options',
        query
      }), () => listAccountOptionsAsync(access, query))
      res.json(ok(options))
    } catch (error) {
      next(error)
    }
  })
}

async function hydrateAccountBalances<T extends { items: import('../../domain/types.js').AccountSummary[] }>(result: T): Promise<T> {
  const physicalIds = result.items
    .filter((account) => account.accessType !== 'authorized' && !account.accountAuthorizationId && !account.authorizationInstanceSourceAccountId)
    .map((account) => account.id)
  if (physicalIds.length === 0) return result
  const [configurations, snapshots] = await Promise.all([
    loadAccountBalanceConfigurationsByAccountIdsAsync(physicalIds),
    loadAccountBalanceSnapshotRecordsByAccountIdsAsync(physicalIds)
  ])
  return {
    ...result,
    items: result.items.map((account) => {
      const configuration = configurations.get(account.id)
      if (!configuration) return account
      const snapshotRecord = snapshots.get(account.id)
      return {
        ...account,
        balanceQueryEnabled: configuration.enabled,
        balanceQueryConfig: configuration.config,
        balanceQueryNextRefreshAt: configuration.nextRefreshAt,
        balanceSnapshot: configuration.enabled
          && !isAccountBalanceSnapshotSuppressed(account.id, { configuration, snapshotRecord })
          && accountBalanceSnapshotMatchesConfiguration(configuration, snapshotRecord)
          ? snapshotRecord.snapshot
          : undefined
      }
    })
  }
}

function serverTimingMetric(name: string, durationMs: number): string {
  return `${name};dur=${Math.max(0, durationMs).toFixed(1)}`
}
