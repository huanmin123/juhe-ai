import { Router, type NextFunction, type Request, type Response } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { sessionCookieOptions } from '../../shared/http-security.js'
import { createSessionAsync, findSessionByTokenAsync, findSystemAccountById, revokeOtherSessionsForAccount, revokeSessionAsync, touchSessionAsync, updateSystemAccount, updateSystemAccountAsync, updateSystemAccountLastLoginAsync, verifySystemAccountCredentialsAsync } from '../../storage/repositories.js'
import { recordOperationLog, safeChange } from '../operation-logs/operation-log.service.js'
import { consumeCaptchaIssueAllowance, createCaptchaChallenge, verifyCaptchaChallenge } from './captcha.service.js'
import { checkLoginAllowedAsync, getLoginClientIp, recordFailedLoginAsync, recordSuccessfulLoginAsync } from './login-guard.service.js'
import { getRequestAuthContext, withRequestAuthContext } from './request-context.js'

export const authRouter = Router()

const sessionCookieName = 'juhe_ai_session'
const sessionMaxAgeMs = 14 * 24 * 60 * 60 * 1000
const whitespacePattern = /\s/

const loginSchema = z.object({
  username: z.string().min(1),
  password: z.string().min(1),
  captchaId: z.string().trim().min(1),
  captchaCode: z.string().trim().min(1)
}).strict()

const passwordSchema = z.object({
  oldPassword: z.string().min(1).optional(),
  newPassword: z.string().min(4)
}).strict()

const profileSchema = z.object({
  displayName: z.string().min(1)
}).strict()

authRouter.get('/captcha', (req, res) => {
  const clientIp = getLoginClientIp(req)
  const issueAllowed = consumeCaptchaIssueAllowance(clientIp)
  if (issueAllowed.blocked) {
    if (issueAllowed.retryAfterSeconds) {
      res.setHeader('Retry-After', String(issueAllowed.retryAfterSeconds))
    }
    res.status(429).json({ message: issueAllowed.message ?? '验证码请求过于频繁，请稍后再试' })
    return
  }
  res.json(ok(createCaptchaChallenge()))
})

authRouter.post('/login', async (req, res, next) => {
  try {
    const parsed = loginSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest('登录参数无效'))
      return
    }

    const clientIp = getLoginClientIp(req)

    if (hasWhitespace(parsed.data.username) || hasWhitespace(parsed.data.password)) {
      res.status(400).json(badRequest('用户名和密码不能包含空格'))
      return
    }

    if (!verifyCaptchaChallenge(parsed.data.captchaId, parsed.data.captchaCode)) {
      res.status(400).json({ message: '验证码错误或已过期' })
      return
    }

    const loginAllowed = await checkLoginAllowedAsync(clientIp, parsed.data.username)
    if (loginAllowed.blocked) {
      if (loginAllowed.retryAfterSeconds) {
        res.setHeader('Retry-After', String(loginAllowed.retryAfterSeconds))
      }
      res.status(429).json({ message: loginAllowed.message ?? '尝试过于频繁，请稍后再试' })
      return
    }

    const account = await verifySystemAccountCredentialsAsync(parsed.data.username, parsed.data.password)
    if (!account) {
      const loginBlock = await recordFailedLoginAsync(clientIp, parsed.data.username)
      if (loginBlock.blocked) {
        if (loginBlock.retryAfterSeconds) {
          res.setHeader('Retry-After', String(loginBlock.retryAfterSeconds))
        }
        res.status(429).json({ message: loginBlock.message ?? '尝试过于频繁，请稍后再试' })
        return
      }
      res.status(401).json({ message: '账号或密码错误' })
      return
    }

    await recordSuccessfulLoginAsync(clientIp, account.username)
    const session = await createSessionAsync(account.id)
    await updateSystemAccountLastLoginAsync(account.id)
    res.cookie(sessionCookieName, session.token, sessionCookieOptions({ maxAge: sessionMaxAgeMs }))
    res.json(ok({ ...account, lastLoginAt: new Date().toISOString() }))
  } catch (error) {
    next(error)
  }
})

authRouter.post('/logout', async (req, res, next) => {
  try {
    const token = parseCookie(req.headers.cookie ?? '')[sessionCookieName]
    if (token) {
      await revokeSessionAsync(token)
    }
    clearSessionCookie(res)
    res.json(ok({ loggedOut: true }))
  } catch (error) {
    next(error)
  }
})

authRouter.get('/me', requireSessionContext, (_req, res) => {
  const context = getRequestAuthContext()
  if (!context) {
    res.status(401).json({ message: '请先登录' })
    return
  }
  res.json(ok({
    id: context.systemAccountId,
    username: context.username,
    displayName: context.displayName,
    role: context.role,
    mustChangePassword: context.mustChangePassword
  }))
})

