import type { NextFunction, Request, Response } from 'express'

import { findSessionByToken, touchSession } from '../../storage/repositories.js'
import { parseCookie, sessionCookieName } from './auth.routes.js'
import { getRequestAuthContext, withRequestAuthContext } from './request-context.js'

export function requireAuth(req: Request, res: Response, next: NextFunction): void {
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

export function requireAdmin(_req: Request, res: Response, next: NextFunction): void {
  const context = getRequestAuthContext()
  if (!context || context.role !== 'admin') {
    res.status(403).json({ message: 'Admin role required' })
    return
  }
  next()
}
