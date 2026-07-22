import type { Router } from 'express'

import { isAdminRole } from '../../domain/types.js'
import { badRequest, ok } from '../../shared/http.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { operationMode, recordOperationLogAsync, resolveOperationOwner, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import { accountExportRequestSchema, exportAccountsForRequestAsync } from './account-export-request.js'
import { accountImportMaxAccounts } from './account-import.service.js'

export function registerAccountExportRoutes(router: Router): void {
  router.post('/export', async (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const parsed = accountExportRequestSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(`账户导出参数无效，单次最多导出 ${accountImportMaxAccounts} 个账户`))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    if (!requestAccess) {
      res.status(401).json(badRequest('缺少系统账户上下文'))
      return
    }
    try {
      const result = await exportAccountsForRequestAsync(parsed.data, requestAccess)
      const ownerSystemAccountId = resolveOperationOwner(undefined, requestAccess)
      const matchedText = typeof result.summary.matchedAccounts === 'number' ? `，匹配 ${result.summary.matchedAccounts} 条` : ''
      const truncatedText = result.summary.truncated ? `，仅处理前 ${accountImportMaxAccounts} 条` : ''
      await recordOperationLogAsync({
        operationScopeSystemAccountId: ownerSystemAccountId,
        mode: operationMode(requestAccess),
        module: 'accounts',
        action: 'export',
        operationKey: 'accounts.export',
        resourceType: 'account',
        resourceName: 'AI 账户导出',
        summary: `导出 AI 账户：${result.summary.accounts} 个账户，${result.summary.proxies} 个代理${matchedText}${truncatedText}`,
        visibilityScope: isAdminRole(requestAccess.role) ? 'admin_only' : 'targeted',
        changes: [
          safeChange('accountExported', '导出账户数', undefined, result.summary.accounts),
          safeChange('proxyExported', '导出代理数', undefined, result.summary.proxies),
          safeChange('accountSkipped', '跳过账户数', undefined, result.summary.skippedAccounts),
          ...(typeof result.summary.matchedAccounts === 'number'
            ? [safeChange('accountMatched', '匹配账户数', undefined, result.summary.matchedAccounts)]
            : []),
          ...(result.summary.truncated
            ? [safeChange('accountExportTruncated', '导出结果截断', false, true)]
            : [])
        ],
        ...(!isAdminRole(requestAccess.role) ? { viewers: viewer(ownerSystemAccountId, 'resource_owner') } : {})
      }, req)
      res.json(ok(result))
    } catch (error) {
      res.status(400).json(badRequest(error instanceof Error ? error.message : '导出账户失败'))
    }
  })
}
