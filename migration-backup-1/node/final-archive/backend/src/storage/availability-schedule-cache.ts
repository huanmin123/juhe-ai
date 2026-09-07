export function availabilityScheduleCacheTtlMs(now = Date.now()): number {
  const nextMinuteAt = Math.floor(now / 60_000) * 60_000 + 60_000
  return Math.max(1, nextMinuteAt - now)
}
