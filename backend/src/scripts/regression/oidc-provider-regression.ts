import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { decodeProtectedHeader, importJWK, jwtVerify } from 'jose'
import type { JWK } from 'jose'

if (process.env.JUHE_AI_OIDC_PROVIDER_REGRESSION_CHILD === '1') {
  await runChild()
  process.exit(0)
}

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-oidc-provider-'))
try {
  const result = spawnSync(process.execPath, ['--import', 'tsx', fileURLToPath(import.meta.url)], {
    cwd: process.cwd(),
    env: {
      ...process.env,
      NODE_ENV: 'test',
      JUHE_AI_OIDC_PROVIDER_REGRESSION_CHILD: '1',
      JUHE_AI_PROCESS_ROLE: 'db-service',
      JUHE_AI_DATABASE_PATH: join(tempRoot, 'business.sqlite3'),
      JUHE_AI_OIDC_ENABLED: 'true',
      JUHE_AI_OIDC_ISSUER: 'http://127.0.0.1:39001',
      JUHE_AI_OIDC_KEY_ENCRYPTION_SECRET: 'oidc-regression-key-encryption-secret-32-bytes',
      JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
      JUHE_AI_LOG_FILE_ENABLED: 'false'
    },
    encoding: 'utf8'
  })
  if (result.status !== 0) {
    process.stdout.write(result.stdout)
    process.stderr.write(result.stderr)
    process.exit(result.status ?? 1)
  }
  process.stdout.write(result.stdout)
} finally {
  rmSync(tempRoot, { recursive: true, force: true })
}

