import { createHash } from 'node:crypto'
import { createReadStream, createWriteStream } from 'node:fs'
import { pipeline } from 'node:stream/promises'
import { Transform } from 'node:stream'

import Busboy from 'busboy'
import { Router, type NextFunction, type Request, type Response } from 'express'

import { requestDbService } from '../db-service/db-service-ipc.js'
import type { GatewayRuntimeRequest } from '../gateway/request/pre-auth.js'
import { gatewayErrorPayload } from '../gateway/response/responses.js'
import type { OpenAICompatibleFileRecord } from '../../storage/openai-compatible-files.repository.js'
import {
  ensureOpenAICompatibleFileObjectParent,
  newOpenAICompatibleFileId,
  normalizeOpenAICompatibleFileMediaType,
  openAICompatibleFileMaxBytes,
  openAICompatibleFileObjectPath,
  removeOpenAICompatibleFileObject,
  storageKeyForOpenAICompatibleFile
} from './file-storage.js'

export const openAICompatibleFilesRouter = Router()

interface UploadedOpenAICompatibleFile {
  fileId: string
  filename: string
  mediaType?: string
  storageKey: string
  bytes: number
  sha256: string
}

class OpenAICompatibleFilesRequestError extends Error {
  constructor(
    message: string,
    readonly statusCode = 400,
    readonly type = 'invalid_request_error',
    readonly code?: string
  ) {
    super(message)
  }
}

openAICompatibleFilesRouter.get('/v1/files', handleOpenAICompatibleFilesRoute(listOpenAICompatibleFiles))
openAICompatibleFilesRouter.post('/v1/files', handleOpenAICompatibleFilesRoute(uploadOpenAICompatibleFile))
openAICompatibleFilesRouter.get('/v1/files/:fileId/content', handleOpenAICompatibleFilesRoute(downloadOpenAICompatibleFileContent))
openAICompatibleFilesRouter.get('/v1/files/:fileId', handleOpenAICompatibleFilesRoute(getOpenAICompatibleFile))
openAICompatibleFilesRouter.delete('/v1/files/:fileId', handleOpenAICompatibleFilesRoute(deleteOpenAICompatibleFileRoute))

async function uploadOpenAICompatibleFile(req: Request, res: Response): Promise<void> {
  const runtime = requireGatewayRuntime(req)
  const contentType = req.header('content-type') ?? ''
  if (!/^multipart\/form-data\b/i.test(contentType)) {
    throw new OpenAICompatibleFilesRequestError('Files upload requires multipart/form-data', 400, 'invalid_request_error', 'invalid_content_type')
  }
  const upload = await readOpenAICompatibleMultipartUpload(req)
  let created: OpenAICompatibleFileRecord | undefined
  try {
    created = await requestDbService({
      type: 'create_openai_compatible_file',
      input: {
        id: upload.file.fileId,
        systemAccountId: runtime.apiKey.system_account_id,
        apiKeyId: runtime.apiKey.id,
        purpose: upload.purpose,
        filename: upload.file.filename,
        bytes: upload.file.bytes,
        mediaType: upload.file.mediaType,
        storageKey: upload.file.storageKey,
        sha256: upload.file.sha256
      }
    }, { timeoutMs: 10_000 })
  } catch (error) {
    await removeOpenAICompatibleFileObject(upload.file.storageKey).catch(() => undefined)
    throw error
  }
  res.status(200).json(openAICompatibleFileObject(created))
}

async function listOpenAICompatibleFiles(req: Request, res: Response): Promise<void> {
  const runtime = requireGatewayRuntime(req)
  const result = await requestDbService({
    type: 'list_openai_compatible_files',
    options: {
      systemAccountId: runtime.apiKey.system_account_id,
      apiKeyId: runtime.apiKey.id,
      purpose: queryString(req.query.purpose),
      limit: queryInteger(req.query.limit),
      order: queryString(req.query.order) === 'asc' ? 'asc' : 'desc',
      after: queryString(req.query.after)
    }
  })
  const data = result.items.map(openAICompatibleFileObject)
  res.json({
    object: 'list',
    data,
    first_id: data[0]?.id,
    last_id: data[data.length - 1]?.id,
    has_more: result.hasMore
  })
}

