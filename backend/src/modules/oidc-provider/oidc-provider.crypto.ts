import { createCipheriv, createDecipheriv, createHash, createHmac, randomBytes } from 'node:crypto'

import { SignJWT, exportJWK, exportPKCS8, generateKeyPair, importPKCS8 } from 'jose'

import { runtimeConfig } from '../../config/runtime.js'

function encryptionKey(): Buffer {
  const secret = runtimeConfig.oidc.keyEncryptionSecret
  if (!secret) throw new Error('OIDC 事务加密密钥未配置')
  return createHash('sha256').update(secret).digest()
}

export function encryptOidcValue(value: unknown): string {
  const iv = randomBytes(12)
  const cipher = createCipheriv('aes-256-gcm', encryptionKey(), iv)
  const encrypted = Buffer.concat([cipher.update(JSON.stringify(value), 'utf8'), cipher.final()])
  return [iv.toString('base64url'), cipher.getAuthTag().toString('base64url'), encrypted.toString('base64url')].join('.')
}

export function decryptOidcValue<T>(value: string): T {
  const [ivText, tagText, encryptedText] = value.split('.')
  if (!ivText || !tagText || !encryptedText) throw new Error('OIDC 事务密文格式无效')
  const decipher = createDecipheriv('aes-256-gcm', encryptionKey(), Buffer.from(ivText, 'base64url'))
  decipher.setAuthTag(Buffer.from(tagText, 'base64url'))
  const plainText = Buffer.concat([decipher.update(Buffer.from(encryptedText, 'base64url')), decipher.final()])
  return JSON.parse(plainText.toString('utf8')) as T
}

export interface OidcSigningKeyMaterial {
  privateKeyCiphertext: string
  publicJwk: Record<string, unknown>
}

export async function createOidcSigningKeyMaterial(kid: string): Promise<OidcSigningKeyMaterial> {
  const { privateKey, publicKey } = await generateKeyPair('RS256', { modulusLength: 2048, extractable: true })
  const publicJwk = await exportJWK(publicKey)
  if (!publicJwk.kty || !publicJwk.n || !publicJwk.e) throw new Error('OIDC RSA 公钥导出失败')
  return {
    privateKeyCiphertext: encryptOidcValue({ privateKeyPem: await exportPKCS8(privateKey) }),
    publicJwk: {
      ...publicJwk,
      kid,
      use: 'sig',
      alg: 'RS256'
    }
  }
}

export async function signOidcIdToken(input: {
  privateKeyCiphertext: string
  kid: string
  issuer: string
  audience: string
  subject: string
  expiresAt: string
  nonce?: string
}): Promise<string> {
  const payload = decryptOidcValue<{ privateKeyPem?: unknown }>(input.privateKeyCiphertext)
  if (typeof payload.privateKeyPem !== 'string' || !payload.privateKeyPem) {
    throw new Error('OIDC 签名私钥内容无效')
  }
  const privateKey = await importPKCS8(payload.privateKeyPem, 'RS256')
  const token = new SignJWT(input.nonce ? { nonce: input.nonce } : {})
    .setProtectedHeader({ alg: 'RS256', kid: input.kid, typ: 'JWT' })
    .setIssuer(input.issuer)
    .setAudience(input.audience)
    .setSubject(input.subject)
    .setIssuedAt()
    .setExpirationTime(Math.floor(Date.parse(input.expiresAt) / 1_000))
  return token.sign(privateKey)
}

/**
 * Token endpoint calls this before consuming a one-time code or device grant.
 * It performs a real RS256 signature so a broken encrypted key fails while the
 * caller can still retry after an operator repairs or rotates the key.
 */
export async function assertOidcSigningKeyUsable(input: {
  privateKeyCiphertext: string
  kid: string
  issuer: string
}): Promise<void> {
  await signOidcIdToken({
    privateKeyCiphertext: input.privateKeyCiphertext,
    kid: input.kid,
    issuer: input.issuer,
    audience: 'juhe-ai-oidc-preflight',
    subject: 'juhe-ai-oidc-preflight',
    expiresAt: new Date(Date.now() + 60_000).toISOString()
  })
}

export function oidcSubjectForSystemAccount(systemAccountId: string): string {
  const secret = runtimeConfig.oidc.keyEncryptionSecret
  const issuer = runtimeConfig.oidc.issuer
  if (!secret || !issuer) throw new Error('OIDC issuer 或 subject 派生密钥未配置')
  return createHmac('sha256', secret)
    .update('juhe-ai:oidc-subject:v1\u0000')
    .update(issuer)
    .update('\u0000')
    .update(systemAccountId)
    .digest('base64url')
}
