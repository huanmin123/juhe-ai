import { createHash, timingSafeEqual } from 'node:crypto'

import express, { Router, type NextFunction, type Request, type Response } from 'express'
import { z } from 'zod'

import { runtimeConfig } from '../../config/runtime.js'
import { badRequest, ok } from '../../shared/http.js'
import { findSessionByTokenAsync } from '../../storage/repositories.js'
import { hashSecret } from '../../storage/crypto.js'
import { parseCookie, sessionCookieName } from '../auth/auth.routes.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { oauthProtocolRateLimit } from './oidc-rate-limit.middleware.js'
import { assertOidcSigningKeyUsable, OidcCiphertextError, oidcSubjectForSystemAccount, signOidcIdToken } from './oidc-provider.crypto.js'
import {
  consumeAuthorizationTransaction,
  createAuthorizationCode,
  createAuthorizationTransaction,
  createDeviceAuthorization,
  createOAuthClient,
  decideDeviceAuthorization,
  deviceAuthorizationRequestsIdToken,
  ensureOidcSigningKey,
  exchangeAuthorizationCode,
  authorizationCodeRequestsIdToken,
  findAccessTokenContext,
  findAuthorizationTransaction,
  findActiveOidcSigningKey,
  findOAuthClient,
  findOAuthClientSecret,
  findSystemAccountProfile,
  listOidcSigningJwks,
  listOAuthClients,
  pollDeviceAuthorization,
  prepareDeviceAuthorization,
  reissueOAuthClientSecret,
  revokeAccessToken,
  rotateAccessToken,
  updateOAuthClientStatus
} from './oidc-provider.repository.js'
import type { OAuthAccessTokenContext, OAuthDeviceAuthorization } from './oidc-provider.repository.js'

export const oauthPublicRouter = Router()
export const oauthManagementRouter = Router()

const resourceScopes = [
  'profile.read', 'profile.write',
  'groups.read', 'groups.write',
  'route_strategies.read', 'route_strategies.write',
  'api_keys.read', 'api_keys.write',
  'ai_accounts.read', 'ai_accounts.write',
  'request_limits.read'
].map((scope) => `juhe:${scope}`)
const oidcScopes = ['openid', 'profile']
const supportedScopes = [...oidcScopes, ...resourceScopes]
const requiredReadScopeByWriteScope: Record<string, string> = {
  'juhe:profile.write': 'juhe:profile.read',
  'juhe:groups.write': 'juhe:groups.read',
  'juhe:route_strategies.write': 'juhe:route_strategies.read',
  'juhe:api_keys.write': 'juhe:api_keys.read',
  'juhe:ai_accounts.write': 'juhe:ai_accounts.read'
}
const deviceCodeGrantType = 'urn:ietf:params:oauth:grant-type:device_code'

const authorizeQuerySchema = z.object({
  response_type: z.literal('code'),
  client_id: z.string().min(1),
  redirect_uri: z.string().url(),
  scope: z.string().min(1),
  state: z.string().min(1).max(1024),
  code_challenge: z.string().regex(/^[A-Za-z0-9\-_]{43,128}$/),
  code_challenge_method: z.literal('S256'),
  nonce: z.string().min(1).max(1024).optional(),
  transaction_id: z.string().uuid().optional()
})

const decisionSchema = z.object({
  transaction_id: z.string().uuid(),
  csrf_token: z.string().min(1),
  decision: z.enum(['allow', 'deny'])
}).strict()

const clientCreateSchema = z.object({
  displayName: z.string().trim().min(1).max(120),
  clientType: z.enum(['public', 'confidential']),
  redirectUris: z.array(z.string().url()).min(1).max(20),
  allowedScopes: z.array(z.string().min(1)).min(1).max(20)
}).strict()

const clientStatusPatchSchema = z.object({
  status: z.enum(['active', 'disabled'])
}).strict()

const deviceDecisionSchema = z.object({
  user_code: z.string().trim().min(1).max(64),
  csrf_token: z.string().min(1),
  decision: z.enum(['allow', 'deny'])
}).strict()

oauthPublicRouter.use(express.urlencoded({ extended: false, limit: '32kb' }))
oauthPublicRouter.use(oauthProtocolRateLimit)
oauthPublicRouter.use(async (req, res, next) => {
  if (!isOidcProtocolRequest(req.originalUrl) || !runtimeConfig.oidc.enabled || !runtimeConfig.oidc.issuer) {
    next()
    return
  }
  try {
    await ensureOidcSigningKey()
    next()
  } catch {
    oidcUnavailable(res)
  }
})

