import { createCipheriv, createDecipheriv, createHash, pbkdf2Sync, randomBytes, timingSafeEqual } from 'node:crypto'

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
  return `sk-${randomBytes(32).toString('hex')}`
}

const passwordIterations = 120_000

export function hashPassword(value: string): string {
  const salt = randomBytes(16).toString('base64url')
  const derived = pbkdf2Sync(value, salt, passwordIterations, 32, 'sha512')
  return ['pbkdf2', 'sha512', String(passwordIterations), salt, derived.toString('base64url')].join('$')
}

export function verifyPassword(value: string, passwordHash: string): boolean {
  const parts = passwordHash.split('$')
  if (parts.length !== 5 || parts[0] !== 'pbkdf2' || parts[1] !== 'sha512') {
    return false
  }

  const iterations = Number(parts[2])
  if (!Number.isFinite(iterations) || iterations < 1) {
    return false
  }

  const salt = parts[3]
  const expected = Buffer.from(parts[4], 'base64url')
  const actual = pbkdf2Sync(value, salt, Math.trunc(iterations), expected.length, 'sha512')
  return expected.length === actual.length && timingSafeEqual(expected, actual)
}
