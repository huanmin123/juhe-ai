import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'

export function normalRouteFirstByteDeadlineAppliesToLane(lane: OpenAIGatewayRequestLane): boolean {
  return lane === 'text'
}

export function normalRouteSpeedFirstAppliesToLane(lane: OpenAIGatewayRequestLane): boolean {
  return normalRouteFirstByteDeadlineAppliesToLane(lane)
}
