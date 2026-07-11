import type { Router } from 'express'

import type { AccountSummary } from '../../domain/types.js'
import { badRequest, ok } from '../../shared/http.js'
import { findAccountForTestAsync, findAccountSummaryAsync } from '../../storage/repositories.js'
import { loadAccountBalanceConfigurationsByAccountIdsAsync } from '../../storage/account-balance.repository.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { applyServerAccountRuntimeToAccount } from '../gateway/runtime/runtime-snapshot.service.js'
import { sanitizeAccountBasicDetailResponse, sanitizeAccountEditBasicDetailResponse, sanitizeAccountResponse } from './account-response-sanitizer.js'

export function registerAccountDetailRoutes(router: Router): void {
  router.get('/:id/advanced', async (req, res, next) => {
    try {
      const account = await loadEditableAccountDetail(req.params.id, req.query)
      if (!account) {
        res.status(404).json({ message: '账户不存在' })
        return
      }
      res.json(ok(account))
    } catch (error) {
      if (error instanceof AccountDetailBadRequestError) {
        res.status(400).json(badRequest(error.message))
        return
      }
      if (error instanceof Error && error.message === '无权查看账户凭据') {
        res.status(403).json({ message: error.message })
        return
      }
      next(error)
    }
  })

  router.get('/:id/edit-basic', async (req, res, next) => {
    try {
      const account = await loadEditableAccountBasicDetail(req.params.id, req.query)
      if (!account) {
        res.status(404).json({ message: '账户不存在' })
        return
      }
      res.json(ok(account))
    } catch (error) {
      if (error instanceof AccountDetailBadRequestError) {
        res.status(400).json(badRequest(error.message))
        return
      }
      if (error instanceof Error && error.message === '无权查看账户凭据') {
        res.status(403).json({ message: error.message })
        return
      }
      next(error)
    }
  })

  router.get('/:id', async (req, res, next) => {
    try {
      const account = await loadBasicAccountDetail(req.params.id, req.query)
      if (!account) {
        res.status(404).json({ message: '账户不存在' })
        return
      }
      res.json(ok(account))
    } catch (error) {
      if (error instanceof AccountDetailBadRequestError) {
        res.status(400).json(badRequest(error.message))
        return
      }
      if (error instanceof Error && error.message === '无权查看账户凭据') {
        res.status(403).json({ message: error.message })
        return
      }
      next(error)
    }
  })
}

async function loadEditableAccountBasicDetail(accountId: string, query: Record<string, unknown>): Promise<AccountSummary | undefined> {
  const scopeQuery = parseRequestScopeQuery(query)
  if (!scopeQuery.success) {
    throw new AccountDetailBadRequestError(scopeQuery.message)
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const account = await findAccountSummaryAsync(accountId, requestAccess)
  if (!account) return undefined
  if (account.permissions?.canViewCredentials === false || account.permissions?.canEdit === false) {
    throw new Error('无权查看账户凭据')
  }
  return sanitizeAccountEditBasicDetailResponse(await hydrateEditableBalanceConfiguration(account))
}

async function loadBasicAccountDetail(accountId: string, query: Record<string, unknown>): Promise<AccountSummary | undefined> {
  const scopeQuery = parseRequestScopeQuery(query)
  if (!scopeQuery.success) {
    throw new AccountDetailBadRequestError(scopeQuery.message)
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const account = await findAccountSummaryAsync(accountId, requestAccess)
  if (!account) return undefined
  const hydratedAccount = await applyServerAccountRuntimeToAccount(account)
  return sanitizeAccountBasicDetailResponse(hydratedAccount)
}

async function loadEditableAccountDetail(accountId: string, query: Record<string, unknown>): Promise<AccountSummary | undefined> {
  const scopeQuery = parseRequestScopeQuery(query)
  if (!scopeQuery.success) {
    throw new AccountDetailBadRequestError(scopeQuery.message)
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const visibleAccount = await findAccountSummaryAsync(accountId, requestAccess)
  if (!visibleAccount) return undefined
  if (visibleAccount.accessType === 'authorized') {
    const hydratedAccount = await applyServerAccountRuntimeToAccount(visibleAccount)
    return sanitizeAccountResponse(hydratedAccount)
  }
  if (visibleAccount.permissions?.canViewCredentials === false || visibleAccount.permissions?.canEdit === false) {
    throw new Error('无权查看账户凭据')
  }
  const account = await findAccountForTestAsync(accountId, requestAccess, visibleAccount)
  if (!account) return undefined
  return applyServerAccountRuntimeToAccount(await hydrateEditableBalanceConfiguration(account))
}

async function hydrateEditableBalanceConfiguration(account: AccountSummary): Promise<AccountSummary> {
  if (account.accessType === 'authorized' || account.accountAuthorizationId || account.authorizationInstanceSourceAccountId) return account
  const configuration = (await loadAccountBalanceConfigurationsByAccountIdsAsync([account.id])).get(account.id)
  if (!configuration) return account
  return {
    ...account,
    balanceQueryEnabled: configuration.enabled,
    balanceQueryConfig: configuration.config,
    balanceQueryNextRefreshAt: configuration.nextRefreshAt
  }
}

class AccountDetailBadRequestError extends Error {}
