import { createHash, randomBytes, randomUUID } from 'node:crypto'

import { getBusinessDatabase } from '../../storage/database.js'
import { hashSecret } from '../../storage/crypto.js'
import {
  createOidcSigningKeyMaterial,
  decryptOidcValue,
  encryptOidcValue,
  type OidcSigningKeyMaterial
} from './oidc-provider.crypto.js'

const authorizationCodeLifetimeMs = 120_000
const grantLifetimeMs = 168 * 60 * 60 * 1_000
const tokenRenewalDelayMs = 72 * 60 * 60 * 1_000

export type OAuthClientType = 'public' | 'confidential'

export interface OAuthClient {
  id: string
  clientId: string
  displayName: string
  clientType: OAuthClientType
  clientSecretHash?: string
  redirectUris: string[]
  allowedScopes: string[]
  status: 'active' | 'disabled'
  createdAt: string
  updatedAt: string
}

export interface OAuthGrant {
  id: string
  clientId: string
  systemAccountId: string
  scopes: string[]
  expiresAt: string
  revokedAt?: string
  createdAt: string
}

export interface OAuthAccessTokenContext {
  tokenId: string
  clientId: string
  grantId: string
  systemAccountId: string
  scopes: string[]
  issuedAt: string
  expiresAt: string
}

export interface OAuthSigningKey {
  id: string
  kid: string
  privateKeyCiphertext: string
  publicJwk: Record<string, unknown>
  status: 'active' | 'retired'
  createdAt: string
  retiredAt?: string
}

export interface OAuthDeviceAuthorization {
  id: string
  clientId: string
  userCode: string
  verificationUri: string
  scopes: string[]
  nonce?: string
  expiresAt: string
  intervalSeconds: number
  status: 'pending' | 'approved' | 'denied' | 'consumed' | 'expired'
  systemAccountId?: string
  lastPolledAt?: string
}

interface OAuthAuthorizationCodeRow {
  id: string
  client_id: string
  grant_id: string
  redirect_uri: string
  code_challenge: string
  nonce_ciphertext?: string
  expires_at: string
}

export interface OAuthAuthorizationTransaction {
  id: string
  clientId: string
  redirectUri: string
  scopes: string[]
  state: string
  codeChallenge: string
  csrfToken: string
  nonce?: string
  expiresAt: string
}

interface OAuthTokenRow extends OAuthAccessTokenContext {
  token_id: string
  client_id: string
  grant_id: string
  system_account_id: string
  scopes_json: string
  issued_at: string
  expires_at: string
}

