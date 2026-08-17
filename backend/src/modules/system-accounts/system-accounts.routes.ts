import { Router } from 'express'
import { z } from 'zod'

import { normalizeUserRequestLimitExpiresOn } from '../../domain/user-request-limits.js'
import { badRequest, ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText, queryTextList } from '../../shared/query-values.js'
import { rfc3339InstantSchema } from '../../shared/zod-rfc3339.js'
import { requireAdmin, requireSuperAdmin } from '../auth/auth.middleware.js'
import { hashPasswordAsync } from '../../storage/crypto.js'
import { createSystemAccountWithPasswordHashAsync, listSystemAccountOptionsAsync, listSystemAccountsPageAsync, patchSystemAccountManagementAsync, SystemAccountManagementPatchConflictError, type SystemAccountManagementPatchField } from '../../storage/repositories.js'
import { bodyField, mutationGuard, normalizedText } from '../deduplication/mutation-guard.middleware.js'
import { runLoggedOperationAsync, safeChange, viewer } from '../operation-logs/operation-log.service.js'

export const systemAccountsRouter = Router()
const whitespacePattern = /\s/
const requestLimitsSchema = z.object({
  perMinute: z.number().int().min(0).max(1_000_000_000).optional(),
  perDay: z.number().int().min(0).max(1_000_000_000).optional(),
  perWeek: z.number().int().min(0).max(1_000_000_000).optional(),
  perMonth: z.number().int().min(0).max(1_000_000_000).optional(),
  expiresOn: z.string().refine((value) => {
    try {
      normalizeUserRequestLimitExpiresOn(value)
      return true
    } catch {
      return false
    }
  }, 'expiresOn 必须是 YYYY-MM-DD 格式的有效日期').optional()
}).strict()

const createSchema = z.object({
  username: z.string().min(2),
  displayName: z.string().min(1),
  description: z.string().trim().max(200).nullable().optional(),
  password: z.string().min(4),
  role: z.enum(['admin', 'user']).optional(),
  status: z.enum(['active', 'disabled']).optional(),
  mustChangePassword: z.boolean().optional(),
  imageGenerationEnabled: z.boolean().optional(),
  requestLimits: requestLimitsSchema.nullable().optional()
}).strict()

const updateSchema = z.object({
  expectedUpdatedAt: rfc3339InstantSchema('系统账户编辑版本格式不正确'),
  displayName: z.string().min(1).optional(),
  description: z.string().trim().max(200).nullable().optional(),
  password: z.string().min(4).optional(),
  role: z.enum(['admin', 'user']).optional(),
  status: z.enum(['active', 'disabled']).optional(),
  mustChangePassword: z.boolean().optional(),
  imageGenerationEnabled: z.boolean().optional(),
  requestLimits: requestLimitsSchema.nullable().optional()
}).strict().refine((input) => Object.keys(input).some((key) => key !== 'expectedUpdatedAt'), '至少提交一个修改字段')

systemAccountsRouter.get('/', requireAdmin, async (req, res, next) => {
  try {
    res.json(ok(await listSystemAccountsPageAsync(parseSystemAccountListOptions(req.query))))
  } catch (error) {
    next(error)
  }
})