oauthPublicRouter.get('/.well-known/openid-configuration', (_req, res) => {
  if (!runtimeConfig.oidc.enabled || !runtimeConfig.oidc.issuer) {
    res.status(404).json({ message: 'OIDC Provider 未启用' })
    return
  }
  if (!findActiveOidcSigningKey()) {
    oidcUnavailable(res)
    return
  }
  res.setHeader('Cache-Control', 'public, max-age=300')
  res.json({
    issuer: runtimeConfig.oidc.issuer,
    authorization_endpoint: `${runtimeConfig.oidc.issuer}/oauth/authorize`,
    token_endpoint: `${runtimeConfig.oidc.issuer}/oauth/token`,
    userinfo_endpoint: `${runtimeConfig.oidc.issuer}/oauth/userinfo`,
    jwks_uri: `${runtimeConfig.oidc.issuer}/oauth/jwks`,
    device_authorization_endpoint: `${runtimeConfig.oidc.issuer}/oauth/device_authorization`,
    revocation_endpoint: `${runtimeConfig.oidc.issuer}/oauth/revoke`,
    juhe_token_renewal_endpoint: `${runtimeConfig.oidc.issuer}/oauth/token/renew`,
    response_types_supported: ['code'],
    grant_types_supported: ['authorization_code', deviceCodeGrantType],
    token_endpoint_auth_methods_supported: ['client_secret_basic', 'none'],
    code_challenge_methods_supported: ['S256'],
    id_token_signing_alg_values_supported: ['RS256'],
    subject_types_supported: ['public'],
    claims_supported: ['sub', 'name', 'preferred_username'],
    scopes_supported: supportedScopes
  })
})

oauthPublicRouter.get('/oauth/jwks', (_req, res) => {
  if (!runtimeConfig.oidc.enabled) {
    res.status(404).json({ message: 'OIDC Provider 未启用' })
    return
  }
  if (!findActiveOidcSigningKey()) {
    oidcUnavailable(res)
    return
  }
  const keys = listOidcSigningJwks()
  if (!keys.length) {
    oidcUnavailable(res)
    return
  }
  res.setHeader('Cache-Control', 'public, max-age=300')
  res.json({ keys })
})

oauthPublicRouter.get('/oauth/authorize', async (req, res, next) => {
  try {
    if (!runtimeConfig.oidc.enabled) {
      res.status(404).json({ message: 'OIDC Provider 未启用' })
      return
    }
    if (!findActiveOidcSigningKey()) {
      oidcUnavailable(res)
      return
    }
    const transactionId = stringQuery(req.query.transaction_id)
    const transaction = transactionId
      ? findAuthorizationTransaction(transactionId)
      : createAuthorizationRequest(req)
    if (!transaction) {
      res.status(400).json(oauthError('invalid_request', '授权请求不存在或已过期'))
      return
    }
    const client = findOAuthClient(transaction.clientId)
    if (!client || client.status !== 'active' || !matchesRegisteredRedirectUri(client.redirectUris, transaction.redirectUri)) {
      res.status(400).json(oauthError('invalid_request', 'Client 或回调地址无效'))
      return
    }
    const session = await browserSession(req)
    if (!session) {
      const loginRedirect = `/__aisys__/login?redirect=${encodeURIComponent(`/oauth/authorize?transaction_id=${transaction.id}`)}`
      res.redirect(302, loginRedirect)
      return
    }
    res.setHeader('Cache-Control', 'no-store')
    res.type('html').send(consentHtml(client.displayName, transaction))
  } catch (error) {
    next(error)
  }
})

oauthPublicRouter.post('/oauth/authorize/decision', async (req, res, next) => {
  try {
    if (!runtimeConfig.oidc.enabled) {
      res.status(404).json({ message: 'OIDC Provider 未启用' })
      return
    }
    if (!findActiveOidcSigningKey()) {
      oidcUnavailable(res)
      return
    }
    const parsed = decisionSchema.safeParse(req.body)
    if (!parsed.success || !(await browserSession(req))) {
      res.status(400).json(oauthError('invalid_request', '授权确认请求无效'))
      return
    }
    const transaction = consumeAuthorizationTransaction({
      id: parsed.data.transaction_id,
      csrfToken: parsed.data.csrf_token
    })
    if (!transaction) {
      res.status(400).json(oauthError('invalid_request', '授权事务无效或已处理'))
      return
    }
    if (parsed.data.decision === 'deny') {
      redirectWithError(res, transaction.redirectUri, transaction.state, 'access_denied')
      return
    }
    const session = await browserSession(req)
    if (!session) {
      res.status(401).json(oauthError('login_required', '请先登录'))
      return
    }
    const issued = createAuthorizationCode({
      clientId: transaction.clientId,
      systemAccountId: session.account.id,
      scopes: transaction.scopes,
      redirectUri: transaction.redirectUri,
      codeChallenge: transaction.codeChallenge,
      nonce: transaction.nonce
    })
    const callback = new URL(transaction.redirectUri)
    callback.searchParams.set('code', issued.code)
    callback.searchParams.set('state', transaction.state)
    res.redirect(302, callback.toString())
  } catch (error) {
    next(error)
  }
})

