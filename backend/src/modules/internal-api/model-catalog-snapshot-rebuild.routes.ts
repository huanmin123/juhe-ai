import { createHmac, timingSafeEqual } from 'node:crypto'

import express, {
  Router,
  type NextFunction,
  type Request,
  type Response
} from 'express'

import { isLoopbackRemoteAddress } from './account-health-check-dispatch.routes.js'

export const modelCatalogSnapshotRebuildInternalPrefix = '/__aiinternal__'
export const modelCatalogSnapshotRebuildSignatureDomain = 'juhe-ai:model-catalog-snapshot-rebuild:v1\n'
export const modelCatalogSnapshotRebuildReadinessSignatureDomain = 'juhe-ai:model-catalog-snapshot-readiness:v1\n'

const modelCatalogSnapshotRebuildPath = '/v1/model-catalog-snapshots/rebuild'
const modelCatalogSnapshotRebuildReadinessPath = '/v1/model-catalog-snapshots/readyz'
const rawBodyLimitBytes = 1024
const signaturePattern = /^v1=([0-9a-f]{64})$/

type BodyParserError = Error & {
  status?: number
  statusCode?: number
  type?: string
}

export interface ModelCatalogSnapshotRebuildRouterOptions {
  secret: string
  schemaVersion: number
  checkReady: () => Promise<void>
  rebuildAll: () => Promise<unknown>
  rebuildPersonal: (systemAccountId: string) => Promise<unknown>
}

export type ModelCatalogSnapshotRebuildPayload =
  | { scope: 'all' }
  | { scope: 'personal'; systemAccountId: string }

export function createModelCatalogSnapshotRebuildSignature(secret: string, rawBody: Uint8Array): string {
  return `v1=${createHmac('sha256', secret)
    .update(modelCatalogSnapshotRebuildSignatureDomain, 'utf8')
    .update(rawBody)
    .digest('hex')}`
}

export function createModelCatalogSnapshotRebuildReadinessSignature(secret: string): string {
  return `v1=${createHmac('sha256', secret)
    .update(modelCatalogSnapshotRebuildReadinessSignatureDomain, 'utf8')
    .update(Buffer.alloc(0))
    .digest('hex')}`
}

export function createModelCatalogSnapshotRebuildRouter(
  options: ModelCatalogSnapshotRebuildRouterOptions
): Router {
  const router = Router({ caseSensitive: true, strict: true })

  router.use((req, res, next) => {
    res.setHeader('Cache-Control', 'no-store')
    if (req.baseUrl !== modelCatalogSnapshotRebuildInternalPrefix) {
      res.status(404).json({ message: '资源不存在' })
      return
    }
    next()
  })

  router.all(
    modelCatalogSnapshotRebuildReadinessPath,
    (req, res, next) => {
      if (req.method !== 'GET') {
        res.status(404).json({ message: '资源不存在' })
        return
      }
      next()
    },
    requireLoopback,
    (req: Request, res: Response) => {
      if (!hasValidReadinessSignature(req, options.secret)) {
        res.status(401).json({ message: '认证失败' })
        return
      }

      let readiness: Promise<void>
      try {
        readiness = options.checkReady()
      } catch {
        res.status(503).json({ ready: false, code: 'dependency_unavailable' })
        return
      }
      void readiness.then(() => {
        if (!res.headersSent && !res.writableEnded) {
          res.status(200).json({
            ready: true,
            component: 'model-catalog-snapshot-rebuild',
            contractVersion: 1,
            databaseDriver: 'postgres',
            schemaVersion: options.schemaVersion
          })
        }
      }).catch(() => {
        if (!res.headersSent && !res.writableEnded) {
          res.status(503).json({ ready: false, code: 'dependency_unavailable' })
        }
      })
    }
  )

  router.post(
    modelCatalogSnapshotRebuildPath,
    requireLoopback,
    requireJsonContentType,
    requireIdentityContentEncoding,
    express.raw({
      type: () => true,
      limit: rawBodyLimitBytes,
      inflate: false
    }),
    handleBodyParserError,
    (req: Request, res: Response, next: NextFunction) => {
      const rawBody = Buffer.isBuffer(req.body) ? req.body : Buffer.alloc(0)
      if (!hasValidSignature(req, options.secret, rawBody)) {
        res.status(401).json({ message: '认证失败' })
        return
      }

      const payload = parsePayload(rawBody)
      if (!payload) {
        res.status(400).json({ message: '请求参数无效' })
        return
      }

      let rebuild: Promise<unknown>
      try {
        rebuild = payload.scope === 'all'
          ? options.rebuildAll()
          : options.rebuildPersonal(payload.systemAccountId)
      } catch (error) {
        next(error)
        return
      }
      void rebuild.then(() => {
        if (!res.headersSent && !res.writableEnded) {
          res.status(202).json({ accepted: true })
        }
      }).catch(next)
    }
  )
  router.all(modelCatalogSnapshotRebuildPath, (_req, res) => {
    res.status(404).json({ message: '资源不存在' })
  })

  return router
}

