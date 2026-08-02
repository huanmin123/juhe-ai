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
  const snapshotIndexesByIdentity = new Map<string, number[]>()
  snapshot.keyIdentities.forEach((identity, index) => {
    const indexes = snapshotIndexesByIdentity.get(identity) ?? []
    indexes.push(index)
    snapshotIndexesByIdentity.set(identity, indexes)
  })
  const snapshotItemsByIndex = new Map(snapshot.items.map((item) => [item.keyIndex, item]))
  const remappedItems: AccountApiKeyRuntimeDetail[] = []
  for (const [currentIndex, identity] of currentIdentities.entries()) {
    const indexes = snapshotIndexesByIdentity.get(identity)
    const snapshotIndex = indexes?.shift()
    if (snapshotIndex === undefined) return undefined
    const item = snapshotItemsByIndex.get(snapshotIndex)
    if (item) remappedItems.push({ ...item, keyIndex: currentIndex })
  }
  return remappedItems
}

function accountApiKeyIdentities(apiKeys: string[]): string[] {
  return apiKeys.map((key) => key.trim())
}
