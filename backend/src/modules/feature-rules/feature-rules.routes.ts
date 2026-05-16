import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { listStreamInterceptRuleCatalog } from '../gateway/openai-gateway-stream-rule-catalog.service.js'
import { listUpstreamErrorFeatureRuleCatalog } from '../gateway/openai-gateway-upstream-error-rule-catalog.service.js'

export const featureRulesRouter = Router()

featureRulesRouter.get('/stream-intercept-rules', (_req, res) => {
  res.json(ok(listStreamInterceptRuleCatalog()))
})

featureRulesRouter.get('/upstream-error-feature-rules', (_req, res) => {
  res.json(ok(listUpstreamErrorFeatureRuleCatalog()))
})