authRouter.patch('/me', requireSessionContext, (req, res) => {
  const context = getRequestAuthContext()
  if (!context) {
    res.status(401).json({ message: '请先登录' })
    return
  }
  if (context.mustChangePassword) {
    res.status(403).json({ message: '请先修改初始密码', code: 'must_change_password' })
    return
  }
  const parsed = profileSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('用户资料参数无效'))
    return
  }
  if (hasWhitespace(parsed.data.displayName)) {
    res.status(400).json(badRequest('显示名称不能包含空格'))
    return
  }
  const displayName = parsed.data.displayName.trim()
  const before = findSystemAccountById(context.systemAccountId)
  if (!before) {
    res.status(404).json({ message: '系统账户不存在' })
    return
  }
  if (before.displayName === displayName) {
    res.json(ok(currentUserSummary(before)))
    return
  }
  try {
    const account = updateSystemAccount(context.systemAccountId, {
      displayName
    })
    if (!account) {
      res.status(404).json({ message: '系统账户不存在' })
      return
    }
    recordOperationLog({
      operationScopeSystemAccountId: account.id,
      mode: 'self',
      module: 'system_accounts',
      action: 'update',
      operationKey: 'auth.update_profile',
      resourceType: 'system_account',
      resourceId: account.id,
      resourceName: account.displayName,
      summary: `修改显示名称：${account.displayName}`,
      changes: [safeChange('displayName', '显示名称', before.displayName, account.displayName)]
    }, req)
    res.json(ok(currentUserSummary(account)))
  } catch (error) {
    res.status(409).json({ message: error instanceof Error ? error.message : '修改显示名称失败' })
  }
})

authRouter.post('/change-password', requireSessionContext, async (req, res, next) => {
  try {
    const context = getRequestAuthContext()
    if (!context) {
      res.status(401).json({ message: '请先登录' })
      return
    }
    const parsed = passwordSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest('密码参数无效'))
      return
    }
    if (hasWhitespace(parsed.data.newPassword) || (parsed.data.oldPassword !== undefined && hasWhitespace(parsed.data.oldPassword))) {
      res.status(400).json(badRequest('登录密码不能包含空格'))
      return
    }
    if (!context.mustChangePassword) {
      if (!parsed.data.oldPassword) {
        res.status(400).json(badRequest('请填写当前密码'))
        return
      }
      const verified = await verifySystemAccountCredentialsAsync(context.username, parsed.data.oldPassword)
      if (!verified || verified.id !== context.systemAccountId) {
        res.status(400).json(badRequest('当前密码不正确'))
        return
      }
    }

    const account = await updateSystemAccountAsync(context.systemAccountId, {
      password: parsed.data.newPassword,
      mustChangePassword: false
    })
    if (!account) {
      res.status(404).json({ message: '系统账户不存在' })
      return
    }
    revokeOtherSessionsForAccount(context.systemAccountId, context.sessionId)
    res.json(ok(account))
  } catch (error) {
    next(error)
  }
})

export function parseCookie(cookieHeader: string): Record<string, string> {
  const result: Record<string, string> = {}
  for (const part of cookieHeader.split(';')) {
    const [rawName, ...rawValue] = part.trim().split('=')
    if (!rawName || rawValue.length === 0) continue
    try {
      result[rawName] = decodeURIComponent(rawValue.join('='))
    } catch {
    }
  }
  return result
}

export function clearSessionCookie(res: { cookie: (name: string, value: string, options: ReturnType<typeof sessionCookieOptions>) => void }): void {
  res.cookie(sessionCookieName, '', sessionCookieOptions({ maxAge: 0 }))
}

async function requireSessionContext(req: Request, res: Response, next: NextFunction): Promise<void> {
  const token = parseCookie(req.headers.cookie ?? '')[sessionCookieName]
  if (!token) {
    res.status(401).json({ message: '请先登录' })
    return
  }

  try {
    const session = await findSessionByTokenAsync(token)
    if (!session) {
      res.status(401).json({ message: '登录会话已过期' })
      return
    }

    await touchSessionAsync(session.sessionId, session.lastSeenAt)
    withRequestAuthContext({
      systemAccountId: session.account.id,
      username: session.account.username,
      displayName: session.account.displayName,
      role: session.account.role,
      mustChangePassword: session.account.mustChangePassword,
      sessionId: session.sessionId
    }, next)
  } catch (error) {
    next(error)
  }
}

function currentUserSummary(account: {
  id: string
  username: string
  displayName: string
  role: 'super_admin' | 'admin' | 'user'
  mustChangePassword: boolean
}) {
  return {
    id: account.id,
    username: account.username,
    displayName: account.displayName,
    role: account.role,
    mustChangePassword: account.mustChangePassword
  }
}

function hasWhitespace(value: string): boolean {
  return whitespacePattern.test(value)
}

export { sessionCookieName }
