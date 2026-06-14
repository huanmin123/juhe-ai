import type { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import { AccountTagInUseError, deleteAccountTag, listAccountTags } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'

export function registerAccountTagsRoutes(router: Router): void {
  router.get('/tags', (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    try {
      res.json(ok(listAccountTags(getRequestAccessScope(scopeQuery.data.systemAccountId))))
    } catch (error) {
      res.status(400).json(badRequest(error instanceof Error ? error.message : '加载账户标签失败'))
    }
  })

  router.delete('/tags/:tagId', (req, res) => {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    try {
      if (!deleteAccountTag(req.params.tagId, getRequestAccessScope(scopeQuery.data.systemAccountId))) {
        res.status(404).json({ message: '标签不存在' })
        return
      }
      res.status(204).send()
    } catch (error) {
      if (error instanceof AccountTagInUseError) {
        res.status(400).json(badRequest(error.message))
        return
      }
      res.status(400).json(badRequest(error instanceof Error ? error.message : '删除账户标签失败'))
    }
  })
}
