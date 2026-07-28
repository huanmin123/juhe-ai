import type { ParsedOpenAIStreamEvent } from '../protocols/openai-v1/stream-events.js'
import type { GatewayStreamInspection } from '../protocols/_shared/types.js'

export const gatewayStreamParsedEventReceiver = Symbol('gatewayStreamParsedEventReceiver')
export const gatewayStreamInspectionReceiver = Symbol('gatewayStreamInspectionReceiver')

export interface GatewayStreamObservationReceiver {
  [gatewayStreamParsedEventReceiver]?: (event: ParsedOpenAIStreamEvent) => void
  [gatewayStreamInspectionReceiver]?: (inspection: GatewayStreamInspection) => void
}

export function hasGatewayStreamParsedEventReceiver(
  response: object
): boolean {
  return typeof (response as GatewayStreamObservationReceiver)[gatewayStreamParsedEventReceiver] === 'function'
}

export function publishGatewayStreamParsedEvent(
  response: object,
  event: ParsedOpenAIStreamEvent
): void {
  const receiver = response as GatewayStreamObservationReceiver
  receiver[gatewayStreamParsedEventReceiver]?.(event)
}

export function publishGatewayStreamInspection(
  response: object,
  inspection: GatewayStreamInspection
): GatewayStreamInspection {
  const receiver = response as GatewayStreamObservationReceiver
  receiver[gatewayStreamInspectionReceiver]?.(inspection)
  return inspection
}
