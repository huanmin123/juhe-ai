import { Router } from 'express'

import type { AccountSummary } from '../../domain/types.js'
import { migrateAccountTraffic } from '../../storage/repositories.js'
import { badRequest, ok } from '../../shared/http.js'
import { getRequestAccessScope, type RequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { migrateOpenAIAccountSessionAffinity } from '../gateway/runtime/session-affinity.service.js'
import { operationMode, resolveOperationOwner, runLoggedOperation, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import { accountTrafficMigrationSchema } from './account-request.schemas.js'
import { sanitizeAccountTrafficMigrationResponse } from './account-response-sanitizer.js'

export function registerAccountTrafficMigrationRoutes(router: Router): void {
  router.post('/:id/traffic-migration', (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const parsed = accountTrafficMigrationSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest('迁移流量参数无效'))
      return
    }

    try {
      let affinityResult = { migratedSessionCount: 0 }
      const migration = runLoggedOperation(() => {
        const migration = migrateAccountTraffic({
          sourceAccountId: req.params.id,
          targetAccountId: parsed.data.targetAccountId,
          sourceStatus: parsed.data.sourceStatus ?? 'temporary_unavailable'
        }, requestAccess)
        if (!migration) {
          throw new Error('账户不存在或无权迁移')
        }
        const ownerSystemAccountId = authorizedLocalOperationOwner(migration.sourceAccount, requestAccess)
          ?? resolveOperationOwner(migration.sourceAccount as unknown as Record<string, unknown>, requestAccess)
        return {
          result: migration,
          afterCommit: () => {
            const affinityScope = authorizedMigrationAffinityScope(migration.sourceAccount, requestAccess)
            affinityResult = migrateOpenAIAccountSessionAffinity(req.params.id, parsed.data.targetAccountId, affinityScope)
          },
          log: {
            operationScopeSystemAccountId: ownerSystemAccountId,
            mode: operationMode(requestAccess),
            module: 'accounts',
            action: 'traffic_migration',
            operationKey: 'accounts.traffic_migration',
            resourceType: 'account',
            resourceId: migration.sourceAccount.id,
            resourceName: migration.sourceAccount.name,
            summary: `迁移账户流量：${migration.sourceAccount.name} -> ${migration.targetAccount.name}`,
            changes: [
              safeChange('targetAccountId', '目标账户', undefined, migration.targetAccount.name),
              safeChange('sourceStatus', '源账户状态', undefined, parsed.data.sourceStatus ?? 'temporary_unavailable')
            ],
            targets: [
              {
                targetType: 'account',
                targetId: migration.targetAccount.id,
                targetName: migration.targetAccount.name,
                targetOwnerSystemAccountId: resolveOperationOwner(migration.targetAccount as unknown as Record<string, unknown>, requestAccess),
                relation: 'affected'
              }
            ],
            viewers: viewer(ownerSystemAccountId, 'resource_owner')
          }
        }
      }, req)
      res.json(ok(sanitizeAccountTrafficMigrationResponse({
        ...migration,
        ...affinityResult,
        sourceStatus: parsed.data.sourceStatus ?? 'temporary_unavailable'
      })))
    } catch (error) {
      if (error instanceof Error && error.message === '账户不存在或无权迁移') {
        res.status(404).json({ message: '账户不存在或无权迁移' })
        return
      }
      res.status(400).json(badRequest(error instanceof Error ? error.message : '迁移流量失败'))
    }
  })
}

function authorizedLocalOperationOwner(account: AccountSummary, access?: RequestAccessScope): string | undefined {
  return account.accessType === 'authorized' ? effectiveRequestSystemAccountId(access) : undefined
}

function authorizedMigrationAffinityScope(account: AccountSummary, access?: RequestAccessScope): { systemAccountId: string; groupId: string } | undefined {
  const systemAccountId = effectiveRequestSystemAccountId(access)
  return account.accessType === 'authorized' && account.boundGroupId && systemAccountId
    ? { systemAccountId, groupId: account.boundGroupId }
    : undefined
}

function effectiveRequestSystemAccountId(access?: RequestAccessScope): string | undefined {
  return access?.systemAccountFilterId?.trim() || access?.systemAccountId
}
