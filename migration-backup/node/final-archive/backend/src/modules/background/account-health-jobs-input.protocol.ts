import { createHash, createHmac, randomUUID } from 'node:crypto'
import { closeSync, fsyncSync, mkdirSync, openSync, readFileSync, renameSync, unlinkSync, writeSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'

export const accountHealthInputSignatureAlgorithm = 'hmac-sha256-v1'
export const accountHealthInputSignatureKeyId = 'runtime-v1'
export const accountHealthInputFileSuffix = '.account-health-input.json'
export const accountHealthRequestFileSuffix = '.account-health-request.json'

export interface AccountHealthJobsSignedInput {
  algorithm: typeof accountHealthInputSignatureAlgorithm
  key_id: string
  payload: string
  signature: string
}

// The payload is encoded before signing so the consumer verifies exact bytes,
// rather than depending on JavaScript/Go object-key serialization behavior.
export function signAccountHealthJobsInput(
  payload: Record<string, unknown>,
  signingKey: string,
  keyId = accountHealthInputSignatureKeyId
): Uint8Array {
  const normalizedKey = signingKey.trim()
  if (!normalizedKey) throw new Error('account-health input 签名密钥缺失')
  if (!keyId.trim()) throw new Error('account-health input 签名 key ID 缺失')
  const key = Buffer.from(normalizedKey, 'base64url')
  if (key.length < 32 || key.toString('base64url') !== normalizedKey.replace(/=+$/u, '')) {
    throw new Error('account-health input 签名密钥必须是至少 32 字节的 canonical base64url')
  }
  const payloadBytes = Buffer.from(JSON.stringify(payload), 'utf8')
  const signature = createHmac('sha256', key)
    .update(`${accountHealthInputSignatureAlgorithm}\n${keyId}\n`, 'utf8')
    .update(payloadBytes)
    .digest('base64url')
  return Buffer.from(JSON.stringify({
    algorithm: accountHealthInputSignatureAlgorithm,
    key_id: keyId,
    payload: payloadBytes.toString('base64url'),
    signature
  } satisfies AccountHealthJobsSignedInput), 'utf8')
}

export function accountHealthJobsInputPath(root: string, accountId: string): string {
  const normalizedRoot = root.trim()
  const normalizedAccountId = accountId.trim()
  if (!normalizedRoot) throw new Error('account-health input 根目录缺失')
  if (!normalizedAccountId) throw new Error('account-health input account ID 缺失')
  // The account ID stays inside the signed payload; the filename is a bounded
  // opaque locator and never lets configuration input escape the root.
  const locator = createHash('sha256').update(normalizedAccountId).digest('hex')
  return join(resolve(normalizedRoot), `${locator}${accountHealthInputFileSuffix}`)
}

export function accountHealthJobsRequestPath(root: string, requestId: string): string {
  const normalizedRoot = root.trim()
  const normalizedRequestId = requestId.trim()
  if (!normalizedRoot) throw new Error('account-health request 根目录缺失')
  if (!normalizedRequestId) throw new Error('account-health request ID 缺失')
  const locator = createHash('sha256').update(normalizedRequestId).digest('hex')
  return join(resolve(normalizedRoot), `${locator}${accountHealthRequestFileSuffix}`)
}

export function publishAccountHealthJobsInput(input: {
  root: string
  accountId: string
  payload: Record<string, unknown>
  signingKey: string
  keyId?: string
}): string {
  const target = accountHealthJobsInputPath(input.root, input.accountId)
  const bytes = signAccountHealthJobsInput(input.payload, input.signingKey, input.keyId)
  return publishSignedAccountHealthJobsFile(target, bytes)
}

export function publishAccountHealthJobsRequest(input: {
  root: string
  requestId: string
  payload: Record<string, unknown>
  signingKey: string
  keyId?: string
}): string {
  const target = accountHealthJobsRequestPath(input.root, input.requestId)
  const bytes = signAccountHealthJobsInput(input.payload, input.signingKey, input.keyId)
  return publishSignedAccountHealthJobsFile(target, bytes)
}

function publishSignedAccountHealthJobsFile(target: string, bytes: Uint8Array): string {
  mkdirSync(dirname(target), { recursive: true })
  const temporary = `${target}.${randomUUID()}.tmp`
  let descriptor: number | undefined
  let published = false
  try {
    // On Windows fsync requires a writable handle.  Keep the same descriptor
    // for write + sync; reopening it read-only causes EPERM and would weaken
    // the publication contract if treated as optional.
    descriptor = openSync(temporary, 'wx', 0o600)
    for (let offset = 0; offset < bytes.length;) {
      const written = writeSync(descriptor, bytes, offset, bytes.length - offset)
      if (written <= 0) throw new Error('account-health input 临时文件写入未取得进度')
      offset += written
    }
    fsyncSync(descriptor)
    closeSync(descriptor)
    descriptor = undefined
    renameSync(temporary, target)
    published = true
    return target
  } finally {
    if (descriptor !== undefined) closeSync(descriptor)
    if (!published) {
      try {
        unlinkSync(temporary)
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error
      }
    }
  }
}

// Test-only/readback helper. Runtime consumers are Go jobs; Node must not use
// this as a source of task state or a fallback execution path.
export function readPublishedAccountHealthJobsInput(path: string): AccountHealthJobsSignedInput {
  const parsed: unknown = JSON.parse(readFileSync(path, 'utf8'))
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('account-health input 文件格式无效')
  }
  const record = parsed as Record<string, unknown>
  if (
    record.algorithm !== accountHealthInputSignatureAlgorithm
    || typeof record.key_id !== 'string'
    || typeof record.payload !== 'string'
    || typeof record.signature !== 'string'
  ) throw new Error('account-health input 文件字段无效')
  return record as unknown as AccountHealthJobsSignedInput
}
