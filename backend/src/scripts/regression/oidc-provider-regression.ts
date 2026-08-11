import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { DatabaseSync } from 'node:sqlite'

import { decodeProtectedHeader, importJWK, jwtVerify } from 'jose'
import type { JWK } from 'jose'

if (process.env.JUHE_AI_OIDC_PROVIDER_DISABLED_REGRESSION_CHILD === '1') {
  await runDisabledChild()
  process.exit(0)
}

if (process.env.JUHE_AI_OIDC_PROVIDER_REGRESSION_CHILD === '1') {
  await runChild()
  process.exit(0)
}

if (process.env.JUHE_AI_OIDC_PROVIDER_LEGACY_SCHEMA_REGRESSION_CHILD === '1') {
  await runLegacySchemaUpgradeChild()
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

  const disabledResult = spawnSync(process.execPath, ['--import', 'tsx', fileURLToPath(import.meta.url)], {
    cwd: process.cwd(),
    env: {
      ...process.env,
      NODE_ENV: 'test',
      JUHE_AI_OIDC_PROVIDER_DISABLED_REGRESSION_CHILD: '1',
      JUHE_AI_PROCESS_ROLE: 'db-service',
      JUHE_AI_DATABASE_PATH: join(tempRoot, 'disabled-provider.sqlite3'),
      JUHE_AI_OIDC_ENABLED: 'false',
      JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
      JUHE_AI_LOG_FILE_ENABLED: 'false'
    },
    encoding: 'utf8'
  })
  if (disabledResult.status !== 0) {
    process.stdout.write(disabledResult.stdout)
    process.stderr.write(disabledResult.stderr)
    process.exit(disabledResult.status ?? 1)
  }
  process.stdout.write(disabledResult.stdout)

  const legacyDatabasePath = join(tempRoot, 'legacy-provider.sqlite3')
  const legacyDatabase = new DatabaseSync(legacyDatabasePath)
  try {
    legacyDatabase.exec(`
      CREATE TABLE oauth_clients (
        id TEXT PRIMARY KEY,
        client_id TEXT NOT NULL UNIQUE,
        display_name TEXT NOT NULL,
        client_type TEXT NOT NULL CHECK (client_type IN ('public', 'confidential')),
        client_secret_hash TEXT,
        redirect_uris_json TEXT NOT NULL,
        allowed_scopes_json TEXT NOT NULL,
        status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
        created_at TEXT NOT NULL,
        updated_at TEXT NOT NULL
      );
      INSERT INTO oauth_clients (
        id, client_id, display_name, client_type, client_secret_hash,
        redirect_uris_json, allowed_scopes_json, status, created_at, updated_at
      ) VALUES (
        'legacy-client-id', 'legacy-confidential-client', 'Legacy confidential client', 'confidential', 'legacy-secret-hash',
        '["https://legacy.example.test/callback"]', '["juhe:profile.read"]', 'active', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z'
      );
    `)
  } finally {
    legacyDatabase.close()
  }
  const legacySchemaResult = spawnSync(process.execPath, ['--import', 'tsx', fileURLToPath(import.meta.url)], {
    cwd: process.cwd(),
    env: {
      ...process.env,
      NODE_ENV: 'test',
      JUHE_AI_OIDC_PROVIDER_LEGACY_SCHEMA_REGRESSION_CHILD: '1',
      JUHE_AI_PROCESS_ROLE: 'db-service',
      JUHE_AI_DATABASE_PATH: legacyDatabasePath,
      JUHE_AI_OIDC_ENABLED: 'true',
      JUHE_AI_OIDC_ISSUER: 'http://127.0.0.1:39001',
      JUHE_AI_OIDC_KEY_ENCRYPTION_SECRET: 'oidc-regression-key-encryption-secret-32-bytes',
      JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
      JUHE_AI_LOG_FILE_ENABLED: 'false'
    },
    encoding: 'utf8'
  })
  if (legacySchemaResult.status !== 0) {
    process.stdout.write(legacySchemaResult.stdout)
    process.stderr.write(legacySchemaResult.stderr)
    process.exit(legacySchemaResult.status ?? 1)
  }
  process.stdout.write(legacySchemaResult.stdout)
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
    findActiveOidcSigningKey,
    exchangeAuthorizationCode,
    findAccessTokenContext,
    oidcSigningKeyRotationIntervalMs,
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
    const httpClient = createOAuthClient({
      displayName: 'OIDC HTTP Regression Client',
      clientType: 'public',
      redirectUris: ['https://example.com/oauth/callback'],
      allowedScopes: ['openid', 'profile', 'juhe:profile.read']
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
      const cookie = `juhe_ai_session=${createSession('sys_admin', 1).token}`
      const normalUserCookie = `juhe_ai_session=${createSession('oidc-user', 1).token}`
      assert.equal((await fetch(`${baseUrl}/__aisys__/api/oauth/integration-info`)).status, 401, '对接信息必须要求登录')
      assert.equal(
        (await fetch(`${baseUrl}/__aisys__/api/oauth/integration-info`, { headers: { cookie: normalUserCookie } })).status,
        403,
        '对接信息必须要求管理员'
      )
      const integrationInfoResponse = await fetch(`${baseUrl}/__aisys__/api/oauth/integration-info`, { headers: { cookie } })
      assert.equal(integrationInfoResponse.status, 200, '管理员必须能读取 OIDC 对接信息')
      const integrationInfo = await integrationInfoResponse.json() as { data?: Record<string, unknown> }
      assert.deepEqual(Object.keys(integrationInfo.data ?? {}).sort(), [
        'authorizationEndpoint',
        'deviceAuthorizationEndpoint',
        'discoveryUrl',
        'idTokenSigningAlgorithm',
        'issuer',
        'jwksUrl',
        'revocationEndpoint',
        'tokenEndpoint',
        'tokenRenewalEndpoint',
        'userinfoEndpoint'
      ], '对接信息只能返回固定的公开字段')
      assert.deepEqual(integrationInfo.data, {
        issuer: 'http://127.0.0.1:39001',
        discoveryUrl: 'http://127.0.0.1:39001/.well-known/openid-configuration',
        jwksUrl: 'http://127.0.0.1:39001/oauth/jwks',
        authorizationEndpoint: 'http://127.0.0.1:39001/oauth/authorize',
        tokenEndpoint: 'http://127.0.0.1:39001/oauth/token',
        userinfoEndpoint: 'http://127.0.0.1:39001/oauth/userinfo',
        deviceAuthorizationEndpoint: 'http://127.0.0.1:39001/oauth/device_authorization',
        revocationEndpoint: 'http://127.0.0.1:39001/oauth/revoke',
        tokenRenewalEndpoint: 'http://127.0.0.1:39001/oauth/token/renew',
        idTokenSigningAlgorithm: 'RS256'
      }, '对接信息必须从 runtime issuer 推导公开地址')
      const serializedIntegrationInfo = JSON.stringify(integrationInfo.data).toLowerCase()
      assert(!serializedIntegrationInfo.includes('private') && !serializedIntegrationInfo.includes('secret') && !serializedIntegrationInfo.includes('ciphertext'), '对接信息不得泄漏私钥、密文或 Client secret')
      const profileOnlyClientResponse = await fetch(`${baseUrl}/__aisys__/api/oauth/clients`, {
        method: 'POST',
        headers: { cookie, 'content-type': 'application/json' },
        body: JSON.stringify({
          displayName: 'Invalid Profile Client',
          clientType: 'public',
          redirectUris: ['https://invalid.example.test/callback'],
          allowedScopes: ['profile']
        })
      })
      assert.equal(profileOnlyClientResponse.status, 400, '管理 API 必须拒绝未同时登记 openid 的 profile scope')
      const writeOnlyClientResponse = await fetch(`${baseUrl}/__aisys__/api/oauth/clients`, {
        method: 'POST',
        headers: { cookie, 'content-type': 'application/json' },
        body: JSON.stringify({
          displayName: 'Invalid Write Only Client',
          clientType: 'public',
          redirectUris: ['https://invalid.example.test/write-callback'],
          allowedScopes: ['juhe:profile.write']
        })
      })
      assert.equal(writeOnlyClientResponse.status, 400, '管理 API 必须拒绝没有对应 read scope 的 write scope')
      const confidentialClientResponse = await fetch(`${baseUrl}/__aisys__/api/oauth/clients`, {
        method: 'POST',
        headers: { cookie, 'content-type': 'application/json' },
        body: JSON.stringify({
          displayName: 'Repeatable Guide Client',
          clientType: 'confidential',
          redirectUris: ['https://repeatable-guide.example.test/callback'],
          allowedScopes: ['openid', 'profile', 'juhe:profile.read']
        })
      })
      const confidentialClientResult = await confidentialClientResponse.json() as {
        data?: { clientId?: string; clientSecret?: string }
      }
      const confidentialClientId = confidentialClientResult.data?.clientId
      const initialClientSecret = confidentialClientResult.data?.clientSecret
      assert.equal(confidentialClientResponse.status, 201, '管理员必须能创建机密 Client')
      assert(confidentialClientId && initialClientSecret, '机密 Client 创建必须返回标识和初始密钥')
      assert.equal(
        (await fetch(`${baseUrl}/__aisys__/api/oauth/clients/${encodeURIComponent(confidentialClientId)}/integration-package`, {
          headers: { cookie: normalUserCookie }
        })).status,
        403,
        '机密 Client 对接包必须要求管理员权限'
      )
      const firstIntegrationPackageResponse = await fetch(`${baseUrl}/__aisys__/api/oauth/clients/${encodeURIComponent(confidentialClientId)}/integration-package`, {
        headers: { cookie }
      })
      const firstIntegrationPackage = await firstIntegrationPackageResponse.json() as {
        data?: { client?: { clientId?: string; clientSecretHash?: unknown }; clientSecret?: string }
      }
      assert.equal(firstIntegrationPackageResponse.status, 200, '管理员必须能下载机密 Client 对接包')
      assert.equal(firstIntegrationPackageResponse.headers.get('cache-control'), 'no-store', '含 Client Secret 的对接包不得被缓存')
      assert.equal(firstIntegrationPackage.data?.client?.clientId, confidentialClientId, '对接包必须绑定目标 Client')
      assert.equal(firstIntegrationPackage.data?.client?.clientSecretHash, undefined, '对接包不得返回 Client Secret 哈希')
      assert.equal(firstIntegrationPackage.data?.clientSecret, initialClientSecret, '首次下载必须返回当前 Client Secret')
      const repeatedIntegrationPackageResponse = await fetch(`${baseUrl}/__aisys__/api/oauth/clients/${encodeURIComponent(confidentialClientId)}/integration-package`, {
        headers: { cookie }
      })
      const repeatedIntegrationPackage = await repeatedIntegrationPackageResponse.json() as { data?: { clientSecret?: string } }
      assert.equal(repeatedIntegrationPackageResponse.status, 200, '管理员必须能重复下载机密 Client 对接包')
      assert.equal(repeatedIntegrationPackage.data?.clientSecret, initialClientSecret, '重复下载必须返回同一个当前 Client Secret')
      const reissueClientSecretResponse = await fetch(`${baseUrl}/__aisys__/api/oauth/clients/${encodeURIComponent(confidentialClientId)}/secret/reissue`, {
        method: 'POST',
        headers: { cookie }
      })
      const reissueClientSecretResult = await reissueClientSecretResponse.json() as { data?: { clientSecret?: string } }
      const reissuedClientSecret = reissueClientSecretResult.data?.clientSecret
      assert.equal(reissueClientSecretResponse.status, 200, '管理员必须能重新签发 Client Secret')
      assert(reissuedClientSecret && reissuedClientSecret !== initialClientSecret, '重新签发必须生成新的 Client Secret')
      const reissuedIntegrationPackageResponse = await fetch(`${baseUrl}/__aisys__/api/oauth/clients/${encodeURIComponent(confidentialClientId)}/integration-package`, {
        headers: { cookie }
      })
      const reissuedIntegrationPackage = await reissuedIntegrationPackageResponse.json() as { data?: { clientSecret?: string } }
      assert.equal(reissuedIntegrationPackage.data?.clientSecret, reissuedClientSecret, '重新签发后下载必须返回新的 Client Secret')
      const legacyClient = createOAuthClient({
        displayName: 'Legacy Secret Recovery Client',
        clientType: 'confidential',
        redirectUris: ['https://legacy-client.example.test/callback'],
        allowedScopes: ['juhe:profile.read']
      })
      database.prepare('UPDATE oauth_clients SET client_secret_ciphertext = NULL WHERE client_id = ?').run(legacyClient.clientId)
      assert.equal(
        (await fetch(`${baseUrl}/__aisys__/api/oauth/clients/${encodeURIComponent(legacyClient.clientId)}/integration-package`, {
          headers: { cookie }
        })).status,
        409,
        '历史 Client 没有密钥加密副本时必须要求重新签发，而不能伪造可下载密钥'
      )
      const legacyReissueResponse = await fetch(`${baseUrl}/__aisys__/api/oauth/clients/${encodeURIComponent(legacyClient.clientId)}/secret/reissue`, {
        method: 'POST',
        headers: { cookie }
      })
      const legacyReissue = await legacyReissueResponse.json() as { data?: { clientSecret?: string } }
      const legacyIntegrationPackageResponse = await fetch(`${baseUrl}/__aisys__/api/oauth/clients/${encodeURIComponent(legacyClient.clientId)}/integration-package`, {
        headers: { cookie }
      })
      const legacyIntegrationPackage = await legacyIntegrationPackageResponse.json() as { data?: { clientSecret?: string } }
      assert.equal(legacyReissueResponse.status, 200, '历史 Client 必须能通过重新签发恢复可下载文档')
      assert.equal(legacyIntegrationPackage.data?.clientSecret, legacyReissue.data?.clientSecret, '历史 Client 重新签发后文档必须含当前密钥')
      const unreadableSecretClient = createOAuthClient({
        displayName: 'Unreadable Secret Recovery Client',
        clientType: 'confidential',
        redirectUris: ['https://unreadable-client.example.test/callback'],
        allowedScopes: ['juhe:profile.read']
      })
      database.prepare('UPDATE oauth_clients SET client_secret_ciphertext = ? WHERE client_id = ?').run('corrupted-client-secret', unreadableSecretClient.clientId)
      const unreadableSecretResponse = await fetch(`${baseUrl}/__aisys__/api/oauth/clients/${encodeURIComponent(unreadableSecretClient.clientId)}/integration-package`, {
        headers: { cookie }
      })
      const unreadableSecretPayload = await unreadableSecretResponse.json() as { message?: string }
      assert.equal(unreadableSecretResponse.status, 409, '无法解密的 Client Secret 不得导致下载文档接口返回 500')
      assert.equal(unreadableSecretPayload.message, '该 Client 的当前 Client Secret 无法读取，请重新签发后再下载对接文档', '无法读取的 Client Secret 必须给出可恢复指引')
      const originalPrepare = database.prepare.bind(database)
      Object.defineProperty(database, 'prepare', {
        configurable: true,
        value(sql: string) {
          if (/SELECT\s+client_type,\s+client_secret_ciphertext/i.test(sql)) {
            throw new Error('forced oauth client secret storage failure')
          }
          return originalPrepare(sql)
        }
      })
      try {
        const storageFailureResponse = await fetch(`${baseUrl}/__aisys__/api/oauth/clients/${encodeURIComponent(confidentialClientId)}/integration-package`, {
          headers: { cookie }
        })
        assert.equal(storageFailureResponse.status, 500, '未知 Client Secret 存储错误不得伪装成可重新签发的 409')
      } finally {
        Reflect.deleteProperty(database, 'prepare')
      }
      assert.equal(findActiveOidcSigningKey(), undefined, '首次 OIDC 请求前不得依赖管理员手动生成签名密钥')
      const initialDiscoveryResponse = await fetch(`${baseUrl}/.well-known/openid-configuration`)
      assert.equal(initialDiscoveryResponse.status, 200, '首次 discovery 必须自动生成签名密钥并可用')
      const initialSigningKey = findActiveOidcSigningKey()
      assert(initialSigningKey, '首次 discovery 后必须存在 active signing key')
      database.prepare('UPDATE oauth_signing_keys SET created_at = ? WHERE kid = ?').run(
        new Date(Date.now() - oidcSigningKeyRotationIntervalMs - 1).toISOString(),
        initialSigningKey.kid
      )
      const discoveryResponse = await fetch(`${baseUrl}/.well-known/openid-configuration`)
      assert.equal(discoveryResponse.status, 200, '超过 7 天后的下一次 discovery 必须自动轮换签名密钥')
      const signingKey = findActiveOidcSigningKey()
      assert(signingKey && signingKey.kid !== initialSigningKey.kid, '每周自动轮换必须产生新的 active kid')
      const storedSigningKey = database.prepare('SELECT private_key_ciphertext FROM oauth_signing_keys WHERE kid = ?').get(signingKey.kid) as { private_key_ciphertext?: string } | undefined
      assert(storedSigningKey?.private_key_ciphertext && !storedSigningKey.private_key_ciphertext.includes('BEGIN PRIVATE KEY'), 'OIDC 私钥必须仅以密文持久化')
      const discovery = await discoveryResponse.json() as { jwks_uri?: string; userinfo_endpoint?: string; device_authorization_endpoint?: string; juhe_token_renewal_endpoint?: string }
      assert(discovery.jwks_uri && discovery.userinfo_endpoint && discovery.device_authorization_endpoint && discovery.juhe_token_renewal_endpoint, 'discovery 必须公开 OIDC 端点元数据和受控轮换端点')
      const jwksResponse = await fetch(localOidcUrl(discovery.jwks_uri))
      const jwks = await jwksResponse.json() as { keys?: JWK[] }
      const publicJwk = jwks.keys?.find(key => key.kid === signingKey.kid)
      const retiredPublicJwk = jwks.keys?.find(key => key.kid === initialSigningKey.kid)
      assert(publicJwk && publicJwk.kty === 'RSA' && publicJwk.d === undefined, 'JWKS 必须公开新的 RSA 公钥且不包含私钥')
      assert(retiredPublicJwk && retiredPublicJwk.d === undefined, 'JWKS 必须暂时保留旧 kid 的公开密钥，供已签发 ID Token 验签')
      assert.equal(
        (await fetch(`${baseUrl}/__aisys__/api/oauth/keys/rotate`, { method: 'POST', headers: { cookie } })).status,
        404,
        '管理面不得再提供手动轮换签名密钥接口'
      )
      clearOidcProtocolRateLimitStateForTest()
      const deviceAuthorizationHeaders = (secret: string): Record<string, string> => ({
        authorization: `Basic ${Buffer.from(`${confidentialClientId}:${secret}`).toString('base64')}`,
        'content-type': 'application/x-www-form-urlencoded'
      })
      const staleSecretDeviceAuthorization = await fetch(`${baseUrl}/oauth/device_authorization`, {
        method: 'POST',
        headers: deviceAuthorizationHeaders(initialClientSecret),
        body: new URLSearchParams({ scope: 'openid', nonce: 'stale-secret-nonce' })
      })
      assert.equal(staleSecretDeviceAuthorization.status, 401, '重新签发后旧 Client Secret 必须立即失效')
      const currentSecretDeviceAuthorization = await fetch(`${baseUrl}/oauth/device_authorization`, {
        method: 'POST',
        headers: deviceAuthorizationHeaders(reissuedClientSecret),
        body: new URLSearchParams({ scope: 'openid', nonce: 'current-secret-nonce' })
      })
      assert.equal(currentSecretDeviceAuthorization.status, 200, '重新签发后的当前 Client Secret 必须立即可用')
      clearOidcProtocolRateLimitStateForTest()
      for (let index = 0; index < 30; index += 1) {
        assert.equal((await fetch(`${baseUrl}/oauth/token`, { method: 'POST' })).status, 401, '未超限的 token 请求应先按 Client 鉴权处理')
      }
      const rateLimitedTokenResponse = await fetch(`${baseUrl}/oauth/token`, { method: 'POST' })
      assert.equal(rateLimitedTokenResponse.status, 429, 'OAuth token 端点必须有独立限流')
      assert(rateLimitedTokenResponse.headers.get('retry-after'), 'OAuth 限流响应必须给出 Retry-After')
      clearOidcProtocolRateLimitStateForTest()
      const pairedWriteScopeClient = createOAuthClient({
        displayName: 'OIDC Paired Write Scope Regression Client',
        clientType: 'public',
        redirectUris: ['https://paired-write.example.test/oauth/callback'],
        allowedScopes: ['juhe:profile.read', 'juhe:profile.write']
      })
      const writeOnlyAuthorizeUrl = new URL(`${baseUrl}/oauth/authorize`)
      writeOnlyAuthorizeUrl.search = new URLSearchParams({
        response_type: 'code',
        client_id: pairedWriteScopeClient.clientId,
        redirect_uri: pairedWriteScopeClient.redirectUris[0],
        scope: 'juhe:profile.write',
        state: 'write-only-state',
        code_challenge: createHash('sha256').update('w'.repeat(64)).digest('base64url'),
        code_challenge_method: 'S256'
      }).toString()
      const writeOnlyAuthorizeResponse = await fetch(writeOnlyAuthorizeUrl, { redirect: 'manual' })
      assert.equal(writeOnlyAuthorizeResponse.status, 400, '授权码流程必须拒绝未同时申请 read scope 的 write scope')
      assert.equal((await writeOnlyAuthorizeResponse.json() as { error?: string }).error, 'invalid_scope', '授权码流程的 write-only scope 必须返回 invalid_scope')
      const writeOnlyDeviceAuthorizationResponse = await fetch(`${baseUrl}/oauth/device_authorization`, {
        method: 'POST',
        headers: { 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({ client_id: pairedWriteScopeClient.clientId, scope: 'juhe:profile.write' })
      })
      assert.equal(writeOnlyDeviceAuthorizationResponse.status, 400, 'Device Flow 必须拒绝未同时申请 read scope 的 write scope')
      assert.equal((await writeOnlyDeviceAuthorizationResponse.json() as { error?: string }).error, 'invalid_scope', 'Device Flow 的 write-only scope 必须返回 invalid_scope')
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
      const revokeHttpTokenResponse = await fetch(`${baseUrl}/oauth/revoke`, {
        method: 'POST',
        headers: { 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({ client_id: httpClient.clientId, token: tokenPayload.access_token ?? '' })
      })
      assert.equal(revokeHttpTokenResponse.status, 200, 'Client 必须能撤销自己的 access token')
      assert.equal((await fetch(`${baseUrl}/oauth/userinfo`, { headers: { authorization: `Bearer ${tokenPayload.access_token}` } })).status, 401, '标准撤销后 access token 必须立即失效')

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

async function runDisabledChild(): Promise<void> {
  const { closeStorageDatabases } = await import('../../storage/database.js')
  const { createSystemApiApp } = await import('../../modules/system-api/system-api-app.js')
  const { createSession } = await import('../../storage/repositories.js')
  const httpServer = createSystemApiApp({ systemApiPrefix: '/__aisys__/api', publicApiPrefix: '/__aipublic__' }).listen(0, '127.0.0.1')
  try {
    await new Promise<void>((resolve, reject) => {
      httpServer.once('listening', () => resolve())
      httpServer.once('error', reject)
    })
    const address = httpServer.address()
    assert(address && typeof address !== 'string', 'OIDC disabled HTTP 回归监听地址无效')
    const cookie = `juhe_ai_session=${createSession('sys_admin', 1).token}`
    const response = await fetch(`http://127.0.0.1:${address.port}/__aisys__/api/oauth/clients`, {
      method: 'POST',
      headers: { cookie, 'content-type': 'application/json' },
      body: JSON.stringify({
        displayName: 'Disabled Provider Client',
        clientType: 'confidential',
        redirectUris: ['https://disabled.example.test/callback'],
        allowedScopes: ['openid', 'profile']
      })
    })
    assert.equal(response.status, 409, 'OIDC 未启用时不得创建会失去一次性交付文档的 Client')
    process.stdout.write('oidc-provider-disabled-regression: passed\n')
  } finally {
    await new Promise<void>((resolve) => httpServer.close(() => resolve()))
    closeStorageDatabases()
  }
}

async function runLegacySchemaUpgradeChild(): Promise<void> {
  const { getBusinessDatabase, closeStorageDatabases } = await import('../../storage/database.js')
  const { createSystemApiApp } = await import('../../modules/system-api/system-api-app.js')
  const { createSession } = await import('../../storage/repositories.js')
  const database = getBusinessDatabase()
  const columns = database.prepare('PRAGMA table_info(oauth_clients)').all() as Array<{ name?: string }>
  assert(columns.some((column) => column.name === 'client_secret_ciphertext'), '旧 SQLite 业务库启动后必须补齐 client_secret_ciphertext 列')
  const now = new Date().toISOString()
  database.prepare(`
    INSERT INTO system_accounts (
      id, username, display_name, role, status, password_hash, must_change_password,
      image_generation_enabled, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run('legacy-admin', 'legacy-admin', 'Legacy Admin', 'admin', 'active', 'not-used', 0, 0, now, now)
  const httpServer = createSystemApiApp({ systemApiPrefix: '/__aisys__/api', publicApiPrefix: '/__aipublic__' }).listen(0, '127.0.0.1')
  try {
    await new Promise<void>((resolve, reject) => {
      httpServer.once('listening', () => resolve())
      httpServer.once('error', reject)
    })
    const address = httpServer.address()
    assert(address && typeof address !== 'string', '旧 schema OIDC 回归监听地址无效')
    const baseUrl = `http://127.0.0.1:${address.port}`
    const cookie = `juhe_ai_session=${createSession('legacy-admin', 1).token}`
    const packageUrl = `${baseUrl}/__aisys__/api/oauth/clients/legacy-confidential-client/integration-package`
    const unavailableResponse = await fetch(packageUrl, { headers: { cookie } })
    assert.equal(unavailableResponse.status, 409, '升级前没有加密副本的历史 Client 下载必须返回可恢复 409')
    const reissueResponse = await fetch(`${baseUrl}/__aisys__/api/oauth/clients/legacy-confidential-client/secret/reissue`, {
      method: 'POST',
      headers: { cookie }
    })
    const reissuePayload = await reissueResponse.json() as { data?: { clientSecret?: string } }
    assert.equal(reissueResponse.status, 200, '旧 schema 升级后必须能重新签发 Client Secret')
    assert(reissuePayload.data?.clientSecret, '旧 schema 升级后的重新签发必须返回新 Client Secret')
    const recoveredResponse = await fetch(packageUrl, { headers: { cookie } })
    const recoveredPayload = await recoveredResponse.json() as { data?: { clientSecret?: string } }
    assert.equal(recoveredResponse.status, 200, '重新签发后历史 Client 必须可以下载对接文档')
    assert.equal(recoveredPayload.data?.clientSecret, reissuePayload.data?.clientSecret, '恢复下载必须返回重新签发后的当前 Client Secret')
    process.stdout.write('oidc-provider-legacy-schema-regression: passed\n')
  } finally {
    await new Promise<void>((resolve) => httpServer.close(() => resolve()))
    closeStorageDatabases()
  }
}
