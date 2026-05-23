import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText, queryTextList } from '../../shared/query-values.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { createSystemAccount, findSystemAccountById, listSystemAccountOptions, listSystemAccounts, listSystemAccountsPage, revokeAllSessionsForAccount, updateSystemAccount } from '../../storage/repositories.js'
import { bodyField, mutationGuard, normalizedText } from '../deduplication/mutation-guard.middleware.js'
import { diffSafeFields, runLoggedOperation, safeChange, viewer } from '../operation-logs/operation-log.service.js'

export const systemAccountsRouter = Router()

const createSchema = z.object({
  username: z.string().trim().min(2),
  displayName: z.string().trim().min(1),
  description: z.string().trim().max(200).nullable().optional(),
  password: z.string().min(4),
  role: z.enum(['admin', 'user']).optional(),
  status: z.enum(['active', 'disabled']).optional(),
  mustChangePassword: z.boolean().optional()
})

const updateSchema = z.object({
  displayName: z.string().trim().min(1).optional(),
  description: z.string().trim().max(200).nullable().optional(),
  password: z.string().min(4).optional(),
  role: z.enum(['admin', 'user']).optional(),
  status: z.enum(['active', 'disabled']).optional(),
  mustChangePassword: z.boolean().optional()
})

systemAccountsRouter.get('/', requireAdmin, (req, res) => {
  if (hasSystemAccountPageQuery(req.query)) {
    res.json(ok(listSystemAccountsPage(parseSystemAccountListOptions(req.query))))
    return
  }
  res.json(ok(listSystemAccounts()))
})

systemAccountsRouter.get('/options', requireAdmin, (req, res) => {
  res.json(ok(listSystemAccountOptions(parseSystemAccountOptionListOptions(req.query))))
})

function parseSystemAccountOptionListOptions(query: Record<string, unknown>) {
  return {
    ids: queryTextList(query.ids, 50),
    keyword: optionalQueryText(query.keyword),
    limit: optionLimitValue(integerQueryValue(query.limit))
  }
}

function optionLimitValue(value: number | undefined): number {
  return typeof value === 'number' ? Math.min(50, Math.max(1, value)) : 50
}

function parseSystemAccountListOptions(query: Record<string, unknown>) {
  return {
    keyword: optionalQueryText(query.keyword),
    page: integerQueryValue(query.page),
    pageSize: integerQueryValue(query.pageSize)
  }
}

function hasSystemAccountPageQuery(query: Record<string, unknown>): boolean {
  return query.page !== undefined || query.pageSize !== undefined || query.limit !== undefined
}

systemAccountsRouter.post('/', requireAdmin, mutationGuard({
  operationKey: 'system_accounts.create',
  fingerprint: (req) => ({
    username: normalizedText(bodyField(req, 'username')),
    displayName: normalizedText(bodyField(req, 'displayName'))
  })
}), (req, res) => {
  const parsed = createSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('系统账户参数无效'))
    return
  }
  try {
    const account = runLoggedOperation(() => {
      const account = createSystemAccount(parsed.data)
      return {
        result: account,
        log: {
          operationScopeSystemAccountId: account.id,
          mode: 'admin',
          module: 'system_accounts',
          action: 'create',
          operationKey: 'system_accounts.create',
          resourceType: 'system_account',
          resourceId: account.id,
          resourceName: account.displayName,
          summary: `创建系统账户：${account.displayName}`,
          changes: [
            safeChange('username', '用户账户', undefined, account.username),
            safeChange('displayName', '用户名称', undefined, account.displayName),
            safeChange('role', '角色', undefined, account.role),
            safeChange('status', '状态', undefined, account.status),
            safeChange('password', '登录密码', undefined, parsed.data.password)
          ],
          viewers: viewer(account.id, 'admin_managed_my_resource')
        }
      }
    }, req)
    res.status(201).json(ok(account))
  } catch (error) {
    res.status(409).json({ message: error instanceof Error ? error.message : '创建系统账户失败' })
  }
})

systemAccountsRouter.patch('/:id', requireAdmin, (req, res) => {
  if (req.body && Object.prototype.hasOwnProperty.call(req.body, 'username')) {
    res.status(400).json(badRequest('用户账户创建后不能修改'))
    return
  }
  const parsed = updateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('系统账户参数无效'))
    return
  }
  try {
    const before = findSystemAccountById(req.params.id)
    const account = runLoggedOperation(() => {
      const account = updateSystemAccount(req.params.id, parsed.data)
      if (!account) {
        throw new Error('系统账户不存在')
      }
      if (parsed.data.status === 'disabled' || parsed.data.password) {
        revokeAllSessionsForAccount(req.params.id)
      }
      return {
        result: account,
        log: {
          operationScopeSystemAccountId: account.id,
          mode: 'admin',
          module: 'system_accounts',
          action: parsed.data.password ? 'reset_password' : 'update',
          operationKey: parsed.data.password ? 'system_accounts.reset_password' : 'system_accounts.update',
          resourceType: 'system_account',
          resourceId: account.id,
          resourceName: account.displayName,
          summary: parsed.data.password ? `重置系统账户密码：${account.displayName}` : `更新系统账户：${account.displayName}`,
          changes: [
            ...diffSafeFields(before as unknown as Record<string, unknown> | undefined, account as unknown as Record<string, unknown>, {
              displayName: '用户名称',
              description: '说明',
              role: '角色',
              status: '状态',
              mustChangePassword: '下次登录改密'
            }),
            ...(parsed.data.password ? [safeChange('password', '登录密码', undefined, parsed.data.password)] : [])
          ],
          viewers: viewer(account.id, 'admin_managed_my_resource')
        }
      }
    }, req)
    res.json(ok(account))
  } catch (error) {
    if (error instanceof Error && error.message === '系统账户不存在') {
      res.status(404).json({ message: '系统账户不存在' })
      return
    }
    res.status(409).json({ message: error instanceof Error ? error.message : '更新系统账户失败' })
  }
})
