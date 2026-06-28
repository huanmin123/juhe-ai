import type { Router } from 'express'

import type { AccountSummary } from '../../domain/types.js'
import { badRequest, ok } from '../../shared/http.js'
import { AccountTagInUseError, deleteAccountTagAsync, findAccountSummaryAsync, listAccountTagsAsync, updateAccountTagsAsync } from '../../storage/repositories.js'
import { getRequestAccessScope, type RequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { operationMode, resolveOperationOwner, runLoggedOperationAsync, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import { accountTagsUpdateSchema } from './account-request.schemas.js'
import { sanitizeAccountResponse } from './account-response-sanitizer.js'

export function registerAccountTagsRoutes(router: Router): void {
  router.get('/tags', async (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    try {
      res.json(ok(await listAccountTagsAsync(getRequestAccessScope(scopeQuery.data.systemAccountId))))
    } catch (error) {
      res.status(400).json(badRequest(error instanceof Error ? error.message : '加载账户标签失败'))
    }
  })

  router.delete('/tags/:tagId', async (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    try {
      if (!(await deleteAccountTagAsync(req.params.tagId, getRequestAccessScope(scopeQuery.data.systemAccountId)))) {
        res.status(404).json({ message: '标签不存在' })
        return
      }
      res.status(204).send()
    } catch (error) {
      if (error instanceof AccountTagInUseError) {
        res.status(400).json(badRequest(error.message))
        return
      }
      res.status(400).json(badRequest(error instanceof Error ? error.message : '删除账户标签失败'))
    }
  })

  router.patch('/:id/tags', async (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const parsed = accountTagsUpdateSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '账户标签参数无效'))
      return
    }
    const before = await findAccountSummaryAsync(req.params.id, requestAccess)
    if (!before) {
      res.status(404).json({ message: '账户不存在' })
      return
    }
    try {
      const account = await runLoggedOperationAsync(async () => {
        const tags = await updateAccountTagsAsync(req.params.id, parsed.data.tags, requestAccess)
        if (!tags) {
          throw new Error('账户不存在')
        }
        const account = await findAccountSummaryAsync(req.params.id, requestAccess)
        if (!account) {
          throw new Error('账户不存在')
        }
        const ownerSystemAccountId = authorizedLocalOperationOwner(account, requestAccess)
          ?? resolveOperationOwner(account as unknown as Record<string, unknown>, requestAccess)
        return {
          result: { ...account, tags },
          log: {
            operationScopeSystemAccountId: ownerSystemAccountId,
            mode: operationMode(requestAccess),
            module: 'accounts',
            action: 'update_tags',
            operationKey: 'accounts.update_tags',
            resourceType: 'account',
            resourceId: account.id,
            resourceName: account.name,
            summary: `更新账户标签：${account.name}`,
            changes: [
              safeChange('tags', '标签', before?.tags, tags)
            ],
            viewers: viewer(ownerSystemAccountId, 'resource_owner')
          }
        }
      }, req)
      res.json(ok(sanitizeAccountResponse(account)))
    } catch (error) {
      if (error instanceof Error && error.message === '账户不存在') {
        res.status(404).json({ message: '账户不存在' })
        return
      }
      res.status(400).json(badRequest(error instanceof Error ? error.message : '更新账户标签失败'))
    }
  })
}

function authorizedLocalOperationOwner(account: AccountSummary, access?: RequestAccessScope): string | undefined {
  return account.accessType === 'authorized' ? effectiveRequestSystemAccountId(access) : undefined
}

function effectiveRequestSystemAccountId(access?: RequestAccessScope): string | undefined {
  return access?.systemAccountFilterId?.trim() || access?.systemAccountId
}