async function getOpenAICompatibleFile(req: Request, res: Response): Promise<void> {
  const record = await findOpenAICompatibleFileForRequest(req)
  if (!record) {
    throw new OpenAICompatibleFilesRequestError('File not found', 404, 'invalid_request_error', 'file_not_found')
  }
  res.json(openAICompatibleFileObject(record))
}

async function downloadOpenAICompatibleFileContent(req: Request, res: Response): Promise<void> {
  const record = await findOpenAICompatibleFileForRequest(req)
  if (!record) {
    throw new OpenAICompatibleFilesRequestError('File not found', 404, 'invalid_request_error', 'file_not_found')
  }
  const filePath = openAICompatibleFileObjectPath(record.storageKey)
  res.status(200)
  res.setHeader('content-type', record.mediaType ?? 'application/octet-stream')
  res.setHeader('content-length', String(record.bytes))
  await pipeline(createReadStream(filePath), res)
}

async function deleteOpenAICompatibleFileRoute(req: Request, res: Response): Promise<void> {
  const runtime = requireGatewayRuntime(req)
  const fileId = pathFileId(req)
  const record = await requestDbService({
    type: 'delete_openai_compatible_file',
    fileId,
    systemAccountId: runtime.apiKey.system_account_id,
    apiKeyId: runtime.apiKey.id
  })
  if (!record) {
    throw new OpenAICompatibleFilesRequestError('File not found', 404, 'invalid_request_error', 'file_not_found')
  }
  await removeOpenAICompatibleFileObject(record.storageKey).catch(() => undefined)
  res.json({
    id: record.id,
    object: 'file',
    deleted: true
  })
}

async function findOpenAICompatibleFileForRequest(req: Request): Promise<OpenAICompatibleFileRecord | undefined> {
  const runtime = requireGatewayRuntime(req)
  return await requestDbService({
    type: 'get_openai_compatible_file',
    fileId: pathFileId(req),
    systemAccountId: runtime.apiKey.system_account_id,
    apiKeyId: runtime.apiKey.id
  })
}

async function readOpenAICompatibleMultipartUpload(req: Request): Promise<{
  purpose: string
  file: UploadedOpenAICompatibleFile
}> {
  let purpose = ''
  const fileUploads: Array<Promise<UploadedOpenAICompatibleFile>> = []
  let parseError: OpenAICompatibleFilesRequestError | undefined
  const busboy = Busboy({
    headers: req.headers,
    limits: {
      files: 1,
      fields: 8,
      fileSize: openAICompatibleFileMaxBytes
    }
  })
  busboy.on('field', (name, value) => {
    if (name === 'purpose') {
      purpose = String(value ?? '').trim()
    }
  })
  busboy.on('file', (name, stream, info) => {
    if (name !== 'file') {
      stream.resume()
      return
    }
    if (fileUploads.length > 0) {
      parseError = new OpenAICompatibleFilesRequestError('Only one file can be uploaded per request', 400, 'invalid_request_error', 'too_many_files')
      stream.resume()
      return
    }
    fileUploads.push(writeUploadedOpenAICompatibleFile(stream, info))
  })
  busboy.on('filesLimit', () => {
    parseError = new OpenAICompatibleFilesRequestError('Only one file can be uploaded per request', 400, 'invalid_request_error', 'too_many_files')
  })
  await new Promise<void>((resolve, reject) => {
    busboy.once('finish', resolve)
    busboy.once('error', reject)
    req.pipe(busboy)
  })
  if (parseError) throw parseError
  const files = await Promise.all(fileUploads)
  const file = files[0]
  if (!purpose) {
    if (file) await removeOpenAICompatibleFileObject(file.storageKey).catch(() => undefined)
    throw new OpenAICompatibleFilesRequestError('Missing required multipart field: purpose', 400, 'invalid_request_error', 'missing_purpose')
  }
  if (!file) {
    throw new OpenAICompatibleFilesRequestError('Missing required multipart file field: file', 400, 'invalid_request_error', 'missing_file')
  }
  return { purpose, file }
}

