import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { listUpstreamErrorFeatureRuleCatalog } from '../gateway/openai-gateway-upstream-error-rule-catalog.service.js'

export const featureRulesRouter = Router()

featureRulesRouter.get('/upstream-error-feature-rules', (_req, res) => {
  res.json(ok(listUpstreamErrorFeatureRuleCatalog()))
})
