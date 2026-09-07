import type { GatewayApiKeyRow } from '../../../storage/repositories.js'
import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'

export const imageGenerationDisabledCode = 'image_generation_disabled'
export const imageGenerationDisabledMessage = '当前用户图像生成被禁用了，请联系管理员开启'

export function isImageGenerationDisabledForApiKey(
  apiKey: Pick<GatewayApiKeyRow, 'system_account_image_generation_enabled'> | undefined,
  requestLane: OpenAIGatewayRequestLane
): boolean {
  return requestLane === 'image' && apiKey !== undefined && apiKey.system_account_image_generation_enabled !== 1
}
