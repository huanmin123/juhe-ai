import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import {
  accountBalanceSnapshotMatchesConfiguration,
  findAccountBalanceManualRefreshCandidateAsync,
  loadAccountBalanceSnapshotRecordsByAccountIdsAsync
} from '../../storage/account-balance.repository.js'
import { findAccountForTestAsync } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { refreshAccountBalanceCandidateWithOutcome, testAccountBalanceCandidate } from './account-balance-query.service.js'
import {
  accountBalanceApiKeyFingerprint,
  MULTI_KEY_ACCOUNT_BALANCE_QUERY_MESSAGE,
  effectiveAccountApiKeys,
  maskAccountBalanceApiKey,
  normalizeAccountBalanceConfig,
  validateAccountBalanceCapability
} from './account-balance-config.js'
import type { AccountBalanceKeySnapshot } from './account-balance.types.js'
import { prepareAccountDraftTestSnapshotAsync } from './account-draft-test.service.js'
import { accountBalanceDraftTestSchema } from './account-request.schemas.js'
import { accountBalanceGoOwnerEnabled, runAccountBalanceManualViaGo } from '../background/account-balance-handover.js'

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

  router.get('/:id/balance/details', async (req, res, next) => {
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
      if (account.accessType === 'authorized' || account.accountAuthorizationId || account.authorizationInstanceSourceAccountId || account.permissions?.canViewCredentials !== true) {
        res.status(403).json({ message: '无权查看该账户的上游余额明细' })
        return
      }
      if (account.balanceQueryEnabled !== true) {
        res.status(404).json({ message: '账户未开启余额查询' })
        return
      }
      const apiKeys = effectiveAccountApiKeys(account.credentials)
      const record = (await loadAccountBalanceSnapshotRecordsByAccountIdsAsync([account.id])).get(account.id)
      const currentSnapshot = accountBalanceSnapshotMatchesConfiguration({
        nextRefreshAt: account.balanceQueryNextRefreshAt,
        ...(account.configRevision === undefined ? {} : { configRevision: account.configRevision })
      }, record)
        ? record?.snapshot
        : undefined
      const storedByFingerprint = new Map(
        (currentSnapshot?.keyBalances ?? []).map((item) => [item.keyFingerprint, item])
      )
      const keyBalances: AccountBalanceKeySnapshot[] = apiKeys.map((apiKey) => storedByFingerprint.get(accountBalanceApiKeyFingerprint(apiKey)) ?? ({
        keyFingerprint: accountBalanceApiKeyFingerprint(apiKey),
        maskedKey: maskAccountBalanceApiKey(apiKey),
        status: 'pending'
      }))
      res.setHeader('Cache-Control', 'no-store')
      res.json(ok({
        accountId: account.id,
        ...(account.configRevision === undefined ? {} : { configRevision: account.configRevision }),
        ...(currentSnapshot?.configRevision === undefined ? {} : { configRevision: currentSnapshot.configRevision }),
        keyCount: apiKeys.length,
        queriedKeyCount: currentSnapshot?.queriedKeyCount ?? 0,
        scope: currentSnapshot?.scope ?? 'unknown',
        aggregation: currentSnapshot?.aggregation ?? 'unknown',
        // A stale record is mapped to pending rows below; do not expose its
        // timestamp as if it described the current Key set/configuration.
        ...(currentSnapshot && record?.updatedAt ? { updatedAt: record.updatedAt } : {}),
        keyBalances
      }))
    } catch (error) {
      next(error)
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
      if (accountBalanceGoOwnerEnabled()) {
        if (effectiveAccountApiKeys(account.credentials).length > 1) {
          res.status(409).json({ message: '当前 Go 余额任务所有者仍仅支持单 Key；多 Key 余额请切换回 Node 任务所有者' })
          return
        }
        const result = await runAccountBalanceManualViaGo(candidate)
        if (result.outcome === 'lease_busy' || result.outcome === 'stale' || !result.committed) {
          res.status(409).json({ message: result.outcome === 'lease_busy' ? '余额查询正在进行，请稍后刷新' : '账户余额配置已变化，请刷新列表后重试' })
          return
        }
        res.json(ok(result.snapshot))
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
