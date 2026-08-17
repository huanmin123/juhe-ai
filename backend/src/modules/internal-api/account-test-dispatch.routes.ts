import { createHmac, timingSafeEqual } from 'node:crypto'
import { BlockList, isIP } from 'node:net'

import express, { Router, type NextFunction, type Request, type Response } from 'express'

export const accountTestDispatchInternalPrefix = '/__aiinternal__'
export const accountTestDispatchSignatureDomain = 'juhe-ai:account-test-dispatch:v1\n'

const accountTestDispatchPath = '/v1/account-test/dispatch'
const rawBodyLimitBytes = 1024
const signaturePattern = /^v1=([0-9a-f]{64})$/
const loopbackAddresses = new BlockList()
loopbackAddresses.addAddress('127.0.0.1', 'ipv4')
loopbackAddresses.addAddress('::1', 'ipv6')

function isLoopbackRemoteAddress(remoteAddress: string | undefined): boolean {
  if (!remoteAddress) return false
  const version = isIP(remoteAddress)
  return version !== 0 && loopbackAddresses.check(remoteAddress, version === 4 ? 'ipv4' : 'ipv6')
}

export interface AccountTestDispatchRouterOptions {
  secret: string
  dispatch: (taskId: string) => boolean
}

type BodyParserError = Error & {
  status?: number
  statusCode?: number
  type?: string
}

export function createAccountTestDispatchSignature(secret: string, rawBody: Uint8Array): string {
  return `v1=${createHmac('sha256', secret)
    .update(accountTestDispatchSignatureDomain, 'utf8')
    .update(rawBody)
    .digest('hex')}`
}

export function createAccountTestDispatchRouter(options: AccountTestDispatchRouterOptions): Router {
  const router = Router({ caseSensitive: true, strict: true })
  router.post(
    accountTestDispatchPath,
    requireLoopback,
    requireJsonContentType,
    requireIdentityContentEncoding,
    express.raw({ type: () => true, limit: rawBodyLimitBytes, inflate: false }),
    handleBodyParserError,
    (req: Request, res: Response, next: NextFunction) => handleDispatch(req, res, next, options)
  )
  return router
}

function requireLoopback(req: Request, res: Response, next: NextFunction): void {
  res.setHeader('Cache-Control', 'no-store')
  if (!isLoopbackRemoteAddress(req.socket.remoteAddress)) {
    res.status(403).json({ message: '禁止访问' })
    return
  }
  next()
}

function requireJsonContentType(req: Request, res: Response, next: NextFunction): void {
  const contentType = req.headers['content-type']
  const mediaType = typeof contentType === 'string' ? contentType.split(';', 1)[0]?.trim().toLowerCase() ?? '' : ''
  if (mediaType !== 'application/json' && !mediaType.endsWith('+json')) {
    res.status(415).json({ message: '仅支持 JSON 请求' })
    return
  }
  next()
}

function requireIdentityContentEncoding(req: Request, res: Response, next: NextFunction): void {
  const contentEncoding = req.headers['content-encoding']
  if (contentEncoding !== undefined && (typeof contentEncoding !== 'string' || contentEncoding.trim().toLowerCase() !== 'identity')) {
    res.status(415).json({ message: '不支持压缩请求体' })
    return
  }
  next()
}

function handleBodyParserError(error: BodyParserError, req: Request, res: Response, next: NextFunction): void {
  if (error.type === 'request.aborted') return
  const status = error.type === 'entity.too.large'
    ? 413
    : [error.statusCode, error.status].find((value) => Number.isInteger(value) && Number(value) >= 400 && Number(value) <= 499)
  if (status === undefined) {
    next(error)
    return
  }
  if (req.aborted || req.destroyed || res.destroyed || res.writableEnded || res.headersSent) return
  res.status(Number(status)).json({ message: Number(status) === 413 ? '请求体过大' : '请求体无效' })
}

function handleDispatch(
  req: Request,
  res: Response,
  next: NextFunction,
  options: AccountTestDispatchRouterOptions
): void {
  const rawBody = Buffer.isBuffer(req.body) ? req.body : Buffer.alloc(0)
  if (!hasValidSignature(req, options.secret, rawBody)) {
    res.status(401).json({ message: '认证失败' })
    return
  }
  const taskId = parseTaskID(rawBody)
  if (!taskId) {
    res.status(400).json({ message: '请求参数无效' })
    return
  }
  try {
    if (!options.dispatch(taskId)) {
      res.status(503).json({ message: '服务暂不可用' })
      return
    }
    res.status(202).end()
  } catch (error) {
    next(error)
  }
}

function hasValidSignature(req: Request, secret: string, rawBody: Buffer): boolean {
  const signature = req.headers['x-juhe-ai-signature']
  if (typeof signature !== 'string') return false
  const match = signaturePattern.exec(signature)
  if (!match) return false
  return timingSafeEqual(
    Buffer.from(match[1]!, 'hex'),
    Buffer.from(createAccountTestDispatchSignature(secret, rawBody).slice(3), 'hex')
  )
}

function parseTaskID(rawBody: Buffer): string | undefined {
  let parsed: unknown
  try {
    parsed = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(rawBody)) as unknown
  } catch {
    return undefined
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) return undefined
  const record = parsed as Record<string, unknown>
  if (Object.keys(record).length !== 2 || record.version !== 1 || typeof record.taskId !== 'string') return undefined
  const taskId = record.taskId.trim()
  return taskId || undefined
}