async function writeUploadedOpenAICompatibleFile(
  stream: NodeJS.ReadableStream,
  info: { filename?: string; mimeType?: string }
): Promise<UploadedOpenAICompatibleFile> {
  const fileId = newOpenAICompatibleFileId()
  const filename = normalizedUploadFilename(info.filename)
  const mediaType = normalizeOpenAICompatibleFileMediaType(info.mimeType, filename)
  const storageKey = storageKeyForOpenAICompatibleFile(fileId)
  const filePath = ensureOpenAICompatibleFileObjectParent(storageKey)
  const hash = createHash('sha256')
  let bytes = 0
  let fileSizeLimitHit = false
  stream.on('limit', () => {
    fileSizeLimitHit = true
  })
  const counter = new Transform({
    transform(chunk: Buffer, _encoding, callback) {
      bytes += chunk.length
      hash.update(chunk)
      callback(null, chunk)
    }
  })
  try {
    await pipeline(stream, counter, createWriteStream(filePath))
    if (fileSizeLimitHit) {
      throw new OpenAICompatibleFilesRequestError('Uploaded file is too large', 413, 'request_too_large', 'file_too_large')
    }
    if (bytes <= 0) {
      throw new OpenAICompatibleFilesRequestError('Uploaded file is empty', 400, 'invalid_request_error', 'empty_file')
    }
    return {
      fileId,
      filename,
      mediaType,
      storageKey,
      bytes,
      sha256: hash.digest('hex')
    }
  } catch (error) {
    await removeOpenAICompatibleFileObject(storageKey).catch(() => undefined)
    throw error
  }
}

function handleOpenAICompatibleFilesRoute(
  handler: (req: Request, res: Response) => Promise<void>
): (req: Request, res: Response, next: NextFunction) => void {
  return (req, res, next) => {
    handler(req, res).catch((error: unknown) => {
      if (res.headersSent) {
        next(error)
        return
      }
      if (error instanceof OpenAICompatibleFilesRequestError) {
        res.status(error.statusCode).json(gatewayErrorPayload(error.message, error.type, error.code))
        return
      }
      next(error)
    })
  }
}

function requireGatewayRuntime(req: Request): Required<Pick<NonNullable<GatewayRuntimeRequest['gatewayRuntime']>, 'apiKey'>> {
  const runtime = (req as GatewayRuntimeRequest).gatewayRuntime
  if (!runtime?.apiKey) {
    throw new OpenAICompatibleFilesRequestError('Missing or invalid API key', 401, 'invalid_request_error', 'invalid_api_key')
  }
  return { apiKey: runtime.apiKey }
}

function openAICompatibleFileObject(record: OpenAICompatibleFileRecord): Record<string, unknown> {
  return {
    id: record.id,
    object: 'file',
    bytes: record.bytes,
    created_at: openAITimestamp(record.createdAt),
    filename: record.filename,
    purpose: record.purpose,
    status: record.status,
    ...(record.expiresAt ? { expires_at: openAITimestamp(record.expiresAt) } : {})
  }
}

function pathFileId(req: Request): string {
  const value = typeof req.params.fileId === 'string' ? req.params.fileId.trim() : ''
  if (!value) {
    throw new OpenAICompatibleFilesRequestError('Missing file id', 400, 'invalid_request_error', 'missing_file_id')
  }
  return value
}

function normalizedUploadFilename(value: string | undefined): string {
  const filename = value?.split(/[\\/]/).pop()?.trim()
  return filename || 'upload'
}

function queryString(value: unknown): string | undefined {
  if (typeof value === 'string') {
    const text = value.trim()
    return text || undefined
  }
  return undefined
}

function queryInteger(value: unknown): number | undefined {
  const text = queryString(value)
  if (!text) return undefined
  const number = Number(text)
  return Number.isFinite(number) ? Math.trunc(number) : undefined
}

function openAITimestamp(value: string): number {
  const ms = Date.parse(value)
  return Number.isFinite(ms) ? Math.floor(ms / 1000) : Math.floor(Date.now() / 1000)
}
