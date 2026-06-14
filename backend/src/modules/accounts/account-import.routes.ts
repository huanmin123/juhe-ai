import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { bodyField, mutationGuard, normalizedText, queryField } from '../deduplication/mutation-guard.middleware.js'
import { operationMode, resolveOperationOwner, runLoggedOperation, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import { executeAccountImport, previewAccountImport, type AccountImportOptions } from './account-import.service.js'
import { accountImportRequestSchema } from './account-request.schemas.js'

export function registerAccountImportRoutes(router: Router): void {
  router.post('/import/preview', (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const parsed = accountImportRequestSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest('账户导入参数无效'))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    res.json(ok(previewAccountImport(parsed.data.data, parsed.data.options, requestAccess)))
  })

  router.post('/import/confirm', mutationGuard({
    operationKey: 'accounts.import',
    scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
    fingerprint: (req) => ({
      owner: normalizedText(queryField(req, 'systemAccountId')),
      data: bodyField(req, 'data'),
      options: bodyField(req, 'options')
    })
  }), (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const parsed = accountImportRequestSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest('账户导入参数无效'))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    if (!requestAccess) {
      res.status(401).json(badRequest('缺少系统账户上下文'))
      return
    }
    const importOptions: AccountImportOptions = parsed.data.options ?? {}
    const result = runLoggedOperation(() => {
      const result = executeAccountImport(parsed.data.data, importOptions, requestAccess)
      const ownerSystemAccountId = resolveOperationOwner(undefined, requestAccess)
      return {
        result,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'accounts',
          action: 'import',
          operationKey: 'accounts.import',
          resourceType: 'account',
          resourceName: 'AI 账户导入',
          summary: `导入 AI 账户：创建 ${result.summary.accounts.create} 个，跳过 ${result.summary.accounts.skip} 个，失败 ${result.summary.accounts.failed} 个`,
          changes: [
            safeChange('accountCreated', '创建账户数', undefined, result.summary.accounts.create),
            safeChange('accountSkipped', '跳过账户数', undefined, result.summary.accounts.skip),
            safeChange('accountFailed', '失败账户数', undefined, result.summary.accounts.failed),
            safeChange('proxyCreated', '创建代理数', undefined, result.summary.proxies.create),
            safeChange('groupCreated', '创建分组数', undefined, result.summary.groups.create)
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    res.json(ok(result))
  })
}
