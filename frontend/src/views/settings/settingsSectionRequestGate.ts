import type { ManagementSettingsSectionKey } from '@/api/domains/settings'

export interface SettingsSectionRequestToken {
  sectionKey: ManagementSettingsSectionKey
  generation: number
  signature: string
}

export function buildSettingsSectionRequestSignature(input: {
  sectionKey: ManagementSettingsSectionKey
  authRevision: number
  viewerId?: string
  viewerRole?: string
}): string {
  return JSON.stringify([
    input.sectionKey,
    input.authRevision,
    input.viewerId ?? 'anonymous',
    input.viewerRole ?? 'anonymous'
  ])
}

export function createSettingsSectionRequestGate() {
  const generations = new Map<ManagementSettingsSectionKey, number>()
  let active = true

  function begin(sectionKey: ManagementSettingsSectionKey, signature: string): SettingsSectionRequestToken {
    const generation = (generations.get(sectionKey) ?? 0) + 1
    generations.set(sectionKey, generation)
    return { sectionKey, generation, signature }
  }

  function isCurrent(token: SettingsSectionRequestToken, currentSignature: string): boolean {
    return active
      && token.signature === currentSignature
      && token.generation === generations.get(token.sectionKey)
  }

  function invalidate(sectionKey?: ManagementSettingsSectionKey): void {
    if (sectionKey) {
      generations.set(sectionKey, (generations.get(sectionKey) ?? 0) + 1)
      return
    }
    for (const key of generations.keys()) invalidate(key)
  }

  function deactivate(): void {
    active = false
    invalidate()
  }

  function activate(): void {
    active = true
  }

  return { activate, begin, deactivate, invalidate, isCurrent }
}
