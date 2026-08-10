import { createHash } from 'node:crypto'

import type { SystemAccountSummary } from '../../../../domain/types.js'
import {
  createAuthorizationCode,
  createAuthorizationTransaction,
  createDeviceAuthorization,
  createOAuthClient,
  decideDeviceAuthorization,
  exchangeAuthorizationCode,
  findActiveOidcSigningKey,
  prepareDeviceAuthorization,
  rotateOidcSigningKey
} from '../../../../modules/oidc-provider/oidc-provider.repository.js'
import { namePrefix, type OidcProviderMockdata } from '../shared.js'

const browserRedirectUri = 'http://127.0.0.1:43817/callback'
const serviceRedirectUri = 'https://mock-client.example.test/oauth/callback'
const browserScopes = [
  'openid',
  'profile',
  'juhe:profile.read',
  'juhe:groups.read',
  'juhe:route_strategies.read',
  'juhe:api_keys.read',
  'juhe:ai_accounts.read',
  'juhe:request_limits.read'
]

export async function createOidcProviderMockdata(admin: SystemAccountSummary): Promise<OidcProviderMockdata> {
  if (!findActiveOidcSigningKey()) {
    await rotateOidcSigningKey()
  }

  const browserClient = createOAuthClient({
    displayName: `${namePrefix}浏览器授权演示应用`,
    clientType: 'public',
    redirectUris: [browserRedirectUri],
    allowedScopes: browserScopes
  })
  const serviceClient = createOAuthClient({
    displayName: `${namePrefix}服务端集成演示应用`,
    clientType: 'confidential',
    redirectUris: [serviceRedirectUri],
    allowedScopes: ['juhe:profile.read', 'juhe:request_limits.read']
  })

  const codeVerifier = 'mockdata-browser-authorize-code-verifier-000000000000000000000000'
  const codeChallenge = createHash('sha256').update(codeVerifier).digest('base64url')
  const authorization = createAuthorizationCode({
    clientId: browserClient.clientId,
    systemAccountId: admin.id,
    scopes: browserScopes,
    redirectUri: browserRedirectUri,
    codeChallenge,
    nonce: 'mockdata-browser-oidc-nonce'
  })
  const exchanged = exchangeAuthorizationCode({
    clientId: browserClient.clientId,
    code: authorization.code,
    redirectUri: browserRedirectUri,
    codeVerifier
  })
  if (!exchanged) {
    throw new Error('Mockdata OIDC 浏览器授权码交换失败')
  }

  createAuthorizationTransaction({
    clientId: serviceClient.clientId,
    redirectUri: serviceRedirectUri,
    scopes: ['juhe:profile.read', 'juhe:request_limits.read'],
    state: 'mockdata-service-authorize-state',
    codeChallenge,
    nonce: 'mockdata-service-oidc-nonce'
  })

  const device = createDeviceAuthorization({
    clientId: browserClient.clientId,
    scopes: ['openid', 'profile', 'juhe:profile.read'],
    nonce: 'mockdata-device-oidc-nonce',
    verificationUri: 'http://127.0.0.1:59752/oauth/device'
  })
  const preparedDevice = prepareDeviceAuthorization(device.authorization.userCode)
  if (!preparedDevice) {
    throw new Error('Mockdata OIDC Device Flow 准备失败')
  }
  const approvedDevice = decideDeviceAuthorization({
    userCode: device.authorization.userCode,
    csrfToken: preparedDevice.csrfToken,
    systemAccountId: admin.id,
    decision: 'allow'
  })
  if (!approvedDevice || approvedDevice.status !== 'approved') {
    throw new Error('Mockdata OIDC Device Flow 授权失败')
  }

  return {
    browserClientId: browserClient.clientId,
    browserClientName: browserClient.displayName,
    serviceClientId: serviceClient.clientId,
    serviceClientName: serviceClient.displayName
  }
}
