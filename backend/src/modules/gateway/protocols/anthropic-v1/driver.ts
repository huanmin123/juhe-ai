import { anthropicEndpointModeForRequestShape } from '../../../../domain/anthropic-endpoint-modes.js'
import {
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  isAnthropicProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { GatewayProtocolDriver } from '../_shared/types.js'
import {
  isAnthropicModelsRequest,
  isAnthropicNativeRequest
} from './route-helpers.js'
import {
  anthropicResponseEndpointFamilyFromPath,
  extractAnthropicJsonSemanticFrames
} from './response-semantics.js'
import { applyAnthropicStreamUsageFallback } from './stream-inspection.js'
import {
  parseAnthropicUsageFromJsonBuffer,
  parseAnthropicUsageFromJsonTextFragment
} from './usage.js'

export const anthropicV1ProtocolDriver: GatewayProtocolDriver = {
  id: 'anthropic-v1',
  protocolCode: ANTHROPIC_PROTOCOL_CODE,
  protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
  responseProtocol: 'anthropic_v1',
  clientErrorProtocol: 'anthropic',
  defaultClientProfile: 'generic_anthropic',
  supportsProfile: isAnthropicProtocolProfile,
  endpointModeForRequestShape: anthropicEndpointModeForRequestShape,
  isNativeRequest: isAnthropicNativeRequest,
  isModelsRequest: isAnthropicModelsRequest,
  isEndpointCapabilityFailure: (req, statusCode) =>
    (statusCode === 404 || statusCode === 405)
    && anthropicResponseEndpointFamilyFromPath(req.originalUrl || req.path) === 'message_token_counting',
  responseEndpointFamilyForRequest: (req) =>
    anthropicResponseEndpointFamilyFromPath(req.originalUrl || req.path),
  extractJsonSemanticFrames: (value, req) =>
    extractAnthropicJsonSemanticFrames(value, anthropicResponseEndpointFamilyFromPath(req.originalUrl || req.path)),
  parseUsageFromJsonBuffer: parseAnthropicUsageFromJsonBuffer,
  parseUsageFromJsonTextFragment: parseAnthropicUsageFromJsonTextFragment,
  applyStreamUsageFallback: applyAnthropicStreamUsageFallback
}