export function createOAuthClient(input: {
  displayName: string
  clientType: OAuthClientType
  redirectUris: string[]
  allowedScopes: string[]
}): OAuthClient & { clientSecret?: string } {
  const database = getBusinessDatabase()
  const now = nowIso()
  const clientId = `juhe_${randomBytes(18).toString('base64url')}`
  const clientSecret = input.clientType === 'confidential'
    ? `jcs_${randomBytes(32).toString('base64url')}`
    : undefined
  const client: OAuthClient = {
    id: randomUUID(),
    clientId,
    displayName: input.displayName,
    clientType: input.clientType,
    clientSecretHash: clientSecret ? hashSecret(clientSecret) : undefined,
    redirectUris: input.redirectUris,
    allowedScopes: input.allowedScopes,
    status: 'active',
    createdAt: now,
    updatedAt: now
  }
  database.prepare(`
    INSERT INTO oauth_clients (
      id, client_id, display_name, client_type, client_secret_hash,
      redirect_uris_json, allowed_scopes_json, status, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    client.id,
    client.clientId,
    client.displayName,
    client.clientType,
    client.clientSecretHash ?? null,
    JSON.stringify(client.redirectUris),
    JSON.stringify(client.allowedScopes),
    client.status,
    client.createdAt,
    client.updatedAt
  )
  return { ...client, clientSecret }
}

export function listOAuthClients(): OAuthClient[] {
  const database = getBusinessDatabase()
  return (database.prepare(`
    SELECT id, client_id, display_name, client_type, client_secret_hash,
      redirect_uris_json, allowed_scopes_json, status, created_at, updated_at
    FROM oauth_clients
    ORDER BY created_at DESC, id DESC
  `).all() as unknown[]).map(oauthClientFromRow)
}

export function findOAuthClient(clientId: string): OAuthClient | undefined {
  const database = getBusinessDatabase()
  const row = database.prepare(`
    SELECT id, client_id, display_name, client_type, client_secret_hash,
      redirect_uris_json, allowed_scopes_json, status, created_at, updated_at
    FROM oauth_clients
    WHERE client_id = ?
  `).get(clientId)
  return row ? oauthClientFromRow(row) : undefined
}

export function authorizationCodeRequestsIdToken(input: {
  clientId: string
  code: string
}): boolean {
  const database = getBusinessDatabase()
  const row = database.prepare(`
    SELECT grants.scopes_json
    FROM oauth_authorization_codes codes
    INNER JOIN oauth_grants grants ON grants.id = codes.grant_id
    WHERE codes.code_hash = ?
      AND codes.client_id = ?
      AND codes.consumed_at IS NULL
  `).get(hashSecret(input.code), input.clientId) as Record<string, unknown> | undefined
  return Boolean(row && parseStringArray(row.scopes_json).includes('openid'))
}

export function deviceAuthorizationRequestsIdToken(input: {
  clientId: string
  deviceCode: string
}): boolean {
  const database = getBusinessDatabase()
  const row = database.prepare(`
    SELECT scopes_json
    FROM oauth_device_authorizations
    WHERE device_code_hash = ?
      AND client_id = ?
      AND status IN ('pending', 'approved')
  `).get(hashSecret(input.deviceCode), input.clientId) as Record<string, unknown> | undefined
  return Boolean(row && parseStringArray(row.scopes_json).includes('openid'))
}

export function updateOAuthClientStatus(
  clientId: string,
  status: OAuthClient['status']
): OAuthClient | undefined {
  const database = getBusinessDatabase()
  const updated = database.prepare(`
    UPDATE oauth_clients
    SET status = ?, updated_at = ?
    WHERE client_id = ?
  `).run(status, nowIso(), clientId)
  return updated.changes === 1 ? findOAuthClient(clientId) : undefined
}

export async function rotateOidcSigningKey(): Promise<OAuthSigningKey> {
  const database = getBusinessDatabase()
  const now = nowIso()
  const keyId = randomUUID()
  const kid = `oidc_${randomBytes(12).toString('base64url')}`
  const material: OidcSigningKeyMaterial = await createOidcSigningKeyMaterial(kid)
  database.exec('BEGIN IMMEDIATE')
  try {
    database.prepare(`
      UPDATE oauth_signing_keys
      SET status = 'retired', retired_at = ?
      WHERE status = 'active'
    `).run(now)
    database.prepare(`
      INSERT INTO oauth_signing_keys (
        id, kid, private_key_ciphertext, public_jwk_json, status, created_at, retired_at
      ) VALUES (?, ?, ?, ?, 'active', ?, NULL)
    `).run(keyId, kid, material.privateKeyCiphertext, JSON.stringify(material.publicJwk), now)
    database.exec('COMMIT')
  } catch (error) {
    rollback(database)
    throw error
  }
  return {
    id: keyId,
    kid,
    privateKeyCiphertext: material.privateKeyCiphertext,
    publicJwk: material.publicJwk,
    status: 'active',
    createdAt: now
  }
}

export function findActiveOidcSigningKey(): OAuthSigningKey | undefined {
  const database = getBusinessDatabase()
  const row = database.prepare(`
    SELECT id, kid, private_key_ciphertext, public_jwk_json, status, created_at, retired_at
    FROM oauth_signing_keys
    WHERE status = 'active'
    ORDER BY created_at DESC, id DESC
    LIMIT 1
  `).get() as Record<string, unknown> | undefined
  return row ? signingKeyFromRow(row) : undefined
}

export function listOidcSigningJwks(): Record<string, unknown>[] {
  const database = getBusinessDatabase()
  const retainedSince = new Date(Date.now() - grantLifetimeMs).toISOString()
  const rows = database.prepare(`
    SELECT public_jwk_json
    FROM oauth_signing_keys
    WHERE status = 'active' OR (status = 'retired' AND retired_at > ?)
    ORDER BY created_at DESC, id DESC
  `).all(retainedSince) as unknown[]
  return rows.flatMap((row) => {
    const value = row as Record<string, unknown>
    try {
      const parsed = JSON.parse(stringValue(value.public_jwk_json)) as Record<string, unknown>
      return parsed.kid && parsed.kty && parsed.n && parsed.e ? [parsed] : []
    } catch {
      return []
    }
  })
}

export function findSystemAccountProfile(systemAccountId: string): {
  id: string
  username: string
  displayName: string
  status: string
} | undefined {
  const database = getBusinessDatabase()
  const row = database.prepare(`
    SELECT id, username, display_name, status
    FROM system_accounts
    WHERE id = ? AND status = 'active'
  `).get(systemAccountId) as Record<string, unknown> | undefined
  if (!row) return undefined
  return {
    id: stringValue(row.id),
    username: stringValue(row.username),
    displayName: stringValue(row.display_name),
    status: stringValue(row.status)
  }
}

export function createAuthorizationCode(input: {
  clientId: string
  systemAccountId: string
  scopes: string[]
  redirectUri: string
  codeChallenge: string
  nonce?: string
}): { code: string; grant: OAuthGrant } {
  const database = getBusinessDatabase()
  const now = nowIso()
  const expiresAt = new Date(Date.now() + grantLifetimeMs).toISOString()
  const grant: OAuthGrant = {
    id: randomUUID(),
    clientId: input.clientId,
    systemAccountId: input.systemAccountId,
    scopes: input.scopes,
    expiresAt,
    createdAt: now
  }
  const code = randomBytes(32).toString('base64url')
  const codeId = randomUUID()
  database.exec('BEGIN IMMEDIATE')
  try {
    database.prepare(`
      INSERT INTO oauth_grants (id, client_id, system_account_id, scopes_json, expires_at, revoked_at, created_at)
      VALUES (?, ?, ?, ?, ?, NULL, ?)
    `).run(grant.id, grant.clientId, grant.systemAccountId, JSON.stringify(grant.scopes), grant.expiresAt, grant.createdAt)
    database.prepare(`
      INSERT INTO oauth_authorization_codes (
        id, code_hash, client_id, grant_id, redirect_uri, code_challenge, expires_at, consumed_at, created_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)
    `).run(
      codeId,
      hashSecret(code),
      input.clientId,
      grant.id,
      input.redirectUri,
      input.codeChallenge,
      new Date(Date.now() + authorizationCodeLifetimeMs).toISOString(),
      now
    )
    if (input.nonce) {
      database.prepare(`
        INSERT INTO oauth_authorization_code_oidc_contexts (code_id, nonce_ciphertext, created_at)
        VALUES (?, ?, ?)
      `).run(codeId, encryptOidcValue({ nonce: input.nonce }), now)
    }
    database.exec('COMMIT')
  } catch (error) {
    rollback(database)
    throw error
  }
  return { code, grant }
}

export function createAuthorizationTransaction(input: {
  clientId: string
  redirectUri: string
  scopes: string[]
  state: string
  codeChallenge: string
  nonce?: string
}): OAuthAuthorizationTransaction {
  const database = getBusinessDatabase()
  const now = nowIso()
  const transaction: OAuthAuthorizationTransaction = {
    id: randomUUID(),
    clientId: input.clientId,
    redirectUri: input.redirectUri,
    scopes: input.scopes,
    state: input.state,
    codeChallenge: input.codeChallenge,
    csrfToken: randomBytes(24).toString('base64url'),
    nonce: input.nonce,
    expiresAt: new Date(Date.now() + 10 * 60 * 1_000).toISOString()
  }
  database.prepare(`
    INSERT INTO oauth_authorization_transactions (
      id, client_id, redirect_uri, scopes_json, state_ciphertext,
      code_challenge, csrf_hash, expires_at, completed_at, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)
  `).run(
    transaction.id,
    transaction.clientId,
    transaction.redirectUri,
    JSON.stringify(transaction.scopes),
    encryptOidcValue({ state: transaction.state, csrfToken: transaction.csrfToken, nonce: input.nonce }),
    transaction.codeChallenge,
    hashSecret(transaction.csrfToken),
    transaction.expiresAt,
    now
  )
  return transaction
}

export function createDeviceAuthorization(input: {
  clientId: string
  scopes: string[]
  nonce?: string
  verificationUri: string
  expiresInSeconds?: number
  intervalSeconds?: number
}): { authorization: OAuthDeviceAuthorization; deviceCode: string } {
  const database = getBusinessDatabase()
  const now = nowIso()
  const deviceCode = randomBytes(32).toString('base64url')
  const userCode = generateUserCode()
  const authorization: OAuthDeviceAuthorization = {
    id: randomUUID(),
    clientId: input.clientId,
    userCode,
    verificationUri: input.verificationUri,
    scopes: input.scopes,
    nonce: input.nonce,
    expiresAt: new Date(Date.now() + (input.expiresInSeconds ?? 600) * 1_000).toISOString(),
    intervalSeconds: input.intervalSeconds ?? 5,
    status: 'pending',
    lastPolledAt: undefined
  }
  database.prepare(`
    INSERT INTO oauth_device_authorizations (
      id, client_id, device_code_hash, user_code, verification_uri, scopes_json,
      nonce_ciphertext, expires_at, interval_seconds, last_polled_at, csrf_hash, status,
      system_account_id, approved_at, denied_at, consumed_at, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, 'pending', NULL, NULL, NULL, NULL, ?)
  `).run(
    authorization.id,
    authorization.clientId,
    hashSecret(deviceCode),
    authorization.userCode,
    authorization.verificationUri,
    JSON.stringify(authorization.scopes),
    input.nonce ? encryptOidcValue({ nonce: input.nonce }) : null,
    authorization.expiresAt,
    authorization.intervalSeconds,
    now
  )
  return { authorization, deviceCode }
}

export function findDeviceAuthorizationByUserCode(userCode: string): OAuthDeviceAuthorization | undefined {
  const database = getBusinessDatabase()
  const row = database.prepare(`
    SELECT id, client_id, user_code, verification_uri, scopes_json, expires_at,
      interval_seconds, status, system_account_id, last_polled_at
    FROM oauth_device_authorizations
    WHERE user_code = ?
  `).get(userCode.trim().toUpperCase()) as Record<string, unknown> | undefined
  return row ? deviceAuthorizationFromRow(row) : undefined
}

export function prepareDeviceAuthorization(userCode: string): (OAuthDeviceAuthorization & { csrfToken: string }) | undefined {
  const database = getBusinessDatabase()
  const now = nowIso()
  const csrfToken = randomBytes(24).toString('base64url')
  database.exec('BEGIN IMMEDIATE')
  try {
    const row = database.prepare(`
      SELECT id, client_id, user_code, verification_uri, scopes_json, expires_at,
        interval_seconds, status, system_account_id, last_polled_at
      FROM oauth_device_authorizations
      WHERE user_code = ? AND status = 'pending' AND expires_at > ?
    `).get(userCode.trim().toUpperCase(), now) as Record<string, unknown> | undefined
    if (!row) {
      database.exec('ROLLBACK')
      return undefined
    }
    database.prepare(`
      UPDATE oauth_device_authorizations
      SET csrf_hash = ?
      WHERE id = ? AND status = 'pending' AND expires_at > ?
    `).run(hashSecret(csrfToken), stringValue(row.id), now)
    database.exec('COMMIT')
    return { ...deviceAuthorizationFromRow(row)!, csrfToken }
  } catch (error) {
    rollback(database)
    throw error
  }
}

export function decideDeviceAuthorization(input: {
  userCode: string
  csrfToken: string
  systemAccountId: string
  decision: 'allow' | 'deny'
}): OAuthDeviceAuthorization | undefined {
  const database = getBusinessDatabase()
  const now = nowIso()
  database.exec('BEGIN IMMEDIATE')
  try {
    const row = database.prepare(`
      SELECT id, client_id, user_code, verification_uri, scopes_json, expires_at,
        interval_seconds, status, system_account_id, last_polled_at
      FROM oauth_device_authorizations
      WHERE user_code = ? AND status = 'pending' AND expires_at > ?
        AND csrf_hash = ?
    `).get(input.userCode.trim().toUpperCase(), now, hashSecret(input.csrfToken)) as Record<string, unknown> | undefined
    if (!row) {
      database.exec('ROLLBACK')
      return undefined
    }
    const status = input.decision === 'allow' ? 'approved' : 'denied'
    const result = database.prepare(`
      UPDATE oauth_device_authorizations
      SET status = ?, system_account_id = ?, approved_at = ?, denied_at = ?
      WHERE id = ? AND status = 'pending' AND expires_at > ?
    `).run(
      status,
      input.decision === 'allow' ? input.systemAccountId : null,
      input.decision === 'allow' ? now : null,
      input.decision === 'deny' ? now : null,
      stringValue(row.id),
      now
    )
    if (result.changes !== 1) {
      database.exec('ROLLBACK')
      return undefined
    }
    database.exec('COMMIT')
    return { ...deviceAuthorizationFromRow(row)!, status, systemAccountId: input.decision === 'allow' ? input.systemAccountId : undefined }
  } catch (error) {
    rollback(database)
    throw error
  }
}

export type OAuthDevicePollResult =
  | { kind: 'invalid' }
  | { kind: 'expired' }
  | { kind: 'slow_down' }
  | { kind: 'authorization_pending' }
  | { kind: 'access_denied' }
  | { kind: 'invalid_grant' }
  | { kind: 'approved'; accessToken: string; context: OAuthAccessTokenContext; nonce?: string }

export function pollDeviceAuthorization(input: {
  clientId: string
  deviceCode: string
}): OAuthDevicePollResult {
  const database = getBusinessDatabase()
  const now = nowIso()
  database.exec('BEGIN IMMEDIATE')
  try {
    const row = database.prepare(`
      SELECT id, client_id, device_code_hash, scopes_json, nonce_ciphertext, expires_at,
        interval_seconds, last_polled_at, status, system_account_id
      FROM oauth_device_authorizations
      WHERE device_code_hash = ?
    `).get(hashSecret(input.deviceCode)) as Record<string, unknown> | undefined
    if (!row || stringValue(row.client_id) !== input.clientId) {
      database.exec('ROLLBACK')
      return { kind: 'invalid' }
    }
    const status = stringValue(row.status)
    if (status === 'consumed') {
      database.exec('ROLLBACK')
      return { kind: 'invalid_grant' }
    }
    if (Date.parse(stringValue(row.expires_at)) <= Date.now()) {
      database.prepare(`UPDATE oauth_device_authorizations SET status = 'expired' WHERE id = ? AND status IN ('pending', 'approved')`).run(stringValue(row.id))
      database.exec('COMMIT')
      return { kind: 'expired' }
    }
    if (status === 'denied') {
      database.exec('ROLLBACK')
      return { kind: 'access_denied' }
    }
    const lastPolledAt = optionalString(row.last_polled_at)
    const intervalSeconds = Math.max(1, Number(row.interval_seconds) || 5)
    if (lastPolledAt && Date.parse(lastPolledAt) + intervalSeconds * 1_000 > Date.now()) {
      database.prepare(`
        UPDATE oauth_device_authorizations
        SET interval_seconds = MIN(interval_seconds + 5, 60), last_polled_at = ?
        WHERE id = ? AND status IN ('pending', 'approved')
      `).run(now, stringValue(row.id))
      database.exec('COMMIT')
      return { kind: 'slow_down' }
    }
    database.prepare(`UPDATE oauth_device_authorizations SET last_polled_at = ? WHERE id = ?`).run(now, stringValue(row.id))
    if (status === 'pending') {
      database.exec('COMMIT')
      return { kind: 'authorization_pending' }
    }
    if (status !== 'approved' || !optionalString(row.system_account_id)) {
      database.exec('COMMIT')
      return { kind: 'invalid_grant' }
    }
    const account = database.prepare(`
      SELECT id FROM system_accounts WHERE id = ? AND status = 'active'
    `).get(stringValue(row.system_account_id))
    if (!account) {
      database.exec('COMMIT')
      return { kind: 'invalid_grant' }
    }
    // Decrypt before mutating the one-time device authorization. A damaged
    // encrypted nonce must not make an otherwise valid device code unretryable.
    const nonce = optionalString(row.nonce_ciphertext) ? nonceFromCiphertext(stringValue(row.nonce_ciphertext)) : undefined
    const grantId = randomUUID()
    const grantExpiresAt = new Date(Date.now() + grantLifetimeMs).toISOString()
    database.prepare(`
      INSERT INTO oauth_grants (id, client_id, system_account_id, scopes_json, expires_at, revoked_at, created_at)
      VALUES (?, ?, ?, ?, ?, NULL, ?)
    `).run(grantId, input.clientId, stringValue(row.system_account_id), stringValue(row.scopes_json), grantExpiresAt, now)
    const accessToken = randomBytes(32).toString('base64url')
    const context = insertAccessToken(database, {
      tokenId: randomUUID(),
      accessToken,
      clientId: input.clientId,
      grantId,
      systemAccountId: stringValue(row.system_account_id),
      scopes: parseStringArray(row.scopes_json),
      issuedAt: now,
      expiresAt: grantExpiresAt
    })
    const consumed = database.prepare(`
      UPDATE oauth_device_authorizations
      SET status = 'consumed', consumed_at = ?
      WHERE id = ? AND status = 'approved' AND consumed_at IS NULL
    `).run(now, stringValue(row.id))
    if (consumed.changes !== 1) {
      database.exec('ROLLBACK')
      return { kind: 'invalid_grant' }
    }
    database.exec('COMMIT')
    return { kind: 'approved', accessToken, context, nonce }
  } catch (error) {
    rollback(database)
    throw error
  }
}

export function findAuthorizationTransaction(id: string): OAuthAuthorizationTransaction | undefined {
  const database = getBusinessDatabase()
  const row = database.prepare(`
    SELECT id, client_id, redirect_uri, scopes_json, state_ciphertext,
      code_challenge, csrf_hash, expires_at
    FROM oauth_authorization_transactions
    WHERE id = ? AND completed_at IS NULL AND expires_at > ?
  `).get(id, nowIso()) as Record<string, unknown> | undefined
  return row ? transactionFromRow(row) : undefined
}

export function consumeAuthorizationTransaction(input: {
  id: string
  csrfToken: string
}): OAuthAuthorizationTransaction | undefined {
  const database = getBusinessDatabase()
  const now = nowIso()
  database.exec('BEGIN IMMEDIATE')
  try {
    const transaction = findAuthorizationTransaction(input.id)
    if (!transaction || hashSecret(input.csrfToken) !== hashSecret(transaction.csrfToken)) {
      database.exec('ROLLBACK')
      return undefined
    }
    const result = database.prepare(`
      UPDATE oauth_authorization_transactions
      SET completed_at = ?
      WHERE id = ? AND completed_at IS NULL AND expires_at > ?
    `).run(now, input.id, now)
    if (result.changes !== 1) {
      database.exec('ROLLBACK')
      return undefined
    }
    database.exec('COMMIT')
    return transaction
  } catch (error) {
    rollback(database)
    throw error
  }
}

export function exchangeAuthorizationCode(input: {
  clientId: string
  code: string
  redirectUri: string
  codeVerifier: string
}): { accessToken: string; context: OAuthAccessTokenContext; nonce?: string } | undefined {
  const database = getBusinessDatabase()
  const now = nowIso()
  database.exec('BEGIN IMMEDIATE')
  try {
    const code = database.prepare(`
      SELECT codes.id, codes.client_id, codes.grant_id, codes.redirect_uri, codes.code_challenge,
        contexts.nonce_ciphertext, codes.expires_at
      FROM oauth_authorization_codes codes
      LEFT JOIN oauth_authorization_code_oidc_contexts contexts ON contexts.code_id = codes.id
      INNER JOIN oauth_grants grants ON grants.id = codes.grant_id
      INNER JOIN oauth_clients clients ON clients.client_id = codes.client_id
      INNER JOIN system_accounts accounts ON accounts.id = grants.system_account_id
      WHERE codes.code_hash = ?
        AND codes.consumed_at IS NULL
        AND codes.expires_at > ?
        AND grants.revoked_at IS NULL
        AND grants.expires_at > ?
        AND clients.status = 'active'
        AND accounts.status = 'active'
    `).get(hashSecret(input.code), now, now) as OAuthAuthorizationCodeRow | undefined
    if (!code || code.client_id !== input.clientId || code.redirect_uri !== input.redirectUri || !verifyPkce(input.codeVerifier, code.code_challenge)) {
      database.exec('ROLLBACK')
      return undefined
    }
    // Decrypt before consuming the authorization code so storage/key failures
    // remain retryable after the operator repairs the affected OIDC record.
    const nonce = code.nonce_ciphertext ? nonceFromCiphertext(code.nonce_ciphertext) : undefined
    const consumed = database.prepare(`
      UPDATE oauth_authorization_codes
      SET consumed_at = ?
      WHERE id = ? AND consumed_at IS NULL
    `).run(now, code.id)
    if (consumed.changes !== 1) {
      database.exec('ROLLBACK')
      return undefined
    }
    const issued = issueAccessTokenInTransaction(database, code.grant_id, input.clientId, now)
    database.exec('COMMIT')
    return { ...issued, nonce }
  } catch (error) {
    rollback(database)
    throw error
  }
}

export function rotateAccessToken(input: {
  clientId: string
  currentAccessToken: string
}): { accessToken: string; context: OAuthAccessTokenContext } | 'not_eligible' | undefined {
  const database = getBusinessDatabase()
  const now = nowIso()
  database.exec('BEGIN IMMEDIATE')
  try {
    const token = database.prepare(`
      SELECT tokens.id AS token_id, tokens.client_id, tokens.grant_id, grants.system_account_id,
        grants.scopes_json, tokens.issued_at, tokens.expires_at
      FROM oauth_access_tokens tokens
      INNER JOIN oauth_grants grants ON grants.id = tokens.grant_id
      INNER JOIN oauth_clients clients ON clients.client_id = tokens.client_id
      INNER JOIN system_accounts accounts ON accounts.id = grants.system_account_id
      WHERE tokens.token_hash = ?
        AND tokens.revoked_at IS NULL
        AND tokens.replaced_at IS NULL
        AND tokens.expires_at > ?
        AND grants.revoked_at IS NULL
        AND grants.expires_at > ?
        AND clients.status = 'active'
        AND accounts.status = 'active'
    `).get(hashSecret(input.currentAccessToken), now, now) as OAuthTokenRow | undefined
    if (!token || token.client_id !== input.clientId) {
      database.exec('ROLLBACK')
      return undefined
    }
    if (Date.parse(token.issued_at) + tokenRenewalDelayMs > Date.now()) {
      database.exec('ROLLBACK')
      return 'not_eligible'
    }
    const tokenId = randomUUID()
    const accessToken = randomBytes(32).toString('base64url')
    const context = insertAccessToken(database, {
      tokenId,
      accessToken,
      clientId: token.client_id,
      grantId: token.grant_id,
      systemAccountId: token.system_account_id,
      scopes: parseStringArray(token.scopes_json),
      issuedAt: now,
      expiresAt: token.expires_at
    })
    const replaced = database.prepare(`
      UPDATE oauth_access_tokens
      SET replaced_at = ?, successor_token_id = ?
      WHERE id = ? AND replaced_at IS NULL AND revoked_at IS NULL
    `).run(now, tokenId, token.token_id)
    if (replaced.changes !== 1) {
      database.exec('ROLLBACK')
      return undefined
    }
    database.exec('COMMIT')
    return { accessToken, context }
  } catch (error) {
    rollback(database)
    throw error
  }
}

export function findAccessTokenContext(accessToken: string): OAuthAccessTokenContext | undefined {
  const database = getBusinessDatabase()
  const now = nowIso()
  const token = database.prepare(`
    SELECT tokens.id AS token_id, tokens.client_id, tokens.grant_id, grants.system_account_id,
      grants.scopes_json, tokens.issued_at, tokens.expires_at
    FROM oauth_access_tokens tokens
    INNER JOIN oauth_grants grants ON grants.id = tokens.grant_id
    INNER JOIN oauth_clients clients ON clients.client_id = tokens.client_id
    INNER JOIN system_accounts accounts ON accounts.id = grants.system_account_id
    WHERE tokens.token_hash = ?
      AND tokens.revoked_at IS NULL
      AND tokens.replaced_at IS NULL
      AND tokens.expires_at > ?
      AND grants.revoked_at IS NULL
      AND grants.expires_at > ?
      AND clients.status = 'active'
      AND accounts.status = 'active'
  `).get(hashSecret(accessToken), now, now) as OAuthTokenRow | undefined
  return token ? tokenContextFromRow(token) : undefined
}

export function revokeAccessToken(accessToken: string, clientId: string): boolean {
  const database = getBusinessDatabase()
  const result = database.prepare(`
    UPDATE oauth_access_tokens
    SET revoked_at = ?
    WHERE token_hash = ? AND client_id = ? AND revoked_at IS NULL
  `).run(nowIso(), hashSecret(accessToken), clientId)
  return result.changes === 1
}

export function revokeClientGrant(systemAccountId: string, clientId: string): boolean {
  const database = getBusinessDatabase()
  const now = nowIso()
  database.exec('BEGIN IMMEDIATE')
  try {
    const grants = database.prepare(`
      UPDATE oauth_grants SET revoked_at = ?
      WHERE system_account_id = ? AND client_id = ? AND revoked_at IS NULL
    `).run(now, systemAccountId, clientId)
    database.prepare(`
      UPDATE oauth_access_tokens SET revoked_at = ?
      WHERE grant_id IN (
        SELECT id FROM oauth_grants WHERE system_account_id = ? AND client_id = ?
      ) AND revoked_at IS NULL
    `).run(now, systemAccountId, clientId)
    database.exec('COMMIT')
    return grants.changes > 0
  } catch (error) {
    rollback(database)
    throw error
  }
}

export function listConnectedOAuthApplications(systemAccountId: string): Array<{
  clientId: string
  displayName: string
  scopes: string[]
  status: 'active' | 'revoked' | 'expired' | 'disabled'
  grantedAt: string
  expiresAt: string
  lastTokenRenewedAt?: string
}> {
  const database = getBusinessDatabase()
  const rows = database.prepare(`
    SELECT grants.client_id, grants.scopes_json, grants.expires_at, grants.revoked_at, grants.created_at,
      clients.display_name, clients.status AS client_status,
      MAX(tokens.issued_at) AS last_token_renewed_at
    FROM oauth_grants grants
    INNER JOIN oauth_clients clients ON clients.client_id = grants.client_id
    LEFT JOIN oauth_access_tokens tokens ON tokens.grant_id = grants.id
    WHERE grants.system_account_id = ?
    GROUP BY grants.id
    ORDER BY grants.created_at DESC, grants.id DESC
  `).all(systemAccountId) as Array<Record<string, unknown>>
  const applications = new Map<string, {
    clientId: string
    displayName: string
    scopes: string[]
    status: 'active' | 'revoked' | 'expired' | 'disabled'
    grantedAt: string
    expiresAt: string
    lastTokenRenewedAt?: string
  }>()
  const now = Date.now()
  for (const row of rows) {
    const clientId = stringValue(row.client_id)
    if (!clientId || applications.has(clientId)) continue
    const expiresAt = stringValue(row.expires_at)
    const status = stringValue(row.client_status) === 'disabled'
      ? 'disabled'
      : optionalString(row.revoked_at)
        ? 'revoked'
        : Date.parse(expiresAt) <= now
          ? 'expired'
          : 'active'
    applications.set(clientId, {
      clientId,
      displayName: stringValue(row.display_name),
      scopes: parseStringArray(row.scopes_json),
      status,
      grantedAt: stringValue(row.created_at),
      expiresAt,
      lastTokenRenewedAt: optionalString(row.last_token_renewed_at)
    })
  }
  return [...applications.values()]
}

function issueAccessTokenInTransaction(
  database: ReturnType<typeof getBusinessDatabase>,
  grantId: string,
  clientId: string,
  issuedAt: string
): { accessToken: string; context: OAuthAccessTokenContext } {
  const grant = database.prepare(`
    SELECT id, client_id, system_account_id, scopes_json, expires_at
    FROM oauth_grants WHERE id = ?
  `).get(grantId) as { id: string; client_id: string; system_account_id: string; scopes_json: string; expires_at: string } | undefined
  if (!grant || grant.client_id !== clientId || Date.parse(grant.expires_at) <= Date.now()) {
    throw new Error('OAuth grant 已失效')
  }
  const accessToken = randomBytes(32).toString('base64url')
  const context = insertAccessToken(database, {
    tokenId: randomUUID(),
    accessToken,
    clientId,
    grantId: grant.id,
    systemAccountId: grant.system_account_id,
    scopes: parseStringArray(grant.scopes_json),
    issuedAt,
    expiresAt: grant.expires_at
  })
  return { accessToken, context }
}

function insertAccessToken(
  database: ReturnType<typeof getBusinessDatabase>,
  input: {
    tokenId: string
    accessToken: string
    clientId: string
    grantId: string
    systemAccountId: string
    scopes: string[]
    issuedAt: string
    expiresAt: string
  }
): OAuthAccessTokenContext {
  database.prepare(`
    INSERT INTO oauth_access_tokens (
      id, token_hash, client_id, grant_id, issued_at, expires_at,
      revoked_at, replaced_at, successor_token_id, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?)
  `).run(input.tokenId, hashSecret(input.accessToken), input.clientId, input.grantId, input.issuedAt, input.expiresAt, input.issuedAt)
  return {
    tokenId: input.tokenId,
    clientId: input.clientId,
    grantId: input.grantId,
    systemAccountId: input.systemAccountId,
    scopes: input.scopes,
    issuedAt: input.issuedAt,
    expiresAt: input.expiresAt
  }
}

function oauthClientFromRow(row: unknown): OAuthClient {
  const value = row as Record<string, unknown>
  return {
    id: stringValue(value.id),
    clientId: stringValue(value.client_id),
    displayName: stringValue(value.display_name),
    clientType: value.client_type === 'confidential' ? 'confidential' : 'public',
    clientSecretHash: optionalString(value.client_secret_hash),
    redirectUris: parseStringArray(value.redirect_uris_json),
    allowedScopes: parseStringArray(value.allowed_scopes_json),
    status: value.status === 'disabled' ? 'disabled' : 'active',
    createdAt: stringValue(value.created_at),
    updatedAt: stringValue(value.updated_at)
  }
}

function signingKeyFromRow(row: Record<string, unknown>): OAuthSigningKey {
  let publicJwk: Record<string, unknown>
  try {
    publicJwk = JSON.parse(stringValue(row.public_jwk_json)) as Record<string, unknown>
  } catch {
    throw new Error('OIDC 签名公钥内容无效')
  }
  if (!publicJwk.kty || !publicJwk.n || !publicJwk.e || !publicJwk.kid) {
    throw new Error('OIDC 签名公钥字段不完整')
  }
  return {
    id: stringValue(row.id),
    kid: stringValue(row.kid),
    privateKeyCiphertext: stringValue(row.private_key_ciphertext),
    publicJwk,
    status: row.status === 'retired' ? 'retired' : 'active',
    createdAt: stringValue(row.created_at),
    retiredAt: optionalString(row.retired_at)
  }
}

function deviceAuthorizationFromRow(row: Record<string, unknown>): OAuthDeviceAuthorization | undefined {
  const status = stringValue(row.status)
  if (!['pending', 'approved', 'denied', 'consumed', 'expired'].includes(status)) return undefined
  return {
    id: stringValue(row.id),
    clientId: stringValue(row.client_id),
    userCode: stringValue(row.user_code),
    verificationUri: stringValue(row.verification_uri),
    scopes: parseStringArray(row.scopes_json),
    expiresAt: stringValue(row.expires_at),
    intervalSeconds: Math.max(1, Number(row.interval_seconds) || 5),
    status: status as OAuthDeviceAuthorization['status'],
    systemAccountId: optionalString(row.system_account_id),
    lastPolledAt: optionalString(row.last_polled_at)
  }
}

function generateUserCode(): string {
  const alphabet = 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789'
  const bytes = randomBytes(8)
  let value = ''
  for (let index = 0; index < 8; index += 1) value += alphabet[bytes[index]! % alphabet.length]
  return value
}

function transactionFromRow(row: Record<string, unknown>): OAuthAuthorizationTransaction {
  const encrypted = stringValue(row.state_ciphertext)
  const payload = decryptOidcValue<{ state?: unknown; csrfToken?: unknown; nonce?: unknown }>(encrypted)
  if (typeof payload.state !== 'string' || typeof payload.csrfToken !== 'string') {
    throw new Error('OIDC 授权事务内容无效')
  }
  return {
    id: stringValue(row.id),
    clientId: stringValue(row.client_id),
    redirectUri: stringValue(row.redirect_uri),
    scopes: parseStringArray(row.scopes_json),
    state: payload.state,
    codeChallenge: stringValue(row.code_challenge),
    csrfToken: payload.csrfToken,
    nonce: typeof payload.nonce === 'string' ? payload.nonce : undefined,
    expiresAt: stringValue(row.expires_at)
  }
}

function nonceFromCiphertext(value: string): string {
  const payload = decryptOidcValue<{ nonce?: unknown }>(value)
  if (typeof payload.nonce !== 'string' || !payload.nonce) {
    throw new Error('OIDC nonce 密文内容无效')
  }
  return payload.nonce
}

function tokenContextFromRow(row: OAuthTokenRow): OAuthAccessTokenContext {
  return {
    tokenId: row.token_id,
    clientId: row.client_id,
    grantId: row.grant_id,
    systemAccountId: row.system_account_id,
    scopes: parseStringArray(row.scopes_json),
    issuedAt: row.issued_at,
    expiresAt: row.expires_at
  }
}

function verifyPkce(verifier: string, expectedChallenge: string): boolean {
  if (!/^[A-Za-z0-9\-._~]{43,128}$/.test(verifier)) return false
  return hashSecret(verifier).length > 0 && sha256Base64Url(verifier) === expectedChallenge
}

function sha256Base64Url(value: string): string {
  return createHash('sha256').update(value).digest('base64url')
}

function parseStringArray(value: unknown): string[] {
  if (typeof value !== 'string') return []
  try {
    const parsed = JSON.parse(value)
    return Array.isArray(parsed) ? parsed.filter((item): item is string => typeof item === 'string') : []
  } catch {
    return []
  }
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value ? value : undefined
}

function nowIso(): string {
  return new Date().toISOString()
}

function rollback(database: ReturnType<typeof getBusinessDatabase>): void {
  try {
    database.exec('ROLLBACK')
  } catch {
    // The transaction may already have ended.
  }
}