function requireLoopback(req: Request, res: Response, next: NextFunction): void {
  if (!isLoopbackRemoteAddress(req.socket.remoteAddress)) {
    res.status(403).json({ message: '禁止访问' })
    return
  }
  next()
}

function requireJsonContentType(req: Request, res: Response, next: NextFunction): void {
  const contentType = req.headers['content-type']
  if (typeof contentType !== 'string' || contentType.split(';', 1)[0]?.trim().toLowerCase() !== 'application/json') {
    res.status(415).json({ message: '仅支持 JSON 请求' })
    return
  }
  next()
}

function requireIdentityContentEncoding(req: Request, res: Response, next: NextFunction): void {
  const contentEncoding = req.headers['content-encoding']
  if (
    contentEncoding !== undefined
    && (typeof contentEncoding !== 'string' || contentEncoding.trim().toLowerCase() !== 'identity')
  ) {
    res.status(415).json({ message: '不支持压缩请求体' })
    return
  }
  next()
}

function handleBodyParserError(
  error: BodyParserError,
  req: Request,
  res: Response,
  next: NextFunction
): void {
  if (error.type === 'request.aborted') return
  const statusCode = error.type === 'entity.too.large'
    ? 413
    : [error.statusCode, error.status].find((value) => Number.isInteger(value) && Number(value) >= 400 && Number(value) <= 499)
  if (statusCode === undefined) {
    next(error)
    return
  }
  if (req.aborted || req.destroyed || res.destroyed || res.writableEnded || res.headersSent) return
  res.status(Number(statusCode)).json({ message: Number(statusCode) === 413 ? '请求体过大' : '请求体无效' })
}

function hasValidSignature(req: Request, secret: string, rawBody: Buffer): boolean {
  const signature = req.headers['x-juhe-ai-signature']
  if (typeof signature !== 'string') return false
  const match = signaturePattern.exec(signature)
  if (!match) return false
  const providedDigest = Buffer.from(match[1]!, 'hex')
  const expectedDigest = Buffer.from(createModelCatalogSnapshotRebuildSignature(secret, rawBody).slice(3), 'hex')
  return timingSafeEqual(providedDigest, expectedDigest)
}

function hasValidReadinessSignature(req: Request, secret: string): boolean {
  const signature = req.headers['x-juhe-ai-signature']
  if (typeof signature !== 'string') return false
  const match = signaturePattern.exec(signature)
  if (!match) return false
  const providedDigest = Buffer.from(match[1]!, 'hex')
  const expectedDigest = Buffer.from(createModelCatalogSnapshotRebuildReadinessSignature(secret).slice(3), 'hex')
  return timingSafeEqual(providedDigest, expectedDigest)
}

function parsePayload(rawBody: Buffer): ModelCatalogSnapshotRebuildPayload | undefined {
  if (rawBody.length === 0) return undefined

  let parsed: unknown
  try {
    parsed = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(rawBody)) as unknown
  } catch {
    return undefined
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) return undefined

  const record = parsed as Record<string, unknown>
  const keys = Object.keys(record)
  if (record.scope === 'all') {
    return keys.length === 1 && keys[0] === 'scope' ? { scope: 'all' } : undefined
  }
  if (record.scope !== 'personal' || keys.length !== 2 || !keys.includes('systemAccountId')) return undefined
  if (typeof record.systemAccountId !== 'string') return undefined
  const systemAccountId = record.systemAccountId.trim()
  return systemAccountId ? { scope: 'personal', systemAccountId } : undefined
}
