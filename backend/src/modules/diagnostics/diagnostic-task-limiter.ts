export const diagnosticTaskMaxInFlight = 8
export const diagnosticTaskRetryAfterSeconds = 1
export const diagnosticTaskBusyMessage = '诊断任务繁忙，请稍后重试'

let activeDiagnosticTaskCount = 0

export function tryAcquireDiagnosticTaskSlot(): (() => void) | undefined {
  if (activeDiagnosticTaskCount >= diagnosticTaskMaxInFlight) {
    return undefined
  }
  activeDiagnosticTaskCount += 1
  let released = false
  return () => {
    if (released) return
    released = true
    activeDiagnosticTaskCount = Math.max(0, activeDiagnosticTaskCount - 1)
  }
}

export function diagnosticTaskRuntime(): { active: number; maxInFlight: number } {
  return {
    active: activeDiagnosticTaskCount,
    maxInFlight: diagnosticTaskMaxInFlight
  }
}