systemAccountsRouter.get('/options', requireAdmin, async (req, res, next) => {
  try {
    const query = parseSystemAccountOptionListOptions(req.query)
    const options = await listSystemAccountOptionsAsync(query)
    res.json(ok(options))
  } catch (error) {
    next(error)
  }
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

systemAccountsRouter.post('/', requireSuperAdmin, mutationGuard({
  operationKey: 'system_accounts.create',
  fingerprint: (req) => ({
    username: normalizedText(bodyField(req, 'username')),
    displayName: normalizedText(bodyField(req, 'displayName'))
  })
}), async (req, res) => {
  try {
    const parsed = createSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest('系统账户参数无效'))
      return
    }
    const whitespaceError = systemAccountWhitespaceError(parsed.data)
    if (whitespaceError) {
      res.status(400).json(badRequest(whitespaceError))
      return
    }
    const passwordHash = await hashPasswordAsync(parsed.data.password)
    const account = await runLoggedOperationAsync(async () => {
      const account = await createSystemAccountWithPasswordHashAsync(parsed.data, passwordHash)
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
            safeChange('imageGenerationEnabled', '支持图像生成', undefined, account.imageGenerationEnabled),
            safeChange('requestLimits', '用户请求限制', undefined, account.requestLimits),
            safeChange('password', '登录密码', undefined, parsed.data.password)
          ],
          viewers: viewer(account.id, 'admin_managed_my_resource')
        }
      }
    }, req)
    const { createdAt: _createdAt, updatedAt, ...listValues } = account
    res.status(201).json(ok({ ...listValues, editVersion: updatedAt }))
  } catch (error) {
    res.status(409).json({ message: error instanceof Error ? error.message : '创建系统账户失败' })
  }
})

systemAccountsRouter.patch('/:id', requireSuperAdmin, async (req, res, next) => {
  try {
    if (req.body && Object.prototype.hasOwnProperty.call(req.body, 'username')) {
      res.status(400).json(badRequest('用户账户创建后不能修改'))
      return
    }
    const parsed = updateSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest('系统账户参数无效'))
      return
    }
    const whitespaceError = systemAccountWhitespaceError(parsed.data)
    if (whitespaceError) {
      res.status(400).json(badRequest(whitespaceError))
      return
    }
    const { expectedUpdatedAt, ...patch } = parsed.data
    const passwordHash = patch.password ? await hashPasswordAsync(patch.password) : undefined
    const result = await runLoggedOperationAsync(async () => {
      const outcome = await patchSystemAccountManagementAsync(req.params.id, patch, expectedUpdatedAt, passwordHash)
      if (!outcome) {
        throw new Error('系统账户不存在')
      }
      return {
        result: outcome.result,
        log: outcome.changes.length ? {
          operationScopeSystemAccountId: outcome.result.id,
          mode: 'admin',
          module: 'system_accounts',
          action: patch.password ? 'reset_password' : 'update',
          operationKey: patch.password ? 'system_accounts.reset_password' : 'system_accounts.update',
          resourceType: 'system_account',
          resourceId: outcome.result.id,
          resourceName: outcome.resourceName,
          summary: patch.password ? `重置系统账户密码：${outcome.resourceName}` : `更新系统账户：${outcome.resourceName}`,
          changes: outcome.changes.map((change) => safeChange(change.field, systemAccountPatchFieldLabel(change.field), change.before, change.after)),
          viewers: viewer(outcome.result.id, 'admin_managed_my_resource')
        } : undefined
      }
    }, req)
    res.json(ok(result))
  } catch (error) {
    if (error instanceof Error && error.message === '系统账户不存在') {
      res.status(404).json({ message: '系统账户不存在' })
      return
    }
    if (error instanceof SystemAccountManagementPatchConflictError) {
      res.status(409).json({ message: error.message })
      return
    }
    res.status(409).json({ message: error instanceof Error ? error.message : '更新系统账户失败' })
  }
})

function systemAccountWhitespaceError(input: { username?: string; displayName?: string; password?: string }): string | undefined {
  if (input.username !== undefined && hasWhitespace(input.username)) return '用户名不能包含空格'
  if (input.displayName !== undefined && hasWhitespace(input.displayName)) return '用户名称不能包含空格'
  if (input.password !== undefined && hasWhitespace(input.password)) return '登录密码不能包含空格'
  return undefined
}

function hasWhitespace(value: string): boolean {
  return whitespacePattern.test(value)
}

function systemAccountPatchFieldLabel(field: SystemAccountManagementPatchField): string {
  return {
    displayName: '用户名称',
    description: '说明',
    password: '登录密码',
    role: '角色',
    status: '状态',
    mustChangePassword: '下次登录改密',
    imageGenerationEnabled: '支持图像生成',
    requestLimits: '用户请求限制'
  }[field]
}
