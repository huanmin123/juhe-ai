import type { CookieOptions, NextFunction, Request, Response } from 'express'

import { runtimeConfig, type RuntimeConfig } from '../config/runtime.js'

type HttpSecurityConfig = RuntimeConfig['httpSecurity']

interface SecurityHeaderWriter {
  setHeader(name: string, value: string): unknown
}

const managementHeaders = Object.freeze({
  'content-security-policy': "default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; font-src 'self' data: https:; connect-src 'self' https: wss:; worker-src 'self' blob:; media-src 'self' data: blob: https:; manifest-src 'self'",
  'x-frame-options': 'DENY',
  'x-content-type-options': 'nosniff',
  'referrer-policy': 'strict-origin-when-cross-origin'
})

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

export function managementSecurityHeaders(): Readonly<Record<string, string>> {
  return managementHeaders
}

export function setManagementSecurityHeaders(response: SecurityHeaderWriter): void {
  for (const [name, value] of Object.entries(managementHeaders)) {
    response.setHeader(name, value)
  }
}

export function managementSecurityHeadersMiddleware(_req: Request, res: Response, next: NextFunction): void {
  setManagementSecurityHeaders(res)
  next()
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
