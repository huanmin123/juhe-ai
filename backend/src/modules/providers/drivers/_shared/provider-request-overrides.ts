import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import { GatewayRequestValidationError } from '../../../gateway/request/validation-error.js'
import { serializeGatewayJsonObject } from '../../../gateway/request/serialized-json-body.js'
import { resolveGptRequestOverrideModelCapabilities } from '../gpt/request-override-capabilities.js'
import { parseAccountRequestOverrideBody } from '../gpt/request-override-body.js'
import {
  effectiveGptAccountRequestOverrides,
  GptAccountRequestOverrideError,
  readGptAccountRequestOverrides,
  type GptRequestOverrideModelCapabilities
} from '../gpt/request-overrides.js'

type ProviderOverrideWireFormat = 'openai_chat' | 'openai_responses' | 'anthropic_messages' | 'gemini_generate_content'

export async function applyProviderAccountRequestOverridesToBody(
  body: Buffer | string | undefined,
  input: {
    account: DispatchAccountSecret
    upstreamModel?: string
    wireFormat: ProviderOverrideWireFormat
    modelCapabilities?: GptRequestOverrideModelCapabilities
    signal?: AbortSignal
  }
): Promise<Buffer | string | undefined> {
  let overrides
  try {
    overrides = readGptAccountRequestOverrides(input.account.credentials)
  } catch (error) {
    throw normalizeOverrideError(error)
  }
  if (!overrides.serviceTier && !overrides.reasoningEffort) return body
  let parsed: Record<string, unknown> | undefined
  let model = input.upstreamModel
  if (!model && !input.modelCapabilities) {
    parsed = await parseAccountRequestOverrideBody(body, input.signal)
    model = typeof parsed.model === 'string' ? parsed.model : undefined
  }
  let effective
  try {
    const capabilities = input.modelCapabilities
      ?? await resolveGptRequestOverrideModelCapabilities(input.account, model)
    effective = effectiveGptAccountRequestOverrides(overrides, capabilities)
  } catch (error) {
    throw normalizeOverrideError(error)
  }
  if (!effective.serviceTier && !effective.reasoningEffort) return body
  parsed ??= await parseAccountRequestOverrideBody(body, input.signal)

  const output = { ...parsed }
  if (input.wireFormat === 'openai_chat' || input.wireFormat === 'openai_responses') {
    if (effective.serviceTier === 'default') delete output.service_tier
    else if (effective.serviceTier) output.service_tier = effective.serviceTier
    if (effective.reasoningEffort) {
      if (input.wireFormat === 'openai_responses') {
        const reasoning = isPlainObject(output.reasoning) ? output.reasoning : {}
        output.reasoning = { ...reasoning, effort: effective.reasoningEffort }
        delete output.reasoning_effort
      } else {
        output.reasoning_effort = effective.reasoningEffort
        delete output.reasoning
      }
    }
  } else if (input.wireFormat === 'anthropic_messages') {
    if (effective.serviceTier === 'default') delete output.service_tier
    else if (effective.serviceTier) output.service_tier = effective.serviceTier
    if (effective.reasoningEffort) {
      const outputConfig = isPlainObject(output.output_config) ? output.output_config : {}
      output.output_config = { ...outputConfig, effort: effective.reasoningEffort }
    }
  } else {
    if (effective.serviceTier === 'default') delete output.service_tier
    else if (effective.serviceTier) output.service_tier = effective.serviceTier
    if (effective.reasoningEffort) {
      const generationConfig = isPlainObject(output.generationConfig) ? output.generationConfig : {}
      const thinkingConfig = isPlainObject(generationConfig.thinkingConfig) ? generationConfig.thinkingConfig : {}
      output.generationConfig = {
        ...generationConfig,
        thinkingConfig: { ...thinkingConfig, thinkingLevel: effective.reasoningEffort }
      }
    }
  }
  return typeof body === 'string'
    ? JSON.stringify(output)
    : serializeGatewayJsonObject(output)
}

function normalizeOverrideError(error: unknown): unknown {
  return error instanceof GptAccountRequestOverrideError
    ? new GatewayRequestValidationError(error.message, error.code, { accountScoped: true })
    : error
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