async function runChild(): Promise<void> {
  const { getBusinessDatabase, closeStorageDatabases } = await import('../../storage/database.js')
  const {
    consumeAuthorizationTransaction,
    createAuthorizationCode,
    createAuthorizationTransaction,
    createOAuthClient,
    exchangeAuthorizationCode,
    findAccessTokenContext,
    revokeClientGrant,
    rotateOidcSigningKey,
    rotateAccessToken
  } = await import('../../modules/oidc-provider/oidc-provider.repository.js')
  const { clearOidcProtocolRateLimitStateForTest } = await import('../../modules/oidc-provider/oidc-rate-limit.middleware.js')
  const { createSystemApiApp } = await import('../../modules/system-api/system-api-app.js')
  const { createSession } = await import('../../storage/repositories.js')

  const database = getBusinessDatabase()
  const now = new Date().toISOString()
  database.prepare(`
    INSERT INTO system_accounts (
      id, username, display_name, role, status, password_hash, must_change_password,
      image_generation_enabled, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run('oidc-user', 'oidc-user', 'OIDC User', 'user', 'active', 'not-used', 0, 0, now, now)
  database.prepare(`
    INSERT INTO system_accounts (
      id, username, display_name, role, status, password_hash, must_change_password,
      image_generation_enabled, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run('oidc-normal-user', 'oidc-normal-user', 'OIDC Normal User', 'user', 'active', 'not-used', 0, 0, now, now)

  try {
    const client = createOAuthClient({
      displayName: 'OIDC Regression Client',
      clientType: 'public',
      redirectUris: ['com.example.app:/oauth/callback'],
      allowedScopes: ['juhe:profile.read']
    })
    const verifier = 'a'.repeat(64)
    const challenge = createHash('sha256').update(verifier).digest('base64url')
    const transaction = createAuthorizationTransaction({
      clientId: client.clientId,
      redirectUri: client.redirectUris[0],
      scopes: ['juhe:profile.read'],
      state: 'caller-state',
      codeChallenge: challenge
    })
    const consumed = consumeAuthorizationTransaction({ id: transaction.id, csrfToken: transaction.csrfToken })
    assert.equal(consumed?.state, 'caller-state', 'OIDC state 应加密存储并在事务消费时恢复')
    assert.equal(consumeAuthorizationTransaction({ id: transaction.id, csrfToken: transaction.csrfToken }), undefined, '授权事务必须一次性消费')

    const authorization = createAuthorizationCode({
      clientId: client.clientId,
      systemAccountId: 'oidc-user',
      scopes: ['juhe:profile.read'],
      redirectUri: client.redirectUris[0],
      codeChallenge: challenge
    })
    const exchanged = exchangeAuthorizationCode({
      clientId: client.clientId,
      code: authorization.code,
      redirectUri: client.redirectUris[0],
      codeVerifier: verifier
    })
    assert(exchanged, '正确 PKCE verifier 必须换得 access token')
    assert(Math.abs(Date.parse(exchanged.context.expiresAt) - Date.now() - 7 * 24 * 60 * 60 * 1_000) < 5_000, '初始 access token 不得超出 grant 的 7 天硬到期')
    assert.equal(exchangeAuthorizationCode({
      clientId: client.clientId,
      code: authorization.code,
      redirectUri: client.redirectUris[0],
      codeVerifier: verifier
    }), undefined, '授权码必须一次性消费')
    assert.equal(rotateAccessToken({ clientId: client.clientId, currentAccessToken: exchanged.accessToken }), 'not_eligible', '72 小时内不得轮换')

    database.prepare('UPDATE oauth_access_tokens SET issued_at = ? WHERE id = ?').run(
      new Date(Date.now() - 73 * 60 * 60 * 1_000).toISOString(),
      exchanged.context.tokenId
    )
    const rotated = rotateAccessToken({ clientId: client.clientId, currentAccessToken: exchanged.accessToken })
    assert(rotated && rotated !== 'not_eligible', '满 72 小时后应能轮换一次')
    assert.equal(findAccessTokenContext(exchanged.accessToken), undefined, '轮换成功后旧 token 必须立即失效')
    assert(findAccessTokenContext(rotated.accessToken), 'successor token 必须可用')
    assert.equal(Date.parse(rotated.context.expiresAt), Date.parse(exchanged.context.expiresAt), '轮换不得延长 grant 硬到期')
    assert.equal(revokeClientGrant('oidc-user', client.clientId), true, '撤销应命中当前用户和 Client 的 grant')
    assert.equal(findAccessTokenContext(rotated.accessToken), undefined, '撤销 grant 后全部 token 必须失效')

    const httpClient = createOAuthClient({
      displayName: 'OIDC HTTP Regression Client',
      clientType: 'public',
      redirectUris: ['https://example.com/oauth/callback'],
      allowedScopes: ['openid', 'profile', 'juhe:profile.read']
    })
    const normalConnectedClient = createOAuthClient({
      displayName: 'OIDC Normal User Connected Client',
      clientType: 'public',
      redirectUris: ['com.example.normal:/oauth/callback'],
      allowedScopes: ['openid']
    })
    createAuthorizationCode({
      clientId: normalConnectedClient.clientId,
      systemAccountId: 'oidc-normal-user',
      scopes: ['openid'],
      redirectUri: normalConnectedClient.redirectUris[0],
      codeChallenge: createHash('sha256').update('n'.repeat(64)).digest('base64url'),
      nonce: 'normal-connected-nonce'
    })
    const httpServer = createSystemApiApp({ systemApiPrefix: '/__aisys__/api', publicApiPrefix: '/__aipublic__' }).listen(0, '127.0.0.1')
    try {
      await new Promise<void>((resolve, reject) => {
        httpServer.once('listening', () => resolve())
        httpServer.once('error', reject)
      })
      const address = httpServer.address()
      assert(address && typeof address !== 'string', 'OIDC HTTP 回归监听地址无效')
      const baseUrl = `http://127.0.0.1:${address.port}`
      const localOidcUrl = (configuredUrl: string): string => {
        const url = new URL(configuredUrl)
        url.port = String(address.port)
        return url.toString()
      }
      const unavailableDiscovery = await fetch(`${baseUrl}/.well-known/openid-configuration`)
      assert.equal(unavailableDiscovery.status, 503, '未显式轮换 active key 时 discovery 必须不可用')
      assert.equal((await fetch(`${baseUrl}/oauth/jwks`)).status, 503, '未显式轮换 active key 时 JWKS 必须不可用')
      assert.equal((await fetch(`${baseUrl}/oauth/authorize`)).status, 503, '未显式轮换 active key 时授权端点必须不可用')
      assert.equal((await fetch(`${baseUrl}/oauth/token`, { method: 'POST' })).status, 503, '未显式轮换 active key 时 token 端点必须不可用')
      assert.equal((await fetch(`${baseUrl}/oauth/device_authorization`, { method: 'POST' })).status, 503, '未显式轮换 active key 时设备端点必须不可用')
      assert.equal((await fetch(`${baseUrl}/oauth/userinfo`)).status, 503, '未显式轮换 active key 时 UserInfo 必须不可用')
      const signingKey = await rotateOidcSigningKey()
      const storedSigningKey = database.prepare('SELECT private_key_ciphertext FROM oauth_signing_keys WHERE kid = ?').get(signingKey.kid) as { private_key_ciphertext?: string } | undefined
      assert(storedSigningKey?.private_key_ciphertext && !storedSigningKey.private_key_ciphertext.includes('BEGIN PRIVATE KEY'), 'OIDC 私钥必须仅以密文持久化')
      const discoveryResponse = await fetch(`${baseUrl}/.well-known/openid-configuration`)
      assert.equal(discoveryResponse.status, 200, '显式轮换 active key 后 discovery 必须可用')
      const discovery = await discoveryResponse.json() as { jwks_uri?: string; userinfo_endpoint?: string; device_authorization_endpoint?: string; juhe_token_renewal_endpoint?: string }
      assert(discovery.jwks_uri && discovery.userinfo_endpoint && discovery.device_authorization_endpoint && discovery.juhe_token_renewal_endpoint, 'discovery 必须公开 OIDC 端点元数据和受控轮换端点')
      const jwksResponse = await fetch(localOidcUrl(discovery.jwks_uri))
      const jwks = await jwksResponse.json() as { keys?: JWK[] }
      const publicJwk = jwks.keys?.find(key => key.kid === signingKey.kid)
      assert(publicJwk && publicJwk.kty === 'RSA' && publicJwk.d === undefined, 'JWKS 只能公开当前 RSA 公钥')
      clearOidcProtocolRateLimitStateForTest()
      for (let index = 0; index < 30; index += 1) {
        assert.equal((await fetch(`${baseUrl}/oauth/token`, { method: 'POST' })).status, 401, '未超限的 token 请求应先按 Client 鉴权处理')
      }
      const rateLimitedTokenResponse = await fetch(`${baseUrl}/oauth/token`, { method: 'POST' })
      assert.equal(rateLimitedTokenResponse.status, 429, 'OAuth token 端点必须有独立限流')
      assert(rateLimitedTokenResponse.headers.get('retry-after'), 'OAuth 限流响应必须给出 Retry-After')
      clearOidcProtocolRateLimitStateForTest()
      const verifier2 = 'b'.repeat(64)
      const authorizeUrl = new URL(`${baseUrl}/oauth/authorize`)
      authorizeUrl.search = new URLSearchParams({
        response_type: 'code',
        client_id: httpClient.clientId,
        redirect_uri: httpClient.redirectUris[0],
        scope: 'openid profile juhe:profile.read',
        state: 'http-state',
        nonce: 'http-regression-nonce',
        code_challenge: createHash('sha256').update(verifier2).digest('base64url'),
        code_challenge_method: 'S256'
      }).toString()
      const unauthenticated = await fetch(authorizeUrl, { redirect: 'manual' })
      assert.equal(unauthenticated.status, 302, '未登录授权必须跳转登录页')
      const loginLocation = unauthenticated.headers.get('location') ?? ''
      const transactionId = new URL(`http://127.0.0.1${loginLocation}`).searchParams.get('transaction_id')
        ?? new URL(`http://127.0.0.1${loginLocation}`).searchParams.get('redirect')?.match(/transaction_id=([0-9a-f-]+)/)?.[1]
      assert(transactionId, '登录跳转必须只携带服务端 transaction_id')
      const cookie = `juhe_ai_session=${createSession('sys_admin', 1).token}`
      const normalUserCookie = `juhe_ai_session=${createSession('oidc-normal-user', 1).token}`
      const consent = await fetch(`${baseUrl}/oauth/authorize?transaction_id=${transactionId}`, {
        headers: { cookie },
        redirect: 'manual'
      })
      const consentHtml = await consent.text()
      assert.equal(consent.status, 200, `已登录授权必须返回同意页：${consentHtml}`)
      const csrfToken = consentHtml.match(/name="csrf_token" value="([^"]+)"/)?.[1]
      assert(csrfToken, '同意页必须包含一次性 CSRF token')
      const decision = await fetch(`${baseUrl}/oauth/authorize/decision`, {
        method: 'POST',
        headers: { cookie, 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({ transaction_id: transactionId, csrf_token: csrfToken, decision: 'allow' }),
        redirect: 'manual'
      })
      const callbackLocation = decision.headers.get('location') ?? ''
      const callback = new URL(callbackLocation)
      assert.equal(decision.status, 302, '允许后必须回调已登记 redirect URI')
      assert.equal(callback.searchParams.get('state'), 'http-state', 'state 必须原样回送')
      const exchangeHttpAuthorizationCode = () => fetch(`${baseUrl}/oauth/token`, {
        method: 'POST',
        headers: { 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({
          grant_type: 'authorization_code',
          client_id: httpClient.clientId,
          code: callback.searchParams.get('code') ?? '',
          redirect_uri: httpClient.redirectUris[0],
          code_verifier: verifier2
        })
      })
      const originalPrivateKeyCiphertext = storedSigningKey?.private_key_ciphertext
      assert(originalPrivateKeyCiphertext, 'OIDC 回归必须保留签名私钥密文')
      database.prepare('UPDATE oauth_signing_keys SET private_key_ciphertext = ? WHERE kid = ?').run('corrupted-oidc-key', signingKey.kid)
      const unavailableTokenResponse = await exchangeHttpAuthorizationCode()
      assert.equal(unavailableTokenResponse.status, 503, '签名密钥不可用时不得消费一次性授权码')
      database.prepare('UPDATE oauth_signing_keys SET private_key_ciphertext = ? WHERE kid = ?').run(originalPrivateKeyCiphertext, signingKey.kid)
      const tokenResponse = await exchangeHttpAuthorizationCode()
      assert.equal(tokenResponse.status, 200, 'HTTP 授权码换 token 必须成功')
      const tokenPayload = await tokenResponse.json() as { token_type?: string; access_token?: string; id_token?: string }
      assert.equal(tokenPayload.token_type, 'Bearer')
      assert(tokenPayload.access_token && tokenPayload.id_token, 'openid 授权码兑换必须返回 access token 与 ID Token')
      assert.equal(decodeProtectedHeader(tokenPayload.id_token).kid, signingKey.kid, 'ID Token 必须使用 active key 的 kid')
      const verified = await jwtVerify(tokenPayload.id_token, await importJWK(publicJwk, 'RS256'), {
        issuer: 'http://127.0.0.1:39001',
        audience: httpClient.clientId
      })
      assert.equal(verified.payload.nonce, 'http-regression-nonce', 'ID Token 必须原样携带授权请求 nonce')
      assert.notEqual(verified.payload.sub, 'sys_admin', 'ID Token sub 不得泄漏内部 system account ID')
      const userinfoResponse = await fetch(`${baseUrl}/oauth/userinfo`, { headers: { authorization: `Bearer ${tokenPayload.access_token}` } })
      const userinfo = await userinfoResponse.json() as Record<string, unknown>
      assert.equal(userinfoResponse.status, 200, 'openid access token 必须可调用 UserInfo')
      assert.equal(userinfo.sub, verified.payload.sub, 'UserInfo 与 ID Token 必须使用相同稳定 sub')
      assert.equal(typeof userinfo.name, 'string', 'profile scope 必须提供标准 name claim')
      assert.equal(userinfo.role, undefined, 'UserInfo 不得泄漏内部 role')
      assert.deepEqual(Object.keys(userinfo).sort(), ['name', 'preferred_username', 'sub'], 'UserInfo 只能返回已授权的标准低敏 claims')

      const delegatedClient = createOAuthClient({
        displayName: 'Delegated API Regression Client',
        clientType: 'public',
        redirectUris: ['https://delegated.example.com/oauth/callback'],
        allowedScopes: ['juhe:profile.read', 'juhe:request_limits.read']
      })
      const delegatedVerifier = 'd'.repeat(64)
      const delegatedAuthorization = createAuthorizationCode({
        clientId: delegatedClient.clientId,
        systemAccountId: 'oidc-user',
        scopes: ['juhe:profile.read', 'juhe:request_limits.read'],
        redirectUri: delegatedClient.redirectUris[0],
        codeChallenge: createHash('sha256').update(delegatedVerifier).digest('base64url')
      })
      const delegatedToken = exchangeAuthorizationCode({
        clientId: delegatedClient.clientId,
        code: delegatedAuthorization.code,
        redirectUri: delegatedClient.redirectUris[0],
        codeVerifier: delegatedVerifier
      })
      assert(delegatedToken, '委托 API 回归必须先取得独立 bearer token')
      const delegatedHeaders = { authorization: `Bearer ${delegatedToken.accessToken}` }
      const delegatedProfileResponse = await fetch(`${baseUrl}/__aidelegated__/v1/profile`, { headers: delegatedHeaders })
      const delegatedProfile = await delegatedProfileResponse.json() as { data?: { username?: string; displayName?: string } }
      assert.equal(delegatedProfileResponse.status, 200, '委托 profile.read 必须可读取本人资料')
      assert.equal(delegatedProfile.data?.username, 'oidc-user', '委托 profile 只能返回 token 绑定的用户')
      const delegatedLimitsResponse = await fetch(`${baseUrl}/__aidelegated__/v1/request-limits`, { headers: delegatedHeaders })
      const delegatedLimits = await delegatedLimitsResponse.json() as { data?: { usageStatus?: string; windows?: Record<string, unknown> } }
      assert.equal(delegatedLimitsResponse.status, 200, '委托 request_limits.read 必须可读取请求限制快照')
      assert(['estimated', 'unavailable', 'not_tracked'].includes(delegatedLimits.data?.usageStatus ?? ''), '请求限制快照必须明确 usageStatus')
      assert(delegatedLimits.data?.windows && Object.keys(delegatedLimits.data.windows).length === 4, '请求限制必须返回四个时间窗口')
      const delegatedScopeDeniedResponse = await fetch(`${baseUrl}/__aidelegated__/v1/groups`, { headers: delegatedHeaders })
      assert.equal(delegatedScopeDeniedResponse.status, 403, '缺少 groups.read 时委托 API 必须返回 insufficient_scope')
      const disableClientResponse = await fetch(`${baseUrl}/__aisys__/api/oauth/clients/${encodeURIComponent(delegatedClient.clientId)}`, {
        method: 'PATCH',
        headers: { cookie, 'content-type': 'application/json' },
        body: JSON.stringify({ status: 'disabled' })
      })
      assert.equal(disableClientResponse.status, 200, '管理员必须能停用 Client')
      assert.equal((await fetch(`${baseUrl}/__aidelegated__/v1/profile`, { headers: delegatedHeaders })).status, 401, '停用 Client 后其现有 token 必须立即失效')

      const connectedApplicationsResponse = await fetch(`${baseUrl}/__aisys__/api/oauth/connected-applications`, { headers: { cookie } })
      const connectedApplications = await connectedApplicationsResponse.json() as { data?: Array<{ clientId?: string; status?: string }> }
      assert.equal(connectedApplicationsResponse.status, 200, '正常会话用户必须可读取自己的已授权应用')
      assert(connectedApplications.data?.some(application => application.clientId === httpClient.clientId && application.status === 'active'), '已授权应用必须包含当前用户的 active grant')
      const revokeConnectedApplication = await fetch(`${baseUrl}/__aisys__/api/oauth/connected-applications/${encodeURIComponent(httpClient.clientId)}`, {
        method: 'DELETE',
        headers: { cookie }
      })
      assert.equal(revokeConnectedApplication.status, 200, '正常会话用户必须能撤销自己的已授权应用')
      assert.equal((await fetch(`${baseUrl}/oauth/userinfo`, { headers: { authorization: `Bearer ${tokenPayload.access_token}` } })).status, 401, '撤销已授权应用后其 access token 必须失效')
      const normalConnectedApplicationsResponse = await fetch(`${baseUrl}/__aisys__/api/oauth/connected-applications`, { headers: { cookie: normalUserCookie } })
      const normalConnectedApplications = await normalConnectedApplicationsResponse.json() as { data?: Array<{ clientId?: string }> }
      assert.equal(normalConnectedApplicationsResponse.status, 200, '普通用户必须可读取自己的已授权应用')
      assert(normalConnectedApplications.data?.some(application => application.clientId === normalConnectedClient.clientId), '普通用户列表必须包含自己的 grant')
      assert(!normalConnectedApplications.data?.some(application => application.clientId === httpClient.clientId), '普通用户列表不得泄漏其他用户的 grant')
      const normalRevoke = await fetch(`${baseUrl}/__aisys__/api/oauth/connected-applications/${encodeURIComponent(normalConnectedClient.clientId)}`, {
        method: 'DELETE',
        headers: { cookie: normalUserCookie }
      })
      assert.equal(normalRevoke.status, 200, '普通用户必须只能撤销自己的 grant')

      const deviceClient = createOAuthClient({
        displayName: 'OIDC Device Regression Client',
        clientType: 'public',
        redirectUris: ['com.example.device:/oauth/callback'],
        allowedScopes: ['openid', 'profile', 'juhe:profile.read']
      })
      const deviceMissingNonce = await fetch(`${baseUrl}/oauth/device_authorization`, {
        method: 'POST',
        headers: { 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({ client_id: deviceClient.clientId, scope: 'openid profile' })
      })
      assert.equal((await deviceMissingNonce.json() as { error?: string }).error, 'invalid_request', 'openid Device Authorization 必须要求 nonce')
      const deviceAuthorizationResponse = await fetch(`${baseUrl}/oauth/device_authorization`, {
        method: 'POST',
        headers: { 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({ client_id: deviceClient.clientId, scope: 'openid profile', nonce: 'device-regression-nonce' })
      })
      const deviceAuthorization = await deviceAuthorizationResponse.json() as {
        device_code?: string
        user_code?: string
        verification_uri_complete?: string
      }
      assert.equal(deviceAuthorizationResponse.status, 200, 'Device Authorization endpoint 必须签发设备码')
      assert(deviceAuthorization.device_code && deviceAuthorization.user_code && deviceAuthorization.verification_uri_complete, 'Device Authorization response 字段不完整')
      const devicePoll = async (): Promise<Response> => fetch(`${baseUrl}/oauth/token`, {
        method: 'POST',
        headers: { 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({
          grant_type: 'urn:ietf:params:oauth:grant-type:device_code',
          client_id: deviceClient.clientId,
          device_code: deviceAuthorization.device_code ?? ''
        })
      })
      const pendingPoll = await devicePoll()
      assert.equal((await pendingPoll.json() as { error?: string }).error, 'authorization_pending', '设备码首次轮询必须保持 pending')
      const slowDownPoll = await devicePoll()
      assert.equal((await slowDownPoll.json() as { error?: string }).error, 'slow_down', '设备码过快轮询必须返回 slow_down')
      const deviceConsent = await fetch(localOidcUrl(deviceAuthorization.verification_uri_complete), {
        headers: { cookie },
        redirect: 'manual'
      })
      const deviceConsentHtml = await deviceConsent.text()
      const deviceCsrfToken = deviceConsentHtml.match(/name="csrf_token" value="([^"]+)"/)?.[1]
      assert.equal(deviceConsent.status, 200, '设备用户码必须打开浏览器同意页')
      assert(deviceCsrfToken, '设备同意页必须包含一次性 CSRF token')
      const deviceDecision = await fetch(`${baseUrl}/oauth/device/decision`, {
        method: 'POST',
        headers: { cookie, 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({
          user_code: deviceAuthorization.user_code,
          csrf_token: deviceCsrfToken,
          decision: 'allow'
        })
      })
      assert.equal(deviceDecision.status, 200, '浏览器允许后设备授权必须完成')
      database.prepare('UPDATE oauth_device_authorizations SET last_polled_at = ? WHERE user_code = ?').run(
        new Date(Date.now() - 90_000).toISOString(),
        deviceAuthorization.user_code
      )
      const approvedPoll = await devicePoll()
      const approvedDeviceToken = await approvedPoll.json() as { access_token?: string; id_token?: string }
      assert.equal(approvedPoll.status, 200, '完成浏览器同意后设备轮询必须签发 token')
      assert(approvedDeviceToken.access_token && approvedDeviceToken.id_token, 'openid Device Flow 必须返回 ID Token')
      const verifiedDeviceToken = await jwtVerify(approvedDeviceToken.id_token, await importJWK(publicJwk, 'RS256'), {
        issuer: 'http://127.0.0.1:39001',
        audience: deviceClient.clientId
      })
      assert.equal(verifiedDeviceToken.payload.nonce, 'device-regression-nonce', 'Device Flow ID Token 必须绑定初始 nonce')
      assert.equal(verifiedDeviceToken.payload.sub, verified.payload.sub, '同一用户跨授权码与 Device Flow 必须保持稳定 sub')
      const consumedPoll = await devicePoll()
      assert.equal((await consumedPoll.json() as { error?: string }).error, 'invalid_grant', '设备码必须一次性消费')

      const deniedAuthorizationResponse = await fetch(`${baseUrl}/oauth/device_authorization`, {
        method: 'POST',
        headers: { 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({ client_id: deviceClient.clientId, scope: 'juhe:profile.read' })
      })
      const deniedAuthorization = await deniedAuthorizationResponse.json() as { device_code?: string; user_code?: string; verification_uri_complete?: string }
      assert(deniedAuthorization.device_code && deniedAuthorization.user_code && deniedAuthorization.verification_uri_complete, '拒绝场景设备码创建失败')
      const deniedConsent = await fetch(localOidcUrl(deniedAuthorization.verification_uri_complete), { headers: { cookie } })
      const deniedCsrfToken = (await deniedConsent.text()).match(/name="csrf_token" value="([^"]+)"/)?.[1]
      assert(deniedCsrfToken, '拒绝场景设备同意页必须包含 CSRF token')
      await fetch(`${baseUrl}/oauth/device/decision`, {
        method: 'POST',
        headers: { cookie, 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({ user_code: deniedAuthorization.user_code, csrf_token: deniedCsrfToken, decision: 'deny' })
      })
      const deniedPoll = await fetch(`${baseUrl}/oauth/token`, {
        method: 'POST',
        headers: { 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({
          grant_type: 'urn:ietf:params:oauth:grant-type:device_code',
          client_id: deviceClient.clientId,
          device_code: deniedAuthorization.device_code
        })
      })
      assert.equal((await deniedPoll.json() as { error?: string }).error, 'access_denied', '用户拒绝时设备轮询必须返回 access_denied')

      const expiredAuthorizationResponse = await fetch(`${baseUrl}/oauth/device_authorization`, {
        method: 'POST',
        headers: { 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({ client_id: deviceClient.clientId, scope: 'juhe:profile.read' })
      })
      const expiredAuthorization = await expiredAuthorizationResponse.json() as { device_code?: string; user_code?: string }
      assert(expiredAuthorization.device_code && expiredAuthorization.user_code, '过期场景设备码创建失败')
      database.prepare('UPDATE oauth_device_authorizations SET expires_at = ? WHERE user_code = ?').run(
        new Date(Date.now() - 1_000).toISOString(),
        expiredAuthorization.user_code
      )
      const expiredPoll = await fetch(`${baseUrl}/oauth/token`, {
        method: 'POST',
        headers: { 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({
          grant_type: 'urn:ietf:params:oauth:grant-type:device_code',
          client_id: deviceClient.clientId,
          device_code: expiredAuthorization.device_code
        })
      })
      assert.equal((await expiredPoll.json() as { error?: string }).error, 'expired_token', '过期设备码必须返回 expired_token')

      const loopbackClient = createOAuthClient({
        displayName: 'OIDC Loopback Regression Client',
        clientType: 'public',
        redirectUris: ['http://127.0.0.1/oauth/callback?flow=1'],
        allowedScopes: ['openid', 'profile']
      })
      const loopbackVerifier = 'c'.repeat(64)
      const loopbackRedirect = 'http://127.0.0.1:49152/oauth/callback?flow=1'
      const loopbackAuthorizeUrl = new URL(`${baseUrl}/oauth/authorize`)
      loopbackAuthorizeUrl.search = new URLSearchParams({
        response_type: 'code',
        client_id: loopbackClient.clientId,
        redirect_uri: loopbackRedirect,
        scope: 'openid profile',
        state: 'loopback-state',
        nonce: 'loopback-regression-nonce',
        code_challenge: createHash('sha256').update(loopbackVerifier).digest('base64url'),
        code_challenge_method: 'S256'
      }).toString()
      const loopbackLogin = await fetch(loopbackAuthorizeUrl, { redirect: 'manual' })
      const loopbackTransactionId = new URL(`http://127.0.0.1${loopbackLogin.headers.get('location') ?? ''}`).searchParams.get('redirect')?.match(/transaction_id=([0-9a-f-]+)/)?.[1]
      assert(loopbackTransactionId, '动态 loopback redirect 必须创建服务端授权事务')
      const loopbackConsent = await fetch(`${baseUrl}/oauth/authorize?transaction_id=${loopbackTransactionId}`, { headers: { cookie } })
      const loopbackCsrfToken = (await loopbackConsent.text()).match(/name="csrf_token" value="([^"]+)"/)?.[1]
      assert(loopbackCsrfToken, '动态 loopback redirect 同意页必须包含 CSRF token')
      const loopbackDecision = await fetch(`${baseUrl}/oauth/authorize/decision`, {
        method: 'POST',
        headers: { cookie, 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({ transaction_id: loopbackTransactionId, csrf_token: loopbackCsrfToken, decision: 'allow' }),
        redirect: 'manual'
      })
      assert.equal(new URL(loopbackDecision.headers.get('location') ?? '').origin, 'http://127.0.0.1:49152', 'loopback redirect 只允许动态端口，不得改变 host/path')
      const loopbackCallback = new URL(loopbackDecision.headers.get('location') ?? '')
      assert.equal(loopbackCallback.pathname, '/oauth/callback', 'loopback redirect path 必须精确匹配')
      assert.equal(loopbackCallback.searchParams.get('flow'), '1', 'loopback redirect query 必须精确匹配')
      const loopbackToken = await fetch(`${baseUrl}/oauth/token`, {
        method: 'POST',
        headers: { 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({
          grant_type: 'authorization_code',
          client_id: loopbackClient.clientId,
          code: loopbackCallback.searchParams.get('code') ?? '',
          redirect_uri: loopbackRedirect,
          code_verifier: loopbackVerifier
        })
      })
      assert.equal(loopbackToken.status, 200, '动态 loopback redirect 必须可完成 token 兑换')
      const wrongLoopbackQuery = new URL(`${baseUrl}/oauth/authorize`)
      wrongLoopbackQuery.search = new URLSearchParams({
        response_type: 'code',
        client_id: loopbackClient.clientId,
        redirect_uri: 'http://127.0.0.1:49152/oauth/callback?flow=2',
        scope: 'openid profile',
        state: 'wrong-loopback-query',
        nonce: 'wrong-loopback-query-nonce',
        code_challenge: createHash('sha256').update('e'.repeat(64)).digest('base64url'),
        code_challenge_method: 'S256'
      }).toString()
      assert.equal((await fetch(wrongLoopbackQuery, { redirect: 'manual' })).status, 400, 'loopback redirect query 必须精确匹配')
      const wrongCustomScheme = new URL(`${baseUrl}/oauth/authorize`)
      wrongCustomScheme.search = new URLSearchParams({
        response_type: 'code',
        client_id: deviceClient.clientId,
        redirect_uri: 'com.example.other:/oauth/callback',
        scope: 'profile',
        state: 'wrong-scheme',
        code_challenge: createHash('sha256').update('d'.repeat(64)).digest('base64url'),
        code_challenge_method: 'S256'
      }).toString()
      assert.equal((await fetch(wrongCustomScheme, { redirect: 'manual' })).status, 400, 'custom URI scheme 必须精确匹配')
    } finally {
      await new Promise<void>((resolve) => httpServer.close(() => resolve()))
    }
    process.stdout.write('oidc-provider-regression: passed\n')
  } finally {
    closeStorageDatabases()
  }
}
