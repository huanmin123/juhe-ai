import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText, queryTextList } from '../../shared/query-values.js'
import { listAuthorizationGranteeAccounts, listAuthorizationGranteeTeams } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'

export const authorizationOptionsRouter = Router()

authorizationOptionsRouter.get('/grantee-accounts', (req, res) => {
  res.json(ok(listAuthorizationGranteeAccounts(getRequestAccessScope(), parseAuthorizationOptionListOptions(req.query))))
})

authorizationOptionsRouter.get('/grantee-teams', (req, res) => {
  res.json(ok(listAuthorizationGranteeTeams(getRequestAccessScope(), parseAuthorizationOptionListOptions(req.query))))
})

function parseAuthorizationOptionListOptions(query: Record<string, unknown>) {
  return {
    ids: queryTextList(query.ids, 50),
    keyword: optionalQueryText(query.keyword),
    limit: optionLimitValue(integerQueryValue(query.limit))
  }
}

function optionLimitValue(value: number | undefined): number {
  return typeof value === 'number' ? Math.min(50, Math.max(1, value)) : 50
}
