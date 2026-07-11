import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import { findAccountBalanceRefreshCandidateAsync, saveAccountBalanceConfigurationAsync } from '../../storage/account-balance.repository.js'
import { findAccountForTestAsync } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { refreshAccountBalanceCandidate } from './account-balance-query.service.js'
import { accountBalanceQueryConfigSchema, normalizeAccountBalanceConfig, validateAccountBalanceCapability } from './account-balance-config.js'
import { requestStatsWriter } from '../background/background-stats-writer.js'

export function registerAccountBalanceRoutes(router: Router): void {
  router.post('/:id/balance/refresh', async (req, res, next) => {
    try {
      const scopeQuery = parseRequestScopeQuery(req.query)
      if (!scopeQuery.success) {
        res.status(400).json(badRequest(scopeQuery.message))
        return
      }
      const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
      const account = await findAccountForTestAsync(req.params.id, requestAccess)
      if (!account) {
        res.status(404).json({ message: '账户不存在' })
        return
      }
      if (account.accessType === 'authorized' || account.accountAuthorizationId || account.authorizationInstanceSourceAccountId || account.permissions?.canEdit === false) {
        res.status(403).json({ message: '无权刷新该账户的上游余额' })
        return
      }
      if (req.body?.balanceQueryConfig !== undefined) {
        const parsedConfig = accountBalanceQueryConfigSchema.safeParse(req.body.balanceQueryConfig)
        if (!parsedConfig.success) {
          res.status(400).json(badRequest('余额查询配置无效'))
          return
        }
        try {
          validateAccountBalanceCapability(account, true)
          await saveAccountBalanceConfigurationAsync({
            accountId: account.id,
            enabled: true,
            config: normalizeAccountBalanceConfig(parsedConfig.data)
          })
          await requestStatsWriter({ type: 'delete_account_balance_snapshot', accountId: account.id })
        } catch (error) {
          res.status(400).json(badRequest(error instanceof Error ? error.message : '余额查询配置无效'))
          return
        }
      }
      const candidate = await findAccountBalanceRefreshCandidateAsync(account.id)
      if (!candidate) {
        res.status(400).json(badRequest('账户未开启余额查询，或当前不可用'))
        return
      }
      res.json(ok(await refreshAccountBalanceCandidate(candidate)))
    } catch (error) {
      next(error)
    }
  })
}
