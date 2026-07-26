export const PROFILE_PATH = '/profile'

export function requiredPasswordProfileLocation(redirectPath?: string) {
  const redirect = normalizeProfileRedirectPath(redirectPath)
  return {
    path: PROFILE_PATH,
    query: {
      section: 'security',
      required: '1',
      ...(redirect ? { redirect } : {})
    }
  }
}

export function normalizeProfileRedirectPath(value: unknown): string | undefined {
  const rawValue = Array.isArray(value) ? value[0] : value
  if (typeof rawValue !== 'string') return undefined
  const path = rawValue.trim()
  if (!path || !path.startsWith('/') || path.startsWith('//') || path.includes('\\')) return undefined
  const pathname = path.split(/[?#]/, 1)[0]
  if (pathname === PROFILE_PATH || pathname === '/login' || pathname === '/service-recovering') return undefined
  return path
}
