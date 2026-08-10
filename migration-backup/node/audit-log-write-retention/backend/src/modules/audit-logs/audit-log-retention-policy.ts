export function auditSuccessRetentionCutoffIso(
  nowMs: number,
  successHotRetentionHours: number,
  successRetentionDays: number
): string {
  const retentionMs = successRetentionDays > 0
    ? successRetentionDays * 24 * 60 * 60 * 1000
    : successHotRetentionHours * 60 * 60 * 1000
  return new Date(nowMs - retentionMs).toISOString()
}
