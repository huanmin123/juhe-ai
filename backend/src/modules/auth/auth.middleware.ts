import type { NextFunction, Request, Response } from 'express'

import { findSessionByToken, touchSession } from '../../storage/repositories.js'
import { bindRequestContextFields } from '../../shared/request-context.js'
import { parseCookie, sessionCookieName } from './auth.routes.js'
import { getRequestAuthContext, withRequestAuthContext } from './request-context.js'

export function requireAuth(req: Request, res: Response, next: NextFunction): void {
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
  bindRequestContextFields({
    systemAccountId: session.account.id,
    role: session.account.role
  })
  withRequestAuthContext({
    systemAccountId: session.account.id,
    username: session.account.username,
    displayName: session.account.displayName,
    role: session.account.role,
    mustChangePassword: session.account.mustChangePassword,
    sessionId: session.sessionId
  }, next)
}

export function requireAdmin(_req: Request, res: Response, next: NextFunction): void {
  const context = getRequestAuthContext()
  if (!context || context.role !== 'admin') {
    res.status(403).json({ message: '需要管理员权限' })
    return
  }
  next()
}

export function forceSelfAccessScope(req: Request, res: Response, next: NextFunction): void {
  const context = getRequestAuthContext()
  if (!context) {
    res.status(401).json({ message: '请先登录' })
    return
  }

  delete (req.query as Record<string, unknown>).systemAccountId
  withRequestAuthContext({ ...context, role: 'user' }, next)
}
