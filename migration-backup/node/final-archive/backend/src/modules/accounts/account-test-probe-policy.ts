import { isOpenAIProtocolProfile, type ProviderProtocolProfileDefinition } from '../../domain/provider-protocol.js'
import type { AccountSupportedEndpointMode } from '../../domain/types.js'

export type AccountTestProbeKind = 'generation' | 'image_generation' | 'models_catalog'

export function accountTestProbeKind(
  account: ProviderProtocolProfileDefinition & { type?: string },
  modelCapabilities: {
    testEndpointMode?: AccountSupportedEndpointMode
    supportedApiProtocols?: readonly string[]
  } = {}
): AccountTestProbeKind {
  const protocols = modelCapabilities.supportedApiProtocols ?? []
  const selectedProtocol = endpointModeProtocol(modelCapabilities.testEndpointMode)
  const selectedProtocolSupported = Boolean(selectedProtocol && protocols.includes(selectedProtocol))
  const imageOnly = protocols.includes('images')
    && !protocols.some((protocol) => protocol !== 'images')
  return isOpenAIProtocolProfile(account)
    && account.type === 'api_key'
    && (
      (modelCapabilities.testEndpointMode === 'images_json' && protocols.includes('images'))
      || (!selectedProtocolSupported && imageOnly)
    )
    ? 'image_generation'
    : 'generation'
}

function endpointModeProtocol(mode: AccountSupportedEndpointMode | undefined): string | undefined {
  if (mode === 'images_json') return 'images'
  if (mode === 'chat_json' || mode === 'chat_sse') return 'chat_completions'
  if (mode === 'responses_json' || mode === 'responses_sse') return 'responses'
  if (mode === 'messages_json' || mode === 'messages_sse') return 'messages'
  if (mode === 'generate_content_json') return 'generate_content'
  if (mode === 'generate_content_sse') return 'stream_generate_content'
  if (mode === 'interactions_json' || mode === 'interactions_sse') return 'interactions'
  return undefined
}
