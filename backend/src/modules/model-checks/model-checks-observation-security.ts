import { createHmac } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'

const hmacVersion = 'hmac-sha256-v1'

export function modelCheckObservationHmac(value: string, purpose: 'upstream' | 'cohort' | 'population' | 'probe' | 'fingerprint'): string {
  return `${hmacVersion}:${createHmac('sha256', runtimeConfig.secret).update(`${purpose}\u0000${value}`).digest('hex')}`
}

export function modelCheckPopulationKey(input: {
  providerCode: string
  providerProtocolProfileId: string
  endpointFamily: string
  credentialMode: string
  probeSetVersion: string
  featureVersion: string
}): string {
  return [
    input.providerCode,
    input.providerProtocolProfileId,
    input.endpointFamily,
    input.credentialMode,
    input.probeSetVersion,
    input.featureVersion
  ].join('\u0000')
}

export function normalizedUpstreamOrigin(baseUrl: string): string {
  try {
    const url = new URL(baseUrl)
    const port = url.port || (url.protocol === 'https:' ? '443' : url.protocol === 'http:' ? '80' : '')
    return `${url.protocol.toLowerCase()}//${url.hostname.toLowerCase()}:${port}`
  } catch {
    return 'invalid-upstream-origin'
  }
}

export function modelCheckCohortKey(input: {
  providerCode: string
  providerProtocolProfileId: string
  endpointFamily: string
  credentialMode: string
  mappedUpstreamModel: string
  probeSetVersion: string
  tokenizerVersion: string
}): string {
  return [
    input.providerCode,
    input.providerProtocolProfileId,
    input.endpointFamily,
    input.credentialMode,
    input.mappedUpstreamModel,
    input.probeSetVersion,
    input.tokenizerVersion
  ].join('\u0000')
}
