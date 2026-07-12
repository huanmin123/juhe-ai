import type { NextFunction, Request, Response } from 'express'

import { isAdminRole, isSuperAdminRole } from '../../domain/types.js'
import { findSessionByTokenAsync, touchSessionAsync } from '../../storage/repositories.js'
import { bindRequestContextFields } from '../../shared/request-context.js'
import { parseCookie, sessionCookieName } from './auth.routes.js'
import { getRequestAuthContext, withRequestAuthContext } from './request-context.js'
import { shouldTouchSessionForSystemApiRequest } from '../system-api/system-api-db-access.js'
import { developmentAutoLoginContextAsync } from './development-auto-login.js'

export async function requireAuth(req: Request, res: Response, next: NextFunction): Promise<void> {
  const token = parseCookie(req.headers.cookie ?? '')[sessionCookieName]
  if (!token) {
    const developmentContext = await developmentAutoLoginContextAsync()
    if (developmentContext) {
      bindRequestContextFields({
        systemAccountId: developmentContext.systemAccountId,
        role: developmentContext.role
      })
      withRequestAuthContext(developmentContext, next)
      return
    }
    res.status(401).json({ message: '请先登录' })
    return
  }

  try {
    const session = await findSessionByTokenAsync(token)
    if (!session) {
      res.status(401).json({ message: '登录会话已过期' })
      return
    }

    if (shouldTouchSessionForSystemApiRequest(res)) {
      await touchSessionAsync(session.sessionId, session.lastSeenAt)
    }
    const context = {
      systemAccountId: session.account.id,
      username: session.account.username,
      displayName: session.account.displayName,
      role: session.account.role,
      mustChangePassword: session.account.mustChangePassword,
      sessionId: session.sessionId
    }

    if (context.mustChangePassword) {
      res.status(403).json({ message: '请先修改初始密码', code: 'must_change_password' })
      return
    }

    bindRequestContextFields({
      systemAccountId: context.systemAccountId,
      role: context.role
    })
    withRequestAuthContext(context, next)
  } catch (error) {
    next(error)
  }
}

export function requireAdmin(_req: Request, res: Response, next: NextFunction): void {
  const context = getRequestAuthContext()
  if (!context || !isAdminRole(context.role)) {
    res.status(403).json({ message: '需要管理员权限' })
    return
  }
  next()
}

export function requireSuperAdmin(_req: Request, res: Response, next: NextFunction): void {
  const context = getRequestAuthContext()
  if (!context || !isSuperAdminRole(context.role)) {
    res.status(403).json({ message: '需要超级管理员权限' })
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
