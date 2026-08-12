#!/usr/bin/env node

import { pathToFileURL } from 'node:url'

export const FRONTEND_API_BASE_ROOT = '/__aisys__/api'
export const MAX_FRONTEND_API_BASE_LENGTH = 2048
export const MAX_FRONTEND_API_BASE_PATH_LENGTH = 1024

export class FrontendApiBaseValidationError extends Error {
  constructor(message, code = 'invalid') {
    super(message)
    this.name = 'FrontendApiBaseValidationError'
    this.code = code
  }
}

export function validateFrontendApiBase(value) {
  if (typeof value !== 'string' || value.length === 0) {
    throw new FrontendApiBaseValidationError('frontend API base URL must not be empty')
  }

  if (value === FRONTEND_API_BASE_ROOT) {
    return value
  }

  // Reject delimiters and ambiguous authority syntax before URL can normalize them.
  if (value.includes('?') || value.includes('#')) {
    throw new FrontendApiBaseValidationError(
      'frontend API base URL must not contain a query or fragment'
    )
  }

  if (/^[A-Za-z]:[\\/]/u.test(value)) {
    throw new FrontendApiBaseValidationError(
      'frontend API base URL must not be a Windows drive path',
      'windows-drive'
    )
  }
  if (value.startsWith('\\\\')) {
    throw new FrontendApiBaseValidationError(
      'frontend API base URL must not be a UNC path',
      'unc'
    )
  }
  if (value.startsWith('//')) {
    throw new FrontendApiBaseValidationError(
      'frontend API base URL must not be protocol-relative',
      'protocol-relative'
    )
  }
  if (value.startsWith('/')) {
    throw new FrontendApiBaseValidationError(
      'frontend API base URL must not be a filesystem path',
      'filesystem'
    )
  }

  if (value.length > MAX_FRONTEND_API_BASE_LENGTH) {
    throw new FrontendApiBaseValidationError(
      `frontend API base URL must not exceed ${MAX_FRONTEND_API_BASE_LENGTH} characters`,
      'length'
    )
  }

  if (value.trim() !== value || /[\s\u0000-\u001F\u007F]/u.test(value) || value.includes('\\')) {
    throw new FrontendApiBaseValidationError(
      'frontend API base URL must not contain whitespace or backslashes'
    )
  }

  if (/%(?![0-9A-Fa-f]{2})/u.test(value)) {
    throw new FrontendApiBaseValidationError(
      'frontend API base URL contains an invalid percent escape'
    )
  }

  const schemeMatch = /^(https?):\/\//u.exec(value)
  if (!schemeMatch) {
    throw new FrontendApiBaseValidationError(
      'frontend API base URL must be exactly /__aisys__/api or a strict HTTP(S) URL',
      'invalid'
    )
  }

  const authorityAndPath = value.slice(schemeMatch[0].length)
  if (authorityAndPath.length === 0 || authorityAndPath.startsWith('/')) {
    throw new FrontendApiBaseValidationError(
      'frontend API base URL must use a non-empty, unambiguous authority'
    )
  }

  const pathStart = authorityAndPath.indexOf('/')
  const authority = pathStart === -1
    ? authorityAndPath
    : authorityAndPath.slice(0, pathStart)
  const rawPath = pathStart === -1 ? '' : authorityAndPath.slice(pathStart)
  if (authority.length === 0 || rawPath.length === 0) {
    throw new FrontendApiBaseValidationError(
      'frontend API base URL must use a non-empty authority and path ending in /__aisys__/api'
    )
  }
  if (authority.includes('@')) {
    throw new FrontendApiBaseValidationError(
      'frontend API base URL must not contain userinfo'
    )
  }
  if (authority.includes('%')) {
    throw new FrontendApiBaseValidationError(
      'frontend API base URL authority must not contain percent escapes'
    )
  }
  if (rawPath.length > MAX_FRONTEND_API_BASE_PATH_LENGTH) {
    throw new FrontendApiBaseValidationError(
      `frontend API base URL path must not exceed ${MAX_FRONTEND_API_BASE_PATH_LENGTH} characters`,
      'length'
    )
  }
  if (!rawPath.endsWith(FRONTEND_API_BASE_ROOT)) {
    throw new FrontendApiBaseValidationError(
      'frontend API base URL path must end with /__aisys__/api'
    )
  }

  const authorityPort = authority.startsWith('[')
    ? (() => {
        const closingBracket = authority.indexOf(']')
        if (closingBracket === -1) {
          return { host: '', port: null, invalid: true }
        }
        const host = authority.slice(0, closingBracket + 1)
        const suffix = authority.slice(closingBracket + 1)
        if (suffix.length === 0) {
          return { host, port: null, invalid: false }
        }
        if (!suffix.startsWith(':')) {
          return { host: '', port: null, invalid: true }
        }
        return { host, port: suffix.slice(1), invalid: false }
      })()
    : (() => {
        const firstColon = authority.indexOf(':')
        if (firstColon === -1) {
          return { host: authority, port: null, invalid: false }
        }
        return {
          host: authority.slice(0, firstColon),
          port: authority.slice(firstColon + 1),
          invalid: authority.slice(firstColon + 1).includes(':')
        }
      })()

  if (authorityPort.invalid || authorityPort.host.length === 0) {
    throw new FrontendApiBaseValidationError(
      'frontend API base URL authority is invalid'
    )
  }
  if (authorityPort.port !== null
    && (!/^\d+$/u.test(authorityPort.port)
      || Number(authorityPort.port) > 65535)) {
    throw new FrontendApiBaseValidationError(
      'frontend API base URL port is invalid'
    )
  }

  let parsed
  try {
    parsed = new URL(value)
  } catch {
    throw new FrontendApiBaseValidationError('frontend API base URL is not a valid HTTP(S) URL')
  }

  if ((parsed.protocol !== 'http:' && parsed.protocol !== 'https:')
    || !parsed.hostname
    || parsed.username
    || parsed.password
    || parsed.search
    || parsed.hash
    || parsed.pathname !== rawPath
    || !parsed.pathname.endsWith(FRONTEND_API_BASE_ROOT)) {
    throw new FrontendApiBaseValidationError(
      'frontend API base URL must use HTTP(S), a non-empty host, and a path ending in /__aisys__/api'
    )
  }

  return value
}

function main() {
  if (process.argv.length !== 3) {
    process.stderr.write('usage: node frontend-api-base-contract.mjs <api-base>\n')
    process.exitCode = 2
    return
  }

  try {
    validateFrontendApiBase(process.argv[2])
  } catch (error) {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`)
    process.exitCode = 2
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  main()
}
