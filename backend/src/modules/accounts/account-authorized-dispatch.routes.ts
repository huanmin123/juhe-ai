import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import {
  AuthorizedAccountDispatchRevisionConflictError,
  updateAuthorizedAccountBindingDispatchAsync,
  type AuthorizedAccountDispatchMutationResult
} from '../../storage/repositories.js'
import { getRequestAccessScope, type RequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { clearServerAccountRuntimeAvailability } from '../db-service/db-service-ipc.js'
import { operationMode, runLoggedOperationAsync, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import { authorizedAccountDispatchSchema } from './account-request.schemas.js'

export function registerAccountAuthorizedDispatchRoutes(router: Router): void {
  router.patch('/:id/authorized-dispatch', async (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const parsed = authorizedAccountDispatchSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '授权账户调度参数无效'))
      return
    }
    try {
      const account = await runLoggedOperationAsync(async () => {
        const patched = await updateAuthorizedAccountBindingDispatchAsync(req.params.id, parsed.data, requestAccess)
        if (!patched) {
          throw new Error('授权账户不存在或尚未绑定分组')
        }
        const ownerSystemAccountId = patched.ownerSystemAccountId || effectiveRequestSystemAccountId(requestAccess)
        return {
          result: patched,
          log: patched.changedFields.length > 0 ? {
            operationScopeSystemAccountId: ownerSystemAccountId,
            mode: operationMode(requestAccess),
            module: 'accounts',
            action: 'authorized_dispatch',
            operationKey: 'accounts.authorized_dispatch',
            resourceType: 'account',
            resourceId: patched.id,
            resourceName: patched.name,
            summary: `调整授权账户使用设置：${patched.name}`,
            changes: patched.changes.map((change) => safeChange(
              change.field,
              authorizedDispatchChangeLabel(change.field),
              change.before,
              change.after
            )),
            viewers: viewer(ownerSystemAccountId, 'resource_owner')
          } : undefined
        }
      }, req)
      if (account.runtimeRestoreRequired) {
        await clearAccountGatewayRuntimeAfterRestore(account, requestAccess)
      }
      res.json(ok({
        id: account.id,
        configRevision: account.configRevision,
        changedFields: account.changedFields,
        patch: account.patch
      }))
    } catch (error) {
      if (error instanceof AuthorizedAccountDispatchRevisionConflictError) {
        res.status(409).json(badRequest('账户配置已被其他操作更新，请刷新后重试'))
        return
      }
      const message = error instanceof Error ? error.message : '更新授权账户调度设置失败'
      if (message === '授权账户不存在或尚未绑定分组') {
        res.status(404).json({ message })
        return
      }
      res.status(400).json(badRequest(message))
    }
  })
}

async function clearAccountGatewayRuntimeAfterRestore(
  account: Pick<AuthorizedAccountDispatchMutationResult, 'id' | 'authorizedBinding'>,
  _access?: RequestAccessScope
): Promise<void> {
  await clearServerAccountRuntimeAvailability({
    accountId: account.id,
    authorizedBinding: account.authorizedBinding
  }).catch(() => undefined)
}

function authorizedDispatchChangeLabel(field: string): string {
  return ({
    status: '实例状态',
    schedulable: '参与调度',
    priority: '分组内优先级',
    superPriorityEnabled: '分组内超级优先',
    fallbackEnabled: '分组内降级备用',
    failureState: '恢复实例异常状态'
  } as Record<string, string>)[field] ?? field
}

function effectiveRequestSystemAccountId(access?: RequestAccessScope): string | undefined {
  return access?.systemAccountFilterId?.trim() || access?.systemAccountId
}