oauthPublicRouter.post('/oauth/device_authorization', (req, res) => {
  if (!runtimeConfig.oidc.enabled || !runtimeConfig.oidc.issuer) {
    res.status(404).json({ message: 'OIDC Provider 未启用' })
    return
  }
  if (!findActiveOidcSigningKey()) {
    oidcUnavailable(res)
    return
  }
  const body = req.body as Record<string, unknown>
  const clientId = clientIdFromTokenRequest(req, body)
  const client = clientId ? findOAuthClient(clientId) : undefined
  if (!client || !authenticateClient(req, client, body)) {
    res.status(401).json(oauthError('invalid_client', 'Client 认证失败'))
    return
  }
  const requestedScope = typeof body.scope === 'string' ? body.scope : ''
  const scopes = normalizeScopes(requestedScope)
  const nonce = typeof body.nonce === 'string' && body.nonce.trim() ? body.nonce.trim() : undefined
  if (!scopes.length || scopes.some((scope) => !client.allowedScopes.includes(scope)) || (scopes.includes('profile') && !scopes.includes('openid')) || !hasRequiredReadScopes(scopes)) {
    res.status(400).json(oauthError('invalid_scope', '请求的 scope 未登记'))
    return
  }
  if (scopes.includes('openid') && (!nonce || nonce.length > 1024)) {
    res.status(400).json(oauthError('invalid_request', '请求 openid scope 时必须提供 nonce'))
    return
  }
  const verificationUri = `${runtimeConfig.oidc.issuer}/oauth/device`
  const created = createDeviceAuthorization({ clientId: client.clientId, scopes, nonce, verificationUri })
  res.setHeader('Cache-Control', 'no-store')
  res.status(200).json({
    device_code: created.deviceCode,
    user_code: created.authorization.userCode,
    verification_uri: verificationUri,
    verification_uri_complete: `${verificationUri}?user_code=${encodeURIComponent(created.authorization.userCode)}`,
    expires_in: secondsUntil(created.authorization.expiresAt),
    interval: created.authorization.intervalSeconds
  })
})

oauthPublicRouter.get('/oauth/device', async (req, res, next) => {
  try {
    if (!runtimeConfig.oidc.enabled) {
      res.status(404).json({ message: 'OIDC Provider 未启用' })
      return
    }
    if (!findActiveOidcSigningKey()) {
      oidcUnavailable(res)
      return
    }
    const userCode = stringQuery(req.query.user_code)
    if (!userCode) {
      res.setHeader('Cache-Control', 'no-store')
      res.type('html').send(deviceCodeEntryHtml())
      return
    }
    const session = await browserSession(req)
    if (!session) {
      const loginRedirect = `/__aisys__/login?redirect=${encodeURIComponent(`/oauth/device?user_code=${encodeURIComponent(userCode)}`)}`
      res.redirect(302, loginRedirect)
      return
    }
    const prepared = prepareDeviceAuthorization(userCode)
    if (!prepared) {
      res.status(400).type('html').send(deviceAuthorizationErrorHtml('设备授权码无效、已过期或已处理'))
      return
    }
    res.setHeader('Cache-Control', 'no-store')
    res.type('html').send(deviceConsentHtml(prepared))
  } catch (error) {
    next(error)
  }
})

oauthPublicRouter.post('/oauth/device/decision', async (req, res, next) => {
  try {
    if (!runtimeConfig.oidc.enabled) {
      res.status(404).json({ message: 'OIDC Provider 未启用' })
      return
    }
    if (!findActiveOidcSigningKey()) {
      oidcUnavailable(res)
      return
    }
    const parsed = deviceDecisionSchema.safeParse(req.body)
    const session = await browserSession(req)
    if (!parsed.success || !session) {
      res.status(400).json(oauthError('invalid_request', '设备授权确认请求无效'))
      return
    }
    const decided = decideDeviceAuthorization({
      userCode: parsed.data.user_code,
      csrfToken: parsed.data.csrf_token,
      systemAccountId: session.account.id,
      decision: parsed.data.decision
    })
    if (!decided) {
      res.status(400).json(oauthError('invalid_request', '设备授权码无效、已过期或已处理'))
      return
    }
    res.setHeader('Cache-Control', 'no-store')
    res.type('html').send(deviceAuthorizationCompleteHtml(parsed.data.decision))
  } catch (error) {
    next(error)
  }
})

