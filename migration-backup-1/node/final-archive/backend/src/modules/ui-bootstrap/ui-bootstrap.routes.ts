import { Router } from 'express'

import { isAdminRole } from '../../domain/types.js'
import { badRequest, ok } from '../../shared/http.js'
import { findUserReferenceDataAsync } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'

export const uiBootstrapRouter = Router()

uiBootstrapRouter.get('/options', async (req, res, next) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }

  const access = getRequestAccessScope(scopeQuery.data.systemAccountId)
  if (!access) {
    res.status(401).json({ message: '请先登录' })
    return
  }
  if (isAdminRole(access.role) && !access.systemAccountFilterId) {
    res.status(400).json(badRequest('请选择目标系统账户'))
    return
  }

  try {
    const referenceData = await findUserReferenceDataAsync(access)
    if (!referenceData) {
      res.status(404).json({ message: '系统账户不存在' })
      return
    }
    res.json(ok(referenceData))
  } catch (error) {
    next(error)
  }
})
