import { createReadStream } from 'node:fs'
import { StringDecoder } from 'node:string_decoder'

import type {
  OpenAICompatibleVectorStoreChunkInput
} from '../../storage/openai-compatible-vector-stores.repository.js'
import type { OpenAICompatibleFileRecord } from '../../storage/openai-compatible-files.repository.js'
import { openAICompatibleFileObjectPath } from '../openai-compatible-files/file-storage.js'

export const openAICompatibleVectorStoreTextIndexMaxBytes = 2 * 1024 * 1024

const vectorStoreChunkMaxChars = 2400
const vectorStoreChunkOverlapChars = 400
const vectorStoreMaxChunksPerFile = 256

export class OpenAICompatibleVectorStoreIndexingError extends Error {
  constructor(
    message: string,
    readonly statusCode = 400,
    readonly type = 'invalid_request_error',
    readonly code = 'openai_compatible_vector_store_index_failed'
  ) {
    super(message)
  }
}

export async function buildOpenAICompatibleVectorStoreChunks(
  file: OpenAICompatibleFileRecord
): Promise<OpenAICompatibleVectorStoreChunkInput[]> {
  if (!isSupportedVectorStoreTextMediaType(file.mediaType)) {
    throw new OpenAICompatibleVectorStoreIndexingError(
      `File ${file.id} media type is not supported by the local vector store text indexer`,
      400,
      'invalid_request_error',
      'openai_compatible_file_mime_unsupported'
    )
  }
  const text = await readOpenAICompatibleFileTextForIndexing(file)
  const normalized = text.replace(/\u0000/g, '').trim()
  if (!normalized) {
    throw new OpenAICompatibleVectorStoreIndexingError(
      `File ${file.id} did not contain indexable text`,
      400,
      'invalid_request_error',
      'openai_compatible_vector_store_empty_file'
    )
  }
  const chunks = chunkTextForVectorStore(normalized)
  if (chunks.length > vectorStoreMaxChunksPerFile) {
    throw new OpenAICompatibleVectorStoreIndexingError(
      `File ${file.id} produced too many chunks for the local vector store text indexer`,
      413,
      'request_too_large',
      'openai_compatible_vector_store_file_too_large'
    )
  }
  return chunks
}

export function isSupportedVectorStoreTextMediaType(mediaType: string | undefined): boolean {
  if (!mediaType) return false
  return mediaType.startsWith('text/')
    || mediaType === 'application/json'
    || mediaType === 'application/typescript'
    || mediaType === 'application/x-sh'
}

async function readOpenAICompatibleFileTextForIndexing(file: OpenAICompatibleFileRecord): Promise<string> {
  if (file.bytes > openAICompatibleVectorStoreTextIndexMaxBytes) {
    throw new OpenAICompatibleVectorStoreIndexingError(
      `File ${file.id} exceeds the local vector store text indexing size limit`,
      413,
      'request_too_large',
      'openai_compatible_vector_store_file_too_large'
    )
  }
  const decoder = new StringDecoder('utf8')
  let bytes = 0
  let text = ''
  for await (const chunk of createReadStream(openAICompatibleFileObjectPath(file.storageKey))) {
    const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
    bytes += buffer.length
    if (bytes > openAICompatibleVectorStoreTextIndexMaxBytes) {
      throw new OpenAICompatibleVectorStoreIndexingError(
        `File ${file.id} exceeds the local vector store text indexing size limit`,
        413,
        'request_too_large',
        'openai_compatible_vector_store_file_too_large'
      )
    }
    text += decoder.write(buffer)
  }
  text += decoder.end()
  return text
}

function chunkTextForVectorStore(text: string): OpenAICompatibleVectorStoreChunkInput[] {
  const chunks: OpenAICompatibleVectorStoreChunkInput[] = []
  let start = 0
  while (start < text.length) {
    const end = Math.min(text.length, start + vectorStoreChunkMaxChars)
    const rawChunk = text.slice(start, end).trim()
    if (rawChunk) {
      chunks.push({
        contentText: rawChunk,
        contentPreview: rawChunk.replace(/\s+/g, ' ').slice(0, 500),
        tokenEstimate: Math.max(1, Math.ceil(rawChunk.length / 4)),
        keywordIndexText: rawChunk.toLowerCase().replace(/\s+/g, ' ')
      })
    }
    if (end >= text.length) break
    start = Math.max(end - vectorStoreChunkOverlapChars, start + 1)
  }
  return chunks
}
