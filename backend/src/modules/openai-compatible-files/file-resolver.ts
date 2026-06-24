import { readFile } from 'node:fs/promises'
import type { Request } from 'express'

import { requestDbService } from '../db-service/db-service-ipc.js'
import type { GatewayRuntimeRequest } from '../gateway/request/pre-auth.js'
import { GatewayRequestValidationError } from '../gateway/request/validation-error.js'
import type {
  OpenAIToAnthropicFileResolver,
  OpenAIToAnthropicResolvedFile
} from '../providers/drivers/_shared/openai-anthropic-bridge.js'
import {
  openAICompatibleBridgeFileMaxBytes,
  openAICompatibleFileObjectPath
} from './file-storage.js'

export function openAICompatibleFilesResolverForGatewayRequest(req: Request): OpenAIToAnthropicFileResolver | undefined {
  const runtime = (req as GatewayRuntimeRequest).gatewayRuntime
  const apiKey = runtime?.apiKey
  if (!apiKey) return undefined
  return {
    async resolveFile(input): Promise<OpenAIToAnthropicResolvedFile | undefined> {
      const record = await requestDbService({
        type: 'get_openai_compatible_file',
        fileId: input.fileId,
        systemAccountId: apiKey.system_account_id,
        apiKeyId: apiKey.id
      })
      if (!record) return undefined
      if (record.bytes > openAICompatibleBridgeFileMaxBytes) {
        throw new GatewayRequestValidationError(
          `文件 ${record.id} 超过 Anthropic bridge 单次解析大小上限`,
          'openai_anthropic_bridge_file_too_large',
          { statusCode: 413, type: 'request_too_large' }
        )
      }
      const buffer = await readFile(openAICompatibleFileObjectPath(record.storageKey))
      if (isTextBridgeMediaType(record.mediaType)) {
        return {
          fileId: record.id,
          filename: record.filename,
          mediaType: record.mediaType,
          bytes: record.bytes,
          contentText: buffer.toString('utf8')
        }
      }
      return {
        fileId: record.id,
        filename: record.filename,
        mediaType: record.mediaType,
        bytes: record.bytes,
        contentBase64: buffer.toString('base64')
      }
    }
  }
}

function isTextBridgeMediaType(mediaType: string | undefined): boolean {
  if (!mediaType) return false
  return mediaType === 'text/plain' || mediaType.startsWith('text/')
}
