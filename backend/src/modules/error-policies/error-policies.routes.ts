import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { listErrorPolicies } from '../../storage/repositories.js'

export const errorPoliciesRouter = Router()

errorPoliciesRouter.get('/', (_req, res) => {
  res.json(ok(listErrorPolicies()))
})