oauthPublicRouter.post('/oauth/token', async (req, res, next) => {
  try {
    if (!runtimeConfig.oidc.enabled) {
      res.status(404).json({ message: 'OIDC Provider 未启用' })
      return
    }
    const signingKey = findActiveOidcSigningKey()
    if (!signingKey || !runtimeConfig.oidc.issuer) {
      oidcUnavailable(res)
      return
    }
    const body = req.body as Record<string, unknown>
    const clientId = clientIdFromTokenRequest(req, body)
    const client = clientId ? findOAuthClient(clientId) : undefined
    if (!client || !authenticateClient(req, client, body)) {
      res.status(401).json(oauthError('invalid_client', 'Client 认证失败'))
      return
    }
    if (body.grant_type === deviceCodeGrantType) {
      if (typeof body.device_code !== 'string') {
        res.status(400).json(oauthError('invalid_request', 'device_code 参数无效'))
        return
      }
      if (!(await oidcSigningPreflightIfRequired({
        requestsIdToken: deviceAuthorizationRequestsIdToken({ clientId: client.clientId, deviceCode: body.device_code }),
        signingKey,
        issuer: runtimeConfig.oidc.issuer
      }))) {
        oidcUnavailable(res)
        return
      }
      const polled = pollDeviceAuthorization({ clientId: client.clientId, deviceCode: body.device_code })
      if (polled.kind !== 'approved') {
        const error = polled.kind === 'invalid'
          ? 'invalid_grant'
          : polled.kind === 'expired'
            ? 'expired_token'
            : polled.kind
        res.status(400).json(oauthError(error, devicePollDescription(error)))
        return
      }
      const idToken = await maybeIssueIdToken(polled.context, polled.nonce)
      sendTokenResponse(res, polled.accessToken, polled.context, idToken)
      return
    }
    if (body.grant_type !== 'authorization_code' || typeof body.code !== 'string' || typeof body.redirect_uri !== 'string' || typeof body.code_verifier !== 'string') {
      res.status(400).json(oauthError('invalid_grant', '授权码参数无效'))
      return
    }
    if (!(await oidcSigningPreflightIfRequired({
      requestsIdToken: authorizationCodeRequestsIdToken({ clientId: client.clientId, code: body.code }),
      signingKey,
      issuer: runtimeConfig.oidc.issuer
    }))) {
      oidcUnavailable(res)
      return
    }
    const issued = exchangeAuthorizationCode({ clientId: client.clientId, code: body.code, redirectUri: body.redirect_uri, codeVerifier: body.code_verifier })
    if (!issued) {
      res.status(400).json(oauthError('invalid_grant', '授权码无效、已过期或已使用'))
      return
    }
    const idToken = await maybeIssueIdToken(issued.context, issued.nonce)
    sendTokenResponse(res, issued.accessToken, issued.context, idToken)
  } catch (error) {
    next(error)
  }
})

oauthPublicRouter.post('/oauth/token/renew', (req, res) => {
  if (!runtimeConfig.oidc.enabled) {
    res.status(404).json({ message: 'OIDC Provider 未启用' })
    return
  }
  if (!findActiveOidcSigningKey()) {
    oidcUnavailable(res)
    return
  }
  const body = req.body as Record<string, unknown>
  const currentAccessToken = typeof body.current_access_token === 'string' ? body.current_access_token : ''
  const clientId = clientIdFromTokenRequest(req, body)
  const client = clientId ? findOAuthClient(clientId) : undefined
  if (!client || !currentAccessToken || !authenticateClient(req, client, body)) {
    res.status(401).json(oauthError('invalid_client', 'Client 认证失败'))
    return
  }
  const renewed = rotateAccessToken({ clientId: client.clientId, currentAccessToken })
  if (renewed === 'not_eligible') {
    res.status(400).json(oauthError('token_renewal_not_eligible', '当前令牌签发未满 72 小时'))
    return
  }
  if (!renewed) {
    res.status(400).json(oauthError('invalid_token', '令牌无效或授权已到期'))
    return
  }
  res.setHeader('Cache-Control', 'no-store')
  res.json({
    access_token: renewed.accessToken,
    token_type: 'Bearer',
    expires_in: Math.max(0, Math.floor((Date.parse(renewed.context.expiresAt) - Date.now()) / 1000)),
    scope: renewed.context.scopes.join(' ')
  })
})

