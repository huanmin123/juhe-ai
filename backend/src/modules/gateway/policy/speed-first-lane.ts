import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'

export function normalRouteSpeedFirstAppliesToLane(lane: OpenAIGatewayRequestLane): boolean {
  return lane === 'text'
}
