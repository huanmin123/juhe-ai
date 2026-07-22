import type { AccountApiKeyRuntimeDetail, AccountApiKeyRuntimeResponse } from '@/types/domain'

export interface SavedAccountApiKeyRuntimeSnapshot {
  keyIdentities: string[]
  items: AccountApiKeyRuntimeDetail[]
}

export function createSavedAccountApiKeyRuntimeSnapshot(input: {
  accountId: string
  configRevision: number
  apiKeys: string[]
  response: AccountApiKeyRuntimeResponse | undefined
}): SavedAccountApiKeyRuntimeSnapshot | undefined {
  if (!input.response) return undefined
  if (input.response.accountId !== input.accountId || input.response.configRevision !== input.configRevision) return undefined
  return {
    keyIdentities: accountApiKeyIdentities(input.apiKeys),
    items: input.response.items.map((item) => ({ ...item }))
  }
}

export function visibleSavedAccountApiKeyRuntimeDetails(
  snapshot: SavedAccountApiKeyRuntimeSnapshot | undefined,
  apiKeys: string[]
): AccountApiKeyRuntimeDetail[] | undefined {
  if (!snapshot) return undefined
  const currentIdentities = accountApiKeyIdentities(apiKeys)
  if (currentIdentities.length !== snapshot.keyIdentities.length) return undefined
  if (currentIdentities.some((identity, index) => identity !== snapshot.keyIdentities[index])) return undefined
  return snapshot.items
}

function accountApiKeyIdentities(apiKeys: string[]): string[] {
  return apiKeys.map((key) => key.trim())
}
