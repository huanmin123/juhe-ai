export function quotaLimits(hourly: number, daily: number, monthly: number): Record<string, unknown> {
  return {
    hourly: { enabled: true, hours: 1, limit: hourly },
    daily: { enabled: true, limit: daily },
    monthly: { enabled: true, limit: monthly }
  }
}
