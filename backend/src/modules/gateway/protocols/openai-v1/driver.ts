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
  openAIResponseEndpointFamilyFromRequest
} from './response-semantics.js'
import { applyOpenAIStreamUsageFallback } from './stream-inspection.js'
import {
  parseOpenAIUsageFromJsonBuffer,
  parseOpenAIUsageFromJsonTextFragment
} from './usage.js'

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
  parseUsageFromJsonBuffer: parseOpenAIUsageFromJsonBuffer,
  parseUsageFromJsonTextFragment: parseOpenAIUsageFromJsonTextFragment,
  applyStreamUsageFallback: applyOpenAIStreamUsageFallback
}
