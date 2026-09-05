import { hashSecret } from './crypto.js'

export function accountCredentialFingerprint(secret: string): string {
  return hashSecret(secret.trim())
}
