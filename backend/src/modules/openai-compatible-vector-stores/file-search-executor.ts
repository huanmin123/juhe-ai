import type { Request } from 'express'

import { requestDbService } from '../db-service/db-service-ipc.js'
import type { GatewayRuntimeRequest } from '../gateway/request/pre-auth.js'
import { GatewayRequestValidationError } from '../gateway/request/validation-error.js'
import type {
  OpenAIToAnthropicFileSearchExecutor,
  OpenAIToAnthropicFileSearchResult
} from '../providers/drivers/_shared/openai-anthropic-bridge.js'

export function openAICompatibleFileSearchExecutorForGatewayRequest(
  req: Request
): OpenAIToAnthropicFileSearchExecutor | undefined {
  const runtime = (req as GatewayRuntimeRequest).gatewayRuntime
  const apiKey = runtime?.apiKey
  if (!apiKey) return undefined
  return {
    async search(input) {
      const maxNumResults = normalizeFileSearchMaxResults(input.maxNumResults)
      const allResults: OpenAIToAnthropicFileSearchResult[] = []
      for (const vectorStoreId of input.vectorStoreIds) {
        const store = await requestDbService({
          type: 'get_openai_compatible_vector_store',
          vectorStoreId,
          systemAccountId: apiKey.system_account_id,
          apiKeyId: apiKey.id
        })
        if (!store) {
          throw new GatewayRequestValidationError(
            `Vector store ${vectorStoreId} not found`,
            'openai_anthropic_bridge_file_search_vector_store_not_found',
            { statusCode: 404, type: 'invalid_request_error' }
          )
        }
        if (store.fileCounts.inProgress > 0) {
          throw new GatewayRequestValidationError(
            `Vector store ${vectorStoreId} files are still being indexed`,
            'openai_anthropic_bridge_file_search_vector_store_not_ready',
            { statusCode: 409, type: 'invalid_request_error' }
          )
        }
        if (store.fileCounts.completed <= 0 && store.fileCounts.failed > 0) {
          throw new GatewayRequestValidationError(
            `Vector store ${vectorStoreId} has no completed files because indexing failed`,
            'openai_anthropic_bridge_file_search_vector_store_failed',
            { statusCode: 400, type: 'invalid_request_error' }
          )
        }
        const results = await requestDbService({
          type: 'search_openai_compatible_vector_store',
          options: {
            vectorStoreId,
            systemAccountId: apiKey.system_account_id,
            apiKeyId: apiKey.id,
            query: input.query,
            maxNumResults,
            filters: input.filters,
            scoreThreshold: scoreThresholdFromRankingOptions(input.rankingOptions)
          }
        })
        allResults.push(...results.map((result) => ({
          fileId: result.fileId,
          filename: result.filename,
          score: result.score,
          contentText: result.contentText
        })))
      }
      allResults.sort((left, right) => right.score - left.score || left.fileId.localeCompare(right.fileId))
      return {
        queries: [input.query],
        results: allResults.slice(0, maxNumResults)
      }
    }
  }
}

function normalizeFileSearchMaxResults(value: number | undefined): number {
  if (!Number.isFinite(value)) return 10
  return Math.max(1, Math.min(Math.trunc(value ?? 10), 50))
}

function scoreThresholdFromRankingOptions(value: Record<string, unknown> | undefined): number | undefined {
  const threshold = value?.score_threshold
  const number = typeof threshold === 'number'
    ? threshold
    : typeof threshold === 'string' && threshold.trim() ? Number(threshold) : NaN
  return Number.isFinite(number) ? number : undefined
}
