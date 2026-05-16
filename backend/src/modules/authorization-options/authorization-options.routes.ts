import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { listAuthorizationGranteeAccounts, listAuthorizationGranteeTeams } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'

export const authorizationOptionsRouter = Router()

authorizationOptionsRouter.get('/grantee-accounts', (_req, res) => {
  res.json(ok(listAuthorizationGranteeAccounts(getRequestAccessScope())))
})

authorizationOptionsRouter.get('/grantee-teams', (_req, res) => {
  res.json(ok(listAuthorizationGranteeTeams(getRequestAccessScope())))
})