oauthPublicRouter.post('/oauth/revoke', (req, res) => {
  if (!runtimeConfig.oidc.enabled) {
    res.status(404).json({ message: 'OIDC Provider 未启用' })
    return
  }
  if (!findActiveOidcSigningKey()) {
    oidcUnavailable(res)
    return
  }
  const body = req.body as Record<string, unknown>
  const token = typeof body.token === 'string' ? body.token : ''
  const clientId = clientIdFromTokenRequest(req, body)
  const client = clientId ? findOAuthClient(clientId) : undefined
  if (!client || !token || !authenticateClient(req, client, body)) {
    res.status(401).json(oauthError('invalid_client', 'Client 认证失败'))
    return
  }
  revokeAccessToken(token, client.clientId)
  res.status(200).end()
})

oauthPublicRouter.get('/oauth/userinfo', (req, res) => {
  if (!runtimeConfig.oidc.enabled) {
    res.status(404).json({ message: 'OIDC Provider 未启用' })
    return
  }
  if (!findActiveOidcSigningKey()) {
    oidcUnavailable(res)
    return
  }
  const token = bearerToken(req)
  const context = token ? findAccessTokenContext(token) : undefined
  if (!context) {
    res.status(401).json(oauthError('invalid_token', '访问令牌无效'))
    return
  }
  if (!context.scopes.includes('openid')) {
    res.status(403).json(oauthError('insufficient_scope', '访问令牌未包含 openid scope'))
    return
  }
  const account = findSystemAccountProfile(context.systemAccountId)
  if (!account) {
    res.status(401).json(oauthError('invalid_token', '访问令牌对应用户无效'))
    return
  }
  const claims: Record<string, string> = {
    sub: oidcSubjectForSystemAccount(account.id)
  }
  if (context.scopes.includes('profile')) {
    claims.name = account.displayName
    claims.preferred_username = account.username
  }
  res.setHeader('Cache-Control', 'no-store')
  res.json(claims)
})

oauthManagementRouter.get('/clients', requireAdmin, (_req, res) => {
  res.json(ok(listOAuthClients().map((client) => ({
    ...client,
    clientSecretHash: undefined
  }))))
})

oauthManagementRouter.get('/clients/:clientId/integration-package', requireAdmin, (req, res, next) => {
  if (!runtimeConfig.oidc.enabled || !runtimeConfig.oidc.issuer) {
    res.status(409).json(badRequest('OIDC Provider 未启用，不能下载对接文档'))
    return
  }
  const client = findOAuthClient(req.params.clientId)
  if (!client) {
    res.status(404).json({ message: 'Client 不存在' })
    return
  }
  let clientSecret: string | undefined
  try {
    clientSecret = client.clientType === 'confidential'
      ? findOAuthClientSecret(client.clientId)
      : undefined
  } catch (error) {
    if (error instanceof OidcCiphertextError) {
      res.status(409).json(badRequest('该 Client 的当前 Client Secret 无法读取，请重新签发后再下载对接文档'))
      return
    }
    next(error)
    return
  }
  if (client.clientType === 'confidential' && !clientSecret) {
    res.status(409).json(badRequest('该 Client 没有可下载的当前 Client Secret，请先重新签发密钥后再下载对接文档'))
    return
  }
  res.json(ok({
    client: { ...client, clientSecretHash: undefined },
    clientSecret
  }))
})

oauthManagementRouter.get('/integration-info', requireAdmin, (_req, res) => {
  if (!runtimeConfig.oidc.enabled || !runtimeConfig.oidc.issuer) {
    res.status(404).json({ message: 'OIDC Provider 未启用' })
    return
  }
  const issuer = runtimeConfig.oidc.issuer
  res.json(ok({
    issuer,
    discoveryUrl: `${issuer}/.well-known/openid-configuration`,
    jwksUrl: `${issuer}/oauth/jwks`,
    authorizationEndpoint: `${issuer}/oauth/authorize`,
    tokenEndpoint: `${issuer}/oauth/token`,
    userinfoEndpoint: `${issuer}/oauth/userinfo`,
    deviceAuthorizationEndpoint: `${issuer}/oauth/device_authorization`,
    revocationEndpoint: `${issuer}/oauth/revoke`,
    tokenRenewalEndpoint: `${issuer}/oauth/token/renew`,
    idTokenSigningAlgorithm: 'RS256'
  }))
})

