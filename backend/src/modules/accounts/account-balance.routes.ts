import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import { findAccountBalanceManualRefreshCandidateAsync } from '../../storage/account-balance.repository.js'
import { findAccountForTestAsync } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { refreshAccountBalanceCandidateWithOutcome, testAccountBalanceCandidate } from './account-balance-query.service.js'
import {
  MULTI_KEY_ACCOUNT_BALANCE_QUERY_MESSAGE,
  normalizeAccountBalanceConfig,
  validateAccountBalanceCapability
} from './account-balance-config.js'
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
      const balanceDecision = validateAccountBalanceCapability(preparedDraft.account, true)
      if (!balanceDecision.enabled) {
        throw new Error(balanceDecision.autoDisabledForMultipleApiKeys
          ? MULTI_KEY_ACCOUNT_BALANCE_QUERY_MESSAGE
          : '当前账户不支持上游余额查询')
      }
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
      const candidate = await findAccountBalanceManualRefreshCandidateAsync(account.id)
      if (!candidate) {
        res.status(400).json(badRequest('账户未开启余额查询或当前账户类型不支持'))
        return
      }
      const result = await refreshAccountBalanceCandidateWithOutcome(candidate, { mode: 'manual' })
      if (!result.persisted) {
        res.status(409).json({
          message: result.outcome === 'lease_busy'
            ? '余额查询正在进行，请稍后刷新'
            : '账户余额配置已变化，请刷新列表后重试'
        })
        return
      }
      res.json(ok(result.snapshot))
    } catch (error) {
      next(error)
    }
  })
}
