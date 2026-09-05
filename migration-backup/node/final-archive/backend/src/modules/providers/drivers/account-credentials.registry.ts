import { normalizeProviderToken } from '../../../domain/provider-protocol.js'
import { anthropicAccountCredentialDriver } from './anthropic/account-credentials.js'
import { deepSeekAccountCredentialDriver } from './deepseek/account-credentials.js'
import type {
  ProviderAccountCredentialContext,
  ProviderAccountCredentialDriver
} from './_shared/account-credentials.js'
import { geminiAccountCredentialDriver } from './gemini/account-credentials.js'
import { glmAccountCredentialDriver } from './glm/account-credentials.js'
import { gptAccountCredentialDriver } from './gpt/account-credentials.js'
import { hybridAccountCredentialDriver } from './hybrid/account-credentials.js'
import { openAICompatibleAccountCredentialDriver } from './openai-compatible/account-credentials.js'
import { xaiAccountCredentialDriver } from './xai/account-credentials.js'

const providerAccountCredentialDrivers: readonly ProviderAccountCredentialDriver[] = [
  openAICompatibleAccountCredentialDriver,
  gptAccountCredentialDriver,
  xaiAccountCredentialDriver,
  deepSeekAccountCredentialDriver,
  anthropicAccountCredentialDriver,
  geminiAccountCredentialDriver,
  glmAccountCredentialDriver,
  hybridAccountCredentialDriver
] as const

export function listProviderAccountCredentialDrivers(): readonly ProviderAccountCredentialDriver[] {
  return providerAccountCredentialDrivers
}

export function providerAccountCredentialDriverForContext(
  context: ProviderAccountCredentialContext
): ProviderAccountCredentialDriver | undefined {
  const normalizedProviderCode = normalizeProviderToken(context.providerCode)
  const normalizedContext = {
    ...context,
    providerCode: normalizedProviderCode
  }
  if (!normalizedProviderCode && !context.protocolCode) {
    return openAICompatibleAccountCredentialDriver
  }
  return providerAccountCredentialDrivers.find((driver) => driver.supportsContext(normalizedContext))
}
