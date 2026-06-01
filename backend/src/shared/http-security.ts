import type { CookieOptions } from 'express'

import { runtimeConfig, type RuntimeConfig } from '../config/runtime.js'

type HttpSecurityConfig = RuntimeConfig['httpSecurity']

export function isCorsOriginAllowed(origin: string | undefined, config: HttpSecurityConfig = runtimeConfig.httpSecurity): boolean {
  if (!origin) return true
  if (config.cors.allowAnyOrigin) return true
  return config.cors.allowedOrigins.includes(origin)
}

export function createCorsOriginDelegate(config: HttpSecurityConfig = runtimeConfig.httpSecurity) {
  return (origin: string | undefined, callback: (error: Error | null, allow?: boolean) => void): void => {
    callback(null, isCorsOriginAllowed(origin, config))
  }
}

export function sessionCookieOptions(
  input: { maxAge: number },
  config: HttpSecurityConfig = runtimeConfig.httpSecurity
): CookieOptions {
  return {
    httpOnly: true,
    sameSite: config.cookie.sameSite,
    secure: config.cookie.secure,
    path: '/',
    maxAge: input.maxAge
  }
}
