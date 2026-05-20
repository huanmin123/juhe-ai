import { Router } from 'express'

import { ok } from '../../shared/http.js'
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

function optionalQueryText(value: unknown): string | undefined {
  const text = Array.isArray(value) ? value[0] : value
  return typeof text === 'string' && text.trim() ? text.trim() : undefined
}

function integerQueryValue(value: unknown): number | undefined {
  const text = Array.isArray(value) ? value[0] : value
  if (typeof text === 'string') {
    const trimmed = text.trim()
    if (!trimmed) return undefined
    const number = Number(trimmed)
    return Number.isInteger(number) ? number : undefined
  }
  return typeof text === 'number' && Number.isInteger(text) ? text : undefined
}
