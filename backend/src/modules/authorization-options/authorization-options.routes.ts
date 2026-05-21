import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText } from '../../shared/query-values.js'
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
    keyword: optionalQueryText(query.keyword),
    limit: integerQueryValue(query.limit)
  }
}
