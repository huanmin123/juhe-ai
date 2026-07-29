import type {
  ExternalIntegrationSourceListItem,
  ExternalIntegrationSourceMutationResult,
  ExternalIntegrationSourcePatchPayload,
  ExternalIntegrationSourceTokenMutationResult,
  ExternalIntegrationSourceTokenPatchPayload,
  ExternalIntegrationSourceTokenSummary
} from '@/types/domain'

export function mergeExternalSourceMutation(
  item: ExternalIntegrationSourceListItem,
  payload: ExternalIntegrationSourcePatchPayload,
  result: ExternalIntegrationSourceMutationResult
): ExternalIntegrationSourceListItem {
  const next = { ...item, id: result.id, updatedAt: result.updatedAt }
  if (payload.name !== undefined) next.name = payload.name
  if (payload.status !== undefined) next.status = payload.status
  if (payload.scopes !== undefined) next.scopes = [...payload.scopes]
  if (payload.rateLimits !== undefined) next.rateLimits = payload.rateLimits.map((rule) => ({ ...rule }))
  assignOptionalText(next, 'expiresAt', payload.expiresAt)
  assignOptionalText(next, 'notes', payload.notes)
  return next
}

export function mergeExternalSourceTokenMutation(
  item: ExternalIntegrationSourceTokenSummary,
  payload: ExternalIntegrationSourceTokenPatchPayload,
  result: ExternalIntegrationSourceTokenMutationResult
): ExternalIntegrationSourceTokenSummary {
  const next = { ...item, id: result.id, updatedAt: result.updatedAt }
  if (payload.name !== undefined) next.name = payload.name
  if (payload.status !== undefined) next.status = payload.status
  if (payload.scopes !== undefined) next.scopes = [...payload.scopes]
  assignOptionalText(next, 'expiresAt', payload.expiresAt)
  return next
}

function assignOptionalText<T extends object, K extends keyof T>(
  target: T,
  key: K,
  value: string | null | undefined
): void {
  if (value === undefined) return
  if (value === null) {
    delete target[key]
    return
  }
  target[key] = value as T[K]
}
