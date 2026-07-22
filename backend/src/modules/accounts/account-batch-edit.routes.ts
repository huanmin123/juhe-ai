import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import {
  AccountBatchUpdateAccessError,
  AccountBatchUpdateVersionConflictError
} from '../../storage/account-batch-update.repository.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import {
  operationMode,
  ownerTarget,
  runLoggedOperationAsync,
  safeChange,
  viewer
} from '../operation-logs/operation-log.service.js'
import { accountBatchEditContextSchema, accountBatchEditSchema } from './account-request.schemas.js'
import { sanitizeAccountBatchEditDetailResponse, sanitizeAccountResponse } from './account-response-sanitizer.js'
import { batchEditAccountsAsync, loadAccountBatchEditContextAsync } from './account-batch-edit.service.js'

export function registerAccountBatchEditRoutes(router: Router): void {
  router.post('/batch-edit-context', async (req, res) => {
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
    const parsed = accountBatchEditContextSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '批量编辑上下文参数无效'))
      return
    }
    try {
      const accounts = await loadAccountBatchEditContextAsync(parsed.data.accountIds, requestAccess)
      res.json(ok(accounts.map(sanitizeAccountBatchEditDetailResponse)))
    } catch (error) {
      if (error instanceof AccountBatchUpdateAccessError) {
        const status = error.message.includes('同一系统账户作用域') ? 400 : 404
        res.status(status).json({ message: error.message })
        return
      }
      res.status(400).json(badRequest(error instanceof Error ? error.message : '获取批量编辑上下文失败'))
    }
  })

  router.post('/batch-update', async (req, res) => {
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
    const parsed = accountBatchEditSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '批量编辑参数无效'))
      return
    }
    try {
      const result = await runLoggedOperationAsync(async () => {
        const result = await batchEditAccountsAsync(parsed.data, requestAccess)
        const ownerSystemAccountId = result.accounts[0]?.ownerSystemAccountId
          ?? result.accounts[0]?.systemAccountId
          ?? requestAccess.systemAccountFilterId
          ?? requestAccess.systemAccountId
        return {
          result,
          log: {
            operationScopeSystemAccountId: ownerSystemAccountId,
            mode: operationMode(requestAccess),
            module: 'accounts',
            action: 'batch_update',
            operationKey: 'accounts.batch_update',
            resourceType: 'account_batch',
            resourceId: result.batchId,
            resourceName: `${result.accounts.length} 个 AI 账户`,
            summary: `批量更新 ${result.accounts.length} 个 AI 账户`,
            changes: [
              safeChange('batchUpdateFields', '批量覆盖字段', [], result.changedFields)
            ],
            targets: result.accounts.map((account) => ownerTarget({
              targetType: 'account',
              targetId: account.id,
              targetName: account.name,
              ownerSystemAccountId: account.ownerSystemAccountId ?? account.systemAccountId,
              relation: 'affected'
            })),
            viewers: viewer(ownerSystemAccountId, 'resource_owner')
          }
        }
      }, req)
      res.json(ok({
        ...result,
        accounts: result.accounts.map(sanitizeAccountResponse)
      }))
    } catch (error) {
      if (error instanceof AccountBatchUpdateVersionConflictError) {
        res.status(409).json({ message: error.message })
        return
      }
      if (error instanceof AccountBatchUpdateAccessError) {
        const status = error.message.includes('同一系统账户作用域') ? 400 : 404
        res.status(status).json({ message: error.message })
        return
      }
      res.status(400).json(badRequest(error instanceof Error ? error.message : '批量编辑账户失败'))
    }
  })
}
