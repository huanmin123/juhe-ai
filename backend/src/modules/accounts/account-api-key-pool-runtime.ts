import type { AccountTestApiKeyPoolItemResult } from '../../domain/types.js'
import { accountApiKeyEntries, isAccountApiKeyPoolIsolationEnabled, type AccountApiKeyEntry } from '../../storage/account-api-key-rotation.js'
import type { OpenAIAccountSecret } from '../../storage/openai-account-selector.types.js'

export function accountApiKeyPoolCredentials(candidate: OpenAIAccountSecret): Record<string, unknown> {
  return {
    ...candidate.credentials,
    api_key: candidate.apiKey,
    ...(candidate.apiKeys?.length ? { api_keys: candidate.apiKeys } : {})
  }
}

export function accountApiKeyPoolEntriesForCandidate(candidate: OpenAIAccountSecret): AccountApiKeyEntry[] {
  return accountApiKeyEntries(accountApiKeyPoolCredentials(candidate))
}

export function isCandidateAccountApiKeyPoolTestable(
  candidate: OpenAIAccountSecret,
  entries = accountApiKeyPoolEntriesForCandidate(candidate)
): boolean {
  return isAccountApiKeyPoolIsolationEnabled({
    providerCode: candidate.providerCode,
    protocolCode: candidate.protocolCode,
    protocolVersion: candidate.protocolVersion,
    type: candidate.type,
    credentials: accountApiKeyPoolCredentials(candidate)
  }) && entries.length >= 2
}

export function fixedAccountApiKeyPoolCandidate(
  candidate: OpenAIAccountSecret,
  entry: AccountApiKeyEntry,
  options: { apiKeyRuntimeStateDisabled?: boolean } = {}
): OpenAIAccountSecret {
  const apiKeys = accountApiKeyPoolEntriesForCandidate(candidate).map((item) => item.key)
  return {
    ...candidate,
    apiKey: entry.key,
    apiKeys,
    selectedApiKeyFingerprint: entry.fingerprint,
    selectedApiKeyIndex: entry.index,
    apiKeyRuntimeStateDisabled: options.apiKeyRuntimeStateDisabled,
    credentials: {
      ...candidate.credentials,
      api_key: entry.key,
      api_keys: apiKeys
    }
  }
}

export function accountApiKeyPoolEntryForResult(
  entries: AccountApiKeyEntry[],
  result: AccountTestApiKeyPoolItemResult
): AccountApiKeyEntry | undefined {
  return entries.find((entry) => entry.index === result.keyIndex)
}
