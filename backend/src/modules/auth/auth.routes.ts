import { Router, type NextFunction, type Request, type Response } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { createSession, findSessionByToken, revokeSession, touchSession, updateSystemAccount, updateSystemAccountLastLogin, verifySystemAccountCredentials } from '../../storage/repositories.js'
import { createCaptchaChallenge, verifyCaptchaChallenge } from './captcha.service.js'
import { checkLoginAllowed, getLoginClientIp, recordFailedLogin, recordSuccessfulLogin } from './login-guard.service.js'
import { getRequestAuthContext, withRequestAuthContext } from './request-context.js'

export const authRouter = Router()

const sessionCookieName = 'juhe_ai_session'
const sessionMaxAgeMs = 14 * 24 * 60 * 60 * 1000

const loginSchema = z.object({
  username: z.string().trim().min(1),
  password: z.string().min(1),
  captchaId: z.string().trim().min(1),
  captchaCode: z.string().trim().min(1)
})

const passwordSchema = z.object({
  oldPassword: z.string().min(1).optional(),
  newPassword: z.string().min(4)
})

authRouter.get('/captcha', (_req, res) => {
  res.json(ok(createCaptchaChallenge()))
})

authRouter.post('/login', (req, res) => {
  const parsed = loginSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('登录参数无效'))
    return
  }

  const clientIp = getLoginClientIp(req)

  if (!verifyCaptchaChallenge(parsed.data.captchaId, parsed.data.captchaCode)) {
    res.status(400).json({ message: '验证码错误或已过期' })
    return
  }

  const loginAllowed = checkLoginAllowed(clientIp, parsed.data.username)
  if (loginAllowed.blocked) {
    if (loginAllowed.retryAfterSeconds) {
      res.setHeader('Retry-After', String(loginAllowed.retryAfterSeconds))
    }
    res.status(429).json({ message: loginAllowed.message ?? '尝试过于频繁，请稍后再试' })
    return
  }

  const account = verifySystemAccountCredentials(parsed.data.username, parsed.data.password)
  if (!account) {
    const loginBlock = recordFailedLogin(clientIp, parsed.data.username)
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

  recordSuccessfulLogin(clientIp, account.username)
  const session = createSession(account.id)
  updateSystemAccountLastLogin(account.id)
  res.cookie(sessionCookieName, session.token, {
    httpOnly: true,
    sameSite: 'lax',
    secure: false,
    path: '/',
    maxAge: sessionMaxAgeMs
  })
  res.json(ok({ ...account, lastLoginAt: new Date().toISOString() }))
})

authRouter.post('/logout', (req, res) => {
  const token = parseCookie(req.headers.cookie ?? '')[sessionCookieName]
  if (token) {
    revokeSession(token)
  }
  clearSessionCookie(res)
  res.json(ok({ loggedOut: true }))
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

authRouter.post('/change-password', requireSessionContext, (req, res) => {
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

  const account = updateSystemAccount(context.systemAccountId, {
    password: parsed.data.newPassword,
    mustChangePassword: false
  })
  if (!account) {
    res.status(404).json({ message: '系统账户不存在' })
    return
  }
  res.json(ok(account))
})

export function parseCookie(cookieHeader: string): Record<string, string> {
  const result: Record<string, string> = {}
  for (const part of cookieHeader.split(';')) {
    const [rawName, ...rawValue] = part.trim().split('=')
    if (!rawName || rawValue.length === 0) continue
    result[rawName] = decodeURIComponent(rawValue.join('='))
  }
  return result
}

export function clearSessionCookie(res: { cookie: (name: string, value: string, options: Record<string, unknown>) => void }): void {
  res.cookie(sessionCookieName, '', {
    httpOnly: true,
    sameSite: 'lax',
    secure: false,
    path: '/',
    maxAge: 0
  })
}

function requireSessionContext(req: Request, res: Response, next: NextFunction): void {
  const token = parseCookie(req.headers.cookie ?? '')[sessionCookieName]
  if (!token) {
    res.status(401).json({ message: '请先登录' })
    return
  }

  const session = findSessionByToken(token)
  if (!session) {
    res.status(401).json({ message: '登录会话已过期' })
    return
  }

  touchSession(session.sessionId, session.lastSeenAt)
  withRequestAuthContext({
    systemAccountId: session.account.id,
    username: session.account.username,
    displayName: session.account.displayName,
    role: session.account.role,
    mustChangePassword: session.account.mustChangePassword,
    sessionId: session.sessionId
  }, next)
}

export { sessionCookieName }
