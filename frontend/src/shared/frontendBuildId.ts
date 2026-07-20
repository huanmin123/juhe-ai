const frontendBuildIdPattern = /^[0-9a-f]{40}$/

export function normalizeFrontendBuildId(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const normalized = value.trim().toLowerCase()
  return frontendBuildIdPattern.test(normalized) ? normalized : undefined
}
