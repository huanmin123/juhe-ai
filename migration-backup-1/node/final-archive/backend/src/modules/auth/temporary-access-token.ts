export const temporaryAccessTokenPrefix = 'juhe_tmp_'

const temporaryAccessTokenPattern = new RegExp(`^${temporaryAccessTokenPrefix}[A-Za-z0-9_-]{43}$`)

export type SystemAccessToken = {
  token: string
  kind: 'cookie' | 'temporary'
}

export type SystemAccessTokenResolution =
  | { kind: 'none' }
  | { kind: 'invalid' }
  | { kind: 'token'; access: SystemAccessToken }

export function resolveSystemAccessToken(
  authorizationHeader: string | string[] | undefined,
  cookieToken: string | undefined
): SystemAccessTokenResolution {
  if (authorizationHeader !== undefined) {
    if (Array.isArray(authorizationHeader) || typeof authorizationHeader !== 'string') {
      return { kind: 'invalid' }
    }
    const match = /^Bearer\s+(.+)$/i.exec(authorizationHeader.trim())
    if (!match || !temporaryAccessTokenPattern.test(match[1])) {
      return { kind: 'invalid' }
    }
    return { kind: 'token', access: { token: match[1], kind: 'temporary' } }
  }

  if (!cookieToken) return { kind: 'none' }
  return { kind: 'token', access: { token: cookieToken, kind: 'cookie' } }
}

export function isTemporaryAccessToken(token: string): boolean {
  return temporaryAccessTokenPattern.test(token)
}