oauthManagementRouter.post('/clients', requireAdmin, (req, res) => {
  if (!runtimeConfig.oidc.enabled || !runtimeConfig.oidc.issuer) {
    res.status(409).json(badRequest('OIDC Provider 未启用，不能创建 Client'))
    return
  }
  const parsed = clientCreateSchema.safeParse(req.body)
  if (
    !parsed.success
    || !parsed.data.allowedScopes.every((scope) => supportedScopes.includes(scope))
    || (parsed.data.allowedScopes.includes('profile') && !parsed.data.allowedScopes.includes('openid'))
    || !hasRequiredReadScopes(parsed.data.allowedScopes)
  ) {
    res.status(400).json(badRequest('Client 参数或 scope 无效'))
    return
  }
  const invalidRedirect = parsed.data.redirectUris.some((uri) => !isAllowedRedirectUri(uri, parsed.data.clientType))
  if (invalidRedirect) {
    res.status(400).json(badRequest('回调地址必须是精确 HTTPS、反向域名协议或本机回环地址'))
    return
  }
  const created = createOAuthClient(parsed.data)
  res.status(201).json(ok({ ...created, clientSecretHash: undefined }))
})

oauthManagementRouter.patch('/clients/:clientId', requireAdmin, (req, res) => {
  const parsed = clientStatusPatchSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Client 状态参数无效'))
    return
  }
  const updated = updateOAuthClientStatus(req.params.clientId, parsed.data.status)
  if (!updated) {
    res.status(404).json({ message: 'Client 不存在' })
    return
  }
  res.json(ok({ ...updated, clientSecretHash: undefined }))
})

oauthManagementRouter.post('/clients/:clientId/secret/reissue', requireAdmin, (req, res) => {
  if (!runtimeConfig.oidc.enabled || !runtimeConfig.oidc.issuer) {
    res.status(409).json(badRequest('OIDC Provider 未启用，不能重新签发 Client Secret'))
    return
  }
  const client = findOAuthClient(req.params.clientId)
  if (!client) {
    res.status(404).json({ message: 'Client 不存在' })
    return
  }
  if (client.clientType !== 'confidential') {
    res.status(400).json(badRequest('公开 Client 不使用 Client Secret'))
    return
  }
  const reissued = reissueOAuthClientSecret(client.clientId)
  if (!reissued) {
    res.status(404).json({ message: 'Client 不存在' })
    return
  }
  res.json(ok({ ...reissued, clientSecretHash: undefined }))
})

function createAuthorizationRequest(req: Request) {
  const parsed = authorizeQuerySchema.safeParse({
    response_type: stringQuery(req.query.response_type),
    client_id: stringQuery(req.query.client_id),
    redirect_uri: stringQuery(req.query.redirect_uri),
    scope: stringQuery(req.query.scope),
    state: stringQuery(req.query.state),
    code_challenge: stringQuery(req.query.code_challenge),
    code_challenge_method: stringQuery(req.query.code_challenge_method),
    nonce: stringQuery(req.query.nonce)
  })
  if (!parsed.success) throw new OAuthRouteError('invalid_request', '授权请求参数无效', 400)
  const client = findOAuthClient(parsed.data.client_id)
  if (!client || client.status !== 'active' || !matchesRegisteredRedirectUri(client.redirectUris, parsed.data.redirect_uri)) {
    throw new OAuthRouteError('invalid_request', 'Client 或回调地址无效', 400)
  }
  const scopes = normalizeScopes(parsed.data.scope)
  if (scopes.some((scope) => !client.allowedScopes.includes(scope)) || (scopes.includes('profile') && !scopes.includes('openid')) || !hasRequiredReadScopes(scopes)) {
    throw new OAuthRouteError('invalid_scope', '请求的 scope 未登记', 400)
  }
  if (scopes.includes('openid') && !parsed.data.nonce) {
    throw new OAuthRouteError('invalid_request', '请求 openid scope 时必须提供 nonce', 400)
  }
  return createAuthorizationTransaction({
    clientId: client.clientId,
    redirectUri: parsed.data.redirect_uri,
    scopes,
    state: parsed.data.state,
    codeChallenge: parsed.data.code_challenge,
    nonce: parsed.data.nonce
  })
}

async function browserSession(req: Request) {
  const cookie = parseCookie(req.headers.cookie ?? '')[sessionCookieName]
  return cookie ? findSessionByTokenAsync(cookie) : undefined
}

function clientIdFromTokenRequest(req: Request, body: Record<string, unknown>): string | undefined {
  const basic = req.headers.authorization
  if (basic?.startsWith('Basic ')) {
    try {
      const decoded = Buffer.from(basic.slice(6), 'base64').toString('utf8')
      return decoded.slice(0, decoded.indexOf(':')) || undefined
    } catch {
      return undefined
    }
  }
  return typeof body.client_id === 'string' ? body.client_id : undefined
}

