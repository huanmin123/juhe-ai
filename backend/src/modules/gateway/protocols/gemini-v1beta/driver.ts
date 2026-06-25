import { geminiEndpointModeForRequestShape } from '../../../../domain/gemini-endpoint-modes.js'
import {
  GEMINI_PROTOCOL_CODE,
  GEMINI_PROTOCOL_VERSION,
  isGeminiProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { GatewayProtocolDriver } from '../_shared/types.js'
import {
  isGeminiModelsRequest,
  isGeminiNativeRequest
} from './route-helpers.js'
import {
  extractGeminiJsonSemanticFrames,
  extractGeminiSseSemanticFrames,
  geminiResponseEndpointFamilyFromPath,
  type GeminiResponseEndpointFamily
} from './response-semantics.js'
import type { ResponseEndpointFamily } from '../openai-v1/response-semantics.js'
import {
  GeminiStreamInspector,
  applyGeminiStreamUsageFallback
} from './stream-inspection.js'
import {
  parseGeminiUsageFromJsonBuffer,
  parseGeminiUsageFromJsonTextFragment
} from './usage.js'
import { parseGeminiErrorPayload } from './error-payload.js'

export const geminiV1BetaProtocolDriver: GatewayProtocolDriver = {
  id: 'gemini-v1beta',
  protocolCode: GEMINI_PROTOCOL_CODE,
  protocolVersion: GEMINI_PROTOCOL_VERSION,
  responseProtocol: 'gemini_v1beta',
  clientErrorProtocol: 'gemini',
  defaultClientProfile: 'generic_openai',
  supportsProfile: isGeminiProtocolProfile,
  endpointModeForRequestShape: geminiEndpointModeForRequestShape,
  isNativeRequest: isGeminiNativeRequest,
  isModelsRequest: isGeminiModelsRequest,
  responseEndpointFamilyForRequest: (req) =>
    geminiResponseEndpointFamilyFromPath(req.originalUrl || req.path),
  extractJsonSemanticFrames: (value, req) =>
    extractGeminiJsonSemanticFrames(value, geminiResponseEndpointFamilyFromPath(req.originalUrl || req.path)),
  createStreamInspector: () => new GeminiStreamInspector(),
  responseInspectionEndpointFamily: geminiEndpointFamilyOrGenerateContent,
  extractSseSemanticFrames: (event, endpointFamily) =>
    extractGeminiSseSemanticFrames(event, geminiEndpointFamilyOrGenerateContent(endpointFamily)),
  sseResponseInspectionFailureEvent: 'none',
  drainForKeepAliveAfterTerminal: false,
  parseUsageFromJsonBuffer: parseGeminiUsageFromJsonBuffer,
  parseUsageFromJsonTextFragment: parseGeminiUsageFromJsonTextFragment,
  parseErrorPayload: parseGeminiErrorPayload,
  applyStreamUsageFallback: applyGeminiStreamUsageFallback
}

function geminiEndpointFamilyOrGenerateContent(endpointFamily?: ResponseEndpointFamily): GeminiResponseEndpointFamily {
  return endpointFamily === 'generate_content'
    || endpointFamily === 'stream_generate_content'
    || endpointFamily === 'count_tokens'
    || endpointFamily === 'embed_content'
    || endpointFamily === 'models'
    ? endpointFamily
    : 'generate_content'
}
