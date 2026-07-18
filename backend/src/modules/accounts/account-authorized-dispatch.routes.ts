import type { Router } from 'express'

import type { AccountSummary } from '../../domain/types.js'
import { badRequest, ok } from '../../shared/http.js'
import { updateAuthorizedAccountBindingDispatchAsync } from '../../storage/repositories.js'
import { getRequestAccessScope, type RequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { clearServerAccountRuntimeAvailability } from '../db-service/db-service-ipc.js'
import { applyServerAccountRuntimeToAccount } from '../gateway/runtime/runtime-snapshot.service.js'
import { operationMode, runLoggedOperationAsync, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import { accountPageDataOwnerIds, publishAccountRuntimeChange, publishAccountStaticChange } from '../page-data/page-data-change.publisher.js'
import { authorizedAccountDispatchSchema } from './account-request.schemas.js'
import { sanitizeAccountResponse } from './account-response-sanitizer.js'

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
      res.status(400).json(badRequest('授权账户调度参数无效'))
      return
    }
    try {
      const account = await runLoggedOperationAsync(async () => {
        const account = await updateAuthorizedAccountBindingDispatchAsync(req.params.id, parsed.data, requestAccess)
        if (!account) {
          throw new Error('授权账户不存在或尚未绑定分组')
        }
        const ownerSystemAccountId = effectiveRequestSystemAccountId(requestAccess)
        return {
          result: account,
          log: {
            operationScopeSystemAccountId: ownerSystemAccountId,
            mode: operationMode(requestAccess),
            module: 'accounts',
            action: 'authorized_dispatch',
            operationKey: 'accounts.authorized_dispatch',
            resourceType: 'account',
            resourceId: account.id,
            resourceName: account.name,
            summary: `调整授权账户使用设置：${account.name}`,
            changes: [
              ...(Object.prototype.hasOwnProperty.call(parsed.data, 'status') ? [safeChange('status', '实例状态', undefined, parsed.data.status)] : []),
              ...(Object.prototype.hasOwnProperty.call(parsed.data, 'priority') ? [safeChange('priority', '分组内优先级', undefined, parsed.data.priority)] : []),
              ...(Object.prototype.hasOwnProperty.call(parsed.data, 'superPriorityEnabled') ? [safeChange('superPriorityEnabled', '分组内超级优先', undefined, parsed.data.superPriorityEnabled)] : []),
              ...(Object.prototype.hasOwnProperty.call(parsed.data, 'fallbackEnabled') ? [safeChange('fallbackEnabled', '分组内降级备用', undefined, parsed.data.fallbackEnabled)] : []),
              ...(parsed.data.clearFailureState === true ? [safeChange('clearFailureState', '恢复实例异常状态', false, true)] : [])
            ],
            viewers: viewer(ownerSystemAccountId, 'resource_owner')
          }
        }
      }, req)
      if (!account) {
        res.status(404).json({ message: '授权账户不存在或尚未绑定分组' })
        return
      }
      if (parsed.data.clearFailureState === true || parsed.data.status === 'active') {
        await clearAccountGatewayRuntimeAfterRestore(account, requestAccess)
      }
      const ownerSystemAccountIds = accountPageDataOwnerIds(account, effectiveRequestSystemAccountId(requestAccess))
      await publishAccountStaticChange({
        accountId: account.id,
        ownerSystemAccountIds,
        fieldMask: Object.keys(parsed.data),
        orderChanged: Object.prototype.hasOwnProperty.call(parsed.data, 'priority')
      })
      if (parsed.data.clearFailureState === true || Object.prototype.hasOwnProperty.call(parsed.data, 'status')) {
        await publishAccountRuntimeChange({ accountId: account.id, ownerSystemAccountIds, fieldMask: ['status', 'schedulable'] })
      }
      res.json(ok(sanitizeAccountResponse(await applyServerAccountRuntimeToAccount(account))))
    } catch (error) {
      res.status(400).json(badRequest(error instanceof Error ? error.message : '更新授权账户调度设置失败'))
    }
  })
}

async function clearAccountGatewayRuntimeAfterRestore(account: AccountSummary, access?: RequestAccessScope): Promise<void> {
  const systemAccountId = account.accessType === 'authorized'
    ? account.bindingSystemAccountId ?? effectiveRequestSystemAccountId(access)
    : undefined
  await clearServerAccountRuntimeAvailability({
    accountId: account.id,
    authorizedBinding: account.accessType === 'authorized' && systemAccountId && account.boundGroupId && account.accountAuthorizationId
      ? {
          systemAccountId,
          groupId: account.boundGroupId,
          accountAuthorizationId: account.accountAuthorizationId
        }
      : undefined
  }).catch(() => undefined)
}

function effectiveRequestSystemAccountId(access?: RequestAccessScope): string | undefined {
  return access?.systemAccountFilterId?.trim() || access?.systemAccountId
}
