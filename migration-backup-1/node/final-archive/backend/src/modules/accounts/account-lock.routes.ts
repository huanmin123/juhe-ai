import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { mutationGuard, normalizedText, bodyField } from '../deduplication/mutation-guard.middleware.js'
import { operationMode, runLoggedOperationAsync, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import { setAccountLockAsync, updateAccountLockConfigAsync } from '../../storage/account-lock.repository.js'
import { accountLockSchema } from './account-request.schemas.js'

const accountLockNotFoundMessage = '账户不存在或无权操作'
const accountConfigConflictMessage = '账户配置已发生并发变更，请刷新列表后重试'
const accountLockStateConflictMessage = '账户锁死状态已发生并发变更，请刷新列表后重试'

export function accountLockMutationErrorStatus(error: unknown): 404 | 409 | undefined {
  if (!(error instanceof Error)) return undefined
  if (error.message === accountLockNotFoundMessage) return 404
  if (error.message === accountConfigConflictMessage || error.message === accountLockStateConflictMessage) return 409
  return undefined
}

export function registerAccountLockRoutes(router: Router): void {
  router.post('/:id/lock-config', mutationGuard({
    operationKey: 'accounts.lock-config',
    scope: (req) => normalizedText(req.query.systemAccountId),
    fingerprint: (req) => ({ accountId: normalizedText(req.params.id), timeout: bodyField(req, 'lockDeathTimeoutSeconds'), interval: bodyField(req, 'lockRetryIntervalSeconds') })
  }), async (req, res, next) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) { res.status(400).json(badRequest(scopeQuery.message)); return }
    const access = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const parsed = accountLockSchema.safeParse(req.body ?? {})
    if (!parsed.success || (parsed.data.lockDeathTimeoutSeconds === undefined && parsed.data.lockRetryIntervalSeconds === undefined)) {
      res.status(400).json(badRequest('请至少提交一项锁死配置'))
      return
    }
    try {
      const state = await runLoggedOperationAsync(async () => {
        const result = await updateAccountLockConfigAsync({ accountId: req.params.id, access, ...parsed.data })
        if (!result) throw new Error(accountLockNotFoundMessage)
        return {
          result,
          log: {
            operationScopeSystemAccountId: access?.systemAccountId ?? '',
            mode: operationMode(access), module: 'accounts', action: 'lock-config',
            operationKey: 'accounts.lock-config', resourceType: 'account', resourceId: result.accountId,
            summary: '更新 AI 账户锁死配置',
            changes: [
              safeChange('lockDeathTimeoutSeconds', '锁死死期', undefined, result.lockDeathTimeoutSeconds),
              safeChange('lockRetryIntervalSeconds', '锁死重试间隔', undefined, result.lockRetryIntervalSeconds)
            ],
            viewers: viewer(access?.systemAccountId ?? '', 'resource_owner')
          }
        }
      }, req)
      res.json(ok(state))
    } catch (error) {
      const status = accountLockMutationErrorStatus(error)
      if (status) {
        res.status(status).json(status === 409 ? badRequest((error as Error).message) : { message: (error as Error).message })
        return
      }
      next(error)
    }
  })
  for (const [action, enabled] of [['lock', true], ['unlock', false] ] as const) {
    router.post(`/:id/${action}`, mutationGuard({
      operationKey: `accounts.${action}`,
      scope: (req) => normalizedText(req.query.systemAccountId),
      fingerprint: (req) => ({ accountId: normalizedText(req.params.id), timeout: bodyField(req, 'lockDeathTimeoutSeconds'), interval: bodyField(req, 'lockRetryIntervalSeconds') })
    }), async (req, res, next) => {
      const scopeQuery = parseRequestScopeQuery(req.query)
      if (!scopeQuery.success) { res.status(400).json(badRequest(scopeQuery.message)); return }
      const access = getRequestAccessScope(scopeQuery.data.systemAccountId)
      const parsed = accountLockSchema.safeParse(req.body ?? {})
      if (!parsed.success) { res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '锁死参数无效')); return }
      try {
        const state = await runLoggedOperationAsync(async () => {
          const result = await setAccountLockAsync({ accountId: req.params.id, enabled, access, ...parsed.data })
          if (!result) throw new Error(accountLockNotFoundMessage)
          return {
            result,
            log: {
              operationScopeSystemAccountId: access?.systemAccountId ?? '',
              mode: operationMode(access), module: 'accounts', action,
              operationKey: `accounts.${action}`, resourceType: 'account', resourceId: result.accountId,
              summary: `${enabled ? '锁死' : '解除锁死'} AI 账户`,
              changes: [safeChange('lockState', '锁死状态', enabled ? 'UNLOCKED' : 'LOCKED_IDLE', result.lockState)],
              viewers: viewer(access?.systemAccountId ?? '', 'resource_owner')
            }
          }
        }, req)
        res.json(ok(state))
      } catch (error) {
        const status = accountLockMutationErrorStatus(error)
        if (status) {
          res.status(status).json(status === 409 ? badRequest((error as Error).message) : { message: (error as Error).message })
          return
        }
        next(error)
      }
    })
  }
}
