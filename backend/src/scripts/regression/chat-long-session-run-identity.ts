import { createHash, randomBytes } from 'node:crypto'
import { closeSync, openSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

export const chatLongSessionRunIdentityFileName = '.chat-long-session-run-id'

export function resolveChatLongSessionRunSecret(
  tempRoot: string,
  options: { resuming: boolean }
): string {
  const identityPath = join(tempRoot, chatLongSessionRunIdentityFileName)
  let identity: string
  try {
    identity = readIdentity(identityPath)
  } catch (error) {
    if (!isMissingFileError(error)) throw error
    if (options.resuming) return `chat-long-${hash(tempRoot)}`
    identity = createIdentity(identityPath)
  }
  return `chat-long-${hash(`chat-long-session-run:${identity}`)}`
}

function createIdentity(path: string): string {
  const identity = randomBytes(32).toString('hex')
  let descriptor: number | undefined
  try {
    descriptor = openSync(path, 'wx', 0o600)
    writeFileSync(descriptor, `${identity}\n`, { encoding: 'utf8' })
  } catch (error) {
    if (!isAlreadyExistsError(error)) throw error
    return readIdentity(path)
  } finally {
    if (descriptor !== undefined) closeSync(descriptor)
  }
  return identity
}

function readIdentity(path: string): string {
  const identity = readFileSync(path, { encoding: 'utf8', flag: 'r' }).trim()
  if (!/^[a-f0-9]{64}$/.test(identity)) throw new Error('chat_long_session_run_identity_invalid')
  return identity
}

function hash(value: string): string {
  return createHash('sha256').update(value).digest('hex')
}

function isMissingFileError(error: unknown): boolean {
  return error instanceof Error && 'code' in error && error.code === 'ENOENT'
}

function isAlreadyExistsError(error: unknown): boolean {
  return error instanceof Error && 'code' in error && error.code === 'EEXIST'
}
