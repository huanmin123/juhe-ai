import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import { findAccountAdvancedDetailAsync } from '../../storage/account-advanced-detail.repository.js'
import { findAccountApiKeyRuntimeAccountAsync } from '../../storage/account-api-key-runtime.repository.js'
import { revalidateAccountApiKeyRuntimePoolAsync } from '../../storage/account-api-key-runtime-state.repository.js'
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
import { accountApiKeyRuntimeRevalidateSchema, accountRuntimeResetSchema } from './account-request.schemas.js'
import { operationMode, runLoggedOperationAsync, viewer } from '../operation-logs/operation-log.service.js'
import { mutationGuard, normalizedText, bodyField } from '../deduplication/mutation-guard.middleware.js'
import {
  AccountManagementPatchRevisionConflictError,
  AuthorizedAccountDispatchRevisionConflictError,
  resetAccountRuntimeStateAsync
} from './account-runtime-reset.service.js'

export function registerAccountDetailRoutes(router: Router): void {
  router.post('/:id/runtime-reset', mutationGuard({
    operationKey: 'accounts.runtime_reset',
    // Runtime cleanup is explicitly retryable: a 200 response may still carry
    // per-store failures (Redis/IPC), so do not retain a succeeded dedup entry.
    succeededTtlMs: 0,
    failedTtlMs: 0,
    scope: (req) => normalizedText(req.query.systemAccountId),
    fingerprint: (req) => ({ accountId: normalizedText(req.params.id), expectedConfigRevision: bodyField(req, 'expectedConfigRevision') })
  }), async (req, res, next) => {
    res.setHeader('Cache-Control', 'no-store')
    const parsed = accountRuntimeResetSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '清理运行状态参数无效'))
      return
    }
    try {
      const scopeQuery = parseRequestScopeQuery(req.query)
      if (!scopeQuery.success) {
        res.status(400).json(badRequest(scopeQuery.message))
        return
      }
      const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
      const result = await runLoggedOperationAsync(async () => {
        const outcome = await resetAccountRuntimeStateAsync({
          accountId: req.params.id,
          expectedConfigRevision: parsed.data.expectedConfigRevision,
          access: requestAccess
        })
        if (!outcome) throw new Error('账户不存在')
        return outcome
      }, req)
      res.json(ok(result))
    } catch (error) {
      if (error instanceof AccountManagementPatchRevisionConflictError || error instanceof AuthorizedAccountDispatchRevisionConflictError) {
        res.status(409).json(badRequest('账户配置已被其他操作更新，请刷新后重试'))
        return
      }
      if (error instanceof AccountDetailBadRequestError) {
        res.status(400).json(badRequest(error.message))
        return
      }
      if (error instanceof Error && error.message === '账户不存在') {
        res.status(404).json({ message: error.message })
        return
      }
      if (error instanceof Error) {
        res.status(400).json(badRequest(error.message))
        return
      }
      next(error)
    }
  })

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

  router.post('/:id/api-key-runtime/revalidate', mutationGuard({
    operationKey: 'accounts.api_key_runtime_revalidate',
    scope: (req) => normalizedText(req.query.systemAccountId),
    fingerprint: (req) => ({ accountId: normalizedText(req.params.id), expectedConfigRevision: bodyField(req, 'expectedConfigRevision') })
  }), async (req, res, next) => {
    res.setHeader('Cache-Control', 'no-store')
    const parsed = accountApiKeyRuntimeRevalidateSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '重新验证 API Key 池参数无效'))
      return
    }
    try {
      const scopeQuery = parseRequestScopeQuery(req.query)
      if (!scopeQuery.success) {
        res.status(400).json(badRequest(scopeQuery.message))
        return
      }
      const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
      const account = await loadAccountForApiKeyRuntime(req.params.id, req.query)
      if (!account) {
        res.status(404).json({ message: '账户不存在' })
        return
      }
      if (account.accessType === 'authorized') {
        res.status(403).json({ message: '授权实例不能重新验证来源账户 API Key 池' })
        return
      }
      if (account.configRevision !== parsed.data.expectedConfigRevision) {
        res.status(409).json({ message: '账户配置已被其他操作更新，请刷新后重试' })
        return
      }
      const revalidated = await revalidateAccountApiKeyRuntimePoolAsync({
        accountId: account.id,
        expectedConfigRevision: parsed.data.expectedConfigRevision
      })
      if (!revalidated.eligible) {
        const message = revalidateIneligibleMessage(revalidated.reason)
        res.status(409).json({
          message,
          code: 'ACCOUNT_API_KEY_RUNTIME_REVALIDATE_NOT_EXECUTABLE',
          reason: revalidated.reason ?? 'not_supported'
        })
        return
      }
      const result = await runLoggedOperationAsync(async () => {
        return {
          result: { id: account.id, configRevision: account.configRevision, changed: revalidated.changed },
          log: {
            operationScopeSystemAccountId: account.ownerSystemAccountId,
            mode: operationMode(requestAccess),
            module: 'accounts',
            action: 'api_key_runtime_revalidate',
            operationKey: 'accounts.api_key_runtime_revalidate',
            resourceType: 'account',
            resourceId: account.id,
            summary: `重新验证账户 API Key 池：${account.id}`,
            changes: [{ field: 'dueProbeKeys', label: '标记待探测 Key 数量', before: 0, after: revalidated.changed }],
            viewers: viewer(account.ownerSystemAccountId, 'resource_owner')
          }
        }
      }, req)
      res.json(ok(result))
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

function revalidateIneligibleMessage(reason?: string): string {
  if (reason === 'account_not_active') return '账户当前未启用，不能重新验证 Key 池'
  if (reason === 'account_unschedulable') return '账户当前不可调度，不能重新验证 Key 池'
  if (reason === 'config_revision_conflict') return '账户配置已被其他操作更新，请刷新后重试'
  if (reason === 'account_not_found') return '账户不存在或已删除'
  if (reason === 'no_revalidatable_key') return '当前账户没有可重新验证的不可用 Key'
  return '当前账户不是启用中的多 Key API Key 池'
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
