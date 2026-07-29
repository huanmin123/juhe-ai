import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import { findAccountAdvancedDetailAsync } from '../../storage/account-advanced-detail.repository.js'
import { findAccountApiKeyRuntimeAccountAsync } from '../../storage/account-api-key-runtime.repository.js'
import { AccountEditBasicForbiddenError, findAccountEditBasicDetailAsync } from '../../storage/account-edit-basic.repository.js'
import {
  AccountInteractionContextConflictError,
  AccountInteractionContextForbiddenError,
  findAccountCloneContextAsync,
  findAccountOAuthReauthorizationContextAsync
} from '../../storage/account-interaction-context.repository.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { loadOwnerAccountApiKeyRuntimeResponse } from './account-api-key-pool-runtime.js'
import { sanitizeAccountApiKeyRuntimeResponse } from './account-response-sanitizer.js'

export function registerAccountDetailRoutes(router: Router): void {
  router.get('/:id/oauth-reauthorization-context', async (req, res, next) => {
    res.setHeader('Cache-Control', 'no-store')
    try {
      const account = await loadAccountOAuthReauthorizationContext(req.params.id, req.query)
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
      if (error instanceof AccountInteractionContextForbiddenError) {
        res.status(403).json({ message: error.message })
        return
      }
      next(error)
    }
  })

  router.get('/:id/clone-context', async (req, res, next) => {
    res.setHeader('Cache-Control', 'no-store')
    try {
      const account = await loadAccountCloneContext(req.params.id, req.query)
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
      if (error instanceof AccountInteractionContextForbiddenError) {
        res.status(403).json({ message: error.message })
        return
      }
      if (error instanceof AccountInteractionContextConflictError) {
        res.status(409).json({ message: error.message })
        return
      }
      next(error)
    }
  })

  router.get('/:id/api-key-runtime', async (req, res, next) => {
    res.setHeader('Cache-Control', 'no-store')
    try {
      const account = await loadAccountForApiKeyRuntime(req.params.id, req.query)
      if (!account) {
        res.status(404).json({ message: '账户不存在' })
        return
      }
      if (account.accessType === 'authorized') {
        res.status(403).json({ message: '授权实例不能查看来源账户 API Key 运行明细' })
        return
      }
      const runtime = await loadOwnerAccountApiKeyRuntimeResponse(account)
      if (!runtime) {
        res.status(403).json({ message: '无权查看账户 API Key 运行明细' })
        return
      }
      res.json(ok(sanitizeAccountApiKeyRuntimeResponse(runtime)))
    } catch (error) {
      if (error instanceof AccountDetailBadRequestError) {
        res.status(400).json(badRequest(error.message))
        return
      }
      next(error)
    }
  })

  router.get('/:id/advanced', async (req, res, next) => {
    res.setHeader('Cache-Control', 'no-store')
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
      next(error)
    }
  })

  router.get('/:id/edit-basic', async (req, res, next) => {
    res.setHeader('Cache-Control', 'no-store')
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
      if (error instanceof AccountEditBasicForbiddenError) {
        res.status(403).json({ message: error.message })
        return
      }
      next(error)
    }
  })

}

async function loadAccountForApiKeyRuntime(accountId: string, query: Record<string, unknown>) {
  const scopeQuery = parseRequestScopeQuery(query)
  if (!scopeQuery.success) {
    throw new AccountDetailBadRequestError(scopeQuery.message)
  }
  return findAccountApiKeyRuntimeAccountAsync(accountId, getRequestAccessScope(scopeQuery.data.systemAccountId))
}

async function loadEditableAccountBasicDetail(accountId: string, query: Record<string, unknown>) {
  const scopeQuery = parseRequestScopeQuery(query)
  if (!scopeQuery.success) {
    throw new AccountDetailBadRequestError(scopeQuery.message)
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  return findAccountEditBasicDetailAsync(accountId, requestAccess)
}

async function loadEditableAccountDetail(accountId: string, query: Record<string, unknown>) {
  const scopeQuery = parseRequestScopeQuery(query)
  if (!scopeQuery.success) {
    throw new AccountDetailBadRequestError(scopeQuery.message)
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  return findAccountAdvancedDetailAsync(accountId, requestAccess)
}

async function loadAccountOAuthReauthorizationContext(accountId: string, query: Record<string, unknown>) {
  const scopeQuery = parseRequestScopeQuery(query)
  if (!scopeQuery.success) throw new AccountDetailBadRequestError(scopeQuery.message)
  return findAccountOAuthReauthorizationContextAsync(
    accountId,
    getRequestAccessScope(scopeQuery.data.systemAccountId)
  )
}

async function loadAccountCloneContext(accountId: string, query: Record<string, unknown>) {
  const scopeQuery = parseRequestScopeQuery(query)
  if (!scopeQuery.success) throw new AccountDetailBadRequestError(scopeQuery.message)
  return findAccountCloneContextAsync(
    accountId,
    getRequestAccessScope(scopeQuery.data.systemAccountId)
  )
}

class AccountDetailBadRequestError extends Error {}
