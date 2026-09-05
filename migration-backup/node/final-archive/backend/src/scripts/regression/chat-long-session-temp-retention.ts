export function shouldRemoveChatLongSessionTemp(input: {
  keepTemp: boolean
  executionSucceeded: boolean
  acceptancePassed: boolean
  realProbe: boolean
  reportWritten: boolean
  primaryError: boolean
  cleanupHealthy: boolean
}): boolean {
  if (input.keepTemp || !input.executionSucceeded || input.primaryError || !input.cleanupHealthy) return false
  if (input.realProbe) return true
  return input.acceptancePassed && input.reportWritten
}
