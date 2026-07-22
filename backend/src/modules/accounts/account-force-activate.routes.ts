import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import { findAccountSummaryAsync, forceActivatePendingAccountAsync } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { clearServerAccountRuntimeAvailability } from '../db-service/db-service-ipc.js'
import { mutationGuard, normalizedText, queryField } from '../deduplication/mutation-guard.middleware.js'
import { operationMode, resolveOperationOwner, runLoggedOperationAsync, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import { sanitizeAccountResponse } from './account-response-sanitizer.js'

export function registerAccountForceActivateRoutes(router: Router): void {
  router.post('/:id/force-activate', mutationGuard({
    operationKey: 'accounts.force_activate_pending',
    scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
    fingerprint: (req) => ({
      accountId: normalizedText(req.params.id),
      acknowledgedAccountAvailable: req.body?.acknowledgedAccountAvailable === true
    })
  }), async (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    if (req.body?.acknowledgedAccountAvailable !== true) {
      res.status(400).json(badRequest('请先确认账户当前可用并接受人工恢复风险'))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const before = await findAccountSummaryAsync(req.params.id, requestAccess)
    if (!before) {
      res.status(404).json({ message: '账户不存在' })
      return
    }
    if (before.accessType === 'authorized') {
      res.status(400).json(badRequest('授权账户不能人工恢复来源账户状态'))
      return
    }
    if (before.status !== 'pending_test') {
      res.status(409).json({ message: '只有待检查账户可以人工恢复正常' })
      return
    }
    try {
      const account = await runLoggedOperationAsync(async () => {
        const result = await forceActivatePendingAccountAsync(before.id, requestAccess)
        if (!result.changed || !result.account) {
          throw new Error('账户状态已变化，请刷新后重试')
        }
        const ownerSystemAccountId = resolveOperationOwner(result.account as unknown as Record<string, unknown>, requestAccess)
        return {
          result: result.account,
          log: {
            operationScopeSystemAccountId: ownerSystemAccountId,
            mode: operationMode(requestAccess),
            module: 'accounts',
            action: 'force_activate',
            operationKey: 'accounts.force_activate_pending',
            resourceType: 'account',
            resourceId: result.account.id,
            resourceName: result.account.name,
            summary: `人工恢复待检查 AI 账户：${result.account.name}`,
            changes: [
              safeChange('status', '状态', before.status, result.account.status),
              safeChange('acknowledgedAccountAvailable', '确认账户当前可用', false, true)
            ],
            viewers: viewer(ownerSystemAccountId, 'resource_owner')
          }
        }
      }, req)
      await clearServerAccountRuntimeAvailability({ accountId: account.id }).catch(() => undefined)
      res.json(ok(sanitizeAccountResponse(account)))
    } catch (error) {
      const message = error instanceof Error ? error.message : '人工恢复账户失败'
      res.status(message.includes('状态已变化') ? 409 : 400).json(badRequest(message))
    }
  })
}
