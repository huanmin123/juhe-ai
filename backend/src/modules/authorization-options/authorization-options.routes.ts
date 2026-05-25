import { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText, queryTextList } from '../../shared/query-values.js'
import { listAuthorizationGranteeAccounts, listAuthorizationGranteeGroups, listAuthorizationGranteeTeams } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'

export const authorizationOptionsRouter = Router()

authorizationOptionsRouter.get('/grantee-accounts', (req, res) => {
  res.json(ok(listAuthorizationGranteeAccounts(getRequestAccessScope(), parseAuthorizationOptionListOptions(req.query))))
})

authorizationOptionsRouter.get('/grantee-teams', (req, res) => {
  res.json(ok(listAuthorizationGranteeTeams(getRequestAccessScope(), parseAuthorizationOptionListOptions(req.query))))
})

authorizationOptionsRouter.get('/grantee-groups', (req, res) => {
  const options = parseAuthorizationGranteeGroupOptionListOptions(req.query)
  if (!options.granteeSystemAccountId) {
    res.status(400).json(badRequest('被授权用户不能为空'))
    return
  }
  res.json(ok(listAuthorizationGranteeGroups(getRequestAccessScope(), options)))
})

function parseAuthorizationOptionListOptions(query: Record<string, unknown>) {
  return {
    ids: queryTextList(query.ids, 50),
    keyword: optionalQueryText(query.keyword),
    limit: optionLimitValue(integerQueryValue(query.limit))
  }
}

function parseAuthorizationGranteeGroupOptionListOptions(query: Record<string, unknown>) {
  return {
    ...parseAuthorizationOptionListOptions(query),
    granteeSystemAccountId: optionalQueryText(query.granteeSystemAccountId),
    providerCode: optionalQueryText(query.providerCode),
    preferDefault: booleanQueryValue(query.preferDefault)
  }
}

function optionLimitValue(value: number | undefined): number {
  return typeof value === 'number' ? Math.min(50, Math.max(1, value)) : 50
}

function booleanQueryValue(value: unknown): boolean | undefined {
  const raw = Array.isArray(value) ? value[0] : value
  if (typeof raw === 'boolean') return raw
  if (typeof raw !== 'string') return undefined
  const normalized = raw.trim().toLowerCase()
  if (['1', 'true', 'yes'].includes(normalized)) return true
  if (['0', 'false', 'no'].includes(normalized)) return false
  return undefined
}
