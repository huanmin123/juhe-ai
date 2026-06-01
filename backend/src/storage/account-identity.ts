import { hashSecret } from './crypto.js'

const accountIdentityFingerprintVersion = 'account-identity-v1'

export interface AccountIdentityFingerprintInput {
  providerCode: string
  type: string
  baseUrl: string
  secret: string
}

export function accountCredentialFingerprint(secret: string): string {
  return hashSecret(secret.trim())
}

export function accountIdentityFingerprint(input: AccountIdentityFingerprintInput): string {
  return hashSecret(JSON.stringify([
    accountIdentityFingerprintVersion,
    normalizedIdentityPart(input.providerCode),
    normalizedIdentityPart(input.type),
    normalizeAccountBaseUrlHost(input.baseUrl),
    input.secret.trim()
  ]))
}

export function normalizeAccountBaseUrlHost(baseUrl: string): string {
  const text = baseUrl.trim()
  if (!text) {
    throw new Error('Base URL 不能为空')
  }
  const candidate = hasUrlScheme(text) ? text : `https://${text}`
  let url: URL
  try {
    url = new URL(candidate)
  } catch {
    throw new Error('Base URL 必须包含有效域名')
  }
  const host = url.host.trim().toLowerCase()
  if (!host) {
    throw new Error('Base URL 必须包含有效域名')
  }
  return host
}

function normalizedIdentityPart(value: string): string {
  return value.trim().toLowerCase()
}

function hasUrlScheme(value: string): boolean {
  return /^[a-z][a-z\d+.-]*:\/\//i.test(value)
}
