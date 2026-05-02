import { Router, type NextFunction, type Request, type Response } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { createSession, findSessionByToken, revokeSession, touchSession, updateSystemAccount, updateSystemAccountLastLogin, verifySystemAccountCredentials } from '../../storage/repositories.js'
import { getRequestAuthContext, withRequestAuthContext } from './request-context.js'

export const authRouter = Router()

const sessionCookieName = 'juhe_ai_session'
const sessionMaxAgeMs = 14 * 24 * 60 * 60 * 1000

const loginSchema = z.object({
  username: z.string().trim().min(1),
  password: z.string().min(1)
})

const passwordSchema = z.object({
  oldPassword: z.string().min(1).optional(),
  newPassword: z.string().min(4)
})

authRouter.post('/login', (req, res) => {
  const parsed = loginSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Invalid login payload'))
    return
  }

  const account = verifySystemAccountCredentials(parsed.data.username, parsed.data.password)
  if (!account) {
    res.status(401).json({ message: '账号或密码错误' })
    return
  }

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
    res.status(401).json({ message: 'Not authenticated' })
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
    res.status(401).json({ message: 'Not authenticated' })
    return
  }
  const parsed = passwordSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Invalid password payload'))
    return
  }

  const account = updateSystemAccount(context.systemAccountId, {
    password: parsed.data.newPassword,
    mustChangePassword: false
  })
  if (!account) {
    res.status(404).json({ message: 'System account not found' })
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
    res.status(401).json({ message: 'Not authenticated' })
    return
  }

  const session = findSessionByToken(token)
  if (!session) {
    res.status(401).json({ message: 'Session expired' })
    return
  }

  touchSession(session.sessionId)
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