function authenticateClient(req: Request, client: ReturnType<typeof findOAuthClient>, body: Record<string, unknown>): boolean {
  if (!client || client.status !== 'active') return false
  if (client.clientType === 'public') return !req.headers.authorization
  const authorization = req.headers.authorization
  if (!authorization?.startsWith('Basic ') || !client.clientSecretHash) return false
  try {
    const decoded = Buffer.from(authorization.slice(6), 'base64').toString('utf8')
    const separator = decoded.indexOf(':')
    if (separator < 1) return false
    const id = decoded.slice(0, separator)
    const secret = decoded.slice(separator + 1)
    return id === client.clientId && safeEqual(hashSecret(secret), client.clientSecretHash)
  } catch {
    return false
  }
}

function safeEqual(left: string, right: string): boolean {
  const a = Buffer.from(left)
  const b = Buffer.from(right)
  return a.length === b.length && timingSafeEqual(a, b)
}

async function oidcSigningPreflightIfRequired(input: {
  requestsIdToken: boolean
  signingKey: ReturnType<typeof findActiveOidcSigningKey>
  issuer: string
}): Promise<boolean> {
  if (!input.requestsIdToken || !input.signingKey) return true
  try {
    await assertOidcSigningKeyUsable({
      privateKeyCiphertext: input.signingKey.privateKeyCiphertext,
      kid: input.signingKey.kid,
      issuer: input.issuer
    })
    return true
  } catch {
    return false
  }
}

async function maybeIssueIdToken(context: OAuthAccessTokenContext, nonce?: string): Promise<string | undefined> {
  if (!context.scopes.includes('openid')) return undefined
  const signingKey = findActiveOidcSigningKey()
  const issuer = runtimeConfig.oidc.issuer
  if (!signingKey || !issuer) throw new OidcUnavailableError()
  const idTokenExpiresAt = new Date(Math.min(
    Date.parse(context.expiresAt),
    Date.now() + 5 * 60 * 1_000
  )).toISOString()
  return signOidcIdToken({
    privateKeyCiphertext: signingKey.privateKeyCiphertext,
    kid: signingKey.kid,
    issuer,
    audience: context.clientId,
    subject: oidcSubjectForSystemAccount(context.systemAccountId),
    expiresAt: idTokenExpiresAt,
    nonce
  })
}

function sendTokenResponse(
  res: Response,
  accessToken: string,
  context: OAuthAccessTokenContext,
  idToken?: string
): void {
  res.setHeader('Cache-Control', 'no-store')
  res.json({
    access_token: accessToken,
    ...(idToken ? { id_token: idToken } : {}),
    token_type: 'Bearer',
    expires_in: secondsUntil(context.expiresAt),
    scope: context.scopes.join(' ')
  })
}

function oidcUnavailable(res: Response): void {
  res.status(503).json(oauthError('temporarily_unavailable', 'OIDC 签名密钥未配置或不可用'))
}

function isOidcProtocolRequest(originalUrl: string): boolean {
  const path = originalUrl.split('?', 1)[0]
  return path === '/.well-known/openid-configuration' || path === '/oauth' || path.startsWith('/oauth/')
}

function devicePollDescription(error: string): string {
  if (error === 'authorization_pending') return '用户尚未完成设备授权确认'
  if (error === 'slow_down') return '设备轮询过于频繁，请降低频率'
  if (error === 'expired_token') return '设备码已过期'
  if (error === 'access_denied') return '用户拒绝了设备授权'
  return '设备码无效或已使用'
}

function secondsUntil(expiresAt: string): number {
  return Math.max(0, Math.floor((Date.parse(expiresAt) - Date.now()) / 1_000))
}

function normalizeScopes(value: string): string[] {
  return Array.from(new Set(value.split(/\s+/).map(scope => scope.trim()).filter(Boolean)))
}

function hasRequiredReadScopes(scopes: string[]): boolean {
  const granted = new Set(scopes)
  return Object.entries(requiredReadScopeByWriteScope).every(([writeScope, readScope]) => (
    !granted.has(writeScope) || granted.has(readScope)
  ))
}

function bearerToken(req: Request): string | undefined {
  const value = req.headers.authorization
  return value?.startsWith('Bearer ') ? value.slice(7).trim() || undefined : undefined
}

function stringQuery(value: unknown): string | undefined {
  return typeof value === 'string' ? value : undefined
}

function oauthError(error: string, description: string): { error: string; error_description: string } {
  return { error, error_description: description }
}

function redirectWithError(res: express.Response, redirectUri: string, state: string, error: string): void {
  const target = new URL(redirectUri)
  target.searchParams.set('error', error)
  target.searchParams.set('state', state)
  res.redirect(302, target.toString())
}

function deviceCodeEntryHtml(): string {
  return '<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>设备授权</title><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><main><h1>设备授权</h1><form method="get" action="/oauth/device"><label>设备码 <input name="user_code" autocomplete="one-time-code" required></label><button type="submit">继续</button></form></main></body></html>'
}

