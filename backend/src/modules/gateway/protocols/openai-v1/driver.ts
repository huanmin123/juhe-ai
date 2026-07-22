import { openAIEndpointModeForRequestShape } from '../../../../domain/openai-endpoint-modes.js'
import {
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  isOpenAIProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { GatewayProtocolDriver } from '../_shared/types.js'
import { isOpenAIModelsRequest } from './route-helpers.js'
import {
  extractOpenAIJsonSemanticFrames,
  extractOpenAISseSemanticFrames,
  type OpenAIResponseEndpointFamily,
  type ResponseEndpointFamily,
  openAIResponseEndpointFamilyFromRequest
} from './response-semantics.js'
import {
  OpenAIStreamInspector,
  applyOpenAIStreamUsageFallback
} from './stream-inspection.js'
import {
  parseOpenAIUsageFromJsonBuffer,
  parseOpenAIUsageFromJsonTextFragment
} from './usage.js'
import { parseOpenAIErrorPayload } from './error-payload.js'

export const openAIV1ProtocolDriver: GatewayProtocolDriver = {
  id: 'openai-v1',
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  responseProtocol: 'openai_v1',
  clientErrorProtocol: 'openai',
  defaultClientProfile: 'generic_openai',
  supportsProfile: isOpenAIProtocolProfile,
  endpointModeForRequestShape: openAIEndpointModeForRequestShape,
  isModelsRequest: isOpenAIModelsRequest,
  responseEndpointFamilyForRequest: openAIResponseEndpointFamilyFromRequest,
  extractJsonSemanticFrames: (value, req) =>
    extractOpenAIJsonSemanticFrames(value, openAIResponseEndpointFamilyFromRequest(req)),
  createStreamInspector: () => new OpenAIStreamInspector(),
  responseInspectionEndpointFamily: openAIEndpointFamilyOrUnknown,
  extractSseSemanticFrames: (event, endpointFamily) =>
    extractOpenAISseSemanticFrames(event, openAIEndpointFamilyOrUnknown(endpointFamily)),
  sseResponseInspectionFailureEvent: 'default',
  drainForKeepAliveAfterTerminal: true,
  parseUsageFromJsonBuffer: parseOpenAIUsageFromJsonBuffer,
  parseUsageFromJsonTextFragment: parseOpenAIUsageFromJsonTextFragment,
  parseErrorPayload: parseOpenAIErrorPayload,
  applyStreamUsageFallback: applyOpenAIStreamUsageFallback
}

function openAIEndpointFamilyOrUnknown(endpointFamily?: ResponseEndpointFamily): OpenAIResponseEndpointFamily {
  return endpointFamily === 'chat_completions' || endpointFamily === 'responses'
    ? endpointFamily
    : 'unknown'
}
