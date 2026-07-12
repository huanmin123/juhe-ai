import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import { findAccountBalanceRefreshCandidateAsync } from '../../storage/account-balance.repository.js'
import { findAccountForTestAsync } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { refreshAccountBalanceCandidate, testAccountBalanceCandidate } from './account-balance-query.service.js'
import { normalizeAccountBalanceConfig, validateAccountBalanceCapability } from './account-balance-config.js'
import { prepareAccountDraftTestSnapshotAsync } from './account-draft-test.service.js'
import { accountBalanceDraftTestSchema } from './account-request.schemas.js'

export function registerAccountBalanceRoutes(router: Router): void {
  router.post('/balance/test-draft', async (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    if (!requestAccess) {
      res.status(403).json({ message: '缺少系统账户上下文' })
      return
    }
    const parsed = accountBalanceDraftTestSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '余额查询测试参数无效'))
      return
    }
    try {
      const preparedDraft = await prepareAccountDraftTestSnapshotAsync({
        accountInput: parsed.data.account,
        requestAccess
      })
      validateAccountBalanceCapability(preparedDraft.account, true)
      res.json(ok(await testAccountBalanceCandidate({
        id: preparedDraft.account.id,
        credentials: preparedDraft.account.credentials,
        config: normalizeAccountBalanceConfig(parsed.data.balanceQueryConfig),
        proxyProfileId: preparedDraft.account.proxyProfileId
      })))
    } catch (error) {
      res.status(400).json(badRequest(error instanceof Error ? error.message : '余额查询测试失败'))
    }
  })

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