function deviceConsentHtml(authorization: OAuthDeviceAuthorization & { csrfToken: string }): string {
  const scopeText = authorization.scopes.map(scope => `<li>${escapeHtml(scope)}</li>`).join('')
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>设备授权确认</title><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><main><h1>设备授权确认</h1><p>设备码 <strong>${escapeHtml(authorization.userCode)}</strong> 请求访问你的 juhe-ai 个人资源。</p><ul>${scopeText}</ul><form method="post" action="/oauth/device/decision"><input type="hidden" name="user_code" value="${escapeHtml(authorization.userCode)}"><input type="hidden" name="csrf_token" value="${escapeHtml(authorization.csrfToken)}"><button name="decision" value="allow" type="submit">允许</button><button name="decision" value="deny" type="submit">拒绝</button></form></main></body></html>`
}

function deviceAuthorizationCompleteHtml(decision: 'allow' | 'deny'): string {
  const message = decision === 'allow' ? '设备已获授权，你可以回到设备继续。' : '设备授权已拒绝。'
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>设备授权完成</title></head><body><main><p>${message}</p></main></body></html>`
}

function deviceAuthorizationErrorHtml(message: string): string {
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>设备授权</title></head><body><main><p>${escapeHtml(message)}</p></main></body></html>`
}

function consentHtml(displayName: string, transaction: { id: string; csrfToken: string; scopes: string[] }): string {
  const scopeText = transaction.scopes.map(scope => `<li>${escapeHtml(scope)}</li>`).join('')
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>授权确认</title><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><main><h1>授权确认</h1><p>应用 <strong>${escapeHtml(displayName)}</strong> 请求访问你的 juhe-ai 个人资源。</p><ul>${scopeText}</ul><form method="post" action="/oauth/authorize/decision"><input type="hidden" name="transaction_id" value="${escapeHtml(transaction.id)}"><input type="hidden" name="csrf_token" value="${escapeHtml(transaction.csrfToken)}"><button name="decision" value="allow" type="submit">允许</button><button name="decision" value="deny" type="submit">拒绝</button></form></main></body></html>`
}

function escapeHtml(value: string): string {
  return value.replace(/[&<>"']/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[char] ?? char))
}

function isAllowedRedirectUri(uri: string, clientType: 'public' | 'confidential'): boolean {
  try {
    const parsed = new URL(uri)
    if (parsed.hash || parsed.username || parsed.password) return false
    if (parsed.protocol === 'https:') return true
    if (clientType === 'public' && parsed.protocol === 'http:' && isLoopbackHostname(parsed.hostname)) return true
    return clientType === 'public' && /^[a-z][a-z0-9+.-]*\.[a-z0-9.-]+$/.test(parsed.protocol.slice(0, -1))
  } catch {
    return false
  }
}

function matchesRegisteredRedirectUri(registeredUris: string[], requestedUri: string): boolean {
  if (registeredUris.includes(requestedUri)) return true
  let requested: URL
  try {
    requested = new URL(requestedUri)
  } catch {
    return false
  }
  if (requested.protocol !== 'http:' || !isLoopbackHostname(requested.hostname) || requested.hash || requested.username || requested.password) {
    return false
  }
  return registeredUris.some((registeredUri) => {
    try {
      const registered = new URL(registeredUri)
      return registered.protocol === requested.protocol
        && isLoopbackHostname(registered.hostname)
        && registered.hostname === requested.hostname
        && registered.pathname === requested.pathname
        && registered.search === requested.search
        && !registered.hash
        && !registered.username
        && !registered.password
    } catch {
      return false
    }
  })
}

function isLoopbackHostname(hostname: string): boolean {
  return hostname === '127.0.0.1' || hostname === '::1' || hostname === '[::1]'
}

class OAuthRouteError extends Error {
  constructor(readonly code: string, message: string, readonly statusCode: number) {
    super(message)
  }
}

class OidcUnavailableError extends Error {
  constructor() {
    super('OIDC 签名密钥未配置或不可用')
  }
}

oauthPublicRouter.use((error: unknown, _req: Request, res: Response, next: NextFunction) => {
  if (res.headersSent) {
    next(error)
    return
  }
  if (error instanceof OidcUnavailableError) {
    oidcUnavailable(res)
    return
  }
  if (error instanceof OAuthRouteError) {
    res.status(error.statusCode).json(oauthError(error.code, error.message))
    return
  }
  const description = process.env.NODE_ENV === 'test' && error instanceof Error
    ? error.message
    : 'OAuth 服务暂时不可用'
  res.status(500).json(oauthError('server_error', description))
})
