export interface SsePendingEventState {
  skipped?: boolean
  oversizedEvent?: boolean
  eventName?: string
  dataLineCount?: number
  dataBytes?: number
  pendingLine?: string
}

export function hasPendingSseProtocolEvent(state: SsePendingEventState): boolean {
  if (state.skipped) {
    return false
  }
  if (
    state.oversizedEvent
    || hasText(state.eventName)
    || positiveCount(state.dataLineCount)
    || positiveCount(state.dataBytes)
  ) {
    return true
  }
  const pendingLine = stripTrailingCarriageReturn(state.pendingLine ?? '')
  return pendingLine.startsWith('event:') || pendingLine.startsWith('data:')
}

function hasText(value: string | undefined): boolean {
  return value !== undefined && value.length > 0
}

function positiveCount(value: number | undefined): boolean {
  return value !== undefined && value > 0
}

function stripTrailingCarriageReturn(value: string): string {
  return value.endsWith('\r') ? value.slice(0, -1) : value
}
