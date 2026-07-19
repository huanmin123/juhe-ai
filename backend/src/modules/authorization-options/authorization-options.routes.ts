import { Router } from 'express'

import { badRequest, ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText, queryTextList } from '../../shared/query-values.js'
import { listAuthorizationGranteeAccountsAsync, listAuthorizationGranteeGroupsAsync, listAuthorizationGranteeTeamsAsync } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { createPageDataDomainReadCache, pageDataReadCacheKey } from '../page-data/page-data-read-cache.service.js'

export const authorizationOptionsRouter = Router()

const authorizationAccountOptionsReadCache = createPageDataDomainReadCache<Awaited<ReturnType<typeof listAuthorizationGranteeAccountsAsync>>>('systemAccounts.options', {
  max: 512,
  ttlMs: 6 * 60 * 60 * 1000
})
const authorizationTeamOptionsReadCache = createPageDataDomainReadCache<Awaited<ReturnType<typeof listAuthorizationGranteeTeamsAsync>>>('teams.options', {
  max: 512,
  ttlMs: 6 * 60 * 60 * 1000
})
const authorizationGroupOptionsReadCache = createPageDataDomainReadCache<Awaited<ReturnType<typeof listAuthorizationGranteeGroupsAsync>>>('groups.static', {
  max: 512,
  ttlMs: 6 * 60 * 60 * 1000
})

authorizationOptionsRouter.get('/grantee-accounts', async (req, res, next) => {
  try {
    const access = getRequestAccessScope()
    const query = parseAuthorizationOptionListOptions(req.query)
    const options = await authorizationAccountOptionsReadCache.load(pageDataReadCacheKey({
      scope: access,
      route: '/authorization-options/grantee-accounts',
      query
    }), () => listAuthorizationGranteeAccountsAsync(access, query))
    res.json(ok(options))
  } catch (error) {
    next(error)
  }
})

authorizationOptionsRouter.get('/grantee-teams', async (req, res, next) => {
  try {
    const access = getRequestAccessScope()
    const query = parseAuthorizationOptionListOptions(req.query)
    const options = await authorizationTeamOptionsReadCache.load(pageDataReadCacheKey({
      scope: access,
      route: '/authorization-options/grantee-teams',
      query
    }), () => listAuthorizationGranteeTeamsAsync(access, query))
    res.json(ok(options))
  } catch (error) {
    next(error)
  }
})

authorizationOptionsRouter.get('/grantee-groups', async (req, res, next) => {
  const options = parseAuthorizationGranteeGroupOptionListOptions(req.query)
  if (!options.granteeSystemAccountId) {
    res.status(400).json(badRequest('被授权用户不能为空'))
    return
  }
  try {
    const access = getRequestAccessScope()
    const groups = await authorizationGroupOptionsReadCache.load(pageDataReadCacheKey({
      scope: access,
      route: '/authorization-options/grantee-groups',
      query: options
    }), () => listAuthorizationGranteeGroupsAsync(access, options))
    res.json(ok(groups))
  } catch (error) {
    next(error)
  }
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
