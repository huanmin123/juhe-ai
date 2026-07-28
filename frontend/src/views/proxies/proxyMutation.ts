import type { ProxyProfileMutationResult, ProxyProfileSummary } from '@/types/domain'

export interface ProxyFormState {
  name: string
  description: string
  type: string
  host: string
  port: number
  username: string
  password: string
  enabled: boolean
}

export function buildProxyCreatePayload(form: ProxyFormState): Record<string, unknown> {
  const payload: Record<string, unknown> = {
    name: form.name.trim(),
    description: form.description.trim() || null,
    type: form.type,
    host: form.host.trim(),
    port: form.port,
    username: form.username.trim(),
    enabled: form.enabled
  }
  if (form.password.trim()) payload.password = form.password
  return payload
}

export function buildProxyPatchPayload(baseline: ProxyFormState, current: ProxyFormState): Record<string, unknown> {
  const payload: Record<string, unknown> = {}
  if (baseline.name.trim() !== current.name.trim()) payload.name = current.name.trim()
  if (normalizedNullableText(baseline.description) !== normalizedNullableText(current.description)) {
    payload.description = normalizedNullableText(current.description)
  }
  if (baseline.type !== current.type) payload.type = current.type
  if (baseline.host.trim() !== current.host.trim()) payload.host = current.host.trim()
  if (baseline.port !== current.port) payload.port = current.port
  if (normalizedNullableText(baseline.username) !== normalizedNullableText(current.username)) {
    payload.username = current.username.trim()
  }
  if (baseline.enabled !== current.enabled) payload.enabled = current.enabled
  if (current.password.trim()) payload.password = current.password
  return payload
}

export function hasProxyPatchChanges(baseline: ProxyFormState, current: ProxyFormState): boolean {
  return Object.keys(buildProxyPatchPayload(baseline, current)).length > 0
}

export function applyProxyMutation(item: ProxyProfileSummary, mutation: ProxyProfileMutationResult): ProxyProfileSummary {
  const values = mutation.values
  return {
    ...item,
    ...(values.name !== undefined ? { name: values.name } : {}),
    ...(values.description !== undefined ? { description: values.description ?? undefined } : {}),
    ...(values.type !== undefined ? { type: values.type } : {}),
    ...(values.host !== undefined ? { host: values.host } : {}),
    ...(values.port !== undefined ? { port: values.port } : {}),
    ...(values.username !== undefined ? { username: values.username ?? undefined } : {}),
    ...(values.enabled !== undefined ? { enabled: values.enabled } : {}),
    ...(values.testStatus !== undefined ? { testStatus: values.testStatus } : {}),
    ...(values.latencyMs !== undefined ? { latencyMs: values.latencyMs ?? undefined } : {}),
    ...(values.outboundIp !== undefined ? { outboundIp: values.outboundIp ?? undefined } : {}),
    ...(values.outboundRegion !== undefined ? { outboundRegion: values.outboundRegion ?? undefined } : {}),
    ...(values.lastTestMessage !== undefined ? { lastTestMessage: values.lastTestMessage ?? undefined } : {}),
    ...(values.lastTestedAt !== undefined ? { lastTestedAt: values.lastTestedAt ?? undefined } : {}),
    updatedAt: mutation.updatedAt
  }
}

function normalizedNullableText(value: string): string | null {
  const text = value.trim()
  return text || null
}
