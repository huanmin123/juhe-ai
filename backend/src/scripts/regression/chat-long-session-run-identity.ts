import { createHash, createHmac, randomBytes } from 'node:crypto'
import { linkSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

export const chatLongSessionRunIdentityFileName = '.chat-long-session-run-id'

export function resolveChatLongSessionRunSecret(
  tempRoot: string,
  options: { resuming: boolean; keyMaterial?: string; allowLegacyPathDerivation?: boolean }
): string {
  const identityPath = join(tempRoot, chatLongSessionRunIdentityFileName)
  let identity: string
  try {
    identity = readIdentity(identityPath)
  } catch (error) {
    if (!isMissingFileError(error)) throw error
    if (options.resuming) {
      if (options.allowLegacyPathDerivation) return `chat-long-${hash(tempRoot)}`
      throw new Error('chat_long_session_run_identity_missing')
    }
    identity = createIdentity(identityPath)
  }
  if (identity.startsWith('v2:')) {
    if (!options.keyMaterial) throw new Error('chat_long_session_run_key_material_required')
    return `chat-long-${createHmac('sha256', options.keyMaterial).update(`chat-long-session-run:${identity}`).digest('hex')}`
  }
  // Existing retained acceptance runs used the v1 opaque identity derivation.
  if (!options.allowLegacyPathDerivation) throw new Error('chat_long_session_legacy_identity_not_allowed')
  return `chat-long-${hash(`chat-long-session-run:${identity}`)}`
}

function createIdentity(path: string): string {
  const identity = `v2:${randomBytes(32).toString('hex')}`
  const temporaryPath = `${path}.${process.pid}.${randomBytes(6).toString('hex')}.tmp`
  try {
    writeFileSync(temporaryPath, `${identity}\n`, { encoding: 'utf8', flag: 'wx', mode: 0o600, flush: true })
    linkSync(temporaryPath, path)
  } catch (error) {
    if (!isAlreadyExistsError(error)) throw error
    return readIdentity(path)
  } finally {
    rmSync(temporaryPath, { force: true })
  }
  return identity
}

function readIdentity(path: string): string {
  const identity = readFileSync(path, { encoding: 'utf8', flag: 'r' }).trim()
  if (!/^(?:v2:)?[a-f0-9]{64}$/.test(identity)) throw new Error('chat_long_session_run_identity_invalid')
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
