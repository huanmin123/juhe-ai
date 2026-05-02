import { createCipheriv, createDecipheriv, createHash, randomBytes } from 'node:crypto'

import { runtimeConfig } from '../config/runtime.js'

const secret = runtimeConfig.secret
const key = createHash('sha256').update(secret).digest()

export function encryptJson(value: unknown): string {
  const iv = randomBytes(12)
  const cipher = createCipheriv('aes-256-gcm', key, iv)
  const plainText = Buffer.from(JSON.stringify(value ?? {}), 'utf8')
  const encrypted = Buffer.concat([cipher.update(plainText), cipher.final()])
  const tag = cipher.getAuthTag()
  return ['v1', iv.toString('base64url'), tag.toString('base64url'), encrypted.toString('base64url')].join(':')
}

export function decryptJson<T = unknown>(value: string): T {
  const [version, ivText, tagText, encryptedText] = value.split(':')
  if (version !== 'v1' || !ivText || !tagText || !encryptedText) {
    throw new Error('Unsupported encrypted payload format')
  }
  const decipher = createDecipheriv('aes-256-gcm', key, Buffer.from(ivText, 'base64url'))
  decipher.setAuthTag(Buffer.from(tagText, 'base64url'))
  const decrypted = Buffer.concat([decipher.update(Buffer.from(encryptedText, 'base64url')), decipher.final()])
  return JSON.parse(decrypted.toString('utf8')) as T
}

export function maskSecret(value: unknown): string {
  if (typeof value !== 'string' || value.length === 0) {
    return ''
  }
  if (value.length <= 10) {
    return `${value.slice(0, 2)}***${value.slice(-2)}`
  }
  return `${value.slice(0, 6)}***${value.slice(-4)}`
}

export function hashSecret(value: string): string {
  return createHash('sha256').update(value).digest('hex')
}

export function createApiKey(): string {
  return randomBytes(32).toString('hex')
}