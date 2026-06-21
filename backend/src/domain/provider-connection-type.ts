import {
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  isGlmProviderCode,
  normalizeProviderToken
} from './provider-protocol.js'

export const GLM_GENERAL_CONNECTION_TYPE = 'general_api_key'
export const GLM_CODING_CONNECTION_TYPE = 'coding_api_key'

export function providerProtocolProfileIdForConnectionType(input: {
  providerCode?: unknown
  connectionType?: unknown
}): string | undefined {
  const connectionType = normalizeProviderToken(input.connectionType)
  if (!connectionType) return undefined
  if (!isGlmProviderCode(input.providerCode)) return undefined
  if (connectionType === GLM_GENERAL_CONNECTION_TYPE) return GLM_GENERAL_OPENAI_V1_PROFILE_ID
  if (connectionType === GLM_CODING_CONNECTION_TYPE) return GLM_CODING_OPENAI_V1_PROFILE_ID
  throw new Error(`智谱 GLM 接入类型不支持：${connectionType}`)
}

export function connectionTypeForProviderProtocolProfile(input: {
  providerCode?: unknown
  providerProtocolProfileId?: unknown
}): string | undefined {
  if (!isGlmProviderCode(input.providerCode)) return undefined
  const profileId = typeof input.providerProtocolProfileId === 'string'
    ? input.providerProtocolProfileId.trim()
    : ''
  if (profileId === GLM_GENERAL_OPENAI_V1_PROFILE_ID) return GLM_GENERAL_CONNECTION_TYPE
  if (profileId === GLM_CODING_OPENAI_V1_PROFILE_ID) return GLM_CODING_CONNECTION_TYPE
  return undefined
}

export function resolveProviderProtocolProfileIdFromConnectionType(input: {
  providerCode?: unknown
  providerProtocolProfileId?: string
  connectionType?: unknown
}): string | undefined {
  const connectionProfileId = providerProtocolProfileIdForConnectionType(input)
  const requestedProfileId = input.providerProtocolProfileId?.trim()
  if (requestedProfileId && connectionProfileId && requestedProfileId !== connectionProfileId) {
    throw new Error('connectionType 与 providerProtocolProfileId 不一致')
  }
  return connectionProfileId ?? requestedProfileId
}

export function isProviderConnectionTypeRequired(input: {
  providerCode?: unknown
  providerProtocolProfileId?: unknown
  connectionType?: unknown
}): boolean {
  if (!isGlmProviderCode(input.providerCode)) return false
  const connectionType = normalizeProviderToken(input.connectionType)
  if (connectionType) return false
  const profileId = typeof input.providerProtocolProfileId === 'string'
    ? input.providerProtocolProfileId.trim()
    : ''
  return profileId !== GLM_GENERAL_OPENAI_V1_PROFILE_ID
    && profileId !== GLM_CODING_OPENAI_V1_PROFILE_ID
}
